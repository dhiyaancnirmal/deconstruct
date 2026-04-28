package browsercapture

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/dhiyaan/deconstruct/daemon/internal/blobstore"
	"github.com/dhiyaan/deconstruct/daemon/internal/bodyparse"
	"github.com/dhiyaan/deconstruct/daemon/internal/id"
	"github.com/dhiyaan/deconstruct/daemon/internal/redact"
	"github.com/dhiyaan/deconstruct/daemon/internal/store"
)

const defaultEndpoint = "http://127.0.0.1:9222"

type Manager struct {
	store *store.Store
	blobs *blobstore.Store
	mu    sync.Mutex
	run   *runState
}

type StartParams struct {
	Endpoint    string `json:"endpoint,omitempty"`
	SessionName string `json:"session_name,omitempty"`
	Redact      *bool  `json:"redact,omitempty"`
}

type Status struct {
	Running   bool   `json:"running"`
	Endpoint  string `json:"endpoint,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Flows     int    `json:"flows"`
	Error     string `json:"error,omitempty"`
}

type runState struct {
	cancel    context.CancelFunc
	endpoint  string
	sessionID string
	redact    bool
	flows     int
	err       error
	done      chan struct{}
}

type pendingFlow struct {
	RequestID      string
	Method         string
	URL            string
	Headers        http.Header
	PostData       []byte
	Status         int
	ResponseHeader http.Header
	ResponseBody   []byte
	StartedAt      time.Time
	FinishedAt     time.Time
}

func NewManager(store *store.Store, blobs *blobstore.Store) *Manager {
	return &Manager{store: store, blobs: blobs}
}

func (m *Manager) Start(ctx context.Context, params StartParams) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.run != nil {
		return m.statusLocked(), fmt.Errorf("browser capture already running")
	}
	endpoint := params.Endpoint
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	sessionID, err := id.New("session")
	if err != nil {
		return Status{}, err
	}
	name := params.SessionName
	if name == "" {
		name = "Browser fidelity capture"
	}
	if err := m.store.EnsureSession(ctx, store.Session{ID: sessionID, Name: name, Kind: "browser_cdp", CreatedAt: time.Now().UTC()}); err != nil {
		return Status{}, err
	}
	redactSecrets := true
	if params.Redact != nil {
		redactSecrets = *params.Redact
	}
	runCtx, cancel := context.WithCancel(context.Background())
	state := &runState{cancel: cancel, endpoint: endpoint, sessionID: sessionID, redact: redactSecrets, done: make(chan struct{})}
	m.run = state
	go m.capture(runCtx, state)
	return m.statusLocked(), nil
}

func (m *Manager) Stop() Status {
	m.mu.Lock()
	state := m.run
	if state != nil {
		state.cancel()
	}
	m.mu.Unlock()
	if state != nil {
		<-state.done
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	status := m.statusLocked()
	m.run = nil
	return status
}

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statusLocked()
}

func (m *Manager) statusLocked() Status {
	if m.run == nil {
		return Status{Running: false}
	}
	status := Status{Running: true, Endpoint: m.run.endpoint, SessionID: m.run.sessionID, Flows: m.run.flows}
	if m.run.err != nil {
		status.Error = m.run.err.Error()
	}
	return status
}

func (m *Manager) capture(ctx context.Context, state *runState) {
	defer close(state.done)
	client, err := dialCDP(ctx, state.endpoint)
	if err != nil {
		m.setError(state, err)
		return
	}
	defer client.Close()
	if _, err := client.Call(ctx, "Network.enable", map[string]any{}); err != nil {
		m.setError(state, err)
		return
	}
	pending := map[string]*pendingFlow{}
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-client.Events():
			if !ok {
				if err := client.Err(); err != nil {
					m.setError(state, err)
				}
				return
			}
			m.handleEvent(ctx, client, state, pending, event)
		}
	}
}

func (m *Manager) handleEvent(ctx context.Context, client *cdpClient, state *runState, pending map[string]*pendingFlow, event map[string]any) {
	method, _ := event["method"].(string)
	params, _ := event["params"].(map[string]any)
	requestID, _ := params["requestId"].(string)
	if requestID == "" {
		return
	}
	switch method {
	case "Network.requestWillBeSent":
		request, _ := params["request"].(map[string]any)
		headers := headersFromAny(request["headers"])
		postData, _ := request["postData"].(string)
		pending[requestID] = &pendingFlow{
			RequestID: requestID,
			Method:    stringValue(request["method"]),
			URL:       stringValue(request["url"]),
			Headers:   headers,
			PostData:  []byte(postData),
			StartedAt: time.Now().UTC(),
		}
	case "Network.responseReceived":
		flow := pending[requestID]
		if flow == nil {
			return
		}
		response, _ := params["response"].(map[string]any)
		flow.Status = int(floatValue(response["status"]))
		flow.ResponseHeader = headersFromAny(response["headers"])
	case "Network.loadingFinished":
		flow := pending[requestID]
		if flow == nil {
			return
		}
		flow.FinishedAt = time.Now().UTC()
		if body, err := m.responseBody(ctx, client, requestID); err == nil {
			flow.ResponseBody = body
		}
		if err := m.persist(ctx, state, flow); err != nil {
			m.setError(state, err)
		} else {
			m.mu.Lock()
			state.flows++
			m.mu.Unlock()
		}
		delete(pending, requestID)
	case "Network.loadingFailed":
		flow := pending[requestID]
		if flow == nil {
			return
		}
		flow.FinishedAt = time.Now().UTC()
		flow.Status = 0
		if err := m.persist(ctx, state, flow); err != nil {
			m.setError(state, err)
		}
		delete(pending, requestID)
	}
}

func (m *Manager) responseBody(ctx context.Context, client *cdpClient, requestID string) ([]byte, error) {
	response, err := client.Call(ctx, "Network.getResponseBody", map[string]any{"requestId": requestID})
	if err != nil {
		return nil, err
	}
	result, _ := response["result"].(map[string]any)
	body, _ := result["body"].(string)
	if base64Encoded, _ := result["base64Encoded"].(bool); base64Encoded {
		decoded, err := base64.StdEncoding.DecodeString(body)
		if err != nil {
			return nil, err
		}
		return decoded, nil
	}
	return []byte(body), nil
}

func (m *Manager) persist(ctx context.Context, state *runState, pending *pendingFlow) error {
	if pending.Method == "" || pending.URL == "" {
		return nil
	}
	requestHeaders := pending.Headers
	responseHeaders := pending.ResponseHeader
	requestBody := pending.PostData
	responseBody := pending.ResponseBody
	if state.redact {
		requestHeaders = redact.Header(requestHeaders)
		responseHeaders = redact.Header(responseHeaders)
		requestBody = redact.Body(requestBody)
		responseBody = redact.Body(responseBody)
	}
	requestBlob, err := m.blobs.Put(requestBody)
	if err != nil {
		return err
	}
	responseBlob, err := m.blobs.Put(responseBody)
	if err != nil {
		return err
	}
	requestHeaderBytes, _ := json.Marshal(requestHeaders)
	responseHeaderBytes, _ := json.Marshal(responseHeaders)
	flowID, err := id.New("flow")
	if err != nil {
		return err
	}
	parsedURL, _ := url.Parse(pending.URL)
	host := ""
	if parsedURL != nil {
		host = parsedURL.Host
	}
	duration := pending.FinishedAt.Sub(pending.StartedAt)
	if duration < 0 {
		duration = 0
	}
	flow := store.Flow{
		ID:              flowID,
		SessionID:       state.sessionID,
		Method:          pending.Method,
		AppName:         "Browser",
		Source:          "browser_cdp",
		URL:             pending.URL,
		Host:            host,
		Status:          pending.Status,
		Duration:        duration,
		RequestHeaders:  requestHeaderBytes,
		ResponseHeaders: responseHeaderBytes,
		RequestBlob:     requestBlob,
		ResponseBlob:    responseBlob,
		CreatedAt:       pending.StartedAt,
	}
	if err := m.store.InsertFlow(ctx, flow); err != nil {
		return err
	}
	_ = m.store.TagFlow(ctx, flowID, "browser_fidelity")
	for _, tag := range bodyparse.Tags(requestHeaders, requestBody) {
		_ = m.store.TagFlow(ctx, flowID, tag)
	}
	for _, tag := range bodyparse.Tags(responseHeaders, responseBody) {
		_ = m.store.TagFlow(ctx, flowID, tag)
	}
	return nil
}

func (m *Manager) setError(state *runState, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state.err = err
}

func headersFromAny(value any) http.Header {
	headers := http.Header{}
	object, _ := value.(map[string]any)
	for key, raw := range object {
		headers.Add(key, fmt.Sprint(raw))
	}
	return headers
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func floatValue(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case int:
		return float64(typed)
	default:
		return 0
	}
}

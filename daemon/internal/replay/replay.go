package replay

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"github.com/dhiyaan/deconstruct/daemon/internal/blobstore"
	"github.com/dhiyaan/deconstruct/daemon/internal/bodyparse"
	"github.com/dhiyaan/deconstruct/daemon/internal/id"
	"github.com/dhiyaan/deconstruct/daemon/internal/redact"
	"github.com/dhiyaan/deconstruct/daemon/internal/store"
)

const replaySessionID = "session_replay"

type Engine struct {
	store    *store.Store
	blobs    *blobstore.Store
	adapters map[string]Adapter
}

type Params struct {
	FlowID         string            `json:"flow_id"`
	Overrides      Overrides         `json:"overrides,omitempty"`
	DryRun         bool              `json:"dry_run,omitempty"`
	Retries        int               `json:"retries,omitempty"`
	Profile        string            `json:"profile,omitempty"`
	ProfileOptions map[string]string `json:"profile_options,omitempty"`
}

type Overrides struct {
	Method        string              `json:"method,omitempty"`
	URL           string              `json:"url,omitempty"`
	Headers       map[string][]string `json:"headers,omitempty"`
	RemoveHeaders []string            `json:"remove_headers,omitempty"`
	Query         map[string]string   `json:"query,omitempty"`
	Body          *string             `json:"body,omitempty"`
}

type Result struct {
	Run             store.ReplayRun `json:"run"`
	PreparedRequest PreparedRequest `json:"prepared_request"`
	Profile         string          `json:"profile"`
}

type Variation struct {
	Name           string            `json:"name"`
	Overrides      Overrides         `json:"overrides,omitempty"`
	Profile        string            `json:"profile,omitempty"`
	ProfileOptions map[string]string `json:"profile_options,omitempty"`
}

type VariationParams struct {
	FlowID     string      `json:"flow_id"`
	Variations []Variation `json:"variations"`
	Retries    int         `json:"retries,omitempty"`
}

type VariationResult struct {
	Name   string `json:"name"`
	Result Result `json:"result"`
}

type PreparedRequest struct {
	Method  string      `json:"method"`
	URL     string      `json:"url"`
	Headers http.Header `json:"headers,omitempty"`
	Body    string      `json:"body,omitempty"`
}

type Adapter interface {
	Name() string
	Execute(context.Context, PreparedRequest, http.Header, []byte, map[string]string) (AdapterResult, error)
}

type AdapterResult struct {
	Status   int
	Headers  http.Header
	Body     []byte
	Duration time.Duration
}

func New(store *store.Store, blobs *blobstore.Store) (*Engine, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	engine := &Engine{
		store: store,
		blobs: blobs,
	}
	engine.adapters = map[string]Adapter{
		"standard":         NewStandardAdapter(jar),
		"":                 NewStandardAdapter(jar),
		"utls":             NewUTLSAdapter(),
		"curl_impersonate": NewCurlImpersonateAdapter(""),
		"browser":          NewBrowserAdapter(),
		"passthrough":      PassthroughAdapter{},
	}
	return engine, nil
}

func (e *Engine) Run(ctx context.Context, params Params) (Result, error) {
	if params.FlowID == "" {
		return Result{}, fmt.Errorf("flow_id is required")
	}
	flow, err := e.store.GetFlow(ctx, params.FlowID)
	if err != nil {
		return Result{}, err
	}
	prepared, rawHeaders, rawBody, err := e.prepare(flow, params.Overrides)
	if err != nil {
		return Result{}, err
	}
	runID, err := id.New("replay")
	if err != nil {
		return Result{}, err
	}
	if params.DryRun {
		run := store.ReplayRun{ID: runID, OriginalFlowID: flow.ID, OK: true, Attempts: 0, CreatedAt: time.Now().UTC()}
		if err := e.store.InsertReplayRun(ctx, run); err != nil {
			return Result{}, err
		}
		return Result{Run: run, PreparedRequest: prepared, Profile: replayProfile(params.Profile)}, nil
	}
	adapter, err := e.adapter(params.Profile)
	if err != nil {
		return Result{}, err
	}
	attempts := params.Retries + 1
	if attempts <= 0 {
		attempts = 1
	}
	var lastErr error
	var responseStatus int
	var replayFlowID string
	attemptsUsed := 0
	for attempt := 1; attempt <= attempts; attempt++ {
		attemptsUsed = attempt
		result, err := adapter.Execute(ctx, prepared, rawHeaders, rawBody, params.ProfileOptions)
		if err != nil {
			lastErr = err
			continue
		}
		responseStatus = result.Status
		replayFlowID, err = e.recordReplayFlow(ctx, flow, prepared, rawHeaders, rawBody, result.Status, result.Headers, result.Body, result.Duration, adapter.Name())
		if err != nil {
			lastErr = err
			continue
		}
		if responseStatus < 500 {
			break
		}
	}
	run := store.ReplayRun{
		ID:             runID,
		OriginalFlowID: flow.ID,
		ReplayFlowID:   replayFlowID,
		OK:             replayFlowID != "" && responseStatus >= 200 && responseStatus < 400,
		Status:         responseStatus,
		Attempts:       attemptsUsed,
		CreatedAt:      time.Now().UTC(),
	}
	if lastErr != nil && replayFlowID == "" {
		run.Error = lastErr.Error()
	}
	if err := e.store.InsertReplayRun(ctx, run); err != nil {
		return Result{}, err
	}
	return Result{Run: run, PreparedRequest: prepared, Profile: adapter.Name()}, nil
}

func (e *Engine) RunVariations(ctx context.Context, params VariationParams) ([]VariationResult, error) {
	if params.FlowID == "" {
		return nil, fmt.Errorf("flow_id is required")
	}
	if len(params.Variations) == 0 {
		return nil, fmt.Errorf("at least one variation is required")
	}
	results := make([]VariationResult, 0, len(params.Variations))
	for i, variation := range params.Variations {
		name := variation.Name
		if name == "" {
			name = fmt.Sprintf("variation_%d", i+1)
		}
		result, err := e.Run(ctx, Params{
			FlowID:         params.FlowID,
			Overrides:      variation.Overrides,
			Retries:        params.Retries,
			Profile:        variation.Profile,
			ProfileOptions: variation.ProfileOptions,
		})
		if err != nil {
			return nil, err
		}
		results = append(results, VariationResult{Name: name, Result: result})
	}
	return results, nil
}

func (e *Engine) prepare(flow store.Flow, overrides Overrides) (PreparedRequest, http.Header, []byte, error) {
	headers := http.Header{}
	if len(flow.RequestHeaders) > 0 {
		if err := json.Unmarshal(flow.RequestHeaders, &headers); err != nil {
			return PreparedRequest{}, nil, nil, err
		}
	}
	body := []byte{}
	if flow.RequestBlob != "" {
		var err error
		body, err = e.blobs.Get(flow.RequestBlob)
		if err != nil {
			return PreparedRequest{}, nil, nil, err
		}
	}
	method := flow.Method
	if overrides.Method != "" {
		method = strings.ToUpper(overrides.Method)
	}
	targetURL := flow.URL
	if overrides.URL != "" {
		targetURL = overrides.URL
	}
	parsedURL, err := url.Parse(targetURL)
	if err != nil {
		return PreparedRequest{}, nil, nil, err
	}
	if len(overrides.Query) > 0 {
		query := parsedURL.Query()
		for key, value := range overrides.Query {
			query.Set(key, value)
		}
		parsedURL.RawQuery = query.Encode()
	}
	for _, header := range overrides.RemoveHeaders {
		headers.Del(header)
	}
	for key, values := range overrides.Headers {
		headers.Del(key)
		for _, value := range values {
			headers.Add(key, value)
		}
	}
	removeHopByHopHeaders(headers)
	if overrides.Body != nil {
		body = []byte(*overrides.Body)
	}
	prepared := PreparedRequest{
		Method:  method,
		URL:     parsedURL.String(),
		Headers: redact.Header(headers),
		Body:    string(redact.Body(body)),
	}
	return prepared, headers, body, nil
}

func (e *Engine) recordReplayFlow(ctx context.Context, original store.Flow, prepared PreparedRequest, requestHeaders http.Header, requestBody []byte, status int, responseHeaders http.Header, responseBody []byte, duration time.Duration, profile string) (string, error) {
	if err := e.store.EnsureSession(ctx, store.Session{ID: replaySessionID, Name: "Replay runs", Kind: "replay", CreatedAt: time.Now().UTC()}); err != nil {
		return "", err
	}
	requestBlob, err := e.blobs.Put(requestBody)
	if err != nil {
		return "", err
	}
	responseBlob, err := e.blobs.Put(responseBody)
	if err != nil {
		return "", err
	}
	requestHeadersBytes, _ := json.Marshal(requestHeaders)
	responseHeadersBytes, _ := json.Marshal(responseHeaders)
	flowID, err := id.New("flow")
	if err != nil {
		return "", err
	}
	parsedURL, _ := url.Parse(prepared.URL)
	host := ""
	if parsedURL != nil {
		host = parsedURL.Host
	}
	flow := store.Flow{
		ID:              flowID,
		SessionID:       replaySessionID,
		Method:          prepared.Method,
		AppName:         "deconstruct",
		Source:          "replay:" + replayProfile(profile),
		URL:             prepared.URL,
		Host:            host,
		Status:          status,
		Duration:        duration,
		RequestHeaders:  requestHeadersBytes,
		ResponseHeaders: responseHeadersBytes,
		RequestBlob:     requestBlob,
		ResponseBlob:    responseBlob,
		CreatedAt:       time.Now().UTC(),
	}
	if err := e.store.InsertFlow(ctx, flow); err != nil {
		return "", err
	}
	_ = e.store.TagFlow(ctx, flowID, "replay")
	_ = e.store.TagFlow(ctx, flowID, "replay_profile:"+replayProfile(profile))
	_ = e.store.TagFlow(ctx, flowID, "replay_of:"+original.ID)
	for _, tag := range bodyparse.Tags(requestHeaders, requestBody) {
		_ = e.store.TagFlow(ctx, flowID, tag)
	}
	for _, tag := range bodyparse.Tags(responseHeaders, responseBody) {
		_ = e.store.TagFlow(ctx, flowID, tag)
	}
	return flowID, nil
}

func (e *Engine) adapter(profile string) (Adapter, error) {
	name := replayProfile(profile)
	adapter, ok := e.adapters[name]
	if !ok {
		return nil, fmt.Errorf("unsupported replay profile: %s", profile)
	}
	return adapter, nil
}

func replayProfile(profile string) string {
	if profile == "" {
		return "standard"
	}
	return profile
}

func removeHopByHopHeaders(headers http.Header) {
	for _, header := range []string{
		"Connection",
		"Keep-Alive",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"Proxy-Connection",
		"Te",
		"Trailer",
		"Transfer-Encoding",
		"Upgrade",
	} {
		headers.Del(header)
	}
}

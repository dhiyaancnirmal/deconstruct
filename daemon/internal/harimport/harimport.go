package harimport

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/dhiyaan/deconstruct/daemon/internal/blobstore"
	"github.com/dhiyaan/deconstruct/daemon/internal/bodyparse"
	"github.com/dhiyaan/deconstruct/daemon/internal/id"
	"github.com/dhiyaan/deconstruct/daemon/internal/redact"
	"github.com/dhiyaan/deconstruct/daemon/internal/store"
)

type Importer struct {
	store *store.Store
	blobs *blobstore.Store
}

type Result struct {
	SessionID string `json:"session_id"`
	Flows     int    `json:"flows"`
}

type Options struct {
	Name    string `json:"name,omitempty"`
	Redact  *bool  `json:"redact,omitempty"`
	AppName string `json:"app_name,omitempty"`
}

func New(store *store.Store, blobs *blobstore.Store) *Importer {
	return &Importer{store: store, blobs: blobs}
}

func (i *Importer) ImportFile(ctx context.Context, path string, options Options) (Result, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{}, fmt.Errorf("read har: %w", err)
	}
	return i.Import(ctx, data, options)
}

func (i *Importer) Import(ctx context.Context, data []byte, options Options) (Result, error) {
	var document harDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return Result{}, fmt.Errorf("decode har: %w", err)
	}
	sessionID, err := id.New("session")
	if err != nil {
		return Result{}, err
	}
	name := options.Name
	if name == "" {
		name = "Imported HAR"
	}
	if err := i.store.EnsureSession(ctx, store.Session{
		ID:        sessionID,
		Name:      name,
		Kind:      "har",
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		return Result{}, err
	}
	redactSecrets := true
	if options.Redact != nil {
		redactSecrets = *options.Redact
	}
	appName := options.AppName
	if appName == "" {
		appName = "HAR"
	}
	count := 0
	for _, entry := range document.Log.Entries {
		if err := i.importEntry(ctx, sessionID, appName, redactSecrets, entry); err != nil {
			return Result{}, err
		}
		count++
	}
	return Result{SessionID: sessionID, Flows: count}, nil
}

func (i *Importer) importEntry(ctx context.Context, sessionID, appName string, redactSecrets bool, entry harEntry) error {
	flowID, err := id.New("flow")
	if err != nil {
		return err
	}
	requestHeaders := headerFromPairs(entry.Request.Headers)
	responseHeaders := headerFromPairs(entry.Response.Headers)
	if requestHeaders.Get("Content-Type") == "" && entry.Request.PostData.MimeType != "" {
		requestHeaders.Set("Content-Type", entry.Request.PostData.MimeType)
	}
	if responseHeaders.Get("Content-Type") == "" && entry.Response.Content.MimeType != "" {
		responseHeaders.Set("Content-Type", entry.Response.Content.MimeType)
	}
	addRequestCookies(requestHeaders, entry.Request.Cookies)
	addResponseCookies(responseHeaders, entry.Response.Cookies)
	requestBody := harText(entry.Request.PostData.Text, entry.Request.PostData.Encoding)
	responseBody := harText(entry.Response.Content.Text, entry.Response.Content.Encoding)
	if redactSecrets {
		requestHeaders = redact.Header(requestHeaders)
		responseHeaders = redact.Header(responseHeaders)
		requestBody = redact.Body(requestBody)
		responseBody = redact.Body(responseBody)
	}
	requestBlob, err := i.blobs.Put(requestBody)
	if err != nil {
		return err
	}
	responseBlob, err := i.blobs.Put(responseBody)
	if err != nil {
		return err
	}
	requestHeaderBytes, _ := json.Marshal(requestHeaders)
	responseHeaderBytes, _ := json.Marshal(responseHeaders)
	parsedURL, _ := url.Parse(entry.Request.URL)
	host := ""
	if parsedURL != nil {
		host = parsedURL.Host
	}
	if err := i.store.InsertFlow(ctx, store.Flow{
		ID:              flowID,
		SessionID:       sessionID,
		Method:          entry.Request.Method,
		AppName:         appName,
		Source:          "har",
		URL:             entry.Request.URL,
		Host:            host,
		Status:          entry.Response.Status,
		Duration:        time.Duration(entry.Time) * time.Millisecond,
		RequestHeaders:  requestHeaderBytes,
		ResponseHeaders: responseHeaderBytes,
		RequestBlob:     requestBlob,
		ResponseBlob:    responseBlob,
		CreatedAt:       parseHARTime(entry.StartedDateTime),
	}); err != nil {
		return err
	}
	_ = i.store.TagFlow(ctx, flowID, "imported_har")
	for _, tag := range bodyparse.Tags(requestHeaders, requestBody) {
		_ = i.store.TagFlow(ctx, flowID, tag)
	}
	for _, tag := range bodyparse.Tags(responseHeaders, responseBody) {
		_ = i.store.TagFlow(ctx, flowID, tag)
	}
	return nil
}

func headerFromPairs(pairs []harNameValue) http.Header {
	headers := make(http.Header)
	for _, pair := range pairs {
		headers.Add(pair.Name, pair.Value)
	}
	return headers
}

func addRequestCookies(headers http.Header, cookies []harCookie) {
	if len(cookies) == 0 || headers.Get("Cookie") != "" {
		return
	}
	var values []string
	for _, cookie := range cookies {
		values = append(values, cookie.Name+"="+cookie.Value)
	}
	headers.Set("Cookie", strings.Join(values, "; "))
}

func addResponseCookies(headers http.Header, cookies []harCookie) {
	for _, cookie := range cookies {
		headers.Add("Set-Cookie", cookie.Name+"="+cookie.Value)
	}
}

func harText(value, encoding string) []byte {
	if strings.EqualFold(encoding, "base64") {
		decoded, err := base64.StdEncoding.DecodeString(value)
		if err == nil {
			return decoded
		}
	}
	return []byte(value)
}

func parseHARTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err == nil {
		return parsed.UTC()
	}
	return time.Now().UTC()
}

type harDocument struct {
	Log harLog `json:"log"`
}

type harLog struct {
	Entries []harEntry `json:"entries"`
}

type harEntry struct {
	StartedDateTime string      `json:"startedDateTime"`
	Time            float64     `json:"time"`
	Request         harRequest  `json:"request"`
	Response        harResponse `json:"response"`
}

type harRequest struct {
	Method   string         `json:"method"`
	URL      string         `json:"url"`
	Headers  []harNameValue `json:"headers"`
	Cookies  []harCookie    `json:"cookies"`
	PostData harPostData    `json:"postData"`
}

type harResponse struct {
	Status  int            `json:"status"`
	Headers []harNameValue `json:"headers"`
	Cookies []harCookie    `json:"cookies"`
	Content harContent     `json:"content"`
}

type harNameValue struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type harCookie struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type harPostData struct {
	MimeType string `json:"mimeType"`
	Text     string `json:"text"`
	Encoding string `json:"encoding"`
}

type harContent struct {
	MimeType string `json:"mimeType"`
	Text     string `json:"text"`
	Encoding string `json:"encoding"`
}

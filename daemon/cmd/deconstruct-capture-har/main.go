package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type cdpClient struct {
	conn    *websocket.Conn
	mu      sync.Mutex
	nextID  int
	waiters map[int]chan map[string]any
	events  chan map[string]any
	err     error
}

type targetInfo struct {
	Type                 string `json:"type"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

type captureFlow struct {
	StartedAt       time.Time
	TimeMS          float64
	Method          string
	URL             string
	RequestHeaders  map[string]string
	RequestBody     string
	Status          int
	ResponseHeaders map[string]string
	ResponseBody    string
	MimeType        string
	Encoding        string
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	urlFlag := flag.String("url", "about:blank", "URL to open before recording")
	out := flag.String("out", "", "HAR output path")
	jsPath := flag.String("js", "", "optional JavaScript file to evaluate after page load")
	wait := flag.Duration("wait", 5*time.Second, "time to wait after navigation/evaluation")
	port := flag.Int("port", 9333, "Chrome remote debugging port")
	headless := flag.Bool("headless", true, "launch Chrome in headless mode")
	chrome := flag.String("chrome", "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "Chrome executable")
	userDataDirFlag := flag.String("user-data-dir", "", "existing Chrome user data directory to use")
	profileDir := flag.String("profile-directory", "", "Chrome profile directory name, for example Default")
	flag.Parse()
	if *out == "" {
		return fmt.Errorf("-out is required")
	}
	userDataDir := *userDataDirFlag
	cleanupProfile := false
	if userDataDir == "" {
		var err error
		userDataDir, err = os.MkdirTemp("", "deconstruct-chrome-*")
		if err != nil {
			return err
		}
		cleanupProfile = true
	}
	if cleanupProfile {
		defer os.RemoveAll(userDataDir)
	}
	args := []string{
		"--remote-debugging-port=" + strconv.Itoa(*port),
		"--user-data-dir=" + userDataDir,
		"--no-first-run",
		"--disable-default-apps",
		"--disable-background-networking",
		"--disable-features=Translate,OptimizationHints",
		"about:blank",
	}
	if *profileDir != "" {
		args = append(args, "--profile-directory="+*profileDir)
	}
	if *headless {
		args = append([]string{"--headless=new", "--disable-gpu"}, args...)
	}
	cmd := exec.Command(*chrome, args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launch chrome: %w", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), *wait+20*time.Second)
	defer cancel()
	wsURL, err := waitForPageTarget(ctx, *port)
	if err != nil {
		return err
	}
	client, err := dial(wsURL)
	if err != nil {
		return err
	}
	defer client.close()
	flows := map[string]*captureFlow{}
	go collect(client, flows)
	if _, err := client.call(ctx, "Network.enable", map[string]any{}); err != nil {
		return err
	}
	if _, err := client.call(ctx, "Page.enable", map[string]any{}); err != nil {
		return err
	}
	if _, err := client.call(ctx, "Page.navigate", map[string]any{"url": *urlFlag}); err != nil {
		return err
	}
	time.Sleep(2 * time.Second)
	if *jsPath != "" {
		source, err := os.ReadFile(*jsPath)
		if err != nil {
			return err
		}
		if _, err := client.call(ctx, "Runtime.evaluate", map[string]any{
			"expression":                  string(source),
			"awaitPromise":                true,
			"userGesture":                 true,
			"returnByValue":               true,
			"allowUnsafeEvalBlockedByCSP": true,
		}); err != nil {
			return err
		}
	}
	time.Sleep(*wait)
	client.close()
	return writeHAR(*out, flows)
}

func waitForPageTarget(ctx context.Context, port int) (string, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d/json", port)
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			var targets []targetInfo
			_ = json.NewDecoder(resp.Body).Decode(&targets)
			_ = resp.Body.Close()
			for _, target := range targets {
				if target.Type == "page" && target.WebSocketDebuggerURL != "" {
					return target.WebSocketDebuggerURL, nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("chrome debugging target not available: %w", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func dial(wsURL string) (*cdpClient, error) {
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return nil, err
	}
	client := &cdpClient{conn: conn, waiters: map[int]chan map[string]any{}, events: make(chan map[string]any, 1000)}
	go client.readLoop()
	return client, nil
}

func (c *cdpClient) call(ctx context.Context, method string, params map[string]any) (map[string]any, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan map[string]any, 1)
	c.waiters[id] = ch
	c.mu.Unlock()
	if err := c.conn.WriteJSON(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	select {
	case response := <-ch:
		if rawErr, ok := response["error"]; ok {
			return nil, fmt.Errorf("cdp %s failed: %v", method, rawErr)
		}
		return response, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *cdpClient) readLoop() {
	defer close(c.events)
	for {
		var message map[string]any
		if err := c.conn.ReadJSON(&message); err != nil {
			c.err = err
			return
		}
		if idFloat, ok := message["id"].(float64); ok {
			id := int(idFloat)
			c.mu.Lock()
			ch := c.waiters[id]
			delete(c.waiters, id)
			c.mu.Unlock()
			if ch != nil {
				ch <- message
			}
			continue
		}
		c.events <- message
	}
}

func (c *cdpClient) close() {
	_ = c.conn.Close()
}

func collect(client *cdpClient, flows map[string]*captureFlow) {
	for event := range client.events {
		method, _ := event["method"].(string)
		params, _ := event["params"].(map[string]any)
		requestID, _ := params["requestId"].(string)
		if requestID == "" {
			continue
		}
		switch method {
		case "Network.requestWillBeSent":
			request, _ := params["request"].(map[string]any)
			flows[requestID] = &captureFlow{
				StartedAt:      time.Now().UTC(),
				Method:         stringValue(request["method"]),
				URL:            stringValue(request["url"]),
				RequestHeaders: headers(request["headers"]),
				RequestBody:    stringValue(request["postData"]),
			}
		case "Network.responseReceived":
			flow := flows[requestID]
			if flow == nil {
				continue
			}
			response, _ := params["response"].(map[string]any)
			flow.Status = int(floatValue(response["status"]))
			flow.ResponseHeaders = headers(response["headers"])
			flow.MimeType = stringValue(response["mimeType"])
		case "Network.loadingFinished":
			flow := flows[requestID]
			if flow == nil {
				continue
			}
			flow.TimeMS = time.Since(flow.StartedAt).Seconds() * 1000
			body, encoding := responseBody(context.Background(), client, requestID)
			flow.ResponseBody = body
			flow.Encoding = encoding
		}
	}
}

func responseBody(ctx context.Context, client *cdpClient, requestID string) (string, string) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	response, err := client.call(ctx, "Network.getResponseBody", map[string]any{"requestId": requestID})
	if err != nil {
		return "", ""
	}
	result, _ := response["result"].(map[string]any)
	body := stringValue(result["body"])
	if encoded, _ := result["base64Encoded"].(bool); encoded {
		if decoded, err := base64.StdEncoding.DecodeString(body); err == nil {
			return string(decoded), ""
		}
		return body, "base64"
	}
	return body, ""
}

func writeHAR(path string, flows map[string]*captureFlow) error {
	entries := make([]map[string]any, 0, len(flows))
	for _, flow := range flows {
		if flow.URL == "" || strings.HasPrefix(flow.URL, "data:") || strings.HasPrefix(flow.URL, "about:") {
			continue
		}
		entries = append(entries, map[string]any{
			"startedDateTime": flow.StartedAt.Format(time.RFC3339Nano),
			"time":            flow.TimeMS,
			"request": map[string]any{
				"method":   flow.Method,
				"url":      flow.URL,
				"headers":  pairs(flow.RequestHeaders),
				"postData": postData(flow.RequestBody, flow.RequestHeaders),
			},
			"response": map[string]any{
				"status":  flow.Status,
				"headers": pairs(flow.ResponseHeaders),
				"content": map[string]any{"mimeType": flow.MimeType, "text": flow.ResponseBody, "encoding": flow.Encoding},
			},
		})
	}
	document := map[string]any{"log": map[string]any{"version": "1.2", "creator": map[string]string{"name": "deconstruct-capture-har"}, "entries": entries}}
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func postData(body string, headers map[string]string) map[string]string {
	if body == "" {
		return nil
	}
	return map[string]string{"mimeType": contentType(headers), "text": body}
}

func contentType(headers map[string]string) string {
	for key, value := range headers {
		if strings.EqualFold(key, "content-type") {
			return value
		}
	}
	return "application/octet-stream"
}

func headers(value any) map[string]string {
	out := map[string]string{}
	object, _ := value.(map[string]any)
	for key, raw := range object {
		out[key] = fmt.Sprint(raw)
	}
	return out
}

func pairs(headers map[string]string) []map[string]string {
	out := make([]map[string]string, 0, len(headers))
	for key, value := range headers {
		out = append(out, map[string]string{"name": key, "value": value})
	}
	return out
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func floatValue(value any) float64 {
	if number, ok := value.(float64); ok {
		return number
	}
	return 0
}

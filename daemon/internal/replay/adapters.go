package replay

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"

	utls "github.com/refraction-networking/utls"
)

type StandardAdapter struct {
	client *http.Client
}

func NewStandardAdapter(jar *cookiejar.Jar) StandardAdapter {
	return StandardAdapter{client: &http.Client{
		Timeout: 60 * time.Second,
		Jar:     jar,
	}}
}

func (a StandardAdapter) Name() string {
	return "standard"
}

func (a StandardAdapter) Execute(ctx context.Context, prepared PreparedRequest, headers http.Header, body []byte, _ map[string]string) (AdapterResult, error) {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, prepared.Method, prepared.URL, bytes.NewReader(body))
	if err != nil {
		return AdapterResult{}, err
	}
	req.Header = headers.Clone()
	resp, err := a.client.Do(req)
	if err != nil {
		return AdapterResult{}, err
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return AdapterResult{}, err
	}
	return AdapterResult{Status: resp.StatusCode, Headers: resp.Header.Clone(), Body: responseBody, Duration: time.Since(start)}, nil
}

type UTLSAdapter struct{}

func NewUTLSAdapter() UTLSAdapter {
	return UTLSAdapter{}
}

func (a UTLSAdapter) Name() string {
	return "utls"
}

func (a UTLSAdapter) Execute(ctx context.Context, prepared PreparedRequest, headers http.Header, body []byte, options map[string]string) (AdapterResult, error) {
	parsed, err := url.Parse(prepared.URL)
	if err != nil {
		return AdapterResult{}, err
	}
	if parsed.Scheme != "https" {
		return StandardAdapter{client: &http.Client{Timeout: 60 * time.Second}}.Execute(ctx, prepared, headers, body, options)
	}
	host := parsed.Hostname()
	port := parsed.Port()
	if port == "" {
		port = "443"
	}
	address := net.JoinHostPort(host, port)
	dialer := net.Dialer{Timeout: 30 * time.Second}
	start := time.Now()
	rawConn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return AdapterResult{}, err
	}
	defer rawConn.Close()
	helloID := clientHelloID(options["hello"])
	config := &utls.Config{
		ServerName: host,
		NextProtos: []string{
			"http/1.1",
		},
		InsecureSkipVerify: options["insecure_skip_verify"] == "true",
	}
	tlsConn := utls.UClient(rawConn, config, helloID)
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		return AdapterResult{}, err
	}
	defer tlsConn.Close()
	req, err := http.NewRequestWithContext(ctx, prepared.Method, prepared.URL, bytes.NewReader(body))
	if err != nil {
		return AdapterResult{}, err
	}
	req.Header = headers.Clone()
	req.Host = parsed.Host
	req.RequestURI = ""
	if err := req.Write(tlsConn); err != nil {
		return AdapterResult{}, err
	}
	resp, err := http.ReadResponse(bufioReader(tlsConn), req)
	if err != nil {
		return AdapterResult{}, err
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return AdapterResult{}, err
	}
	return AdapterResult{Status: resp.StatusCode, Headers: resp.Header.Clone(), Body: responseBody, Duration: time.Since(start)}, nil
}

type CurlImpersonateAdapter struct {
	binary string
}

func NewCurlImpersonateAdapter(binary string) CurlImpersonateAdapter {
	return CurlImpersonateAdapter{binary: binary}
}

func (a CurlImpersonateAdapter) Name() string {
	return "curl_impersonate"
}

func (a CurlImpersonateAdapter) Execute(ctx context.Context, prepared PreparedRequest, headers http.Header, body []byte, options map[string]string) (AdapterResult, error) {
	binary, err := a.binaryPath(options)
	if err != nil {
		return AdapterResult{}, err
	}
	args := []string{"-i", "-sS", "-X", prepared.Method}
	for key, values := range headers {
		for _, value := range values {
			args = append(args, "-H", key+": "+value)
		}
	}
	if len(body) > 0 {
		args = append(args, "--data-binary", string(body))
	}
	if connectTimeout := options["connect_timeout"]; connectTimeout != "" {
		args = append(args, "--connect-timeout", connectTimeout)
	}
	args = append(args, prepared.URL)
	start := time.Now()
	cmd := exec.CommandContext(ctx, binary, args...)
	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return AdapterResult{}, fmt.Errorf("curl-impersonate failed: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return AdapterResult{}, err
	}
	status, responseHeaders, responseBody, err := parseCurlHTTPOutput(output)
	if err != nil {
		return AdapterResult{}, err
	}
	return AdapterResult{Status: status, Headers: responseHeaders, Body: responseBody, Duration: time.Since(start)}, nil
}

type BrowserAdapter struct{}

func NewBrowserAdapter() BrowserAdapter {
	return BrowserAdapter{}
}

func (a BrowserAdapter) Name() string {
	return "browser"
}

func (a BrowserAdapter) Execute(ctx context.Context, prepared PreparedRequest, headers http.Header, body []byte, options map[string]string) (AdapterResult, error) {
	endpoint := options["cdp_url"]
	if endpoint == "" {
		endpoint = "http://127.0.0.1:9222"
	}
	start := time.Now()
	wsURL, err := discoverCDPWebSocket(ctx, endpoint)
	if err != nil {
		return AdapterResult{}, err
	}
	conn, err := dialWebSocket(ctx, wsURL)
	if err != nil {
		return AdapterResult{}, err
	}
	defer conn.Close()
	expression, err := browserFetchExpression(prepared, headers, body)
	if err != nil {
		return AdapterResult{}, err
	}
	response, err := conn.Call(ctx, "Runtime.evaluate", map[string]any{
		"expression":                  expression,
		"awaitPromise":                true,
		"returnByValue":               true,
		"userGesture":                 true,
		"allowUnsafeEvalBlockedByCSP": true,
	})
	if err != nil {
		return AdapterResult{}, err
	}
	value, err := cdpResultValue(response)
	if err != nil {
		return AdapterResult{}, err
	}
	status := int(value["status"].(float64))
	responseHeaders := http.Header{}
	if rawHeaders, ok := value["headers"].(map[string]any); ok {
		for key, raw := range rawHeaders {
			responseHeaders.Set(key, fmt.Sprint(raw))
		}
	}
	responseBody := []byte(fmt.Sprint(value["body"]))
	return AdapterResult{Status: status, Headers: responseHeaders, Body: responseBody, Duration: time.Since(start)}, nil
}

type PassthroughAdapter struct{}

func (PassthroughAdapter) Name() string {
	return "passthrough"
}

func (PassthroughAdapter) Execute(context.Context, PreparedRequest, http.Header, []byte, map[string]string) (AdapterResult, error) {
	return AdapterResult{
		Status: http.StatusNoContent,
		Headers: http.Header{
			"X-Deconstruct-Passthrough": []string{"validated"},
		},
		Body:     []byte(`{"ok":true,"mode":"passthrough_validation"}`),
		Duration: 0,
	}, nil
}

func clientHelloID(name string) utls.ClientHelloID {
	switch strings.ToLower(name) {
	case "firefox":
		return utls.HelloFirefox_Auto
	case "safari":
		return utls.HelloSafari_Auto
	case "ios":
		return utls.HelloIOS_Auto
	case "android":
		return utls.HelloAndroid_11_OkHttp
	case "edge":
		return utls.HelloEdge_Auto
	case "random", "randomized":
		return utls.HelloRandomizedALPN
	case "chrome", "":
		return utls.HelloChrome_Auto
	default:
		return utls.HelloChrome_Auto
	}
}

func (a CurlImpersonateAdapter) binaryPath(options map[string]string) (string, error) {
	if options["binary"] != "" {
		return options["binary"], nil
	}
	if a.binary != "" {
		return a.binary, nil
	}
	for _, candidate := range []string{"curl-impersonate", "curl_chrome", "curl-impersonate-chrome", "curl_ff", "curl-impersonate-ff"} {
		if path, err := exec.LookPath(candidate); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("curl-impersonate binary not found; install curl-impersonate or pass profile_options.binary")
}

func parseCurlHTTPOutput(output []byte) (int, http.Header, []byte, error) {
	separator := []byte("\r\n\r\n")
	parts := bytes.Split(output, separator)
	if len(parts) < 2 {
		separator = []byte("\n\n")
		parts = bytes.Split(output, separator)
	}
	if len(parts) < 2 {
		return 0, nil, nil, fmt.Errorf("curl output did not include HTTP headers")
	}
	headerBlock := parts[len(parts)-2]
	body := parts[len(parts)-1]
	lines := strings.Split(strings.ReplaceAll(string(headerBlock), "\r\n", "\n"), "\n")
	status := 0
	if len(lines) > 0 {
		fields := strings.Fields(lines[0])
		if len(fields) >= 2 {
			status, _ = strconv.Atoi(fields[1])
		}
	}
	headers := http.Header{}
	for _, line := range lines[1:] {
		key, value, ok := strings.Cut(line, ":")
		if ok {
			headers.Add(strings.TrimSpace(key), strings.TrimSpace(value))
		}
	}
	if status == 0 {
		return 0, nil, nil, fmt.Errorf("curl output did not include a parseable status line")
	}
	return status, headers, body, nil
}

func browserFetchExpression(prepared PreparedRequest, headers http.Header, body []byte) (string, error) {
	headerMap := map[string]string{}
	for key, values := range headers {
		headerMap[key] = strings.Join(values, ", ")
	}
	payload := map[string]any{
		"url":     prepared.URL,
		"method":  prepared.Method,
		"headers": headerMap,
		"body":    base64.StdEncoding.EncodeToString(body),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return `(async () => {
const input = ` + string(data) + `;
const headers = input.headers || {};
const init = { method: input.method, headers, credentials: "include" };
if (input.body) {
  const raw = atob(input.body);
  const bytes = new Uint8Array(raw.length);
  for (let i = 0; i < raw.length; i++) bytes[i] = raw.charCodeAt(i);
  init.body = bytes;
}
const response = await fetch(input.url, init);
const responseHeaders = {};
response.headers.forEach((value, key) => responseHeaders[key] = value);
return { status: response.status, headers: responseHeaders, body: await response.text() };
})()`, nil
}

func discoverCDPWebSocket(ctx context.Context, endpoint string) (string, error) {
	endpoint = strings.TrimRight(endpoint, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/json/version", nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var version struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&version); err != nil {
		return "", err
	}
	if version.WebSocketDebuggerURL == "" {
		return "", fmt.Errorf("CDP endpoint did not expose webSocketDebuggerUrl")
	}
	return version.WebSocketDebuggerURL, nil
}

func cdpResultValue(response map[string]any) (map[string]any, error) {
	result, ok := response["result"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("CDP response missing result")
	}
	if exception, ok := result["exceptionDetails"]; ok {
		return nil, fmt.Errorf("browser replay failed: %v", exception)
	}
	remote, ok := result["result"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("CDP response missing remote object")
	}
	value, ok := remote["value"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("CDP response missing return value")
	}
	return value, nil
}

func bufioReader(r io.Reader) *bufio.Reader {
	return bufio.NewReader(r)
}

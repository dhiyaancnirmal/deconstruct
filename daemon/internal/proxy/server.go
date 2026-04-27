package proxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dhiyaan/deconstruct/daemon/internal/blobstore"
	"github.com/dhiyaan/deconstruct/daemon/internal/bodyparse"
	"github.com/dhiyaan/deconstruct/daemon/internal/certs"
	"github.com/dhiyaan/deconstruct/daemon/internal/id"
	"github.com/dhiyaan/deconstruct/daemon/internal/redact"
	"github.com/dhiyaan/deconstruct/daemon/internal/store"
)

const defaultSessionID = "session_live_proxy"

type Server struct {
	store   *store.Store
	blobs   *blobstore.Store
	ca      *certs.Authority
	client  *http.Client
	enabled atomic.Bool
}

func NewServer(store *store.Store, blobs *blobstore.Store, ca *certs.Authority) *Server {
	server := &Server{
		store: store,
		blobs: blobs,
		ca:    ca,
		client: &http.Client{
			Timeout: 60 * time.Second,
			Transport: &http.Transport{
				Proxy: nil,
			},
		},
	}
	server.enabled.Store(true)
	return server
}

func (s *Server) StartCapture() {
	s.enabled.Store(true)
}

func (s *Server) StopCapture() {
	s.enabled.Store(false)
}

func (s *Server) Status() map[string]any {
	return map[string]any{
		"enabled": s.enabled.Load(),
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.enabled.Load() {
		http.Error(w, "capture is stopped", http.StatusServiceUnavailable)
		return
	}
	if r.Method == http.MethodConnect {
		s.handleConnect(w, r)
		return
	}
	if r.URL == nil || !r.URL.IsAbs() {
		http.Error(w, "proxy requests must use absolute-form URLs", http.StatusBadRequest)
		return
	}
	resp, body, err := s.execute(r.Context(), r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	if s.ca == nil {
		http.Error(w, "CONNECT interception requires a local CA", http.StatusNotImplemented)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "response writer does not support hijacking", http.StatusInternalServerError)
		return
	}
	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		slog.Error("hijack connect", "error", err)
		return
	}
	defer clientConn.Close()

	cert, err := s.ca.TLSCertificateForHost(r.Host)
	if err != nil {
		_, _ = io.WriteString(clientConn, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
		slog.Error("generate leaf certificate", "host", r.Host, "error", err)
		return
	}
	if _, err := io.WriteString(clientConn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		slog.Error("acknowledge connect", "error", err)
		return
	}

	tlsConn := tls.Server(clientConn, &tls.Config{
		Certificates: []tls.Certificate{*cert},
		MinVersion:   tls.VersionTLS12,
	})
	if err := tlsConn.Handshake(); err != nil {
		slog.Error("tls handshake", "host", r.Host, "error", err)
		return
	}
	defer tlsConn.Close()

	reader := bufio.NewReader(tlsConn)
	for {
		req, err := http.ReadRequest(reader)
		if err != nil {
			if err != io.EOF && !isClosedNetworkError(err) {
				slog.Error("read tunneled request", "host", r.Host, "error", err)
			}
			return
		}
		req.URL.Scheme = "https"
		req.URL.Host = r.Host
		req.Host = r.Host

		resp, body, err := s.execute(context.Background(), req)
		if err != nil {
			_ = writeTunnelError(tlsConn, http.StatusBadGateway, err.Error())
			continue
		}
		resp.Body = io.NopCloser(bytes.NewReader(body))
		resp.ContentLength = int64(len(body))
		resp.Close = req.Close
		if err := resp.Write(tlsConn); err != nil {
			_ = resp.Body.Close()
			slog.Error("write tunneled response", "host", r.Host, "error", err)
			return
		}
		_ = resp.Body.Close()
		if req.Close {
			return
		}
	}
}

func (s *Server) execute(ctx context.Context, r *http.Request) (*http.Response, []byte, error) {
	start := time.Now()
	if err := s.store.EnsureSession(ctx, store.Session{
		ID:        defaultSessionID,
		Name:      "Live proxy capture",
		Kind:      "capture",
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		return nil, nil, err
	}

	requestBody, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("read request body: %w", err)
	}
	_ = r.Body.Close()

	outbound, err := http.NewRequestWithContext(ctx, r.Method, r.URL.String(), bytes.NewReader(requestBody))
	if err != nil {
		return nil, nil, fmt.Errorf("create outbound request: %w", err)
	}
	copyHeaders(outbound.Header, r.Header)
	removeHopByHopHeaders(outbound.Header)
	outbound.RequestURI = ""
	outbound.Host = r.Host

	resp, err := s.client.Do(outbound)
	if err != nil {
		s.record(ctx, r, requestBody, nil, nil, 0, time.Since(start))
		return nil, nil, err
	}

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		_ = resp.Body.Close()
		s.record(ctx, r, requestBody, resp.Header, nil, resp.StatusCode, time.Since(start))
		return nil, nil, fmt.Errorf("read response body: %w", err)
	}
	_ = resp.Body.Close()

	s.record(ctx, r, requestBody, resp.Header, responseBody, resp.StatusCode, time.Since(start))
	resp.Body = io.NopCloser(bytes.NewReader(responseBody))
	return resp, responseBody, nil
}

func (s *Server) record(ctx context.Context, r *http.Request, requestBody []byte, responseHeaders http.Header, responseBody []byte, status int, duration time.Duration) {
	requestBlob, err := s.blobs.Put(requestBody)
	if err != nil {
		slog.Error("store request blob", "error", err)
	}
	responseBlob, err := s.blobs.Put(responseBody)
	if err != nil {
		slog.Error("store response blob", "error", err)
	}
	requestHeaders, err := json.Marshal(r.Header)
	if err != nil {
		slog.Error("marshal request headers", "error", err)
	}
	responseHeaderBytes, err := json.Marshal(responseHeaders)
	if err != nil {
		slog.Error("marshal response headers", "error", err)
	}
	flowID, err := id.New("flow")
	if err != nil {
		slog.Error("generate flow id", "error", err)
		flowID = fmt.Sprintf("flow_%d", time.Now().UnixNano())
	}
	flow := store.Flow{
		ID:              flowID,
		SessionID:       defaultSessionID,
		Method:          r.Method,
		AppName:         "Unknown",
		Source:          r.RemoteAddr,
		URL:             r.URL.String(),
		Host:            r.URL.Host,
		Status:          status,
		Duration:        duration,
		RequestHeaders:  requestHeaders,
		ResponseHeaders: responseHeaderBytes,
		RequestBlob:     requestBlob,
		ResponseBlob:    responseBlob,
		CreatedAt:       time.Now().UTC(),
	}
	if err := s.store.InsertFlow(ctx, flow); err != nil {
		slog.Error("record flow", "error", err)
		return
	}
	s.applyTags(ctx, flowID, r.Header, requestBody, responseHeaders, responseBody)
}

func (s *Server) applyTags(ctx context.Context, flowID string, requestHeaders http.Header, requestBody []byte, responseHeaders http.Header, responseBody []byte) {
	tags := map[string]bool{}
	for _, tag := range bodyparse.Tags(requestHeaders, requestBody) {
		tags[tag] = true
	}
	for _, tag := range bodyparse.Tags(responseHeaders, responseBody) {
		tags[tag] = true
	}
	if redact.HeaderHasSensitive(requestHeaders) || redact.HeaderHasSensitive(responseHeaders) {
		tags["auth_candidate"] = true
	}
	for tag := range tags {
		if err := s.store.TagFlow(ctx, flowID, tag); err != nil {
			slog.Error("tag flow", "flow_id", flowID, "tag", tag, "error", err)
		}
	}
}

func copyHeaders(dst, src http.Header) {
	for key, values := range src {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
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

func writeTunnelError(w io.Writer, status int, message string) error {
	resp := &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewBufferString(message + "\n")),
	}
	resp.Header.Set("Content-Type", "text/plain; charset=utf-8")
	resp.ContentLength = int64(len(message) + 1)
	return resp.Write(w)
}

func isClosedNetworkError(err error) bool {
	if errors.Is(err, net.ErrClosed) {
		return true
	}
	message := err.Error()
	return strings.Contains(message, "use of closed network connection") || strings.Contains(message, "connection reset by peer")
}

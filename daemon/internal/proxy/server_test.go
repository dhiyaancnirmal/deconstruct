package proxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/dhiyaan/deconstruct/daemon/internal/blobstore"
	"github.com/dhiyaan/deconstruct/daemon/internal/certs"
	"github.com/dhiyaan/deconstruct/daemon/internal/store"
)

func TestProxyCapturesHTTPFlow(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer target.Close()

	db, blobs, authority := testDeps(t)
	proxy := httptest.NewServer(NewServer(db, blobs, authority))
	defer proxy.Close()

	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		},
	}
	req, err := http.NewRequest(http.MethodGet, target.URL+"/api", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	count, err := db.CountFlows(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1 captured flow, got %d", count)
	}
}

func TestProxyCapturesHTTPSConnectFlow(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"secure":true}`))
	}))
	defer target.Close()

	db, blobs, authority := testDeps(t)
	handler := NewServer(db, blobs, authority)
	handler.client = target.Client()
	proxy := httptest.NewServer(handler)
	defer proxy.Close()

	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(authority.CertificatePEM()) {
		t.Fatal("failed to append deconstruct root ca")
	}
	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{
				RootCAs: roots,
			},
		},
	}
	resp, err := client.Get(target.URL + "/secure")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"secure":true}` {
		t.Fatalf("unexpected body: %s", body)
	}

	flows, err := db.ListFlows(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(flows) != 1 {
		t.Fatalf("expected 1 captured flow, got %d", len(flows))
	}
	if flows[0].Method != http.MethodGet || flows[0].Status != http.StatusOK {
		t.Fatalf("unexpected flow: %#v", flows[0])
	}
	if got := flows[0].URL; len(got) < 8 || got[:8] != "https://" {
		t.Fatalf("expected https URL, got %q", got)
	}
}

func TestProxyRejectsConnectWithoutCA(t *testing.T) {
	db, blobs, _ := testDeps(t)
	req := httptest.NewRequest(http.MethodConnect, "http://example.test:443", nil)
	rec := httptest.NewRecorder()
	NewServer(db, blobs, nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d", rec.Code)
	}
}

func TestProxyStopCapture(t *testing.T) {
	db, blobs, authority := testDeps(t)
	handler := NewServer(db, blobs, authority)
	handler.StopCapture()
	req := httptest.NewRequest(http.MethodGet, "http://example.test/path", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
	handler.StartCapture()
	if !handler.Status()["enabled"].(bool) {
		t.Fatal("expected capture to be enabled")
	}
}

func testDeps(t *testing.T) (*store.Store, *blobstore.Store, *certs.Authority) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	blobs, err := blobstore.Open(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	authority, err := certs.LoadOrCreate(filepath.Join(t.TempDir(), "ca"))
	if err != nil {
		t.Fatal(err)
	}
	return db, blobs, authority
}

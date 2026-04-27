package replay

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dhiyaan/deconstruct/daemon/internal/blobstore"
	"github.com/dhiyaan/deconstruct/daemon/internal/store"
	"github.com/gorilla/websocket"
)

func TestReplayRunRecordsFlowAndRun(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("name") != "new" {
			t.Fatalf("expected query override")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"created":true}`))
	}))
	defer target.Close()

	db, blobs := testDeps(t)
	bodyHash, err := blobs.Put([]byte(`{"old":true}`))
	if err != nil {
		t.Fatal(err)
	}
	headers, _ := json.Marshal(http.Header{"Content-Type": []string{"application/json"}})
	ctx := context.Background()
	if err := db.EnsureSession(ctx, store.Session{ID: "session_1", Name: "Capture", Kind: "capture", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertFlow(ctx, store.Flow{
		ID:              "flow_1",
		SessionID:       "session_1",
		Method:          "POST",
		URL:             target.URL + "/items?name=old",
		Host:            "127.0.0.1",
		Status:          200,
		RequestHeaders:  headers,
		ResponseHeaders: []byte(`{}`),
		RequestBlob:     bodyHash,
		CreatedAt:       time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	engine, err := New(db, blobs)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"new":true}`
	result, err := engine.Run(ctx, Params{
		FlowID: "flow_1",
		Overrides: Overrides{
			Query: map[string]string{"name": "new"},
			Body:  &body,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Run.OK || result.Run.ReplayFlowID == "" || result.Run.Status != http.StatusCreated {
		t.Fatalf("unexpected replay result: %#v", result)
	}
	flow, err := db.GetFlow(ctx, result.Run.ReplayFlowID)
	if err != nil {
		t.Fatal(err)
	}
	if flow.Source != "replay:standard" || flow.Status != http.StatusCreated {
		t.Fatalf("unexpected replay flow: %#v", flow)
	}
}

func TestReplayDryRun(t *testing.T) {
	db, blobs := testDeps(t)
	ctx := context.Background()
	if err := db.EnsureSession(ctx, store.Session{ID: "session_1", Name: "Capture", Kind: "capture", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertFlow(ctx, store.Flow{
		ID:              "flow_1",
		SessionID:       "session_1",
		Method:          "GET",
		URL:             "https://example.test",
		Host:            "example.test",
		Status:          200,
		RequestHeaders:  []byte(`{}`),
		ResponseHeaders: []byte(`{}`),
		CreatedAt:       time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	engine, err := New(db, blobs)
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Run(ctx, Params{FlowID: "flow_1", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Run.OK || result.Run.Attempts != 0 || result.Run.ReplayFlowID != "" {
		t.Fatalf("unexpected dry run: %#v", result.Run)
	}
}

func TestReplayVariations(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	db, blobs := testDeps(t)
	ctx := context.Background()
	if err := db.EnsureSession(ctx, store.Session{ID: "session_1", Name: "Capture", Kind: "capture", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertFlow(ctx, store.Flow{
		ID:              "flow_1",
		SessionID:       "session_1",
		Method:          "POST",
		URL:             target.URL,
		Host:            "127.0.0.1",
		Status:          200,
		RequestHeaders:  []byte(`{}`),
		ResponseHeaders: []byte(`{}`),
		CreatedAt:       time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	engine, err := New(db, blobs)
	if err != nil {
		t.Fatal(err)
	}
	first := `{"case":1}`
	second := `{"case":2}`
	results, err := engine.RunVariations(ctx, VariationParams{
		FlowID: "flow_1",
		Variations: []Variation{
			{Name: "first", Overrides: Overrides{Body: &first}},
			{Name: "second", Overrides: Overrides{Body: &second}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || !results[0].Result.Run.OK || !results[1].Result.Run.OK {
		t.Fatalf("unexpected variation results: %#v", results)
	}
}

func TestReplayUTLSProfile(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test") != "utls" {
			t.Fatalf("missing header")
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("ok"))
	}))
	defer target.Close()
	db, blobs := testDeps(t)
	ctx := context.Background()
	if err := db.EnsureSession(ctx, store.Session{ID: "session_1", Name: "Capture", Kind: "capture", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertFlow(ctx, store.Flow{
		ID:              "flow_utls",
		SessionID:       "session_1",
		Method:          "GET",
		URL:             target.URL,
		Host:            "127.0.0.1",
		Status:          200,
		RequestHeaders:  []byte(`{"X-Test":["utls"]}`),
		ResponseHeaders: []byte(`{}`),
		CreatedAt:       time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	engine, err := New(db, blobs)
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Run(ctx, Params{
		FlowID:  "flow_utls",
		Profile: "utls",
		ProfileOptions: map[string]string{
			"insecure_skip_verify": "true",
			"hello":                "chrome",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Run.OK || result.Profile != "utls" || result.Run.Status != http.StatusAccepted {
		t.Fatalf("unexpected utls result: %#v", result)
	}
}

func TestReplayCurlImpersonateProfile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake is POSIX-only")
	}
	db, blobs := testDeps(t)
	ctx := context.Background()
	if err := db.EnsureSession(ctx, store.Session{ID: "session_1", Name: "Capture", Kind: "capture", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertFlow(ctx, store.Flow{
		ID:              "flow_curl",
		SessionID:       "session_1",
		Method:          "POST",
		URL:             "https://example.test/api",
		Host:            "example.test",
		Status:          200,
		RequestHeaders:  []byte(`{"Content-Type":["application/json"]}`),
		ResponseHeaders: []byte(`{}`),
		CreatedAt:       time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(t.TempDir(), "curl_chrome")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nprintf 'HTTP/1.1 202 Accepted\\r\\nContent-Type: application/json\\r\\n\\r\\n{\"ok\":true}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	engine, err := New(db, blobs)
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Run(ctx, Params{
		FlowID:  "flow_curl",
		Profile: "curl_impersonate",
		ProfileOptions: map[string]string{
			"binary": fake,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Run.OK || result.Profile != "curl_impersonate" || result.Run.Status != http.StatusAccepted {
		t.Fatalf("unexpected curl result: %#v", result)
	}
}

func TestReplayBrowserCDPProfile(t *testing.T) {
	upgrader := websocket.Upgrader{}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/json/version":
			wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/devtools/browser/test"
			_, _ = fmt.Fprintf(w, `{"webSocketDebuggerUrl":%q}`, wsURL)
		case "/devtools/browser/test":
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			var request map[string]any
			if err := conn.ReadJSON(&request); err != nil {
				t.Fatal(err)
			}
			response := map[string]any{
				"id": request["id"],
				"result": map[string]any{
					"result": map[string]any{
						"value": map[string]any{
							"status":  float64(203),
							"headers": map[string]any{"content-type": "text/plain"},
							"body":    "browser-ok",
						},
					},
				},
			}
			if err := conn.WriteJSON(response); err != nil {
				t.Fatal(err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	db, blobs := testDeps(t)
	ctx := context.Background()
	if err := db.EnsureSession(ctx, store.Session{ID: "session_1", Name: "Capture", Kind: "capture", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertFlow(ctx, store.Flow{
		ID:              "flow_browser",
		SessionID:       "session_1",
		Method:          "GET",
		URL:             "https://example.test/browser",
		Host:            "example.test",
		Status:          200,
		RequestHeaders:  []byte(`{}`),
		ResponseHeaders: []byte(`{}`),
		CreatedAt:       time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	engine, err := New(db, blobs)
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Run(ctx, Params{
		FlowID:  "flow_browser",
		Profile: "browser",
		ProfileOptions: map[string]string{
			"cdp_url": server.URL,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Run.OK || result.Profile != "browser" || result.Run.Status != 203 {
		t.Fatalf("unexpected browser result: %#v", result)
	}
}

func TestReplayPassthroughProfile(t *testing.T) {
	db, blobs := testDeps(t)
	ctx := context.Background()
	if err := db.EnsureSession(ctx, store.Session{ID: "session_1", Name: "Capture", Kind: "capture", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertFlow(ctx, store.Flow{
		ID:              "flow_passthrough",
		SessionID:       "session_1",
		Method:          "GET",
		URL:             "https://pinned.example.test",
		Host:            "pinned.example.test",
		Status:          200,
		RequestHeaders:  []byte(`{}`),
		ResponseHeaders: []byte(`{}`),
		CreatedAt:       time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	engine, err := New(db, blobs)
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Run(ctx, Params{FlowID: "flow_passthrough", Profile: "passthrough"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Run.OK || result.Profile != "passthrough" || result.Run.Status != http.StatusNoContent {
		t.Fatalf("unexpected passthrough result: %#v", result)
	}
}

func testDeps(t *testing.T) (*store.Store, *blobstore.Store) {
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
	return db, blobs
}

package replay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/dhiyaan/deconstruct/daemon/internal/blobstore"
	"github.com/dhiyaan/deconstruct/daemon/internal/store"
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
	if flow.Source != "replay" || flow.Status != http.StatusCreated {
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

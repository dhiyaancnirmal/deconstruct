package workflow

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dhiyaan/deconstruct/daemon/internal/blobstore"
	"github.com/dhiyaan/deconstruct/daemon/internal/store"
)

func TestSuggestAndRunWorkflow(t *testing.T) {
	var received string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"created"}`))
	}))
	defer target.Close()

	db, blobs := testDeps(t)
	ctx := context.Background()
	if err := db.EnsureSession(ctx, store.Session{ID: "session_1", Name: "Capture", Kind: "capture", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	bodyHash, err := blobs.Put([]byte(`{"name":"old","token":"secret"}`))
	if err != nil {
		t.Fatal(err)
	}
	headers, _ := json.Marshal(http.Header{"Content-Type": []string{"application/json"}})
	if err := db.InsertFlow(ctx, store.Flow{
		ID:              "flow_1",
		SessionID:       "session_1",
		Method:          "POST",
		URL:             target.URL + "/items",
		Host:            "127.0.0.1",
		Status:          http.StatusCreated,
		RequestHeaders:  headers,
		ResponseHeaders: []byte(`{}`),
		RequestBlob:     bodyHash,
		CreatedAt:       time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	builder := NewBuilder(db, blobs)
	draft, err := builder.Suggest(ctx, SuggestParams{Name: "Create item", FlowIDs: []string{"flow_1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(draft.Steps) != 1 || draft.Steps[0].Class != "primary_action" || !draft.Steps[0].Included {
		t.Fatalf("unexpected draft: %#v", draft)
	}
	if !strings.Contains(string(draft.InputSchema), `"name"`) {
		t.Fatalf("expected name input schema: %s", draft.InputSchema)
	}
	saved, err := builder.Save(ctx, draft)
	if err != nil {
		t.Fatal(err)
	}
	run, err := builder.Run(ctx, saved, RunParams{WorkflowID: saved.ID, Inputs: map[string]any{"name": "new"}})
	if err != nil {
		t.Fatal(err)
	}
	if !run.OK || len(run.StepRuns) != 1 || run.StepRuns[0].Status != http.StatusCreated {
		t.Fatalf("unexpected run: %#v", run)
	}
	if !strings.Contains(received, `"name":"new"`) {
		t.Fatalf("expected input binding in replay body, got %s", received)
	}
}

func TestSuggestExcludesStaticAsset(t *testing.T) {
	db, blobs := testDeps(t)
	ctx := context.Background()
	if err := db.EnsureSession(ctx, store.Session{ID: "session_1", Name: "Capture", Kind: "capture", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertFlow(ctx, store.Flow{
		ID:              "flow_static",
		SessionID:       "session_1",
		Method:          "GET",
		URL:             "https://example.test/app.js",
		Host:            "example.test",
		Status:          http.StatusOK,
		RequestHeaders:  []byte(`{}`),
		ResponseHeaders: []byte(`{}`),
		CreatedAt:       time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	draft, err := NewBuilder(db, blobs).Suggest(ctx, SuggestParams{FlowIDs: []string{"flow_static"}})
	if err != nil {
		t.Fatal(err)
	}
	if draft.Steps[0].Included || draft.Steps[0].Class != "static_asset" {
		t.Fatalf("expected static exclusion: %#v", draft.Steps[0])
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

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
	"github.com/dhiyaan/deconstruct/daemon/internal/replay"
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

func TestRunUsesStepReplayProfile(t *testing.T) {
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
		Status:          http.StatusOK,
		RequestHeaders:  []byte(`{}`),
		ResponseHeaders: []byte(`{}`),
		CreatedAt:       time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	workflow := store.Workflow{
		ID:   "workflow_passthrough",
		Name: "Pinned validation",
		Steps: []store.WorkflowStep{{
			ID:            "step_1",
			FlowID:        "flow_passthrough",
			Included:      true,
			ReplayProfile: "passthrough",
			Assertions:    store.WorkflowAssertions{Status: []int{http.StatusNoContent}},
		}},
	}
	if err := db.SaveWorkflow(ctx, workflow); err != nil {
		t.Fatal(err)
	}
	run, err := NewBuilder(db, blobs).Run(ctx, workflow, RunParams{WorkflowID: workflow.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !run.OK || len(run.StepRuns) != 1 || run.StepRuns[0].Status != http.StatusNoContent {
		t.Fatalf("unexpected passthrough run: %#v", run)
	}
}

func TestRunRefreshesAuthAndRetriesFailedStep(t *testing.T) {
	refreshed := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/refresh":
			refreshed = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"access_token":"fresh"}`))
		case "/private":
			if !refreshed {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer target.Close()
	db, blobs := testDeps(t)
	ctx := context.Background()
	if err := db.EnsureSession(ctx, store.Session{ID: "session_1", Name: "Capture", Kind: "capture", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	for _, flow := range []store.Flow{
		{
			ID:              "flow_private",
			SessionID:       "session_1",
			Method:          "POST",
			URL:             target.URL + "/private",
			Host:            "127.0.0.1",
			Status:          http.StatusNoContent,
			RequestHeaders:  []byte(`{}`),
			ResponseHeaders: []byte(`{}`),
			CreatedAt:       time.Now().UTC(),
		},
		{
			ID:              "flow_refresh",
			SessionID:       "session_1",
			Method:          "POST",
			URL:             target.URL + "/refresh",
			Host:            "127.0.0.1",
			Status:          http.StatusOK,
			RequestHeaders:  []byte(`{}`),
			ResponseHeaders: []byte(`{}`),
			CreatedAt:       time.Now().UTC(),
		},
	} {
		if err := db.InsertFlow(ctx, flow); err != nil {
			t.Fatal(err)
		}
	}
	workflow := store.Workflow{
		ID:   "workflow_private",
		Name: "Private action",
		Steps: []store.WorkflowStep{{
			ID:         "step_private",
			FlowID:     "flow_private",
			Included:   true,
			Assertions: store.WorkflowAssertions{Status: []int{http.StatusNoContent}},
		}},
	}
	if err := db.SaveWorkflow(ctx, workflow); err != nil {
		t.Fatal(err)
	}
	run, err := NewBuilder(db, blobs).Run(ctx, workflow, RunParams{WorkflowID: workflow.ID, AuthRefreshFlowID: "flow_refresh"})
	if err != nil {
		t.Fatal(err)
	}
	if !run.OK || len(run.StepRuns) != 3 || run.StepRuns[0].Status != http.StatusUnauthorized || run.StepRuns[1].StepID != "auth_refresh" || run.StepRuns[2].Status != http.StatusNoContent {
		t.Fatalf("unexpected refreshed workflow run: %#v", run)
	}
}

func TestRunExtractsOutputsAndAppliesStepBindings(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/csrf":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"token":"abc123"}`))
		case "/action":
			if r.Header.Get("X-CSRF") != "abc123" {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"ok":false}`))
				return
			}
			w.Header().Set("X-Result", "created")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"ok":true,"id":"item_1"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer target.Close()

	db, blobs := testDeps(t)
	ctx := context.Background()
	if err := db.EnsureSession(ctx, store.Session{ID: "session_1", Name: "Capture", Kind: "capture", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	for _, flow := range []store.Flow{
		{
			ID:              "flow_csrf",
			SessionID:       "session_1",
			Method:          "GET",
			URL:             target.URL + "/csrf",
			Host:            "127.0.0.1",
			Status:          http.StatusOK,
			RequestHeaders:  []byte(`{}`),
			ResponseHeaders: []byte(`{}`),
			CreatedAt:       time.Now().UTC(),
		},
		{
			ID:              "flow_action",
			SessionID:       "session_1",
			Method:          "POST",
			URL:             target.URL + "/action",
			Host:            "127.0.0.1",
			Status:          http.StatusCreated,
			RequestHeaders:  []byte(`{}`),
			ResponseHeaders: []byte(`{}`),
			CreatedAt:       time.Now().UTC(),
		},
	} {
		if err := db.InsertFlow(ctx, flow); err != nil {
			t.Fatal(err)
		}
	}
	overrides, err := json.Marshal(replay.Overrides{
		Headers: map[string][]string{"X-CSRF": {"${steps.step_csrf.csrf}"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	workflow := store.Workflow{
		ID:   "workflow_bound",
		Name: "Bound action",
		Steps: []store.WorkflowStep{
			{
				ID:         "step_csrf",
				FlowID:     "flow_csrf",
				Included:   true,
				Assertions: store.WorkflowAssertions{Status: []int{http.StatusOK}, JSONPath: []string{"$.token"}},
				Extract:    map[string]string{"csrf": "$.token"},
			},
			{
				ID:         "step_action",
				FlowID:     "flow_action",
				Included:   true,
				Overrides:  overrides,
				Assertions: store.WorkflowAssertions{Status: []int{http.StatusCreated}, JSONPath: []string{"$.ok"}, Header: []string{"X-Result"}, BodyContains: []string{"item_1"}},
			},
		},
	}
	if err := db.SaveWorkflow(ctx, workflow); err != nil {
		t.Fatal(err)
	}
	run, err := NewBuilder(db, blobs).Run(ctx, workflow, RunParams{WorkflowID: workflow.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !run.OK || len(run.StepRuns) != 2 || run.StepRuns[1].Status != http.StatusCreated {
		t.Fatalf("unexpected bound workflow run: %#v", run)
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

package auth

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

func TestAnalyzerTracesAuthChain(t *testing.T) {
	db, blobs := authTestStore(t)
	ctx := context.Background()
	if err := db.EnsureSession(ctx, store.Session{ID: "session_1", Name: "Auth", Kind: "capture", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	insertAuthFlow(t, ctx, db, blobs, store.Flow{
		ID:              "flow_bootstrap",
		SessionID:       "session_1",
		Method:          "GET",
		URL:             "https://app.example.test/form",
		Host:            "app.example.test",
		Status:          http.StatusOK,
		RequestHeaders:  mustHeaders(t, http.Header{}),
		ResponseHeaders: mustHeaders(t, http.Header{"Set-Cookie": {"sid=abc; Path=/; HttpOnly"}, "X-CSRF-Token": {"csrf123"}}),
		ResponseBlob:    mustBlob(t, blobs, []byte(`{"csrf_token":"csrf123"}`)),
		CreatedAt:       time.Now().Add(-3 * time.Second).UTC(),
	})
	insertAuthFlow(t, ctx, db, blobs, store.Flow{
		ID:              "flow_action",
		SessionID:       "session_1",
		Method:          "POST",
		URL:             "https://app.example.test/items",
		Host:            "app.example.test",
		Status:          http.StatusCreated,
		RequestHeaders:  mustHeaders(t, http.Header{"Cookie": {"sid=abc"}, "X-CSRF-Token": {"csrf123"}, "Content-Type": {"application/json"}}),
		ResponseHeaders: mustHeaders(t, http.Header{}),
		RequestBlob:     mustBlob(t, blobs, []byte(`{"name":"one"}`)),
		CreatedAt:       time.Now().Add(-2 * time.Second).UTC(),
	})
	insertAuthFlow(t, ctx, db, blobs, store.Flow{
		ID:              "flow_refresh",
		SessionID:       "session_1",
		Method:          "POST",
		URL:             "https://app.example.test/oauth/token",
		Host:            "app.example.test",
		Status:          http.StatusOK,
		RequestHeaders:  mustHeaders(t, http.Header{"Content-Type": {"application/json"}}),
		ResponseHeaders: mustHeaders(t, http.Header{"Content-Type": {"application/json"}}),
		ResponseBlob:    mustBlob(t, blobs, []byte(`{"access_token":"new-access","refresh_token":"new-refresh"}`)),
		CreatedAt:       time.Now().Add(-time.Second).UTC(),
	})
	insertAuthFlow(t, ctx, db, blobs, store.Flow{
		ID:              "flow_expired",
		SessionID:       "session_1",
		Method:          "GET",
		URL:             "https://app.example.test/api/private",
		Host:            "app.example.test",
		Status:          http.StatusUnauthorized,
		RequestHeaders:  mustHeaders(t, http.Header{}),
		ResponseHeaders: mustHeaders(t, http.Header{}),
		CreatedAt:       time.Now().UTC(),
	})

	chain, err := NewAnalyzer(db, blobs).Trace(ctx, TraceParams{SessionID: "session_1", Host: "app.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if chain.ID == "" || len(chain.FlowIDs) != 4 {
		t.Fatalf("unexpected chain: %#v", chain)
	}
	if len(chain.Artifacts) < 3 {
		t.Fatalf("expected cookie and token artifacts: %#v", chain.Artifacts)
	}
	if len(chain.CSRFLinks) != 1 || chain.CSRFLinks[0].SourceFlowID != "flow_bootstrap" || chain.CSRFLinks[0].UsedByFlowID != "flow_action" {
		t.Fatalf("expected csrf lineage, got %#v", chain.CSRFLinks)
	}
	if len(chain.RefreshEndpoints) != 1 || chain.RefreshEndpoints[0].FlowID != "flow_refresh" || chain.RefreshEndpoints[0].Confidence != "high" {
		t.Fatalf("expected refresh endpoint, got %#v", chain.RefreshEndpoints)
	}
	if len(chain.Failures) != 1 || chain.Failures[0].Type != "unauthenticated" {
		t.Fatalf("expected auth failure, got %#v", chain.Failures)
	}
	saved, err := db.GetAuthChain(ctx, chain.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID != chain.ID {
		t.Fatalf("chain was not saved: %#v", saved)
	}
}

func TestWarmingRunnerRunsDueProfile(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "sid=fresh" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	db, blobs := authTestStore(t)
	ctx := context.Background()
	if err := db.EnsureSession(ctx, store.Session{ID: "session_1", Name: "Capture", Kind: "capture", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	body := mustBlob(t, blobs, nil)
	if err := db.InsertFlow(ctx, store.Flow{
		ID:              "flow_warm",
		SessionID:       "session_1",
		Method:          "GET",
		URL:             target.URL + "/warm",
		Host:            "127.0.0.1",
		Status:          http.StatusNoContent,
		RequestHeaders:  mustHeaders(t, http.Header{"Cookie": {"sid=stale"}}),
		ResponseHeaders: mustHeaders(t, http.Header{}),
		RequestBlob:     body,
		ResponseBlob:    body,
		CreatedAt:       time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	workflow := store.Workflow{
		ID:   "workflow_warm",
		Name: "Warm session",
		Steps: []store.WorkflowStep{{
			ID:         "step_1",
			FlowID:     "flow_warm",
			Name:       "Warm",
			Included:   true,
			Assertions: store.WorkflowAssertions{Status: []int{http.StatusNoContent}},
		}},
	}
	if err := db.SaveWorkflow(ctx, workflow); err != nil {
		t.Fatal(err)
	}
	profile := store.AuthProfile{
		ID:      "auth_profile_warm",
		Name:    "Warm",
		Host:    "127.0.0.1",
		ChainID: "auth_chain_1",
		CookieWarming: &store.CookieWarmingSchedule{
			Enabled:         true,
			IntervalSeconds: 60,
			WorkflowID:      workflow.ID,
		},
	}
	if err := db.SaveAuthProfile(ctx, profile); err != nil {
		t.Fatal(err)
	}
	vault, err := NewVault(db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := vault.Save(ctx, profile.ID, map[string]string{"cookie.sid": "fresh"})
	if err != nil {
		t.Fatal(err)
	}
	profile.SecretBundleID = bundle.ID
	if err := db.SaveAuthProfile(ctx, profile); err != nil {
		t.Fatal(err)
	}
	runner := NewWarmingRunner(db, blobs, vault)
	results, err := runner.RunDue(ctx, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Ran || results[0].Run == nil || !results[0].Run.OK {
		t.Fatalf("unexpected warming result: %#v", results)
	}
	due, err := runner.DueProfiles(ctx, time.Now().Add(10*time.Second).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Fatalf("profile should not be due yet: %#v", due)
	}
}

func TestVaultEncryptsSecretBundles(t *testing.T) {
	db, _ := authTestStore(t)
	ctx := context.Background()
	profile := store.AuthProfile{ID: "auth_profile_1", Name: "App", Host: "app.example.test"}
	if err := db.SaveAuthProfile(ctx, profile); err != nil {
		t.Fatal(err)
	}
	vault, err := NewVault(db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := vault.Save(ctx, profile.ID, map[string]string{"sid": "abc"})
	if err != nil {
		t.Fatal(err)
	}
	if string(bundle.Ciphertext) == `{"sid":"abc"}` {
		t.Fatal("secret bundle was stored as plaintext")
	}
	opened, err := vault.Open(ctx, bundle.ID)
	if err != nil {
		t.Fatal(err)
	}
	if opened["sid"] != "abc" {
		t.Fatalf("unexpected opened bundle: %#v", opened)
	}
}

func authTestStore(t *testing.T) (*store.Store, *blobstore.Store) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	blobs, err := blobstore.Open(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	return db, blobs
}

func insertAuthFlow(t *testing.T, ctx context.Context, db *store.Store, blobs *blobstore.Store, flow store.Flow) {
	t.Helper()
	if flow.RequestBlob == "" {
		flow.RequestBlob = mustBlob(t, blobs, nil)
	}
	if flow.ResponseBlob == "" {
		flow.ResponseBlob = mustBlob(t, blobs, nil)
	}
	if err := db.InsertFlow(ctx, flow); err != nil {
		t.Fatal(err)
	}
}

func mustHeaders(t *testing.T, headers http.Header) []byte {
	t.Helper()
	data, err := json.Marshal(headers)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mustBlob(t *testing.T, blobs *blobstore.Store, body []byte) string {
	t.Helper()
	hash, err := blobs.Put(body)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreSessionAndFlow(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	session := Session{
		ID:        "session_1",
		Name:      "Proxy capture",
		Kind:      "capture",
		CreatedAt: time.Now().UTC(),
	}
	if err := db.EnsureSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertFlow(ctx, Flow{
		ID:              "flow_1",
		SessionID:       session.ID,
		Method:          "GET",
		URL:             "http://example.test/path",
		Host:            "example.test",
		Status:          200,
		Duration:        10 * time.Millisecond,
		RequestHeaders:  []byte(`{"accept":["*/*"]}`),
		ResponseHeaders: []byte(`{"content-type":["text/plain"]}`),
	}); err != nil {
		t.Fatal(err)
	}

	sessions, err := db.ListSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ID != session.ID {
		t.Fatalf("unexpected sessions: %#v", sessions)
	}
	count, err := db.CountFlows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1 flow, got %d", count)
	}
	flows, err := db.ListFlows(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(flows) != 1 || flows[0].ID != "flow_1" {
		t.Fatalf("unexpected flows: %#v", flows)
	}
	flow, err := db.GetFlow(ctx, "flow_1")
	if err != nil {
		t.Fatal(err)
	}
	if flow.URL != "http://example.test/path" {
		t.Fatalf("unexpected flow URL: %s", flow.URL)
	}
	run := ReplayRun{
		ID:             "replay_1",
		OriginalFlowID: "flow_1",
		ReplayFlowID:   "flow_1",
		OK:             true,
		Status:         200,
		Attempts:       1,
	}
	if err := db.InsertReplayRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	runs, err := db.ListReplayRuns(ctx, "flow_1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || !runs[0].OK {
		t.Fatalf("unexpected replay runs: %#v", runs)
	}
	loadedRun, err := db.GetReplayRun(ctx, "replay_1")
	if err != nil {
		t.Fatal(err)
	}
	if loadedRun.ReplayFlowID != "flow_1" {
		t.Fatalf("unexpected replay run: %#v", loadedRun)
	}
	item := Workflow{
		ID:   "workflow_1",
		Name: "Create item",
		Steps: []WorkflowStep{{
			ID:       "step_1",
			FlowID:   "flow_1",
			Name:     "GET http://example.test/path",
			Class:    "primary_action",
			Included: true,
		}},
	}
	if err := db.SaveWorkflow(ctx, item); err != nil {
		t.Fatal(err)
	}
	loadedWorkflow, err := db.GetWorkflow(ctx, "workflow_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(loadedWorkflow.Steps) != 1 || loadedWorkflow.Steps[0].FlowID != "flow_1" {
		t.Fatalf("unexpected workflow: %#v", loadedWorkflow)
	}
	if err := db.InsertWorkflowRun(ctx, WorkflowRun{
		ID:         "workflow_run_1",
		WorkflowID: "workflow_1",
		OK:         true,
		StepRuns:   []WorkflowStepRun{{StepID: "step_1", FlowID: "flow_1", OK: true, Status: 200}},
	}); err != nil {
		t.Fatal(err)
	}
	workflowRuns, err := db.ListWorkflowRuns(ctx, "workflow_1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(workflowRuns) != 1 || !workflowRuns[0].OK {
		t.Fatalf("unexpected workflow runs: %#v", workflowRuns)
	}
	chain := AuthChain{
		ID:        "auth_chain_1",
		SessionID: "session_1",
		Host:      "example.test",
		FlowIDs:   []string{"flow_1"},
		Artifacts: []AuthArtifact{{
			ID:            "auth_artifact_1",
			Type:          "cookie",
			Name:          "sid",
			ValueHash:     "hash",
			SourceFlowID:  "flow_1",
			UsedByFlowIDs: []string{"flow_1"},
		}},
	}
	if err := db.SaveAuthChain(ctx, chain); err != nil {
		t.Fatal(err)
	}
	loadedChain, err := db.GetAuthChain(ctx, "auth_chain_1")
	if err != nil {
		t.Fatal(err)
	}
	if loadedChain.Host != "example.test" || len(loadedChain.Artifacts) != 1 {
		t.Fatalf("unexpected auth chain: %#v", loadedChain)
	}
	chains, err := db.ListAuthChains(ctx, "session_1", "example.test", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(chains) != 1 {
		t.Fatalf("unexpected auth chains: %#v", chains)
	}
	profile := AuthProfile{
		ID:      "auth_profile_1",
		Name:    "Example",
		Host:    "example.test",
		ChainID: "auth_chain_1",
		CookieWarming: &CookieWarmingSchedule{
			Enabled:         true,
			IntervalSeconds: 300,
			WorkflowID:      "workflow_1",
		},
	}
	if err := db.SaveAuthProfile(ctx, profile); err != nil {
		t.Fatal(err)
	}
	loadedProfile, err := db.GetAuthProfile(ctx, "auth_profile_1")
	if err != nil {
		t.Fatal(err)
	}
	if loadedProfile.CookieWarming == nil || loadedProfile.CookieWarming.IntervalSeconds != 300 {
		t.Fatalf("unexpected auth profile: %#v", loadedProfile)
	}
	if err := db.SaveAuthSecretBundle(ctx, AuthSecretBundle{
		ID:         "auth_secret_1",
		ProfileID:  "auth_profile_1",
		Nonce:      []byte("nonce"),
		Ciphertext: []byte("ciphertext"),
	}); err != nil {
		t.Fatal(err)
	}
	bundle, err := db.GetAuthSecretBundle(ctx, "auth_secret_1")
	if err != nil {
		t.Fatal(err)
	}
	if string(bundle.Ciphertext) != "ciphertext" {
		t.Fatalf("unexpected auth secret bundle: %#v", bundle)
	}
	if err := db.SaveExportArtifact(ctx, ExportArtifact{
		ID:         "export_1",
		WorkflowID: "workflow_1",
		Kind:       "openapi",
		FilesJSON:  []byte(`[{"path":"openapi.json"}]`),
	}); err != nil {
		t.Fatal(err)
	}
	exports, err := db.ListExportArtifacts(ctx, "workflow_1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(exports) != 1 || exports[0].Kind != "openapi" {
		t.Fatalf("unexpected exports: %#v", exports)
	}
}

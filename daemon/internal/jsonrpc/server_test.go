package jsonrpc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dhiyaan/deconstruct/daemon/internal/auth"
	"github.com/dhiyaan/deconstruct/daemon/internal/blobstore"
	"github.com/dhiyaan/deconstruct/daemon/internal/browsercapture"
	"github.com/dhiyaan/deconstruct/daemon/internal/certs"
	"github.com/dhiyaan/deconstruct/daemon/internal/diagnostics"
	"github.com/dhiyaan/deconstruct/daemon/internal/exporter"
	"github.com/dhiyaan/deconstruct/daemon/internal/harimport"
	"github.com/dhiyaan/deconstruct/daemon/internal/mobile"
	"github.com/dhiyaan/deconstruct/daemon/internal/replay"
	"github.com/dhiyaan/deconstruct/daemon/internal/store"
	"github.com/dhiyaan/deconstruct/daemon/internal/systemproxy"
	"github.com/dhiyaan/deconstruct/daemon/internal/truststore"
	workflowbuilder "github.com/dhiyaan/deconstruct/daemon/internal/workflow"
)

func TestStatus(t *testing.T) {
	db := testStore(t)
	server := NewServer(Dependencies{
		Store:      db,
		Version:    "test",
		DataDir:    "/tmp/deconstruct-test",
		ProxyAddr:  "127.0.0.1:18080",
		SocketPath: "/tmp/deconstruct-test.sock",
	})

	result, rpcErr := server.handle(context.Background(), request{
		JSONRPC: "2.0",
		Method:  "daemon.status",
	})
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %#v", rpcErr)
	}
	status, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected status type: %#v", result)
	}
	if status["name"] != "deconstructd" {
		t.Fatalf("unexpected name: %#v", status["name"])
	}
}

func TestFlowsList(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	if err := db.EnsureSession(ctx, store.Session{
		ID:        "session_1",
		Name:      "Test",
		Kind:      "capture",
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertFlow(ctx, store.Flow{
		ID:              "flow_1",
		SessionID:       "session_1",
		Method:          "GET",
		URL:             "http://example.test",
		Host:            "example.test",
		Status:          200,
		RequestHeaders:  []byte(`{}`),
		ResponseHeaders: []byte(`{}`),
		CreatedAt:       time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	params, err := json.Marshal(flowsListParams{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(Dependencies{Store: db})
	result, rpcErr := server.handle(ctx, request{
		JSONRPC: "2.0",
		Method:  "flows.list",
		Params:  params,
	})
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %#v", rpcErr)
	}
	flows, ok := result.([]store.FlowSummary)
	if !ok {
		t.Fatalf("unexpected flows type: %#v", result)
	}
	if len(flows) != 1 || flows[0].ID != "flow_1" {
		t.Fatalf("unexpected flows: %#v", flows)
	}
}

func TestInspectorQueryTagsHostsFiltersAndCompare(t *testing.T) {
	db := testStore(t)
	blobs, err := blobstore.Open(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := db.EnsureSession(ctx, store.Session{
		ID:        "session_1",
		Name:      "Test",
		Kind:      "capture",
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	body1, err := blobs.Put([]byte(`{"name":"one"}`))
	if err != nil {
		t.Fatal(err)
	}
	body2, err := blobs.Put([]byte(`{"name":"two"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, flow := range []store.Flow{
		{
			ID:              "flow_1",
			SessionID:       "session_1",
			Method:          "GET",
			URL:             "https://api.example.test/items?kind=old",
			Host:            "api.example.test",
			Status:          200,
			RequestHeaders:  []byte(`{"Cookie":["sid=abc"],"Accept":["application/json"]}`),
			ResponseHeaders: []byte(`{"Set-Cookie":["seen=1"],"Content-Type":["application/json"]}`),
			RequestBlob:     body1,
			ResponseBlob:    body1,
			CreatedAt:       time.Now().Add(-time.Second).UTC(),
		},
		{
			ID:              "flow_2",
			SessionID:       "session_1",
			Method:          "POST",
			AppName:         "Demo",
			URL:             "https://api.example.test/items?kind=new",
			Host:            "api.example.test",
			Status:          201,
			RequestHeaders:  []byte(`{"Content-Type":["application/json"]}`),
			ResponseHeaders: []byte(`{"Content-Type":["application/json"]}`),
			RequestBlob:     body2,
			ResponseBlob:    body2,
			CreatedAt:       time.Now().UTC(),
		},
	} {
		if err := db.InsertFlow(ctx, flow); err != nil {
			t.Fatal(err)
		}
	}
	server := NewServer(Dependencies{Store: db, Blobs: blobs})

	tagParams, err := json.Marshal(tagParams{FlowID: "flow_2", Tag: "Action"})
	if err != nil {
		t.Fatal(err)
	}
	if _, rpcErr := server.handle(ctx, request{JSONRPC: "2.0", Method: "tags.add", Params: tagParams}); rpcErr != nil {
		t.Fatalf("unexpected tag error: %#v", rpcErr)
	}

	status := 201
	queryParams, err := json.Marshal(store.FlowQuery{Status: &status, Tag: "action", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	result, rpcErr := server.handle(ctx, request{JSONRPC: "2.0", Method: "flows.query", Params: queryParams})
	if rpcErr != nil {
		t.Fatalf("unexpected query error: %#v", rpcErr)
	}
	queryResult, ok := result.(store.FlowQueryResult)
	if !ok || queryResult.Total != 1 || queryResult.Flows[0].ID != "flow_2" {
		t.Fatalf("unexpected query result: %#v", result)
	}

	result, rpcErr = server.handle(ctx, request{JSONRPC: "2.0", Method: "hosts.list", Params: []byte(`{"limit":10}`)})
	if rpcErr != nil {
		t.Fatalf("unexpected hosts error: %#v", rpcErr)
	}
	hosts, ok := result.([]store.HostCount)
	if !ok || len(hosts) != 1 || hosts[0].Count != 2 {
		t.Fatalf("unexpected hosts: %#v", result)
	}

	result, rpcErr = server.handle(ctx, request{JSONRPC: "2.0", Method: "apps.list", Params: []byte(`{"limit":10}`)})
	if rpcErr != nil {
		t.Fatalf("unexpected apps error: %#v", rpcErr)
	}
	apps, ok := result.([]store.AppCount)
	if !ok || len(apps) == 0 {
		t.Fatalf("unexpected apps: %#v", result)
	}

	filterParams, err := json.Marshal(savedFilterParams{Name: "Actions", Query: queryParams})
	if err != nil {
		t.Fatal(err)
	}
	result, rpcErr = server.handle(ctx, request{JSONRPC: "2.0", Method: "filters.save", Params: filterParams})
	if rpcErr != nil {
		t.Fatalf("unexpected filter save error: %#v", rpcErr)
	}
	filterID := result.(map[string]string)["id"]
	result, rpcErr = server.handle(ctx, request{JSONRPC: "2.0", Method: "filters.list"})
	if rpcErr != nil {
		t.Fatalf("unexpected filters list error: %#v", rpcErr)
	}
	filters, ok := result.([]store.SavedFilter)
	if !ok || len(filters) != 1 || filters[0].Name != "Actions" {
		t.Fatalf("unexpected filters: %#v", result)
	}

	compareParams, err := json.Marshal(compareParams{IDs: []string{"flow_1", "flow_2"}})
	if err != nil {
		t.Fatal(err)
	}
	result, rpcErr = server.handle(ctx, request{JSONRPC: "2.0", Method: "flows.compare", Params: compareParams})
	if rpcErr != nil {
		t.Fatalf("unexpected compare error: %#v", rpcErr)
	}
	comparison, ok := result.(flowComparison)
	if !ok || len(comparison.Differences) == 0 {
		t.Fatalf("unexpected comparison: %#v", result)
	}

	deleteParams, err := json.Marshal(struct {
		ID string `json:"id"`
	}{ID: filterID})
	if err != nil {
		t.Fatal(err)
	}
	if _, rpcErr = server.handle(ctx, request{JSONRPC: "2.0", Method: "filters.delete", Params: deleteParams}); rpcErr != nil {
		t.Fatalf("unexpected filter delete error: %#v", rpcErr)
	}
}

func TestFlowExportCurl(t *testing.T) {
	db := testStore(t)
	blobs, err := blobstore.Open(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := db.EnsureSession(ctx, store.Session{
		ID:        "session_1",
		Name:      "Test",
		Kind:      "capture",
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	bodyHash, err := blobs.Put([]byte(`{"ok":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InsertFlow(ctx, store.Flow{
		ID:              "flow_1",
		SessionID:       "session_1",
		Method:          "POST",
		URL:             "https://example.test/api",
		Host:            "example.test",
		Status:          200,
		RequestHeaders:  []byte(`{"Content-Type":["application/json"]}`),
		ResponseHeaders: []byte(`{}`),
		RequestBlob:     bodyHash,
		CreatedAt:       time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	params, err := json.Marshal(flowIDParams{ID: "flow_1"})
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(Dependencies{Store: db, Blobs: blobs})
	result, rpcErr := server.handle(ctx, request{
		JSONRPC: "2.0",
		Method:  "flows.export_curl",
		Params:  params,
	})
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %#v", rpcErr)
	}
	payload, ok := result.(map[string]string)
	if !ok {
		t.Fatalf("unexpected result: %#v", result)
	}
	if payload["command"] == "" {
		t.Fatal("expected curl command")
	}
}

func TestFlowReadParsesJSONBodies(t *testing.T) {
	db := testStore(t)
	blobs, err := blobstore.Open(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := db.EnsureSession(ctx, store.Session{
		ID:        "session_1",
		Name:      "Test",
		Kind:      "capture",
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	bodyHash, err := blobs.Put([]byte(`{"message":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InsertFlow(ctx, store.Flow{
		ID:              "flow_1",
		SessionID:       "session_1",
		Method:          "POST",
		URL:             "https://example.test/api",
		Host:            "example.test",
		Status:          200,
		RequestHeaders:  []byte(`{"Authorization":["Bearer secret"],"Content-Type":["application/json"]}`),
		ResponseHeaders: []byte(`{}`),
		RequestBlob:     bodyHash,
		ResponseBlob:    bodyHash,
		CreatedAt:       time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	params, err := json.Marshal(flowIDParams{ID: "flow_1"})
	if err != nil {
		t.Fatal(err)
	}
	result, rpcErr := NewServer(Dependencies{Store: db, Blobs: blobs}).handle(ctx, request{
		JSONRPC: "2.0",
		Method:  "flows.read",
		Params:  params,
	})
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %#v", rpcErr)
	}
	detail, ok := result.(flowDetail)
	if !ok {
		t.Fatalf("unexpected detail: %#v", result)
	}
	parsed, ok := detail.RequestJSON.(map[string]any)
	if !ok || parsed["message"] != "hello" {
		t.Fatalf("unexpected parsed json: %#v", detail.RequestJSON)
	}
	if detail.RequestHeaders.Get("Authorization") != "[redacted]" {
		t.Fatalf("expected redacted authorization header, got %q", detail.RequestHeaders.Get("Authorization"))
	}
}

func TestHARImportRPC(t *testing.T) {
	db := testStore(t)
	blobs, err := blobstore.Open(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	har := json.RawMessage(`{
	  "log": {
	    "entries": [
	      {
	        "startedDateTime": "2026-04-27T10:00:00Z",
	        "time": 10,
	        "request": {
	          "method": "POST",
	          "url": "https://api.example.test/graphql",
	          "headers": [{"name":"Content-Type","value":"application/json"}],
	          "postData": {"mimeType":"application/json","text":"{\"query\":\"query Me { me { id } }\"}"}
	        },
	        "response": {
	          "status": 200,
	          "headers": [{"name":"Content-Type","value":"application/json"}],
	          "content": {"mimeType":"application/json","text":"{\"data\":{\"me\":{\"id\":\"1\"}}}"}
	        }
	      }
	    ]
	  }
	}`)
	params, err := json.Marshal(harImportParams{
		Data:    har,
		Options: harimport.Options{Name: "RPC HAR"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, rpcErr := NewServer(Dependencies{Store: db, Blobs: blobs}).handle(context.Background(), request{
		JSONRPC: "2.0",
		Method:  "har.import",
		Params:  params,
	})
	if rpcErr != nil {
		t.Fatalf("unexpected har import error: %#v", rpcErr)
	}
	importResult, ok := result.(harimport.Result)
	if !ok || importResult.Flows != 1 {
		t.Fatalf("unexpected import result: %#v", result)
	}
	query, err := db.QueryFlows(context.Background(), store.FlowQuery{Tag: "graphql", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if query.Total != 1 {
		t.Fatalf("expected graphql flow, got %#v", query)
	}
}

func TestReplayAndScriptExportRPC(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer target.Close()

	db := testStore(t)
	blobs, err := blobstore.Open(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := db.EnsureSession(ctx, store.Session{ID: "session_1", Name: "Capture", Kind: "capture", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	bodyHash, err := blobs.Put([]byte(`{"token":"secret"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InsertFlow(ctx, store.Flow{
		ID:              "flow_replay",
		SessionID:       "session_1",
		Method:          "POST",
		URL:             target.URL + "/api",
		Host:            "127.0.0.1",
		Status:          200,
		RequestHeaders:  []byte(`{"Content-Type":["application/json"],"Authorization":["Bearer secret"]}`),
		ResponseHeaders: []byte(`{}`),
		RequestBlob:     bodyHash,
		CreatedAt:       time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	server := NewServer(Dependencies{Store: db, Blobs: blobs})
	replayParams, err := json.Marshal(replay.Params{FlowID: "flow_replay"})
	if err != nil {
		t.Fatal(err)
	}
	result, rpcErr := server.handle(ctx, request{JSONRPC: "2.0", Method: "replay.run", Params: replayParams})
	if rpcErr != nil {
		t.Fatalf("unexpected replay error: %#v", rpcErr)
	}
	replayResult, ok := result.(replay.Result)
	if !ok || !replayResult.Run.OK || replayResult.Run.ReplayFlowID == "" {
		t.Fatalf("unexpected replay result: %#v", result)
	}
	result, rpcErr = server.handle(ctx, request{JSONRPC: "2.0", Method: "replay.runs.list", Params: []byte(`{"original_flow_id":"flow_replay"}`)})
	if rpcErr != nil {
		t.Fatalf("unexpected replay list error: %#v", rpcErr)
	}
	runs, ok := result.([]store.ReplayRun)
	if !ok || len(runs) != 1 {
		t.Fatalf("unexpected replay runs: %#v", result)
	}
	exportParams, err := json.Marshal(exportScriptParams{ID: "flow_replay", Language: "typescript"})
	if err != nil {
		t.Fatal(err)
	}
	result, rpcErr = server.handle(ctx, request{JSONRPC: "2.0", Method: "flows.export_script", Params: exportParams})
	if rpcErr != nil {
		t.Fatalf("unexpected script export error: %#v", rpcErr)
	}
	script := result.(map[string]string)["script"]
	if !strings.Contains(script, "[redacted]") {
		t.Fatalf("expected redacted script:\n%s", script)
	}
}

func TestWorkflowRPC(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer target.Close()

	db := testStore(t)
	blobs, err := blobstore.Open(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := db.EnsureSession(ctx, store.Session{ID: "session_1", Name: "Capture", Kind: "capture", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	bodyHash, err := blobs.Put([]byte(`{"name":"old"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InsertFlow(ctx, store.Flow{
		ID:              "flow_workflow",
		SessionID:       "session_1",
		Method:          "POST",
		URL:             target.URL + "/items",
		Host:            "127.0.0.1",
		Status:          http.StatusCreated,
		RequestHeaders:  []byte(`{"Content-Type":["application/json"]}`),
		ResponseHeaders: []byte(`{}`),
		RequestBlob:     bodyHash,
		CreatedAt:       time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	server := NewServer(Dependencies{Store: db, Blobs: blobs})
	suggestParams, err := json.Marshal(workflowbuilder.SuggestParams{Name: "Create item", FlowIDs: []string{"flow_workflow"}})
	if err != nil {
		t.Fatal(err)
	}
	result, rpcErr := server.handle(ctx, request{JSONRPC: "2.0", Method: "workflows.suggest", Params: suggestParams})
	if rpcErr != nil {
		t.Fatalf("unexpected suggest error: %#v", rpcErr)
	}
	draft, ok := result.(store.Workflow)
	if !ok || len(draft.Steps) != 1 || !draft.Steps[0].Included {
		t.Fatalf("unexpected draft: %#v", result)
	}
	saveParams, err := json.Marshal(workflowSaveParams{Workflow: draft})
	if err != nil {
		t.Fatal(err)
	}
	result, rpcErr = server.handle(ctx, request{JSONRPC: "2.0", Method: "workflows.save", Params: saveParams})
	if rpcErr != nil {
		t.Fatalf("unexpected save error: %#v", rpcErr)
	}
	saved := result.(store.Workflow)
	runParams, err := json.Marshal(workflowbuilder.RunParams{WorkflowID: saved.ID, Inputs: map[string]any{"name": "new"}})
	if err != nil {
		t.Fatal(err)
	}
	result, rpcErr = server.handle(ctx, request{JSONRPC: "2.0", Method: "workflows.run", Params: runParams})
	if rpcErr != nil {
		t.Fatalf("unexpected run error: %#v", rpcErr)
	}
	run, ok := result.(store.WorkflowRun)
	if !ok || !run.OK || len(run.StepRuns) != 1 {
		t.Fatalf("unexpected workflow run: %#v", result)
	}
	result, rpcErr = server.handle(ctx, request{JSONRPC: "2.0", Method: "workflow_runs.list", Params: []byte(`{"workflow_id":"` + saved.ID + `"}`)})
	if rpcErr != nil {
		t.Fatalf("unexpected workflow run list error: %#v", rpcErr)
	}
	runs, ok := result.([]store.WorkflowRun)
	if !ok || len(runs) != 1 {
		t.Fatalf("unexpected workflow runs: %#v", result)
	}
	result, rpcErr = server.handle(ctx, request{JSONRPC: "2.0", Method: "workflows.export_openapi", Params: []byte(`{"workflow_id":"` + saved.ID + `"}`)})
	if rpcErr != nil {
		t.Fatalf("unexpected openapi export error: %#v", rpcErr)
	}
	openapi, ok := result.(exporter.OpenAPI)
	if !ok || openapi.OpenAPI != "3.1.0" {
		t.Fatalf("unexpected openapi: %#v", result)
	}
	result, rpcErr = server.handle(ctx, request{JSONRPC: "2.0", Method: "workflows.export_project", Params: []byte(`{"workflow_id":"` + saved.ID + `","language":"typescript"}`)})
	if rpcErr != nil {
		t.Fatalf("unexpected project export error: %#v", rpcErr)
	}
	project, ok := result.(exporter.Project)
	if !ok || len(project.Files) == 0 {
		t.Fatalf("unexpected project: %#v", result)
	}
	result, rpcErr = server.handle(ctx, request{JSONRPC: "2.0", Method: "workflows.export_postman", Params: []byte(`{"workflow_id":"` + saved.ID + `"}`)})
	if rpcErr != nil {
		t.Fatalf("unexpected postman export error: %#v", rpcErr)
	}
	result, rpcErr = server.handle(ctx, request{JSONRPC: "2.0", Method: "exports.list", Params: []byte(`{"workflow_id":"` + saved.ID + `"}`)})
	if rpcErr != nil {
		t.Fatalf("unexpected exports list error: %#v", rpcErr)
	}
	artifacts, ok := result.([]store.ExportArtifact)
	if !ok || len(artifacts) < 3 {
		t.Fatalf("unexpected export artifacts: %#v", result)
	}
	if err := db.TagFlow(ctx, "flow_workflow", "sse"); err != nil {
		t.Fatal(err)
	}
	result, rpcErr = server.handle(ctx, request{JSONRPC: "2.0", Method: "fidelity.profiles.list"})
	if rpcErr != nil {
		t.Fatalf("unexpected fidelity profiles error: %#v", rpcErr)
	}
	result, rpcErr = server.handle(ctx, request{JSONRPC: "2.0", Method: "fidelity.diagnose", Params: []byte(`{"flow_id":"flow_workflow"}`)})
	if rpcErr != nil {
		t.Fatalf("unexpected fidelity diagnose error: %#v", rpcErr)
	}
}

func TestAuthRPC(t *testing.T) {
	db := testStore(t)
	blobs, err := blobstore.Open(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := db.EnsureSession(ctx, store.Session{ID: "session_auth", Name: "Auth", Kind: "capture", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	requestBody, err := blobs.Put([]byte(`{"ok":true}`))
	if err != nil {
		t.Fatal(err)
	}
	responseBody, err := blobs.Put([]byte(`{"access_token":"access","refresh_token":"refresh"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InsertFlow(ctx, store.Flow{
		ID:              "flow_auth_bootstrap",
		SessionID:       "session_auth",
		Method:          "GET",
		URL:             "https://app.example.test/form",
		Host:            "app.example.test",
		Status:          http.StatusOK,
		RequestHeaders:  []byte(`{}`),
		ResponseHeaders: []byte(`{"Set-Cookie":["sid=abc"],"X-CSRF-Token":["csrf"]}`),
		RequestBlob:     requestBody,
		ResponseBlob:    requestBody,
		CreatedAt:       time.Now().Add(-time.Second).UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertFlow(ctx, store.Flow{
		ID:              "flow_auth_refresh",
		SessionID:       "session_auth",
		Method:          "POST",
		URL:             "https://app.example.test/oauth/token",
		Host:            "app.example.test",
		Status:          http.StatusOK,
		RequestHeaders:  []byte(`{"Cookie":["sid=abc"],"X-CSRF-Token":["csrf"],"Content-Type":["application/json"]}`),
		ResponseHeaders: []byte(`{"Content-Type":["application/json"]}`),
		RequestBlob:     requestBody,
		ResponseBlob:    responseBody,
		CreatedAt:       time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	server := NewServer(Dependencies{Store: db, Blobs: blobs, DataDir: t.TempDir()})
	result, rpcErr := server.handle(ctx, request{JSONRPC: "2.0", Method: "auth.trace", Params: []byte(`{"session_id":"session_auth","host":"app.example.test"}`)})
	if rpcErr != nil {
		t.Fatalf("unexpected auth trace error: %#v", rpcErr)
	}
	chain, ok := result.(store.AuthChain)
	if !ok || len(chain.CSRFLinks) != 1 || len(chain.RefreshEndpoints) != 1 {
		t.Fatalf("unexpected auth chain: %#v", result)
	}
	result, rpcErr = server.handle(ctx, request{JSONRPC: "2.0", Method: "auth.chains.list", Params: []byte(`{"session_id":"session_auth"}`)})
	if rpcErr != nil {
		t.Fatalf("unexpected auth chain list error: %#v", rpcErr)
	}
	chains, ok := result.([]store.AuthChain)
	if !ok || len(chains) != 1 {
		t.Fatalf("unexpected chains: %#v", result)
	}
	profileParams, err := json.Marshal(authProfileSaveParams{Profile: store.AuthProfile{
		Name:    "App",
		Host:    "app.example.test",
		ChainID: chain.ID,
		CookieWarming: &store.CookieWarmingSchedule{
			Enabled:         true,
			IntervalSeconds: 300,
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	result, rpcErr = server.handle(ctx, request{JSONRPC: "2.0", Method: "auth.profiles.save", Params: profileParams})
	if rpcErr != nil {
		t.Fatalf("unexpected auth profile save error: %#v", rpcErr)
	}
	profile, ok := result.(store.AuthProfile)
	if !ok || profile.ID == "" {
		t.Fatalf("unexpected profile: %#v", result)
	}
	vaultParams, err := json.Marshal(authVaultSaveParams{ProfileID: profile.ID, Material: map[string]string{"sid": "abc"}})
	if err != nil {
		t.Fatal(err)
	}
	result, rpcErr = server.handle(ctx, request{JSONRPC: "2.0", Method: "auth.vault.save", Params: vaultParams})
	if rpcErr != nil {
		t.Fatalf("unexpected auth vault save error: %#v", rpcErr)
	}
	bundleID := result.(map[string]string)["bundle_id"]
	if bundleID == "" {
		t.Fatalf("unexpected vault save result: %#v", result)
	}
	vault, err := auth.NewVault(db, server.deps.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	material, err := vault.Open(ctx, bundleID)
	if err != nil {
		t.Fatal(err)
	}
	if material["sid"] != "abc" {
		t.Fatalf("unexpected vault material: %#v", material)
	}
	result, rpcErr = server.handle(ctx, request{JSONRPC: "2.0", Method: "auth.profiles.read", Params: []byte(`{"id":"` + profile.ID + `"}`)})
	if rpcErr != nil {
		t.Fatalf("unexpected auth profile read error: %#v", rpcErr)
	}
	readProfile, ok := result.(store.AuthProfile)
	if !ok || readProfile.SecretBundleID != bundleID {
		t.Fatalf("profile did not reference bundle: %#v", result)
	}
}

func TestWorkflowRunUsesAuthProfileMaterial(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "sid=fresh" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	db := testStore(t)
	blobs, err := blobstore.Open(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := db.EnsureSession(ctx, store.Session{ID: "session_1", Name: "Capture", Kind: "capture", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	bodyHash, err := blobs.Put(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InsertFlow(ctx, store.Flow{
		ID:              "flow_auth_workflow",
		SessionID:       "session_1",
		Method:          "POST",
		URL:             target.URL + "/private",
		Host:            "127.0.0.1",
		Status:          http.StatusNoContent,
		RequestHeaders:  []byte(`{"Cookie":["sid=stale"]}`),
		ResponseHeaders: []byte(`{}`),
		RequestBlob:     bodyHash,
		ResponseBlob:    bodyHash,
		CreatedAt:       time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	workflow := store.Workflow{
		ID:   "workflow_auth",
		Name: "Private action",
		Steps: []store.WorkflowStep{{
			ID:       "step_1",
			FlowID:   "flow_auth_workflow",
			Name:     "POST private",
			Class:    "primary_action",
			Included: true,
			Assertions: store.WorkflowAssertions{
				Status: []int{http.StatusNoContent},
			},
		}},
	}
	if err := db.SaveWorkflow(ctx, workflow); err != nil {
		t.Fatal(err)
	}
	profile := store.AuthProfile{ID: "auth_profile_workflow", Name: "Workflow auth", Host: "127.0.0.1"}
	if err := db.SaveAuthProfile(ctx, profile); err != nil {
		t.Fatal(err)
	}
	server := NewServer(Dependencies{Store: db, Blobs: blobs, DataDir: t.TempDir()})
	vault, err := auth.NewVault(db, server.deps.DataDir)
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
	runParams, err := json.Marshal(workflowbuilder.RunParams{WorkflowID: workflow.ID, AuthProfileID: profile.ID})
	if err != nil {
		t.Fatal(err)
	}
	result, rpcErr := server.handle(ctx, request{JSONRPC: "2.0", Method: "workflows.run", Params: runParams})
	if rpcErr != nil {
		t.Fatalf("unexpected workflow run error: %#v", rpcErr)
	}
	run, ok := result.(store.WorkflowRun)
	if !ok || !run.OK || len(run.StepRuns) != 1 {
		t.Fatalf("unexpected workflow run: %#v", result)
	}
}

func TestSystemProxyEnable(t *testing.T) {
	runner := &rpcProxyRunner{}
	manager := systemproxy.New(t.TempDir(), runner)
	params, err := json.Marshal(systemProxyParams{Service: "Wi-Fi"})
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(Dependencies{
		Store:       testStore(t),
		SystemProxy: manager,
		ProxyAddr:   "127.0.0.1:18080",
	})
	_, rpcErr := server.handle(context.Background(), request{
		JSONRPC: "2.0",
		Method:  "system_proxy.enable",
		Params:  params,
	})
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %#v", rpcErr)
	}
	if got := strings.Join(runner.calls, "\n"); !strings.Contains(got, "networksetup -setsecurewebproxystate Wi-Fi on") {
		t.Fatalf("proxy was not enabled:\n%s", got)
	}
}

func TestCAInfoTrustAndRotate(t *testing.T) {
	authority, err := certs.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	trustRunner := &rpcProxyRunner{}
	server := NewServer(Dependencies{
		Store:      testStore(t),
		CA:         authority,
		TrustStore: truststore.New(trustRunner),
	})
	result, rpcErr := server.handle(context.Background(), request{
		JSONRPC: "2.0",
		Method:  "ca.info",
	})
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %#v", rpcErr)
	}
	info, ok := result.(map[string]string)
	if !ok || info["certificate_path"] == "" {
		t.Fatalf("unexpected ca info: %#v", result)
	}
	_, rpcErr = server.handle(context.Background(), request{
		JSONRPC: "2.0",
		Method:  "ca.trust",
	})
	if rpcErr != nil {
		t.Fatalf("unexpected trust error: %#v", rpcErr)
	}
	before := string(authority.CertificatePEM())
	_, rpcErr = server.handle(context.Background(), request{
		JSONRPC: "2.0",
		Method:  "ca.rotate",
	})
	if rpcErr != nil {
		t.Fatalf("unexpected rotate error: %#v", rpcErr)
	}
	if before == string(authority.CertificatePEM()) {
		t.Fatal("expected rotated ca")
	}
}

func TestProxyStartStop(t *testing.T) {
	controller := &fakeProxyController{enabled: true}
	server := NewServer(Dependencies{Store: testStore(t), Proxy: controller})
	result, rpcErr := server.handle(context.Background(), request{
		JSONRPC: "2.0",
		Method:  "proxy.stop",
	})
	if rpcErr != nil {
		t.Fatalf("unexpected stop error: %#v", rpcErr)
	}
	status, ok := result.(map[string]any)
	if !ok || status["enabled"] != false {
		t.Fatalf("unexpected status: %#v", result)
	}
	result, rpcErr = server.handle(context.Background(), request{
		JSONRPC: "2.0",
		Method:  "proxy.start",
	})
	if rpcErr != nil {
		t.Fatalf("unexpected start error: %#v", rpcErr)
	}
	status, ok = result.(map[string]any)
	if !ok || status["enabled"] != true {
		t.Fatalf("unexpected status: %#v", result)
	}
}

func TestBrowserCaptureStatusRPC(t *testing.T) {
	server := NewServer(Dependencies{Store: testStore(t), Browser: browsercapture.NewManager(testStore(t), nil)})
	result, rpcErr := server.handle(context.Background(), request{
		JSONRPC: "2.0",
		Method:  "browser_capture.status",
	})
	if rpcErr != nil {
		t.Fatalf("unexpected browser status error: %#v", rpcErr)
	}
	status, ok := result.(browsercapture.Status)
	if !ok || status.Running {
		t.Fatalf("unexpected browser status: %#v", result)
	}
}

func TestMobileProtoMaintenanceAndDiagnosticsRPC(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	server := NewServer(Dependencies{
		Store:     db,
		Version:   "test",
		DataDir:   t.TempDir(),
		ProxyAddr: "127.0.0.1:18080",
		CAPath:    "/tmp/deconstruct-ca.pem",
	})
	result, rpcErr := server.handle(ctx, request{JSONRPC: "2.0", Method: "mobile.setup"})
	if rpcErr != nil {
		t.Fatalf("unexpected mobile setup error: %#v", rpcErr)
	}
	if setup, ok := result.(mobile.Setup); !ok || setup.ProxyPort != 18080 {
		t.Fatalf("unexpected mobile setup: %#v", result)
	}

	policyParams, err := json.Marshal(capturePolicySaveParams{Policy: store.CapturePolicy{
		Scope:   "bundle_id",
		Matcher: "com.example.NativeApp",
		Action:  "include",
		TLSMode: "decrypt",
	}})
	if err != nil {
		t.Fatal(err)
	}
	result, rpcErr = server.handle(ctx, request{JSONRPC: "2.0", Method: "local_capture.policies.save", Params: policyParams})
	if rpcErr != nil {
		t.Fatalf("unexpected capture policy save error: %#v", rpcErr)
	}
	policy, ok := result.(store.CapturePolicy)
	if !ok || policy.ID == "" || policy.TLSMode != "decrypt" {
		t.Fatalf("unexpected capture policy: %#v", result)
	}
	result, rpcErr = server.handle(ctx, request{JSONRPC: "2.0", Method: "local_capture.policies.list", Params: []byte(`{"scope":"bundle_id"}`)})
	if rpcErr != nil {
		t.Fatalf("unexpected capture policy list error: %#v", rpcErr)
	}
	if policies, ok := result.([]store.CapturePolicy); !ok || len(policies) != 1 {
		t.Fatalf("unexpected capture policies: %#v", result)
	}

	protoParams, err := json.Marshal(map[string]string{
		"name":   "Demo",
		"source": "syntax = \"proto3\"; package demo; message Request { string id = 1; } service Demo { rpc Send(Request) returns (Request); }",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, rpcErr = server.handle(ctx, request{JSONRPC: "2.0", Method: "protocol_schemas.import_proto", Params: protoParams})
	if rpcErr != nil {
		t.Fatalf("unexpected proto import error: %#v", rpcErr)
	}
	schema, ok := result.(store.ProtocolSchema)
	if !ok || schema.ID == "" || schema.Kind != "protobuf" {
		t.Fatalf("unexpected proto schema: %#v", result)
	}
	result, rpcErr = server.handle(ctx, request{JSONRPC: "2.0", Method: "maintenance.stats"})
	if rpcErr != nil {
		t.Fatalf("unexpected stats error: %#v", rpcErr)
	}
	if stats, ok := result.(map[string]int64); !ok || stats["protocol_schemas"] != 1 {
		t.Fatalf("unexpected stats: %#v", result)
	}
	result, rpcErr = server.handle(ctx, request{JSONRPC: "2.0", Method: "diagnostics.bundle"})
	if rpcErr != nil {
		t.Fatalf("unexpected diagnostics error: %#v", rpcErr)
	}
	if bundle, ok := result.(diagnostics.Bundle); !ok || bundle.Path == "" {
		t.Fatalf("unexpected diagnostics bundle: %#v", result)
	}
}

func TestUnknownMethod(t *testing.T) {
	server := NewServer(Dependencies{Store: testStore(t)})
	_, rpcErr := server.handle(context.Background(), request{
		JSONRPC: "2.0",
		Method:  "missing.method",
	})
	if rpcErr == nil || rpcErr.Code != -32601 {
		t.Fatalf("expected method not found, got %#v", rpcErr)
	}
}

type fakeProxyController struct {
	enabled bool
}

func (f *fakeProxyController) StartCapture() {
	f.enabled = true
}

func (f *fakeProxyController) StopCapture() {
	f.enabled = false
}

func (f *fakeProxyController) Status() map[string]any {
	return map[string]any{"enabled": f.enabled}
}

type rpcProxyRunner struct {
	calls []string
}

func (r *rpcProxyRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	if len(args) > 0 && (args[0] == "-getwebproxy" || args[0] == "-getsecurewebproxy") {
		return []byte("Enabled: No\nServer: \nPort: 0\n"), nil
	}
	return nil, nil
}

func testStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	return db
}

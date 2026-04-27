package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dhiyaan/deconstruct/daemon/internal/blobstore"
	"github.com/dhiyaan/deconstruct/daemon/internal/store"
)

func TestMCPResourcesAndTools(t *testing.T) {
	db, blobs := mcpTestStore(t)
	ctx := context.Background()
	if err := db.EnsureSession(ctx, store.Session{ID: "session_1", Name: "Capture", Kind: "capture", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	bodyHash, err := blobs.Put([]byte(`{"name":"one"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InsertFlow(ctx, store.Flow{
		ID:              "flow_1",
		SessionID:       "session_1",
		Method:          "POST",
		URL:             "https://example.test/items",
		Host:            "example.test",
		Status:          http.StatusCreated,
		RequestHeaders:  []byte(`{"Authorization":["Bearer secret"],"Content-Type":["application/json"]}`),
		ResponseHeaders: []byte(`{"Content-Type":["application/json"]}`),
		RequestBlob:     bodyHash,
		ResponseBlob:    bodyHash,
		CreatedAt:       time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	workflow := store.Workflow{
		ID:   "workflow_1",
		Name: "Create item",
		Steps: []store.WorkflowStep{{
			ID:       "step_1",
			FlowID:   "flow_1",
			Name:     "Create",
			Included: true,
		}},
	}
	if err := db.SaveWorkflow(ctx, workflow); err != nil {
		t.Fatal(err)
	}
	server := NewServer(db, blobs)
	result, rpcErr := server.Handle(ctx, Request{JSONRPC: "2.0", Method: "resources/list"})
	if rpcErr != nil {
		t.Fatalf("unexpected resources error: %#v", rpcErr)
	}
	if !jsonContains(t, result, "deconstruct://sessions/session_1") {
		t.Fatalf("resources missing session: %#v", result)
	}
	result, rpcErr = server.Handle(ctx, Request{JSONRPC: "2.0", Method: "tools/call", Params: raw(t, map[string]any{
		"name":      "search_flows",
		"arguments": map[string]any{"host": "example.test", "limit": 10},
	})})
	if rpcErr != nil {
		t.Fatalf("unexpected search tool error: %#v", rpcErr)
	}
	if !jsonContains(t, result, "flow_1") {
		t.Fatalf("search result missing flow: %#v", result)
	}
	result, rpcErr = server.Handle(ctx, Request{JSONRPC: "2.0", Method: "tools/call", Params: raw(t, map[string]any{
		"name":      "export_openapi",
		"arguments": map[string]any{"workflow_id": "workflow_1"},
	})})
	if rpcErr != nil {
		t.Fatalf("unexpected openapi tool error: %#v", rpcErr)
	}
	if !jsonContains(t, result, "3.1.0") {
		t.Fatalf("openapi result missing version: %#v", result)
	}
	result, rpcErr = server.Handle(ctx, Request{JSONRPC: "2.0", Method: "resources/read", Params: raw(t, map[string]any{
		"uri": "deconstruct://flows/flow_1",
	})})
	if rpcErr != nil {
		t.Fatalf("unexpected resource read error: %#v", rpcErr)
	}
	if !jsonContains(t, result, "[redacted]") {
		t.Fatalf("flow resource should redact secrets: %#v", result)
	}
}

func mcpTestStore(t *testing.T) (*store.Store, *blobstore.Store) {
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

func raw(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func jsonContains(t *testing.T, value any, text string) bool {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Contains(string(data), text)
}

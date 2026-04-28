package exporter

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/dhiyaan/deconstruct/daemon/internal/blobstore"
	"github.com/dhiyaan/deconstruct/daemon/internal/store"
)

func TestCurlExport(t *testing.T) {
	command, err := Curl(store.Flow{
		Method:         "POST",
		URL:            "https://example.test/api",
		RequestHeaders: []byte(`{":authority":["example.test"],"Content-Type":["application/json"],"Host":["example.test"]}`),
	}, []byte(`{"name":"dhiyaan"}`))
	if err != nil {
		t.Fatal(err)
	}
	want := "curl -X 'POST' -H 'Content-Type: application/json' --data-binary '{\"name\":\"dhiyaan\"}' 'https://example.test/api'"
	if command != want {
		t.Fatalf("unexpected command:\n%s", command)
	}
}

func TestWorkflowProjectExports(t *testing.T) {
	db, blobs := exportTestStore(t)
	ctx := context.Background()
	if err := db.EnsureSession(ctx, store.Session{ID: "session_1", Name: "Capture", Kind: "capture", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	body, err := blobs.Put([]byte(`{"name":"one"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InsertFlow(ctx, store.Flow{
		ID:              "flow_1",
		SessionID:       "session_1",
		Method:          "POST",
		URL:             "https://example.test/items",
		Host:            "example.test",
		Status:          201,
		RequestHeaders:  []byte(`{"Content-Type":["application/json"],"Authorization":["Bearer secret"]}`),
		ResponseHeaders: []byte(`{}`),
		RequestBlob:     body,
		ResponseBlob:    body,
		CreatedAt:       time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	workflow := store.Workflow{
		ID:           "workflow_1",
		Name:         "Create Item",
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","default":"one"}}}`),
		OutputSchema: json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}}}`),
		Steps: []store.WorkflowStep{{
			ID:       "step_1",
			FlowID:   "flow_1",
			Name:     "Create",
			Class:    "primary_action",
			Included: true,
		}},
	}
	ts, err := TypeScriptProject(ctx, db, blobs, ProjectParams{Workflow: workflow})
	if err != nil {
		t.Fatal(err)
	}
	if len(ts.Files) == 0 || !projectHasFile(ts, "openapi.json") || !projectContains(ts, "[redacted]") {
		t.Fatalf("unexpected typescript project: %#v", ts)
	}
	privateTS, err := TypeScriptProject(ctx, db, blobs, ProjectParams{Workflow: workflow, IncludeSecrets: true})
	if err != nil {
		t.Fatal(err)
	}
	if !projectContains(privateTS, "Bearer secret") {
		t.Fatalf("private typescript project did not include requested secrets: %#v", privateTS)
	}
	if !projectContains(privateTS, `const defaultInput`) || !projectContains(privateTS, `"name":"one"`) {
		t.Fatalf("typescript project did not apply workflow defaults: %#v", privateTS)
	}
	py, err := PythonProject(ctx, db, blobs, ProjectParams{Workflow: workflow})
	if err != nil {
		t.Fatal(err)
	}
	if len(py.Files) == 0 || !projectHasFile(py, "generated_action/workflow.py") {
		t.Fatalf("unexpected python project: %#v", py)
	}
	openapi, err := OpenAPIDocument(ctx, db, workflow, "")
	if err != nil {
		t.Fatal(err)
	}
	if openapi.OpenAPI != "3.1.0" || openapi.Paths["/actions/create_item"] == nil {
		t.Fatalf("unexpected openapi: %#v", openapi)
	}
	postman, err := Postman(ctx, db, blobs, workflow)
	if err != nil {
		t.Fatal(err)
	}
	if len(postman.Item) != 1 || postman.Item[0].Request.Method != "POST" {
		t.Fatalf("unexpected postman: %#v", postman)
	}
}

func exportTestStore(t *testing.T) (*store.Store, *blobstore.Store) {
	t.Helper()
	db, err := store.Open(t.TempDir() + "/test.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	blobs, err := blobstore.Open(t.TempDir() + "/blobs")
	if err != nil {
		t.Fatal(err)
	}
	return db, blobs
}

func projectHasFile(project Project, path string) bool {
	for _, file := range project.Files {
		if file.Path == path {
			return true
		}
	}
	return false
}

func projectContains(project Project, text string) bool {
	for _, file := range project.Files {
		if strings.Contains(file.Content, text) {
			return true
		}
	}
	return false
}

func TestScriptExports(t *testing.T) {
	flow := store.Flow{
		Method:         "POST",
		URL:            "https://example.test/api",
		RequestHeaders: []byte(`{"Authorization":["Bearer secret"],"Content-Type":["application/json"]}`),
	}
	ts, err := TypeScript(flow, []byte(`{"token":"secret","ok":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ts, "[redacted]") {
		t.Fatalf("expected redaction in TypeScript export:\n%s", ts)
	}
	py, err := Python(flow, []byte(`{"token":"secret","ok":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(py, "[redacted]") {
		t.Fatalf("expected redaction in Python export:\n%s", py)
	}
}

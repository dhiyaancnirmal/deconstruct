package exporter

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dhiyaan/deconstruct/daemon/internal/store"
)

type memoryWorkflowStore map[string]store.Flow

func (m memoryWorkflowStore) GetFlow(_ context.Context, id string) (store.Flow, error) {
	return m[id], nil
}

func TestProjectsIncludeLocalAPIWrappers(t *testing.T) {
	workflow := store.Workflow{
		ID:   "workflow_1",
		Name: "Send Message",
		Steps: []store.WorkflowStep{{
			ID:       "step_1",
			FlowID:   "flow_1",
			Included: true,
			Name:     "POST /message",
		}},
		CreatedAt: time.Now(),
	}
	flows := memoryWorkflowStore{"flow_1": {ID: "flow_1", Method: "POST", URL: "https://example.test/message", Host: "example.test", Status: 200, RequestHeaders: []byte(`{}`), ResponseHeaders: []byte(`{}`)}}
	tsProject, err := TypeScriptProject(context.Background(), flows, nil, ProjectParams{Workflow: workflow})
	if err != nil {
		t.Fatal(err)
	}
	pyProject, err := PythonProject(context.Background(), flows, nil, ProjectParams{Workflow: workflow})
	if err != nil {
		t.Fatal(err)
	}
	if !projectHas(tsProject, "src/server.ts", "/actions/send_message") || !projectHas(tsProject, "Dockerfile", "npm\", \"run\", \"serve") {
		t.Fatalf("typescript project missing API wrapper: %#v", tsProject.Files)
	}
	if !projectHas(pyProject, "generated_action/server.py", "/actions/send_message") || !projectHas(pyProject, "Dockerfile", "uvicorn") {
		t.Fatalf("python project missing API wrapper: %#v", pyProject.Files)
	}
}

func projectHas(project Project, path, content string) bool {
	for _, file := range project.Files {
		if file.Path == path && strings.Contains(file.Content, content) {
			return true
		}
	}
	return false
}

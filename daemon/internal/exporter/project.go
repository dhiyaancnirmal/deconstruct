package exporter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/dhiyaan/deconstruct/daemon/internal/blobstore"
	"github.com/dhiyaan/deconstruct/daemon/internal/store"
)

type WorkflowStore interface {
	GetFlow(context.Context, string) (store.Flow, error)
}

type ProjectParams struct {
	Workflow  store.Workflow `json:"workflow"`
	Language  string         `json:"language"`
	ServerURL string         `json:"server_url,omitempty"`
}

type ProjectFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type Project struct {
	Language string        `json:"language"`
	Files    []ProjectFile `json:"files"`
}

type OpenAPI struct {
	OpenAPI    string              `json:"openapi"`
	Info       map[string]string   `json:"info"`
	Servers    []map[string]string `json:"servers,omitempty"`
	Paths      map[string]any      `json:"paths"`
	Components map[string]any      `json:"components,omitempty"`
}

type PostmanCollection struct {
	Info map[string]string `json:"info"`
	Item []PostmanItem     `json:"item"`
}

type PostmanItem struct {
	Name    string         `json:"name"`
	Request PostmanRequest `json:"request"`
}

type PostmanRequest struct {
	Method string              `json:"method"`
	Header []map[string]string `json:"header,omitempty"`
	URL    string              `json:"url"`
	Body   map[string]string   `json:"body,omitempty"`
}

func TypeScriptProject(ctx context.Context, store WorkflowStore, blobs *blobstore.Store, params ProjectParams) (Project, error) {
	action := actionName(params.Workflow)
	openAPI, err := OpenAPIDocument(ctx, store, params.Workflow, params.ServerURL)
	if err != nil {
		return Project{}, err
	}
	openAPIJSON, _ := json.MarshalIndent(openAPI, "", "  ")
	workflowCode, err := typescriptWorkflow(ctx, store, blobs, params.Workflow)
	if err != nil {
		return Project{}, err
	}
	files := []ProjectFile{
		{Path: "package.json", Content: fmt.Sprintf(`{"type":"module","scripts":{"start":"tsx src/index.ts","test":"node --test"},"dependencies":{"zod":"latest","tsx":"latest"}}%s`, "\n")},
		{Path: "src/index.ts", Content: fmt.Sprintf("export { %s } from './workflow.js';\n", action)},
		{Path: "src/workflow.ts", Content: workflowCode},
		{Path: "src/schemas.ts", Content: "import { z } from 'zod';\n\nexport const inputSchema = z.object({}).passthrough();\nexport const outputSchema = z.object({}).passthrough();\n"},
		{Path: "openapi.json", Content: string(openAPIJSON) + "\n"},
		{Path: ".env.example", Content: "# Add workflow secrets here. Do not commit live tokens.\n"},
		{Path: "README.md", Content: fmt.Sprintf("# %s\n\nGenerated deconstruct TypeScript workflow.\n", params.Workflow.Name)},
		{Path: "Dockerfile", Content: "FROM node:22-alpine\nWORKDIR /app\nCOPY package.json ./\nRUN npm install\nCOPY . .\nCMD [\"npm\", \"start\"]\n"},
	}
	return Project{Language: "typescript", Files: files}, nil
}

func PythonProject(ctx context.Context, store WorkflowStore, blobs *blobstore.Store, params ProjectParams) (Project, error) {
	openAPI, err := OpenAPIDocument(ctx, store, params.Workflow, params.ServerURL)
	if err != nil {
		return Project{}, err
	}
	openAPIJSON, _ := json.MarshalIndent(openAPI, "", "  ")
	workflowCode, err := pythonWorkflow(ctx, store, blobs, params.Workflow)
	if err != nil {
		return Project{}, err
	}
	files := []ProjectFile{
		{Path: "pyproject.toml", Content: "[project]\nname = \"generated-action\"\nversion = \"0.1.0\"\ndependencies = [\"httpx\", \"pydantic\", \"fastapi\", \"uvicorn\"]\n"},
		{Path: "generated_action/__init__.py", Content: "from .workflow import run\n"},
		{Path: "generated_action/workflow.py", Content: workflowCode},
		{Path: "generated_action/schemas.py", Content: "from pydantic import BaseModel, ConfigDict\n\nclass WorkflowInput(BaseModel):\n    model_config = ConfigDict(extra='allow')\n\nclass WorkflowOutput(BaseModel):\n    model_config = ConfigDict(extra='allow')\n"},
		{Path: "openapi.json", Content: string(openAPIJSON) + "\n"},
		{Path: ".env.example", Content: "# Add workflow secrets here. Do not commit live tokens.\n"},
		{Path: "README.md", Content: fmt.Sprintf("# %s\n\nGenerated deconstruct Python workflow.\n", params.Workflow.Name)},
		{Path: "Dockerfile", Content: "FROM python:3.13-slim\nWORKDIR /app\nCOPY pyproject.toml ./\nRUN pip install .\nCOPY . .\nCMD [\"python\", \"-m\", \"generated_action.workflow\"]\n"},
	}
	return Project{Language: "python", Files: files}, nil
}

func OpenAPIDocument(ctx context.Context, store WorkflowStore, workflow store.Workflow, serverURL string) (OpenAPI, error) {
	action := actionName(workflow)
	if serverURL == "" {
		serverURL = "http://127.0.0.1:8080"
	}
	method := map[string]any{
		"operationId": action,
		"requestBody": map[string]any{
			"required": true,
			"content": map[string]any{
				"application/json": map[string]any{
					"schema": rawSchema(workflow.InputSchema),
				},
			},
		},
		"responses": map[string]any{
			"200": map[string]any{
				"description": "Workflow executed",
				"content": map[string]any{
					"application/json": map[string]any{
						"schema": rawSchema(workflow.OutputSchema),
					},
				},
			},
			"4XX": map[string]any{"description": "Workflow input or auth error"},
			"5XX": map[string]any{"description": "Workflow execution error"},
		},
	}
	if example, err := exampleFromFirstIncludedStep(ctx, store, workflow); err == nil && example != nil {
		method["x-deconstruct-example"] = example
	}
	return OpenAPI{
		OpenAPI: "3.1.0",
		Info: map[string]string{
			"title":   workflow.Name,
			"version": "0.1.0",
		},
		Servers: []map[string]string{{"url": serverURL}},
		Paths: map[string]any{
			"/actions/" + action: map[string]any{
				"post": method,
			},
		},
		Components: map[string]any{
			"securitySchemes": map[string]any{
				"workflowSecrets": map[string]any{"type": "apiKey", "in": "header", "name": "X-Deconstruct-Secret-Profile"},
			},
		},
	}, nil
}

func Postman(ctx context.Context, store WorkflowStore, blobs *blobstore.Store, workflow store.Workflow) (PostmanCollection, error) {
	var items []PostmanItem
	for _, step := range workflow.Steps {
		if !step.Included {
			continue
		}
		flow, err := store.GetFlow(ctx, step.FlowID)
		if err != nil {
			return PostmanCollection{}, err
		}
		body := []byte{}
		if blobs != nil && flow.RequestBlob != "" {
			body, _ = blobs.Get(flow.RequestBlob)
		}
		headers, redactedBody, err := redactedExportInputs(flow, body)
		if err != nil {
			return PostmanCollection{}, err
		}
		var headerList []map[string]string
		for key, value := range headers {
			headerList = append(headerList, map[string]string{"key": key, "value": value})
		}
		sort.Slice(headerList, func(i, j int) bool { return headerList[i]["key"] < headerList[j]["key"] })
		item := PostmanItem{
			Name: step.Name,
			Request: PostmanRequest{
				Method: flow.Method,
				Header: headerList,
				URL:    flow.URL,
			},
		}
		if len(redactedBody) > 0 {
			item.Request.Body = map[string]string{"mode": "raw", "raw": string(redactedBody)}
		}
		items = append(items, item)
	}
	return PostmanCollection{
		Info: map[string]string{"name": workflow.Name, "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json"},
		Item: items,
	}, nil
}

func typescriptWorkflow(ctx context.Context, store WorkflowStore, blobs *blobstore.Store, workflow store.Workflow) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "import { inputSchema } from './schemas.js';\n\nexport async function %s(input: unknown) {\n  const parsed = inputSchema.parse(input);\n", actionName(workflow))
	for index, step := range workflow.Steps {
		if !step.Included {
			continue
		}
		flow, err := store.GetFlow(ctx, step.FlowID)
		if err != nil {
			return "", err
		}
		body := ""
		if blobs != nil && flow.RequestBlob != "" {
			raw, _ := blobs.Get(flow.RequestBlob)
			body = string(raw)
		}
		headers, redactedBody, err := redactedExportInputs(flow, []byte(body))
		if err != nil {
			return "", err
		}
		if len(redactedBody) > 0 {
			body = string(redactedBody)
		}
		headerJSON, _ := json.Marshal(headers)
		bodyJSON, _ := json.Marshal(body)
		fmt.Fprintf(&b, "  const step%d = await fetch(%q, { method: %q, headers: %s, body: %s });\n", index+1, flow.URL, flow.Method, string(headerJSON), string(bodyJSON))
		fmt.Fprintf(&b, "  if (!step%d.ok) throw new Error(%q + step%d.status);\n", index+1, step.ID+" failed: ", index+1)
	}
	b.WriteString("  return { ok: true, input: parsed };\n}\n")
	return b.String(), nil
}

func pythonWorkflow(ctx context.Context, store WorkflowStore, blobs *blobstore.Store, workflow store.Workflow) (string, error) {
	var b strings.Builder
	b.WriteString("import httpx\n\n\ndef run(input_data=None):\n    input_data = input_data or {}\n")
	for _, step := range workflow.Steps {
		if !step.Included {
			continue
		}
		flow, err := store.GetFlow(ctx, step.FlowID)
		if err != nil {
			return "", err
		}
		body := []byte{}
		if blobs != nil && flow.RequestBlob != "" {
			body, _ = blobs.Get(flow.RequestBlob)
		}
		headers, redactedBody, err := redactedExportInputs(flow, body)
		if err != nil {
			return "", err
		}
		headerJSON, _ := json.Marshal(headers)
		bodyJSON, _ := json.Marshal(string(redactedBody))
		fmt.Fprintf(&b, "    response = httpx.request(%q, %q, headers=%s, content=%s, timeout=60)\n", flow.Method, flow.URL, string(headerJSON), string(bodyJSON))
		fmt.Fprintf(&b, "    response.raise_for_status()\n")
	}
	b.WriteString("    return {\"ok\": True, \"input\": input_data}\n")
	return b.String(), nil
}

func exampleFromFirstIncludedStep(ctx context.Context, store WorkflowStore, workflow store.Workflow) (map[string]any, error) {
	for _, step := range workflow.Steps {
		if !step.Included {
			continue
		}
		flow, err := store.GetFlow(ctx, step.FlowID)
		if err != nil {
			return nil, err
		}
		parsed, _ := url.Parse(flow.URL)
		host := flow.Host
		if parsed != nil && parsed.Host != "" {
			host = parsed.Host
		}
		return map[string]any{"method": flow.Method, "host": host, "status": flow.Status}, nil
	}
	return nil, nil
}

func rawSchema(data json.RawMessage) any {
	if len(data) == 0 {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	var decoded any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return decoded
}

func actionName(workflow store.Workflow) string {
	name := strings.ToLower(strings.TrimSpace(workflow.Name))
	var b strings.Builder
	lastUnderscore := false
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		out = "run_workflow"
	}
	return out
}

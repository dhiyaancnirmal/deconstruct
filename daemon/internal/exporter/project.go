package exporter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/dhiyaan/deconstruct/daemon/internal/blobstore"
	"github.com/dhiyaan/deconstruct/daemon/internal/replay"
	"github.com/dhiyaan/deconstruct/daemon/internal/store"
)

type WorkflowStore interface {
	GetFlow(context.Context, string) (store.Flow, error)
}

type ProjectParams struct {
	Workflow       store.Workflow `json:"workflow"`
	Language       string         `json:"language"`
	ServerURL      string         `json:"server_url,omitempty"`
	IncludeSecrets bool           `json:"include_secrets,omitempty"`
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
	workflowCode, err := typescriptWorkflow(ctx, store, blobs, params.Workflow, params.IncludeSecrets)
	if err != nil {
		return Project{}, err
	}
	files := []ProjectFile{
		{Path: "package.json", Content: fmt.Sprintf(`{"type":"module","scripts":{"start":"tsx src/index.ts","serve":"tsx src/server.ts","test":"tsx --test test/*.test.js"},"dependencies":{"zod":"latest","tsx":"latest"}}%s`, "\n")},
		{Path: "src/index.ts", Content: fmt.Sprintf("export { %s } from './workflow.js';\n", action)},
		{Path: "src/workflow.ts", Content: workflowCode},
		{Path: "src/server.ts", Content: typescriptServer(action)},
		{Path: "src/schemas.ts", Content: "import { z } from 'zod';\n\nexport const inputSchema = z.object({}).passthrough();\nexport const outputSchema = z.object({}).passthrough();\n"},
		{Path: "test/workflow.test.js", Content: fmt.Sprintf("import assert from 'node:assert/strict';\nimport { test } from 'node:test';\nimport { %s } from '../src/workflow.ts';\n\ntest('workflow function is exported', () => {\n  assert.equal(typeof %s, 'function');\n});\n", action, action)},
		{Path: "openapi.json", Content: string(openAPIJSON) + "\n"},
		{Path: ".env.example", Content: "# Add workflow secrets here. Do not commit live tokens.\n"},
		{Path: "README.md", Content: fmt.Sprintf("# %s\n\nGenerated deconstruct TypeScript workflow. Run `npm run serve` to expose the local API wrapper.\n", params.Workflow.Name)},
		{Path: "Dockerfile", Content: "FROM node:22-alpine\nWORKDIR /app\nCOPY package.json ./\nRUN npm install\nCOPY . .\nCMD [\"npm\", \"run\", \"serve\"]\n"},
	}
	return Project{Language: "typescript", Files: files}, nil
}

func PythonProject(ctx context.Context, store WorkflowStore, blobs *blobstore.Store, params ProjectParams) (Project, error) {
	action := actionName(params.Workflow)
	openAPI, err := OpenAPIDocument(ctx, store, params.Workflow, params.ServerURL)
	if err != nil {
		return Project{}, err
	}
	openAPIJSON, _ := json.MarshalIndent(openAPI, "", "  ")
	workflowCode, err := pythonWorkflow(ctx, store, blobs, params.Workflow, params.IncludeSecrets)
	if err != nil {
		return Project{}, err
	}
	files := []ProjectFile{
		{Path: "pyproject.toml", Content: "[project]\nname = \"generated-action\"\nversion = \"0.1.0\"\ndependencies = [\"httpx\", \"pydantic\", \"fastapi\", \"uvicorn\"]\n"},
		{Path: "generated_action/__init__.py", Content: "from .workflow import run\n"},
		{Path: "generated_action/workflow.py", Content: workflowCode},
		{Path: "generated_action/server.py", Content: pythonServer(action)},
		{Path: "generated_action/schemas.py", Content: "from pydantic import BaseModel, ConfigDict\n\nclass WorkflowInput(BaseModel):\n    model_config = ConfigDict(extra='allow')\n\nclass WorkflowOutput(BaseModel):\n    model_config = ConfigDict(extra='allow')\n"},
		{Path: "tests/test_workflow.py", Content: "from generated_action.workflow import run\n\n\ndef test_workflow_callable():\n    assert callable(run)\n"},
		{Path: "openapi.json", Content: string(openAPIJSON) + "\n"},
		{Path: ".env.example", Content: "# Add workflow secrets here. Do not commit live tokens.\n"},
		{Path: "README.md", Content: fmt.Sprintf("# %s\n\nGenerated deconstruct Python workflow. Run `uvicorn generated_action.server:app --host 0.0.0.0 --port 8080` to expose the local API wrapper.\n", params.Workflow.Name)},
		{Path: "Dockerfile", Content: "FROM python:3.13-slim\nWORKDIR /app\nCOPY pyproject.toml ./\nRUN pip install .\nCOPY . .\nCMD [\"uvicorn\", \"generated_action.server:app\", \"--host\", \"0.0.0.0\", \"--port\", \"8080\"]\n"},
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
		headers, redactedBody, err := exportInputs(flow, body, true)
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

func typescriptWorkflow(ctx context.Context, store WorkflowStore, blobs *blobstore.Store, workflow store.Workflow, includeSecrets bool) (string, error) {
	var b strings.Builder
	defaultJSON, _ := json.Marshal(schemaDefaultValues(workflow.InputSchema))
	fmt.Fprintf(&b, `import { inputSchema } from './schemas.js';

type StepOutputs = Record<string, Record<string, unknown>>;
const defaultInput: Record<string, unknown> = %s;

function readPath(value: unknown, path: string): unknown {
  if (path === '$') return value;
  if (!path.startsWith('$.')) return undefined;
  let current: unknown = value;
  for (const segment of path.slice(2).split('.')) {
    const match = segment.match(/^([^\[]+)(?:\[(\d+)\])?$/);
    if (!match || current === null || typeof current !== 'object') return undefined;
    current = (current as Record<string, unknown>)[match[1]];
    if (match[2] !== undefined) {
      if (!Array.isArray(current)) return undefined;
      current = current[Number(match[2])];
    }
  }
  return current;
}

function bind(value: string, input: Record<string, unknown>, steps: StepOutputs): string {
  return value
    .replace(/\$\{input\.([^}]+)\}/g, (_, key) => String(input[key] ?? ''))
    .replace(/\$\{steps\.([^.}]+)\.([^}]+)\}/g, (_, stepID, key) => String(steps[stepID]?.[key] ?? ''));
}

function bindHeaders(headers: Record<string, string>, input: Record<string, unknown>, steps: StepOutputs): Record<string, string> {
  return Object.fromEntries(Object.entries(headers).map(([key, value]) => [key, bind(value, input, steps)]));
}

export async function %s(input: unknown) {
  const parsed = { ...defaultInput, ...inputSchema.parse(input) };
  const steps: StepOutputs = {};
  let lastResult: unknown = undefined;
`, string(defaultJSON), actionName(workflow))
	for index, step := range workflow.Steps {
		if !step.Included {
			continue
		}
		flow, err := store.GetFlow(ctx, step.FlowID)
		if err != nil {
			return "", err
		}
		compiled, err := compileStep(blobs, flow, step, includeSecrets)
		if err != nil {
			return "", err
		}
		headerJSON, _ := json.Marshal(compiled.Headers)
		bodyJSON, _ := json.Marshal(compiled.Body)
		statusJSON, _ := json.Marshal(compiled.Statuses)
		extractJSON, _ := json.Marshal(stringMap(step.Extract))
		jsonPathJSON, _ := json.Marshal(stringSlice(step.Assertions.JSONPath))
		bodyContainsJSON, _ := json.Marshal(stringSlice(step.Assertions.BodyContains))
		fmt.Fprintf(&b, "  const step%dBody = %s === '' ? undefined : bind(%s, parsed, steps);\n", index+1, bodyJSON, bodyJSON)
		fmt.Fprintf(&b, "  const step%d = await fetch(%q, { method: %q, headers: bindHeaders(%s, parsed, steps), body: step%dBody });\n", index+1, compiled.URL, compiled.Method, string(headerJSON), index+1)
		fmt.Fprintf(&b, "  if (!%s.includes(step%d.status)) throw new Error(%q + step%d.status);\n", string(statusJSON), index+1, step.ID+" failed: ", index+1)
		fmt.Fprintf(&b, "  const step%dText = await step%d.text();\n  let step%dJSON: unknown = undefined;\n  try { step%dJSON = step%dText ? JSON.parse(step%dText) : undefined; } catch {}\n", index+1, index+1, index+1, index+1, index+1, index+1)
		fmt.Fprintf(&b, "  lastResult = step%dJSON === undefined ? step%dText : step%dJSON;\n", index+1, index+1, index+1)
		fmt.Fprintf(&b, "  for (const path of %s) if (readPath(step%dJSON, path) === undefined) throw new Error(%q + path);\n", string(jsonPathJSON), index+1, step.ID+" missing JSON path: ")
		fmt.Fprintf(&b, "  for (const needle of %s) if (!step%dText.includes(needle)) throw new Error(%q + needle);\n", string(bodyContainsJSON), index+1, step.ID+" missing body text: ")
		fmt.Fprintf(&b, "  steps[%q] = Object.fromEntries(Object.entries(%s).map(([key, path]) => [key, readPath(step%dJSON, path as string)]));\n", step.ID, string(extractJSON), index+1)
	}
	b.WriteString("  return { ok: true, input: parsed, result: lastResult };\n}\n")
	return b.String(), nil
}

type compiledStep struct {
	Method   string
	URL      string
	Headers  map[string]string
	Body     string
	Statuses []int
}

func compileStep(blobs *blobstore.Store, flow store.Flow, step store.WorkflowStep, includeSecrets bool) (compiledStep, error) {
	body := []byte{}
	if blobs != nil && flow.RequestBlob != "" {
		raw, _ := blobs.Get(flow.RequestBlob)
		body = raw
	}
	headers, redactedBody, err := exportInputs(flow, body, !includeSecrets)
	if err != nil {
		return compiledStep{}, err
	}
	method := flow.Method
	targetURL := flow.URL
	bodyText := string(redactedBody)
	var overrides replay.Overrides
	if len(step.Overrides) > 0 {
		if err := json.Unmarshal(step.Overrides, &overrides); err != nil {
			return compiledStep{}, err
		}
		if overrides.Method != "" {
			method = overrides.Method
		}
		if overrides.URL != "" {
			targetURL = overrides.URL
		}
		for _, name := range overrides.RemoveHeaders {
			delete(headers, name)
		}
		for name, values := range overrides.Headers {
			headers[name] = strings.Join(values, ", ")
		}
		if len(overrides.Query) > 0 {
			parsed, err := url.Parse(targetURL)
			if err != nil {
				return compiledStep{}, err
			}
			query := parsed.Query()
			for key, value := range overrides.Query {
				query.Set(key, value)
			}
			parsed.RawQuery = query.Encode()
			targetURL = parsed.String()
		}
		if overrides.Body != nil {
			bodyText = *overrides.Body
		}
	}
	statuses := step.Assertions.Status
	if len(statuses) == 0 {
		statuses = expectedStatusSet(flow.Status)
	}
	return compiledStep{Method: method, URL: targetURL, Headers: headers, Body: bodyText, Statuses: statuses}, nil
}

func expectedStatusSet(status int) []int {
	if status >= 200 && status < 400 {
		return []int{status}
	}
	return []int{200, 201, 202, 204}
}

func stringMap(values map[string]string) map[string]string {
	if values == nil {
		return map[string]string{}
	}
	return values
}

func stringSlice(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func pythonWorkflow(ctx context.Context, store WorkflowStore, blobs *blobstore.Store, workflow store.Workflow, includeSecrets bool) (string, error) {
	var b strings.Builder
	defaultJSON, _ := json.Marshal(schemaDefaultValues(workflow.InputSchema))
	fmt.Fprintf(&b, "import httpx\n\n\nDEFAULT_INPUT = %s\n\n\ndef run(input_data=None):\n    input_data = {**DEFAULT_INPUT, **(input_data or {})}\n    last_result = None\n", string(defaultJSON))
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
		headers, redactedBody, err := exportInputs(flow, body, !includeSecrets)
		if err != nil {
			return "", err
		}
		headerJSON, _ := json.Marshal(headers)
		bodyJSON, _ := json.Marshal(string(redactedBody))
		fmt.Fprintf(&b, "    response = httpx.request(%q, %q, headers=%s, content=%s, timeout=60)\n", flow.Method, flow.URL, string(headerJSON), string(bodyJSON))
		fmt.Fprintf(&b, "    response.raise_for_status()\n")
		b.WriteString("    try:\n        last_result = response.json()\n    except ValueError:\n        last_result = response.text\n")
	}
	b.WriteString("    return {\"ok\": True, \"input\": input_data, \"result\": last_result}\n")
	return b.String(), nil
}

func typescriptServer(action string) string {
	return fmt.Sprintf(`import http from 'node:http';
import { %s } from './workflow.js';

const port = Number(process.env.PORT || 8080);

const server = http.createServer(async (req, res) => {
  if (req.method !== 'POST' || req.url !== '/actions/%s') {
    res.writeHead(404, { 'content-type': 'application/json' });
    res.end(JSON.stringify({ ok: false, error: 'not_found' }));
    return;
  }
  try {
    const chunks: Buffer[] = [];
    for await (const chunk of req) chunks.push(Buffer.from(chunk));
    const input = chunks.length > 0 ? JSON.parse(Buffer.concat(chunks).toString('utf8')) : {};
    const result = await %s(input);
    res.writeHead(200, { 'content-type': 'application/json' });
    res.end(JSON.stringify({ ok: true, result }));
  } catch (error) {
    res.writeHead(500, { 'content-type': 'application/json' });
    res.end(JSON.stringify({ ok: false, error: error instanceof Error ? error.message : String(error) }));
  }
});

server.listen(port, '0.0.0.0');
`, action, action, action)
}

func pythonServer(action string) string {
	return fmt.Sprintf(`from fastapi import FastAPI, HTTPException

from .workflow import run

app = FastAPI(title="Generated deconstruct workflow")


@app.post("/actions/%s")
def run_action(input_data: dict):
    try:
        return {"ok": True, "result": run(input_data)}
    except Exception as exc:
        raise HTTPException(status_code=500, detail=str(exc)) from exc
`, action)
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

func schemaDefaultValues(data json.RawMessage) map[string]any {
	var decoded struct {
		Properties map[string]map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return map[string]any{}
	}
	defaults := map[string]any{}
	for key, property := range decoded.Properties {
		if value, ok := property["default"]; ok {
			defaults[key] = value
		}
	}
	return defaults
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

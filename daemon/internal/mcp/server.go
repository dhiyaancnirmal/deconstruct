package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/dhiyaan/deconstruct/daemon/internal/blobstore"
	"github.com/dhiyaan/deconstruct/daemon/internal/bodyparse"
	"github.com/dhiyaan/deconstruct/daemon/internal/exporter"
	"github.com/dhiyaan/deconstruct/daemon/internal/redact"
	"github.com/dhiyaan/deconstruct/daemon/internal/store"
	workflowbuilder "github.com/dhiyaan/deconstruct/daemon/internal/workflow"
)

type Server struct {
	store *store.Store
	blobs *blobstore.Store
}

type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type Resource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

func NewServer(store *store.Store, blobs *blobstore.Store) *Server {
	return &Server{store: store, blobs: blobs}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "MCP endpoint accepts POST", http.StatusMethodNotAllowed)
		return
	}
	var req Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		write(w, Response{JSONRPC: "2.0", Error: &Error{Code: -32700, Message: "parse error"}})
		return
	}
	result, rpcErr := s.Handle(r.Context(), req)
	resp := Response{JSONRPC: "2.0", ID: req.ID, Result: result}
	if rpcErr != nil {
		resp.Result = nil
		resp.Error = rpcErr
	}
	write(w, resp)
}

func (s *Server) Handle(ctx context.Context, req Request) (any, *Error) {
	switch req.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": "2025-11-25",
			"serverInfo":      map[string]string{"name": "deconstruct", "version": "0.1.0-dev"},
			"capabilities":    map[string]any{"tools": map[string]any{}, "resources": map[string]any{}, "prompts": map[string]any{}},
		}, nil
	case "tools/list":
		return map[string]any{"tools": []Tool{
			{"search_flows", "Query captured traffic"},
			{"read_flow", "Read a redacted flow"},
			{"read_sequence", "Read flows around a selected flow"},
			{"create_workflow", "Create a workflow draft from flow ids"},
			{"run_workflow", "Run a saved workflow"},
			{"export_openapi", "Export OpenAPI for a workflow"},
			{"export_script", "Export a single captured request as script"},
		}}, nil
	case "resources/list":
		return s.resources(ctx)
	case "resources/read":
		var params struct {
			URI string `json:"uri"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil || params.URI == "" {
			return nil, invalidParams()
		}
		return s.readResource(ctx, params.URI)
	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil || params.Name == "" {
			return nil, invalidParams()
		}
		return s.callTool(ctx, params.Name, params.Arguments)
	case "prompts/list":
		return map[string]any{"prompts": []map[string]string{
			{"name": "build_api_from_recording", "description": "Convert selected flow IDs into a workflow draft"},
			{"name": "find_auth_chain", "description": "Inspect captured auth/session behavior"},
			{"name": "infer_openapi", "description": "Generate OpenAPI for a workflow"},
			{"name": "debug_replay_failure", "description": "Compare original and replay flows"},
		}}, nil
	default:
		return nil, &Error{Code: -32601, Message: "method not found"}
	}
}

func (s *Server) resources(ctx context.Context) (any, *Error) {
	sessions, err := s.store.ListSessions(ctx)
	if err != nil {
		return nil, internal(err)
	}
	workflows, err := s.store.ListWorkflows(ctx)
	if err != nil {
		return nil, internal(err)
	}
	resources := []Resource{{URI: "deconstruct://sessions", Name: "Sessions", MimeType: "application/json"}}
	for _, session := range sessions {
		resources = append(resources, Resource{URI: "deconstruct://sessions/" + session.ID, Name: session.Name, MimeType: "application/json"})
	}
	for _, workflow := range workflows {
		resources = append(resources, Resource{URI: "deconstruct://workflows/" + workflow.ID, Name: workflow.Name, MimeType: "application/json"})
		resources = append(resources, Resource{URI: "deconstruct://schemas/" + workflow.ID, Name: workflow.Name + " schema", MimeType: "application/json"})
	}
	return map[string]any{"resources": resources}, nil
}

func (s *Server) readResource(ctx context.Context, uri string) (any, *Error) {
	switch {
	case uri == "deconstruct://sessions":
		sessions, err := s.store.ListSessions(ctx)
		if err != nil {
			return nil, internal(err)
		}
		return content(uri, sessions), nil
	case strings.HasPrefix(uri, "deconstruct://sessions/"):
		id := strings.TrimPrefix(uri, "deconstruct://sessions/")
		result, err := s.store.QueryFlows(ctx, store.FlowQuery{SessionID: id, Limit: 100})
		if err != nil {
			return nil, internal(err)
		}
		return content(uri, result), nil
	case strings.HasPrefix(uri, "deconstruct://flows/"):
		id := strings.TrimPrefix(uri, "deconstruct://flows/")
		flow, err := s.store.GetFlow(ctx, id)
		if err != nil {
			return nil, internal(err)
		}
		return content(uri, s.redactedFlow(flow)), nil
	case strings.HasPrefix(uri, "deconstruct://workflows/"):
		id := strings.TrimPrefix(uri, "deconstruct://workflows/")
		workflow, err := s.store.GetWorkflow(ctx, id)
		if err != nil {
			return nil, internal(err)
		}
		return content(uri, workflow), nil
	case strings.HasPrefix(uri, "deconstruct://schemas/"):
		id := strings.TrimPrefix(uri, "deconstruct://schemas/")
		workflow, err := s.store.GetWorkflow(ctx, id)
		if err != nil {
			return nil, internal(err)
		}
		return content(uri, map[string]any{"input": json.RawMessage(workflow.InputSchema), "output": json.RawMessage(workflow.OutputSchema)}), nil
	default:
		return nil, &Error{Code: -32602, Message: "unknown resource"}
	}
}

func (s *Server) callTool(ctx context.Context, name string, arguments json.RawMessage) (any, *Error) {
	switch name {
	case "search_flows":
		var query store.FlowQuery
		if len(arguments) > 0 {
			if err := json.Unmarshal(arguments, &query); err != nil {
				return nil, invalidParams()
			}
		}
		result, err := s.store.QueryFlows(ctx, query)
		if err != nil {
			return nil, internal(err)
		}
		return toolContent(result), nil
	case "read_flow":
		var params struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(arguments, &params); err != nil || params.ID == "" {
			return nil, invalidParams()
		}
		flow, err := s.store.GetFlow(ctx, params.ID)
		if err != nil {
			return nil, internal(err)
		}
		return toolContent(s.redactedFlow(flow)), nil
	case "read_sequence":
		var params struct {
			SessionID string `json:"session_id"`
			Limit     int    `json:"limit,omitempty"`
		}
		if err := json.Unmarshal(arguments, &params); err != nil || params.SessionID == "" {
			return nil, invalidParams()
		}
		if params.Limit == 0 {
			params.Limit = 20
		}
		result, err := s.store.QueryFlows(ctx, store.FlowQuery{SessionID: params.SessionID, Limit: params.Limit, SortBy: "time", SortDir: "asc"})
		if err != nil {
			return nil, internal(err)
		}
		return toolContent(result), nil
	case "create_workflow":
		var params workflowbuilder.SuggestParams
		if err := json.Unmarshal(arguments, &params); err != nil {
			return nil, invalidParams()
		}
		workflow, err := workflowbuilder.NewBuilder(s.store, s.blobs).Suggest(ctx, params)
		if err != nil {
			return nil, internal(err)
		}
		return toolContent(workflow), nil
	case "run_workflow":
		var params workflowbuilder.RunParams
		if err := json.Unmarshal(arguments, &params); err != nil || params.WorkflowID == "" {
			return nil, invalidParams()
		}
		workflow, err := s.store.GetWorkflow(ctx, params.WorkflowID)
		if err != nil {
			return nil, internal(err)
		}
		run, err := workflowbuilder.NewBuilder(s.store, s.blobs).Run(ctx, workflow, params)
		if err != nil {
			return nil, internal(err)
		}
		return toolContent(run), nil
	case "export_openapi":
		var params struct {
			WorkflowID string `json:"workflow_id"`
			ServerURL  string `json:"server_url,omitempty"`
		}
		if err := json.Unmarshal(arguments, &params); err != nil || params.WorkflowID == "" {
			return nil, invalidParams()
		}
		workflow, err := s.store.GetWorkflow(ctx, params.WorkflowID)
		if err != nil {
			return nil, internal(err)
		}
		doc, err := exporter.OpenAPIDocument(ctx, s.store, workflow, params.ServerURL)
		if err != nil {
			return nil, internal(err)
		}
		return toolContent(doc), nil
	case "export_script":
		var params struct {
			ID       string `json:"id"`
			Language string `json:"language"`
		}
		if err := json.Unmarshal(arguments, &params); err != nil || params.ID == "" || params.Language == "" {
			return nil, invalidParams()
		}
		flow, err := s.store.GetFlow(ctx, params.ID)
		if err != nil {
			return nil, internal(err)
		}
		var body []byte
		if s.blobs != nil && flow.RequestBlob != "" {
			body, _ = s.blobs.Get(flow.RequestBlob)
		}
		var script string
		switch params.Language {
		case "typescript", "ts":
			script, err = exporter.TypeScript(flow, body)
		case "python", "py":
			script, err = exporter.Python(flow, body)
		default:
			return nil, &Error{Code: -32602, Message: "unsupported language"}
		}
		if err != nil {
			return nil, internal(err)
		}
		return toolContent(map[string]string{"script": script}), nil
	default:
		return nil, &Error{Code: -32602, Message: "unknown tool"}
	}
}

type redactedFlow struct {
	Flow            store.Flow  `json:"flow"`
	RequestHeaders  http.Header `json:"request_headers,omitempty"`
	ResponseHeaders http.Header `json:"response_headers,omitempty"`
	RequestParsed   any         `json:"request_parsed,omitempty"`
	ResponseParsed  any         `json:"response_parsed,omitempty"`
}

func content(uri string, value any) any {
	data, _ := json.MarshalIndent(value, "", "  ")
	return map[string]any{"contents": []map[string]string{{"uri": uri, "mimeType": "application/json", "text": string(data)}}}
}

func toolContent(value any) any {
	data, _ := json.MarshalIndent(value, "", "  ")
	return map[string]any{"content": []map[string]string{{"type": "text", "text": string(data)}}}
}

func write(w http.ResponseWriter, response Response) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func invalidParams() *Error {
	return &Error{Code: -32602, Message: "invalid params"}
}

func internal(err error) *Error {
	return &Error{Code: -32000, Message: err.Error()}
}

func RedactedFlow(flow store.Flow, blobs *blobstore.Store) redactedFlow {
	detail := redactedFlow{Flow: flow}
	_ = json.Unmarshal(flow.RequestHeaders, &detail.RequestHeaders)
	_ = json.Unmarshal(flow.ResponseHeaders, &detail.ResponseHeaders)
	detail.RequestHeaders = redact.Header(detail.RequestHeaders)
	detail.ResponseHeaders = redact.Header(detail.ResponseHeaders)
	if blobs != nil && flow.RequestBlob != "" {
		if body, err := blobs.Get(flow.RequestBlob); err == nil {
			redacted := redact.Body(body)
			detail.RequestParsed = bodyparse.Parse(detail.RequestHeaders, redacted)
		}
	}
	if blobs != nil && flow.ResponseBlob != "" {
		if body, err := blobs.Get(flow.ResponseBlob); err == nil {
			redacted := redact.Body(body)
			detail.ResponseParsed = bodyparse.Parse(detail.ResponseHeaders, redacted)
		}
	}
	return detail
}

func (s *Server) redactedFlow(flow store.Flow) redactedFlow {
	return RedactedFlow(flow, s.blobs)
}

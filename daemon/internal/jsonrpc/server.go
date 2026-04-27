package jsonrpc

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strconv"

	"github.com/dhiyaan/deconstruct/daemon/internal/auth"
	"github.com/dhiyaan/deconstruct/daemon/internal/blobstore"
	"github.com/dhiyaan/deconstruct/daemon/internal/bodyparse"
	"github.com/dhiyaan/deconstruct/daemon/internal/certs"
	"github.com/dhiyaan/deconstruct/daemon/internal/exporter"
	"github.com/dhiyaan/deconstruct/daemon/internal/fidelity"
	"github.com/dhiyaan/deconstruct/daemon/internal/harimport"
	"github.com/dhiyaan/deconstruct/daemon/internal/id"
	"github.com/dhiyaan/deconstruct/daemon/internal/redact"
	"github.com/dhiyaan/deconstruct/daemon/internal/replay"
	"github.com/dhiyaan/deconstruct/daemon/internal/store"
	"github.com/dhiyaan/deconstruct/daemon/internal/systemproxy"
	"github.com/dhiyaan/deconstruct/daemon/internal/truststore"
	workflowbuilder "github.com/dhiyaan/deconstruct/daemon/internal/workflow"
)

type Dependencies struct {
	Store       *store.Store
	Blobs       *blobstore.Store
	CA          *certs.Authority
	Proxy       ProxyController
	SystemProxy *systemproxy.Manager
	TrustStore  *truststore.Manager
	Version     string
	DataDir     string
	ProxyAddr   string
	SocketPath  string
	CAPath      string
}

type ProxyController interface {
	StartCapture()
	StopCapture()
	Status() map[string]any
}

type Server struct {
	deps Dependencies
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type flowsListParams struct {
	Limit int `json:"limit"`
}

type limitParams struct {
	Limit int `json:"limit"`
}

type flowIDParams struct {
	ID string `json:"id"`
}

type exportScriptParams struct {
	ID       string `json:"id"`
	Language string `json:"language"`
}

type exportWorkflowParams struct {
	WorkflowID string `json:"workflow_id"`
	Language   string `json:"language,omitempty"`
	ServerURL  string `json:"server_url,omitempty"`
}

type exportArtifactListParams struct {
	WorkflowID string `json:"workflow_id,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

type replayListParams struct {
	OriginalFlowID string `json:"original_flow_id,omitempty"`
	Limit          int    `json:"limit,omitempty"`
}

type workflowSaveParams struct {
	Workflow store.Workflow `json:"workflow"`
}

type workflowRunListParams struct {
	WorkflowID string `json:"workflow_id,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

type authChainListParams struct {
	SessionID string `json:"session_id,omitempty"`
	Host      string `json:"host,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type authProfileSaveParams struct {
	Profile store.AuthProfile `json:"profile"`
}

type authProfileListParams struct {
	Host  string `json:"host,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

type authVaultSaveParams struct {
	ProfileID string            `json:"profile_id"`
	Material  map[string]string `json:"material"`
}

type tagParams struct {
	FlowID string `json:"flow_id"`
	Tag    string `json:"tag"`
}

type savedFilterParams struct {
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name"`
	Query json.RawMessage `json:"query"`
}

type compareParams struct {
	IDs []string `json:"ids"`
}

type harImportParams struct {
	Path    string            `json:"path,omitempty"`
	Data    json.RawMessage   `json:"data,omitempty"`
	Options harimport.Options `json:"options,omitempty"`
}

type flowDetail struct {
	Flow            store.Flow       `json:"flow"`
	RequestHeaders  http.Header      `json:"request_headers,omitempty"`
	ResponseHeaders http.Header      `json:"response_headers,omitempty"`
	Query           url.Values       `json:"query,omitempty"`
	RequestCookies  []*http.Cookie   `json:"request_cookies,omitempty"`
	ResponseCookies []*http.Cookie   `json:"response_cookies,omitempty"`
	RequestBody     string           `json:"request_body,omitempty"`
	ResponseBody    string           `json:"response_body,omitempty"`
	RequestJSON     any              `json:"request_json,omitempty"`
	ResponseJSON    any              `json:"response_json,omitempty"`
	RequestParsed   bodyparse.Parsed `json:"request_parsed,omitempty"`
	ResponseParsed  bodyparse.Parsed `json:"response_parsed,omitempty"`
}

type flowComparison struct {
	Flows       []flowDetail `json:"flows"`
	Differences []difference `json:"differences"`
}

type difference struct {
	Field  string         `json:"field"`
	Values map[string]any `json:"values"`
}

type systemProxyParams struct {
	Service string `json:"service"`
	Host    string `json:"host,omitempty"`
	Port    int    `json:"port,omitempty"`
}

func NewServer(deps Dependencies) *Server {
	return &Server{deps: deps}
}

func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	defer listener.Close()
	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				return fmt.Errorf("accept rpc connection: %w", err)
			}
		}
		go s.serveConn(ctx, conn)
	}
}

func (s *Server) serveConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	scanner := bufio.NewScanner(conn)
	encoder := json.NewEncoder(conn)
	for scanner.Scan() {
		var req request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			_ = encoder.Encode(errorResponse(nil, -32700, "parse error"))
			continue
		}
		result, rpcErr := s.handle(ctx, req)
		resp := response{JSONRPC: "2.0", ID: req.ID}
		if rpcErr != nil {
			resp.Error = rpcErr
		} else {
			resp.Result = result
		}
		_ = encoder.Encode(resp)
	}
}

func (s *Server) handle(ctx context.Context, req request) (any, *rpcError) {
	if req.JSONRPC != "2.0" {
		return nil, &rpcError{Code: -32600, Message: "invalid request"}
	}
	switch req.Method {
	case "daemon.status":
		count, err := s.deps.Store.CountFlows(ctx)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		return map[string]any{
			"name":        "deconstructd",
			"version":     s.deps.Version,
			"data_dir":    s.deps.DataDir,
			"socket_path": s.deps.SocketPath,
			"proxy_addr":  s.deps.ProxyAddr,
			"ca_path":     s.deps.CAPath,
			"flow_count":  count,
		}, nil
	case "ca.info":
		if s.deps.CA == nil {
			return nil, &rpcError{Code: -32000, Message: "certificate authority is not configured"}
		}
		return map[string]string{
			"certificate_path": s.deps.CA.CertificatePath(),
			"key_path":         s.deps.CA.KeyPath(),
		}, nil
	case "ca.trust":
		if s.deps.CA == nil || s.deps.TrustStore == nil {
			return nil, &rpcError{Code: -32000, Message: "certificate trust is not configured"}
		}
		if err := s.deps.TrustStore.TrustRoot(ctx, s.deps.CA.CertificatePath()); err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		return map[string]string{"status": "trusted"}, nil
	case "ca.revoke_trust":
		if s.deps.CA == nil || s.deps.TrustStore == nil {
			return nil, &rpcError{Code: -32000, Message: "certificate trust is not configured"}
		}
		if err := s.deps.TrustStore.RevokeRoot(ctx, s.deps.CA.CertificatePath()); err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		return map[string]string{"status": "revoked"}, nil
	case "ca.rotate":
		if s.deps.CA == nil {
			return nil, &rpcError{Code: -32000, Message: "certificate authority is not configured"}
		}
		if err := s.deps.CA.Rotate(); err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		return map[string]string{
			"status":           "rotated",
			"certificate_path": s.deps.CA.CertificatePath(),
		}, nil
	case "proxy.status":
		if s.deps.Proxy == nil {
			return nil, &rpcError{Code: -32000, Message: "proxy is not configured"}
		}
		return s.deps.Proxy.Status(), nil
	case "proxy.start":
		if s.deps.Proxy == nil {
			return nil, &rpcError{Code: -32000, Message: "proxy is not configured"}
		}
		s.deps.Proxy.StartCapture()
		return s.deps.Proxy.Status(), nil
	case "proxy.stop":
		if s.deps.Proxy == nil {
			return nil, &rpcError{Code: -32000, Message: "proxy is not configured"}
		}
		s.deps.Proxy.StopCapture()
		return s.deps.Proxy.Status(), nil
	case "sessions.list":
		sessions, err := s.deps.Store.ListSessions(ctx)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		return sessions, nil
	case "flows.list":
		params := flowsListParams{Limit: 100}
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				return nil, &rpcError{Code: -32602, Message: "invalid params"}
			}
		}
		flows, err := s.deps.Store.ListFlows(ctx, params.Limit)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		return flows, nil
	case "flows.query":
		query := store.FlowQuery{Limit: 100}
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &query); err != nil {
				return nil, &rpcError{Code: -32602, Message: "invalid params"}
			}
		}
		result, err := s.deps.Store.QueryFlows(ctx, query)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		return result, nil
	case "flows.read":
		params, rpcErr := parseFlowIDParams(req.Params)
		if rpcErr != nil {
			return nil, rpcErr
		}
		flow, err := s.deps.Store.GetFlow(ctx, params.ID)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		detail, err := s.flowDetail(flow)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		return detail, nil
	case "flows.compare":
		var params compareParams
		if err := json.Unmarshal(req.Params, &params); err != nil || len(params.IDs) < 2 {
			return nil, &rpcError{Code: -32602, Message: "compare requires at least two flow ids"}
		}
		comparison, err := s.compareFlows(ctx, params.IDs)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		return comparison, nil
	case "flows.export_curl":
		params, rpcErr := parseFlowIDParams(req.Params)
		if rpcErr != nil {
			return nil, rpcErr
		}
		flow, err := s.deps.Store.GetFlow(ctx, params.ID)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		var body []byte
		if s.deps.Blobs != nil && flow.RequestBlob != "" {
			body, err = s.deps.Blobs.Get(flow.RequestBlob)
			if err != nil {
				return nil, &rpcError{Code: -32000, Message: err.Error()}
			}
		}
		command, err := exporter.Curl(flow, body)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		return map[string]string{"command": command}, nil
	case "flows.export_script":
		var params exportScriptParams
		if err := json.Unmarshal(req.Params, &params); err != nil || params.ID == "" || params.Language == "" {
			return nil, &rpcError{Code: -32602, Message: "invalid params"}
		}
		flow, err := s.deps.Store.GetFlow(ctx, params.ID)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		var body []byte
		if s.deps.Blobs != nil && flow.RequestBlob != "" {
			body, err = s.deps.Blobs.Get(flow.RequestBlob)
			if err != nil {
				return nil, &rpcError{Code: -32000, Message: err.Error()}
			}
		}
		var script string
		switch params.Language {
		case "typescript", "ts":
			script, err = exporter.TypeScript(flow, body)
		case "python", "py":
			script, err = exporter.Python(flow, body)
		default:
			return nil, &rpcError{Code: -32602, Message: "unsupported language"}
		}
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		return map[string]string{"script": script}, nil
	case "workflows.export_project":
		if s.deps.Blobs == nil {
			return nil, &rpcError{Code: -32000, Message: "blob store is not configured"}
		}
		var params exportWorkflowParams
		if err := json.Unmarshal(req.Params, &params); err != nil || params.WorkflowID == "" || params.Language == "" {
			return nil, &rpcError{Code: -32602, Message: "invalid params"}
		}
		workflow, err := s.deps.Store.GetWorkflow(ctx, params.WorkflowID)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		var project exporter.Project
		switch params.Language {
		case "typescript", "ts":
			project, err = exporter.TypeScriptProject(ctx, s.deps.Store, s.deps.Blobs, exporter.ProjectParams{Workflow: workflow, Language: params.Language, ServerURL: params.ServerURL})
		case "python", "py":
			project, err = exporter.PythonProject(ctx, s.deps.Store, s.deps.Blobs, exporter.ProjectParams{Workflow: workflow, Language: params.Language, ServerURL: params.ServerURL})
		default:
			return nil, &rpcError{Code: -32602, Message: "unsupported language"}
		}
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		if err := s.saveExportArtifact(ctx, workflow.ID, "project:"+project.Language, project.Files); err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		return project, nil
	case "workflows.export_openapi":
		var params exportWorkflowParams
		if err := json.Unmarshal(req.Params, &params); err != nil || params.WorkflowID == "" {
			return nil, &rpcError{Code: -32602, Message: "invalid params"}
		}
		workflow, err := s.deps.Store.GetWorkflow(ctx, params.WorkflowID)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		document, err := exporter.OpenAPIDocument(ctx, s.deps.Store, workflow, params.ServerURL)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		if err := s.saveExportArtifact(ctx, workflow.ID, "openapi", []exporter.ProjectFile{{Path: "openapi.json", Content: mustMarshalString(document)}}); err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		return document, nil
	case "workflows.export_postman":
		if s.deps.Blobs == nil {
			return nil, &rpcError{Code: -32000, Message: "blob store is not configured"}
		}
		var params exportWorkflowParams
		if err := json.Unmarshal(req.Params, &params); err != nil || params.WorkflowID == "" {
			return nil, &rpcError{Code: -32602, Message: "invalid params"}
		}
		workflow, err := s.deps.Store.GetWorkflow(ctx, params.WorkflowID)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		collection, err := exporter.Postman(ctx, s.deps.Store, s.deps.Blobs, workflow)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		if err := s.saveExportArtifact(ctx, workflow.ID, "postman", []exporter.ProjectFile{{Path: "postman.json", Content: mustMarshalString(collection)}}); err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		return collection, nil
	case "exports.list":
		var params exportArtifactListParams
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				return nil, &rpcError{Code: -32602, Message: "invalid params"}
			}
		}
		artifacts, err := s.deps.Store.ListExportArtifacts(ctx, params.WorkflowID, params.Limit)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		return artifacts, nil
	case "fidelity.profiles.list":
		return fidelity.Profiles(), nil
	case "fidelity.diagnose":
		var params struct {
			FlowID string `json:"flow_id"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil || params.FlowID == "" {
			return nil, &rpcError{Code: -32602, Message: "invalid params"}
		}
		diagnostics, err := fidelity.Diagnose(ctx, s.deps.Store, params.FlowID)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		return diagnostics, nil
	case "replay.run":
		if s.deps.Blobs == nil {
			return nil, &rpcError{Code: -32000, Message: "blob store is not configured"}
		}
		var params replay.Params
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, &rpcError{Code: -32602, Message: "invalid params"}
		}
		engine, err := replay.New(s.deps.Store, s.deps.Blobs)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		result, err := engine.Run(ctx, params)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		return result, nil
	case "replay.variations":
		if s.deps.Blobs == nil {
			return nil, &rpcError{Code: -32000, Message: "blob store is not configured"}
		}
		var params replay.VariationParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, &rpcError{Code: -32602, Message: "invalid params"}
		}
		engine, err := replay.New(s.deps.Store, s.deps.Blobs)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		results, err := engine.RunVariations(ctx, params)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		return results, nil
	case "replay.runs.list":
		var params replayListParams
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				return nil, &rpcError{Code: -32602, Message: "invalid params"}
			}
		}
		runs, err := s.deps.Store.ListReplayRuns(ctx, params.OriginalFlowID, params.Limit)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		return runs, nil
	case "replay.runs.read":
		var params struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil || params.ID == "" {
			return nil, &rpcError{Code: -32602, Message: "invalid params"}
		}
		run, err := s.deps.Store.GetReplayRun(ctx, params.ID)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		return run, nil
	case "har.import":
		if s.deps.Blobs == nil {
			return nil, &rpcError{Code: -32000, Message: "blob store is not configured"}
		}
		var params harImportParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, &rpcError{Code: -32602, Message: "invalid params"}
		}
		importer := harimport.New(s.deps.Store, s.deps.Blobs)
		var result harimport.Result
		var err error
		switch {
		case params.Path != "":
			result, err = importer.ImportFile(ctx, params.Path, params.Options)
		case len(params.Data) > 0:
			result, err = importer.Import(ctx, params.Data, params.Options)
		default:
			return nil, &rpcError{Code: -32602, Message: "path or data is required"}
		}
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		return result, nil
	case "workflows.suggest":
		if s.deps.Blobs == nil {
			return nil, &rpcError{Code: -32000, Message: "blob store is not configured"}
		}
		var params workflowbuilder.SuggestParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, &rpcError{Code: -32602, Message: "invalid params"}
		}
		builder := workflowbuilder.NewBuilder(s.deps.Store, s.deps.Blobs)
		workflow, err := builder.Suggest(ctx, params)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		return workflow, nil
	case "workflows.save":
		var params workflowSaveParams
		if err := json.Unmarshal(req.Params, &params); err != nil || params.Workflow.Name == "" {
			return nil, &rpcError{Code: -32602, Message: "invalid params"}
		}
		builder := workflowbuilder.NewBuilder(s.deps.Store, s.deps.Blobs)
		workflow, err := builder.Save(ctx, params.Workflow)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		return workflow, nil
	case "workflows.list":
		workflows, err := s.deps.Store.ListWorkflows(ctx)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		return workflows, nil
	case "workflows.read":
		var params struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil || params.ID == "" {
			return nil, &rpcError{Code: -32602, Message: "invalid params"}
		}
		workflow, err := s.deps.Store.GetWorkflow(ctx, params.ID)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		return workflow, nil
	case "workflows.run":
		if s.deps.Blobs == nil {
			return nil, &rpcError{Code: -32000, Message: "blob store is not configured"}
		}
		var params workflowbuilder.RunParams
		if err := json.Unmarshal(req.Params, &params); err != nil || params.WorkflowID == "" {
			return nil, &rpcError{Code: -32602, Message: "invalid params"}
		}
		workflow, err := s.deps.Store.GetWorkflow(ctx, params.WorkflowID)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		if params.AuthProfileID != "" {
			profile, err := s.deps.Store.GetAuthProfile(ctx, params.AuthProfileID)
			if err != nil {
				return nil, &rpcError{Code: -32000, Message: err.Error()}
			}
			if profile.SecretBundleID == "" {
				if profile.InteractiveLoginRequired {
					return nil, &rpcError{Code: -32000, Message: "interactive login required for auth profile"}
				}
				return nil, &rpcError{Code: -32000, Message: "auth profile has no secret bundle"}
			}
			vault, err := auth.NewVault(s.deps.Store, s.deps.DataDir)
			if err != nil {
				return nil, &rpcError{Code: -32000, Message: err.Error()}
			}
			material, err := vault.Open(ctx, profile.SecretBundleID)
			if err != nil {
				return nil, &rpcError{Code: -32000, Message: err.Error()}
			}
			params.AuthMaterial = material
		}
		builder := workflowbuilder.NewBuilder(s.deps.Store, s.deps.Blobs)
		run, err := builder.Run(ctx, workflow, params)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		return run, nil
	case "workflow_runs.list":
		var params workflowRunListParams
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				return nil, &rpcError{Code: -32602, Message: "invalid params"}
			}
		}
		runs, err := s.deps.Store.ListWorkflowRuns(ctx, params.WorkflowID, params.Limit)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		return runs, nil
	case "auth.trace":
		if s.deps.Blobs == nil {
			return nil, &rpcError{Code: -32000, Message: "blob store is not configured"}
		}
		var params auth.TraceParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, &rpcError{Code: -32602, Message: "invalid params"}
		}
		analyzer := auth.NewAnalyzer(s.deps.Store, s.deps.Blobs)
		chain, err := analyzer.Trace(ctx, params)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		return chain, nil
	case "auth.chains.list":
		var params authChainListParams
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				return nil, &rpcError{Code: -32602, Message: "invalid params"}
			}
		}
		chains, err := s.deps.Store.ListAuthChains(ctx, params.SessionID, params.Host, params.Limit)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		return chains, nil
	case "auth.chains.read":
		var params struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil || params.ID == "" {
			return nil, &rpcError{Code: -32602, Message: "invalid params"}
		}
		chain, err := s.deps.Store.GetAuthChain(ctx, params.ID)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		return chain, nil
	case "auth.profiles.save":
		var params authProfileSaveParams
		if err := json.Unmarshal(req.Params, &params); err != nil || params.Profile.Name == "" {
			return nil, &rpcError{Code: -32602, Message: "invalid params"}
		}
		profile := params.Profile
		if profile.ID == "" {
			var err error
			profile.ID, err = id.New("auth_profile")
			if err != nil {
				return nil, &rpcError{Code: -32000, Message: err.Error()}
			}
		}
		if err := s.deps.Store.SaveAuthProfile(ctx, profile); err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		return profile, nil
	case "auth.profiles.list":
		var params authProfileListParams
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				return nil, &rpcError{Code: -32602, Message: "invalid params"}
			}
		}
		profiles, err := s.deps.Store.ListAuthProfiles(ctx, params.Host, params.Limit)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		return profiles, nil
	case "auth.profiles.read":
		var params struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil || params.ID == "" {
			return nil, &rpcError{Code: -32602, Message: "invalid params"}
		}
		profile, err := s.deps.Store.GetAuthProfile(ctx, params.ID)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		return profile, nil
	case "auth.vault.save":
		var params authVaultSaveParams
		if err := json.Unmarshal(req.Params, &params); err != nil || params.ProfileID == "" || len(params.Material) == 0 {
			return nil, &rpcError{Code: -32602, Message: "invalid params"}
		}
		profile, err := s.deps.Store.GetAuthProfile(ctx, params.ProfileID)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		vault, err := auth.NewVault(s.deps.Store, s.deps.DataDir)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		bundle, err := vault.Save(ctx, params.ProfileID, params.Material)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		profile.SecretBundleID = bundle.ID
		if err := s.deps.Store.SaveAuthProfile(ctx, profile); err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		return map[string]string{"bundle_id": bundle.ID}, nil
	case "hosts.list":
		params := limitParams{Limit: 100}
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				return nil, &rpcError{Code: -32602, Message: "invalid params"}
			}
		}
		hosts, err := s.deps.Store.HostCounts(ctx, params.Limit)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		return hosts, nil
	case "apps.list":
		params := limitParams{Limit: 100}
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				return nil, &rpcError{Code: -32602, Message: "invalid params"}
			}
		}
		apps, err := s.deps.Store.AppCounts(ctx, params.Limit)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		return apps, nil
	case "tags.list":
		tags, err := s.deps.Store.ListTags(ctx)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		return tags, nil
	case "tags.add":
		params, rpcErr := parseTagParams(req.Params)
		if rpcErr != nil {
			return nil, rpcErr
		}
		if err := s.deps.Store.TagFlow(ctx, params.FlowID, params.Tag); err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		return map[string]string{"status": "tagged"}, nil
	case "tags.remove":
		params, rpcErr := parseTagParams(req.Params)
		if rpcErr != nil {
			return nil, rpcErr
		}
		if err := s.deps.Store.UntagFlow(ctx, params.FlowID, params.Tag); err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		return map[string]string{"status": "untagged"}, nil
	case "filters.list":
		filters, err := s.deps.Store.ListSavedFilters(ctx)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		return filters, nil
	case "filters.save":
		var params savedFilterParams
		if err := json.Unmarshal(req.Params, &params); err != nil || params.Name == "" || !json.Valid(params.Query) {
			return nil, &rpcError{Code: -32602, Message: "invalid params"}
		}
		filterID := params.ID
		if filterID == "" {
			var err error
			filterID, err = id.New("filter")
			if err != nil {
				return nil, &rpcError{Code: -32000, Message: err.Error()}
			}
		}
		filter := store.SavedFilter{ID: filterID, Name: params.Name, Query: params.Query}
		if err := s.deps.Store.SaveFilter(ctx, filter); err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		return map[string]string{"id": filterID}, nil
	case "filters.delete":
		var params struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil || params.ID == "" {
			return nil, &rpcError{Code: -32602, Message: "invalid params"}
		}
		if err := s.deps.Store.DeleteSavedFilter(ctx, params.ID); err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		return map[string]string{"status": "deleted"}, nil
	case "system_proxy.read":
		params, rpcErr := parseSystemProxyParams(req.Params)
		if rpcErr != nil {
			return nil, rpcErr
		}
		if s.deps.SystemProxy == nil {
			return nil, &rpcError{Code: -32000, Message: "system proxy manager is not configured"}
		}
		state, err := s.deps.SystemProxy.Read(ctx, params.Service)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		return state, nil
	case "system_proxy.enable":
		params, rpcErr := parseSystemProxyParams(req.Params)
		if rpcErr != nil {
			return nil, rpcErr
		}
		host, port, err := s.proxyEndpoint(params)
		if err != nil {
			return nil, &rpcError{Code: -32602, Message: err.Error()}
		}
		if s.deps.SystemProxy == nil {
			return nil, &rpcError{Code: -32000, Message: "system proxy manager is not configured"}
		}
		if err := s.deps.SystemProxy.Enable(ctx, params.Service, host, port); err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		return map[string]string{"status": "enabled"}, nil
	case "system_proxy.disable":
		params, rpcErr := parseSystemProxyParams(req.Params)
		if rpcErr != nil {
			return nil, rpcErr
		}
		if s.deps.SystemProxy == nil {
			return nil, &rpcError{Code: -32000, Message: "system proxy manager is not configured"}
		}
		if err := s.deps.SystemProxy.Disable(ctx, params.Service); err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		return map[string]string{"status": "restored"}, nil
	default:
		return nil, &rpcError{Code: -32601, Message: "method not found"}
	}
}

func (s *Server) flowDetail(flow store.Flow) (flowDetail, error) {
	detail := flowDetail{Flow: flow}
	if err := json.Unmarshal(flow.RequestHeaders, &detail.RequestHeaders); err != nil {
		return flowDetail{}, fmt.Errorf("decode request headers: %w", err)
	}
	if err := json.Unmarshal(flow.ResponseHeaders, &detail.ResponseHeaders); err != nil {
		return flowDetail{}, fmt.Errorf("decode response headers: %w", err)
	}
	detail.RequestHeaders = redact.Header(detail.RequestHeaders)
	detail.ResponseHeaders = redact.Header(detail.ResponseHeaders)
	parsedURL, err := url.Parse(flow.URL)
	if err == nil {
		detail.Query = parsedURL.Query()
	}
	request := http.Request{Header: detail.RequestHeaders}
	detail.RequestCookies = request.Cookies()
	response := http.Response{Header: detail.ResponseHeaders}
	detail.ResponseCookies = response.Cookies()
	if s.deps.Blobs != nil && flow.RequestBlob != "" {
		body, err := s.deps.Blobs.Get(flow.RequestBlob)
		if err != nil {
			return flowDetail{}, err
		}
		redactedBody := redact.Body(body)
		detail.RequestBody = string(redactedBody)
		detail.RequestParsed = bodyparse.Parse(detail.RequestHeaders, redactedBody)
		detail.RequestJSON = detail.RequestParsed.JSON
	}
	if s.deps.Blobs != nil && flow.ResponseBlob != "" {
		body, err := s.deps.Blobs.Get(flow.ResponseBlob)
		if err != nil {
			return flowDetail{}, err
		}
		redactedBody := redact.Body(body)
		detail.ResponseBody = string(redactedBody)
		detail.ResponseParsed = bodyparse.Parse(detail.ResponseHeaders, redactedBody)
		detail.ResponseJSON = detail.ResponseParsed.JSON
	}
	return detail, nil
}

func (s *Server) compareFlows(ctx context.Context, ids []string) (flowComparison, error) {
	comparison := flowComparison{}
	for _, id := range ids {
		flow, err := s.deps.Store.GetFlow(ctx, id)
		if err != nil {
			return flowComparison{}, err
		}
		detail, err := s.flowDetail(flow)
		if err != nil {
			return flowComparison{}, err
		}
		comparison.Flows = append(comparison.Flows, detail)
	}
	for _, candidate := range []struct {
		field string
		value func(flowDetail) any
	}{
		{"method", func(d flowDetail) any { return d.Flow.Method }},
		{"url", func(d flowDetail) any { return d.Flow.URL }},
		{"host", func(d flowDetail) any { return d.Flow.Host }},
		{"status", func(d flowDetail) any { return d.Flow.Status }},
		{"query", func(d flowDetail) any { return d.Query }},
		{"request_headers", func(d flowDetail) any { return d.RequestHeaders }},
		{"response_headers", func(d flowDetail) any { return d.ResponseHeaders }},
		{"request_body", func(d flowDetail) any { return d.RequestBody }},
		{"response_body", func(d flowDetail) any { return d.ResponseBody }},
	} {
		values := map[string]any{}
		var first any
		changed := false
		for i, detail := range comparison.Flows {
			value := candidate.value(detail)
			values[detail.Flow.ID] = value
			if i == 0 {
				first = value
				continue
			}
			if !reflect.DeepEqual(first, value) {
				changed = true
			}
		}
		if changed {
			comparison.Differences = append(comparison.Differences, difference{Field: candidate.field, Values: values})
		}
	}
	return comparison, nil
}

func (s *Server) proxyEndpoint(params systemProxyParams) (string, int, error) {
	if params.Host != "" && params.Port > 0 {
		return params.Host, params.Port, nil
	}
	host, portString, err := net.SplitHostPort(s.deps.ProxyAddr)
	if err != nil {
		return "", 0, fmt.Errorf("proxy endpoint must include host and port")
	}
	port, err := strconv.Atoi(portString)
	if err != nil {
		return "", 0, fmt.Errorf("proxy port is invalid")
	}
	return host, port, nil
}

func (s *Server) saveExportArtifact(ctx context.Context, workflowID, kind string, files []exporter.ProjectFile) error {
	artifactID, err := id.New("export")
	if err != nil {
		return err
	}
	filesJSON, err := json.Marshal(files)
	if err != nil {
		return err
	}
	return s.deps.Store.SaveExportArtifact(ctx, store.ExportArtifact{
		ID:         artifactID,
		WorkflowID: workflowID,
		Kind:       kind,
		FilesJSON:  filesJSON,
	})
}

func mustMarshalString(value any) string {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(data) + "\n"
}

func parseTagParams(raw json.RawMessage) (tagParams, *rpcError) {
	if len(raw) == 0 {
		return tagParams{}, &rpcError{Code: -32602, Message: "missing params"}
	}
	var params tagParams
	if err := json.Unmarshal(raw, &params); err != nil || params.FlowID == "" || params.Tag == "" {
		return tagParams{}, &rpcError{Code: -32602, Message: "invalid params"}
	}
	return params, nil
}

func parseSystemProxyParams(raw json.RawMessage) (systemProxyParams, *rpcError) {
	if len(raw) == 0 {
		return systemProxyParams{}, &rpcError{Code: -32602, Message: "missing params"}
	}
	var params systemProxyParams
	if err := json.Unmarshal(raw, &params); err != nil || params.Service == "" {
		return systemProxyParams{}, &rpcError{Code: -32602, Message: "invalid params"}
	}
	return params, nil
}

func parseFlowIDParams(raw json.RawMessage) (flowIDParams, *rpcError) {
	if len(raw) == 0 {
		return flowIDParams{}, &rpcError{Code: -32602, Message: "missing params"}
	}
	var params flowIDParams
	if err := json.Unmarshal(raw, &params); err != nil || params.ID == "" {
		return flowIDParams{}, &rpcError{Code: -32602, Message: "invalid params"}
	}
	return params, nil
}

func errorResponse(id json.RawMessage, code int, message string) response {
	return response{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &rpcError{Code: code, Message: message},
	}
}

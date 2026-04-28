package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/dhiyaan/deconstruct/daemon/internal/blobstore"
	"github.com/dhiyaan/deconstruct/daemon/internal/exporter"
	"github.com/dhiyaan/deconstruct/daemon/internal/harimport"
	"github.com/dhiyaan/deconstruct/daemon/internal/store"
	workflowbuilder "github.com/dhiyaan/deconstruct/daemon/internal/workflow"
)

type HAREvalParams struct {
	Name           string `json:"name,omitempty"`
	HARPath        string `json:"har_path,omitempty"`
	OutputDir      string `json:"output_dir,omitempty"`
	Replay         bool   `json:"replay,omitempty"`
	Redact         bool   `json:"redact,omitempty"`
	ServerURL      string `json:"server_url,omitempty"`
	IncludeSecrets bool   `json:"include_secrets,omitempty"`
}

type HAREvalReport struct {
	Name           string             `json:"name"`
	HARPath        string             `json:"har_path,omitempty"`
	SessionID      string             `json:"session_id"`
	ImportedFlows  int                `json:"imported_flows"`
	FlowIDs        []string           `json:"flow_ids"`
	IncludedSteps  int                `json:"included_steps"`
	ExcludedSteps  int                `json:"excluded_steps"`
	ExtractedKeys  int                `json:"extracted_keys"`
	BoundTemplates int                `json:"bound_templates"`
	WorkflowID     string             `json:"workflow_id"`
	ReplayRun      *store.WorkflowRun `json:"replay_run,omitempty"`
	Exports        map[string]int     `json:"exports"`
	OutputDir      string             `json:"output_dir,omitempty"`
	Failures       []string           `json:"failures,omitempty"`
	CreatedAt      time.Time          `json:"created_at"`
}

func RunHAR(ctx context.Context, db *store.Store, blobs *blobstore.Store, params HAREvalParams) (HAREvalReport, error) {
	if params.HARPath == "" {
		return HAREvalReport{}, fmt.Errorf("har_path is required")
	}
	name := params.Name
	if name == "" {
		name = filepath.Base(params.HARPath)
	}
	report := HAREvalReport{Name: name, HARPath: params.HARPath, Exports: map[string]int{}, CreatedAt: time.Now().UTC()}
	redact := params.Redact
	importResult, err := harimport.New(db, blobs).ImportFile(ctx, params.HARPath, harimport.Options{Name: name, Redact: &redact})
	if err != nil {
		return report, err
	}
	report.SessionID = importResult.SessionID
	report.ImportedFlows = importResult.Flows
	flowIDs, err := sessionFlowIDs(ctx, db, importResult.SessionID)
	if err != nil {
		return report, err
	}
	report.FlowIDs = flowIDs
	if len(flowIDs) == 0 {
		report.Failures = append(report.Failures, "HAR imported zero flows")
		return report, nil
	}
	builder := workflowbuilder.NewBuilder(db, blobs)
	workflow, err := builder.Suggest(ctx, workflowbuilder.SuggestParams{Name: name, FlowIDs: flowIDs})
	if err != nil {
		return report, err
	}
	workflow, err = builder.Save(ctx, workflow)
	if err != nil {
		return report, err
	}
	report.WorkflowID = workflow.ID
	for _, step := range workflow.Steps {
		if step.Included {
			report.IncludedSteps++
		} else {
			report.ExcludedSteps++
		}
		report.ExtractedKeys += len(step.Extract)
		if len(step.Overrides) > 0 {
			report.BoundTemplates++
		}
	}
	if report.IncludedSteps == 0 {
		report.Failures = append(report.Failures, "compiler included zero executable steps")
	}
	tsProject, err := exporter.TypeScriptProject(ctx, db, blobs, exporter.ProjectParams{Workflow: workflow, Language: "typescript", ServerURL: params.ServerURL, IncludeSecrets: params.IncludeSecrets})
	if err != nil {
		return report, err
	}
	pyProject, err := exporter.PythonProject(ctx, db, blobs, exporter.ProjectParams{Workflow: workflow, Language: "python", ServerURL: params.ServerURL, IncludeSecrets: params.IncludeSecrets})
	if err != nil {
		return report, err
	}
	openAPI, err := exporter.OpenAPIDocument(ctx, db, workflow, params.ServerURL)
	if err != nil {
		return report, err
	}
	postman, err := exporter.Postman(ctx, db, blobs, workflow)
	if err != nil {
		return report, err
	}
	report.Exports["typescript_files"] = len(tsProject.Files)
	report.Exports["python_files"] = len(pyProject.Files)
	report.Exports["openapi_paths"] = len(openAPI.Paths)
	report.Exports["postman_items"] = len(postman.Item)
	if params.OutputDir != "" {
		if err := writeOutputs(params.OutputDir, report, tsProject, pyProject, openAPI, postman); err != nil {
			return report, err
		}
		report.OutputDir = params.OutputDir
	}
	if params.Replay {
		run, err := builder.Run(ctx, workflow, workflowbuilder.RunParams{WorkflowID: workflow.ID, Inputs: schemaDefaults(workflow.InputSchema)})
		if err != nil {
			report.Failures = append(report.Failures, err.Error())
		} else {
			report.ReplayRun = &run
			if !run.OK {
				report.Failures = append(report.Failures, "workflow replay failed: "+run.Error)
			}
		}
	}
	return report, nil
}

func schemaDefaults(schema json.RawMessage) map[string]any {
	var decoded struct {
		Properties map[string]map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(schema, &decoded); err != nil {
		return nil
	}
	defaults := map[string]any{}
	for key, property := range decoded.Properties {
		if value, ok := property["default"]; ok {
			defaults[key] = value
		}
	}
	return defaults
}

func sessionFlowIDs(ctx context.Context, db *store.Store, sessionID string) ([]string, error) {
	result, err := db.QueryFlows(ctx, store.FlowQuery{SessionID: sessionID, Limit: 1000, SortBy: "time", SortDir: "asc"})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(result.Flows))
	for _, flow := range result.Flows {
		ids = append(ids, flow.ID)
	}
	return ids, nil
}

func writeOutputs(outputDir string, report HAREvalReport, tsProject, pyProject exporter.Project, openAPI exporter.OpenAPI, postman exporter.PostmanCollection) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}
	for _, file := range tsProject.Files {
		if err := writeFile(filepath.Join(outputDir, "typescript", file.Path), file.Content); err != nil {
			return err
		}
	}
	for _, file := range pyProject.Files {
		if err := writeFile(filepath.Join(outputDir, "python", file.Path), file.Content); err != nil {
			return err
		}
	}
	openAPIJSON, _ := json.MarshalIndent(openAPI, "", "  ")
	if err := writeFile(filepath.Join(outputDir, "openapi.json"), string(openAPIJSON)+"\n"); err != nil {
		return err
	}
	postmanJSON, _ := json.MarshalIndent(postman, "", "  ")
	if err := writeFile(filepath.Join(outputDir, "postman.json"), string(postmanJSON)+"\n"); err != nil {
		return err
	}
	reportJSON, _ := json.MarshalIndent(report, "", "  ")
	return writeFile(filepath.Join(outputDir, "report.json"), string(reportJSON)+"\n")
}

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func Failed(report HAREvalReport) bool {
	return len(report.Failures) > 0
}

func SortReports(reports []HAREvalReport) {
	sort.Slice(reports, func(i, j int) bool { return reports[i].Name < reports[j].Name })
}

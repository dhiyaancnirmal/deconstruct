package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/dhiyaan/deconstruct/daemon/internal/blobstore"
	"github.com/dhiyaan/deconstruct/daemon/internal/id"
	"github.com/dhiyaan/deconstruct/daemon/internal/replay"
	"github.com/dhiyaan/deconstruct/daemon/internal/store"
)

type Store interface {
	GetFlow(context.Context, string) (store.Flow, error)
	SaveWorkflow(context.Context, store.Workflow) error
	InsertWorkflowRun(context.Context, store.WorkflowRun) error
}

type Builder struct {
	store Store
	blobs *blobstore.Store
}

type SuggestParams struct {
	Name    string   `json:"name,omitempty"`
	FlowIDs []string `json:"flow_ids"`
}

type RunParams struct {
	WorkflowID    string            `json:"workflow_id"`
	Inputs        map[string]any    `json:"inputs,omitempty"`
	DryRun        bool              `json:"dry_run,omitempty"`
	AuthProfileID string            `json:"auth_profile_id,omitempty"`
	AuthMaterial  map[string]string `json:"-"`
}

func NewBuilder(store Store, blobs *blobstore.Store) *Builder {
	return &Builder{store: store, blobs: blobs}
}

func (b *Builder) Suggest(ctx context.Context, params SuggestParams) (store.Workflow, error) {
	if len(params.FlowIDs) == 0 {
		return store.Workflow{}, fmt.Errorf("at least one flow id is required")
	}
	workflowID, err := id.New("workflow")
	if err != nil {
		return store.Workflow{}, err
	}
	name := params.Name
	if name == "" {
		name = "Captured workflow"
	}
	workflow := store.Workflow{
		ID:           workflowID,
		Name:         name,
		InputSchema:  json.RawMessage(`{"type":"object","properties":{}}`),
		OutputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		CreatedAt:    time.Now().UTC(),
	}
	properties := map[string]any{}
	required := []string{}
	for index, flowID := range params.FlowIDs {
		flow, err := b.store.GetFlow(ctx, flowID)
		if err != nil {
			return store.Workflow{}, err
		}
		class, included, reason := classify(flow)
		stepID := fmt.Sprintf("step_%02d", index+1)
		step := store.WorkflowStep{
			ID:       stepID,
			FlowID:   flow.ID,
			Name:     flow.Method + " " + flow.URL,
			Class:    class,
			Included: included,
			Reason:   reason,
			Assertions: store.WorkflowAssertions{
				Status: expectedStatuses(flow.Status),
			},
		}
		for _, field := range b.inferInputFields(flow) {
			properties[field] = map[string]string{"type": "string"}
			required = append(required, field)
		}
		if step.Included {
			if bodyTemplate := b.inferBodyTemplate(flow); bodyTemplate != "" {
				overrides := replay.Overrides{Body: &bodyTemplate}
				overrideBytes, err := json.Marshal(overrides)
				if err != nil {
					return store.Workflow{}, err
				}
				step.Overrides = overrideBytes
			}
		}
		workflow.Steps = append(workflow.Steps, step)
	}
	sort.Strings(required)
	inputSchema, err := json.Marshal(map[string]any{
		"type":       "object",
		"properties": properties,
		"required":   unique(required),
	})
	if err != nil {
		return store.Workflow{}, err
	}
	workflow.InputSchema = inputSchema
	return workflow, nil
}

func (b *Builder) Save(ctx context.Context, workflow store.Workflow) (store.Workflow, error) {
	if workflow.ID == "" {
		workflowID, err := id.New("workflow")
		if err != nil {
			return store.Workflow{}, err
		}
		workflow.ID = workflowID
	}
	if workflow.CreatedAt.IsZero() {
		workflow.CreatedAt = time.Now().UTC()
	}
	if err := b.store.SaveWorkflow(ctx, workflow); err != nil {
		return store.Workflow{}, err
	}
	return workflow, nil
}

func (b *Builder) Run(ctx context.Context, workflow store.Workflow, params RunParams) (store.WorkflowRun, error) {
	runID, err := id.New("workflow_run")
	if err != nil {
		return store.WorkflowRun{}, err
	}
	storeBackend, ok := b.store.(*store.Store)
	if !ok {
		return store.WorkflowRun{}, fmt.Errorf("workflow runner requires store backend")
	}
	engine, err := replay.New(storeBackend, b.blobs)
	if err != nil {
		return store.WorkflowRun{}, err
	}
	run := store.WorkflowRun{
		ID:         runID,
		WorkflowID: workflow.ID,
		OK:         true,
		CreatedAt:  time.Now().UTC(),
	}
	for _, step := range workflow.Steps {
		if !step.Included {
			continue
		}
		overrides, err := stepOverrides(step, params.Inputs)
		if err != nil {
			run.OK = false
			run.Error = err.Error()
			run.StepRuns = append(run.StepRuns, store.WorkflowStepRun{StepID: step.ID, FlowID: step.FlowID, OK: false, Error: err.Error()})
			break
		}
		applyAuthMaterial(&overrides, params.AuthMaterial)
		result, err := engine.Run(ctx, replay.Params{FlowID: step.FlowID, Overrides: overrides, DryRun: params.DryRun})
		if err != nil {
			run.OK = false
			run.Error = err.Error()
			run.StepRuns = append(run.StepRuns, store.WorkflowStepRun{StepID: step.ID, FlowID: step.FlowID, OK: false, Error: err.Error()})
			break
		}
		ok := assertStatus(result.Run.Status, step.Assertions.Status, params.DryRun)
		if !ok {
			run.OK = false
			run.Error = fmt.Sprintf("step %s status assertion failed: %d", step.ID, result.Run.Status)
		}
		run.StepRuns = append(run.StepRuns, store.WorkflowStepRun{
			StepID:      step.ID,
			FlowID:      step.FlowID,
			ReplayRunID: result.Run.ID,
			OK:          ok,
			Status:      result.Run.Status,
			Error:       result.Run.Error,
		})
		if !ok {
			break
		}
	}
	if err := b.store.InsertWorkflowRun(ctx, run); err != nil {
		return store.WorkflowRun{}, err
	}
	return run, nil
}

func classify(flow store.Flow) (string, bool, string) {
	method := strings.ToUpper(flow.Method)
	if method == "POST" || method == "PUT" || method == "PATCH" || method == "DELETE" {
		return "primary_action", true, "mutating method"
	}
	path := flow.URL
	if parsed, err := url.Parse(flow.URL); err == nil {
		path = parsed.Path
	}
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".css", ".js", ".map", ".png", ".jpg", ".jpeg", ".gif", ".svg", ".ico", ".woff", ".woff2":
		return "static_asset", false, "static asset extension"
	}
	if strings.Contains(strings.ToLower(path), "csrf") || strings.Contains(strings.ToLower(path), "token") || strings.Contains(strings.ToLower(path), "auth") {
		return "auth_session", true, "auth/token path"
	}
	return "unknown", false, "no deterministic action signal"
}

func expectedStatuses(status int) []int {
	if status >= 200 && status < 400 {
		return []int{status}
	}
	return []int{200, 201, 202, 204}
}

func (b *Builder) inferInputFields(flow store.Flow) []string {
	if b.blobs == nil || flow.RequestBlob == "" {
		return nil
	}
	body, err := b.blobs.Get(flow.RequestBlob)
	if err != nil {
		return nil
	}
	var object map[string]any
	if err := json.Unmarshal(body, &object); err != nil {
		return nil
	}
	var fields []string
	for key, value := range object {
		if _, ok := value.(string); ok && !sensitiveField(key) {
			fields = append(fields, key)
		}
	}
	sort.Strings(fields)
	return fields
}

func (b *Builder) inferBodyTemplate(flow store.Flow) string {
	if b.blobs == nil || flow.RequestBlob == "" {
		return ""
	}
	body, err := b.blobs.Get(flow.RequestBlob)
	if err != nil {
		return ""
	}
	var object map[string]any
	if err := json.Unmarshal(body, &object); err != nil {
		return ""
	}
	changed := false
	for key, value := range object {
		if _, ok := value.(string); ok && !sensitiveField(key) {
			object[key] = "${input." + key + "}"
			changed = true
		}
	}
	if !changed {
		return ""
	}
	templated, err := json.Marshal(object)
	if err != nil {
		return ""
	}
	return string(templated)
}

func applyAuthMaterial(overrides *replay.Overrides, material map[string]string) {
	if len(material) == 0 {
		return
	}
	if overrides.Headers == nil {
		overrides.Headers = map[string][]string{}
	}
	if value := material["authorization"]; value != "" {
		overrides.Headers["Authorization"] = []string{value}
	}
	if value := material["Authorization"]; value != "" {
		overrides.Headers["Authorization"] = []string{value}
	}
	if value := material["cookie"]; value != "" {
		overrides.Headers["Cookie"] = []string{value}
	} else if value := material["Cookie"]; value != "" {
		overrides.Headers["Cookie"] = []string{value}
	} else {
		var cookies []string
		for key, value := range material {
			if strings.HasPrefix(key, "cookie.") && value != "" {
				cookies = append(cookies, strings.TrimPrefix(key, "cookie.")+"="+value)
			}
		}
		sort.Strings(cookies)
		if len(cookies) > 0 {
			overrides.Headers["Cookie"] = []string{strings.Join(cookies, "; ")}
		}
	}
	for _, key := range []string{"x-csrf-token", "x-xsrf-token", "X-CSRF-Token", "X-XSRF-Token"} {
		if value := material[key]; value != "" {
			overrides.Headers[canonicalAuthHeader(key)] = []string{value}
		}
	}
	for key, value := range material {
		if strings.HasPrefix(key, "header.") && value != "" {
			overrides.Headers[strings.TrimPrefix(key, "header.")] = []string{value}
		}
	}
}

func canonicalAuthHeader(key string) string {
	switch strings.ToLower(key) {
	case "x-xsrf-token":
		return "X-XSRF-Token"
	default:
		return "X-CSRF-Token"
	}
}

func stepOverrides(step store.WorkflowStep, inputs map[string]any) (replay.Overrides, error) {
	if len(step.Overrides) == 0 {
		return replay.Overrides{}, nil
	}
	var overrides replay.Overrides
	if err := json.Unmarshal(step.Overrides, &overrides); err != nil {
		return replay.Overrides{}, err
	}
	if overrides.Body != nil {
		body := *overrides.Body
		for key, value := range inputs {
			body = strings.ReplaceAll(body, "${input."+key+"}", fmt.Sprint(value))
		}
		overrides.Body = &body
	}
	for key, value := range overrides.Query {
		overrides.Query[key] = replaceInputs(value, inputs)
	}
	for key, values := range overrides.Headers {
		for i, value := range values {
			values[i] = replaceInputs(value, inputs)
		}
		overrides.Headers[key] = values
	}
	return overrides, nil
}

func replaceInputs(value string, inputs map[string]any) string {
	for key, input := range inputs {
		value = strings.ReplaceAll(value, "${input."+key+"}", fmt.Sprint(input))
	}
	return value
}

func assertStatus(status int, allowed []int, dryRun bool) bool {
	if dryRun {
		return true
	}
	if len(allowed) == 0 {
		return status >= 200 && status < 400
	}
	for _, allowedStatus := range allowed {
		if status == allowedStatus {
			return true
		}
	}
	return false
}

func unique(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

var sensitivePattern = regexp.MustCompile(`(?i)(token|secret|password|auth|csrf|session|key)`)

func sensitiveField(name string) bool {
	return sensitivePattern.MatchString(name)
}

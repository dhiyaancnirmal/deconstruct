package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
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
	WorkflowID        string                       `json:"workflow_id"`
	Inputs            map[string]any               `json:"inputs,omitempty"`
	DryRun            bool                         `json:"dry_run,omitempty"`
	AuthProfileID     string                       `json:"auth_profile_id,omitempty"`
	AuthRefreshFlowID string                       `json:"-"`
	ReplayProfile     string                       `json:"replay_profile,omitempty"`
	ProfileOptions    map[string]string            `json:"profile_options,omitempty"`
	AuthMaterial      map[string]string            `json:"-"`
	StepOutputs       map[string]map[string]string `json:"-"`
}

type sourceValue struct {
	StepID string
	Key    string
	Path   string
	Value  string
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
	flows := map[string]store.Flow{}
	for index, flowID := range params.FlowIDs {
		flow, err := b.store.GetFlow(ctx, flowID)
		if err != nil {
			return store.Workflow{}, err
		}
		flows[flow.ID] = flow
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
		for field, value := range b.inferInputDefaults(flow) {
			properties[field] = map[string]string{"type": "string", "default": value}
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
	if err := b.inferCrossStepBindings(workflow.Steps, flows); err != nil {
		return store.Workflow{}, err
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

func (b *Builder) inferCrossStepBindings(steps []store.WorkflowStep, flows map[string]store.Flow) error {
	if b.blobs == nil {
		return nil
	}
	var previous []sourceValue
	for stepIndex := range steps {
		step := &steps[stepIndex]
		flow := flows[step.FlowID]
		if step.Included && len(previous) > 0 {
			overrides, err := stepOverrides(*step, nil, nil)
			if err != nil {
				return err
			}
			changed := false
			headers := http.Header{}
			if len(flow.RequestHeaders) > 0 {
				_ = json.Unmarshal(flow.RequestHeaders, &headers)
			}
			for name, values := range headers {
				for _, value := range values {
					source, ok := matchingSource(previous, value)
					if !ok {
						continue
					}
					if overrides.Headers == nil {
						overrides.Headers = map[string][]string{}
					}
					overrides.Headers[name] = []string{"${steps." + source.StepID + "." + source.Key + "}"}
					ensureExtraction(&steps, source)
					changed = true
				}
			}
			if flow.RequestBlob != "" {
				body, err := b.blobs.Get(flow.RequestBlob)
				if err == nil && len(body) > 0 {
					bodyText := string(body)
					if overrides.Body != nil {
						bodyText = *overrides.Body
					}
					for _, source := range previous {
						if source.Value == "" || !strings.Contains(bodyText, source.Value) {
							continue
						}
						bodyText = strings.ReplaceAll(bodyText, source.Value, "${steps."+source.StepID+"."+source.Key+"}")
						ensureExtraction(&steps, source)
						changed = true
					}
					if changed {
						overrides.Body = &bodyText
					}
				}
			}
			if changed {
				data, err := json.Marshal(overrides)
				if err != nil {
					return err
				}
				step.Overrides = data
			}
		}
		if step.Included && flow.ResponseBlob != "" {
			body, err := b.blobs.Get(flow.ResponseBlob)
			if err == nil {
				for _, source := range jsonPrimitiveSources(step.ID, body) {
					previous = append(previous, source)
				}
			}
		}
	}
	return nil
}

func matchingSource(sources []sourceValue, value string) (sourceValue, bool) {
	if value == "" {
		return sourceValue{}, false
	}
	for _, source := range sources {
		if source.Value == value {
			return source, true
		}
	}
	return sourceValue{}, false
}

func ensureExtraction(steps *[]store.WorkflowStep, source sourceValue) {
	for i := range *steps {
		if (*steps)[i].ID != source.StepID {
			continue
		}
		if (*steps)[i].Extract == nil {
			(*steps)[i].Extract = map[string]string{}
		}
		(*steps)[i].Extract[source.Key] = source.Path
		return
	}
}

func jsonPrimitiveSources(stepID string, body []byte) []sourceValue {
	var root any
	if err := json.Unmarshal(body, &root); err != nil {
		return nil
	}
	var sources []sourceValue
	var walk func(path string, value any)
	walk = func(path string, value any) {
		switch typed := value.(type) {
		case map[string]any:
			for key, child := range typed {
				childPath := "$." + key
				if path != "$" {
					childPath = path + "." + key
				}
				walk(childPath, child)
			}
		case []any:
			for i, child := range typed {
				walk(fmt.Sprintf("%s[%d]", path, i), child)
			}
		case string:
			if typed != "" {
				sources = append(sources, sourceValue{StepID: stepID, Key: extractionKey(path), Path: path, Value: typed})
			}
		case float64, bool:
			sources = append(sources, sourceValue{StepID: stepID, Key: extractionKey(path), Path: path, Value: fmt.Sprint(typed)})
		}
	}
	walk("$", root)
	return sources
}

func extractionKey(path string) string {
	path = strings.TrimPrefix(path, "$.")
	path = strings.ReplaceAll(path, "[", "_")
	path = strings.ReplaceAll(path, "]", "")
	path = strings.ReplaceAll(path, ".", "_")
	path = strings.Trim(path, "_")
	if path == "" {
		return "value"
	}
	if sensitiveField(path) {
		return "value_" + strconv.Itoa(len(path))
	}
	return path
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
	stepOutputs := map[string]map[string]string{}
	for _, step := range workflow.Steps {
		if !step.Included {
			continue
		}
		params.StepOutputs = stepOutputs
		stepRun, err := b.runStep(ctx, engine, step, params)
		if err != nil {
			run.OK = false
			run.Error = err.Error()
			run.StepRuns = append(run.StepRuns, stepRun)
			break
		}
		ok := stepRun.OK
		if !ok {
			if refreshRun, refreshed := b.tryAuthRefresh(ctx, engine, params, stepRun); refreshed {
				run.StepRuns = append(run.StepRuns, stepRun)
				run.StepRuns = append(run.StepRuns, refreshRun)
				if !refreshRun.OK {
					run.OK = false
					run.Error = fmt.Sprintf("auth refresh failed: %s", refreshRun.Error)
					break
				}
				params.StepOutputs = stepOutputs
				retriedStepRun, err := b.runStep(ctx, engine, step, params)
				if err != nil {
					run.OK = false
					run.Error = err.Error()
					run.StepRuns = append(run.StepRuns, retriedStepRun)
					break
				}
				stepRun = retriedStepRun
				ok = stepRun.OK
			}
		}
		if !ok {
			run.OK = false
			run.Error = fmt.Sprintf("step %s assertion failed: %s", step.ID, stepRun.Error)
		}
		run.StepRuns = append(run.StepRuns, stepRun)
		if !ok {
			break
		}
		outputs, err := b.extractStepOutputs(ctx, step, stepRun)
		if err != nil {
			run.OK = false
			run.Error = fmt.Sprintf("step %s extraction failed: %s", step.ID, err)
			break
		}
		if len(outputs) > 0 {
			stepOutputs[step.ID] = outputs
		}
	}
	if err := b.store.InsertWorkflowRun(ctx, run); err != nil {
		return store.WorkflowRun{}, err
	}
	return run, nil
}

func (b *Builder) runStep(ctx context.Context, engine *replay.Engine, step store.WorkflowStep, params RunParams) (store.WorkflowStepRun, error) {
	overrides, err := stepOverrides(step, params.Inputs, params.StepOutputs)
	if err != nil {
		return store.WorkflowStepRun{StepID: step.ID, FlowID: step.FlowID, OK: false, Error: err.Error()}, err
	}
	applyAuthMaterial(&overrides, params.AuthMaterial)
	profile := params.ReplayProfile
	profileOptions := params.ProfileOptions
	if step.ReplayProfile != "" {
		profile = step.ReplayProfile
		profileOptions = step.ProfileOptions
	}
	result, err := engine.Run(ctx, replay.Params{FlowID: step.FlowID, Overrides: overrides, DryRun: params.DryRun, Profile: profile, ProfileOptions: profileOptions})
	if err != nil {
		return store.WorkflowStepRun{StepID: step.ID, FlowID: step.FlowID, OK: false, Error: err.Error()}, err
	}
	ok, assertionError := b.assertStep(ctx, result.Run, step, params.DryRun)
	if result.Run.Error != "" && assertionError == "" {
		assertionError = result.Run.Error
	}
	return store.WorkflowStepRun{
		StepID:      step.ID,
		FlowID:      step.FlowID,
		ReplayRunID: result.Run.ID,
		OK:          ok,
		Status:      result.Run.Status,
		Error:       assertionError,
	}, nil
}

func (b *Builder) assertStep(ctx context.Context, run store.ReplayRun, step store.WorkflowStep, dryRun bool) (bool, string) {
	if !assertStatus(run.Status, step.Assertions.Status, dryRun) {
		return false, fmt.Sprintf("status %d not in %v", run.Status, step.Assertions.Status)
	}
	if dryRun {
		return true, ""
	}
	if len(step.Assertions.JSONPath) == 0 && len(step.Assertions.Header) == 0 && len(step.Assertions.BodyContains) == 0 {
		return run.OK, run.Error
	}
	body, headers, err := b.responseForReplay(ctx, run)
	if err != nil {
		return false, err.Error()
	}
	for _, name := range step.Assertions.Header {
		if headers.Get(name) == "" {
			return false, fmt.Sprintf("missing response header %s", name)
		}
	}
	for _, needle := range step.Assertions.BodyContains {
		if !strings.Contains(string(body), needle) {
			return false, fmt.Sprintf("response body missing %q", needle)
		}
	}
	for _, path := range step.Assertions.JSONPath {
		if _, ok := jsonPathValue(body, path); !ok {
			return false, fmt.Sprintf("missing response json path %s", path)
		}
	}
	return run.OK, run.Error
}

func (b *Builder) extractStepOutputs(ctx context.Context, step store.WorkflowStep, stepRun store.WorkflowStepRun) (map[string]string, error) {
	if len(step.Extract) == 0 {
		return nil, nil
	}
	replayRun, err := b.replayRun(ctx, stepRun.ReplayRunID)
	if err != nil {
		return nil, err
	}
	body, _, err := b.responseForReplay(ctx, replayRun)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for key, path := range step.Extract {
		value, ok := jsonPathValue(body, path)
		if !ok {
			return nil, fmt.Errorf("missing extraction path %s", path)
		}
		out[key] = fmt.Sprint(value)
	}
	return out, nil
}

func (b *Builder) responseForReplay(ctx context.Context, run store.ReplayRun) ([]byte, http.Header, error) {
	if run.ReplayFlowID == "" {
		return nil, nil, fmt.Errorf("replay flow is unavailable")
	}
	flow, err := b.store.GetFlow(ctx, run.ReplayFlowID)
	if err != nil {
		return nil, nil, err
	}
	headers := http.Header{}
	if len(flow.ResponseHeaders) > 0 {
		_ = json.Unmarshal(flow.ResponseHeaders, &headers)
	}
	if b.blobs == nil || flow.ResponseBlob == "" {
		return nil, headers, nil
	}
	body, err := b.blobs.Get(flow.ResponseBlob)
	if err != nil {
		return nil, nil, err
	}
	return body, headers, nil
}

func (b *Builder) replayRun(ctx context.Context, id string) (store.ReplayRun, error) {
	if id == "" {
		return store.ReplayRun{}, fmt.Errorf("replay run is unavailable")
	}
	storeBackend, ok := b.store.(*store.Store)
	if !ok {
		return store.ReplayRun{}, fmt.Errorf("workflow runner requires store backend")
	}
	return storeBackend.GetReplayRun(ctx, id)
}

func (b *Builder) tryAuthRefresh(ctx context.Context, engine *replay.Engine, params RunParams, failed store.WorkflowStepRun) (store.WorkflowStepRun, bool) {
	if params.DryRun || params.AuthRefreshFlowID == "" || !authFailureStatus(failed.Status) {
		return store.WorkflowStepRun{}, false
	}
	overrides := replay.Overrides{}
	applyAuthMaterial(&overrides, params.AuthMaterial)
	result, err := engine.Run(ctx, replay.Params{FlowID: params.AuthRefreshFlowID, Overrides: overrides, Profile: params.ReplayProfile, ProfileOptions: params.ProfileOptions})
	if err != nil {
		return store.WorkflowStepRun{StepID: "auth_refresh", FlowID: params.AuthRefreshFlowID, OK: false, Error: err.Error()}, true
	}
	ok := result.Run.OK
	return store.WorkflowStepRun{
		StepID:      "auth_refresh",
		FlowID:      params.AuthRefreshFlowID,
		ReplayRunID: result.Run.ID,
		OK:          ok,
		Status:      result.Run.Status,
		Error:       result.Run.Error,
	}, true
}

func authFailureStatus(status int) bool {
	return status == 401 || status == 403 || status == 419 || status == 440
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
	defaults := b.inferInputDefaults(flow)
	fields := make([]string, 0, len(defaults))
	for field := range defaults {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	return fields
}

func (b *Builder) inferInputDefaults(flow store.Flow) map[string]string {
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
	fields := map[string]string{}
	for key, value := range object {
		if text, ok := value.(string); ok && !sensitiveField(key) {
			fields[key] = text
		}
	}
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

func stepOverrides(step store.WorkflowStep, inputs map[string]any, stepOutputs map[string]map[string]string) (replay.Overrides, error) {
	if len(step.Overrides) == 0 {
		return replay.Overrides{}, nil
	}
	var overrides replay.Overrides
	if err := json.Unmarshal(step.Overrides, &overrides); err != nil {
		return replay.Overrides{}, err
	}
	if overrides.Body != nil {
		body := *overrides.Body
		body = replaceBindings(body, inputs, stepOutputs)
		overrides.Body = &body
	}
	for key, value := range overrides.Query {
		overrides.Query[key] = replaceBindings(value, inputs, stepOutputs)
	}
	for key, values := range overrides.Headers {
		for i, value := range values {
			values[i] = replaceBindings(value, inputs, stepOutputs)
		}
		overrides.Headers[key] = values
	}
	return overrides, nil
}

func replaceBindings(value string, inputs map[string]any, stepOutputs map[string]map[string]string) string {
	for key, input := range inputs {
		value = strings.ReplaceAll(value, "${input."+key+"}", fmt.Sprint(input))
	}
	for stepID, outputs := range stepOutputs {
		for key, output := range outputs {
			value = strings.ReplaceAll(value, "${steps."+stepID+"."+key+"}", output)
		}
	}
	return value
}

func jsonPathValue(body []byte, path string) (any, bool) {
	var root any
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, false
	}
	if path == "$" {
		return root, true
	}
	if !strings.HasPrefix(path, "$.") {
		return nil, false
	}
	current := root
	for _, token := range splitJSONPath(strings.TrimPrefix(path, "$.")) {
		switch value := current.(type) {
		case map[string]any:
			next, ok := value[token.field]
			if !ok {
				return nil, false
			}
			current = next
		default:
			return nil, false
		}
		if token.index != nil {
			items, ok := current.([]any)
			if !ok || *token.index < 0 || *token.index >= len(items) {
				return nil, false
			}
			current = items[*token.index]
		}
	}
	return current, true
}

type jsonPathToken struct {
	field string
	index *int
}

func splitJSONPath(path string) []jsonPathToken {
	raw := strings.Split(path, ".")
	tokens := make([]jsonPathToken, 0, len(raw))
	for _, part := range raw {
		token := jsonPathToken{field: part}
		if open := strings.Index(part, "["); open >= 0 && strings.HasSuffix(part, "]") {
			token.field = part[:open]
			indexValue, err := strconv.Atoi(strings.TrimSuffix(part[open+1:], "]"))
			if err == nil {
				token.index = &indexValue
			}
		}
		tokens = append(tokens, token)
	}
	return tokens
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

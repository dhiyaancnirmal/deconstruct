package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

type Session struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	CreatedAt time.Time `json:"created_at"`
}

type Flow struct {
	ID              string        `json:"id"`
	SessionID       string        `json:"session_id"`
	Method          string        `json:"method"`
	AppName         string        `json:"app_name"`
	Source          string        `json:"source"`
	URL             string        `json:"url"`
	Host            string        `json:"host"`
	Status          int           `json:"status"`
	Duration        time.Duration `json:"duration"`
	RequestHeaders  []byte        `json:"-"`
	ResponseHeaders []byte        `json:"-"`
	RequestBlob     string        `json:"request_blob,omitempty"`
	ResponseBlob    string        `json:"response_blob,omitempty"`
	CreatedAt       time.Time     `json:"created_at"`
}

type FlowSummary struct {
	ID        string    `json:"id"`
	SessionID string    `json:"session_id"`
	Method    string    `json:"method"`
	AppName   string    `json:"app_name"`
	Source    string    `json:"source"`
	URL       string    `json:"url"`
	Host      string    `json:"host"`
	Status    int       `json:"status"`
	Duration  int64     `json:"duration_ms"`
	CreatedAt time.Time `json:"created_at"`
	Tags      []string  `json:"tags,omitempty"`
}

type ReplayRun struct {
	ID             string    `json:"id"`
	OriginalFlowID string    `json:"original_flow_id"`
	ReplayFlowID   string    `json:"replay_flow_id,omitempty"`
	OK             bool      `json:"ok"`
	Status         int       `json:"status"`
	Error          string    `json:"error,omitempty"`
	Attempts       int       `json:"attempts"`
	CreatedAt      time.Time `json:"created_at"`
}

type Workflow struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	InputSchema  json.RawMessage `json:"input_schema,omitempty"`
	OutputSchema json.RawMessage `json:"output_schema,omitempty"`
	Steps        []WorkflowStep  `json:"steps"`
	CreatedAt    time.Time       `json:"created_at"`
}

type WorkflowStep struct {
	ID             string             `json:"id"`
	FlowID         string             `json:"flow_id"`
	Name           string             `json:"name"`
	Class          string             `json:"class"`
	Included       bool               `json:"included"`
	Reason         string             `json:"reason,omitempty"`
	Assertions     WorkflowAssertions `json:"assertions,omitempty"`
	Overrides      json.RawMessage    `json:"overrides,omitempty"`
	Extract        map[string]string  `json:"extract,omitempty"`
	ReplayProfile  string             `json:"replay_profile,omitempty"`
	ProfileOptions map[string]string  `json:"profile_options,omitempty"`
}

type WorkflowAssertions struct {
	Status       []int    `json:"status,omitempty"`
	JSONPath     []string `json:"json_path,omitempty"`
	Header       []string `json:"header,omitempty"`
	BodyContains []string `json:"body_contains,omitempty"`
}

type WorkflowRun struct {
	ID         string            `json:"id"`
	WorkflowID string            `json:"workflow_id"`
	OK         bool              `json:"ok"`
	Error      string            `json:"error,omitempty"`
	StepRuns   []WorkflowStepRun `json:"step_runs"`
	CreatedAt  time.Time         `json:"created_at"`
}

type WorkflowStepRun struct {
	StepID      string `json:"step_id"`
	FlowID      string `json:"flow_id"`
	ReplayRunID string `json:"replay_run_id,omitempty"`
	OK          bool   `json:"ok"`
	Status      int    `json:"status"`
	Error       string `json:"error,omitempty"`
}

type AuthChain struct {
	ID               string                `json:"id"`
	SessionID        string                `json:"session_id,omitempty"`
	Host             string                `json:"host,omitempty"`
	FlowIDs          []string              `json:"flow_ids,omitempty"`
	Artifacts        []AuthArtifact        `json:"artifacts,omitempty"`
	CSRFLinks        []AuthCSRFLink        `json:"csrf_links,omitempty"`
	RefreshEndpoints []AuthRefreshEndpoint `json:"refresh_endpoints,omitempty"`
	Checkpoints      []AuthCheckpoint      `json:"checkpoints,omitempty"`
	Failures         []AuthFailure         `json:"failures,omitempty"`
	CreatedAt        time.Time             `json:"created_at"`
}

type AuthArtifact struct {
	ID            string    `json:"id"`
	Type          string    `json:"type"`
	Name          string    `json:"name"`
	ValueHash     string    `json:"value_hash,omitempty"`
	SourceFlowID  string    `json:"source_flow_id,omitempty"`
	FirstSeen     time.Time `json:"first_seen,omitempty"`
	LastSeen      time.Time `json:"last_seen,omitempty"`
	UsedByFlowIDs []string  `json:"used_by_flow_ids,omitempty"`
	Locations     []string  `json:"locations,omitempty"`
	Storage       string    `json:"storage,omitempty"`
}

type AuthCSRFLink struct {
	TokenName    string `json:"token_name"`
	SourceFlowID string `json:"source_flow_id"`
	UsedByFlowID string `json:"used_by_flow_id"`
	Source       string `json:"source"`
	Usage        string `json:"usage"`
}

type AuthRefreshEndpoint struct {
	FlowID     string   `json:"flow_id"`
	URL        string   `json:"url"`
	Method     string   `json:"method"`
	Status     int      `json:"status"`
	Artifacts  []string `json:"artifacts,omitempty"`
	Confidence string   `json:"confidence"`
}

type AuthCheckpoint struct {
	FlowID string `json:"flow_id"`
	Type   string `json:"type"`
	Reason string `json:"reason"`
}

type AuthFailure struct {
	FlowID string `json:"flow_id"`
	Type   string `json:"type"`
	Status int    `json:"status"`
	Reason string `json:"reason"`
}

type AuthProfile struct {
	ID                       string                 `json:"id"`
	Name                     string                 `json:"name"`
	Host                     string                 `json:"host,omitempty"`
	ChainID                  string                 `json:"chain_id,omitempty"`
	SecretBundleID           string                 `json:"secret_bundle_id,omitempty"`
	CookieWarming            *CookieWarmingSchedule `json:"cookie_warming,omitempty"`
	InteractiveLoginRequired bool                   `json:"interactive_login_required,omitempty"`
	CreatedAt                time.Time              `json:"created_at"`
}

type CookieWarmingSchedule struct {
	Enabled         bool   `json:"enabled"`
	IntervalSeconds int    `json:"interval_seconds,omitempty"`
	WorkflowID      string `json:"workflow_id,omitempty"`
}

type AuthSecretBundle struct {
	ID         string    `json:"id"`
	ProfileID  string    `json:"profile_id"`
	Nonce      []byte    `json:"nonce,omitempty"`
	Ciphertext []byte    `json:"ciphertext,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

type ExportArtifact struct {
	ID         string          `json:"id"`
	WorkflowID string          `json:"workflow_id"`
	Kind       string          `json:"kind"`
	FilesJSON  json.RawMessage `json:"files_json,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
}

type ProtocolSchema struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Kind      string          `json:"kind"`
	Source    string          `json:"source,omitempty"`
	Summary   json.RawMessage `json:"summary,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

type CapturePolicy struct {
	ID        string    `json:"id"`
	Scope     string    `json:"scope"`
	Matcher   string    `json:"matcher"`
	Action    string    `json:"action"`
	TLSMode   string    `json:"tls_mode,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate(ctx context.Context) error {
	statements := []string{
		`PRAGMA journal_mode = WAL`,
		`PRAGMA foreign_keys = ON`,
		`CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TEXT NOT NULL
		)`,
		`INSERT OR IGNORE INTO schema_migrations (version, applied_at)
			VALUES (1, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))`,
		`CREATE TABLE IF NOT EXISTS sessions (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			kind TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS flows (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
			method TEXT NOT NULL,
			url TEXT NOT NULL,
			host TEXT NOT NULL,
			status INTEGER NOT NULL,
			duration_ms INTEGER NOT NULL,
			request_headers BLOB NOT NULL,
			response_headers BLOB NOT NULL,
			request_blob TEXT NOT NULL,
			response_blob TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_flows_session_created ON flows(session_id, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_flows_host ON flows(host)`,
		`CREATE TABLE IF NOT EXISTS flow_tags (
			flow_id TEXT NOT NULL REFERENCES flows(id) ON DELETE CASCADE,
			tag TEXT NOT NULL,
			created_at TEXT NOT NULL,
			PRIMARY KEY (flow_id, tag)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_flow_tags_tag ON flow_tags(tag)`,
		`CREATE TABLE IF NOT EXISTS saved_filters (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			query_json TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS replay_runs (
			id TEXT PRIMARY KEY,
			original_flow_id TEXT NOT NULL REFERENCES flows(id) ON DELETE CASCADE,
			replay_flow_id TEXT REFERENCES flows(id) ON DELETE SET NULL,
			ok INTEGER NOT NULL,
			status INTEGER NOT NULL,
			error TEXT NOT NULL,
			attempts INTEGER NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_replay_runs_original ON replay_runs(original_flow_id, created_at)`,
		`CREATE TABLE IF NOT EXISTS workflows (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			input_schema TEXT NOT NULL,
			output_schema TEXT NOT NULL,
			steps_json TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS workflow_runs (
			id TEXT PRIMARY KEY,
			workflow_id TEXT NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
			ok INTEGER NOT NULL,
			error TEXT NOT NULL,
			step_runs_json TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_workflow_runs_workflow ON workflow_runs(workflow_id, created_at)`,
		`CREATE TABLE IF NOT EXISTS auth_chains (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			host TEXT NOT NULL,
			chain_json TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_auth_chains_session ON auth_chains(session_id, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_auth_chains_host ON auth_chains(host, created_at)`,
		`CREATE TABLE IF NOT EXISTS auth_profiles (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			host TEXT NOT NULL,
			chain_id TEXT,
			secret_bundle_id TEXT,
			cookie_warming_json TEXT NOT NULL,
			interactive_login_required INTEGER NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_auth_profiles_host ON auth_profiles(host, created_at)`,
		`CREATE TABLE IF NOT EXISTS auth_secret_bundles (
			id TEXT PRIMARY KEY,
			profile_id TEXT NOT NULL REFERENCES auth_profiles(id) ON DELETE CASCADE,
			nonce BLOB NOT NULL,
			ciphertext BLOB NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_auth_secret_bundles_profile ON auth_secret_bundles(profile_id, created_at)`,
		`CREATE TABLE IF NOT EXISTS export_artifacts (
			id TEXT PRIMARY KEY,
			workflow_id TEXT NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
			kind TEXT NOT NULL,
			files_json TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_export_artifacts_workflow ON export_artifacts(workflow_id, created_at)`,
		`CREATE TABLE IF NOT EXISTS protocol_schemas (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			kind TEXT NOT NULL,
			source TEXT NOT NULL,
			summary_json TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_protocol_schemas_kind ON protocol_schemas(kind, created_at)`,
		`CREATE TABLE IF NOT EXISTS capture_policies (
			id TEXT PRIMARY KEY,
			scope TEXT NOT NULL,
			matcher TEXT NOT NULL,
			action TEXT NOT NULL,
			tls_mode TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_capture_policies_scope ON capture_policies(scope, matcher)`,
	}
	for _, stmt := range statements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate sqlite: %w", err)
		}
	}
	if err := s.ensureFlowColumn(ctx, "app_name", "TEXT NOT NULL DEFAULT 'Unknown'"); err != nil {
		return err
	}
	if err := s.ensureFlowColumn(ctx, "source", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	return nil
}

func (s *Store) ensureFlowColumn(ctx context.Context, name, definition string) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(flows)`)
	if err != nil {
		return fmt.Errorf("inspect flows schema: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var columnName string
		var columnType string
		var notNull int
		var defaultValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &columnName, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return fmt.Errorf("scan flows schema: %w", err)
		}
		if columnName == name {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read flows schema: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE flows ADD COLUMN `+name+` `+definition); err != nil {
		return fmt.Errorf("add flows column %s: %w", name, err)
	}
	return nil
}

func (s *Store) EnsureSession(ctx context.Context, session Session) error {
	if session.ID == "" {
		return errors.New("session id is required")
	}
	if session.Name == "" {
		return errors.New("session name is required")
	}
	if session.Kind == "" {
		return errors.New("session kind is required")
	}
	if session.CreatedAt.IsZero() {
		session.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sessions (id, name, kind, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING
	`, session.ID, session.Name, session.Kind, session.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("ensure session: %w", err)
	}
	return nil
}

func (s *Store) ListSessions(ctx context.Context) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, kind, created_at
		FROM sessions
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	var sessions []Session
	for rows.Next() {
		var session Session
		var created string
		if err := rows.Scan(&session.ID, &session.Name, &session.Kind, &created); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		session.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, fmt.Errorf("parse session time: %w", err)
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

func (s *Store) ListFlows(ctx context.Context, limit int) ([]FlowSummary, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, session_id, method, app_name, source, url, host, status, duration_ms, created_at
		FROM flows
		ORDER BY created_at DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list flows: %w", err)
	}
	defer rows.Close()

	flows, err := scanFlowSummaries(rows)
	if err != nil {
		return nil, err
	}
	if err := s.attachTags(ctx, flows); err != nil {
		return nil, err
	}
	return flows, nil
}

func (s *Store) InsertFlow(ctx context.Context, flow Flow) error {
	if flow.ID == "" {
		return errors.New("flow id is required")
	}
	if flow.SessionID == "" {
		return errors.New("session id is required")
	}
	if flow.CreatedAt.IsZero() {
		flow.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO flows (
			id, session_id, method, app_name, source, url, host, status, duration_ms,
			request_headers, response_headers, request_blob, response_blob, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, flow.ID, flow.SessionID, flow.Method, defaultString(flow.AppName, "Unknown"), flow.Source, flow.URL, flow.Host, flow.Status,
		flow.Duration.Milliseconds(), flow.RequestHeaders, flow.ResponseHeaders,
		flow.RequestBlob, flow.ResponseBlob, flow.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("insert flow: %w", err)
	}
	return nil
}

func (s *Store) GetFlow(ctx context.Context, id string) (Flow, error) {
	var flow Flow
	var created string
	var durationMS int64
	err := s.db.QueryRowContext(ctx, `
		SELECT id, session_id, method, url, host, status, duration_ms,
			app_name, source, request_headers, response_headers, request_blob, response_blob, created_at
		FROM flows
		WHERE id = ?
	`, id).Scan(
		&flow.ID,
		&flow.SessionID,
		&flow.Method,
		&flow.URL,
		&flow.Host,
		&flow.Status,
		&durationMS,
		&flow.AppName,
		&flow.Source,
		&flow.RequestHeaders,
		&flow.ResponseHeaders,
		&flow.RequestBlob,
		&flow.ResponseBlob,
		&created,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Flow{}, fmt.Errorf("flow not found: %s", id)
		}
		return Flow{}, fmt.Errorf("get flow: %w", err)
	}
	flow.Duration = time.Duration(durationMS) * time.Millisecond
	flow.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return Flow{}, fmt.Errorf("parse flow time: %w", err)
	}
	return flow, nil
}

func (s *Store) CountFlows(ctx context.Context) (int64, error) {
	var count int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM flows`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count flows: %w", err)
	}
	return count, nil
}

func (s *Store) InsertReplayRun(ctx context.Context, run ReplayRun) error {
	if run.ID == "" {
		return errors.New("replay run id is required")
	}
	if run.OriginalFlowID == "" {
		return errors.New("original flow id is required")
	}
	if run.CreatedAt.IsZero() {
		run.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO replay_runs (
			id, original_flow_id, replay_flow_id, ok, status, error, attempts, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, run.ID, run.OriginalFlowID, nullString(run.ReplayFlowID), boolInt(run.OK), run.Status, run.Error, run.Attempts, run.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("insert replay run: %w", err)
	}
	return nil
}

func (s *Store) ListReplayRuns(ctx context.Context, originalFlowID string, limit int) ([]ReplayRun, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `
		SELECT id, original_flow_id, replay_flow_id, ok, status, error, attempts, created_at
		FROM replay_runs
	`
	var args []any
	if originalFlowID != "" {
		query += `WHERE original_flow_id = ? `
		args = append(args, originalFlowID)
	}
	query += `ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list replay runs: %w", err)
	}
	defer rows.Close()
	var runs []ReplayRun
	for rows.Next() {
		var run ReplayRun
		var ok int
		var replayFlow sql.NullString
		var created string
		if err := rows.Scan(&run.ID, &run.OriginalFlowID, &replayFlow, &ok, &run.Status, &run.Error, &run.Attempts, &created); err != nil {
			return nil, fmt.Errorf("scan replay run: %w", err)
		}
		run.ReplayFlowID = replayFlow.String
		run.OK = ok == 1
		run.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, fmt.Errorf("parse replay run time: %w", err)
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (s *Store) GetReplayRun(ctx context.Context, id string) (ReplayRun, error) {
	var run ReplayRun
	var ok int
	var replayFlow sql.NullString
	var created string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, original_flow_id, replay_flow_id, ok, status, error, attempts, created_at
		FROM replay_runs
		WHERE id = ?
	`, id).Scan(&run.ID, &run.OriginalFlowID, &replayFlow, &ok, &run.Status, &run.Error, &run.Attempts, &created)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ReplayRun{}, fmt.Errorf("replay run not found: %s", id)
		}
		return ReplayRun{}, fmt.Errorf("get replay run: %w", err)
	}
	run.ReplayFlowID = replayFlow.String
	run.OK = ok == 1
	run.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return ReplayRun{}, fmt.Errorf("parse replay run time: %w", err)
	}
	return run, nil
}

func (s *Store) SaveWorkflow(ctx context.Context, item Workflow) error {
	if item.ID == "" {
		return errors.New("workflow id is required")
	}
	if item.Name == "" {
		return errors.New("workflow name is required")
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now().UTC()
	}
	steps, err := json.Marshal(item.Steps)
	if err != nil {
		return fmt.Errorf("marshal workflow steps: %w", err)
	}
	inputSchema := item.InputSchema
	if len(inputSchema) == 0 {
		inputSchema = json.RawMessage(`{"type":"object","properties":{}}`)
	}
	outputSchema := item.OutputSchema
	if len(outputSchema) == 0 {
		outputSchema = json.RawMessage(`{"type":"object","properties":{}}`)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO workflows (id, name, input_schema, output_schema, steps_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			input_schema = excluded.input_schema,
			output_schema = excluded.output_schema,
			steps_json = excluded.steps_json
	`, item.ID, item.Name, string(inputSchema), string(outputSchema), string(steps), item.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("save workflow: %w", err)
	}
	return nil
}

func (s *Store) GetWorkflow(ctx context.Context, id string) (Workflow, error) {
	var item Workflow
	var inputSchema string
	var outputSchema string
	var stepsJSON string
	var created string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, input_schema, output_schema, steps_json, created_at
		FROM workflows
		WHERE id = ?
	`, id).Scan(&item.ID, &item.Name, &inputSchema, &outputSchema, &stepsJSON, &created)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Workflow{}, fmt.Errorf("workflow not found: %s", id)
		}
		return Workflow{}, fmt.Errorf("get workflow: %w", err)
	}
	item.InputSchema = json.RawMessage(inputSchema)
	item.OutputSchema = json.RawMessage(outputSchema)
	if err := json.Unmarshal([]byte(stepsJSON), &item.Steps); err != nil {
		return Workflow{}, fmt.Errorf("decode workflow steps: %w", err)
	}
	item.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return Workflow{}, fmt.Errorf("parse workflow time: %w", err)
	}
	return item, nil
}

func (s *Store) ListWorkflows(ctx context.Context) ([]Workflow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, input_schema, output_schema, steps_json, created_at
		FROM workflows
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("list workflows: %w", err)
	}
	defer rows.Close()
	var items []Workflow
	for rows.Next() {
		var item Workflow
		var inputSchema string
		var outputSchema string
		var stepsJSON string
		var created string
		if err := rows.Scan(&item.ID, &item.Name, &inputSchema, &outputSchema, &stepsJSON, &created); err != nil {
			return nil, fmt.Errorf("scan workflow: %w", err)
		}
		item.InputSchema = json.RawMessage(inputSchema)
		item.OutputSchema = json.RawMessage(outputSchema)
		if err := json.Unmarshal([]byte(stepsJSON), &item.Steps); err != nil {
			return nil, fmt.Errorf("decode workflow steps: %w", err)
		}
		item.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, fmt.Errorf("parse workflow time: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) InsertWorkflowRun(ctx context.Context, run WorkflowRun) error {
	if run.ID == "" {
		return errors.New("workflow run id is required")
	}
	if run.WorkflowID == "" {
		return errors.New("workflow id is required")
	}
	if run.CreatedAt.IsZero() {
		run.CreatedAt = time.Now().UTC()
	}
	stepRuns, err := json.Marshal(run.StepRuns)
	if err != nil {
		return fmt.Errorf("marshal workflow step runs: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO workflow_runs (id, workflow_id, ok, error, step_runs_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, run.ID, run.WorkflowID, boolInt(run.OK), run.Error, string(stepRuns), run.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("insert workflow run: %w", err)
	}
	return nil
}

func (s *Store) ListWorkflowRuns(ctx context.Context, workflowID string, limit int) ([]WorkflowRun, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `
		SELECT id, workflow_id, ok, error, step_runs_json, created_at
		FROM workflow_runs
	`
	var args []any
	if workflowID != "" {
		query += `WHERE workflow_id = ? `
		args = append(args, workflowID)
	}
	query += `ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list workflow runs: %w", err)
	}
	defer rows.Close()
	var runs []WorkflowRun
	for rows.Next() {
		var run WorkflowRun
		var ok int
		var stepRuns string
		var created string
		if err := rows.Scan(&run.ID, &run.WorkflowID, &ok, &run.Error, &stepRuns, &created); err != nil {
			return nil, fmt.Errorf("scan workflow run: %w", err)
		}
		run.OK = ok == 1
		if err := json.Unmarshal([]byte(stepRuns), &run.StepRuns); err != nil {
			return nil, fmt.Errorf("decode workflow step runs: %w", err)
		}
		run.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, fmt.Errorf("parse workflow run time: %w", err)
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (s *Store) SaveAuthChain(ctx context.Context, chain AuthChain) error {
	if chain.ID == "" {
		return errors.New("auth chain id is required")
	}
	if chain.CreatedAt.IsZero() {
		chain.CreatedAt = time.Now().UTC()
	}
	data, err := json.Marshal(chain)
	if err != nil {
		return fmt.Errorf("marshal auth chain: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO auth_chains (id, session_id, host, chain_json, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			session_id = excluded.session_id,
			host = excluded.host,
			chain_json = excluded.chain_json
	`, chain.ID, chain.SessionID, chain.Host, string(data), chain.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("save auth chain: %w", err)
	}
	return nil
}

func (s *Store) GetAuthChain(ctx context.Context, id string) (AuthChain, error) {
	var data string
	err := s.db.QueryRowContext(ctx, `SELECT chain_json FROM auth_chains WHERE id = ?`, id).Scan(&data)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AuthChain{}, fmt.Errorf("auth chain not found: %s", id)
		}
		return AuthChain{}, fmt.Errorf("get auth chain: %w", err)
	}
	var chain AuthChain
	if err := json.Unmarshal([]byte(data), &chain); err != nil {
		return AuthChain{}, fmt.Errorf("decode auth chain: %w", err)
	}
	return chain, nil
}

func (s *Store) ListAuthChains(ctx context.Context, sessionID, host string, limit int) ([]AuthChain, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `SELECT chain_json FROM auth_chains `
	var clauses []string
	var args []any
	if sessionID != "" {
		clauses = append(clauses, "session_id = ?")
		args = append(args, sessionID)
	}
	if host != "" {
		clauses = append(clauses, "host = ?")
		args = append(args, host)
	}
	if len(clauses) > 0 {
		query += `WHERE ` + strings.Join(clauses, " AND ") + ` `
	}
	query += `ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list auth chains: %w", err)
	}
	defer rows.Close()
	var chains []AuthChain
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, fmt.Errorf("scan auth chain: %w", err)
		}
		var chain AuthChain
		if err := json.Unmarshal([]byte(data), &chain); err != nil {
			return nil, fmt.Errorf("decode auth chain: %w", err)
		}
		chains = append(chains, chain)
	}
	return chains, rows.Err()
}

func (s *Store) SaveAuthProfile(ctx context.Context, profile AuthProfile) error {
	if profile.ID == "" {
		return errors.New("auth profile id is required")
	}
	if profile.Name == "" {
		return errors.New("auth profile name is required")
	}
	if profile.CreatedAt.IsZero() {
		profile.CreatedAt = time.Now().UTC()
	}
	warming := json.RawMessage(`null`)
	if profile.CookieWarming != nil {
		data, err := json.Marshal(profile.CookieWarming)
		if err != nil {
			return fmt.Errorf("marshal cookie warming schedule: %w", err)
		}
		warming = data
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO auth_profiles (
			id, name, host, chain_id, secret_bundle_id, cookie_warming_json, interactive_login_required, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			host = excluded.host,
			chain_id = excluded.chain_id,
			secret_bundle_id = excluded.secret_bundle_id,
			cookie_warming_json = excluded.cookie_warming_json,
			interactive_login_required = excluded.interactive_login_required
	`, profile.ID, profile.Name, profile.Host, nullString(profile.ChainID), nullString(profile.SecretBundleID),
		string(warming), boolInt(profile.InteractiveLoginRequired), profile.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("save auth profile: %w", err)
	}
	return nil
}

func (s *Store) GetAuthProfile(ctx context.Context, id string) (AuthProfile, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, host, chain_id, secret_bundle_id, cookie_warming_json, interactive_login_required, created_at
		FROM auth_profiles
		WHERE id = ?
	`, id)
	if err != nil {
		return AuthProfile{}, fmt.Errorf("get auth profile: %w", err)
	}
	defer rows.Close()
	profiles, err := scanAuthProfiles(rows)
	if err != nil {
		return AuthProfile{}, err
	}
	if len(profiles) == 0 {
		return AuthProfile{}, fmt.Errorf("auth profile not found: %s", id)
	}
	return profiles[0], nil
}

func (s *Store) ListAuthProfiles(ctx context.Context, host string, limit int) ([]AuthProfile, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `
		SELECT id, name, host, chain_id, secret_bundle_id, cookie_warming_json, interactive_login_required, created_at
		FROM auth_profiles
	`
	var args []any
	if host != "" {
		query += `WHERE host = ? `
		args = append(args, host)
	}
	query += `ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list auth profiles: %w", err)
	}
	defer rows.Close()
	return scanAuthProfiles(rows)
}

func (s *Store) SaveAuthSecretBundle(ctx context.Context, bundle AuthSecretBundle) error {
	if bundle.ID == "" {
		return errors.New("auth secret bundle id is required")
	}
	if bundle.ProfileID == "" {
		return errors.New("auth profile id is required")
	}
	if len(bundle.Nonce) == 0 || len(bundle.Ciphertext) == 0 {
		return errors.New("auth secret bundle must be encrypted")
	}
	if bundle.CreatedAt.IsZero() {
		bundle.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO auth_secret_bundles (id, profile_id, nonce, ciphertext, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			profile_id = excluded.profile_id,
			nonce = excluded.nonce,
			ciphertext = excluded.ciphertext
	`, bundle.ID, bundle.ProfileID, bundle.Nonce, bundle.Ciphertext, bundle.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("save auth secret bundle: %w", err)
	}
	return nil
}

func (s *Store) GetAuthSecretBundle(ctx context.Context, id string) (AuthSecretBundle, error) {
	var bundle AuthSecretBundle
	var created string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, profile_id, nonce, ciphertext, created_at
		FROM auth_secret_bundles
		WHERE id = ?
	`, id).Scan(&bundle.ID, &bundle.ProfileID, &bundle.Nonce, &bundle.Ciphertext, &created)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AuthSecretBundle{}, fmt.Errorf("auth secret bundle not found: %s", id)
		}
		return AuthSecretBundle{}, fmt.Errorf("get auth secret bundle: %w", err)
	}
	bundle.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return AuthSecretBundle{}, fmt.Errorf("parse auth secret bundle time: %w", err)
	}
	return bundle, nil
}

func scanAuthProfiles(rows *sql.Rows) ([]AuthProfile, error) {
	var profiles []AuthProfile
	for rows.Next() {
		var profile AuthProfile
		var chainID sql.NullString
		var secretBundleID sql.NullString
		var warmingJSON string
		var interactive int
		var created string
		if err := rows.Scan(&profile.ID, &profile.Name, &profile.Host, &chainID, &secretBundleID, &warmingJSON, &interactive, &created); err != nil {
			return nil, fmt.Errorf("scan auth profile: %w", err)
		}
		profile.ChainID = chainID.String
		profile.SecretBundleID = secretBundleID.String
		profile.InteractiveLoginRequired = interactive == 1
		if warmingJSON != "" && warmingJSON != "null" {
			var schedule CookieWarmingSchedule
			if err := json.Unmarshal([]byte(warmingJSON), &schedule); err != nil {
				return nil, fmt.Errorf("decode cookie warming schedule: %w", err)
			}
			profile.CookieWarming = &schedule
		}
		var err error
		profile.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, fmt.Errorf("parse auth profile time: %w", err)
		}
		profiles = append(profiles, profile)
	}
	return profiles, rows.Err()
}

func (s *Store) SaveExportArtifact(ctx context.Context, artifact ExportArtifact) error {
	if artifact.ID == "" {
		return errors.New("export artifact id is required")
	}
	if artifact.WorkflowID == "" {
		return errors.New("workflow id is required")
	}
	if artifact.Kind == "" {
		return errors.New("export artifact kind is required")
	}
	if len(artifact.FilesJSON) == 0 {
		artifact.FilesJSON = json.RawMessage(`[]`)
	}
	if artifact.CreatedAt.IsZero() {
		artifact.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO export_artifacts (id, workflow_id, kind, files_json, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			workflow_id = excluded.workflow_id,
			kind = excluded.kind,
			files_json = excluded.files_json
	`, artifact.ID, artifact.WorkflowID, artifact.Kind, string(artifact.FilesJSON), artifact.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("save export artifact: %w", err)
	}
	return nil
}

func (s *Store) ListExportArtifacts(ctx context.Context, workflowID string, limit int) ([]ExportArtifact, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `
		SELECT id, workflow_id, kind, files_json, created_at
		FROM export_artifacts
	`
	var args []any
	if workflowID != "" {
		query += `WHERE workflow_id = ? `
		args = append(args, workflowID)
	}
	query += `ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list export artifacts: %w", err)
	}
	defer rows.Close()
	var artifacts []ExportArtifact
	for rows.Next() {
		var artifact ExportArtifact
		var files string
		var created string
		if err := rows.Scan(&artifact.ID, &artifact.WorkflowID, &artifact.Kind, &files, &created); err != nil {
			return nil, fmt.Errorf("scan export artifact: %w", err)
		}
		artifact.FilesJSON = json.RawMessage(files)
		artifact.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, fmt.Errorf("parse export artifact time: %w", err)
		}
		artifacts = append(artifacts, artifact)
	}
	return artifacts, rows.Err()
}

func (s *Store) SaveProtocolSchema(ctx context.Context, schema ProtocolSchema) error {
	if schema.ID == "" {
		return errors.New("protocol schema id is required")
	}
	if schema.Name == "" {
		return errors.New("protocol schema name is required")
	}
	if schema.Kind == "" {
		return errors.New("protocol schema kind is required")
	}
	if len(schema.Summary) == 0 {
		schema.Summary = json.RawMessage(`{}`)
	}
	if schema.CreatedAt.IsZero() {
		schema.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO protocol_schemas (id, name, kind, source, summary_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			kind = excluded.kind,
			source = excluded.source,
			summary_json = excluded.summary_json
	`, schema.ID, schema.Name, schema.Kind, schema.Source, string(schema.Summary), schema.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("save protocol schema: %w", err)
	}
	return nil
}

func (s *Store) GetProtocolSchema(ctx context.Context, id string) (ProtocolSchema, error) {
	var schema ProtocolSchema
	var summary string
	var created string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, kind, source, summary_json, created_at
		FROM protocol_schemas
		WHERE id = ?
	`, id).Scan(&schema.ID, &schema.Name, &schema.Kind, &schema.Source, &summary, &created)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ProtocolSchema{}, fmt.Errorf("protocol schema not found: %s", id)
		}
		return ProtocolSchema{}, fmt.Errorf("get protocol schema: %w", err)
	}
	schema.Summary = json.RawMessage(summary)
	schema.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return ProtocolSchema{}, fmt.Errorf("parse protocol schema time: %w", err)
	}
	return schema, nil
}

func (s *Store) ListProtocolSchemas(ctx context.Context, kind string, limit int) ([]ProtocolSchema, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `
		SELECT id, name, kind, source, summary_json, created_at
		FROM protocol_schemas
	`
	var args []any
	if kind != "" {
		query += `WHERE kind = ? `
		args = append(args, kind)
	}
	query += `ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list protocol schemas: %w", err)
	}
	defer rows.Close()
	var schemas []ProtocolSchema
	for rows.Next() {
		var schema ProtocolSchema
		var summary string
		var created string
		if err := rows.Scan(&schema.ID, &schema.Name, &schema.Kind, &schema.Source, &summary, &created); err != nil {
			return nil, fmt.Errorf("scan protocol schema: %w", err)
		}
		schema.Summary = json.RawMessage(summary)
		schema.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, fmt.Errorf("parse protocol schema time: %w", err)
		}
		schemas = append(schemas, schema)
	}
	return schemas, rows.Err()
}

func (s *Store) Stats(ctx context.Context) (map[string]int64, error) {
	tables := []string{"sessions", "flows", "replay_runs", "workflows", "workflow_runs", "auth_chains", "auth_profiles", "export_artifacts", "protocol_schemas", "capture_policies"}
	stats := map[string]int64{}
	for _, table := range tables {
		var count int64
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil {
			return nil, fmt.Errorf("count %s: %w", table, err)
		}
		stats[table] = count
	}
	return stats, nil
}

func (s *Store) PruneFlowsBefore(ctx context.Context, before time.Time, dryRun bool) (int64, error) {
	var count int64
	beforeText := before.UTC().Format(time.RFC3339Nano)
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM flows WHERE created_at < ?`, beforeText).Scan(&count); err != nil {
		return 0, fmt.Errorf("count prunable flows: %w", err)
	}
	if dryRun || count == 0 {
		return count, nil
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM flows WHERE created_at < ?`, beforeText); err != nil {
		return 0, fmt.Errorf("prune flows: %w", err)
	}
	return count, nil
}

func (s *Store) SaveCapturePolicy(ctx context.Context, policy CapturePolicy) error {
	if policy.ID == "" {
		return errors.New("capture policy id is required")
	}
	if policy.Scope == "" || policy.Matcher == "" || policy.Action == "" {
		return errors.New("capture policy scope, matcher, and action are required")
	}
	if policy.TLSMode == "" {
		policy.TLSMode = "metadata"
	}
	if policy.CreatedAt.IsZero() {
		policy.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO capture_policies (id, scope, matcher, action, tls_mode, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			scope = excluded.scope,
			matcher = excluded.matcher,
			action = excluded.action,
			tls_mode = excluded.tls_mode
	`, policy.ID, policy.Scope, policy.Matcher, policy.Action, policy.TLSMode, policy.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("save capture policy: %w", err)
	}
	return nil
}

func (s *Store) ListCapturePolicies(ctx context.Context, scope string) ([]CapturePolicy, error) {
	query := `
		SELECT id, scope, matcher, action, tls_mode, created_at
		FROM capture_policies
	`
	var args []any
	if scope != "" {
		query += `WHERE scope = ? `
		args = append(args, scope)
	}
	query += `ORDER BY created_at DESC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list capture policies: %w", err)
	}
	defer rows.Close()
	var policies []CapturePolicy
	for rows.Next() {
		var policy CapturePolicy
		var created string
		if err := rows.Scan(&policy.ID, &policy.Scope, &policy.Matcher, &policy.Action, &policy.TLSMode, &created); err != nil {
			return nil, fmt.Errorf("scan capture policy: %w", err)
		}
		policy.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, fmt.Errorf("parse capture policy time: %w", err)
		}
		policies = append(policies, policy)
	}
	return policies, rows.Err()
}

func (s *Store) DeleteCapturePolicy(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("capture policy id is required")
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM capture_policies WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete capture policy: %w", err)
	}
	return nil
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nullString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: value != ""}
}

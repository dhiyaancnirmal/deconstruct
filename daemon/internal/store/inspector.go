package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type FlowQuery struct {
	SessionID   string `json:"session_id,omitempty"`
	Method      string `json:"method,omitempty"`
	AppName     string `json:"app_name,omitempty"`
	Host        string `json:"host,omitempty"`
	URLContains string `json:"url_contains,omitempty"`
	Tag         string `json:"tag,omitempty"`
	Status      *int   `json:"status,omitempty"`
	Limit       int    `json:"limit,omitempty"`
	Offset      int    `json:"offset,omitempty"`
	SortBy      string `json:"sort_by,omitempty"`
	SortDir     string `json:"sort_dir,omitempty"`
}

type FlowQueryResult struct {
	Flows []FlowSummary `json:"flows"`
	Total int64         `json:"total"`
}

type HostCount struct {
	Host  string `json:"host"`
	Count int64  `json:"count"`
}

type AppCount struct {
	AppName string `json:"app_name"`
	Count   int64  `json:"count"`
}

type TagCount struct {
	Tag   string `json:"tag"`
	Count int64  `json:"count"`
}

type SavedFilter struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Query     json.RawMessage `json:"query"`
	CreatedAt time.Time       `json:"created_at"`
}

func (s *Store) QueryFlows(ctx context.Context, query FlowQuery) (FlowQueryResult, error) {
	limit := query.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	offset := query.Offset
	if offset < 0 {
		offset = 0
	}

	where, args, join := buildFlowWhere(query)
	countSQL := `SELECT COUNT(*) FROM flows f ` + join + where
	var total int64
	if err := s.db.QueryRowContext(ctx, countSQL, args...).Scan(&total); err != nil {
		return FlowQueryResult{}, fmt.Errorf("count queried flows: %w", err)
	}

	orderBy := flowOrderBy(query.SortBy)
	sortDir := "DESC"
	if strings.EqualFold(query.SortDir, "asc") {
		sortDir = "ASC"
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT f.id, f.session_id, f.method, f.app_name, f.source, f.url, f.host, f.status, f.duration_ms, f.created_at
		FROM flows f
		`+join+where+`
		ORDER BY `+orderBy+` `+sortDir+`
		LIMIT ? OFFSET ?
	`, append(args, limit, offset)...)
	if err != nil {
		return FlowQueryResult{}, fmt.Errorf("query flows: %w", err)
	}
	defer rows.Close()

	flows, err := scanFlowSummaries(rows)
	if err != nil {
		return FlowQueryResult{}, err
	}
	if err := s.attachTags(ctx, flows); err != nil {
		return FlowQueryResult{}, err
	}
	return FlowQueryResult{Flows: flows, Total: total}, nil
}

func (s *Store) HostCounts(ctx context.Context, limit int) ([]HostCount, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT host, COUNT(*)
		FROM flows
		GROUP BY host
		ORDER BY COUNT(*) DESC, host ASC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list host counts: %w", err)
	}
	defer rows.Close()

	var counts []HostCount
	for rows.Next() {
		var count HostCount
		if err := rows.Scan(&count.Host, &count.Count); err != nil {
			return nil, fmt.Errorf("scan host count: %w", err)
		}
		counts = append(counts, count)
	}
	return counts, rows.Err()
}

func (s *Store) AppCounts(ctx context.Context, limit int) ([]AppCount, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT app_name, COUNT(*)
		FROM flows
		GROUP BY app_name
		ORDER BY COUNT(*) DESC, app_name ASC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list app counts: %w", err)
	}
	defer rows.Close()

	var counts []AppCount
	for rows.Next() {
		var count AppCount
		if err := rows.Scan(&count.AppName, &count.Count); err != nil {
			return nil, fmt.Errorf("scan app count: %w", err)
		}
		counts = append(counts, count)
	}
	return counts, rows.Err()
}

func (s *Store) TagFlow(ctx context.Context, flowID, tag string) error {
	tag = normalizeTag(tag)
	if flowID == "" || tag == "" {
		return errors.New("flow id and tag are required")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO flow_tags (flow_id, tag, created_at)
		VALUES (?, ?, ?)
		ON CONFLICT(flow_id, tag) DO NOTHING
	`, flowID, tag, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("tag flow: %w", err)
	}
	return nil
}

func (s *Store) UntagFlow(ctx context.Context, flowID, tag string) error {
	tag = normalizeTag(tag)
	if flowID == "" || tag == "" {
		return errors.New("flow id and tag are required")
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM flow_tags WHERE flow_id = ? AND tag = ?`, flowID, tag); err != nil {
		return fmt.Errorf("untag flow: %w", err)
	}
	return nil
}

func (s *Store) ListTags(ctx context.Context) ([]TagCount, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT tag, COUNT(*)
		FROM flow_tags
		GROUP BY tag
		ORDER BY tag ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	defer rows.Close()

	var tags []TagCount
	for rows.Next() {
		var tag TagCount
		if err := rows.Scan(&tag.Tag, &tag.Count); err != nil {
			return nil, fmt.Errorf("scan tag count: %w", err)
		}
		tags = append(tags, tag)
	}
	return tags, rows.Err()
}

func (s *Store) FlowTags(ctx context.Context, flowID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT tag FROM flow_tags WHERE flow_id = ? ORDER BY tag ASC`, flowID)
	if err != nil {
		return nil, fmt.Errorf("list flow tags: %w", err)
	}
	defer rows.Close()
	var tags []string
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return nil, fmt.Errorf("scan flow tag: %w", err)
		}
		tags = append(tags, tag)
	}
	return tags, rows.Err()
}

func (s *Store) SaveFilter(ctx context.Context, filter SavedFilter) error {
	if filter.ID == "" {
		return errors.New("filter id is required")
	}
	if filter.Name == "" {
		return errors.New("filter name is required")
	}
	if !json.Valid(filter.Query) {
		return errors.New("filter query must be valid json")
	}
	if filter.CreatedAt.IsZero() {
		filter.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO saved_filters (id, name, query_json, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			query_json = excluded.query_json
	`, filter.ID, filter.Name, string(filter.Query), filter.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("save filter: %w", err)
	}
	return nil
}

func (s *Store) ListSavedFilters(ctx context.Context) ([]SavedFilter, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, query_json, created_at
		FROM saved_filters
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("list saved filters: %w", err)
	}
	defer rows.Close()

	var filters []SavedFilter
	for rows.Next() {
		var filter SavedFilter
		var query string
		var created string
		if err := rows.Scan(&filter.ID, &filter.Name, &query, &created); err != nil {
			return nil, fmt.Errorf("scan saved filter: %w", err)
		}
		filter.Query = json.RawMessage(query)
		var err error
		filter.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, fmt.Errorf("parse filter time: %w", err)
		}
		filters = append(filters, filter)
	}
	return filters, rows.Err()
}

func (s *Store) DeleteSavedFilter(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("filter id is required")
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM saved_filters WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete saved filter: %w", err)
	}
	return nil
}

func buildFlowWhere(query FlowQuery) (string, []any, string) {
	var clauses []string
	var args []any
	join := ""
	if query.Tag != "" {
		join = "JOIN flow_tags ft ON ft.flow_id = f.id "
		clauses = append(clauses, "ft.tag = ?")
		args = append(args, normalizeTag(query.Tag))
	}
	if query.SessionID != "" {
		clauses = append(clauses, "f.session_id = ?")
		args = append(args, query.SessionID)
	}
	if query.Method != "" {
		clauses = append(clauses, "f.method = ?")
		args = append(args, strings.ToUpper(query.Method))
	}
	if query.AppName != "" {
		clauses = append(clauses, "f.app_name = ?")
		args = append(args, query.AppName)
	}
	if query.Host != "" {
		clauses = append(clauses, "f.host = ?")
		args = append(args, query.Host)
	}
	if query.URLContains != "" {
		clauses = append(clauses, "f.url LIKE ?")
		args = append(args, "%"+query.URLContains+"%")
	}
	if query.Status != nil {
		clauses = append(clauses, "f.status = ?")
		args = append(args, *query.Status)
	}
	if len(clauses) == 0 {
		return "", args, join
	}
	return "WHERE " + strings.Join(clauses, " AND "), args, join
}

func flowOrderBy(sortBy string) string {
	switch sortBy {
	case "method":
		return "f.method"
	case "app":
		return "f.app_name"
	case "url":
		return "f.url"
	case "host":
		return "f.host"
	case "status":
		return "f.status"
	case "duration":
		return "f.duration_ms"
	case "time", "created_at", "":
		return "f.created_at"
	default:
		return "f.created_at"
	}
}

func scanFlowSummaries(rows *sql.Rows) ([]FlowSummary, error) {
	var flows []FlowSummary
	for rows.Next() {
		var flow FlowSummary
		var created string
		if err := rows.Scan(&flow.ID, &flow.SessionID, &flow.Method, &flow.AppName, &flow.Source, &flow.URL, &flow.Host, &flow.Status, &flow.Duration, &created); err != nil {
			return nil, fmt.Errorf("scan flow: %w", err)
		}
		var err error
		flow.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, fmt.Errorf("parse flow time: %w", err)
		}
		flows = append(flows, flow)
	}
	return flows, rows.Err()
}

func (s *Store) attachTags(ctx context.Context, flows []FlowSummary) error {
	for i := range flows {
		rows, err := s.db.QueryContext(ctx, `SELECT tag FROM flow_tags WHERE flow_id = ? ORDER BY tag ASC`, flows[i].ID)
		if err != nil {
			return fmt.Errorf("list flow tags: %w", err)
		}
		for rows.Next() {
			var tag string
			if err := rows.Scan(&tag); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan flow tag: %w", err)
			}
			flows[i].Tags = append(flows[i].Tags, tag)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close flow tags: %w", err)
		}
	}
	return nil
}

func normalizeTag(tag string) string {
	return strings.ToLower(strings.TrimSpace(tag))
}

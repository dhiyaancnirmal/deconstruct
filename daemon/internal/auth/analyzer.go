package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/dhiyaan/deconstruct/daemon/internal/blobstore"
	"github.com/dhiyaan/deconstruct/daemon/internal/id"
	"github.com/dhiyaan/deconstruct/daemon/internal/store"
)

type Store interface {
	GetFlow(context.Context, string) (store.Flow, error)
	QueryFlows(context.Context, store.FlowQuery) (store.FlowQueryResult, error)
	SaveAuthChain(context.Context, store.AuthChain) error
}

type Analyzer struct {
	store Store
	blobs *blobstore.Store
}

type TraceParams struct {
	SessionID string   `json:"session_id,omitempty"`
	Host      string   `json:"host,omitempty"`
	FlowIDs   []string `json:"flow_ids,omitempty"`
	Save      *bool    `json:"save,omitempty"`
}

func NewAnalyzer(store Store, blobs *blobstore.Store) *Analyzer {
	return &Analyzer{store: store, blobs: blobs}
}

func (a *Analyzer) Trace(ctx context.Context, params TraceParams) (store.AuthChain, error) {
	flows, err := a.loadFlows(ctx, params)
	if err != nil {
		return store.AuthChain{}, err
	}
	if len(flows) == 0 {
		return store.AuthChain{}, fmt.Errorf("no flows matched auth trace")
	}
	chainID, err := id.New("auth_chain")
	if err != nil {
		return store.AuthChain{}, err
	}
	chain := store.AuthChain{
		ID:        chainID,
		SessionID: params.SessionID,
		Host:      params.Host,
		CreatedAt: time.Now().UTC(),
	}
	if chain.SessionID == "" {
		chain.SessionID = flows[0].SessionID
	}
	if chain.Host == "" {
		chain.Host = flows[0].Host
	}
	index := artifactIndex{byKey: map[string]*store.AuthArtifact{}}
	for _, flow := range flows {
		chain.FlowIDs = append(chain.FlowIDs, flow.ID)
		requestHeaders := decodeHeaders(flow.RequestHeaders)
		responseHeaders := decodeHeaders(flow.ResponseHeaders)
		requestBody := a.body(flow.RequestBlob)
		responseBody := a.body(flow.ResponseBlob)
		for _, observed := range responseArtifacts(responseHeaders, responseBody) {
			artifact := index.upsert(observed, flow, "source")
			if artifact.SourceFlowID == "" {
				artifact.SourceFlowID = flow.ID
			}
		}
		for _, observed := range requestArtifacts(requestHeaders, requestBody) {
			artifact := index.upsert(observed, flow, "usage")
			if !contains(artifact.UsedByFlowIDs, flow.ID) {
				artifact.UsedByFlowIDs = append(artifact.UsedByFlowIDs, flow.ID)
			}
			sourceArtifact := artifact
			if isCSRFName(observed.Name) && sourceArtifact.SourceFlowID == "" {
				sourceArtifact = index.findCSRFSource(observed.Value)
			}
			if isCSRFName(observed.Name) && sourceArtifact != nil && sourceArtifact.SourceFlowID != "" && sourceArtifact.SourceFlowID != flow.ID {
				chain.CSRFLinks = append(chain.CSRFLinks, store.AuthCSRFLink{
					TokenName:    observed.Name,
					SourceFlowID: sourceArtifact.SourceFlowID,
					UsedByFlowID: flow.ID,
					Source:       firstLocation(sourceArtifact.Locations),
					Usage:        observed.Location,
				})
			}
		}
		if endpoint, ok := refreshEndpoint(flow, responseHeaders, responseBody); ok {
			chain.RefreshEndpoints = append(chain.RefreshEndpoints, endpoint)
		}
		if checkpoint, ok := interactiveCheckpoint(flow, responseHeaders, responseBody); ok {
			chain.Checkpoints = append(chain.Checkpoints, checkpoint)
		}
		if failure, ok := ClassifyFailure(flow, responseHeaders, responseBody); ok {
			chain.Failures = append(chain.Failures, failure)
		}
	}
	for _, artifact := range index.sorted() {
		sort.Strings(artifact.UsedByFlowIDs)
		sort.Strings(artifact.Locations)
		chain.Artifacts = append(chain.Artifacts, *artifact)
	}
	chain.CSRFLinks = uniqueCSRFLinks(chain.CSRFLinks)
	if params.Save == nil || *params.Save {
		if err := a.store.SaveAuthChain(ctx, chain); err != nil {
			return store.AuthChain{}, err
		}
	}
	return chain, nil
}

func (a *Analyzer) loadFlows(ctx context.Context, params TraceParams) ([]store.Flow, error) {
	if len(params.FlowIDs) > 0 {
		flows := make([]store.Flow, 0, len(params.FlowIDs))
		for _, flowID := range params.FlowIDs {
			flow, err := a.store.GetFlow(ctx, flowID)
			if err != nil {
				return nil, err
			}
			flows = append(flows, flow)
		}
		sort.SliceStable(flows, func(i, j int) bool { return flows[i].CreatedAt.Before(flows[j].CreatedAt) })
		return flows, nil
	}
	result, err := a.store.QueryFlows(ctx, store.FlowQuery{
		SessionID: params.SessionID,
		Host:      params.Host,
		Limit:     1000,
		SortBy:    "time",
		SortDir:   "asc",
	})
	if err != nil {
		return nil, err
	}
	flows := make([]store.Flow, 0, len(result.Flows))
	for _, summary := range result.Flows {
		flow, err := a.store.GetFlow(ctx, summary.ID)
		if err != nil {
			return nil, err
		}
		flows = append(flows, flow)
	}
	return flows, nil
}

func (a *Analyzer) body(hash string) []byte {
	if a.blobs == nil || hash == "" {
		return nil
	}
	body, err := a.blobs.Get(hash)
	if err != nil {
		return nil
	}
	return body
}

type observedArtifact struct {
	Type     string
	Name     string
	Value    string
	Location string
}

type artifactIndex struct {
	byKey map[string]*store.AuthArtifact
}

func (i artifactIndex) upsert(observed observedArtifact, flow store.Flow, mode string) *store.AuthArtifact {
	key := observed.Type + "\x00" + observed.Name + "\x00" + fingerprint(observed.Value)
	artifact, ok := i.byKey[key]
	if !ok {
		artifactID, _ := id.New("auth_artifact")
		artifact = &store.AuthArtifact{
			ID:        artifactID,
			Type:      observed.Type,
			Name:      observed.Name,
			ValueHash: fingerprint(observed.Value),
			FirstSeen: flow.CreatedAt,
			LastSeen:  flow.CreatedAt,
			Storage:   "vault_ref",
		}
		i.byKey[key] = artifact
	}
	if flow.CreatedAt.Before(artifact.FirstSeen) || artifact.FirstSeen.IsZero() {
		artifact.FirstSeen = flow.CreatedAt
	}
	if flow.CreatedAt.After(artifact.LastSeen) {
		artifact.LastSeen = flow.CreatedAt
	}
	if mode == "source" && artifact.SourceFlowID == "" {
		artifact.SourceFlowID = flow.ID
	}
	if observed.Location != "" && !contains(artifact.Locations, observed.Location) {
		artifact.Locations = append(artifact.Locations, observed.Location)
	}
	return artifact
}

func (i artifactIndex) sorted() []*store.AuthArtifact {
	items := make([]*store.AuthArtifact, 0, len(i.byKey))
	for _, artifact := range i.byKey {
		items = append(items, artifact)
	}
	sort.SliceStable(items, func(a, b int) bool {
		if items[a].FirstSeen.Equal(items[b].FirstSeen) {
			return items[a].Name < items[b].Name
		}
		return items[a].FirstSeen.Before(items[b].FirstSeen)
	})
	return items
}

func (i artifactIndex) findCSRFSource(value string) *store.AuthArtifact {
	hash := fingerprint(value)
	for _, artifact := range i.byKey {
		if artifact.ValueHash == hash && artifact.SourceFlowID != "" && (artifact.Type == "csrf_token" || isCSRFName(artifact.Name)) {
			return artifact
		}
	}
	return nil
}

func requestArtifacts(headers http.Header, body []byte) []observedArtifact {
	var out []observedArtifact
	if authHeader := headerValue(headers, "Authorization"); authHeader != "" {
		out = append(out, observedArtifact{Type: authHeaderType(authHeader), Name: "Authorization", Value: authHeader, Location: "request.header.Authorization"})
	}
	for _, cookie := range (&http.Request{Header: headers}).Cookies() {
		out = append(out, observedArtifact{Type: "cookie", Name: cookie.Name, Value: cookie.Value, Location: "request.cookie." + cookie.Name})
	}
	for _, header := range []string{"X-CSRF-Token", "X-XSRF-Token", "X-CSRFToken"} {
		if value := headerValue(headers, header); value != "" {
			out = append(out, observedArtifact{Type: "csrf_token", Name: header, Value: value, Location: "request.header." + header})
		}
	}
	out = append(out, bodyArtifacts(body, "request.body")...)
	return out
}

func responseArtifacts(headers http.Header, body []byte) []observedArtifact {
	var out []observedArtifact
	for _, cookie := range (&http.Response{Header: headers}).Cookies() {
		out = append(out, observedArtifact{Type: "cookie", Name: cookie.Name, Value: cookie.Value, Location: "response.set_cookie." + cookie.Name})
	}
	for _, header := range []string{"X-CSRF-Token", "X-XSRF-Token", "X-CSRFToken"} {
		if value := headerValue(headers, header); value != "" {
			out = append(out, observedArtifact{Type: "csrf_token", Name: header, Value: value, Location: "response.header." + header})
		}
	}
	out = append(out, bodyArtifacts(body, "response.body")...)
	return out
}

func bodyArtifacts(body []byte, prefix string) []observedArtifact {
	if len(body) == 0 {
		return nil
	}
	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil
	}
	var out []observedArtifact
	walkJSON(parsed, prefix, &out)
	return out
}

func walkJSON(value any, path string, out *[]observedArtifact) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			childPath := path + "." + key
			if tokenKind(key) != "" {
				if raw, ok := scalarString(child); ok && raw != "" {
					*out = append(*out, observedArtifact{Type: tokenKind(key), Name: key, Value: raw, Location: childPath})
				}
			}
			walkJSON(child, childPath, out)
		}
	case []any:
		for index, child := range typed {
			walkJSON(child, fmt.Sprintf("%s[%d]", path, index), out)
		}
	}
}

func tokenKind(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "csrf") || strings.Contains(lower, "xsrf"):
		return "csrf_token"
	case strings.Contains(lower, "refresh"):
		return "refresh_token"
	case strings.Contains(lower, "access") && strings.Contains(lower, "token"):
		return "access_token"
	case strings.Contains(lower, "id_token"):
		return "id_token"
	case strings.Contains(lower, "session"):
		return "session_token"
	case strings.Contains(lower, "token"):
		return "token"
	default:
		return ""
	}
}

func scalarString(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	case float64, bool:
		return fmt.Sprint(typed), true
	default:
		return "", false
	}
}

func refreshEndpoint(flow store.Flow, headers http.Header, body []byte) (store.AuthRefreshEndpoint, bool) {
	parsed, _ := url.Parse(flow.URL)
	path := strings.ToLower(flow.URL)
	if parsed != nil {
		path = strings.ToLower(parsed.Path)
	}
	if !strings.Contains(path, "refresh") && !strings.Contains(path, "token") && !strings.Contains(path, "oauth") {
		return store.AuthRefreshEndpoint{}, false
	}
	artifacts := responseArtifacts(headers, body)
	names := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Type == "access_token" || artifact.Type == "refresh_token" || artifact.Type == "token" || artifact.Type == "id_token" {
			names = append(names, artifact.Name)
		}
	}
	if len(names) == 0 {
		return store.AuthRefreshEndpoint{}, false
	}
	confidence := "medium"
	if flow.Status >= 200 && flow.Status < 300 && flow.Method != "GET" {
		confidence = "high"
	}
	sort.Strings(names)
	return store.AuthRefreshEndpoint{
		FlowID:     flow.ID,
		URL:        flow.URL,
		Method:     flow.Method,
		Status:     flow.Status,
		Artifacts:  unique(names),
		Confidence: confidence,
	}, true
}

func interactiveCheckpoint(flow store.Flow, headers http.Header, body []byte) (store.AuthCheckpoint, bool) {
	target := strings.ToLower(flow.URL + " " + headerValue(headers, "Location") + " " + string(body))
	switch {
	case strings.Contains(target, "mfa") || strings.Contains(target, "otp") || strings.Contains(target, "two-factor"):
		return store.AuthCheckpoint{FlowID: flow.ID, Type: "mfa", Reason: "MFA marker in URL, redirect, or body"}, true
	case strings.Contains(target, "sso") || strings.Contains(target, "saml"):
		return store.AuthCheckpoint{FlowID: flow.ID, Type: "sso", Reason: "SSO marker in URL, redirect, or body"}, true
	case strings.Contains(target, "login") || strings.Contains(target, "signin") || strings.Contains(target, "authorize"):
		return store.AuthCheckpoint{FlowID: flow.ID, Type: "interactive_login", Reason: "login or authorization marker"}, true
	default:
		return store.AuthCheckpoint{}, false
	}
}

func ClassifyFailure(flow store.Flow, headers http.Header, body []byte) (store.AuthFailure, bool) {
	if flow.Status == http.StatusUnauthorized {
		return store.AuthFailure{FlowID: flow.ID, Type: "unauthenticated", Status: flow.Status, Reason: "HTTP 401"}, true
	}
	if flow.Status == http.StatusForbidden {
		return store.AuthFailure{FlowID: flow.ID, Type: "forbidden", Status: flow.Status, Reason: "HTTP 403"}, true
	}
	location := strings.ToLower(headerValue(headers, "Location"))
	if flow.Status >= 300 && flow.Status < 400 && (strings.Contains(location, "login") || strings.Contains(location, "signin") || strings.Contains(location, "authorize")) {
		return store.AuthFailure{FlowID: flow.ID, Type: "login_redirect", Status: flow.Status, Reason: "redirected to login"}, true
	}
	bodyText := strings.ToLower(string(body))
	if strings.Contains(bodyText, "<form") && (strings.Contains(bodyText, "password") || strings.Contains(bodyText, "login") || strings.Contains(bodyText, "sign in")) {
		return store.AuthFailure{FlowID: flow.ID, Type: "login_page", Status: flow.Status, Reason: "response resembles login page"}, true
	}
	return store.AuthFailure{}, false
}

func decodeHeaders(data []byte) http.Header {
	headers := http.Header{}
	if len(data) == 0 {
		return headers
	}
	_ = json.Unmarshal(data, &headers)
	return headers
}

func authHeaderType(value string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(value)), "bearer ") {
		return "bearer_token"
	}
	return "authorization"
}

func headerValue(headers http.Header, name string) string {
	if value := headers.Get(name); value != "" {
		return value
	}
	for key, values := range headers {
		if strings.EqualFold(key, name) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

func isCSRFName(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, "csrf") || strings.Contains(lower, "xsrf")
}

func fingerprint(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:24]
}

func firstLocation(locations []string) string {
	if len(locations) == 0 {
		return ""
	}
	return locations[0]
}

func uniqueCSRFLinks(links []store.AuthCSRFLink) []store.AuthCSRFLink {
	seen := map[string]bool{}
	var out []store.AuthCSRFLink
	for _, link := range links {
		key := link.TokenName + "\x00" + link.SourceFlowID + "\x00" + link.UsedByFlowID
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, link)
	}
	return out
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

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

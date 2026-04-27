package fidelity

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/dhiyaan/deconstruct/daemon/internal/store"
)

type Profile struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Mode        string   `json:"mode"`
	Description string   `json:"description"`
	Visibility  string   `json:"visibility"`
	Fidelity    string   `json:"fidelity"`
	Protocols   []string `json:"protocols,omitempty"`
}

type Diagnostics struct {
	FlowID           string   `json:"flow_id"`
	Recommended      string   `json:"recommended_profile"`
	Warnings         []string `json:"warnings,omitempty"`
	ObservedTags     []string `json:"observed_tags,omitempty"`
	Visibility       string   `json:"visibility"`
	FidelityTradeoff string   `json:"fidelity_tradeoff"`
}

type TagStore interface {
	GetFlow(context.Context, string) (store.Flow, error)
	FlowTags(context.Context, string) ([]string, error)
}

func Profiles() []Profile {
	return []Profile{
		{ID: "standard", Name: "Standard replay", Mode: "http_client", Description: "Fast local replay through the Go HTTP client.", Visibility: "request_response", Fidelity: "normal", Protocols: []string{"http1", "http2"}},
		{ID: "browser", Name: "Browser replay", Mode: "browser", Description: "Replay through a browser-controlled network stack.", Visibility: "browser_observed", Fidelity: "highest", Protocols: []string{"http1", "http2", "websocket", "sse"}},
		{ID: "utls", Name: "uTLS replay", Mode: "utls", Description: "Replay with selectable ClientHello-style profiles.", Visibility: "request_response", Fidelity: "high", Protocols: []string{"http1", "http2"}},
		{ID: "curl_impersonate", Name: "curl-impersonate replay", Mode: "external", Description: "Adapter profile for browser-like TLS and HTTP handshakes.", Visibility: "request_response", Fidelity: "high", Protocols: []string{"http1", "http2"}},
		{ID: "passthrough", Name: "Passthrough validation", Mode: "metadata_only", Description: "Validate flow structure without decrypting or replaying sensitive endpoints.", Visibility: "metadata", Fidelity: "original_client"},
	}
}

func Diagnose(ctx context.Context, store TagStore, flowID string) (Diagnostics, error) {
	flow, err := store.GetFlow(ctx, flowID)
	if err != nil {
		return Diagnostics{}, err
	}
	tags := tagsForFlow(ctx, store, flowID)
	diagnostics := Diagnostics{
		FlowID:           flow.ID,
		Recommended:      "standard",
		ObservedTags:     tags,
		Visibility:       "request_response",
		FidelityTradeoff: "standard replay is fast but does not preserve the original client TLS handshake",
	}
	headers := http.Header{}
	_ = json.Unmarshal(flow.RequestHeaders, &headers)
	lowerURL := strings.ToLower(flow.URL)
	switch {
	case contains(tags, "websocket") || contains(tags, "sse"):
		diagnostics.Recommended = "browser"
		diagnostics.FidelityTradeoff = "browser replay preserves browser network behavior for streaming flows"
	case contains(tags, "grpc") || contains(tags, "protobuf"):
		diagnostics.Recommended = "standard"
		diagnostics.Warnings = append(diagnostics.Warnings, "gRPC/protobuf replay may require proto schemas or reflection")
	case strings.Contains(lowerURL, "login") || strings.Contains(lowerURL, "mfa") || strings.Contains(lowerURL, "sso"):
		diagnostics.Recommended = "passthrough"
		diagnostics.Visibility = "metadata"
		diagnostics.Warnings = append(diagnostics.Warnings, "interactive auth endpoints should use checkpoints or passthrough validation")
	case headers.Get("Authorization") != "" || headers.Get("Cookie") != "":
		diagnostics.Recommended = "utls"
		diagnostics.Warnings = append(diagnostics.Warnings, "auth-bearing replay should use a profile and secret vault material")
	}
	return diagnostics, nil
}

func tagsForFlow(ctx context.Context, store TagStore, flowID string) []string {
	tags, err := store.FlowTags(ctx, flowID)
	if err != nil {
		return nil
	}
	return tags
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

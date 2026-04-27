package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestQueryFlowsHostsTagsAndFilters(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	session := Session{ID: "session_1", Name: "Capture", Kind: "capture", CreatedAt: time.Now().UTC()}
	if err := db.EnsureSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	for _, flow := range []Flow{
		{
			ID:              "flow_1",
			SessionID:       session.ID,
			Method:          "GET",
			URL:             "http://api.example.test/items",
			Host:            "api.example.test",
			Status:          200,
			Duration:        10 * time.Millisecond,
			RequestHeaders:  []byte(`{}`),
			ResponseHeaders: []byte(`{}`),
			CreatedAt:       time.Now().Add(-time.Second).UTC(),
		},
		{
			ID:              "flow_2",
			SessionID:       session.ID,
			Method:          "POST",
			AppName:         "Demo",
			URL:             "http://api.example.test/items",
			Host:            "api.example.test",
			Status:          201,
			Duration:        20 * time.Millisecond,
			RequestHeaders:  []byte(`{}`),
			ResponseHeaders: []byte(`{}`),
			CreatedAt:       time.Now().UTC(),
		},
	} {
		if err := db.InsertFlow(ctx, flow); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.TagFlow(ctx, "flow_2", "Action"); err != nil {
		t.Fatal(err)
	}

	status := 201
	result, err := db.QueryFlows(ctx, FlowQuery{Method: "post", AppName: "Demo", Status: &status, Tag: "action", SortBy: "duration", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || len(result.Flows) != 1 || result.Flows[0].ID != "flow_2" {
		t.Fatalf("unexpected query result: %#v", result)
	}
	if len(result.Flows[0].Tags) != 1 || result.Flows[0].Tags[0] != "action" {
		t.Fatalf("unexpected tags: %#v", result.Flows[0].Tags)
	}

	hosts, err := db.HostCounts(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}

	apps, err := db.AppCounts(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 2 || apps[0].AppName != "Demo" || apps[0].Count != 1 {
		t.Fatalf("unexpected app counts: %#v", apps)
	}
	if len(hosts) != 1 || hosts[0].Host != "api.example.test" || hosts[0].Count != 2 {
		t.Fatalf("unexpected host counts: %#v", hosts)
	}

	tags, err := db.ListTags(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 1 || tags[0].Tag != "action" || tags[0].Count != 1 {
		t.Fatalf("unexpected tags: %#v", tags)
	}

	query, err := json.Marshal(FlowQuery{Tag: "action"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SaveFilter(ctx, SavedFilter{ID: "filter_1", Name: "Actions", Query: query}); err != nil {
		t.Fatal(err)
	}
	filters, err := db.ListSavedFilters(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(filters) != 1 || filters[0].Name != "Actions" {
		t.Fatalf("unexpected filters: %#v", filters)
	}
	if err := db.DeleteSavedFilter(ctx, "filter_1"); err != nil {
		t.Fatal(err)
	}
	filters, err = db.ListSavedFilters(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(filters) != 0 {
		t.Fatalf("expected no filters, got %#v", filters)
	}
}

package fidelity

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/dhiyaan/deconstruct/daemon/internal/store"
)

func TestProfilesAndDiagnostics(t *testing.T) {
	profiles := Profiles()
	if len(profiles) < 5 {
		t.Fatalf("expected replay profiles, got %#v", profiles)
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "test.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.EnsureSession(ctx, store.Session{ID: "session_1", Name: "Capture", Kind: "capture", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertFlow(ctx, store.Flow{
		ID:              "flow_1",
		SessionID:       "session_1",
		Method:          "GET",
		URL:             "https://example.test/events",
		Host:            "example.test",
		Status:          200,
		RequestHeaders:  []byte(`{}`),
		ResponseHeaders: []byte(`{}`),
		CreatedAt:       time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.TagFlow(ctx, "flow_1", "sse"); err != nil {
		t.Fatal(err)
	}
	diagnostics, err := Diagnose(ctx, db, "flow_1")
	if err != nil {
		t.Fatal(err)
	}
	if diagnostics.Recommended != "browser" || len(diagnostics.ObservedTags) != 1 {
		t.Fatalf("unexpected diagnostics: %#v", diagnostics)
	}
}

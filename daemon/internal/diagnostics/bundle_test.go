package diagnostics

import (
	"archive/zip"
	"context"
	"path/filepath"
	"testing"

	"github.com/dhiyaan/deconstruct/daemon/internal/store"
)

func TestCreateDiagnosticsBundle(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	bundle, err := Create(context.Background(), db, t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	reader, err := zip.OpenReader(bundle.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if len(reader.File) != 1 || reader.File[0].Name != "manifest.json" {
		t.Fatalf("unexpected bundle entries: %#v", reader.File)
	}
	if bundle.Manifest.Stats["flows"] != 0 {
		t.Fatalf("unexpected stats: %#v", bundle.Manifest.Stats)
	}
}

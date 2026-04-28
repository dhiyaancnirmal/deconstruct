package diagnostics

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/dhiyaan/deconstruct/daemon/internal/store"
)

type Store interface {
	Stats(context.Context) (map[string]int64, error)
}

type Bundle struct {
	Path     string         `json:"path"`
	Manifest BundleManifest `json:"manifest"`
}

type BundleManifest struct {
	CreatedAt time.Time        `json:"created_at"`
	Version   string           `json:"version,omitempty"`
	Stats     map[string]int64 `json:"stats,omitempty"`
	Notes     []string         `json:"notes,omitempty"`
}

func Create(ctx context.Context, db Store, dataDir, version string) (Bundle, error) {
	stats, err := db.Stats(ctx)
	if err != nil {
		return Bundle{}, err
	}
	manifest := BundleManifest{
		CreatedAt: time.Now().UTC(),
		Version:   version,
		Stats:     stats,
		Notes: []string{
			"Diagnostic bundles include counts and configuration metadata only.",
			"Raw request bodies, response bodies, cookies, and tokens are intentionally excluded.",
		},
	}
	dir := filepath.Join(dataDir, "diagnostics")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Bundle{}, fmt.Errorf("create diagnostics dir: %w", err)
	}
	path := filepath.Join(dir, "deconstruct-diagnostics-"+manifest.CreatedAt.Format("20060102T150405Z")+".zip")
	file, err := os.Create(path)
	if err != nil {
		return Bundle{}, fmt.Errorf("create diagnostics bundle: %w", err)
	}
	defer file.Close()
	zw := zip.NewWriter(file)
	manifestBytes, _ := json.MarshalIndent(manifest, "", "  ")
	w, err := zw.Create("manifest.json")
	if err != nil {
		_ = zw.Close()
		return Bundle{}, fmt.Errorf("create manifest entry: %w", err)
	}
	if _, err := w.Write(append(manifestBytes, '\n')); err != nil {
		_ = zw.Close()
		return Bundle{}, fmt.Errorf("write manifest entry: %w", err)
	}
	if err := zw.Close(); err != nil {
		return Bundle{}, fmt.Errorf("close diagnostics bundle: %w", err)
	}
	return Bundle{Path: path, Manifest: manifest}, nil
}

var _ Store = (*store.Store)(nil)

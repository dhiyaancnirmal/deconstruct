package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/dhiyaan/deconstruct/daemon/internal/blobstore"
	"github.com/dhiyaan/deconstruct/daemon/internal/eval"
	"github.com/dhiyaan/deconstruct/daemon/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	harPath := flag.String("har", "", "HAR file to compile and evaluate")
	outDir := flag.String("out", "", "directory to write generated artifacts and report")
	name := flag.String("name", "", "scenario name")
	replay := flag.Bool("replay", false, "replay compiled workflow after import")
	redact := flag.Bool("redact", false, "redact secrets before compiler evaluation")
	serverURL := flag.String("server-url", "", "server URL for generated OpenAPI document")
	flag.Parse()
	if *harPath == "" {
		return fmt.Errorf("-har is required")
	}
	tempDir, err := os.MkdirTemp("", "deconstruct-eval-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir)
	db, err := store.Open(filepath.Join(tempDir, "eval.sqlite3"))
	if err != nil {
		return err
	}
	defer db.Close()
	blobs, err := blobstore.Open(filepath.Join(tempDir, "blobs"))
	if err != nil {
		return err
	}
	report, err := eval.RunHAR(context.Background(), db, blobs, eval.HAREvalParams{
		Name:      *name,
		HARPath:   *harPath,
		OutputDir: *outDir,
		Replay:    *replay,
		Redact:    *redact,
		ServerURL: *serverURL,
	})
	if err != nil {
		return err
	}
	encoded, _ := json.MarshalIndent(report, "", "  ")
	fmt.Println(string(encoded))
	if eval.Failed(report) {
		return fmt.Errorf("HAR eval failed with %d failure(s)", len(report.Failures))
	}
	return nil
}

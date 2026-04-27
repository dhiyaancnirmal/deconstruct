package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const ShutdownTimeout = 5 * time.Second

type Paths struct {
	DataDir    string
	RuntimeDir string
	SocketPath string
}

func DefaultPaths() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("resolve home dir: %w", err)
	}
	dataDir := filepath.Join(home, "Library", "Application Support", "deconstruct")
	runtimeDir := filepath.Join(os.TempDir(), "deconstruct")
	return Paths{
		DataDir:    dataDir,
		RuntimeDir: runtimeDir,
		SocketPath: filepath.Join(runtimeDir, "deconstructd.sock"),
	}, nil
}

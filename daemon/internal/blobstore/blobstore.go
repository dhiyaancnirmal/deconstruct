package blobstore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/klauspost/compress/zstd"
)

type Store struct {
	root string
}

func Open(root string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(root, "sha256"), 0o700); err != nil {
		return nil, fmt.Errorf("create blob store: %w", err)
	}
	return &Store{root: root}, nil
}

func (s *Store) Put(data []byte) (string, error) {
	if len(data) == 0 {
		return "", nil
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	path := s.path(hash)
	if _, err := os.Stat(path); err == nil {
		return hash, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("stat blob: %w", err)
	}

	var compressed bytes.Buffer
	encoder, err := zstd.NewWriter(&compressed)
	if err != nil {
		return "", fmt.Errorf("create zstd encoder: %w", err)
	}
	if _, err := encoder.Write(data); err != nil {
		_ = encoder.Close()
		return "", fmt.Errorf("compress blob: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return "", fmt.Errorf("finish blob compression: %w", err)
	}
	if err := os.WriteFile(path, compressed.Bytes(), 0o600); err != nil {
		return "", fmt.Errorf("write blob: %w", err)
	}
	return hash, nil
}

func (s *Store) Get(hash string) ([]byte, error) {
	file, err := os.Open(s.path(hash))
	if err != nil {
		return nil, fmt.Errorf("open blob: %w", err)
	}
	defer file.Close()

	decoder, err := zstd.NewReader(file)
	if err != nil {
		return nil, fmt.Errorf("create zstd decoder: %w", err)
	}
	defer decoder.Close()

	data, err := io.ReadAll(decoder)
	if err != nil {
		return nil, fmt.Errorf("read blob: %w", err)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != hash {
		return nil, fmt.Errorf("blob hash mismatch: %s", hash)
	}
	return data, nil
}

func (s *Store) path(hash string) string {
	return filepath.Join(s.root, "sha256", hash+".bin.zst")
}

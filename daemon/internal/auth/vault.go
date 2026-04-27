package auth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/dhiyaan/deconstruct/daemon/internal/id"
	"github.com/dhiyaan/deconstruct/daemon/internal/store"
)

type VaultStore interface {
	SaveAuthSecretBundle(context.Context, store.AuthSecretBundle) error
	GetAuthSecretBundle(context.Context, string) (store.AuthSecretBundle, error)
}

type Vault struct {
	store VaultStore
	aead  cipher.AEAD
}

func NewVault(store VaultStore, dataDir string) (*Vault, error) {
	if dataDir == "" {
		dataDir = filepath.Join(os.TempDir(), "deconstruct-auth-vault")
	}
	key, err := loadOrCreateKey(filepath.Join(dataDir, "auth_vault.key"))
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create auth vault cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create auth vault gcm: %w", err)
	}
	return &Vault{store: store, aead: aead}, nil
}

func (v *Vault) Save(ctx context.Context, profileID string, material map[string]string) (store.AuthSecretBundle, error) {
	if profileID == "" {
		return store.AuthSecretBundle{}, fmt.Errorf("profile_id is required")
	}
	if len(material) == 0 {
		return store.AuthSecretBundle{}, fmt.Errorf("secret material is required")
	}
	plain, err := json.Marshal(material)
	if err != nil {
		return store.AuthSecretBundle{}, fmt.Errorf("marshal secret material: %w", err)
	}
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return store.AuthSecretBundle{}, fmt.Errorf("create vault nonce: %w", err)
	}
	bundleID, err := id.New("auth_secret")
	if err != nil {
		return store.AuthSecretBundle{}, err
	}
	bundle := store.AuthSecretBundle{
		ID:         bundleID,
		ProfileID:  profileID,
		Nonce:      nonce,
		Ciphertext: v.aead.Seal(nil, nonce, plain, []byte(profileID)),
		CreatedAt:  time.Now().UTC(),
	}
	if err := v.store.SaveAuthSecretBundle(ctx, bundle); err != nil {
		return store.AuthSecretBundle{}, err
	}
	return bundle, nil
}

func (v *Vault) Open(ctx context.Context, bundleID string) (map[string]string, error) {
	bundle, err := v.store.GetAuthSecretBundle(ctx, bundleID)
	if err != nil {
		return nil, err
	}
	plain, err := v.aead.Open(nil, bundle.Nonce, bundle.Ciphertext, []byte(bundle.ProfileID))
	if err != nil {
		return nil, fmt.Errorf("decrypt auth secret bundle: %w", err)
	}
	var material map[string]string
	if err := json.Unmarshal(plain, &material); err != nil {
		return nil, fmt.Errorf("decode auth secret bundle: %w", err)
	}
	return material, nil
}

func loadOrCreateKey(path string) ([]byte, error) {
	if data, err := os.ReadFile(path); err == nil {
		key := sha256.Sum256(data)
		return key[:], nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create auth vault dir: %w", err)
	}
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		return nil, fmt.Errorf("create auth vault key: %w", err)
	}
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		return nil, fmt.Errorf("write auth vault key: %w", err)
	}
	key := sha256.Sum256(seed)
	return key[:], nil
}

package secure

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type Envelope struct {
	Version    int    `json:"version"`
	KeyID      string `json:"key_id"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
}

type Vault struct {
	aead  cipher.AEAD
	keyID string
}

func OpenVault(dataDir, configuredKey string) (*Vault, error) {
	key, err := loadOrCreateMasterKey(dataDir, configuredKey)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create AES-GCM: %w", err)
	}
	digest := TokenHash(base64.RawStdEncoding.EncodeToString(key))
	return &Vault{aead: aead, keyID: base64.RawURLEncoding.EncodeToString(digest[:8])}, nil
}

func (v *Vault) Seal(plaintext, aad []byte) ([]byte, error) {
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("create encryption nonce: %w", err)
	}
	ciphertext := v.aead.Seal(nil, nonce, plaintext, aad)
	return json.Marshal(Envelope{
		Version:    1,
		KeyID:      v.keyID,
		Nonce:      base64.RawStdEncoding.EncodeToString(nonce),
		Ciphertext: base64.RawStdEncoding.EncodeToString(ciphertext),
	})
}

func (v *Vault) Open(encoded, aad []byte) ([]byte, error) {
	var envelope Envelope
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		return nil, fmt.Errorf("decode secret envelope: %w", err)
	}
	if envelope.Version != 1 || envelope.KeyID != v.keyID {
		return nil, fmt.Errorf("unsupported secret envelope")
	}
	nonce, err := base64.RawStdEncoding.DecodeString(envelope.Nonce)
	if err != nil {
		return nil, fmt.Errorf("decode secret nonce: %w", err)
	}
	ciphertext, err := base64.RawStdEncoding.DecodeString(envelope.Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("decode secret ciphertext: %w", err)
	}
	plaintext, err := v.aead.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, fmt.Errorf("decrypt secret: %w", err)
	}
	return plaintext, nil
}

func loadOrCreateMasterKey(dataDir, configured string) ([]byte, error) {
	if configured != "" {
		key, err := base64.RawStdEncoding.DecodeString(configured)
		if err != nil || len(key) != 32 {
			return nil, fmt.Errorf("HERDRX_MASTER_KEY must be raw base64 encoding of exactly 32 bytes")
		}
		return key, nil
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	path := filepath.Join(dataDir, "master.key")
	encoded, err := os.ReadFile(path)
	if err == nil {
		key, decodeErr := base64.RawStdEncoding.DecodeString(string(encoded))
		if decodeErr != nil || len(key) != 32 {
			return nil, fmt.Errorf("invalid master key file %s", path)
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read master key: %w", err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("create master key: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create master key file: %w", err)
	}
	if _, err := file.WriteString(base64.RawStdEncoding.EncodeToString(key)); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("write master key file: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("sync master key file: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close master key file: %w", err)
	}
	return key, nil
}

package encryptedfile

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"goxidized/pkg/goxidized"
)

type Provider struct {
	Path   string
	KeyEnv string

	cache map[string]record
}

type record struct {
	Username      string `json:"username"`
	Password      string `json:"password"`
	PrivateKeyPEM string `json:"private_key_pem"`
	EnableSecret  string `json:"enable_secret"`
}

func New(path, keyEnv string) *Provider {
	return &Provider{Path: path, KeyEnv: keyEnv}
}

func (p *Provider) Resolve(ctx context.Context, ref string) (goxidized.Credentials, error) {
	select {
	case <-ctx.Done():
		return goxidized.Credentials{}, ctx.Err()
	default:
	}
	if p.cache == nil {
		if err := p.load(); err != nil {
			return goxidized.Credentials{}, &goxidized.BackupError{Category: goxidized.FailureCredentialProvider, Op: "encrypted-file load", Err: err}
		}
	}
	name := strings.TrimPrefix(ref, "encfile://")
	name = strings.TrimPrefix(name, "encrypted-file://")
	rec, ok := p.cache[name]
	if !ok {
		return goxidized.Credentials{}, &goxidized.BackupError{Category: goxidized.FailureCredentialProvider, Op: "encrypted-file resolve", Err: fmt.Errorf("credential ref %q not found", name)}
	}
	creds := goxidized.Credentials{
		Username:      rec.Username,
		Password:      goxidized.NewSecretString(rec.Password),
		PrivateKeyPEM: goxidized.NewSecretBytes([]byte(rec.PrivateKeyPEM)),
		EnableSecret:  goxidized.NewSecretString(rec.EnableSecret),
		Source:        "encrypted-file",
	}
	switch {
	case !creds.PrivateKeyPEM.IsZero():
		creds.AuthType = "private_key"
	case !creds.Password.IsZero():
		creds.AuthType = "password"
	}
	return creds, nil
}

func (p *Provider) load() error {
	if p.Path == "" {
		return errors.New("encrypted credential file path is required")
	}
	key, err := decodeKey(os.Getenv(p.KeyEnv))
	if err != nil {
		return err
	}
	data, err := os.ReadFile(p.Path)
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	if len(data) <= gcm.NonceSize() {
		return errors.New("encrypted file is too short")
	}
	nonce := data[:gcm.NonceSize()]
	ciphertext := data[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return err
	}
	var records map[string]record
	if err := json.Unmarshal(plain, &records); err != nil {
		return err
	}
	p.cache = records
	return nil
}

func decodeKey(raw string) ([]byte, error) {
	if raw == "" {
		return nil, errors.New("encryption key environment variable is empty")
	}
	if b, err := hex.DecodeString(raw); err == nil && validAESKeyLen(len(b)) {
		return b, nil
	}
	if b, err := base64.StdEncoding.DecodeString(raw); err == nil && validAESKeyLen(len(b)) {
		return b, nil
	}
	if validAESKeyLen(len(raw)) {
		return []byte(raw), nil
	}
	return nil, errors.New("encryption key must be 16, 24, or 32 bytes as raw, hex, or base64")
}

func validAESKeyLen(n int) bool {
	return n == 16 || n == 24 || n == 32
}

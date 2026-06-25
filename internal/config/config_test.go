package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadExampleConfig(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "config.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Scheduler.DefaultInterval != 24*time.Hour {
		t.Fatalf("default interval=%s, want 24h", cfg.Scheduler.DefaultInterval)
	}
	if cfg.Transport.SSH.CommandTimeout != 60*time.Second {
		t.Fatalf("command timeout=%s, want 60s", cfg.Transport.SSH.CommandTimeout)
	}
}

func TestOIDCConfigValidation(t *testing.T) {
	cfg := Default()
	cfg.Server.Auth.OIDC.Enabled = true
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "issuer_url") {
		t.Fatalf("Validate() error=%v, want missing issuer_url", err)
	}

	cfg.Server.Auth.OIDC.IssuerURL = "https://issuer.example"
	cfg.Server.Auth.OIDC.ClientID = "goxidized"
	cfg.Server.Auth.OIDC.RedirectURL = "http://127.0.0.1:8080/auth/oidc/callback"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid OIDC config returned error: %v", err)
	}

	cfg.Server.Auth.OIDC.Scopes = []string{"profile"}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "openid") {
		t.Fatalf("Validate() error=%v, want missing openid scope", err)
	}
}

package config

import (
	"path/filepath"
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

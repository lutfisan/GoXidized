package sshtransport

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func TestTOFURejectsHostKeyChange(t *testing.T) {
	key1 := testPublicKey(t)
	key2 := testPublicKey(t)
	path := t.TempDir() + "/known_hosts"
	if err := os.WriteFile(path, []byte(knownhosts.Line([]string{"example.com"}, key1)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cb := New(Config{HostKeyMode: "tofu", TOFUPath: path}).tofuCallback(path)
	err := cb("example.com", &net.TCPAddr{}, key2)
	if err == nil {
		t.Fatalf("expected host-key mismatch error")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "example.com") != 1 {
		t.Fatalf("tofu appended changed key: %s", data)
	}
}

func TestTOFUAppendsUnknownHost(t *testing.T) {
	key := testPublicKey(t)
	path := t.TempDir() + "/known_hosts"
	cb := New(Config{HostKeyMode: "tofu", TOFUPath: path}).tofuCallback(path)
	if err := cb("new.example.com", &net.TCPAddr{}, key); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "new.example.com") {
		t.Fatalf("tofu did not append unknown host: %s", data)
	}
}

func TestParseJumpHost(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		user string
		host string
		port string
	}{
		{name: "host only", raw: "bastion.example.com", user: "netops", host: "bastion.example.com", port: "22"},
		{name: "host port", raw: "bastion.example.com:2222", user: "netops", host: "bastion.example.com", port: "2222"},
		{name: "user host port", raw: "jumpuser@bastion.example.com:2222", user: "jumpuser", host: "bastion.example.com", port: "2222"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseJumpHost(tt.raw, "netops")
			if err != nil {
				t.Fatal(err)
			}
			if got.username != tt.user || got.host != tt.host || got.port != tt.port {
				t.Fatalf("parseJumpHost()=%+v, want user=%s host=%s port=%s", got, tt.user, tt.host, tt.port)
			}
		})
	}
}

func TestCleanInteractiveOutputDropsEchoPagingAndPrompt(t *testing.T) {
	prompt := New(Config{HostKeyMode: "insecure"}).Config.PromptPattern
	re := mustRegexp(t, prompt)
	raw := []byte("\r\nshow running-config\r\nversion 17.9\r\n --More--\r\nusername admin password 0 cisco\r\nrouter#")
	got := string(cleanInteractiveOutput(raw, "show running-config", re))
	if strings.Contains(got, "show running-config") || strings.Contains(got, "--More--") || strings.Contains(got, "router#") {
		t.Fatalf("interactive cleanup left shell artifacts: %q", got)
	}
	if !strings.Contains(got, "version 17.9") {
		t.Fatalf("interactive cleanup lost config content: %q", got)
	}
}

func testPublicKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func mustRegexp(t *testing.T, pattern string) *regexp.Regexp {
	t.Helper()
	re, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatal(err)
	}
	return re
}

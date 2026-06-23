package goxidized

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSecretStringRedacts(t *testing.T) {
	s := NewSecretString("super-secret")
	if s.String() != "[REDACTED]" || s.GoString() != "[REDACTED]" {
		t.Fatalf("secret string leaked via formatting")
	}
	if s.Reveal() != "super-secret" {
		t.Fatalf("Reveal returned wrong value")
	}
	data, err := json.Marshal(struct {
		Password SecretString `json:"password"`
	}{Password: s})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "super-secret") {
		t.Fatalf("secret leaked through JSON: %s", data)
	}
}

func TestSecretBytesCopies(t *testing.T) {
	raw := []byte("private-key")
	s := NewSecretBytes(raw)
	raw[0] = 'X'
	if string(s.Reveal()) != "private-key" {
		t.Fatalf("secret bytes did not copy input")
	}
	revealed := s.Reveal()
	revealed[0] = 'Y'
	if string(s.Reveal()) != "private-key" {
		t.Fatalf("secret bytes reveal did not copy output")
	}
}

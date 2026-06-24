package telnettransport

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"goxidized/pkg/goxidized"
)

func TestDialRequiresGlobalAndTargetOptIn(t *testing.T) {
	d := NewConfig(Config{Enabled: false})
	_, err := d.Dial(context.Background(), goxidized.Target{ID: "r1", IPAddress: "127.0.0.1", TelnetEnabled: true}, goxidized.Credentials{})
	if err == nil || !strings.Contains(err.Error(), "telnet requires both global and target opt-in") {
		t.Fatalf("expected global telnet gate error, got %v", err)
	}

	d = NewConfig(Config{Enabled: true})
	_, err = d.Dial(context.Background(), goxidized.Target{ID: "r1", IPAddress: "127.0.0.1", TelnetEnabled: false}, goxidized.Credentials{})
	if err == nil || !strings.Contains(err.Error(), "telnet requires both global and target opt-in") {
		t.Fatalf("expected target telnet gate error, got %v", err)
	}
}

func TestCleanOutputDropsEchoPagingAndPrompt(t *testing.T) {
	prompt := regexp.MustCompile(defaultPromptPattern)
	raw := []byte("\r\nshow configuration\r\nsystem hostname edge-1\r\n--More--\r\nedge-1#")
	got := string(cleanOutput(raw, "show configuration", prompt))
	if strings.Contains(got, "show configuration") || strings.Contains(got, "--More--") || strings.Contains(got, "edge-1#") {
		t.Fatalf("cleanup left telnet artifacts: %q", got)
	}
	if !strings.Contains(got, "system hostname edge-1") {
		t.Fatalf("cleanup lost payload: %q", got)
	}
}

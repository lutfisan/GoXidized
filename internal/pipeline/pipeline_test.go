package pipeline

import (
	"context"
	"strings"
	"testing"

	"goxidized/pkg/goxidized"
)

func TestNormalizeRedactAndRisk(t *testing.T) {
	p := NewProcessor(true, []byte("0123456789abcdef0123456789abcdef"))
	raw := []byte("Last configuration change at 12:00\nhostname r1\nsnmp-server community public RO\ntacacs-server host 192.0.2.1 key secret\n")
	norm, err := p.Normalize(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(norm), "Last configuration") {
		t.Fatalf("volatile line was not removed: %s", norm)
	}
	redacted, report, err := p.Redact(context.Background(), norm)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(redacted), "public") || strings.Contains(string(redacted), " secret") {
		t.Fatalf("secret leaked after redaction: %s", redacted)
	}
	if report.SecretsFound != 2 {
		t.Fatalf("secrets found=%d, want 2", report.SecretsFound)
	}
	diff, err := UnifiedDiff(context.Background(), "r1", "old", "new", []byte("hostname r1\n"), redacted)
	if err != nil {
		t.Fatal(err)
	}
	if diff.Risk != goxidized.RiskHigh {
		t.Fatalf("risk=%s, want high", diff.Risk)
	}
}

func TestHuaweiSecretModifiersRedactValue(t *testing.T) {
	p := NewProcessor(true, nil)
	raw := []byte("super password cipher verysecret\nsnmp-agent community read public\nhwtacacs-server shared-key cipher tacacssecret\n")
	redacted, report, err := p.Redact(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	text := string(redacted)
	for _, secret := range []string{"verysecret", "public", "tacacssecret"} {
		if strings.Contains(text, secret) {
			t.Fatalf("secret %q leaked in %s", secret, text)
		}
	}
	if report.SecretsFound != 3 {
		t.Fatalf("secrets found=%d, want 3", report.SecretsFound)
	}
}

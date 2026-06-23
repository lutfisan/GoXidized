package conformance

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goxidized/internal/drivers"
	"goxidized/pkg/goxidized"
)

func TestDriverFixtureReplay(t *testing.T) {
	drivers.RegisterDefaults()
	driver, err := drivers.Get("cisco_iosxe")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", "cisco_iosxe", "running.cfg"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, report, err := RunDriverFixture(context.Background(), driver, DriverFixture{
		Target: goxidized.Target{ID: "r1", Hostname: "r1", IPAddress: "192.0.2.1", Vendor: "cisco_iosxe", Group: "core", CredentialRef: "dotenv://R1", Enabled: true},
		Responses: map[string][]byte{
			"terminal length 0":   []byte(""),
			"show running-config": data,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cfg.Content), "public") {
		t.Fatalf("fixture leaked secret: %s", cfg.Content)
	}
	if report.SecretsFound == 0 {
		t.Fatalf("expected redaction report")
	}
}

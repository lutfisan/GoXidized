package csv

import (
	"context"
	"strings"
	"testing"
)

func TestParseCanonicalCSV(t *testing.T) {
	input := `id,hostname,ip_address,port,vendor,group,site,role,tags,jump_host,credential_ref,enabled
r1,r1,192.0.2.1,22,cisco_iosxe,core,dc1,pe,prod|edge,,dotenv://R1,true
bad,,192.0.2.2,22,cisco_iosxe,core,dc1,pe,,,true
`
	targets, errs, err := Parse(context.Background(), strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 {
		t.Fatalf("targets=%d, want 1", len(targets))
	}
	if len(errs) != 1 {
		t.Fatalf("errs=%d, want 1", len(errs))
	}
	if targets[0].ID != "r1" || targets[0].Tags[1] != "edge" {
		t.Fatalf("unexpected target: %+v", targets[0])
	}
}

func TestParseColonRouterDB(t *testing.T) {
	input := `r1:cisco_iosxe:192.0.2.1:core:dotenv://R1:dc1:pe`
	targets, errs, err := Parse(context.Background(), strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) != 0 || len(targets) != 1 {
		t.Fatalf("targets=%d errs=%d", len(targets), len(errs))
	}
	if targets[0].Vendor != "cisco_iosxe" || targets[0].Site != "dc1" {
		t.Fatalf("unexpected target: %+v", targets[0])
	}
}

func TestParseRejectsInvalidPort(t *testing.T) {
	input := `id,hostname,ip_address,port,vendor,group,credential_ref,enabled
r1,r1,192.0.2.1,70000,cisco_iosxe,core,dotenv://R1,true
`
	targets, errs, err := Parse(context.Background(), strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 0 {
		t.Fatalf("targets=%d, want 0", len(targets))
	}
	if len(errs) != 1 {
		t.Fatalf("errs=%d, want 1", len(errs))
	}
}

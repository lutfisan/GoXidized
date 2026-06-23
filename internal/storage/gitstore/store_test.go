package gitstore

import (
	"context"
	"strings"
	"testing"

	"goxidized/pkg/goxidized"
)

func TestSaveLatestAndDiff(t *testing.T) {
	store := New(t.TempDir(), goxidized.ShardByRole, 0, "Test", "test@example.invalid")
	target := goxidized.Target{ID: "r1", Hostname: "r1", IPAddress: "192.0.2.1", Vendor: "cisco_iosxe", Group: "core", Role: "pe", CredentialRef: "dotenv://R1", Enabled: true}
	rev1, err := store.Save(context.Background(), target, goxidized.RedactedConfig{TargetID: "r1", Content: []byte("hostname r1\n")}, goxidized.CommitMeta{Trigger: "test", Actor: "test"})
	if err != nil {
		t.Fatal(err)
	}
	rev2, err := store.Save(context.Background(), target, goxidized.RedactedConfig{TargetID: "r1", Content: []byte("hostname r1\ninterface Gi0/0\n")}, goxidized.CommitMeta{Trigger: "test", Actor: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if !rev2.Changed {
		t.Fatalf("second save should have changed")
	}
	latest, _, err := store.Latest(context.Background(), "r1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(latest.Content), "interface") {
		t.Fatalf("latest content mismatch: %s", latest.Content)
	}
	diff, err := store.Diff(context.Background(), "r1", rev1.ID, rev2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "+interface Gi0/0") {
		t.Fatalf("diff missing added line: %s", diff)
	}
}

func TestLatestUsesLastCommitForTargetFileInSharedShard(t *testing.T) {
	store := New(t.TempDir(), goxidized.ShardByRole, 0, "Test", "test@example.invalid")
	r1 := goxidized.Target{ID: "r1", Hostname: "r1", IPAddress: "192.0.2.1", Vendor: "cisco_iosxe", Group: "core", Role: "pe", CredentialRef: "dotenv://R1", Enabled: true}
	r2 := goxidized.Target{ID: "r2", Hostname: "r2", IPAddress: "192.0.2.2", Vendor: "cisco_iosxe", Group: "core", Role: "pe", CredentialRef: "dotenv://R2", Enabled: true}
	rev1, err := store.Save(context.Background(), r1, goxidized.RedactedConfig{TargetID: "r1", Content: []byte("hostname r1\n")}, goxidized.CommitMeta{Trigger: "test", Actor: "test"})
	if err != nil {
		t.Fatal(err)
	}
	rev2, err := store.Save(context.Background(), r2, goxidized.RedactedConfig{TargetID: "r2", Content: []byte("hostname r2\n")}, goxidized.CommitMeta{Trigger: "test", Actor: "test"})
	if err != nil {
		t.Fatal(err)
	}
	_, latestR1, err := store.Latest(context.Background(), "r1")
	if err != nil {
		t.Fatal(err)
	}
	if latestR1.CommitSHA != rev1.CommitSHA {
		t.Fatalf("latest r1 commit=%s, want %s; r2 head was %s", latestR1.CommitSHA, rev1.CommitSHA, rev2.CommitSHA)
	}
	noChange, err := store.Save(context.Background(), r1, goxidized.RedactedConfig{TargetID: "r1", Content: []byte("hostname r1\n")}, goxidized.CommitMeta{Trigger: "test", Actor: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if noChange.CommitSHA != rev1.CommitSHA {
		t.Fatalf("no-change commit=%s, want %s", noChange.CommitSHA, rev1.CommitSHA)
	}
}

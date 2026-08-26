package artifacts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecordAndVerifyArtifact(t *testing.T) {
	root := t.TempDir()
	writeArtifact(t, root, "raw/placements.jsonl", "{\"run_id\":\"one\"}\n")

	entry, err := Record(root, "raw/placements.jsonl")
	if err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	if entry.RelativePath != "raw/placements.jsonl" || entry.Bytes != 17 {
		t.Fatalf("Record() = %#v", entry)
	}
	if err := Verify(root); err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
}

func TestVerifyDetectsChangedArtifact(t *testing.T) {
	root := t.TempDir()
	path := writeArtifact(t, root, "raw/groups.jsonl", "original\n")
	if _, err := Record(root, "raw/groups.jsonl"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := Verify(root)
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("Verify() error = %v, want sha256 mismatch", err)
	}
}

func TestVerifyRejectsMissingOrEmptyLedger(t *testing.T) {
	root := t.TempDir()
	if err := Verify(root); err == nil || !strings.Contains(err.Error(), "ledger") {
		t.Fatalf("Verify() missing-ledger error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, LedgerName), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Verify(root); err == nil || !strings.Contains(err.Error(), "no entries") {
		t.Fatalf("Verify() empty-ledger error = %v", err)
	}
}

func TestRequireLedgerEntriesRejectsExistingButUnlistedArtifact(t *testing.T) {
	root := t.TempDir()
	writeArtifact(t, root, "manifest.json", "{}\n")
	writeArtifact(t, root, "load-calibration.json", "{}\n")
	if _, err := Record(root, "manifest.json"); err != nil {
		t.Fatal(err)
	}
	if err := Verify(root); err != nil {
		t.Fatal(err)
	}

	err := RequireLedgerEntries(root, "manifest.json", "load-calibration.json")
	if err == nil || !strings.Contains(err.Error(), "load-calibration.json") || !strings.Contains(err.Error(), "absent") {
		t.Fatalf("RequireLedgerEntries() error = %v, want unlisted artifact rejection", err)
	}
}

func TestRequireLedgerEntriesAcceptsVerifiedMembership(t *testing.T) {
	root := t.TempDir()
	writeArtifact(t, root, "manifest.json", "{}\n")
	if _, err := Record(root, "manifest.json"); err != nil {
		t.Fatal(err)
	}
	if err := Verify(root); err != nil {
		t.Fatal(err)
	}
	if err := RequireLedgerEntries(root, "manifest.json"); err != nil {
		t.Fatalf("RequireLedgerEntries() error = %v", err)
	}
}

func TestRecordRejectsDuplicatePath(t *testing.T) {
	root := t.TempDir()
	writeArtifact(t, root, "raw/groups.jsonl", "original\n")
	if _, err := Record(root, "raw/groups.jsonl"); err != nil {
		t.Fatal(err)
	}

	_, err := Record(root, "raw/groups.jsonl")
	if err == nil || !strings.Contains(err.Error(), "already recorded") {
		t.Fatalf("Record() error = %v, want already recorded", err)
	}
}

func TestRecordRejectsPathOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), "outside.txt")
	if err := os.WriteFile(outside, []byte("do not hash"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })

	_, err := Record(root, "../outside.txt")
	if err == nil || !strings.Contains(err.Error(), "relative path") {
		t.Fatalf("Record() error = %v, want relative path rejection", err)
	}
}

func writeArtifact(t *testing.T, root, relative, content string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

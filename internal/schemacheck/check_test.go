package schemacheck

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestValidateDirectoriesAcceptsValidAndRejectsInvalidFixtures(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	report, err := ValidateDirectories(
		filepath.Join(root, "benchmarks", "expected-schema"),
		filepath.Join(root, "internal", "evidence", "testdata"),
	)
	if err != nil {
		t.Fatalf("ValidateDirectories() error = %v", err)
	}
	if report.ValidAccepted != 3 || report.InvalidRejected != 1 {
		t.Fatalf("ValidateDirectories() report = %#v, want 3 valid and 1 invalid", report)
	}
}

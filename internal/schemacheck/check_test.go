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
	if report.ValidAccepted != 4 || report.InvalidRejected != 2 {
		t.Fatalf("ValidateDirectories() report = %#v, want 4 valid and 2 invalid", report)
	}
}

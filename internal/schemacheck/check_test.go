package schemacheck

import (
	"encoding/json"
	"os"
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

func TestObservedTTFTSchemaRequiresDurationAndDispatch(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	schemas, err := compileSchemas(filepath.Join(root, "benchmarks", "expected-schema"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "internal", "evidence", "testdata", "group-valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	var instance map[string]any
	if err := json.Unmarshal(raw, &instance); err != nil {
		t.Fatal(err)
	}
	child := instance["children"].([]any)[0].(map[string]any)
	delete(child, "ttft_seconds")
	if err := schemas["group"].Validate(instance); err == nil {
		t.Fatal("observed token accepted without TTFT")
	}
	delete(child, "first_token_at")
	child["outcome"] = "failure"
	child["failure"] = "no semantic token"
	if err := schemas["group"].Validate(instance); err != nil {
		t.Fatalf("missing observation rejected: %v", err)
	}
}

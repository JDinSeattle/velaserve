package zeroprobe

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JDinSeattle/velaserve/internal/artifacts"
)

func TestPrepareCreatesImmutableManifestBeforeTraffic(t *testing.T) {
	root := filepath.Join(t.TempDir(), "z0")
	manifest, err := Prepare(PrepareOptions{
		Phase:               PhaseZ0A,
		PreregistrationPath: filepath.Join("..", "preregistration", "z0-v1.yaml"),
		ArtifactRoot:        root,
		CreatedAt:           time.Date(2026, 8, 26, 8, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if manifest.SchemaVersion != ManifestSchemaVersion || manifest.Seed != 20260825 || manifest.PreregistrationSHA256 == "" {
		t.Fatalf("manifest = %#v", manifest)
	}
	for _, name := range []string{"manifest.json", "preregistration.yaml", artifacts.LedgerName} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	if err := artifacts.Verify(root); err != nil {
		t.Fatalf("artifact ledger verification failed: %v", err)
	}
	if _, err := Prepare(PrepareOptions{
		Phase:               PhaseZ0A,
		PreregistrationPath: filepath.Join("..", "preregistration", "z0-v1.yaml"),
		ArtifactRoot:        root,
		CreatedAt:           time.Now(),
	}); err == nil {
		t.Fatal("second Prepare() overwrote immutable manifest")
	}
}

func TestRunRetainsFailedGroupsButNeverMarksThemComplete(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "intentional model failure", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	root := t.TempDir()
	profilePath := filepath.Join(root, "profile.yaml")
	profile := `schema_version: velaserve.benchmark-profile/v1
seed: 20260825
run_id_prefix: failure-test
model: test-model
max_width: 16
timeout_ms: 1000
repetitions: 1
widths: [2, 4, 8, 16]
arrival_skews_ms: [0, 1, 5, 20]
arms: [arm-b-load-aware-p2p]
prefixes: [{id: short, estimated_tokens: 16, repeat_text: "prefix ", repeat_count: 4}]
outputs: [{id: short, max_tokens: 1}]
load_regimes: [idle]
cache_states: [cold]
transports: [tcp]
epp_replicas: [1]
suffixes: ["01", "02", "03", "04", "05", "06", "07", "08", "09", "10", "11", "12", "13", "14", "15", "16"]
`
	if err := os.WriteFile(profilePath, []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}
	artifactRoot := filepath.Join(root, "artifacts")
	completion, err := Run(context.Background(), RunOptions{
		PrepareOptions: PrepareOptions{
			Phase:               PhaseZ0A,
			PreregistrationPath: filepath.Join("..", "preregistration", "z0-v1.yaml"),
			ArtifactRoot:        artifactRoot,
		},
		BenchmarkProfilePath: profilePath,
		Endpoint:             server.URL,
		MaxEventBytes:        1024,
	})
	if err == nil {
		t.Fatal("Run() error = nil, want failed-group incompleteness")
	}
	if completion.Complete || completion.FailedGroups != 16 || completion.PersistedGroups != 16 {
		t.Fatalf("completion = %#v; error = %v", completion, err)
	}
	if _, statErr := os.Stat(filepath.Join(artifactRoot, "groups.jsonl")); statErr != nil {
		t.Fatalf("failed group was not retained: %v", statErr)
	}
	if got := completion.Failure; got != fmt.Sprintf("16 failed groups and %d cancelled groups", completion.CancelledGroups) {
		t.Fatalf("failure = %q", got)
	}
}

func TestRunRejectsZ0CWithoutRegisteredSourceCondition(t *testing.T) {
	root := filepath.Join(t.TempDir(), "z0-c")
	_, err := Run(context.Background(), RunOptions{
		PrepareOptions:       PrepareOptions{Phase: PhaseZ0C, PreregistrationPath: filepath.Join("..", "preregistration", "z0-v1.yaml"), ArtifactRoot: root},
		BenchmarkProfilePath: filepath.Join("..", "..", "benchmarks", "profiles", "local-calibration.yaml"),
	})
	if err == nil || !strings.Contains(err.Error(), "prefix source count") {
		t.Fatalf("Run() error = %v", err)
	}
	if _, statErr := os.Stat(root); !os.IsNotExist(statErr) {
		t.Fatalf("invalid Z0-C run created artifact root: %v", statErr)
	}
}

func TestRunRequiresPreflightBindingBeforeConditionAttestedTraffic(t *testing.T) {
	root := filepath.Join(t.TempDir(), "real-gpu")
	_, err := Run(context.Background(), RunOptions{
		PrepareOptions: PrepareOptions{
			Phase:               PhaseZ0B,
			PreregistrationPath: filepath.Join("..", "preregistration", "z0-v1.yaml"),
			ArtifactRoot:        root,
		},
		BenchmarkProfilePath:        filepath.Join("..", "..", "benchmarks", "profiles", "aws-z0-template.yaml"),
		RequireConditionAttestation: true,
	})
	if err == nil || !strings.Contains(err.Error(), "preflight binding") {
		t.Fatalf("Run() error = %v, want preflight-binding refusal", err)
	}
	if _, statErr := os.Stat(root); !os.IsNotExist(statErr) {
		t.Fatalf("missing preflight binding created artifact root: %v", statErr)
	}
}

func TestRequiredArtifactPathsCoverStageSpecificCompilerInputs(t *testing.T) {
	common := strings.Join(requiredArtifactPaths(PhaseZ0A), "\n")
	for _, required := range []string{
		"manifest.json", "preregistration.yaml", "benchmark-profile.yaml", "groups.jsonl",
		"envoy.jsonl", "epp.jsonl", "placements.jsonl", "unmatched.jsonl", "ingest-report.json",
		"oracle-calibration.yaml", "oracle.jsonl", "analysis-metrics.prom", "gate-evidence.jsonl", "analysis-report.json",
	} {
		if !strings.Contains(common, required) {
			t.Fatalf("common required paths omit %q", required)
		}
	}
	z0c := strings.Join(requiredArtifactPaths(PhaseZ0C), "\n")
	for _, required := range []string{"condition.json", "vllm-runtime.jsonl", "p2p-transfers.jsonl", "source-pressure.jsonl"} {
		if !strings.Contains(z0c, required) {
			t.Fatalf("Z0-C required paths omit %q", required)
		}
	}
	realGPU := strings.Join(realGPURequiredArtifactPaths(), "\n")
	for _, required := range []string{"preflight-binding.json", "profile-calibration.json", "load-calibration.json"} {
		if !strings.Contains(realGPU, required) {
			t.Fatalf("real-GPU required paths omit %q", required)
		}
	}
}

func TestValidateVerificationMetricsIsCanonicalLedgeredAndReadOnly(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := artifacts.Record(root, "manifest.json"); err != nil {
		t.Fatal(err)
	}
	if err := writeVerificationMetrics(root); err != nil {
		t.Fatal(err)
	}
	ledgerBefore, err := os.ReadFile(filepath.Join(root, artifacts.LedgerName))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateVerificationMetrics(root); err != nil {
		t.Fatal(err)
	}
	ledgerAfter, err := os.ReadFile(filepath.Join(root, artifacts.LedgerName))
	if err != nil {
		t.Fatal(err)
	}
	if string(ledgerAfter) != string(ledgerBefore) {
		t.Fatal("read-only verification metrics validation mutated the ledger")
	}
	contents, err := os.ReadFile(filepath.Join(root, "verification-metrics.prom"))
	if err != nil || string(contents) != verifiedArtifactMetrics {
		t.Fatalf("verification metrics = %q, error = %v", contents, err)
	}
}

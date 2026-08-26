package zeroprobe

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

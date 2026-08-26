package integration

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/JDinSeattle/velaserve/internal/simfleet"
	zeroprobe "github.com/JDinSeattle/velaserve/research/zeroprobe"
)

func TestLocalZeroingProducesVerifiedSimulationOnlyBundle(t *testing.T) {
	repository := repositoryRoot(t)
	root := filepath.Join(t.TempDir(), "z0")
	fleet, err := simfleet.New(simfleet.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(fleet)
	defer server.Close()

	completion, err := zeroprobe.Run(context.Background(), zeroprobe.RunOptions{
		PrepareOptions: zeroprobe.PrepareOptions{
			Phase:               zeroprobe.PhaseZ0A,
			PreregistrationPath: filepath.Join(repository, "research", "preregistration", "z0-v1.yaml"),
			ArtifactRoot:        root,
			CreatedAt:           time.Date(2026, 8, 26, 8, 0, 0, 0, time.UTC),
		},
		BenchmarkProfilePath: filepath.Join(repository, "benchmarks", "profiles", "local-calibration.yaml"),
		Endpoint:             server.URL + "/v1/chat/completions",
		MaxEventBytes:        1 << 20,
		SimulatorMode:        true,
	})
	if err != nil {
		t.Fatalf("zeroprobe.Run() error = %v", err)
	}
	if !completion.Complete || completion.PersistedGroups != 32 {
		t.Fatalf("completion = %#v", completion)
	}
	writeEPPLog(t, filepath.Join(root, "epp.jsonl"), fleet)
	writeEnvoyLog(t, filepath.Join(root, "envoy.jsonl"), fleet)
	if _, err := zeroprobe.Ingest(context.Background(), root); err != nil {
		t.Fatalf("zeroprobe.Ingest() error = %v", err)
	}
	if err := zeroprobe.Analyze(zeroprobe.AnalyzeOptions{
		ArtifactRoot:    root,
		CalibrationPath: filepath.Join(repository, "benchmarks", "profiles", "oracle-local-sim.yaml"),
		EvidenceScope:   "simulation_only",
	}); err != nil {
		t.Fatalf("zeroprobe.Analyze() error = %v", err)
	}
	if err := zeroprobe.Verify(root); err != nil {
		t.Fatalf("zeroprobe.Verify() error = %v", err)
	}
	for _, name := range []string{"placements.jsonl", "oracle.jsonl", "ledger.jsonl", "analysis-report.json", "gate-evidence.jsonl", "metrics.prom", "traces.jsonl"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	contents, err := os.ReadFile(filepath.Join(root, "analysis-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(contents) || !containsBytes(contents, []byte(`"evidence_scope": "simulation_only"`)) {
		t.Fatalf("analysis report lacks simulation-only scope: %s", contents)
	}
}

func writeEPPLog(t *testing.T, path string, fleet *simfleet.Fleet) {
	t.Helper()
	writeRecords(t, path, func(encoder *json.Encoder) error {
		for _, record := range fleet.EPPRecords() {
			if err := encoder.Encode(record); err != nil {
				return err
			}
		}
		return nil
	})
}

func writeEnvoyLog(t *testing.T, path string, fleet *simfleet.Fleet) {
	t.Helper()
	writeRecords(t, path, func(encoder *json.Encoder) error {
		for _, record := range fleet.EnvoyRecords() {
			line := map[string]any{
				"START_TIME":               record.StartedAt.Format(time.RFC3339Nano),
				"REQ(X-REQUEST-ID)":        record.RequestID,
				"REQ(X-VELA-FANOUT-GROUP)": record.GroupID,
				"UPSTREAM_HOST":            record.UpstreamHost,
				"RESPONSE_CODE":            record.ResponseCode,
				"DURATION":                 record.DurationMS,
				"TRACE_ID":                 record.TraceID,
				"SPAN_ID":                  record.SpanID,
			}
			if err := encoder.Encode(line); err != nil {
				return err
			}
		}
		return nil
	})
}

func writeRecords(t *testing.T, path string, write func(*json.Encoder) error) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := write(json.NewEncoder(file)); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func containsBytes(haystack, needle []byte) bool {
	for index := 0; index+len(needle) <= len(haystack); index++ {
		match := true
		for offset := range needle {
			if haystack[index+offset] != needle[offset] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

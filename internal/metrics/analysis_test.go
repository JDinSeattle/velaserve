package metrics

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JDinSeattle/velaserve/internal/evidence"
	replay "github.com/JDinSeattle/velaserve/research/oracle-replay"
)

func TestWriteAnalysisTextfileDerivesTruthfulSnapshotMetrics(t *testing.T) {
	dispatched := time.Date(2026, 8, 26, 8, 0, 0, 0, time.UTC)
	observed := dispatched.Add(25 * time.Millisecond)
	finalized := dispatched.Add(time.Second)
	group := evidence.GroupResult{
		RunID: "run", GroupID: "group", Arm: evidence.ArmLoadAwareP2P, FanoutWidth: 2,
		Cell:      evidence.BenchmarkCell{LoadRegime: evidence.LoadModerate, PrefixRegime: "near", Transport: "tcp"},
		Children:  []evidence.ChildResult{{RequestID: "request", DispatchedAt: &dispatched}},
		Condition: &evidence.ConditionAttestation{FinalizedAt: &finalized, ObservedState: evidence.ConditionObservedState{OrdinaryTrafficMeanLatencySeconds: .04}},
	}
	placement := evidence.PlacementEvent{
		RunID: "run", GroupID: "group", RequestID: "request", Arm: evidence.ArmLoadAwareP2P,
		Target: evidence.EndpointRef{ID: "model-0", Model: "model"}, ObservedAt: observed,
	}
	oracle := replay.OracleRecord{
		RunID: "run", GroupID: "group",
		ArmB: replay.ReplayResult{PredictedMakespan: 1.2}, Oracle: replay.OracleResult{BestMakespan: .8},
	}
	path := filepath.Join(t.TempDir(), "analysis-metrics.prom")
	if err := WriteAnalysisTextfile(path, []evidence.GroupResult{group}, []evidence.PlacementEvent{placement}, []replay.OracleRecord{oracle}); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"velaserve_placement_target_total",
		"velaserve_oracle_regret_seconds_bucket",
		"velaserve_dispatch_to_epp_observation_seconds_bucket",
		"velaserve_ordinary_traffic_latency_seconds_bucket",
	} {
		if !strings.Contains(string(contents), required) {
			t.Fatalf("analysis metrics omit %q:\n%s", required, contents)
		}
	}
}

func TestWriteAnalysisTextfileRejectsPlacementBeforeDispatch(t *testing.T) {
	dispatched := time.Now().UTC()
	group := evidence.GroupResult{RunID: "run", GroupID: "group", FanoutWidth: 2, Children: []evidence.ChildResult{{RequestID: "request", DispatchedAt: &dispatched}}}
	placement := evidence.PlacementEvent{RunID: "run", GroupID: "group", RequestID: "request", ObservedAt: dispatched.Add(-time.Millisecond)}
	err := WriteAnalysisTextfile(filepath.Join(t.TempDir(), "analysis-metrics.prom"), []evidence.GroupResult{group}, []evidence.PlacementEvent{placement}, nil)
	if err == nil || !strings.Contains(err.Error(), "predates") {
		t.Fatalf("WriteAnalysisTextfile() error = %v", err)
	}
}

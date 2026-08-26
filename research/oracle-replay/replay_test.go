package replay

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JDinSeattle/velaserve/internal/evidence"
	"github.com/JDinSeattle/velaserve/internal/fanout/planner"
	"github.com/JDinSeattle/velaserve/internal/jsonl"
)

func TestReplayArmBSimultaneousRequestsCollideBeforeInflightPublication(t *testing.T) {
	in := fixtureInput(4, 4)
	in.ArrivalSkewSeconds = 0
	in.InflightPublicationDelaySeconds = 0.005

	got, err := ReplayArmB(in)
	if err != nil {
		t.Fatalf("ReplayArmB() error = %v", err)
	}
	want := []string{"endpoint-0", "endpoint-0", "endpoint-0", "endpoint-0"}
	if targets := replayTargets(got); !sameStrings(targets, want) {
		t.Fatalf("targets = %v, want collision vector %v", targets, want)
	}
	if got.K != 1 {
		t.Fatalf("K = %d, want 1", got.K)
	}
}

func TestReplayFileWritesOneRecordPerGroupWithInputHashes(t *testing.T) {
	root := t.TempDir()
	placementsPath := filepath.Join(root, "placements.jsonl")
	calibrationPath := filepath.Join(root, "calibration.yaml")
	outputPath := filepath.Join(root, "oracle.jsonl")
	event := placementFixture("request-1")
	if err := jsonl.Append(placementsPath, event); err != nil {
		t.Fatal(err)
	}
	event.RequestID = "request-2"
	if err := jsonl.Append(placementsPath, event); err != nil {
		t.Fatal(err)
	}
	calibration := `prefix_tokens: 4096
inflight_publication_delay_ms: 5
affinity_load_gate_seconds: 2
calibration:
  prefill_tokens_per_second: 1000
  pull_bytes_per_second: 1000000
  bytes_per_cached_token: 512
  service_seconds: 1
`
	if err := os.WriteFile(calibrationPath, []byte(calibration), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := ReplayFile(FileOptions{PlacementsPath: placementsPath, CalibrationPath: calibrationPath, OutputPath: outputPath}); err != nil {
		t.Fatalf("ReplayFile() error = %v", err)
	}
	records, err := jsonl.Read[OracleRecord](outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("record count = %d, want 1", len(records))
	}
	if records[0].PlacementSHA256 == "" || records[0].CalibrationSHA256 == "" {
		t.Fatalf("missing input hashes: %#v", records[0])
	}
	if records[0].RunID != event.RunID || records[0].GroupID != event.GroupID {
		t.Fatalf("record identity = %#v", records[0])
	}
	if _, err := json.Marshal(records[0]); err != nil {
		t.Fatalf("OracleRecord is not JSON serializable: %v", err)
	}
}

func TestReplayFileRefusesExistingOutputWithoutAppend(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "oracle.jsonl")
	if err := os.WriteFile(output, []byte("existing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := ReplayFile(FileOptions{OutputPath: output})
	if err == nil {
		t.Fatal("ReplayFile() error = nil, want existing output rejection")
	}
}

func TestReplayArmBPublishesEarlierAssignmentBeforeStaggeredSibling(t *testing.T) {
	in := fixtureInput(2, 2)
	in.ArrivalSkewSeconds = 0.010
	in.InflightPublicationDelaySeconds = 0.005

	got, err := ReplayArmB(in)
	if err != nil {
		t.Fatalf("ReplayArmB() error = %v", err)
	}
	want := []string{"endpoint-0", "endpoint-1"}
	if targets := replayTargets(got); !sameStrings(targets, want) {
		t.Fatalf("targets = %v, want %v", targets, want)
	}
}

func TestOracleEvaluatesEveryFeasibleK(t *testing.T) {
	in := fixtureInput(8, 4)
	result, err := EvaluateAllWidths(in)
	if err != nil {
		t.Fatalf("EvaluateAllWidths() error = %v", err)
	}
	want := []uint32{1, 2, 3, 4}
	got := make([]uint32, len(result.Evaluations))
	for index, evaluation := range result.Evaluations {
		got[index] = evaluation.K
		if len(evaluation.TargetCounts) != int(evaluation.K) {
			t.Fatalf("evaluation k=%d has %d target counts", evaluation.K, len(evaluation.TargetCounts))
		}
		var total uint32
		for _, target := range evaluation.TargetCounts {
			if target.Count == 0 {
				t.Fatalf("evaluation k=%d contains zero-count target", evaluation.K)
			}
			total += target.Count
		}
		if total != in.Width {
			t.Fatalf("evaluation k=%d assigned %d slots, want %d", evaluation.K, total, in.Width)
		}
	}
	if !sameUint32s(got, want) {
		t.Fatalf("evaluated K values = %v, want %v", got, want)
	}
}

func TestOracleNeverWorseThanFrozenArmBModel(t *testing.T) {
	in := fixtureInput(16, 8)
	in.InflightPublicationDelaySeconds = 0.005
	armB, err := ReplayArmB(in)
	if err != nil {
		t.Fatal(err)
	}
	oracle, err := EvaluateAllWidths(in)
	if err != nil {
		t.Fatal(err)
	}
	if oracle.BestMakespan > armB.PredictedMakespan {
		t.Fatalf("oracle makespan %v exceeds Arm B %v", oracle.BestMakespan, armB.PredictedMakespan)
	}
	if oracle.BaselineMakespan != armB.PredictedMakespan {
		t.Fatalf("oracle baseline = %v, Arm B = %v", oracle.BaselineMakespan, armB.PredictedMakespan)
	}
}

func TestOracleUsesStableLexicographicTieBreak(t *testing.T) {
	in := fixtureInput(2, 3)
	result, err := EvaluateAllWidths(in)
	if err != nil {
		t.Fatal(err)
	}
	evaluation := result.Evaluations[1]
	if evaluation.K != 2 {
		t.Fatalf("second evaluation K = %d, want 2", evaluation.K)
	}
	want := []string{"endpoint-0", "endpoint-1"}
	got := []string{evaluation.TargetCounts[0].Target.ID, evaluation.TargetCounts[1].Target.ID}
	if !sameStrings(got, want) {
		t.Fatalf("tie targets = %v, want %v", got, want)
	}
}

func fixtureInput(width uint32, endpoints int) Input {
	states := make([]evidence.EndpointState, endpoints)
	for index := range states {
		states[index] = evidence.EndpointState{
			Ref:                evidence.EndpointRef{ID: "endpoint-" + string(rune('0'+index)), Model: "test-model"},
			Healthy:            true,
			Compatible:         true,
			AvailableAtSeconds: 0,
			LocalPrefixTokens:  4096,
		}
	}
	return Input{
		Width:        width,
		PrefixTokens: 4096,
		Snapshot: evidence.EndpointSnapshot{
			ObservedAt: time.Date(2026, 8, 25, 20, 0, 0, 0, time.UTC),
			Endpoints:  states,
		},
		Calibration: planner.Calibration{
			PrefillTokensPerSecond: 1000,
			PullBytesPerSecond:     1_000_000,
			BytesPerCachedToken:    512,
			ServiceSeconds:         1,
		},
		ArrivalSkewSeconds:              0,
		InflightPublicationDelaySeconds: 0.005,
		AffinityLoadGateSeconds:         2,
	}
}

func placementFixture(requestID string) evidence.PlacementEvent {
	in := fixtureInput(2, 2)
	return evidence.PlacementEvent{
		SchemaVersion: evidence.SchemaVersion,
		RunID:         "fixture-run",
		Arm:           evidence.ArmLoadAwareP2P,
		GroupID:       "01K39J6FJ4N5W7V57QK9Q0C6CF",
		RequestID:     requestID,
		FanoutWidth:   2,
		ArrivalSkewMS: 1,
		EPPReplicas:   1,
		LoadRegime:    evidence.LoadModerate,
		Snapshot:      in.Snapshot,
		Target:        in.Snapshot.Endpoints[0].Ref,
		ObservedAt:    in.Snapshot.ObservedAt,
	}
}

func replayTargets(result ReplayResult) []string {
	got := make([]string, len(result.Slots))
	for index, slot := range result.Slots {
		got[index] = slot.Target.ID
	}
	return got
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sameUint32s(left, right []uint32) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

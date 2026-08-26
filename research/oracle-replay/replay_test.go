package replay

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JDinSeattle/velaserve/internal/evidence"
	"github.com/JDinSeattle/velaserve/internal/fanout/model"
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

func TestOracleAndObservedBaselineUseTheSameStaggeredArrivalVector(t *testing.T) {
	in := fixtureInput(4, 1)
	in.ArrivalSkewSeconds = 0.4
	in.Calibration.ServiceSeconds = 0.1

	result, err := EvaluateAllWidths(in)
	if err != nil {
		t.Fatalf("EvaluateAllWidths() error = %v", err)
	}
	if result.RegretFraction != 0 || result.BestMakespan != result.BaselineMakespan {
		t.Fatalf("identical one-target allocation produced arrival-skew regret: %#v", result)
	}
	if len(result.Evaluations) != 1 || len(result.Evaluations[0].Slots) != 4 {
		t.Fatalf("oracle did not retain its explicit slot schedule: %#v", result.Evaluations)
	}
}

func TestReplayUsesExactObservedArrivalOffsetsWhenProvided(t *testing.T) {
	in := fixtureInput(3, 1)
	in.ArrivalSkewSeconds = 0.001
	in.ArrivalOffsetsSeconds = []float64{0, 0.3, 0.9}
	in.Calibration.ServiceSeconds = 0.1

	got, err := ReplayArmB(in)
	if err != nil {
		t.Fatalf("ReplayArmB() error = %v", err)
	}
	if got.PredictedMakespan != 1.0 {
		t.Fatalf("predicted makespan = %v, want 1.0 from exact arrival vector", got.PredictedMakespan)
	}
}

func TestReplayFileWritesOneRecordPerGroupWithInputHashes(t *testing.T) {
	root := t.TempDir()
	groupsPath := filepath.Join(root, "groups.jsonl")
	placementsPath := filepath.Join(root, "placements.jsonl")
	calibrationPath := filepath.Join(root, "calibration.yaml")
	outputPath := filepath.Join(root, "oracle.jsonl")
	event := placementFixture("request-1")
	if err := jsonl.Append(placementsPath, event); err != nil {
		t.Fatal(err)
	}
	if err := jsonl.Append(groupsPath, oracleGroupFixture(event, 4096, "short", 32, "request-1", "request-2")); err != nil {
		t.Fatal(err)
	}
	event.RequestID = "request-2"
	event.ObservedAt = event.ObservedAt.Add(time.Millisecond)
	event.Snapshot.ObservedAt = event.ObservedAt
	event.Target = event.Snapshot.Endpoints[1].Ref
	if err := jsonl.Append(placementsPath, event); err != nil {
		t.Fatal(err)
	}
	armA := placementFixture("arm-a-request-1")
	armA.Arm = evidence.ArmAffinityP2P
	armA.GroupID = "01K39J6FJ4N5W7V57QK9Q0C6CG"
	if err := jsonl.Append(placementsPath, armA); err != nil {
		t.Fatal(err)
	}
	armA.RequestID = "arm-a-request-2"
	armA.ObservedAt = armA.ObservedAt.Add(time.Millisecond)
	armA.Snapshot.ObservedAt = armA.ObservedAt
	if err := jsonl.Append(placementsPath, armA); err != nil {
		t.Fatal(err)
	}
	calibration := `inflight_publication_delay_ms: 5
affinity_load_gate_seconds: 2
calibration:
  prefill_tokens_per_second: 1000
  pull_bytes_per_second: 1000000
  bytes_per_cached_token: 512
service_seconds_by_output_regime:
  short:
    max_tokens: 32
    seconds: 1
`
	if err := os.WriteFile(calibrationPath, []byte(calibration), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := ReplayFile(FileOptions{GroupsPath: groupsPath, PlacementsPath: placementsPath, CalibrationPath: calibrationPath, OutputPath: outputPath}); err != nil {
		t.Fatalf("ReplayFile() error = %v", err)
	}
	records, err := jsonl.Read[OracleRecord](outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("record count = %d, want 1", len(records))
	}
	if records[0].GroupSHA256 == "" || records[0].PlacementSHA256 == "" || records[0].CalibrationSHA256 == "" {
		t.Fatalf("missing input hashes: %#v", records[0])
	}
	if records[0].RunID != event.RunID || records[0].GroupID != event.GroupID {
		t.Fatalf("record identity = %#v", records[0])
	}
	if got := replayTargets(records[0].ArmB); !sameStrings(got, []string{"endpoint-0", "endpoint-1"}) {
		t.Fatalf("Arm-B targets = %v, want the two targets actually selected by upstream", got)
	}
	if _, err := json.Marshal(records[0]); err != nil {
		t.Fatalf("OracleRecord is not JSON serializable: %v", err)
	}
}

func TestReplayFileUsesGroupSpecificPrefixAndOutputCalibration(t *testing.T) {
	root := t.TempDir()
	groupsPath := filepath.Join(root, "groups.jsonl")
	placementsPath := filepath.Join(root, "placements.jsonl")
	calibrationPath := filepath.Join(root, "calibration.yaml")
	outputPath := filepath.Join(root, "oracle.jsonl")
	first := placementFixture("request-1")
	for index := range first.Snapshot.Endpoints {
		first.Snapshot.Endpoints[index].LocalPrefixTokens = 0
	}
	second := first
	second.RequestID = "request-2"
	second.ObservedAt = second.ObservedAt.Add(time.Millisecond)
	second.Snapshot.ObservedAt = second.ObservedAt
	for _, event := range []evidence.PlacementEvent{first, second} {
		if err := jsonl.Append(placementsPath, event); err != nil {
			t.Fatal(err)
		}
	}
	if err := jsonl.Append(groupsPath, oracleGroupFixture(first, 1024, "short", 32, "request-1", "request-2")); err != nil {
		t.Fatal(err)
	}
	third := first
	third.GroupID = "01K39J6FJ4N5W7V57QK9Q0C6CH"
	third.RequestID = "request-3"
	fourth := third
	fourth.RequestID = "request-4"
	fourth.ObservedAt = fourth.ObservedAt.Add(time.Millisecond)
	fourth.Snapshot.ObservedAt = fourth.ObservedAt
	for _, event := range []evidence.PlacementEvent{third, fourth} {
		if err := jsonl.Append(placementsPath, event); err != nil {
			t.Fatal(err)
		}
	}
	if err := jsonl.Append(groupsPath, oracleGroupFixture(third, 8192, "moderate", 128, "request-3", "request-4")); err != nil {
		t.Fatal(err)
	}
	calibration := `inflight_publication_delay_ms: 5
affinity_load_gate_seconds: 2
calibration:
  prefill_tokens_per_second: 1000
  pull_bytes_per_second: 1000000
  bytes_per_cached_token: 512
service_seconds_by_output_regime:
  short: {max_tokens: 32, seconds: 1}
  moderate: {max_tokens: 128, seconds: 4}
`
	if err := os.WriteFile(calibrationPath, []byte(calibration), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ReplayFile(FileOptions{GroupsPath: groupsPath, PlacementsPath: placementsPath, CalibrationPath: calibrationPath, OutputPath: outputPath}); err != nil {
		t.Fatal(err)
	}
	records, err := jsonl.Read[OracleRecord](outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].PrefixTokens != 1024 || records[0].OutputRegime != "short" || records[0].MaxTokens != 32 || records[0].ServiceSeconds != 1 || records[1].PrefixTokens != 8192 || records[1].OutputRegime != "moderate" || records[1].MaxTokens != 128 || records[1].ServiceSeconds != 4 {
		t.Fatalf("records = %#v", records)
	}
	if records[1].ArmB.PredictedMakespan <= records[0].ArmB.PredictedMakespan {
		t.Fatalf("group-specific replay costs were not applied: %#v", records)
	}
}

func TestEvaluateObservedArmBUsesEveryActualSiblingTarget(t *testing.T) {
	first := placementFixture("request-1")
	first.Target = first.Snapshot.Endpoints[1].Ref
	second := placementFixture("request-2")
	second.Target = second.Snapshot.Endpoints[1].Ref
	second.ObservedAt = first.ObservedAt.Add(time.Millisecond)
	second.Snapshot.ObservedAt = second.ObservedAt

	got, err := EvaluateObservedArmB([]evidence.PlacementEvent{second, first}, fixtureInput(2, 2).Calibration, 4096)
	if err != nil {
		t.Fatalf("EvaluateObservedArmB() error = %v", err)
	}
	if targets := replayTargets(got); !sameStrings(targets, []string{"endpoint-1", "endpoint-1"}) {
		t.Fatalf("targets = %v, want observed upstream target vector", targets)
	}
	if got.K != 1 || got.PredictedMakespan <= fixtureInput(2, 2).Calibration.ServiceSeconds {
		t.Fatalf("observed baseline = %#v, want serialized work on one actual target", got)
	}
}

func TestEvaluateObservedArmBRejectsIncompleteSiblingVector(t *testing.T) {
	event := placementFixture("request-1")
	if _, err := EvaluateObservedArmB([]evidence.PlacementEvent{event}, fixtureInput(2, 2).Calibration, 4096); err == nil {
		t.Fatal("EvaluateObservedArmB() accepted one event for a width-two group")
	}
}

func TestEvaluateObservedArmBUsesRecordedSelectedP2PSource(t *testing.T) {
	event := placementFixture("request-1")
	event.FanoutWidth = 2
	event.Snapshot.Endpoints[0].LocalPrefixTokens = 0
	fast := evidence.PrefixSource{Source: event.Snapshot.Endpoints[1].Ref, CachedTokens: 4096, TransferBytes: 100}
	slow := evidence.PrefixSource{Source: evidence.EndpointRef{ID: "endpoint-2", Model: "test-model"}, CachedTokens: 4096, TransferBytes: 10_000_000}
	event.Snapshot.Endpoints = append(event.Snapshot.Endpoints, evidence.EndpointState{Ref: slow.Source, Healthy: true, Compatible: true, LocalPrefixTokens: 4096})
	event.Snapshot.Endpoints[0].P2PSources = []evidence.PrefixSource{fast, slow}
	event.SelectedP2PSource = &slow
	event.SelectedP2PSourceHost = "endpoint-2.local"
	event.SelectedP2PSourcePort = 5600
	second := event
	second.RequestID = "request-2"
	second.ObservedAt = second.ObservedAt.Add(time.Millisecond)
	second.Snapshot.ObservedAt = second.ObservedAt

	got, err := EvaluateObservedArmB([]evidence.PlacementEvent{event, second}, fixtureInput(2, 2).Calibration, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if got.Slots[0].PrefixReadyAt != 10 || got.Slots[0].Acquisition != model.AcquisitionP2PPull {
		t.Fatalf("first slot = %#v, want forced selected P2P transfer time 10", got.Slots[0])
	}
}

func TestOracleScoresSharedIndexedP2PSourceForEveryCounterfactualTarget(t *testing.T) {
	in := fixtureInput(2, 3)
	in.Calibration.ServiceSeconds = 0.1
	shared := in.Snapshot.Endpoints[0].Ref
	in.Snapshot.Endpoints[0].LocalPrefixTokens = 0
	in.Snapshot.Endpoints[0].SourcePrefixTokens = 4096
	for index := 1; index < 3; index++ {
		in.Snapshot.Endpoints[index].LocalPrefixTokens = 0
		in.Snapshot.Endpoints[index].P2PSources = []evidence.PrefixSource{{Source: shared, CachedTokens: 4096}}
	}
	first := placementFixture("request-1")
	first.Snapshot = in.Snapshot
	first.ObservedAt = in.Snapshot.ObservedAt
	first.Target = in.Snapshot.Endpoints[1].Ref
	first.SelectedP2PSource = &evidence.PrefixSource{Source: shared, CachedTokens: 4096}
	first.SelectedP2PSourceHost = "endpoint-0.local"
	first.SelectedP2PSourcePort = 5600
	second := first
	second.RequestID = "request-2"
	second.Target = in.Snapshot.Endpoints[2].Ref
	second.ObservedAt = first.ObservedAt.Add(time.Millisecond)
	second.Snapshot.ObservedAt = second.ObservedAt

	baseline, err := EvaluateObservedArmB([]evidence.PlacementEvent{first, second}, in.Calibration, in.PrefixTokens)
	if err != nil {
		t.Fatal(err)
	}
	in.ArrivalOffsetsSeconds = []float64{0, .001}
	result, err := EvaluateAllWidthsAgainst(in, baseline)
	if err != nil {
		t.Fatalf("counterfactual shared-source replay failed: %v", err)
	}
	if result.BestK != 2 {
		t.Fatalf("best K = %d, want two P2P-capable targets: %#v", result.BestK, result)
	}
	for _, slot := range result.Evaluations[1].Slots {
		if slot.Acquisition != model.AcquisitionP2PPull {
			t.Fatalf("slot = %#v, want indexed shared-source P2P", slot)
		}
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

func oracleGroupFixture(event evidence.PlacementEvent, prefixTokens uint64, outputRegime string, maxTokens uint32, requestIDs ...string) evidence.GroupResult {
	children := make([]evidence.ChildResult, len(requestIDs))
	dispatched := event.ObservedAt
	firstToken := dispatched.Add(500 * time.Millisecond)
	completed := dispatched.Add(time.Second)
	for index, requestID := range requestIDs {
		children[index] = evidence.ChildResult{RequestID: requestID, TTFTSeconds: .5, LatencySeconds: 1, Outcome: evidence.OutcomeSuccess, DispatchedAt: &dispatched, FirstTokenAt: &firstToken, CompletedAt: &completed}
	}
	return evidence.GroupResult{
		SchemaVersion: evidence.SchemaVersion, RunID: event.RunID, Arm: event.Arm, GroupID: event.GroupID, FanoutWidth: event.FanoutWidth,
		MakespanSeconds: 1, SlowestChildTTFTSeconds: .5,
		Cell:     evidence.BenchmarkCell{Repetition: 1, PrefixRegime: "fixture-prefix", PrefixTokens: prefixTokens, OutputRegime: outputRegime, MaxTokens: maxTokens, ArrivalSkewMS: event.ArrivalSkewMS, LoadRegime: event.LoadRegime, CacheState: "warm-owner", Transport: "tcp", EPPReplicas: event.EPPReplicas},
		Outcome:  evidence.OutcomeSuccess,
		Children: children,
	}
}

func replayTargets(result ReplayResult) []string {
	got := make([]string, len(result.Slots))
	for index, slot := range result.Slots {
		got[index] = slot.Target.ID
	}
	return got
}

func TestMeasuredQueueAndInflightBecomeAvailability(t *testing.T) {
	now := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)
	snapshot := evidence.EndpointSnapshot{ObservedAt: now, Endpoints: []evidence.EndpointState{{QueueDepth: 2, RunningRequests: 3, ObservedInflight: 4}}}
	got := withMeasuredAvailability(snapshot, 0.5)
	if got.Endpoints[0].AvailableAtSeconds != 3.0 {
		t.Fatalf("availability = %v, want 3", got.Endpoints[0].AvailableAtSeconds)
	}
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

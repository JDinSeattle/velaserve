package evidence

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

func TestValidatePlacementEventAcceptsRegisteredCell(t *testing.T) {
	event := validPlacementEvent()
	if err := ValidatePlacementEvent(event); err != nil {
		t.Fatalf("ValidatePlacementEvent() error = %v", err)
	}
}

func TestValidatePlacementEventRejectsUnregisteredWidth(t *testing.T) {
	event := validPlacementEvent()
	event.FanoutWidth = 3
	assertErrorContains(t, ValidatePlacementEvent(event), "fanout_width")
}

func TestValidatePlacementEventRejectsUnregisteredArrivalSkew(t *testing.T) {
	event := validPlacementEvent()
	event.ArrivalSkewMS = 2
	assertErrorContains(t, ValidatePlacementEvent(event), "arrival_skew_ms")
}

func TestValidatePlacementEventRejectsMissingSelectedTarget(t *testing.T) {
	event := validPlacementEvent()
	event.Target = EndpointRef{}
	assertErrorContains(t, ValidatePlacementEvent(event), "target")
}

func TestValidatePlacementEventRejectsSnapshotTimeDrift(t *testing.T) {
	event := validPlacementEvent()
	event.Snapshot.ObservedAt = event.ObservedAt.Add(time.Nanosecond)
	assertErrorContains(t, ValidatePlacementEvent(event), "snapshot observation time")
}

func TestValidatePlacementEventRejectsSelectedSourceNotAdvertisedByTarget(t *testing.T) {
	event := validPlacementEvent()
	source := EndpointState{Ref: EndpointRef{ID: "sim-1", Model: "test-model"}, Healthy: true, Compatible: true, LocalPrefixTokens: 4096, SourcePrefixTokens: 4096}
	event.Snapshot.Endpoints = append(event.Snapshot.Endpoints, source)
	selected := PrefixSource{Source: source.Ref, CachedTokens: 4096, TransferBytes: 2 << 20}
	event.SelectedP2PSource = &selected
	event.SelectedP2PSourceHost = "10.0.0.2"
	event.SelectedP2PSourcePort = 7777
	assertErrorContains(t, ValidatePlacementEvent(event), "not advertised by the compute target")
	event.Snapshot.Endpoints[0].P2PSources = []PrefixSource{selected}
	if err := ValidatePlacementEvent(event); err != nil {
		t.Fatalf("ValidatePlacementEvent() rejected target-advertised source: %v", err)
	}
}

func TestValidateGroupResultKeepsFailedRun(t *testing.T) {
	result := validGroupResult()
	result.Outcome = OutcomeFailure
	result.Failure = "upstream timeout"
	result.Children[1].Outcome = OutcomeFailure
	result.Children[1].Failure = "HTTP 504"
	result.RecomputedPrefixTokens = uint64Pointer(2048)
	if err := ValidateGroupResult(result); err != nil {
		t.Fatalf("ValidateGroupResult() rejected a recorded failure: %v", err)
	}
}

func TestValidateGroupResultRejectsNonFiniteTiming(t *testing.T) {
	result := validGroupResult()
	result.MakespanSeconds = math.Inf(1)
	assertErrorContains(t, ValidateGroupResult(result), "makespan_seconds")
}

func TestValidateGroupResultRejectsTimestampMetricTampering(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*GroupResult)
		want   string
	}{
		{name: "child TTFT", mutate: func(result *GroupResult) { result.Children[0].TTFTSeconds += .1 }, want: "ttft_seconds does not match timestamps"},
		{name: "child latency", mutate: func(result *GroupResult) { result.Children[0].LatencySeconds += .1 }, want: "latency_seconds does not match timestamps"},
		{name: "slowest TTFT", mutate: func(result *GroupResult) { result.SlowestChildTTFTSeconds += .1 }, want: "slowest_child_ttft_seconds does not match children"},
		{name: "makespan", mutate: func(result *GroupResult) { result.MakespanSeconds += .1 }, want: "makespan_seconds does not match timestamps"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := validGroupResult()
			test.mutate(&result)
			assertErrorContains(t, ValidateGroupResult(result), test.want)
		})
	}
}

func TestValidateGroupResultRejectsMissingExecutionTimestamps(t *testing.T) {
	result := validGroupResult()
	for index := range result.Children {
		result.Children[index].DispatchedAt = nil
		result.Children[index].FirstTokenAt = nil
		result.Children[index].CompletedAt = nil
	}
	assertErrorContains(t, ValidateGroupResult(result), "dispatch and completion timestamps are required")
}

func TestValidateGroupResultRejectsConditionAppliedAfterDispatch(t *testing.T) {
	result := validGroupResult()
	result.Condition = &ConditionAttestation{
		SchemaVersion: ConditionAttestationSchemaVersion,
		RunID:         result.RunID, GroupID: result.GroupID, LoadRegime: result.Cell.LoadRegime, CacheState: result.Cell.CacheState,
		ControllerRevision: strings.Repeat("a", 40), ObservedState: testConditionState([]string{"model-0"}), AppliedAt: result.Children[0].DispatchedAt.Add(time.Millisecond), FinalizedAt: timePointer(result.Children[0].CompletedAt.Add(time.Second)),
	}
	result.Condition.StateSHA256 = conditionStateSHA256(*result.Condition)
	assertErrorContains(t, ValidateGroupResult(result), "must precede every child dispatch")
}

func TestValidateGroupResultRejectsSuccessWithFailureReason(t *testing.T) {
	result := validGroupResult()
	result.Failure = "stale error"
	assertErrorContains(t, ValidateGroupResult(result), "failure")
}

func TestValidateGroupResultRejectsWrongChildCount(t *testing.T) {
	result := validGroupResult()
	result.Children = result.Children[:1]
	assertErrorContains(t, ValidateGroupResult(result), "children")
}

func TestValidateGroupResultRejectsMissingBenchmarkCell(t *testing.T) {
	result := validGroupResult()
	result.Cell = BenchmarkCell{}
	assertErrorContains(t, ValidateGroupResult(result), "cell")
}

func TestValidateGroupResultAllowsTargetToBeCorrelatedLater(t *testing.T) {
	result := validGroupResult()
	result.Children[0].Target = nil
	if err := ValidateGroupResult(result); err != nil {
		t.Fatalf("ValidateGroupResult() rejected uncorrelated target: %v", err)
	}
}

func TestValidateGroupResultRejectsTamperedConditionObservedState(t *testing.T) {
	result := validGroupResult()
	result.Condition = &ConditionAttestation{
		SchemaVersion: ConditionAttestationSchemaVersion,
		RunID:         result.RunID, GroupID: result.GroupID, LoadRegime: result.Cell.LoadRegime, CacheState: result.Cell.CacheState,
		ControllerRevision: strings.Repeat("a", 40), ObservedState: testConditionState([]string{"model-0"}), AppliedAt: result.Children[0].DispatchedAt.Add(-time.Second), FinalizedAt: timePointer(result.Children[0].CompletedAt.Add(time.Second)),
	}
	result.Condition.StateSHA256 = conditionStateSHA256(*result.Condition)
	if err := ValidateGroupResult(result); err != nil {
		t.Fatalf("ValidateGroupResult() rejected bound condition state: %v", err)
	}
	result.Condition.ObservedState.AchievedLoadQPS = 40
	assertErrorContains(t, ValidateGroupResult(result), "state SHA-256")
}

func TestValidateGroupResultRetainsUnfinalizedAppliedConditionOnlyAsFailure(t *testing.T) {
	result := validGroupResult()
	result.Outcome = OutcomeFailure
	result.Failure = "finalize workload condition: outside offered-load band"
	result.ConditionFailure = result.Failure
	result.Condition = &ConditionAttestation{
		SchemaVersion: ConditionAttestationSchemaVersion,
		RunID:         result.RunID, GroupID: result.GroupID, LoadRegime: result.Cell.LoadRegime, CacheState: result.Cell.CacheState,
		ControllerRevision: strings.Repeat("a", 40), ObservedState: testConditionState([]string{"model-0"}), AppliedAt: result.Children[0].DispatchedAt.Add(-time.Second),
	}
	result.Condition.StateSHA256 = conditionStateSHA256(*result.Condition)
	if err := ValidateGroupResult(result); err != nil {
		t.Fatalf("ValidateGroupResult() rejected retained finalization failure: %v", err)
	}
	result.ConditionFailure = ""
	assertErrorContains(t, ValidateGroupResult(result), "unfinalized")
}

func testConditionState(cached []string) ConditionObservedState {
	return ConditionObservedState{
		OfferedLoadQPS: 50, AchievedLoadQPS: 50, SaturationQPS: 100,
		LoadProfileSHA256: strings.Repeat("1", 64), LoadProfilesSHA256: strings.Repeat("2", 64), LoadCalibrationSHA256: strings.Repeat("3", 64),
		CachedEndpointIDs: cached, OwnerEndpointOrder: []string{"model-0", "model-1"}, DrainedEndpointIDs: []string{"model-0", "model-1"}, DrainStableSamples: 2, OrdinaryTrafficMeanLatencySeconds: .01, MeasurementSource: "load-calibration:" + strings.Repeat("3", 64),
	}
}

func timePointer(value time.Time) *time.Time { return &value }

func TestGroupResultOmitsUnmeasuredRecomputedPrefixTokens(t *testing.T) {
	result := validGroupResult()
	result.RecomputedPrefixTokens = nil
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "recomputed_prefix_tokens") {
		t.Fatalf("unmeasured result serialized recomputed_prefix_tokens: %s", encoded)
	}
}

func TestValidateGroupResultRecomputesPrefixTokensFromChildReadbacks(t *testing.T) {
	result := validGroupResult()
	if err := ValidateGroupResult(result); err != nil {
		t.Fatal(err)
	}
	*result.RecomputedPrefixTokens++
	assertErrorContains(t, ValidateGroupResult(result), "recomputed_prefix_tokens")
	result = validGroupResult()
	result.Children[0].CachedTokens = nil
	assertErrorContains(t, ValidateGroupResult(result), "without complete")
}

func validPlacementEvent() PlacementEvent {
	now := time.Date(2026, 8, 25, 20, 0, 0, 0, time.UTC)
	return PlacementEvent{
		SchemaVersion: SchemaVersion,
		RunID:         "run-local-001",
		Arm:           ArmLoadAwareP2P,
		GroupID:       "01K39J6FJ4N5W7V57QK9Q0C6CF",
		RequestID:     "request-01",
		FanoutWidth:   2,
		ArrivalSkewMS: 1,
		EPPReplicas:   2,
		LoadRegime:    LoadModerate,
		Snapshot: EndpointSnapshot{
			ObservedAt: now,
			Endpoints: []EndpointState{
				{
					Ref:                EndpointRef{ID: "sim-0", Model: "test-model"},
					Healthy:            true,
					Compatible:         true,
					AvailableAtSeconds: 0.25,
					LocalPrefixTokens:  4096,
				},
			},
		},
		Target:     EndpointRef{ID: "sim-0", Model: "test-model"},
		ObservedAt: now,
	}
}

func validGroupResult() GroupResult {
	dispatched := time.Date(2026, 8, 25, 20, 0, 0, 0, time.UTC)
	firstToken := dispatched.Add(400 * time.Millisecond)
	firstCompleted := dispatched.Add(1300 * time.Millisecond)
	secondFirstToken := dispatched.Add(500 * time.Millisecond)
	secondCompleted := dispatched.Add(1500 * time.Millisecond)
	return GroupResult{
		SchemaVersion:           SchemaVersion,
		RunID:                   "run-local-001",
		Arm:                     ArmLoadAwareP2P,
		GroupID:                 "01K39J6FJ4N5W7V57QK9Q0C6CF",
		FanoutWidth:             2,
		MakespanSeconds:         1.5,
		SlowestChildTTFTSeconds: 0.5,
		RecomputedPrefixTokens:  uint64Pointer(4096),
		Cell: BenchmarkCell{
			Repetition:    1,
			PrefixRegime:  "near-crossover",
			PrefixTokens:  4096,
			OutputRegime:  "short",
			MaxTokens:     32,
			ArrivalSkewMS: 1,
			LoadRegime:    LoadModerate,
			CacheState:    "warm-owner",
			Transport:     "tcp",
			EPPReplicas:   2,
		},
		Outcome: OutcomeSuccess,
		Children: []ChildResult{
			{RequestID: "request-01", Target: &EndpointRef{ID: "sim-0", Model: "test-model"}, TTFTSeconds: 0.4, LatencySeconds: 1.3, PromptTokens: 4097, CachedTokens: uint64Pointer(2048), Outcome: OutcomeSuccess, DispatchedAt: &dispatched, FirstTokenAt: &firstToken, CompletedAt: &firstCompleted},
			{RequestID: "request-02", Target: &EndpointRef{ID: "sim-1", Model: "test-model"}, TTFTSeconds: 0.5, LatencySeconds: 1.5, PromptTokens: 4097, CachedTokens: uint64Pointer(2048), Outcome: OutcomeSuccess, DispatchedAt: &dispatched, FirstTokenAt: &secondFirstToken, CompletedAt: &secondCompleted},
		},
	}
}

func uint64Pointer(value uint64) *uint64 { return &value }

func assertErrorContains(t *testing.T, err error, fragment string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error containing %q, got nil", fragment)
	}
	if got := err.Error(); !contains(got, fragment) {
		t.Fatalf("error = %q, want substring %q", got, fragment)
	}
}

func contains(s, sub string) bool {
	if sub == "" {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

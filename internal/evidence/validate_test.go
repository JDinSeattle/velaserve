package evidence

import (
	"math"
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

func TestValidateGroupResultKeepsFailedRun(t *testing.T) {
	result := validGroupResult()
	result.Outcome = OutcomeFailure
	result.Failure = "upstream timeout"
	result.Children[1].Outcome = OutcomeFailure
	result.Children[1].Failure = "HTTP 504"
	if err := ValidateGroupResult(result); err != nil {
		t.Fatalf("ValidateGroupResult() rejected a recorded failure: %v", err)
	}
}

func TestValidateGroupResultRejectsNonFiniteTiming(t *testing.T) {
	result := validGroupResult()
	result.MakespanSeconds = math.Inf(1)
	assertErrorContains(t, ValidateGroupResult(result), "makespan_seconds")
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
	return GroupResult{
		SchemaVersion:           SchemaVersion,
		RunID:                   "run-local-001",
		Arm:                     ArmLoadAwareP2P,
		GroupID:                 "01K39J6FJ4N5W7V57QK9Q0C6CF",
		FanoutWidth:             2,
		MakespanSeconds:         1.5,
		SlowestChildTTFTSeconds: 0.5,
		RecomputedPrefixTokens:  4096,
		Outcome:                 OutcomeSuccess,
		Children: []ChildResult{
			{RequestID: "request-01", Target: EndpointRef{ID: "sim-0", Model: "test-model"}, TTFTSeconds: 0.4, LatencySeconds: 1.3, Outcome: OutcomeSuccess},
			{RequestID: "request-02", Target: EndpointRef{ID: "sim-1", Model: "test-model"}, TTFTSeconds: 0.5, LatencySeconds: 1.5, Outcome: OutcomeSuccess},
		},
	}
}

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

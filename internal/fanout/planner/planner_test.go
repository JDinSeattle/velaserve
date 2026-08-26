package planner

import (
	"context"
	"testing"
	"time"

	"github.com/JDinSeattle/velaserve/internal/evidence"
)

func TestPlanSpreadsWhenWarmOwnerIsBusy(t *testing.T) {
	warm := endpoint("warm")
	idle := endpoint("idle")
	in := Input{
		Width:        4,
		PrefixTokens: 1000,
		Snapshot: snapshot(
			state(warm, 12, 1000),
			stateWithSource(idle, 0, 0, warm, 1000, 1000),
		),
		Calibration: Calibration{
			PrefillTokensPerSecond: 100,
			PullBytesPerSecond:     1000,
			BytesPerCachedToken:    1,
			ServiceSeconds:         4,
		},
	}

	got, err := Plan(context.Background(), in)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	wantTargets := []string{"idle", "idle", "idle", "warm"}
	if targets := targetIDs(got); !equalStrings(targets, wantTargets) {
		t.Fatalf("targets = %v, want %v", targets, wantTargets)
	}
	if got.K != 2 {
		t.Fatalf("K = %d, want 2", got.K)
	}
	if got.MaxPredictedFinish != 16 {
		t.Fatalf("MaxPredictedFinish = %v, want 16", got.MaxPredictedFinish)
	}
}

func TestPlanReusesPrefixReadinessOnSameEndpoint(t *testing.T) {
	owner := endpoint("only")
	in := Input{
		Width:        2,
		PrefixTokens: 1000,
		Snapshot:     snapshot(state(owner, 0, 0)),
		Calibration: Calibration{
			PrefillTokensPerSecond: 100,
			PullBytesPerSecond:     1000,
			BytesPerCachedToken:    1,
			ServiceSeconds:         2,
		},
	}

	got, err := Plan(context.Background(), in)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if got.Slots[0].PrefixReadyAt != 10 || got.Slots[0].PredictedFinish != 12 {
		t.Fatalf("first slot = %#v, want prefix ready 10 and finish 12", got.Slots[0])
	}
	if got.Slots[1].PrefixReadyAt != 10 || got.Slots[1].PredictedFinish != 14 {
		t.Fatalf("second slot = %#v, want reused prefix ready 10 and finish 14", got.Slots[1])
	}
}

func TestPlanNeverUsesIneligibleEndpoint(t *testing.T) {
	dead := state(endpoint("dead"), 0, 1000)
	dead.Healthy = false
	incompatible := state(endpoint("wrong-model"), 0, 1000)
	incompatible.Compatible = false
	live := state(endpoint("live"), 0, 0)
	in := Input{
		Width:        8,
		PrefixTokens: 1000,
		Snapshot:     snapshot(dead, incompatible, live),
		Calibration:  validCalibration(),
	}

	got, err := Plan(context.Background(), in)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	for _, slot := range got.Slots {
		if slot.Target.ID != "live" {
			t.Fatalf("selected ineligible target %q", slot.Target.ID)
		}
	}
}

func TestPlanBreaksTiesByStableEndpointID(t *testing.T) {
	in := Input{
		Width:        2,
		PrefixTokens: 0,
		Snapshot:     snapshot(state(endpoint("b"), 0, 0), state(endpoint("a"), 0, 0)),
		Calibration:  validCalibration(),
	}

	got, err := Plan(context.Background(), in)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	want := []string{"a", "b"}
	if targets := targetIDs(got); !equalStrings(targets, want) {
		t.Fatalf("targets = %v, want %v", targets, want)
	}
}

func TestPlanRejectsNoEligibleEndpoints(t *testing.T) {
	dead := state(endpoint("dead"), 0, 0)
	dead.Healthy = false
	_, err := Plan(context.Background(), Input{Width: 2, Snapshot: snapshot(dead), Calibration: validCalibration()})
	if err == nil {
		t.Fatal("Plan() error = nil, want no eligible endpoints")
	}
}

func endpoint(id string) evidence.EndpointRef {
	return evidence.EndpointRef{ID: id, Model: "test-model"}
}

func state(ref evidence.EndpointRef, available float64, localTokens uint64) evidence.EndpointState {
	return evidence.EndpointState{
		Ref:                ref,
		Healthy:            true,
		Compatible:         true,
		AvailableAtSeconds: available,
		LocalPrefixTokens:  localTokens,
	}
}

func stateWithSource(ref evidence.EndpointRef, available float64, localTokens uint64, source evidence.EndpointRef, cachedTokens, transferBytes uint64) evidence.EndpointState {
	result := state(ref, available, localTokens)
	result.P2PSources = []evidence.PrefixSource{{Source: source, CachedTokens: cachedTokens, TransferBytes: transferBytes}}
	return result
}

func snapshot(states ...evidence.EndpointState) evidence.EndpointSnapshot {
	return evidence.EndpointSnapshot{ObservedAt: time.Date(2026, 8, 25, 20, 0, 0, 0, time.UTC), Endpoints: states}
}

func validCalibration() Calibration {
	return Calibration{PrefillTokensPerSecond: 100, PullBytesPerSecond: 1000, BytesPerCachedToken: 1, ServiceSeconds: 1}
}

func targetIDs(plan GroupPlan) []string {
	result := make([]string, len(plan.Slots))
	for index, slot := range plan.Slots {
		result[index] = slot.Target.ID
	}
	return result
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}

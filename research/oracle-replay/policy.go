package replay

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/JDinSeattle/velaserve/internal/evidence"
	"github.com/JDinSeattle/velaserve/internal/fanout/planner"
)

type Input struct {
	Width                           uint32                    `json:"width"`
	PrefixTokens                    uint64                    `json:"prefix_tokens"`
	Snapshot                        evidence.EndpointSnapshot `json:"snapshot"`
	Calibration                     planner.Calibration       `json:"calibration"`
	ArrivalSkewSeconds              float64                   `json:"arrival_skew_seconds"`
	InflightPublicationDelaySeconds float64                   `json:"inflight_publication_delay_seconds"`
	AffinityLoadGateSeconds         float64                   `json:"affinity_load_gate_seconds"`
}

type ReplayResult struct {
	Arm               evidence.Arm             `json:"arm"`
	Width             uint32                   `json:"width"`
	K                 uint32                   `json:"k"`
	PredictedMakespan float64                  `json:"predicted_makespan_seconds"`
	Slots             []planner.SlotAssignment `json:"slots"`
}

type Policy interface {
	Name() evidence.Arm
	Replay(Input) (ReplayResult, error)
}

type LoadAwareP2PPolicy struct{}

func (LoadAwareP2PPolicy) Name() evidence.Arm { return evidence.ArmLoadAwareP2P }
func (LoadAwareP2PPolicy) Replay(in Input) (ReplayResult, error) {
	return replayPerRequest(in, evidence.ArmLoadAwareP2P, chooseLoadAware)
}

type AffinityP2PPolicy struct{}

func (AffinityP2PPolicy) Name() evidence.Arm { return evidence.ArmAffinityP2P }
func (AffinityP2PPolicy) Replay(in Input) (ReplayResult, error) {
	return replayPerRequest(in, evidence.ArmAffinityP2P, chooseAffinity)
}

func ReplayArmA(in Input) (ReplayResult, error) { return AffinityP2PPolicy{}.Replay(in) }
func ReplayArmB(in Input) (ReplayResult, error) { return LoadAwareP2PPolicy{}.Replay(in) }

type chooser func(Input, evidence.EndpointSnapshot) (evidence.EndpointRef, error)

type pendingPublication struct {
	publishAt   float64
	target      evidence.EndpointRef
	availableAt float64
}

func replayPerRequest(in Input, arm evidence.Arm, selectTarget chooser) (ReplayResult, error) {
	if err := validateReplayInput(in); err != nil {
		return ReplayResult{}, err
	}
	observed := copySnapshot(in.Snapshot)
	actualAvailability := make(map[evidence.EndpointRef]float64, len(observed.Endpoints))
	for _, endpoint := range observed.Endpoints {
		actualAvailability[endpoint.Ref] = endpoint.AvailableAtSeconds
	}
	pending := make([]pendingPublication, 0, in.Width)
	result := ReplayResult{Arm: arm, Width: in.Width, Slots: make([]planner.SlotAssignment, 0, in.Width)}
	distinct := make(map[evidence.EndpointRef]struct{})
	for slotID := uint32(0); slotID < in.Width; slotID++ {
		arrival := float64(slotID) * in.ArrivalSkewSeconds
		remaining := pending[:0]
		for _, update := range pending {
			if update.publishAt <= arrival+1e-12 {
				setAvailability(&observed, update.target, update.availableAt)
			} else {
				remaining = append(remaining, update)
			}
		}
		pending = remaining
		decisionSnapshot := relativeSnapshot(observed, arrival)
		target, err := selectTarget(in, decisionSnapshot)
		if err != nil {
			return ReplayResult{}, fmt.Errorf("replay %s slot %d: %w", arm, slotID, err)
		}
		actualEndpoint, ok := findEndpoint(in.Snapshot, target)
		if !ok {
			return ReplayResult{}, fmt.Errorf("replay %s slot %d: selected endpoint %q is absent", arm, slotID, target.ID)
		}
		actualEndpoint.AvailableAtSeconds = math.Max(0, actualAvailability[target]-arrival)
		actualPlan, err := planner.Plan(context.Background(), planner.Input{
			Width:        1,
			PrefixTokens: in.PrefixTokens,
			Snapshot: evidence.EndpointSnapshot{
				ObservedAt: in.Snapshot.ObservedAt,
				Endpoints:  []evidence.EndpointState{actualEndpoint},
			},
			Calibration: in.Calibration,
		})
		if err != nil {
			return ReplayResult{}, fmt.Errorf("replay %s slot %d actual completion: %w", arm, slotID, err)
		}
		assignment := actualPlan.Slots[0]
		assignment.SlotID = slotID
		assignment.PrefixReadyAt += arrival
		assignment.PredictedFinish += arrival
		result.Slots = append(result.Slots, assignment)
		actualAvailability[target] = assignment.PredictedFinish
		pending = append(pending, pendingPublication{
			publishAt:   arrival + in.InflightPublicationDelaySeconds,
			target:      target,
			availableAt: assignment.PredictedFinish,
		})
		distinct[target] = struct{}{}
		if assignment.PredictedFinish > result.PredictedMakespan {
			result.PredictedMakespan = assignment.PredictedFinish
		}
	}
	result.K = uint32(len(distinct))
	return result, nil
}

func chooseLoadAware(in Input, snapshot evidence.EndpointSnapshot) (evidence.EndpointRef, error) {
	plan, err := planner.Plan(context.Background(), planner.Input{
		Width:        1,
		PrefixTokens: in.PrefixTokens,
		Snapshot:     snapshot,
		Calibration:  in.Calibration,
	})
	if err != nil {
		return evidence.EndpointRef{}, err
	}
	return plan.Slots[0].Target, nil
}

func chooseAffinity(in Input, snapshot evidence.EndpointSnapshot) (evidence.EndpointRef, error) {
	eligible := make([]evidence.EndpointState, 0, len(snapshot.Endpoints))
	for _, endpoint := range snapshot.Endpoints {
		if endpoint.Healthy && endpoint.Compatible {
			eligible = append(eligible, endpoint)
		}
	}
	if len(eligible) == 0 {
		return evidence.EndpointRef{}, fmt.Errorf("no eligible endpoints")
	}
	sort.Slice(eligible, func(i, j int) bool {
		if eligible[i].LocalPrefixTokens != eligible[j].LocalPrefixTokens {
			return eligible[i].LocalPrefixTokens > eligible[j].LocalPrefixTokens
		}
		if eligible[i].AvailableAtSeconds != eligible[j].AvailableAtSeconds {
			return eligible[i].AvailableAtSeconds < eligible[j].AvailableAtSeconds
		}
		return refLess(eligible[i].Ref, eligible[j].Ref)
	})
	warm := eligible[0]
	minimumAvailability := warm.AvailableAtSeconds
	for _, endpoint := range eligible[1:] {
		minimumAvailability = math.Min(minimumAvailability, endpoint.AvailableAtSeconds)
	}
	if warm.AvailableAtSeconds-minimumAvailability > in.AffinityLoadGateSeconds {
		return chooseLoadAware(in, snapshot)
	}
	return warm.Ref, nil
}

func validateReplayInput(in Input) error {
	if in.Width == 0 || in.Width > 4096 {
		return fmt.Errorf("width must be in [1,4096]")
	}
	if invalidNonNegative(in.ArrivalSkewSeconds) {
		return fmt.Errorf("arrival skew must be finite and non-negative")
	}
	if invalidNonNegative(in.InflightPublicationDelaySeconds) {
		return fmt.Errorf("inflight publication delay must be finite and non-negative")
	}
	if invalidNonNegative(in.AffinityLoadGateSeconds) {
		return fmt.Errorf("affinity load gate must be finite and non-negative")
	}
	_, err := planner.Plan(context.Background(), planner.Input{
		Width:        1,
		PrefixTokens: in.PrefixTokens,
		Snapshot:     in.Snapshot,
		Calibration:  in.Calibration,
	})
	return err
}

func relativeSnapshot(snapshot evidence.EndpointSnapshot, at float64) evidence.EndpointSnapshot {
	result := copySnapshot(snapshot)
	for index := range result.Endpoints {
		result.Endpoints[index].AvailableAtSeconds = math.Max(0, result.Endpoints[index].AvailableAtSeconds-at)
	}
	return result
}

func copySnapshot(snapshot evidence.EndpointSnapshot) evidence.EndpointSnapshot {
	result := snapshot
	result.Endpoints = append([]evidence.EndpointState(nil), snapshot.Endpoints...)
	return result
}

func setAvailability(snapshot *evidence.EndpointSnapshot, ref evidence.EndpointRef, availableAt float64) {
	for index := range snapshot.Endpoints {
		if snapshot.Endpoints[index].Ref == ref && availableAt > snapshot.Endpoints[index].AvailableAtSeconds {
			snapshot.Endpoints[index].AvailableAtSeconds = availableAt
			return
		}
	}
}

func findEndpoint(snapshot evidence.EndpointSnapshot, ref evidence.EndpointRef) (evidence.EndpointState, bool) {
	for _, endpoint := range snapshot.Endpoints {
		if endpoint.Ref == ref {
			return endpoint, true
		}
	}
	return evidence.EndpointState{}, false
}

func invalidNonNegative(value float64) bool {
	return math.IsNaN(value) || math.IsInf(value, 0) || value < 0
}

func refLess(left, right evidence.EndpointRef) bool {
	if left.ID != right.ID {
		return left.ID < right.ID
	}
	if left.Model != right.Model {
		return left.Model < right.Model
	}
	return left.Zone < right.Zone
}

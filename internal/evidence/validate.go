package evidence

import (
	"fmt"
	"math"
	"strings"
)

var registeredZ0Widths = map[uint32]struct{}{2: {}, 4: {}, 8: {}, 16: {}}
var registeredBenchmarkWidths = map[uint32]struct{}{1: {}, 2: {}, 4: {}, 8: {}, 16: {}}
var registeredSkews = map[uint32]struct{}{0: {}, 1: {}, 5: {}, 20: {}}

func ValidatePlacementEvent(event PlacementEvent) error {
	if event.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schema_version: got %q, want %q", event.SchemaVersion, SchemaVersion)
	}
	if err := requireID("run_id", event.RunID); err != nil {
		return err
	}
	if err := requireID("group_id", event.GroupID); err != nil {
		return err
	}
	if err := requireID("request_id", event.RequestID); err != nil {
		return err
	}
	if !isObservedArm(event.Arm) {
		return fmt.Errorf("arm: unsupported value %q", event.Arm)
	}
	if _, ok := registeredZ0Widths[event.FanoutWidth]; !ok {
		return fmt.Errorf("fanout_width: %d is not registered for Z0", event.FanoutWidth)
	}
	if _, ok := registeredSkews[event.ArrivalSkewMS]; !ok {
		return fmt.Errorf("arrival_skew_ms: %d is not registered", event.ArrivalSkewMS)
	}
	if event.EPPReplicas != 1 && event.EPPReplicas != 2 {
		return fmt.Errorf("epp_replicas: got %d, want 1 or 2", event.EPPReplicas)
	}
	if !isLoadRegime(event.LoadRegime) {
		return fmt.Errorf("load_regime: unsupported value %q", event.LoadRegime)
	}
	if event.ObservedAt.IsZero() {
		return fmt.Errorf("observed_at: required")
	}
	if err := validateSnapshot(event.Snapshot); err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}
	if err := validateEndpointRef(event.Target); err != nil {
		return fmt.Errorf("target: %w", err)
	}
	if !snapshotContains(event.Snapshot, event.Target) {
		return fmt.Errorf("target: endpoint %q is absent from snapshot", event.Target.ID)
	}
	return nil
}

func ValidateGroupResult(result GroupResult) error {
	if result.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schema_version: got %q, want %q", result.SchemaVersion, SchemaVersion)
	}
	if err := requireID("run_id", result.RunID); err != nil {
		return err
	}
	if err := requireID("group_id", result.GroupID); err != nil {
		return err
	}
	if !isResultArm(result.Arm) {
		return fmt.Errorf("arm: unsupported value %q", result.Arm)
	}
	if _, ok := registeredBenchmarkWidths[result.FanoutWidth]; !ok {
		return fmt.Errorf("fanout_width: %d is not registered", result.FanoutWidth)
	}
	if err := finiteNonNegative("makespan_seconds", result.MakespanSeconds); err != nil {
		return err
	}
	if err := finiteNonNegative("slowest_child_ttft_seconds", result.SlowestChildTTFTSeconds); err != nil {
		return err
	}
	if result.EstimatedPrefillSeconds != nil {
		if err := finiteNonNegative("estimated_prefill_gpu_seconds", *result.EstimatedPrefillSeconds); err != nil {
			return err
		}
	}
	if err := validateOutcome("outcome", result.Outcome, result.Failure); err != nil {
		return err
	}
	if len(result.Children) != int(result.FanoutWidth) {
		return fmt.Errorf("children: got %d, want fanout_width %d", len(result.Children), result.FanoutWidth)
	}
	seen := make(map[string]struct{}, len(result.Children))
	for index, child := range result.Children {
		if err := requireID(fmt.Sprintf("children[%d].request_id", index), child.RequestID); err != nil {
			return err
		}
		if _, exists := seen[child.RequestID]; exists {
			return fmt.Errorf("children[%d].request_id: duplicate %q", index, child.RequestID)
		}
		seen[child.RequestID] = struct{}{}
		if err := validateEndpointRef(child.Target); err != nil {
			return fmt.Errorf("children[%d].target: %w", index, err)
		}
		if err := finiteNonNegative(fmt.Sprintf("children[%d].ttft_seconds", index), child.TTFTSeconds); err != nil {
			return err
		}
		if err := finiteNonNegative(fmt.Sprintf("children[%d].latency_seconds", index), child.LatencySeconds); err != nil {
			return err
		}
		if child.TTFTSeconds > child.LatencySeconds {
			return fmt.Errorf("children[%d].ttft_seconds: exceeds latency_seconds", index)
		}
		if err := validateOutcome(fmt.Sprintf("children[%d].outcome", index), child.Outcome, child.Failure); err != nil {
			return err
		}
	}
	return nil
}

func validateSnapshot(snapshot EndpointSnapshot) error {
	if snapshot.ObservedAt.IsZero() {
		return fmt.Errorf("observed_at: required")
	}
	if len(snapshot.Endpoints) == 0 {
		return fmt.Errorf("endpoints: at least one endpoint is required")
	}
	seen := make(map[string]struct{}, len(snapshot.Endpoints))
	for index, endpoint := range snapshot.Endpoints {
		if err := validateEndpointRef(endpoint.Ref); err != nil {
			return fmt.Errorf("endpoints[%d].ref: %w", index, err)
		}
		key := endpoint.Ref.ID + "\x00" + endpoint.Ref.Model
		if _, exists := seen[key]; exists {
			return fmt.Errorf("endpoints[%d].ref: duplicate endpoint %q", index, endpoint.Ref.ID)
		}
		seen[key] = struct{}{}
		if err := finiteNonNegative(fmt.Sprintf("endpoints[%d].available_at_seconds", index), endpoint.AvailableAtSeconds); err != nil {
			return err
		}
	}
	return nil
}

func snapshotContains(snapshot EndpointSnapshot, target EndpointRef) bool {
	for _, endpoint := range snapshot.Endpoints {
		if endpoint.Ref == target {
			return true
		}
	}
	return false
}

func validateEndpointRef(ref EndpointRef) error {
	if strings.TrimSpace(ref.ID) == "" {
		return fmt.Errorf("id: required")
	}
	if strings.TrimSpace(ref.Model) == "" {
		return fmt.Errorf("model: required")
	}
	return nil
}

func validateOutcome(field string, outcome Outcome, failure string) error {
	switch outcome {
	case OutcomeSuccess:
		if strings.TrimSpace(failure) != "" {
			return fmt.Errorf("%s failure: success cannot include a failure reason", field)
		}
	case OutcomeFailure, OutcomeCancelled:
		if strings.TrimSpace(failure) == "" {
			return fmt.Errorf("%s failure: %s requires a failure reason", field, outcome)
		}
	default:
		return fmt.Errorf("%s: unsupported value %q", field, outcome)
	}
	return nil
}

func finiteNonNegative(field string, value float64) error {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return fmt.Errorf("%s: must be finite and non-negative", field)
	}
	return nil
}

func requireID(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s: required", field)
	}
	return nil
}

func isObservedArm(arm Arm) bool {
	return arm == ArmAffinityP2P || arm == ArmLoadAwareP2P || arm == ArmVelaPlacementP2P || arm == ArmLoadAwareNoP2P || arm == ArmSourceControlP2P
}

func isResultArm(arm Arm) bool {
	return isObservedArm(arm) || arm == ArmNawareOracle
}

func isLoadRegime(regime LoadRegime) bool {
	return regime == LoadIdle || regime == LoadModerate || regime == LoadNearSaturation
}

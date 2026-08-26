package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

var registeredZ0Widths = map[uint32]struct{}{2: {}, 4: {}, 8: {}, 16: {}}
var registeredBenchmarkWidths = map[uint32]struct{}{1: {}, 2: {}, 4: {}, 8: {}, 16: {}}
var registeredSkews = map[uint32]struct{}{0: {}, 1: {}, 5: {}, 20: {}}

const timestampMetricToleranceSeconds = 1e-6

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
	if !event.Snapshot.ObservedAt.Equal(event.ObservedAt) {
		return fmt.Errorf("snapshot observation time must equal placement observation time")
	}
	if err := validateEndpointRef(event.Target); err != nil {
		return fmt.Errorf("target: %w", err)
	}
	if !snapshotContains(event.Snapshot, event.Target) {
		return fmt.Errorf("target: endpoint %q is absent from snapshot", event.Target.ID)
	}
	seenScores := make(map[EndpointRef]struct{}, len(event.ScoredCandidates))
	for index, scored := range event.ScoredCandidates {
		if err := validateEndpointRef(scored.Endpoint); err != nil || !snapshotContains(event.Snapshot, scored.Endpoint) || math.IsNaN(scored.Score) || math.IsInf(scored.Score, 0) {
			return fmt.Errorf("scored_candidates[%d]: invalid endpoint or score", index)
		}
		if _, exists := seenScores[scored.Endpoint]; exists {
			return fmt.Errorf("scored_candidates[%d]: duplicate endpoint", index)
		}
		seenScores[scored.Endpoint] = struct{}{}
	}
	if event.SelectedP2PSource != nil {
		if err := validateEndpointRef(event.SelectedP2PSource.Source); err != nil || !snapshotContains(event.Snapshot, event.SelectedP2PSource.Source) || event.SelectedP2PSource.CachedTokens == 0 || strings.TrimSpace(event.SelectedP2PSourceHost) == "" || event.SelectedP2PSourcePort == 0 {
			return fmt.Errorf("selected_p2p_source: invalid or absent from snapshot")
		}
		if event.SelectedP2PSource.Source == event.Target {
			return fmt.Errorf("selected_p2p_source: cannot equal the compute target")
		}
		targetState, _ := snapshotEndpoint(event.Snapshot, event.Target)
		listed := false
		for _, source := range targetState.P2PSources {
			if source == *event.SelectedP2PSource {
				listed = true
				break
			}
		}
		if !listed {
			return fmt.Errorf("selected_p2p_source: not advertised by the compute target")
		}
	} else if event.SelectedP2PSourceHost != "" || event.SelectedP2PSourcePort != 0 {
		return fmt.Errorf("selected_p2p_source endpoint cannot exist without a selected source")
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
	if err := validateBenchmarkCell(result.Cell); err != nil {
		return fmt.Errorf("cell: %w", err)
	}
	if result.Condition != nil {
		condition := result.Condition
		if condition.SchemaVersion != ConditionAttestationSchemaVersion || condition.RunID != result.RunID || condition.GroupID != result.GroupID {
			return fmt.Errorf("condition: schema or group identity mismatch")
		}
		if condition.LoadRegime != result.Cell.LoadRegime || condition.CacheState != result.Cell.CacheState {
			return fmt.Errorf("condition: applied load/cache state does not match benchmark cell")
		}
		if strings.TrimSpace(condition.ControllerRevision) == "" || !isLowerHexSHA256(condition.StateSHA256) || condition.AppliedAt.IsZero() {
			return fmt.Errorf("condition: controller revision, state SHA-256, and valid apply time are required")
		}
		conditionFailure := strings.TrimSpace(result.ConditionFailure)
		if condition.FinalizedAt == nil {
			if conditionFailure == "" || result.Outcome != OutcomeFailure {
				return fmt.Errorf("condition: an unfinalized applied condition requires an explicit failed-group finalization error")
			}
		} else {
			if condition.FinalizedAt.IsZero() || condition.FinalizedAt.Before(condition.AppliedAt) {
				return fmt.Errorf("condition: valid finalize time is required")
			}
			if conditionFailure != "" {
				return fmt.Errorf("condition: a finalized condition cannot include condition_failure")
			}
		}
		if err := ValidateConditionObservedState(condition.ObservedState, condition.LoadRegime, condition.CacheState, condition.PrefixSourceCount); err != nil {
			return fmt.Errorf("condition: %w", err)
		}
		if conditionStateSHA256(*condition) != condition.StateSHA256 {
			return fmt.Errorf("condition: observed state SHA-256 does not match its applied condition")
		}
		if condition.PrefixSourceCount != 0 && condition.PrefixSourceCount != 1 && condition.PrefixSourceCount != 2 && condition.PrefixSourceCount != 4 {
			return fmt.Errorf("condition: prefix source count is not registered")
		}
	} else if strings.TrimSpace(result.ConditionFailure) != "" {
		return fmt.Errorf("condition_failure requires the retained applied condition")
	}
	if err := validateOutcome("outcome", result.Outcome, result.Failure); err != nil {
		return err
	}
	if len(result.Children) != int(result.FanoutWidth) {
		return fmt.Errorf("children: got %d, want fanout_width %d", len(result.Children), result.FanoutWidth)
	}
	seen := make(map[string]struct{}, len(result.Children))
	var earliestDispatch, latestCompletion *time.Time
	var observedSlowestTTFT float64
	recomputedPrefixTokens := uint64(0)
	recomputedMeasured := result.Cell.PrefixTokens > 0
	successfulChildren := 0
	for index, child := range result.Children {
		if err := requireID(fmt.Sprintf("children[%d].request_id", index), child.RequestID); err != nil {
			return err
		}
		if _, exists := seen[child.RequestID]; exists {
			return fmt.Errorf("children[%d].request_id: duplicate %q", index, child.RequestID)
		}
		seen[child.RequestID] = struct{}{}
		if child.Target != nil {
			if err := validateEndpointRef(*child.Target); err != nil {
				return fmt.Errorf("children[%d].target: %w", index, err)
			}
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
		if child.CachedTokens != nil && (child.PromptTokens == 0 || *child.CachedTokens > child.PromptTokens) {
			return fmt.Errorf("children[%d].cached_tokens: missing prompt-token count or exceeds it", index)
		}
		if err := validateOutcome(fmt.Sprintf("children[%d].outcome", index), child.Outcome, child.Failure); err != nil {
			return err
		}
		if result.Outcome == OutcomeSuccess && child.Outcome != OutcomeSuccess {
			return fmt.Errorf("children[%d].outcome: successful group contains a non-success child", index)
		}
		if child.Outcome == OutcomeSuccess {
			successfulChildren++
			if child.CachedTokens == nil || child.PromptTokens == 0 {
				recomputedMeasured = false
			} else {
				cachedSharedTokens := min(*child.CachedTokens, result.Cell.PrefixTokens)
				recomputed := result.Cell.PrefixTokens - cachedSharedTokens
				if ^uint64(0)-recomputedPrefixTokens < recomputed {
					recomputedMeasured = false
				} else {
					recomputedPrefixTokens += recomputed
				}
			}
		}
		if child.CompletedAt == nil || child.CompletedAt.IsZero() {
			return fmt.Errorf("children[%d]: valid dispatch and completion timestamps are required", index)
		}
		if latestCompletion == nil || child.CompletedAt.After(*latestCompletion) {
			latestCompletion = child.CompletedAt
		}
		if child.DispatchedAt == nil {
			if child.Outcome == OutcomeSuccess || child.FirstTokenAt != nil || !secondsEqual(child.TTFTSeconds, 0) || !secondsEqual(child.LatencySeconds, 0) {
				return fmt.Errorf("children[%d]: pre-dispatch terminal child must be failed or cancelled with zero timing", index)
			}
			continue
		}
		if child.DispatchedAt.IsZero() || child.CompletedAt.Before(*child.DispatchedAt) {
			return fmt.Errorf("children[%d]: valid dispatch and completion timestamps are required", index)
		}
		if child.Outcome == OutcomeSuccess && (child.FirstTokenAt == nil || child.FirstTokenAt.IsZero()) {
			return fmt.Errorf("children[%d]: successful child requires first-token timestamp", index)
		}
		if child.FirstTokenAt != nil {
			if child.FirstTokenAt.IsZero() || child.FirstTokenAt.Before(*child.DispatchedAt) || child.FirstTokenAt.After(*child.CompletedAt) {
				return fmt.Errorf("children[%d]: first-token timestamp is outside execution interval", index)
			}
			observedTTFT := child.FirstTokenAt.Sub(*child.DispatchedAt).Seconds()
			if !secondsEqual(child.TTFTSeconds, observedTTFT) {
				return fmt.Errorf("children[%d].ttft_seconds does not match timestamps", index)
			}
		} else if !secondsEqual(child.TTFTSeconds, 0) {
			return fmt.Errorf("children[%d].ttft_seconds has no first-token timestamp", index)
		}
		observedLatency := child.CompletedAt.Sub(*child.DispatchedAt).Seconds()
		if !secondsEqual(child.LatencySeconds, observedLatency) {
			return fmt.Errorf("children[%d].latency_seconds does not match timestamps", index)
		}
		if earliestDispatch == nil || child.DispatchedAt.Before(*earliestDispatch) {
			earliestDispatch = child.DispatchedAt
		}
		if child.TTFTSeconds > observedSlowestTTFT {
			observedSlowestTTFT = child.TTFTSeconds
		}
	}
	if recomputedMeasured && successfulChildren > 0 {
		if result.RecomputedPrefixTokens == nil || *result.RecomputedPrefixTokens != recomputedPrefixTokens {
			return fmt.Errorf("recomputed_prefix_tokens does not match successful child cache readbacks")
		}
	} else if result.RecomputedPrefixTokens != nil {
		return fmt.Errorf("recomputed_prefix_tokens is present without complete successful child cache readbacks")
	}
	if result.Condition != nil && earliestDispatch != nil && result.Condition.AppliedAt.After(*earliestDispatch) {
		return fmt.Errorf("condition: applied time must precede every child dispatch")
	}
	if result.Condition != nil && result.Condition.FinalizedAt != nil && latestCompletion != nil && result.Condition.FinalizedAt.Before(*latestCompletion) {
		return fmt.Errorf("condition: finalized time must follow every child completion")
	}
	if !secondsEqual(result.SlowestChildTTFTSeconds, observedSlowestTTFT) {
		return fmt.Errorf("slowest_child_ttft_seconds does not match children")
	}
	observedMakespan := 0.0
	if earliestDispatch != nil && latestCompletion.After(*earliestDispatch) {
		observedMakespan = latestCompletion.Sub(*earliestDispatch).Seconds()
	}
	if !secondsEqual(result.MakespanSeconds, observedMakespan) {
		return fmt.Errorf("makespan_seconds does not match timestamps")
	}
	return nil
}

func secondsEqual(recorded, observed float64) bool {
	return math.Abs(recorded-observed) <= timestampMetricToleranceSeconds
}

func ValidateConditionObservedState(state ConditionObservedState, load LoadRegime, cacheState string, prefixSourceCount uint32) error {
	if !finiteNonNegativeValue(state.OfferedLoadQPS) || !finiteNonNegativeValue(state.AchievedLoadQPS) || !finiteNonNegativeValue(state.SaturationQPS) || !finiteNonNegativeValue(state.OrdinaryTrafficMeanLatencySeconds) || state.SaturationQPS == 0 || state.DroppedLoadRequests != 0 || !isLowerHexSHA256(state.LoadProfileSHA256) || !isLowerHexSHA256(state.LoadProfilesSHA256) || !isLowerHexSHA256(state.LoadCalibrationSHA256) || state.MeasurementSource != "load-calibration:"+state.LoadCalibrationSHA256 {
		return fmt.Errorf("observed load rates and measurement source are invalid")
	}
	offeredFraction := state.OfferedLoadQPS / state.SaturationQPS
	achievedFraction := state.AchievedLoadQPS / state.SaturationQPS
	switch load {
	case LoadIdle:
		if offeredFraction > 0.05 || achievedFraction > 0.05 || state.OrdinaryTrafficMeanLatencySeconds != 0 {
			return fmt.Errorf("idle offered or achieved load exceeds 5 percent of saturation")
		}
	case LoadModerate:
		if offeredFraction < 0.40 || offeredFraction > 0.60 || achievedFraction < 0.40 || achievedFraction > 0.60 || state.OrdinaryTrafficMeanLatencySeconds <= 0 {
			return fmt.Errorf("moderate offered or achieved load is outside 40 to 60 percent of saturation")
		}
	case LoadNearSaturation:
		if offeredFraction < 0.85 || offeredFraction > 0.95 || achievedFraction < 0.85 || achievedFraction > 0.95 || state.OrdinaryTrafficMeanLatencySeconds <= 0 {
			return fmt.Errorf("near-saturation offered or achieved load is outside 85 to 95 percent of saturation")
		}
	default:
		return fmt.Errorf("observed load regime is unsupported")
	}
	cached, err := uniqueConditionIDs(state.CachedEndpointIDs)
	if err != nil {
		return fmt.Errorf("cached endpoint IDs: %w", err)
	}
	sources, err := uniqueConditionIDs(state.PrefixSourceEndpointIDs)
	if err != nil {
		return fmt.Errorf("prefix-source endpoint IDs: %w", err)
	}
	drained, err := uniqueConditionIDs(state.DrainedEndpointIDs)
	if err != nil || len(drained) == 0 || state.DrainStableSamples < 2 {
		return fmt.Errorf("drained endpoint IDs and two stable idle samples are required")
	}
	ownerOrder, err := uniqueConditionIDs(state.OwnerEndpointOrder)
	if err != nil || len(ownerOrder) != len(drained) {
		return fmt.Errorf("owner endpoint order must contain every drained endpoint exactly once")
	}
	for id := range ownerOrder {
		if _, exists := drained[id]; !exists {
			return fmt.Errorf("owner endpoint %q was not observed drained", id)
		}
	}
	for id := range cached {
		if _, exists := drained[id]; !exists {
			return fmt.Errorf("cached endpoint %q was not observed drained", id)
		}
	}
	if prefixSourceCount != 0 {
		if prefixSourceCount != 1 && prefixSourceCount != 2 && prefixSourceCount != 4 {
			return fmt.Errorf("prefix source count is not registered")
		}
		if cacheState != "distributed-warm" || len(sources) != int(prefixSourceCount) || len(cached) != len(sources) {
			return fmt.Errorf("Z0-C requires an exact distributed-warm source set")
		}
		for id := range sources {
			if _, exists := cached[id]; !exists {
				return fmt.Errorf("Z0-C cached and prefix-source endpoint sets differ")
			}
		}
		if !conditionIDPrefix(state.CachedEndpointIDs, state.OwnerEndpointOrder) || !conditionIDPrefix(state.PrefixSourceEndpointIDs, state.OwnerEndpointOrder) {
			return fmt.Errorf("Z0-C source identities must be the nested prefix of the attested owner rotation")
		}
		return nil
	}
	if len(sources) != 0 {
		return fmt.Errorf("non-Z0-C state cannot include prefix-source endpoints")
	}
	switch cacheState {
	case "cold":
		if len(cached) != 0 {
			return fmt.Errorf("cold cache state contains cached endpoints")
		}
	case "warm-owner":
		if len(cached) != 1 || !conditionIDPrefix(state.CachedEndpointIDs, state.OwnerEndpointOrder) {
			return fmt.Errorf("warm-owner cache state requires exactly one cached endpoint")
		}
	case "distributed-warm":
		if len(cached) < 2 || len(cached) != len(ownerOrder) || !conditionIDPrefix(state.CachedEndpointIDs, state.OwnerEndpointOrder) {
			return fmt.Errorf("distributed-warm cache state requires at least two cached endpoints")
		}
	default:
		return fmt.Errorf("observed cache state is unsupported")
	}
	return nil
}

func conditionStateSHA256(condition ConditionAttestation) string {
	state := struct {
		SchemaVersion     string                 `json:"schema_version"`
		LoadRegime        LoadRegime             `json:"load_regime"`
		CacheState        string                 `json:"cache_state"`
		PrefixSourceCount uint32                 `json:"prefix_source_count,omitempty"`
		OwnerRotation     uint32                 `json:"owner_rotation"`
		ObservedState     ConditionObservedState `json:"observed_state"`
	}{
		SchemaVersion: "velaserve.applied-condition/v1", LoadRegime: condition.LoadRegime,
		CacheState: condition.CacheState, PrefixSourceCount: condition.PrefixSourceCount, OwnerRotation: condition.OwnerRotation, ObservedState: condition.ObservedState,
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func uniqueConditionIDs(values []string) (map[string]struct{}, error) {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("contains an empty identity")
		}
		if _, exists := result[value]; exists {
			return nil, fmt.Errorf("contains duplicate identity %q", value)
		}
		result[value] = struct{}{}
	}
	return result, nil
}

func conditionIDPrefix(values, order []string) bool {
	if len(values) > len(order) {
		return false
	}
	for index := range values {
		if values[index] != order[index] {
			return false
		}
	}
	return true
}

func finiteNonNegativeValue(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}

func isLowerHexSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func validateBenchmarkCell(cell BenchmarkCell) error {
	if cell.Repetition == 0 {
		return fmt.Errorf("repetition: must be positive")
	}
	if strings.TrimSpace(cell.PrefixRegime) == "" {
		return fmt.Errorf("prefix_regime: required")
	}
	if cell.PrefixTokens == 0 {
		return fmt.Errorf("prefix_tokens: must be positive")
	}
	if strings.TrimSpace(cell.OutputRegime) == "" {
		return fmt.Errorf("output_regime: required")
	}
	if cell.MaxTokens == 0 {
		return fmt.Errorf("max_tokens: must be positive")
	}
	if _, ok := registeredSkews[cell.ArrivalSkewMS]; !ok {
		return fmt.Errorf("arrival_skew_ms: %d is not registered", cell.ArrivalSkewMS)
	}
	if !isLoadRegime(cell.LoadRegime) {
		return fmt.Errorf("load_regime: unsupported value %q", cell.LoadRegime)
	}
	switch cell.CacheState {
	case "warm-owner", "distributed-warm", "cold":
	default:
		return fmt.Errorf("cache_state: unsupported value %q", cell.CacheState)
	}
	if strings.TrimSpace(cell.Transport) == "" {
		return fmt.Errorf("transport: required")
	}
	if cell.EPPReplicas != 1 && cell.EPPReplicas != 2 {
		return fmt.Errorf("epp_replicas: got %d, want 1 or 2", cell.EPPReplicas)
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
	_, exists := snapshotEndpoint(snapshot, target)
	return exists
}

func snapshotEndpoint(snapshot EndpointSnapshot, target EndpointRef) (EndpointState, bool) {
	for _, endpoint := range snapshot.Endpoints {
		if endpoint.Ref == target {
			return endpoint, true
		}
	}
	return EndpointState{}, false
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

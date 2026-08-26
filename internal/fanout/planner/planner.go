package planner

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/JDinSeattle/velaserve/internal/evidence"
	"github.com/JDinSeattle/velaserve/internal/fanout/model"
)

type Calibration struct {
	PrefillTokensPerSecond float64 `json:"prefill_tokens_per_second" yaml:"prefill_tokens_per_second"`
	PullBytesPerSecond     float64 `json:"pull_bytes_per_second" yaml:"pull_bytes_per_second"`
	BytesPerCachedToken    float64 `json:"bytes_per_cached_token" yaml:"bytes_per_cached_token"`
	ServiceSeconds         float64 `json:"service_seconds" yaml:"service_seconds"`
}

type Input struct {
	Width        uint32                    `json:"width"`
	PrefixTokens uint64                    `json:"prefix_tokens"`
	Snapshot     evidence.EndpointSnapshot `json:"snapshot"`
	Calibration  Calibration               `json:"calibration"`
}

type SlotAssignment struct {
	SlotID          uint32                 `json:"slot_id"`
	Target          evidence.EndpointRef   `json:"target"`
	Acquisition     model.AcquisitionClass `json:"acquisition"`
	PrefixReadyAt   float64                `json:"prefix_ready_at_seconds"`
	PredictedFinish float64                `json:"predicted_finish_seconds"`
}

type GroupPlan struct {
	Width              uint32           `json:"width"`
	K                  uint32           `json:"k"`
	MaxPredictedFinish float64          `json:"max_predicted_finish_seconds"`
	Slots              []SlotAssignment `json:"slots"`
}

type endpointRuntime struct {
	state         evidence.EndpointState
	availableAt   float64
	prefixReadyAt float64
	prefixReady   bool
	acquisition   model.AcquisitionClass
}

type candidate struct {
	index         int
	finish        float64
	prefixReadyAt float64
	acquisition   model.AcquisitionClass
}

func PlanGroup(ctx context.Context, in Input) (GroupPlan, error) {
	return Plan(ctx, in)
}

func Plan(ctx context.Context, in Input) (GroupPlan, error) {
	if err := validateInput(in); err != nil {
		return GroupPlan{}, err
	}
	runtimes, err := eligibleRuntimes(in.Snapshot)
	if err != nil {
		return GroupPlan{}, err
	}
	if len(runtimes) == 0 {
		return GroupPlan{}, fmt.Errorf("plan fan-out: no eligible endpoints")
	}

	result := GroupPlan{Width: in.Width, Slots: make([]SlotAssignment, 0, in.Width)}
	distinct := make(map[evidence.EndpointRef]struct{})
	for slotID := uint32(0); slotID < in.Width; slotID++ {
		if err := ctx.Err(); err != nil {
			return GroupPlan{}, fmt.Errorf("plan fan-out slot %d: %w", slotID, err)
		}
		best := candidate{index: -1, finish: math.Inf(1)}
		for index := range runtimes {
			current := evaluate(runtimes[index], in)
			current.index = index
			if better(current, best, runtimes) {
				best = current
			}
		}
		selected := &runtimes[best.index]
		if !selected.prefixReady {
			selected.prefixReady = true
			selected.prefixReadyAt = best.prefixReadyAt
			selected.acquisition = best.acquisition
		}
		selected.availableAt = best.finish
		assignment := SlotAssignment{
			SlotID:          slotID,
			Target:          selected.state.Ref,
			Acquisition:     selected.acquisition,
			PrefixReadyAt:   selected.prefixReadyAt,
			PredictedFinish: best.finish,
		}
		result.Slots = append(result.Slots, assignment)
		distinct[selected.state.Ref] = struct{}{}
		if best.finish > result.MaxPredictedFinish {
			result.MaxPredictedFinish = best.finish
		}
	}
	result.K = uint32(len(distinct))
	return result, nil
}

func validateInput(in Input) error {
	if in.Width == 0 {
		return fmt.Errorf("plan fan-out: width must be positive")
	}
	if in.Width > 4096 {
		return fmt.Errorf("plan fan-out: width %d exceeds safety limit 4096", in.Width)
	}
	values := map[string]float64{
		"prefill_tokens_per_second": in.Calibration.PrefillTokensPerSecond,
		"pull_bytes_per_second":     in.Calibration.PullBytesPerSecond,
		"bytes_per_cached_token":    in.Calibration.BytesPerCachedToken,
		"service_seconds":           in.Calibration.ServiceSeconds,
	}
	for name, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
			return fmt.Errorf("plan fan-out: calibration %s must be finite and positive", name)
		}
	}
	return nil
}

func eligibleRuntimes(snapshot evidence.EndpointSnapshot) ([]endpointRuntime, error) {
	states := append([]evidence.EndpointState(nil), snapshot.Endpoints...)
	sort.Slice(states, func(i, j int) bool { return endpointLess(states[i].Ref, states[j].Ref) })
	result := make([]endpointRuntime, 0, len(states))
	var previous *evidence.EndpointRef
	for _, state := range states {
		if math.IsNaN(state.AvailableAtSeconds) || math.IsInf(state.AvailableAtSeconds, 0) || state.AvailableAtSeconds < 0 {
			return nil, fmt.Errorf("plan fan-out: endpoint %q has invalid availability", state.Ref.ID)
		}
		if previous != nil && *previous == state.Ref {
			return nil, fmt.Errorf("plan fan-out: duplicate endpoint %q", state.Ref.ID)
		}
		refCopy := state.Ref
		previous = &refCopy
		if !state.Healthy || !state.Compatible {
			continue
		}
		if state.Ref.ID == "" || state.Ref.Model == "" {
			return nil, fmt.Errorf("plan fan-out: eligible endpoint requires stable ID and model")
		}
		result = append(result, endpointRuntime{state: state, availableAt: state.AvailableAtSeconds})
	}
	return result, nil
}

func evaluate(runtime endpointRuntime, in Input) candidate {
	prefixReadyAt := runtime.prefixReadyAt
	acquisition := runtime.acquisition
	if !runtime.prefixReady {
		prefixReadyAt, acquisition = acquisitionCost(runtime.state, in.PrefixTokens, in.Calibration)
	}
	start := math.Max(runtime.availableAt, prefixReadyAt)
	return candidate{
		finish:        start + in.Calibration.ServiceSeconds,
		prefixReadyAt: prefixReadyAt,
		acquisition:   acquisition,
	}
}

func acquisitionCost(endpoint evidence.EndpointState, prefixTokens uint64, calibration Calibration) (float64, model.AcquisitionClass) {
	if endpoint.LocalPrefixTokens >= prefixTokens {
		return 0, model.AcquisitionLocalHit
	}
	missingTokens := prefixTokens - endpoint.LocalPrefixTokens
	recomputeSeconds := float64(missingTokens) / calibration.PrefillTokensPerSecond
	bestPullSeconds := math.Inf(1)
	for _, source := range endpoint.P2PSources {
		if source.CachedTokens < prefixTokens {
			continue
		}
		bytes := source.TransferBytes
		if bytes == 0 {
			bytes = uint64(math.Ceil(float64(missingTokens) * calibration.BytesPerCachedToken))
		}
		seconds := float64(bytes) / calibration.PullBytesPerSecond
		if seconds < bestPullSeconds {
			bestPullSeconds = seconds
		}
	}
	if bestPullSeconds < recomputeSeconds {
		return bestPullSeconds, model.AcquisitionP2PPull
	}
	return recomputeSeconds, model.AcquisitionRecompute
}

func better(current, best candidate, runtimes []endpointRuntime) bool {
	if best.index == -1 {
		return true
	}
	if current.finish < best.finish-1e-12 {
		return true
	}
	if math.Abs(current.finish-best.finish) > 1e-12 {
		return false
	}
	return endpointLess(runtimes[current.index].state.Ref, runtimes[best.index].state.Ref)
}

func endpointLess(left, right evidence.EndpointRef) bool {
	if left.ID != right.ID {
		return left.ID < right.ID
	}
	if left.Model != right.Model {
		return left.Model < right.Model
	}
	return left.Zone < right.Zone
}

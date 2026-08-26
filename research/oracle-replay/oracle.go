package replay

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/JDinSeattle/velaserve/internal/evidence"
	"github.com/JDinSeattle/velaserve/internal/fanout/model"
	"github.com/JDinSeattle/velaserve/internal/fanout/planner"
)

type TargetCount struct {
	Target evidence.EndpointRef `json:"target"`
	Count  uint32               `json:"count"`
}

type WidthEvaluation struct {
	K                 uint32                   `json:"k"`
	PredictedMakespan float64                  `json:"predicted_makespan_seconds"`
	TargetCounts      []TargetCount            `json:"target_counts"`
	Slots             []planner.SlotAssignment `json:"slots"`
}

type OracleResult struct {
	Width            uint32            `json:"width"`
	BaselineMakespan float64           `json:"baseline_makespan_seconds"`
	BestK            uint32            `json:"best_k"`
	BestMakespan     float64           `json:"best_makespan_seconds"`
	RegretFraction   float64           `json:"regret_fraction"`
	Evaluations      []WidthEvaluation `json:"evaluations"`
}

func EvaluateAllWidths(in Input) (OracleResult, error) {
	baseline, err := ReplayArmB(in)
	if err != nil {
		return OracleResult{}, err
	}
	return EvaluateAllWidthsAgainst(in, baseline)
}

// EvaluateAllWidthsAgainst compares the N-aware oracle with a caller-provided
// Arm-B target vector that was scored by the same frozen cost model.
func EvaluateAllWidthsAgainst(in Input, baseline ReplayResult) (OracleResult, error) {
	if err := validateReplayInput(in); err != nil {
		return OracleResult{}, err
	}
	if baseline.Arm != evidence.ArmLoadAwareP2P || baseline.Width != in.Width || len(baseline.Slots) != int(in.Width) || invalidNonNegative(baseline.PredictedMakespan) {
		return OracleResult{}, fmt.Errorf("observed Arm-B baseline is incomplete or incompatible")
	}
	eligible := make([]evidence.EndpointState, 0, len(in.Snapshot.Endpoints))
	for _, endpoint := range in.Snapshot.Endpoints {
		if endpoint.Healthy && endpoint.Compatible {
			eligible = append(eligible, endpoint)
		}
	}
	sort.Slice(eligible, func(i, j int) bool { return refLess(eligible[i].Ref, eligible[j].Ref) })
	if len(eligible) > 16 {
		return OracleResult{}, fmt.Errorf("oracle supports at most 16 eligible endpoints, got %d", len(eligible))
	}
	maxK := len(eligible)
	if int(in.Width) < maxK {
		maxK = int(in.Width)
	}
	result := OracleResult{
		Width:            in.Width,
		BaselineMakespan: baseline.PredictedMakespan,
		BestMakespan:     math.Inf(1),
		Evaluations:      make([]WidthEvaluation, 0, maxK),
	}
	for k := 1; k <= maxK; k++ {
		best := WidthEvaluation{K: uint32(k), PredictedMakespan: math.Inf(1)}
		forEachCombination(len(eligible), k, func(indexes []int) {
			selected := make([]evidence.EndpointState, len(indexes))
			for index, endpointIndex := range indexes {
				selected[index] = eligible[endpointIndex]
			}
			forEachComposition(in.Width, k, func(counts []uint32) {
				candidate, candidateErr := evaluateAllocation(in, selected, counts)
				if candidateErr != nil {
					return
				}
				if widthEvaluationLess(candidate, best) {
					best = candidate
				}
			})
		})
		if math.IsInf(best.PredictedMakespan, 1) {
			return OracleResult{}, fmt.Errorf("oracle could not evaluate k=%d", k)
		}
		result.Evaluations = append(result.Evaluations, best)
		if best.PredictedMakespan < result.BestMakespan-1e-12 ||
			(math.Abs(best.PredictedMakespan-result.BestMakespan) <= 1e-12 && (result.BestK == 0 || best.K < result.BestK)) {
			result.BestMakespan = best.PredictedMakespan
			result.BestK = best.K
		}
	}
	if result.BestMakespan > result.BaselineMakespan+1e-9 {
		return OracleResult{}, fmt.Errorf("oracle invariant violated: best makespan %.9f exceeds Arm B %.9f", result.BestMakespan, result.BaselineMakespan)
	}
	if result.BaselineMakespan > 0 {
		result.RegretFraction = (result.BaselineMakespan - result.BestMakespan) / result.BaselineMakespan
	}
	return result, nil
}

func evaluateAllocation(in Input, endpoints []evidence.EndpointState, counts []uint32) (WidthEvaluation, error) {
	result := WidthEvaluation{K: uint32(len(endpoints)), TargetCounts: make([]TargetCount, len(endpoints)), Slots: make([]planner.SlotAssignment, 0, in.Width)}
	remaining := append([]uint32(nil), counts...)
	nextAvailable := make([]float64, len(endpoints))
	prefixReady := make([]float64, len(endpoints))
	acquisitions := make([]model.AcquisitionClass, len(endpoints))
	for index, endpoint := range endpoints {
		prefixPlan, err := planner.Plan(context.Background(), planner.Input{
			Width:        1,
			PrefixTokens: in.PrefixTokens,
			Snapshot: evidence.EndpointSnapshot{
				ObservedAt: in.Snapshot.ObservedAt,
				Endpoints:  []evidence.EndpointState{endpoint},
			},
			Calibration: in.Calibration,
		})
		if err != nil {
			return WidthEvaluation{}, err
		}
		first := prefixPlan.Slots[0]
		nextAvailable[index] = endpoint.AvailableAtSeconds
		prefixReady[index] = first.PrefixReadyAt
		acquisitions[index] = first.Acquisition
		result.TargetCounts[index] = TargetCount{Target: endpoint.Ref, Count: counts[index]}
	}
	for slotID := uint32(0); slotID < in.Width; slotID++ {
		arrival := arrivalAt(in, slotID)
		bestIndex := -1
		bestFinish := math.Inf(1)
		for index, endpoint := range endpoints {
			if remaining[index] == 0 {
				continue
			}
			finish := math.Max(arrival, math.Max(nextAvailable[index], prefixReady[index])) + in.Calibration.ServiceSeconds
			if finish < bestFinish-1e-12 || (math.Abs(finish-bestFinish) <= 1e-12 && (bestIndex < 0 || refLess(endpoint.Ref, endpoints[bestIndex].Ref))) {
				bestIndex = index
				bestFinish = finish
			}
		}
		if bestIndex < 0 {
			return WidthEvaluation{}, fmt.Errorf("allocation has fewer target slots than width %d", in.Width)
		}
		remaining[bestIndex]--
		nextAvailable[bestIndex] = bestFinish
		result.Slots = append(result.Slots, planner.SlotAssignment{
			SlotID: slotID, Target: endpoints[bestIndex].Ref, Acquisition: acquisitions[bestIndex],
			PrefixReadyAt: prefixReady[bestIndex], PredictedFinish: bestFinish,
		})
		if bestFinish > result.PredictedMakespan {
			result.PredictedMakespan = bestFinish
		}
	}
	return result, nil
}

func forEachCombination(n, k int, visit func([]int)) {
	current := make([]int, k)
	var walk func(start, depth int)
	walk = func(start, depth int) {
		if depth == k {
			visit(append([]int(nil), current...))
			return
		}
		for index := start; index <= n-(k-depth); index++ {
			current[depth] = index
			walk(index+1, depth+1)
		}
	}
	walk(0, 0)
}

func forEachComposition(total uint32, parts int, visit func([]uint32)) {
	current := make([]uint32, parts)
	var walk func(remaining uint32, index int)
	walk = func(remaining uint32, index int) {
		if index == parts-1 {
			if remaining > 0 {
				current[index] = remaining
				visit(append([]uint32(nil), current...))
			}
			return
		}
		minimumRemaining := uint32(parts - index - 1)
		for count := uint32(1); count <= remaining-minimumRemaining; count++ {
			current[index] = count
			walk(remaining-count, index+1)
		}
	}
	walk(total, 0)
}

func widthEvaluationLess(left, right WidthEvaluation) bool {
	if left.PredictedMakespan < right.PredictedMakespan-1e-12 {
		return true
	}
	if math.Abs(left.PredictedMakespan-right.PredictedMakespan) > 1e-12 {
		return false
	}
	for index := range left.TargetCounts {
		leftTarget := left.TargetCounts[index]
		rightTarget := right.TargetCounts[index]
		if leftTarget.Target != rightTarget.Target {
			return refLess(leftTarget.Target, rightTarget.Target)
		}
		if leftTarget.Count != rightTarget.Count {
			return leftTarget.Count < rightTarget.Count
		}
	}
	return false
}

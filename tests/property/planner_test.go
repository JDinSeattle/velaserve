package property_test

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/JDinSeattle/velaserve/internal/evidence"
	"github.com/JDinSeattle/velaserve/internal/fanout/planner"
	"pgregory.net/rapid"
)

func TestPlannerDeterminismAndEligibility(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		endpointCount := rapid.IntRange(1, 32).Draw(t, "endpoint-count")
		width := uint32(rapid.IntRange(1, 64).Draw(t, "width"))
		prefixTokens := uint64(rapid.IntRange(0, 32768).Draw(t, "prefix-tokens"))
		states := make([]evidence.EndpointState, 0, endpointCount)
		eligible := make(map[evidence.EndpointRef]struct{})
		for index := 0; index < endpointCount; index++ {
			ref := evidence.EndpointRef{ID: fmt.Sprintf("endpoint-%02d", index), Model: "property-model"}
			healthy := rapid.Bool().Draw(t, fmt.Sprintf("healthy-%d", index))
			compatible := rapid.Bool().Draw(t, fmt.Sprintf("compatible-%d", index))
			if index == endpointCount-1 && len(eligible) == 0 {
				healthy = true
				compatible = true
			}
			state := evidence.EndpointState{
				Ref:                ref,
				Healthy:            healthy,
				Compatible:         compatible,
				AvailableAtSeconds: float64(rapid.IntRange(0, 1000).Draw(t, fmt.Sprintf("available-%d", index))) / 10,
				LocalPrefixTokens:  uint64(rapid.IntRange(0, 32768).Draw(t, fmt.Sprintf("local-prefix-%d", index))),
			}
			if healthy && compatible {
				eligible[ref] = struct{}{}
			}
			states = append(states, state)
		}
		input := planner.Input{
			Width:        width,
			PrefixTokens: prefixTokens,
			Snapshot: evidence.EndpointSnapshot{
				ObservedAt: time.Date(2026, 8, 25, 20, 0, 0, 0, time.UTC),
				Endpoints:  states,
			},
			Calibration: planner.Calibration{
				PrefillTokensPerSecond: 1200,
				PullBytesPerSecond:     10_000_000,
				BytesPerCachedToken:    512,
				ServiceSeconds:         0.75,
			},
		}

		first, err := planner.Plan(context.Background(), input)
		if err != nil {
			t.Fatalf("first Plan() error = %v", err)
		}
		second, err := planner.Plan(context.Background(), input)
		if err != nil {
			t.Fatalf("second Plan() error = %v", err)
		}
		if !reflect.DeepEqual(first, second) {
			t.Fatalf("same input produced different plans\nfirst=%#v\nsecond=%#v", first, second)
		}
		if len(first.Slots) != int(width) {
			t.Fatalf("slot count = %d, want %d", len(first.Slots), width)
		}
		distinct := make(map[evidence.EndpointRef]struct{})
		lastFinish := make(map[evidence.EndpointRef]float64)
		maxFinish := 0.0
		for index, slot := range first.Slots {
			if slot.SlotID != uint32(index) {
				t.Fatalf("slot[%d].SlotID = %d", index, slot.SlotID)
			}
			if _, ok := eligible[slot.Target]; !ok {
				t.Fatalf("slot[%d] selected ineligible target %#v", index, slot.Target)
			}
			if math.IsNaN(slot.PredictedFinish) || math.IsInf(slot.PredictedFinish, 0) {
				t.Fatalf("slot[%d] has non-finite finish %v", index, slot.PredictedFinish)
			}
			if previous, ok := lastFinish[slot.Target]; ok && slot.PredictedFinish <= previous {
				t.Fatalf("target %s finish did not increase: %v then %v", slot.Target.ID, previous, slot.PredictedFinish)
			}
			lastFinish[slot.Target] = slot.PredictedFinish
			distinct[slot.Target] = struct{}{}
			if slot.PredictedFinish > maxFinish {
				maxFinish = slot.PredictedFinish
			}
		}
		if first.K != uint32(len(distinct)) {
			t.Fatalf("K = %d, distinct targets = %d", first.K, len(distinct))
		}
		if first.MaxPredictedFinish != maxFinish {
			t.Fatalf("max finish = %v, want %v", first.MaxPredictedFinish, maxFinish)
		}
	})
}

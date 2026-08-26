package analysis

import (
	"fmt"
	"testing"
	"time"

	"github.com/JDinSeattle/velaserve/internal/evidence"
	placementrecorder "github.com/JDinSeattle/velaserve/research/placement-recorder"
)

func TestSourcePressureGateCellsRequiresAllRegisteredSourceCounts(t *testing.T) {
	runs := []SourcePressureRun{sourceRun(1, 1.0), sourceRun(2, 0.8)}
	if _, err := SourcePressureGateCells(runs, 7, 100, 0.95); err == nil {
		t.Fatal("SourcePressureGateCells() accepted a matrix without four-source evidence")
	}
}

func TestSourcePressureGateCellsPairsRawGroupsAcrossSourceConditions(t *testing.T) {
	cells, err := SourcePressureGateCells([]SourcePressureRun{sourceRun(1, 1.0), sourceRun(2, 0.8), sourceRun(4, 0.7)}, 7, 200, 0.95)
	if err != nil {
		t.Fatal(err)
	}
	if len(cells) != 2 {
		t.Fatalf("cells = %d, want 2", len(cells))
	}
	if cells[0].Pairs != 20 || cells[0].BaselineSourceCount != 1 || cells[0].CandidateSourceCount != 2 {
		t.Fatalf("first cell = %#v", cells[0])
	}
}

func TestSourcePressureGateCellsRetainsGroupsWithoutRealizedP2P(t *testing.T) {
	runs := []SourcePressureRun{sourceRun(1, 1.0), sourceRun(2, 0.8), sourceRun(4, 0.7)}
	for runIndex := range runs {
		for observationIndex := range runs[runIndex].Observations {
			observation := &runs[runIndex].Observations[observationIndex]
			observation.Acquisition = placementrecorder.AcquisitionLocal
			observation.ChosenSource = nil
			observation.TransferBytes = nil
			observation.TransferStartedAt = nil
			observation.TransferCompletedAt = nil
			observation.PeakConcurrentPulls = nil
			observation.TransferBytesPerSecond = nil
		}
	}
	if _, err := SourcePressureGateCells(runs, 7, 100, 0.95); err != nil {
		t.Fatalf("SourcePressureGateCells() rejected legitimate no-P2P groups: %v", err)
	}
}

func sourceRun(sourceCount uint32, ttft float64) SourcePressureRun {
	run := SourcePressureRun{CandidateSourceCount: sourceCount}
	for repetition := uint32(1); repetition <= 20; repetition++ {
		groupID := fmt.Sprintf("group-%02d", repetition)
		requestID := fmt.Sprintf("request-%02d-a", repetition)
		requestID2 := fmt.Sprintf("request-%02d-b", repetition)
		start := time.Date(2026, 8, 26, 0, 0, int(repetition), 0, time.UTC)
		firstToken := start.Add(time.Duration(ttft * float64(time.Second)))
		completed := start.Add(time.Duration((ttft + 0.1) * float64(time.Second)))
		group := evidence.GroupResult{
			SchemaVersion: evidence.SchemaVersion, RunID: fmt.Sprintf("run-%d", sourceCount), Arm: evidence.ArmLoadAwareP2P,
			GroupID: groupID, FanoutWidth: 2, MakespanSeconds: ttft + 0.1, SlowestChildTTFTSeconds: ttft,
			Cell:    evidence.BenchmarkCell{Repetition: repetition, PrefixRegime: "near", PrefixTokens: 4096, OutputRegime: "short", MaxTokens: 32, LoadRegime: evidence.LoadModerate, CacheState: "warm-owner", Transport: "tcp", EPPReplicas: 1},
			Outcome: evidence.OutcomeSuccess, Children: []evidence.ChildResult{
				{RequestID: requestID, TTFTSeconds: ttft, LatencySeconds: ttft + 0.1, OutputTokens: 1, Outcome: evidence.OutcomeSuccess, DispatchedAt: &start, FirstTokenAt: &firstToken, CompletedAt: &completed},
				{RequestID: requestID2, TTFTSeconds: ttft, LatencySeconds: ttft + 0.1, OutputTokens: 1, Outcome: evidence.OutcomeSuccess, DispatchedAt: &start, FirstTokenAt: &firstToken, CompletedAt: &completed},
			},
		}
		run.Groups = append(run.Groups, group)
		end := start.Add(time.Millisecond)
		bytes := uint64(1024)
		peakPulls := uint32(1)
		throughput := float64(bytes) / end.Sub(start).Seconds()
		run.Observations = append(run.Observations, placementrecorder.SourcePressureObservation{
			SchemaVersion: placementrecorder.SourcePressureSchemaVersion, RunID: group.RunID, GroupID: groupID, RequestID: requestID,
			FanoutWidth: 2, Acquisition: placementrecorder.AcquisitionP2P,
			ChosenSource: &evidence.EndpointRef{ID: "source", Model: "model"}, CandidateSourceCount: sourceCount,
			TransferBytes: &bytes, TransferStartedAt: &start, TransferCompletedAt: &end,
			PeakConcurrentPulls: &peakPulls, TransferBytesPerSecond: &throughput,
			LastSiblingTTFTSeconds: ttft, ObservedAt: end,
		})
		run.Observations = append(run.Observations, placementrecorder.SourcePressureObservation{
			SchemaVersion: placementrecorder.SourcePressureSchemaVersion, RunID: group.RunID, GroupID: groupID, RequestID: requestID2,
			FanoutWidth: 2, Acquisition: placementrecorder.AcquisitionLocal, CandidateSourceCount: sourceCount,
			LastSiblingTTFTSeconds: ttft, ObservedAt: end,
		})
	}
	return run
}

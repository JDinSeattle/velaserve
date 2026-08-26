package analysis

import (
	"fmt"
	"testing"
	"time"

	"github.com/JDinSeattle/velaserve/internal/evidence"
	replay "github.com/JDinSeattle/velaserve/research/oracle-replay"
)

func TestPlacementGateCellsJoinsArmBAndComputesP95(t *testing.T) {
	groups := make([]evidence.GroupResult, 0, 40)
	oracles := make([]replay.OracleRecord, 0, 40)
	for _, width := range []uint32{2, 4} {
		for repetition := 1; repetition <= 20; repetition++ {
			groupID := fmt.Sprintf("group-%d-%02d", width, repetition)
			groups = append(groups, placementGroup(groupID, width, uint32(repetition)))
			oracles = append(oracles, replay.OracleRecord{
				RunID:       "run",
				GroupID:     groupID,
				FanoutWidth: width,
				ArmB:        replay.ReplayResult{Width: width, PredictedMakespan: 10},
				Oracle:      replay.OracleResult{Width: width, BestMakespan: 8},
			})
		}
	}
	cells, err := PlacementGateCells(groups, oracles, 20260825, 1000, 0.95)
	if err != nil {
		t.Fatal(err)
	}
	if len(cells) != 2 {
		t.Fatalf("cells = %d, want 2", len(cells))
	}
	for _, cell := range cells {
		if cell.Pairs != 20 || cell.Improvement.Lower < 0.19 || cell.Improvement.Upper > 0.21 {
			t.Fatalf("cell = %#v", cell)
		}
	}
}

func TestPlacementGateCellsOmitsUndersampledCell(t *testing.T) {
	group := placementGroup("group-one", 2, 1)
	oracle := replay.OracleRecord{RunID: "run", GroupID: group.GroupID, FanoutWidth: 2, ArmB: replay.ReplayResult{Width: 2, PredictedMakespan: 1}, Oracle: replay.OracleResult{Width: 2, BestMakespan: 0.5}}
	cells, err := PlacementGateCells([]evidence.GroupResult{group}, []replay.OracleRecord{oracle}, 1, 100, 0.95)
	if err != nil {
		t.Fatal(err)
	}
	if len(cells) != 0 {
		t.Fatalf("undersampled cells = %#v", cells)
	}
}

func TestPlacementGateCellsAllowsNonArmBGroupsWithoutOracleRecords(t *testing.T) {
	armB := placementGroup("arm-b", 2, 1)
	armA := placementGroup("arm-a", 2, 1)
	armA.Arm = evidence.ArmAffinityP2P
	oracle := replay.OracleRecord{RunID: "run", GroupID: armB.GroupID, FanoutWidth: 2, ArmB: replay.ReplayResult{Width: 2, PredictedMakespan: 1}, Oracle: replay.OracleResult{Width: 2, BestMakespan: 0.5}}
	if _, err := PlacementGateCells([]evidence.GroupResult{armA, armB}, []replay.OracleRecord{oracle}, 1, 100, 0.95); err != nil {
		t.Fatalf("PlacementGateCells() rejected unrelated Arm-A group: %v", err)
	}
}

func placementGroup(groupID string, width, repetition uint32) evidence.GroupResult {
	children := make([]evidence.ChildResult, width)
	dispatched := time.Date(2026, 8, 26, 0, 0, int(repetition), 0, time.UTC)
	firstToken := dispatched.Add(100 * time.Millisecond)
	completed := dispatched.Add(time.Second)
	for index := range children {
		children[index] = evidence.ChildResult{RequestID: fmt.Sprintf("%s-%d", groupID, index), TTFTSeconds: 0.1, LatencySeconds: 1, Outcome: evidence.OutcomeSuccess, DispatchedAt: &dispatched, FirstTokenAt: &firstToken, CompletedAt: &completed}
	}
	return evidence.GroupResult{
		SchemaVersion:           evidence.SchemaVersion,
		RunID:                   "run",
		Arm:                     evidence.ArmLoadAwareP2P,
		GroupID:                 groupID,
		FanoutWidth:             width,
		MakespanSeconds:         1,
		SlowestChildTTFTSeconds: 0.1,
		Cell: evidence.BenchmarkCell{
			Repetition: repetition, PrefixRegime: "long", PrefixTokens: 4096,
			OutputRegime: "short", MaxTokens: 8, ArrivalSkewMS: 0,
			LoadRegime: evidence.LoadModerate, CacheState: "warm-owner", Transport: "tcp", EPPReplicas: 1,
		},
		Outcome:  evidence.OutcomeSuccess,
		Children: children,
	}
}

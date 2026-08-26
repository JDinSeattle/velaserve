package placementrecorder

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JDinSeattle/velaserve/internal/evidence"
	"github.com/JDinSeattle/velaserve/internal/jsonl"
)

func TestCompileSourcePressureJoinsTransferTelemetryToAttestedGroups(t *testing.T) {
	root := t.TempDir()
	groupsPath := filepath.Join(root, "groups.jsonl")
	transfersPath := filepath.Join(root, "p2p-transfers.jsonl")
	outputPath := filepath.Join(root, "source-pressure.jsonl")
	group := sourceCompilerGroup(t, 2)
	if err := jsonl.Append(groupsPath, group); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 26, 8, 0, 0, 0, time.UTC)
	end := start.Add(20 * time.Millisecond)
	bytesTransferred := uint64(4096)
	events := []P2PTransferEvent{
		{SchemaVersion: P2PTransferSchemaVersion, RunID: group.RunID, GroupID: group.GroupID, RequestID: group.Children[0].RequestID, Acquisition: AcquisitionP2P, ChosenSource: &evidence.EndpointRef{ID: "model-0", Model: "model"}, TransferBytes: &bytesTransferred, TransferStartedAt: &start, TransferCompletedAt: &end, ObservedAt: end},
		{SchemaVersion: P2PTransferSchemaVersion, RunID: group.RunID, GroupID: group.GroupID, RequestID: group.Children[1].RequestID, Acquisition: AcquisitionLocal, ObservedAt: end},
	}
	for _, event := range events {
		if err := jsonl.Append(transfersPath, event); err != nil {
			t.Fatal(err)
		}
	}
	count, err := CompileSourcePressure(groupsPath, transfersPath, outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("compiled count = %d, want 2", count)
	}
	records, err := jsonl.Read[SourcePressureObservation](outputPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.CandidateSourceCount != 2 || record.LastSiblingTTFTSeconds != group.SlowestChildTTFTSeconds {
			t.Fatalf("compiled record = %#v", record)
		}
	}
}

func TestCompileSourcePressureRejectsMissingChildTelemetryWithoutOutput(t *testing.T) {
	root := t.TempDir()
	groupsPath := filepath.Join(root, "groups.jsonl")
	transfersPath := filepath.Join(root, "p2p-transfers.jsonl")
	outputPath := filepath.Join(root, "source-pressure.jsonl")
	if err := jsonl.Append(groupsPath, sourceCompilerGroup(t, 1)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transfersPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CompileSourcePressure(groupsPath, transfersPath, outputPath); err == nil {
		t.Fatal("CompileSourcePressure() accepted missing child telemetry")
	}
	if _, err := os.Stat(outputPath); !os.IsNotExist(err) {
		t.Fatalf("failed compilation retained output: %v", err)
	}
}

func sourceCompilerGroup(t *testing.T, sourceCount uint32) evidence.GroupResult {
	t.Helper()
	observed := evidence.ConditionObservedState{OfferedLoadQPS: 50, AchievedLoadQPS: 50, SaturationQPS: 100, CachedEndpointIDs: make([]string, sourceCount), PrefixSourceEndpointIDs: make([]string, sourceCount), MeasurementSource: "driver"}
	for index := uint32(0); index < sourceCount; index++ {
		observed.CachedEndpointIDs[index] = fmt.Sprintf("model-%d", index)
		observed.PrefixSourceEndpointIDs[index] = fmt.Sprintf("model-%d", index)
	}
	condition := evidence.ConditionAttestation{
		SchemaVersion: evidence.ConditionAttestationSchemaVersion, RunID: "run", GroupID: "group", LoadRegime: evidence.LoadModerate,
		CacheState: "distributed-warm", PrefixSourceCount: sourceCount, ControllerRevision: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ObservedState: observed, AppliedAt: time.Now().UTC(),
	}
	state := struct {
		SchemaVersion     string                          `json:"schema_version"`
		LoadRegime        evidence.LoadRegime             `json:"load_regime"`
		CacheState        string                          `json:"cache_state"`
		PrefixSourceCount uint32                          `json:"prefix_source_count,omitempty"`
		ObservedState     evidence.ConditionObservedState `json:"observed_state"`
	}{"velaserve.applied-condition/v1", condition.LoadRegime, condition.CacheState, condition.PrefixSourceCount, condition.ObservedState}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	condition.StateSHA256 = hex.EncodeToString(digest[:])
	return evidence.GroupResult{
		SchemaVersion: evidence.SchemaVersion, RunID: "run", GroupID: "group", Arm: evidence.ArmLoadAwareP2P, FanoutWidth: 2,
		MakespanSeconds: 1, SlowestChildTTFTSeconds: .5, Cell: evidence.BenchmarkCell{Repetition: 1, PrefixRegime: "near", PrefixTokens: 4096, OutputRegime: "short", MaxTokens: 32, ArrivalSkewMS: 0, LoadRegime: evidence.LoadModerate, CacheState: "distributed-warm", Transport: "tcp", EPPReplicas: 1},
		Outcome: evidence.OutcomeSuccess, Condition: &condition, Children: []evidence.ChildResult{
			{RequestID: "request-1", TTFTSeconds: .4, LatencySeconds: .8, Outcome: evidence.OutcomeSuccess},
			{RequestID: "request-2", TTFTSeconds: .5, LatencySeconds: 1, Outcome: evidence.OutcomeSuccess},
		},
	}
}

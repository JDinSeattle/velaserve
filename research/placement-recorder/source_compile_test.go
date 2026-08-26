package placementrecorder

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JDinSeattle/velaserve/internal/evidence"
	"github.com/JDinSeattle/velaserve/internal/jsonl"
)

func TestCompileSourcePressureJoinsTransferTelemetryToAttestedGroups(t *testing.T) {
	root := t.TempDir()
	groupsPath := filepath.Join(root, "groups.jsonl")
	placementsPath := filepath.Join(root, "placements.jsonl")
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
	writeSourceCompilerPlacements(t, placementsPath, group, events)
	count, err := CompileSourcePressure(groupsPath, placementsPath, transfersPath, outputPath)
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
		if record.Acquisition == AcquisitionP2P {
			if record.PeakConcurrentPulls == nil || *record.PeakConcurrentPulls != 1 || record.TransferBytesPerSecond == nil || *record.TransferBytesPerSecond != 204800 {
				t.Fatalf("compiled P2P measurements = %#v", record)
			}
		}
	}
}

func TestCompileSourcePressureDerivesPeakConcurrentPullsPerSource(t *testing.T) {
	root := t.TempDir()
	group := sourceCompilerGroup(t, 1)
	start := *group.Children[0].DispatchedAt
	end := start.Add(20 * time.Millisecond)
	secondStart := *group.Children[1].DispatchedAt
	secondEnd := secondStart.Add(20 * time.Millisecond)
	bytesTransferred := uint64(4096)
	events := []P2PTransferEvent{
		{SchemaVersion: P2PTransferSchemaVersion, RunID: group.RunID, GroupID: group.GroupID, RequestID: group.Children[0].RequestID, Acquisition: AcquisitionP2P, ChosenSource: &evidence.EndpointRef{ID: "model-0", Model: "model"}, TransferBytes: &bytesTransferred, TransferStartedAt: &start, TransferCompletedAt: &end, ObservedAt: end},
		{SchemaVersion: P2PTransferSchemaVersion, RunID: group.RunID, GroupID: group.GroupID, RequestID: group.Children[1].RequestID, Acquisition: AcquisitionP2P, ChosenSource: &evidence.EndpointRef{ID: "model-0", Model: "model"}, TransferBytes: &bytesTransferred, TransferStartedAt: &secondStart, TransferCompletedAt: &secondEnd, ObservedAt: secondEnd},
	}
	if err := jsonl.Append(filepath.Join(root, "groups.jsonl"), group); err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if err := jsonl.Append(filepath.Join(root, "transfers.jsonl"), event); err != nil {
			t.Fatal(err)
		}
	}
	writeSourceCompilerPlacements(t, filepath.Join(root, "placements.jsonl"), group, events)
	if _, err := CompileSourcePressure(filepath.Join(root, "groups.jsonl"), filepath.Join(root, "placements.jsonl"), filepath.Join(root, "transfers.jsonl"), filepath.Join(root, "source-pressure.jsonl")); err != nil {
		t.Fatal(err)
	}
	records, err := jsonl.Read[SourcePressureObservation](filepath.Join(root, "source-pressure.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.PeakConcurrentPulls == nil || *record.PeakConcurrentPulls != 2 {
			t.Fatalf("peak concurrent pulls = %#v, want 2", record.PeakConcurrentPulls)
		}
	}
}

func TestNormalizeVLLMRuntimeBuildsCompleteMeasuredAcquisitionFile(t *testing.T) {
	root := t.TempDir()
	group := sourceCompilerGroup(t, 1)
	cached := uint64(4096)
	zero := uint64(0)
	group.Children[0].PromptTokens, group.Children[0].CachedTokens = 4097, &cached
	group.Children[1].PromptTokens, group.Children[1].CachedTokens = 4097, &zero
	group.RecomputedPrefixTokens = pointerUint64(4096)
	if err := jsonl.Append(filepath.Join(root, "groups.jsonl"), group); err != nil {
		t.Fatal(err)
	}
	transferStart := group.Children[0].DispatchedAt.Add(10 * time.Millisecond)
	transferEnd := transferStart.Add(20 * time.Millisecond)
	placements := []P2PTransferEvent{
		{RequestID: group.Children[0].RequestID, Acquisition: AcquisitionP2P, ChosenSource: &evidence.EndpointRef{ID: "model-0", Model: "model"}, TransferBytes: pointerUint64(4096)},
	}
	emitter := RuntimeEmitterBinding{PodName: "target", PodUID: "target-uid"}
	writeSourceCompilerPlacements(t, filepath.Join(root, "placements.jsonl"), group, placements)
	for _, record := range []VLLMRuntimeRecord{
		// The raw observer export intentionally covers the whole run window, so
		// condition probes and background load coexist with benchmark records.
		// They must remain valid raw evidence without entering the derived file.
		{SchemaVersion: VLLMAcquisitionSchemaVersion, EngineRequestID: "chatcmpl-condition-probe", EmitterPodName: emitter.PodName, EmitterPodUID: emitter.PodUID, LocalCachedTokens: pointerUint64(0), ExternalCachedTokens: pointerUint64(0), ObservedAt: group.Children[0].DispatchedAt.Add(-time.Second)},
		{SchemaVersion: VLLMP2PTransferSchemaVersion, EngineRequestID: "chatcmpl-background-load", EmitterPodName: emitter.PodName, EmitterPodUID: emitter.PodUID, Source: &evidence.EndpointRef{ID: "model-0", Model: "model"}, SourceHost: "10.0.0.1", SourcePort: 7777, TransferBytes: pointerUint64(1024), TransferSubmittedAt: pointerTime(group.Children[0].DispatchedAt.Add(-2 * time.Second)), TransferObservedCompletedAt: pointerTime(group.Children[0].DispatchedAt.Add(-time.Second)), ReportedTransferDurationSeconds: pointerFloat64(1), ObservedAt: group.Children[0].DispatchedAt.Add(-time.Second)},
		{SchemaVersion: VLLMAcquisitionSchemaVersion, EngineRequestID: "chatcmpl-" + group.Children[0].RequestID, EmitterPodName: emitter.PodName, EmitterPodUID: emitter.PodUID, LocalCachedTokens: pointerUint64(0), ExternalCachedTokens: pointerUint64(4096), Source: &evidence.EndpointRef{ID: "model-0", Model: "model"}, SourceHost: "10.0.0.1", SourcePort: 7777, ObservedAt: group.Children[0].DispatchedAt.Add(5 * time.Millisecond)},
		{SchemaVersion: VLLMP2PTransferSchemaVersion, EngineRequestID: "chatcmpl-" + group.Children[0].RequestID, EmitterPodName: emitter.PodName, EmitterPodUID: emitter.PodUID, Source: &evidence.EndpointRef{ID: "model-0", Model: "model"}, SourceHost: "10.0.0.1", SourcePort: 7777, TransferBytes: pointerUint64(4096), TransferSubmittedAt: &transferStart, TransferObservedCompletedAt: &transferEnd, ReportedTransferDurationSeconds: pointerFloat64(transferEnd.Sub(transferStart).Seconds()), ObservedAt: transferEnd},
		{SchemaVersion: VLLMAcquisitionSchemaVersion, EngineRequestID: "chatcmpl-" + group.Children[1].RequestID, EmitterPodName: emitter.PodName, EmitterPodUID: emitter.PodUID, LocalCachedTokens: pointerUint64(0), ExternalCachedTokens: pointerUint64(0), ObservedAt: group.Children[1].DispatchedAt.Add(5 * time.Millisecond)},
	} {
		if err := jsonl.Append(filepath.Join(root, "runtime.jsonl"), record); err != nil {
			t.Fatal(err)
		}
	}
	count, err := NormalizeVLLMRuntime(
		filepath.Join(root, "groups.jsonl"), filepath.Join(root, "placements.jsonl"),
		filepath.Join(root, "runtime.jsonl"), filepath.Join(root, "transfers.jsonl"), []RuntimeEmitterBinding{emitter},
	)
	if err != nil {
		t.Fatal(err)
	}
	records, err := jsonl.Read[P2PTransferEvent](filepath.Join(root, "transfers.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 || len(records) != 2 || records[0].Acquisition != AcquisitionP2P || records[1].Acquisition != AcquisitionRecompute {
		t.Fatalf("normalized records = %#v", records)
	}
	for _, record := range records {
		if record.RequestID == "condition-probe" || record.RequestID == "background-load" {
			t.Fatalf("non-benchmark runtime record entered derived evidence: %#v", record)
		}
	}
	runtimePath := filepath.Join(root, "runtime.jsonl")
	rawRecords, err := jsonl.Read[VLLMRuntimeRecord](runtimePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(runtimePath); err != nil {
		t.Fatal(err)
	}
	for index := range rawRecords {
		if rawRecords[index].EngineRequestID == "chatcmpl-"+group.Children[0].RequestID && rawRecords[index].SchemaVersion == VLLMP2PTransferSchemaVersion {
			rawRecords[index].SourceHost = "10.0.0.99"
		}
		if err := jsonl.Append(runtimePath, rawRecords[index]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := NormalizeVLLMRuntime(filepath.Join(root, "groups.jsonl"), filepath.Join(root, "placements.jsonl"), runtimePath, filepath.Join(root, "spoofed-job.jsonl"), []RuntimeEmitterBinding{emitter}); err == nil || !strings.Contains(err.Error(), "inconsistent source") {
		t.Fatalf("NormalizeVLLMRuntime() error = %v, want transfer endpoint mismatch", err)
	}
	if err := os.Remove(runtimePath); err != nil {
		t.Fatal(err)
	}
	for index := range rawRecords {
		if rawRecords[index].EngineRequestID == "chatcmpl-"+group.Children[0].RequestID {
			rawRecords[index].SourceHost = "10.0.0.99"
		}
		if err := jsonl.Append(runtimePath, rawRecords[index]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := NormalizeVLLMRuntime(filepath.Join(root, "groups.jsonl"), filepath.Join(root, "placements.jsonl"), runtimePath, filepath.Join(root, "spoofed-acquisition.jsonl"), []RuntimeEmitterBinding{emitter}); err == nil || !strings.Contains(err.Error(), "disagrees with EPP") {
		t.Fatalf("NormalizeVLLMRuntime() error = %v, want EPP source endpoint mismatch", err)
	}
	if err := os.Remove(runtimePath); err != nil {
		t.Fatal(err)
	}
	for index := range rawRecords {
		if rawRecords[index].EngineRequestID == "chatcmpl-"+group.Children[0].RequestID {
			rawRecords[index].SourceHost = "10.0.0.1"
		}
		if rawRecords[index].EngineRequestID == "chatcmpl-"+group.Children[1].RequestID {
			rawRecords[index].Source = &evidence.EndpointRef{ID: "model-0", Model: "model"}
			rawRecords[index].SourceHost = "10.0.0.1"
			rawRecords[index].SourcePort = 7777
			rawRecords[index].ExternalCachedTokens = pointerUint64(4096)
		}
		if err := jsonl.Append(runtimePath, rawRecords[index]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := NormalizeVLLMRuntime(filepath.Join(root, "groups.jsonl"), filepath.Join(root, "placements.jsonl"), runtimePath, filepath.Join(root, "unselected-source.jsonl"), []RuntimeEmitterBinding{emitter}); err == nil || !strings.Contains(err.Error(), "EPP did not select") {
		t.Fatalf("NormalizeVLLMRuntime() error = %v, want unselected source rejection", err)
	}
	if err := os.Remove(runtimePath); err != nil {
		t.Fatal(err)
	}
	for index := range rawRecords {
		if rawRecords[index].EngineRequestID == "chatcmpl-"+group.Children[0].RequestID {
			rawRecords[index].EmitterPodName = "different-target"
			rawRecords[index].EmitterPodUID = "different-uid"
		}
		if err := jsonl.Append(runtimePath, rawRecords[index]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := NormalizeVLLMRuntime(filepath.Join(root, "groups.jsonl"), filepath.Join(root, "placements.jsonl"), runtimePath, filepath.Join(root, "wrong-emitter.jsonl"), []RuntimeEmitterBinding{emitter, {PodName: "different-target", PodUID: "different-uid"}}); err == nil || !strings.Contains(err.Error(), "emitter does not match") {
		t.Fatalf("NormalizeVLLMRuntime() error = %v, want target emitter mismatch", err)
	}
}

func TestValidateVLLMRuntimeRejectsPartialSourceEndpointTuple(t *testing.T) {
	record := VLLMRuntimeRecord{SchemaVersion: VLLMAcquisitionSchemaVersion, EngineRequestID: "chatcmpl-request", EmitterPodName: "target", EmitterPodUID: "target-uid", LocalCachedTokens: pointerUint64(0), ExternalCachedTokens: pointerUint64(0), SourceHost: "10.0.0.1", ObservedAt: time.Now().UTC()}
	if err := validateVLLMRuntime(record); err == nil || !strings.Contains(err.Error(), "all-or-none") {
		t.Fatalf("validateVLLMRuntime() error = %v, want partial endpoint tuple rejection", err)
	}
}

func TestCompileSourcePressureRejectsMissingChildTelemetryWithoutOutput(t *testing.T) {
	root := t.TempDir()
	groupsPath := filepath.Join(root, "groups.jsonl")
	placementsPath := filepath.Join(root, "placements.jsonl")
	transfersPath := filepath.Join(root, "p2p-transfers.jsonl")
	outputPath := filepath.Join(root, "source-pressure.jsonl")
	if err := jsonl.Append(groupsPath, sourceCompilerGroup(t, 1)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transfersPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	writeSourceCompilerPlacements(t, placementsPath, sourceCompilerGroup(t, 1), nil)
	if _, err := CompileSourcePressure(groupsPath, placementsPath, transfersPath, outputPath); err == nil {
		t.Fatal("CompileSourcePressure() accepted missing child telemetry")
	}
	if _, err := os.Stat(outputPath); !os.IsNotExist(err) {
		t.Fatalf("failed compilation retained output: %v", err)
	}
}

func TestCompileSourcePressureRejectsSourceOutsideAttestedSet(t *testing.T) {
	root := t.TempDir()
	groupsPath := filepath.Join(root, "groups.jsonl")
	placementsPath := filepath.Join(root, "placements.jsonl")
	transfersPath := filepath.Join(root, "transfers.jsonl")
	outputPath := filepath.Join(root, "source-pressure.jsonl")
	group := sourceCompilerGroup(t, 1)
	if err := jsonl.Append(groupsPath, group); err != nil {
		t.Fatal(err)
	}
	event := sourceCompilerTransfer(group, group.Children[0], AcquisitionP2P)
	event.ChosenSource.ID = "unattested-model"
	if err := jsonl.Append(transfersPath, event); err != nil {
		t.Fatal(err)
	}
	for _, child := range group.Children[1:] {
		if err := jsonl.Append(transfersPath, sourceCompilerTransfer(group, child, AcquisitionLocal)); err != nil {
			t.Fatal(err)
		}
	}
	writeSourceCompilerPlacements(t, placementsPath, group, nil)
	if _, err := CompileSourcePressure(groupsPath, placementsPath, transfersPath, outputPath); err == nil || !strings.Contains(err.Error(), "outside the condition-attested source set") {
		t.Fatalf("CompileSourcePressure() error = %v", err)
	}
}

func TestCompileSourcePressureRejectsTelemetryOutsideChildExecution(t *testing.T) {
	root := t.TempDir()
	groupsPath := filepath.Join(root, "groups.jsonl")
	placementsPath := filepath.Join(root, "placements.jsonl")
	transfersPath := filepath.Join(root, "transfers.jsonl")
	outputPath := filepath.Join(root, "source-pressure.jsonl")
	group := sourceCompilerGroup(t, 1)
	if err := jsonl.Append(groupsPath, group); err != nil {
		t.Fatal(err)
	}
	for index, child := range group.Children {
		event := sourceCompilerTransfer(group, child, AcquisitionLocal)
		if index == 0 {
			event.ObservedAt = child.CompletedAt.Add(time.Second)
		}
		if err := jsonl.Append(transfersPath, event); err != nil {
			t.Fatal(err)
		}
	}
	writeSourceCompilerPlacements(t, placementsPath, group, nil)
	if _, err := CompileSourcePressure(groupsPath, placementsPath, transfersPath, outputPath); err == nil || !strings.Contains(err.Error(), "outside its execution interval") {
		t.Fatalf("CompileSourcePressure() error = %v", err)
	}
}

func TestCompileSourcePressureRejectsTransferAfterFirstToken(t *testing.T) {
	root := t.TempDir()
	group := sourceCompilerGroup(t, 1)
	event := sourceCompilerTransfer(group, group.Children[0], AcquisitionP2P)
	event.TransferCompletedAt = pointerTime(group.Children[0].FirstTokenAt.Add(time.Millisecond))
	event.ObservedAt = *event.TransferCompletedAt
	events := []P2PTransferEvent{event, sourceCompilerTransfer(group, group.Children[1], AcquisitionLocal)}
	for _, value := range events {
		if err := jsonl.Append(filepath.Join(root, "transfers.jsonl"), value); err != nil {
			t.Fatal(err)
		}
	}
	if err := jsonl.Append(filepath.Join(root, "groups.jsonl"), group); err != nil {
		t.Fatal(err)
	}
	writeSourceCompilerPlacements(t, filepath.Join(root, "placements.jsonl"), group, events)
	if _, err := CompileSourcePressure(filepath.Join(root, "groups.jsonl"), filepath.Join(root, "placements.jsonl"), filepath.Join(root, "transfers.jsonl"), filepath.Join(root, "out.jsonl")); err == nil || !strings.Contains(err.Error(), "after first content") {
		t.Fatalf("CompileSourcePressure() error = %v", err)
	}
}

func TestCompileSourcePressureRejectsObservationBeforeTransferCompletion(t *testing.T) {
	root := t.TempDir()
	group := sourceCompilerGroup(t, 1)
	event := sourceCompilerTransfer(group, group.Children[0], AcquisitionP2P)
	event.ObservedAt = event.TransferCompletedAt.Add(-time.Millisecond)
	events := []P2PTransferEvent{event, sourceCompilerTransfer(group, group.Children[1], AcquisitionLocal)}
	for _, value := range events {
		if err := jsonl.Append(filepath.Join(root, "transfers.jsonl"), value); err != nil {
			t.Fatal(err)
		}
	}
	if err := jsonl.Append(filepath.Join(root, "groups.jsonl"), group); err != nil {
		t.Fatal(err)
	}
	writeSourceCompilerPlacements(t, filepath.Join(root, "placements.jsonl"), group, events)
	if _, err := CompileSourcePressure(filepath.Join(root, "groups.jsonl"), filepath.Join(root, "placements.jsonl"), filepath.Join(root, "transfers.jsonl"), filepath.Join(root, "out.jsonl")); err == nil || !strings.Contains(err.Error(), "observation predates transfer completion") {
		t.Fatalf("CompileSourcePressure() error = %v", err)
	}
}

func TestCompileSourcePressureRejectsRuntimeSourceDifferentFromEPPSelection(t *testing.T) {
	root := t.TempDir()
	group := sourceCompilerGroup(t, 2)
	event := sourceCompilerTransfer(group, group.Children[0], AcquisitionP2P)
	events := []P2PTransferEvent{event, sourceCompilerTransfer(group, group.Children[1], AcquisitionLocal)}
	for _, value := range events {
		if err := jsonl.Append(filepath.Join(root, "transfers.jsonl"), value); err != nil {
			t.Fatal(err)
		}
	}
	if err := jsonl.Append(filepath.Join(root, "groups.jsonl"), group); err != nil {
		t.Fatal(err)
	}
	writeSourceCompilerPlacements(t, filepath.Join(root, "placements.jsonl"), group, events)
	placements, err := jsonl.Read[evidence.PlacementEvent](filepath.Join(root, "placements.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	placements[0].SelectedP2PSource.Source = evidence.EndpointRef{ID: "model-1", Model: "model"}
	placements[0].Snapshot.Endpoints[len(placements[0].Snapshot.Endpoints)-1].P2PSources = []evidence.PrefixSource{*placements[0].SelectedP2PSource}
	if err := os.Remove(filepath.Join(root, "placements.jsonl")); err != nil {
		t.Fatal(err)
	}
	for _, placement := range placements {
		if err := jsonl.Append(filepath.Join(root, "placements.jsonl"), placement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := CompileSourcePressure(filepath.Join(root, "groups.jsonl"), filepath.Join(root, "placements.jsonl"), filepath.Join(root, "transfers.jsonl"), filepath.Join(root, "out.jsonl")); err == nil || !strings.Contains(err.Error(), "does not match EPP-selected source") {
		t.Fatalf("CompileSourcePressure() error = %v", err)
	}
}

func sourceCompilerGroup(t *testing.T, sourceCount uint32) evidence.GroupResult {
	t.Helper()
	observed := evidence.ConditionObservedState{OfferedLoadQPS: 50, AchievedLoadQPS: 50, SaturationQPS: 100, LoadProfileSHA256: strings.Repeat("1", 64), LoadProfilesSHA256: strings.Repeat("2", 64), LoadCalibrationSHA256: strings.Repeat("3", 64), CachedEndpointIDs: make([]string, sourceCount), PrefixSourceEndpointIDs: make([]string, sourceCount), OwnerEndpointOrder: []string{"model-0", "model-1", "model-2", "model-3"}, DrainedEndpointIDs: []string{"model-0", "model-1", "model-2", "model-3"}, DrainStableSamples: 2, OrdinaryTrafficMeanLatencySeconds: .01, MeasurementSource: "load-calibration:" + strings.Repeat("3", 64)}
	for index := uint32(0); index < sourceCount; index++ {
		observed.CachedEndpointIDs[index] = fmt.Sprintf("model-%d", index)
		observed.PrefixSourceEndpointIDs[index] = fmt.Sprintf("model-%d", index)
	}
	condition := evidence.ConditionAttestation{
		SchemaVersion: evidence.ConditionAttestationSchemaVersion, RunID: "run", GroupID: "group", LoadRegime: evidence.LoadModerate,
		CacheState: "distributed-warm", PrefixSourceCount: sourceCount, ControllerRevision: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ObservedState: observed, AppliedAt: time.Date(2026, 8, 26, 7, 59, 59, 0, time.UTC), FinalizedAt: timePointer(time.Date(2026, 8, 26, 8, 0, 2, 0, time.UTC)),
	}
	state := struct {
		SchemaVersion     string                          `json:"schema_version"`
		LoadRegime        evidence.LoadRegime             `json:"load_regime"`
		CacheState        string                          `json:"cache_state"`
		PrefixSourceCount uint32                          `json:"prefix_source_count,omitempty"`
		OwnerRotation     uint32                          `json:"owner_rotation"`
		ObservedState     evidence.ConditionObservedState `json:"observed_state"`
	}{"velaserve.applied-condition/v1", condition.LoadRegime, condition.CacheState, condition.PrefixSourceCount, condition.OwnerRotation, condition.ObservedState}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	condition.StateSHA256 = hex.EncodeToString(digest[:])
	dispatched := time.Date(2026, 8, 26, 8, 0, 0, 0, time.UTC)
	firstCompleted := dispatched.Add(800 * time.Millisecond)
	firstToken := dispatched.Add(400 * time.Millisecond)
	secondDispatched := dispatched.Add(time.Millisecond)
	secondCompleted := dispatched.Add(time.Second)
	secondFirstToken := secondDispatched.Add(500 * time.Millisecond)
	return evidence.GroupResult{
		SchemaVersion: evidence.SchemaVersion, RunID: "run", GroupID: "group", Arm: evidence.ArmLoadAwareP2P, FanoutWidth: 2,
		MakespanSeconds: 1, SlowestChildTTFTSeconds: .5, Cell: evidence.BenchmarkCell{Repetition: 1, PrefixRegime: "near", PrefixTokens: 4096, OutputRegime: "short", MaxTokens: 32, ArrivalSkewMS: 0, LoadRegime: evidence.LoadModerate, CacheState: "distributed-warm", Transport: "tcp", EPPReplicas: 1},
		Outcome: evidence.OutcomeSuccess, Condition: &condition, Children: []evidence.ChildResult{
			{RequestID: "request-1", TTFTSeconds: .4, LatencySeconds: .8, Outcome: evidence.OutcomeSuccess, DispatchedAt: &dispatched, FirstTokenAt: &firstToken, CompletedAt: &firstCompleted},
			{RequestID: "request-2", TTFTSeconds: .5, LatencySeconds: .999, Outcome: evidence.OutcomeSuccess, DispatchedAt: &secondDispatched, FirstTokenAt: &secondFirstToken, CompletedAt: &secondCompleted},
		},
	}
}

func timePointer(value time.Time) *time.Time { return &value }

func writeSourceCompilerPlacements(t *testing.T, path string, group evidence.GroupResult, events []P2PTransferEvent) {
	t.Helper()
	eventsByRequest := make(map[string]P2PTransferEvent, len(events))
	for _, event := range events {
		eventsByRequest[event.RequestID] = event
	}
	refs := make([]evidence.EndpointState, 0, len(group.Condition.ObservedState.CachedEndpointIDs)+1)
	for _, id := range group.Condition.ObservedState.CachedEndpointIDs {
		refs = append(refs, evidence.EndpointState{Ref: evidence.EndpointRef{ID: id, Model: "model"}, Healthy: true, Compatible: true, LocalPrefixTokens: group.Cell.PrefixTokens, SourcePrefixTokens: group.Cell.PrefixTokens})
	}
	refs = append(refs, evidence.EndpointState{Ref: evidence.EndpointRef{ID: "target", Model: "model"}, Healthy: true, Compatible: true})
	for _, child := range group.Children {
		observedAt := child.DispatchedAt.Add(time.Millisecond)
		placement := evidence.PlacementEvent{
			SchemaVersion: evidence.SchemaVersion, RunID: group.RunID, Arm: group.Arm, GroupID: group.GroupID, RequestID: child.RequestID,
			FanoutWidth: group.FanoutWidth, ArrivalSkewMS: group.Cell.ArrivalSkewMS, EPPReplicas: group.Cell.EPPReplicas, LoadRegime: group.Cell.LoadRegime,
			Snapshot: evidence.EndpointSnapshot{ObservedAt: observedAt, Endpoints: append([]evidence.EndpointState(nil), refs...)}, Target: refs[len(refs)-1].Ref, ObservedAt: observedAt,
		}
		if event, exists := eventsByRequest[child.RequestID]; exists && event.Acquisition == AcquisitionP2P && event.ChosenSource != nil {
			selected := evidence.PrefixSource{Source: *event.ChosenSource, CachedTokens: group.Cell.PrefixTokens, TransferBytes: *event.TransferBytes}
			placement.SelectedP2PSource = &selected
			placement.SelectedP2PSourceHost = "10.0.0.1"
			placement.SelectedP2PSourcePort = 7777
			placement.Snapshot.Endpoints[len(placement.Snapshot.Endpoints)-1].P2PSources = []evidence.PrefixSource{selected}
		}
		if err := jsonl.Append(path, placement); err != nil {
			t.Fatal(err)
		}
	}
}

func pointerTime(value time.Time) *time.Time { return &value }

func pointerFloat64(value float64) *float64 { return &value }
func pointerUint64(value uint64) *uint64    { return &value }

func sourceCompilerTransfer(group evidence.GroupResult, child evidence.ChildResult, acquisition Acquisition) P2PTransferEvent {
	observedAt := child.DispatchedAt.Add(50 * time.Millisecond)
	event := P2PTransferEvent{
		SchemaVersion: P2PTransferSchemaVersion, RunID: group.RunID, GroupID: group.GroupID,
		RequestID: child.RequestID, Acquisition: acquisition, ObservedAt: observedAt,
	}
	if acquisition == AcquisitionP2P {
		bytes := uint64(4096)
		started := child.DispatchedAt.Add(10 * time.Millisecond)
		completed := child.DispatchedAt.Add(40 * time.Millisecond)
		event.ChosenSource = &evidence.EndpointRef{ID: group.Condition.ObservedState.PrefixSourceEndpointIDs[0], Model: "model"}
		event.TransferBytes = &bytes
		event.TransferStartedAt = &started
		event.TransferCompletedAt = &completed
	}
	return event
}

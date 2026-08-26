package gatecompiler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JDinSeattle/velaserve/internal/bench"
	"github.com/JDinSeattle/velaserve/internal/conditiondriver"
	"github.com/JDinSeattle/velaserve/internal/evidence"
	"github.com/JDinSeattle/velaserve/internal/jsonl"
	gate "github.com/JDinSeattle/velaserve/research/gate-decision"
	replay "github.com/JDinSeattle/velaserve/research/oracle-replay"
	placementrecorder "github.com/JDinSeattle/velaserve/research/placement-recorder"
	zeroprobe "github.com/JDinSeattle/velaserve/research/zeroprobe"
)

func timePointer(value time.Time) *time.Time { return &value }

func TestVerifyWorkloadMatrixRejectsReplacedCoordinateWithUniqueGroupID(t *testing.T) {
	profile, err := bench.LoadProfile("testdata/placement/benchmark-profile.yaml")
	if err != nil {
		t.Fatal(err)
	}
	requests, err := bench.ExpandProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	groups := make([]evidence.GroupResult, len(requests))
	for index, request := range requests {
		groups[index] = evidence.GroupResult{RunID: "run", GroupID: fmt.Sprintf("group-%04d", index), Arm: request.Arm, FanoutWidth: uint32(len(request.Suffixes)), Cell: request.Cell, Condition: &evidence.ConditionAttestation{PrefixSourceCount: request.PrefixSourceCount, OwnerRotation: request.OwnerRotation}}
	}
	groups[len(groups)-1].Cell = groups[0].Cell
	groups[len(groups)-1].FanoutWidth = groups[0].FanoutWidth
	if err := verifyWorkloadMatrix(profile, groups, "run"); err == nil || !strings.Contains(err.Error(), "duplicate or unregistered workload coordinate") {
		t.Fatalf("verifyWorkloadMatrix() error = %v", err)
	}
}

func TestVerifyWorkloadMatrixRejectsOwnerRotationReplacement(t *testing.T) {
	profile, err := bench.LoadProfile("testdata/placement/benchmark-profile.yaml")
	if err != nil {
		t.Fatal(err)
	}
	requests, err := bench.ExpandProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	groups := make([]evidence.GroupResult, len(requests))
	for index, request := range requests {
		groups[index] = evidence.GroupResult{RunID: "run", GroupID: fmt.Sprintf("group-%04d", index), Arm: request.Arm, FanoutWidth: uint32(len(request.Suffixes)), Cell: request.Cell, Condition: &evidence.ConditionAttestation{PrefixSourceCount: request.PrefixSourceCount, OwnerRotation: request.OwnerRotation}}
	}
	groups[len(groups)-1].Condition.OwnerRotation = groups[0].Condition.OwnerRotation
	if err := verifyWorkloadMatrix(profile, groups, "run"); err == nil || !strings.Contains(err.Error(), "duplicate or unregistered workload coordinate") {
		t.Fatalf("verifyWorkloadMatrix() error = %v", err)
	}
}

func TestVerifyPlacementMatrixRejectsMissingPreregisteredCell(t *testing.T) {
	current := bundle{root: "testdata/placement", evidence: []evidence.GateEvidenceCell{{
		FanoutWidth: 2, LoadRegime: evidence.LoadModerate, PrefixRegime: "near", Transport: "tcp",
		Metric: gate.PlacementMetric, Pairs: 20, Improvement: evidence.ConfidenceInterval{Estimate: .1, Lower: .05, Upper: .15},
	}}}
	if err := verifyPlacementMatrix(current); err == nil {
		t.Fatal("verifyPlacementMatrix() accepted a partial matrix")
	}
}

func TestVerifyBundleBindingsRejectsDeploymentDrift(t *testing.T) {
	binding, err := zeroprobe.LoadPreflightBinding("../../internal/evidence/testdata/preflight-valid.json")
	if err != nil {
		t.Fatal(err)
	}
	drifted := binding
	drifted.EPP.Pods = append([]zeroprobe.PodBinding(nil), binding.EPP.Pods...)
	drifted.EPP.ProxyPods = append([]zeroprobe.PodBinding(nil), binding.EPP.ProxyPods...)
	drifted.EPP.Pods[0].UID = "different-epp-pod"
	drifted.EPP.ProxyPods[0].UID = "different-epp-pod"
	err = verifyBundleBindings([]bundle{
		{manifest: zeroprobe.Manifest{RunID: "z0-a"}, binding: binding},
		{manifest: zeroprobe.Manifest{RunID: "z0-b"}, binding: drifted},
	})
	if err == nil || !strings.Contains(err.Error(), "immutable deployment") {
		t.Fatalf("verifyBundleBindings() error = %v", err)
	}
}

func TestVerifyBundleBindingsAllowsPhaseSpecificBenchmarkProfiles(t *testing.T) {
	binding, err := zeroprobe.LoadPreflightBinding("../../internal/evidence/testdata/preflight-valid.json")
	if err != nil {
		t.Fatal(err)
	}
	z0c := binding
	z0c.BenchmarkProfileSHA256 = strings.Repeat("9", 64)
	err = verifyBundleBindings([]bundle{
		{manifest: zeroprobe.Manifest{RunID: "z0-b"}, binding: binding},
		{manifest: zeroprobe.Manifest{RunID: "z0-c"}, binding: z0c},
	})
	if err != nil {
		t.Fatalf("verifyBundleBindings() rejected phase-specific profile hashes: %v", err)
	}
}

func TestLoadCalibrationDeploymentRequiresExactRoutingAndEveryNode(t *testing.T) {
	binding, err := zeroprobe.LoadPreflightBinding("../../internal/evidence/testdata/preflight-valid.json")
	if err != nil {
		t.Fatal(err)
	}
	calibration := conditiondriver.LoadCalibration{
		ModelRevision: binding.Model.Revision, ModelImage: binding.Model.Image, ReplicaCount: uint32(len(binding.Model.Pods)),
		Transport: binding.Transport, ActiveArm: binding.ActiveArm, RoutingSHA256: binding.RoutingSHA256,
		RouterConfigSHA256: binding.RouterConfigSHA256, ProfileCalibrationSHA256: binding.ProfileCalibrationSHA256,
		GatewayChatURL: binding.Endpoint,
		InstanceType:   binding.Nodes[0].InstanceType, GPUModel: binding.Nodes[0].GPUModel, GPUDriverVersion: binding.Nodes[0].GPUDriverVersion,
	}
	for _, endpoint := range binding.Driver.Endpoints {
		calibration.Endpoints = append(calibration.Endpoints, conditiondriver.LoadCalibrationEndpoint{ID: endpoint.ID, PodUID: endpoint.PodUID, MetricsURL: endpoint.MetricsURL})
	}
	if err := validateLoadCalibrationDeployment(calibration, binding); err != nil {
		t.Fatalf("exact deployment binding rejected: %v", err)
	}
	for name, mutate := range map[string]func(*conditiondriver.LoadCalibration){
		"arm":                 func(value *conditiondriver.LoadCalibration) { value.ActiveArm = evidence.ArmAffinityP2P },
		"routing":             func(value *conditiondriver.LoadCalibration) { value.RoutingSHA256 = strings.Repeat("f", 64) },
		"router config":       func(value *conditiondriver.LoadCalibration) { value.RouterConfigSHA256 = strings.Repeat("f", 64) },
		"profile calibration": func(value *conditiondriver.LoadCalibration) { value.ProfileCalibrationSHA256 = strings.Repeat("f", 64) },
		"gateway URL": func(value *conditiondriver.LoadCalibration) {
			value.GatewayChatURL = "https://different.example/v1/chat/completions"
		},
		"endpoint pod UID": func(value *conditiondriver.LoadCalibration) {
			value.Endpoints = append([]conditiondriver.LoadCalibrationEndpoint(nil), value.Endpoints...)
			value.Endpoints[len(value.Endpoints)-1].PodUID = "different-pod-uid"
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := calibration
			mutate(&changed)
			if err := validateLoadCalibrationDeployment(changed, binding); err == nil {
				t.Fatal("mismatched calibration unexpectedly passed")
			}
		})
	}
	drifted := binding
	drifted.Nodes = append([]zeroprobe.NodeBinding(nil), binding.Nodes...)
	drifted.Nodes[len(drifted.Nodes)-1].GPUModel = "different-gpu"
	if err := validateLoadCalibrationDeployment(calibration, drifted); err == nil {
		t.Fatal("last-node hardware drift unexpectedly passed")
	}
}

func TestVerifiedOracleRecordsRejectsReledgerableDerivedTampering(t *testing.T) {
	root := t.TempDir()
	writeGateCompilerOracleFixture(t, root)
	oraclePath := filepath.Join(root, "oracle.jsonl")
	records, err := jsonl.Read[replay.OracleRecord](oraclePath)
	if err != nil {
		t.Fatal(err)
	}
	records[0].Oracle.BestMakespan++
	if err := os.Remove(oraclePath); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONL(oraclePath, records); err != nil {
		t.Fatal(err)
	}
	if _, err := verifiedOracleRecords(root, loadGateCompilerTestBinding(t)); err == nil || !strings.Contains(err.Error(), "raw groups, Envoy, EPP, and calibration") {
		t.Fatalf("verifiedOracleRecords() error = %v", err)
	}
}

func TestVerifiedPlacementsRejectsReledgerableDerivedTampering(t *testing.T) {
	root := t.TempDir()
	writeGateCompilerOracleFixture(t, root)
	path := filepath.Join(root, "placements.jsonl")
	placements, err := jsonl.Read[evidence.PlacementEvent](path)
	if err != nil {
		t.Fatal(err)
	}
	placements[0].Target = placements[0].Snapshot.Endpoints[1].Ref
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONL(path, placements); err != nil {
		t.Fatal(err)
	}
	if _, err := verifiedPlacements(root, loadGateCompilerTestBinding(t)); err == nil || !strings.Contains(err.Error(), "retained placements do not match raw groups, Envoy, and EPP") {
		t.Fatalf("verifiedPlacements() error = %v", err)
	}
}

func TestVerifiedSourcePressureRejectsReledgerableDerivedTampering(t *testing.T) {
	root := t.TempDir()
	writeGateCompilerSourceFixture(t, root)
	derivedPath := filepath.Join(root, "source-pressure.jsonl")
	records, err := jsonl.Read[placementrecorder.SourcePressureObservation](derivedPath)
	if err != nil {
		t.Fatal(err)
	}
	records[0].LastSiblingTTFTSeconds++
	if err := os.Remove(derivedPath); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONL(derivedPath, records); err != nil {
		t.Fatal(err)
	}
	binding := loadGateCompilerTestBinding(t)
	if _, err := verifiedSourcePressure(root, binding); err == nil || !strings.Contains(err.Error(), "raw groups, placements, and vLLM runtime") {
		t.Fatalf("verifiedSourcePressure() error = %v", err)
	}
}

func TestVerifiedSourcePressureRejectsConsistentlyRewrittenDerivedFiles(t *testing.T) {
	root := t.TempDir()
	writeGateCompilerSourceFixture(t, root)
	transfersPath := filepath.Join(root, "p2p-transfers.jsonl")
	transfers, err := jsonl.Read[placementrecorder.P2PTransferEvent](transfersPath)
	if err != nil {
		t.Fatal(err)
	}
	for index := range transfers {
		transfers[index].Acquisition = placementrecorder.AcquisitionRecompute
	}
	if err := os.Remove(transfersPath); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONL(transfersPath, transfers); err != nil {
		t.Fatal(err)
	}
	derivedPath := filepath.Join(root, "source-pressure.jsonl")
	if err := os.Remove(derivedPath); err != nil {
		t.Fatal(err)
	}
	if _, err := placementrecorder.CompileSourcePressure(
		filepath.Join(root, "groups.jsonl"), filepath.Join(root, "placements.jsonl"), transfersPath, derivedPath,
	); err != nil {
		t.Fatal(err)
	}
	binding := loadGateCompilerTestBinding(t)
	if _, err := verifiedSourcePressure(root, binding); err == nil || !strings.Contains(err.Error(), "normalized P2P transfers do not match raw vLLM runtime") {
		t.Fatalf("verifiedSourcePressure() error = %v", err)
	}
}

func writeGateCompilerOracleFixture(t *testing.T, root string) {
	t.Helper()
	observedAt := time.Date(2026, 8, 26, 8, 0, 0, 0, time.UTC)
	snapshot := evidence.EndpointSnapshot{ObservedAt: observedAt, Endpoints: []evidence.EndpointState{
		{Ref: evidence.EndpointRef{ID: "model-0", Model: "model"}, Healthy: true, Compatible: true, LocalPrefixTokens: 4096},
		{Ref: evidence.EndpointRef{ID: "model-1", Model: "model"}, Healthy: true, Compatible: true},
	}}
	placements := make([]evidence.PlacementEvent, 0, 2)
	for index := 0; index < 2; index++ {
		at := observedAt.Add(time.Duration(index) * time.Millisecond)
		event := evidence.PlacementEvent{
			SchemaVersion: evidence.SchemaVersion, RunID: "run", Arm: evidence.ArmLoadAwareP2P, GroupID: "group",
			RequestID: "request-" + string(rune('1'+index)), FanoutWidth: 2, ArrivalSkewMS: 1, EPPReplicas: 1,
			LoadRegime: evidence.LoadModerate, Snapshot: snapshot, Target: snapshot.Endpoints[index].Ref, ObservedAt: at,
		}
		event.Snapshot.ObservedAt = at
		placements = append(placements, event)
	}
	calibration := []byte("inflight_publication_delay_ms: 5\naffinity_load_gate_seconds: 2\ncalibration:\n  prefill_tokens_per_second: 1000\n  pull_bytes_per_second: 1000000\n  bytes_per_cached_token: 512\nservice_seconds_by_output_regime:\n  short: {max_tokens: 32, seconds: 1}\n")
	if err := os.WriteFile(filepath.Join(root, "oracle-calibration.yaml"), calibration, 0o600); err != nil {
		t.Fatal(err)
	}
	firstToken := observedAt.Add(500 * time.Millisecond)
	completed := observedAt.Add(time.Second)
	conditionObserved := evidence.ConditionObservedState{OfferedLoadQPS: 50, AchievedLoadQPS: 50, SaturationQPS: 100, LoadProfileSHA256: strings.Repeat("1", 64), LoadProfilesSHA256: strings.Repeat("2", 64), LoadCalibrationSHA256: strings.Repeat("3", 64), CachedEndpointIDs: []string{"model-0"}, OwnerEndpointOrder: []string{"model-0", "model-1"}, DrainedEndpointIDs: []string{"model-0", "model-1"}, DrainStableSamples: 2, OrdinaryTrafficMeanLatencySeconds: .01, MeasurementSource: "load-calibration:" + strings.Repeat("3", 64)}
	conditionState := struct {
		SchemaVersion     string                          `json:"schema_version"`
		LoadRegime        evidence.LoadRegime             `json:"load_regime"`
		CacheState        string                          `json:"cache_state"`
		PrefixSourceCount uint32                          `json:"prefix_source_count,omitempty"`
		OwnerRotation     uint32                          `json:"owner_rotation"`
		ObservedState     evidence.ConditionObservedState `json:"observed_state"`
	}{"velaserve.applied-condition/v1", evidence.LoadModerate, "warm-owner", 0, 0, conditionObserved}
	conditionBytes, err := json.Marshal(conditionState)
	if err != nil {
		t.Fatal(err)
	}
	conditionDigest := sha256.Sum256(conditionBytes)
	group := evidence.GroupResult{
		SchemaVersion: evidence.SchemaVersion, RunID: "run", Arm: evidence.ArmLoadAwareP2P, GroupID: "group", FanoutWidth: 2,
		MakespanSeconds: 1, SlowestChildTTFTSeconds: .5,
		Cell:      evidence.BenchmarkCell{Repetition: 1, PrefixRegime: "near", PrefixTokens: 4096, OutputRegime: "short", MaxTokens: 32, ArrivalSkewMS: 1, LoadRegime: evidence.LoadModerate, CacheState: "warm-owner", Transport: "tcp", EPPReplicas: 1},
		Outcome:   evidence.OutcomeSuccess,
		Condition: &evidence.ConditionAttestation{SchemaVersion: evidence.ConditionAttestationSchemaVersion, RunID: "run", GroupID: "group", LoadRegime: evidence.LoadModerate, CacheState: "warm-owner", ControllerRevision: strings.Repeat("a", 40), StateSHA256: hex.EncodeToString(conditionDigest[:]), ObservedState: conditionObserved, AppliedAt: observedAt.Add(-time.Second), FinalizedAt: timePointer(completed.Add(time.Second))},
		Children:  []evidence.ChildResult{{RequestID: "request-1", TTFTSeconds: .5, LatencySeconds: 1, Outcome: evidence.OutcomeSuccess, DispatchedAt: &observedAt, FirstTokenAt: &firstToken, CompletedAt: &completed}, {RequestID: "request-2", TTFTSeconds: .5, LatencySeconds: 1, Outcome: evidence.OutcomeSuccess, DispatchedAt: &observedAt, FirstTokenAt: &firstToken, CompletedAt: &completed}},
	}
	if err := jsonl.Append(filepath.Join(root, "groups.jsonl"), group); err != nil {
		t.Fatal(err)
	}
	writeRawPlacementFixture(t, root, placements)
	if err := replay.ReplayFile(replay.FileOptions{GroupsPath: filepath.Join(root, "groups.jsonl"), PlacementsPath: filepath.Join(root, "placements.jsonl"), CalibrationPath: filepath.Join(root, "oracle-calibration.yaml"), OutputPath: filepath.Join(root, "oracle.jsonl")}); err != nil {
		t.Fatal(err)
	}
}

func writeGateCompilerSourceFixture(t *testing.T, root string) {
	t.Helper()
	dispatched := time.Date(2026, 8, 26, 8, 0, 0, 0, time.UTC)
	completed := dispatched.Add(time.Second)
	observed := evidence.ConditionObservedState{SaturationQPS: 100, LoadProfileSHA256: strings.Repeat("1", 64), LoadProfilesSHA256: strings.Repeat("2", 64), LoadCalibrationSHA256: strings.Repeat("3", 64), CachedEndpointIDs: []string{"model-0"}, PrefixSourceEndpointIDs: []string{"model-0"}, OwnerEndpointOrder: []string{"model-0"}, DrainedEndpointIDs: []string{"model-0"}, DrainStableSamples: 2, MeasurementSource: "load-calibration:" + strings.Repeat("3", 64)}
	state := struct {
		SchemaVersion     string                          `json:"schema_version"`
		LoadRegime        evidence.LoadRegime             `json:"load_regime"`
		CacheState        string                          `json:"cache_state"`
		PrefixSourceCount uint32                          `json:"prefix_source_count,omitempty"`
		OwnerRotation     uint32                          `json:"owner_rotation"`
		ObservedState     evidence.ConditionObservedState `json:"observed_state"`
	}{"velaserve.applied-condition/v1", evidence.LoadIdle, "distributed-warm", 1, 0, observed}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	condition := evidence.ConditionAttestation{
		SchemaVersion: evidence.ConditionAttestationSchemaVersion, RunID: "run", GroupID: "group", LoadRegime: evidence.LoadIdle,
		CacheState: "distributed-warm", PrefixSourceCount: 1, ControllerRevision: strings.Repeat("a", 40),
		StateSHA256: hex.EncodeToString(digest[:]), ObservedState: observed, AppliedAt: dispatched, FinalizedAt: timePointer(completed.Add(time.Second)),
	}
	firstToken := dispatched.Add(500 * time.Millisecond)
	children := []evidence.ChildResult{
		{RequestID: "request-1", PromptTokens: 4097, CachedTokens: uint64Pointer(4096), TTFTSeconds: .5, LatencySeconds: 1, Outcome: evidence.OutcomeSuccess, DispatchedAt: &dispatched, FirstTokenAt: &firstToken, CompletedAt: &completed},
		{RequestID: "request-2", PromptTokens: 4097, CachedTokens: uint64Pointer(4096), TTFTSeconds: .5, LatencySeconds: 1, Outcome: evidence.OutcomeSuccess, DispatchedAt: &dispatched, FirstTokenAt: &firstToken, CompletedAt: &completed},
	}
	group := evidence.GroupResult{
		SchemaVersion: evidence.SchemaVersion, RunID: "run", Arm: evidence.ArmLoadAwareP2P, GroupID: "group", FanoutWidth: 2,
		MakespanSeconds: 1, SlowestChildTTFTSeconds: .5,
		Cell:    evidence.BenchmarkCell{Repetition: 1, PrefixRegime: "near", PrefixTokens: 4096, OutputRegime: "short", MaxTokens: 32, LoadRegime: evidence.LoadIdle, CacheState: "distributed-warm", Transport: "tcp", EPPReplicas: 1},
		Outcome: evidence.OutcomeSuccess, Children: children, Condition: &condition, RecomputedPrefixTokens: uint64Pointer(0),
	}
	if err := jsonl.Append(filepath.Join(root, "groups.jsonl"), group); err != nil {
		t.Fatal(err)
	}
	var vllmRaw bytes.Buffer
	fmt.Fprintln(&vllmRaw, `VELASERVE_STREAM_BINDING {"schema_version":"velaserve.stream-emitter-binding/v1","pod_name":"model-0","pod_uid":"m0"}`)
	placements := make([]evidence.PlacementEvent, 0, len(children))
	for _, child := range children {
		observedAt := dispatched.Add(100 * time.Millisecond)
		runtime := placementrecorder.VLLMRuntimeRecord{
			SchemaVersion: placementrecorder.VLLMAcquisitionSchemaVersion, EngineRequestID: "chatcmpl-" + child.RequestID,
			LocalCachedTokens: uint64Pointer(4096), ExternalCachedTokens: uint64Pointer(0), ObservedAt: observedAt,
		}
		encodedRuntime, err := json.Marshal(runtime)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&vllmRaw, "[model-0/vllm] VELASERVE_VLLM_RUNTIME %s\n", encodedRuntime)
		placement := evidence.PlacementEvent{
			SchemaVersion: evidence.SchemaVersion, RunID: "run", Arm: evidence.ArmLoadAwareP2P, GroupID: "group", RequestID: child.RequestID,
			FanoutWidth: 2, EPPReplicas: 1, LoadRegime: evidence.LoadIdle, ObservedAt: observedAt,
			Snapshot: evidence.EndpointSnapshot{ObservedAt: observedAt, Endpoints: []evidence.EndpointState{{
				Ref: evidence.EndpointRef{ID: "model-0", Model: "model"}, Healthy: true, Compatible: true, LocalPrefixTokens: 4096, SourcePrefixTokens: 4096,
			}}},
			Target: evidence.EndpointRef{ID: "model-0", Model: "model"},
		}
		placements = append(placements, placement)
	}
	if err := os.WriteFile(filepath.Join(root, "vllm-raw.log"), vllmRaw.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := placementrecorder.NormalizePinnedVLLMLog(filepath.Join(root, "vllm-raw.log"), filepath.Join(root, "vllm-runtime.jsonl")); err != nil {
		t.Fatal(err)
	}
	writeRawPlacementFixture(t, root, placements)
	if _, err := placementrecorder.NormalizeVLLMRuntime(filepath.Join(root, "groups.jsonl"), filepath.Join(root, "placements.jsonl"), filepath.Join(root, "vllm-runtime.jsonl"), filepath.Join(root, "p2p-transfers.jsonl"), []placementrecorder.RuntimeEmitterBinding{{PodName: "model-0", PodUID: "m0"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := placementrecorder.CompileSourcePressure(filepath.Join(root, "groups.jsonl"), filepath.Join(root, "placements.jsonl"), filepath.Join(root, "p2p-transfers.jsonl"), filepath.Join(root, "source-pressure.jsonl")); err != nil {
		t.Fatal(err)
	}
}

func loadGateCompilerTestBinding(t *testing.T) zeroprobe.PreflightBinding {
	t.Helper()
	binding, err := zeroprobe.LoadPreflightBinding("../../internal/evidence/testdata/preflight-valid.json")
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

func writeRawPlacementFixture(t *testing.T, root string, placements []evidence.PlacementEvent) {
	t.Helper()
	groups, err := jsonl.Read[evidence.GroupResult](filepath.Join(root, "groups.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	children := make(map[string]evidence.ChildResult)
	for _, group := range groups {
		for _, child := range group.Children {
			children[child.RequestID] = child
		}
	}
	bindingLine := `VELASERVE_STREAM_BINDING {"schema_version":"velaserve.stream-emitter-binding/v1","pod_name":"epp-0","pod_uid":"e0"}`
	var envoyRaw bytes.Buffer
	var eppRaw bytes.Buffer
	fmt.Fprintln(&envoyRaw, bindingLine)
	fmt.Fprintln(&eppRaw, bindingLine)
	for _, placement := range placements {
		child := children[placement.RequestID]
		if child.DispatchedAt == nil || child.CompletedAt == nil {
			t.Fatalf("placement fixture child %q lacks chronology", placement.RequestID)
		}
		duration := uint64(child.CompletedAt.Sub(*child.DispatchedAt) / time.Millisecond)
		envoy := map[string]any{
			"START_TIME": child.DispatchedAt.Format(time.RFC3339Nano), "REQ(X-REQUEST-ID)": placement.RequestID,
			"REQ(X-VELA-FANOUT-GROUP)": placement.GroupID, "UPSTREAM_HOST": placement.Target.ID,
			"RESPONSE_CODE": 200, "DURATION": duration,
		}
		encodedEnvoy, err := json.Marshal(envoy)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&envoyRaw, "[epp-0/inner-envoy] %s\n", encodedEnvoy)
		epp := placementrecorder.EPPRecord{
			SchemaVersion: placementrecorder.EPPSchedulingSchemaVersion, RequestID: placement.RequestID, GroupID: placement.GroupID,
			ObservedAt: placement.ObservedAt, Snapshot: placement.Snapshot, Target: placement.Target, TargetHost: placement.Target.ID,
			ScoredCandidates: placement.ScoredCandidates, SelectedP2PSource: placement.SelectedP2PSource,
			SelectedP2PSourceHost: placement.SelectedP2PSourceHost, SelectedP2PSourcePort: placement.SelectedP2PSourcePort,
		}
		encodedEPP, err := json.Marshal(epp)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&eppRaw, "[epp-0/epp] VELASERVE_EPP_RECORD %s\n", encodedEPP)
	}
	if err := os.WriteFile(filepath.Join(root, "envoy-raw.log"), envoyRaw.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "epp-raw.log"), eppRaw.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := placementrecorder.NormalizePinnedEnvoyLog(filepath.Join(root, "envoy-raw.log"), filepath.Join(root, "envoy.jsonl")); err != nil {
		t.Fatal(err)
	}
	if _, err := placementrecorder.NormalizePinnedEPPLog(filepath.Join(root, "epp-raw.log"), filepath.Join(root, "epp.jsonl")); err != nil {
		t.Fatal(err)
	}
	report, err := placementrecorder.Ingest(context.Background(), placementrecorder.IngestOptions{
		ArtifactRoot: root, GroupsPath: "groups.jsonl", EnvoyPath: "envoy.jsonl", EPPPath: "epp.jsonl",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Complete {
		t.Fatalf("placement fixture ingest = %#v", report)
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ingest-report.json"), append(encoded, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func uint64Pointer(value uint64) *uint64 {
	return &value
}

package gatecompiler

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	benchmarkanalysis "github.com/JDinSeattle/velaserve/benchmarks/analysis"
	"github.com/JDinSeattle/velaserve/internal/artifacts"
	"github.com/JDinSeattle/velaserve/internal/bench"
	"github.com/JDinSeattle/velaserve/internal/conditiondriver"
	"github.com/JDinSeattle/velaserve/internal/evidence"
	"github.com/JDinSeattle/velaserve/internal/jsonl"
	gate "github.com/JDinSeattle/velaserve/research/gate-decision"
	replay "github.com/JDinSeattle/velaserve/research/oracle-replay"
	placementrecorder "github.com/JDinSeattle/velaserve/research/placement-recorder"
	zeroprobe "github.com/JDinSeattle/velaserve/research/zeroprobe"
)

const BundleLedgerSchemaVersion = "velaserve.gate-bundle-ledger/v1"

type Options struct {
	BundleRoots         []string
	PreregistrationPath string
	OutputDirectory     string
	DecisionID          string
	DecidedAt           time.Time
	PrivateKeyPath      string
}

type BundleLedgerEntry struct {
	SchemaVersion        string          `json:"schema_version"`
	RunID                string          `json:"run_id"`
	Phase                zeroprobe.Phase `json:"phase"`
	ArtifactLedgerSHA256 string          `json:"artifact_ledger_sha256"`
	GateEvidenceSHA256   string          `json:"gate_evidence_sha256"`
}

type bundle struct {
	root       string
	manifest   zeroprobe.Manifest
	report     zeroprobe.AnalysisReport
	binding    zeroprobe.PreflightBinding
	ledgerHash string
	evidence   []evidence.GateEvidenceCell
}

func Compile(options Options) (evidence.GateDecision, error) {
	if len(options.BundleRoots) == 0 {
		return evidence.GateDecision{}, fmt.Errorf("at least one verified artifact bundle is required")
	}
	if strings.TrimSpace(options.DecisionID) == "" {
		return evidence.GateDecision{}, fmt.Errorf("decision ID is required")
	}
	if strings.TrimSpace(options.PrivateKeyPath) == "" {
		return evidence.GateDecision{}, fmt.Errorf("private signing key is required; compiler output is always signed")
	}
	if options.DecidedAt.IsZero() {
		options.DecidedAt = time.Now().UTC()
	}
	preregistrationHash, err := hashPath(options.PreregistrationPath)
	if err != nil {
		return evidence.GateDecision{}, fmt.Errorf("hash preregistration: %w", err)
	}

	bundles := make([]bundle, 0, len(options.BundleRoots))
	seenRun := make(map[string]struct{}, len(options.BundleRoots))
	for _, root := range options.BundleRoots {
		loaded, err := loadBundle(root, preregistrationHash)
		if err != nil {
			return evidence.GateDecision{}, err
		}
		if _, exists := seenRun[loaded.manifest.RunID]; exists {
			return evidence.GateDecision{}, fmt.Errorf("run %q is supplied more than once", loaded.manifest.RunID)
		}
		seenRun[loaded.manifest.RunID] = struct{}{}
		bundles = append(bundles, loaded)
	}
	if err := verifyBundleBindings(bundles); err != nil {
		return evidence.GateDecision{}, err
	}

	var placementBundle *bundle
	var sourceBundle *bundle
	for index := range bundles {
		current := bundles[index]
		switch current.manifest.Phase {
		case zeroprobe.PhaseZ0B:
			if placementBundle != nil {
				return evidence.GateDecision{}, fmt.Errorf("exactly one Z0-B bundle is allowed")
			}
			placementBundle = &bundles[index]
		case zeroprobe.PhaseZ0C:
			if sourceBundle != nil {
				return evidence.GateDecision{}, fmt.Errorf("exactly one interleaved Z0-C bundle is allowed")
			}
			sourceBundle = &bundles[index]
		default:
			return evidence.GateDecision{}, fmt.Errorf("run %q phase %s cannot enter the gate compiler", current.manifest.RunID, current.manifest.Phase)
		}
	}
	if placementBundle == nil {
		return evidence.GateDecision{}, fmt.Errorf("one complete real-GPU Z0-B bundle is required")
	}
	if err := verifyPlacementMatrix(*placementBundle); err != nil {
		return evidence.GateDecision{}, err
	}
	placementCells := onlyMetric(placementBundle.evidence, gate.PlacementMetric)
	preview, err := gate.Decide(gate.DecisionInput{
		DecisionID: options.DecisionID, PreregistrationSHA256: preregistrationHash,
		ArtifactLedgerSHA256: strings.Repeat("a", 64), EvidenceSHA256: strings.Repeat("b", 64),
		Threshold: 0.10, Confidence: 0.95, EvidenceComplete: true, Evidence: placementCells, DecidedAt: options.DecidedAt,
	})
	if err != nil {
		return evidence.GateDecision{}, err
	}

	cells := placementCells
	if preview.Branch == evidence.BranchPlacement {
		if sourceBundle != nil {
			return evidence.GateDecision{}, fmt.Errorf("Z0-C bundles are not eligible after the Z0-B placement gate passes")
		}
	} else {
		if sourceBundle == nil {
			return evidence.GateDecision{}, fmt.Errorf("Z0-B did not pass; one complete interleaved Z0-C bundle is required")
		}
		_, groups, err := loadAndVerifyWorkloadMatrix(*sourceBundle)
		if err != nil {
			return evidence.GateDecision{}, fmt.Errorf("verify interleaved Z0-C workload matrix: %w", err)
		}
		observations, err := verifiedSourcePressure(sourceBundle.root, sourceBundle.binding)
		if err != nil {
			return evidence.GateDecision{}, err
		}
		runs, err := splitSourcePressureRuns(groups, observations)
		if err != nil {
			return evidence.GateDecision{}, err
		}
		sourceCells, err := benchmarkanalysis.SourcePressureGateCells(runs, int64(placementBundle.manifest.Seed), 10_000, 0.95)
		if err != nil {
			return evidence.GateDecision{}, fmt.Errorf("compile Z0-C evidence: %w", err)
		}
		cells = append(cells, sourceCells...)
	}

	ledgerEntries := make([]BundleLedgerEntry, 0, len(bundles))
	for _, current := range bundles {
		ledgerEntries = append(ledgerEntries, BundleLedgerEntry{
			SchemaVersion: BundleLedgerSchemaVersion, RunID: current.manifest.RunID, Phase: current.manifest.Phase,
			ArtifactLedgerSHA256: current.ledgerHash, GateEvidenceSHA256: current.report.EvidenceSHA256,
		})
	}
	sort.Slice(ledgerEntries, func(i, j int) bool {
		if ledgerEntries[i].Phase != ledgerEntries[j].Phase {
			return ledgerEntries[i].Phase < ledgerEntries[j].Phase
		}
		return ledgerEntries[i].RunID < ledgerEntries[j].RunID
	})
	return writeCompiled(options, preregistrationHash, ledgerEntries, cells)
}

func verifyBundleBindings(bundles []bundle) error {
	boundInvariant := ""
	for _, current := range bundles {
		invariant, err := current.binding.InvariantSHA256()
		if err != nil {
			return fmt.Errorf("validate run %q preflight binding: %w", current.manifest.RunID, err)
		}
		if boundInvariant == "" {
			boundInvariant = invariant
		} else if invariant != boundInvariant {
			return fmt.Errorf("real-GPU bundles do not share one immutable deployment preflight binding")
		}
	}
	return nil
}

func splitSourcePressureRuns(groups []evidence.GroupResult, observations []placementrecorder.SourcePressureObservation) ([]benchmarkanalysis.SourcePressureRun, error) {
	groupsByCount := make(map[uint32][]evidence.GroupResult, 3)
	for index, group := range groups {
		if group.Condition == nil {
			return nil, fmt.Errorf("Z0-C group %d lacks a source-count condition", index+1)
		}
		count := group.Condition.PrefixSourceCount
		if count != 1 && count != 2 && count != 4 {
			return nil, fmt.Errorf("Z0-C group %d has unregistered source count %d", index+1, count)
		}
		groupsByCount[count] = append(groupsByCount[count], group)
	}
	observationsByCount := make(map[uint32][]placementrecorder.SourcePressureObservation, 3)
	for index, observation := range observations {
		count := observation.CandidateSourceCount
		if count != 1 && count != 2 && count != 4 {
			return nil, fmt.Errorf("Z0-C observation %d has unregistered source count %d", index+1, count)
		}
		observationsByCount[count] = append(observationsByCount[count], observation)
	}
	runs := make([]benchmarkanalysis.SourcePressureRun, 0, 3)
	for _, count := range []uint32{1, 2, 4} {
		if len(groupsByCount[count]) == 0 || len(observationsByCount[count]) == 0 {
			return nil, fmt.Errorf("interleaved Z0-C bundle lacks complete source-count %d evidence", count)
		}
		runs = append(runs, benchmarkanalysis.SourcePressureRun{
			CandidateSourceCount: count,
			Groups:               groupsByCount[count],
			Observations:         observationsByCount[count],
		})
	}
	return runs, nil
}

func loadBundle(root, preregistrationHash string) (bundle, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return bundle{}, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return bundle{}, fmt.Errorf("resolve bundle %q: %w", root, err)
	}
	if err := zeroprobe.Verify(root); err != nil {
		return bundle{}, fmt.Errorf("verify bundle %q: %w", root, err)
	}
	manifest, err := readJSON[zeroprobe.Manifest](filepath.Join(root, "manifest.json"))
	if err != nil {
		return bundle{}, err
	}
	if manifest.PreregistrationSHA256 != preregistrationHash {
		return bundle{}, fmt.Errorf("run %q preregistration hash does not match compiler input", manifest.RunID)
	}
	copyHash, err := hashPath(filepath.Join(root, "preregistration.yaml"))
	if err != nil || copyHash != preregistrationHash {
		return bundle{}, fmt.Errorf("run %q preregistration copy is not the compiler input", manifest.RunID)
	}
	report, err := readJSON[zeroprobe.AnalysisReport](filepath.Join(root, "analysis-report.json"))
	if err != nil {
		return bundle{}, err
	}
	if report.EvidenceScope != "real_gpu" {
		return bundle{}, fmt.Errorf("run %q has evidence scope %q; only real_gpu is gate eligible", manifest.RunID, report.EvidenceScope)
	}
	binding, err := zeroprobe.LoadPreflightBinding(filepath.Join(root, "preflight-binding.json"))
	if err != nil {
		return bundle{}, fmt.Errorf("run %q preflight binding: %w", manifest.RunID, err)
	}
	loadCalibration, err := readJSON[conditiondriver.LoadCalibration](filepath.Join(root, "load-calibration.json"))
	if err != nil {
		return bundle{}, fmt.Errorf("run %q retained load calibration: %w", manifest.RunID, err)
	}
	loadCalibrationHash, err := conditiondriver.LoadCalibrationSHA256(loadCalibration)
	if err != nil || loadCalibrationHash != binding.Driver.LoadCalibrationSHA256 {
		return bundle{}, fmt.Errorf("run %q retained load calibration is not the preflight-bound saturation sweep", manifest.RunID)
	}
	ledgerHash, err := hashPath(filepath.Join(root, artifacts.LedgerName))
	if err != nil {
		return bundle{}, err
	}
	cells, err := jsonl.Read[evidence.GateEvidenceCell](filepath.Join(root, "gate-evidence.jsonl"))
	if err != nil {
		return bundle{}, err
	}
	loaded := bundle{root: root, manifest: manifest, report: report, binding: binding, ledgerHash: ledgerHash, evidence: cells}
	registeredSourceCounts := map[uint32]struct{}{}
	if manifest.Phase == zeroprobe.PhaseZ0C {
		condition, err := readJSON[zeroprobe.ExperimentCondition](filepath.Join(root, "condition.json"))
		if err != nil {
			return bundle{}, err
		}
		if condition.SchemaVersion != zeroprobe.ConditionSchemaVersion || condition.Phase != zeroprobe.PhaseZ0C || len(condition.PrefixSourceCounts) != 3 || condition.PrefixSourceCounts[0] != 1 || condition.PrefixSourceCounts[1] != 2 || condition.PrefixSourceCounts[2] != 4 {
			return bundle{}, fmt.Errorf("run %q does not declare the frozen interleaved Z0-C source counts", manifest.RunID)
		}
		for _, count := range condition.PrefixSourceCounts {
			registeredSourceCounts[count] = struct{}{}
		}
	}
	groups, err := jsonl.Read[evidence.GroupResult](filepath.Join(root, "groups.jsonl"))
	if err != nil {
		return bundle{}, err
	}
	controllerRevision := ""
	type loadShape struct {
		prefixTokens uint64
		maxTokens    uint32
	}
	loadProfilesByShape := make(map[loadShape]conditiondriver.LoadProfile)
	observedSourceCounts := map[uint32]struct{}{}
	for index, group := range groups {
		if group.Condition == nil {
			return bundle{}, fmt.Errorf("run %q real-GPU group %d lacks condition attestation", manifest.RunID, index+1)
		}
		if manifest.Phase == zeroprobe.PhaseZ0C {
			if _, registered := registeredSourceCounts[group.Condition.PrefixSourceCount]; !registered {
				return bundle{}, fmt.Errorf("run %q group %d source-count attestation is outside its phase condition", manifest.RunID, index+1)
			}
			observedSourceCounts[group.Condition.PrefixSourceCount] = struct{}{}
		} else if group.Condition.PrefixSourceCount != 0 {
			return bundle{}, fmt.Errorf("run %q non-Z0-C group %d unexpectedly has a source-count condition", manifest.RunID, index+1)
		}
		if group.Condition.ObservedState.LoadProfilesSHA256 != binding.Driver.LoadProfilesSHA256 {
			return bundle{}, fmt.Errorf("run %q group %d load profiles are not the preflight-bound driver profiles", manifest.RunID, index+1)
		}
		if group.Condition.ObservedState.LoadCalibrationSHA256 != binding.Driver.LoadCalibrationSHA256 {
			return bundle{}, fmt.Errorf("run %q group %d load calibration is not the preflight-bound saturation sweep", manifest.RunID, index+1)
		}
		profile := conditiondriver.LoadProfile{
			PrefixTokens: group.Cell.PrefixTokens, MaxTokens: group.Cell.MaxTokens,
			SaturationQPS: group.Condition.ObservedState.SaturationQPS, MeasurementSource: group.Condition.ObservedState.MeasurementSource,
			CalibrationSHA256: group.Condition.ObservedState.LoadCalibrationSHA256,
		}
		profileHash, err := conditiondriver.LoadProfileSHA256(profile)
		if err != nil || profileHash != group.Condition.ObservedState.LoadProfileSHA256 {
			return bundle{}, fmt.Errorf("run %q group %d load profile is not canonically derived from its receipt", manifest.RunID, index+1)
		}
		shape := loadShape{prefixTokens: profile.PrefixTokens, maxTokens: profile.MaxTokens}
		if previous, exists := loadProfilesByShape[shape]; exists && previous != profile {
			return bundle{}, fmt.Errorf("run %q mixes saturation profiles for one prompt/output shape", manifest.RunID)
		}
		loadProfilesByShape[shape] = profile
		if controllerRevision == "" {
			controllerRevision = group.Condition.ControllerRevision
		}
		if group.Condition.ControllerRevision != controllerRevision {
			return bundle{}, fmt.Errorf("run %q mixes condition-controller revisions", manifest.RunID)
		}
	}
	loadProfiles := make([]conditiondriver.LoadProfile, 0, len(loadProfilesByShape))
	for _, profile := range loadProfilesByShape {
		loadProfiles = append(loadProfiles, profile)
	}
	loadProfilesHash, err := conditiondriver.LoadProfilesSHA256(loadProfiles)
	if err != nil || loadProfilesHash != binding.Driver.LoadProfilesSHA256 {
		return bundle{}, fmt.Errorf("run %q receipt-derived load profiles do not match the preflight binding", manifest.RunID)
	}
	loadCalibrationHash, err = conditiondriver.ValidateLoadCalibration(loadCalibration, loadProfiles, binding.Model.ID)
	if err != nil || loadCalibrationHash != binding.Driver.LoadCalibrationSHA256 {
		return bundle{}, fmt.Errorf("run %q saturation profiles are not derived from the retained raw calibration: %w", manifest.RunID, err)
	}
	if err := validateLoadCalibrationDeployment(loadCalibration, binding); err != nil {
		return bundle{}, fmt.Errorf("run %q load calibration deployment identity does not match preflight: %w", manifest.RunID, err)
	}
	if manifest.Phase == zeroprobe.PhaseZ0C && len(observedSourceCounts) != len(registeredSourceCounts) {
		return bundle{}, fmt.Errorf("run %q does not cover every interleaved Z0-C source count", manifest.RunID)
	}
	return loaded, nil
}

func validateLoadCalibrationDeployment(loadCalibration conditiondriver.LoadCalibration, binding zeroprobe.PreflightBinding) error {
	if loadCalibration.ModelRevision != binding.Model.Revision || loadCalibration.ModelImage != binding.Model.Image || loadCalibration.ReplicaCount != uint32(len(binding.Model.Pods)) || loadCalibration.Transport != binding.Transport || loadCalibration.ActiveArm != binding.ActiveArm || loadCalibration.RoutingSHA256 != binding.RoutingSHA256 || loadCalibration.RouterConfigSHA256 != binding.RouterConfigSHA256 || loadCalibration.ProfileCalibrationSHA256 != binding.ProfileCalibrationSHA256 {
		return fmt.Errorf("model, fleet, transport, arm, routing, or tokenizer calibration differs")
	}
	if loadCalibration.GatewayChatURL != binding.Endpoint || loadCalibration.GatewayChatURL != binding.Driver.GatewayChatURL {
		return fmt.Errorf("load calibration gateway URL differs from the preflight route or condition driver")
	}
	if len(binding.Nodes) == 0 {
		return fmt.Errorf("preflight contains no hardware nodes")
	}
	for _, node := range binding.Nodes {
		if loadCalibration.GPUDriverVersion != node.GPUDriverVersion || loadCalibration.GPUModel != node.GPUModel || loadCalibration.InstanceType != node.InstanceType {
			return fmt.Errorf("hardware differs on node %q", node.Name)
		}
	}
	if len(loadCalibration.Endpoints) != len(binding.Driver.Endpoints) {
		return fmt.Errorf("calibration endpoint count differs from preflight")
	}
	calibrationEndpoints := make(map[string]conditiondriver.LoadCalibrationEndpoint, len(loadCalibration.Endpoints))
	for _, endpoint := range loadCalibration.Endpoints {
		calibrationEndpoints[endpoint.ID] = endpoint
	}
	for _, endpoint := range binding.Driver.Endpoints {
		calibrationEndpoint, exists := calibrationEndpoints[endpoint.ID]
		if !exists || calibrationEndpoint.PodUID != endpoint.PodUID || calibrationEndpoint.MetricsURL != endpoint.MetricsURL {
			return fmt.Errorf("calibration endpoint %q differs from its exact preflight pod UID/metrics binding", endpoint.ID)
		}
	}
	return nil
}

func verifyPlacementMatrix(current bundle) error {
	profile, groups, err := loadAndVerifyWorkloadMatrix(current)
	if err != nil {
		return fmt.Errorf("verify Z0-B workload matrix: %w", err)
	}
	if len(profile.Arms) != 1 || profile.Arms[0] != evidence.ArmLoadAwareP2P {
		return fmt.Errorf("Z0-B profile must contain only Arm B")
	}
	oracles, err := verifiedOracleRecords(current.root, current.binding)
	if err != nil {
		return err
	}
	recomputed, err := benchmarkanalysis.PlacementGateCells(groups, oracles, int64(current.manifest.Seed), 10_000, 0.95)
	if err != nil {
		return fmt.Errorf("recompute Z0-B evidence from raw bundle: %w", err)
	}
	if !reflect.DeepEqual(recomputed, onlyMetric(current.evidence, gate.PlacementMetric)) {
		return fmt.Errorf("Z0-B gate evidence does not match deterministic recomputation from raw groups and oracle records")
	}
	pairs := uint32(profile.Repetitions * uint32(len(profile.Outputs)) * uint32(len(profile.ArrivalSkewsMS)) * uint32(len(profile.CacheStates)) * uint32(len(profile.EPPReplicas)))
	if pairs < 20 {
		return fmt.Errorf("Z0-B profile provides only %d pairs per placement cell; need 20", pairs)
	}
	type key struct {
		width             uint32
		load              evidence.LoadRegime
		prefix, transport string
	}
	want := make(map[key]uint32)
	for _, width := range profile.Widths {
		for _, load := range profile.LoadRegimes {
			for _, prefix := range profile.Prefixes {
				for _, transport := range profile.Transports {
					want[key{width, load, prefix.ID, transport}] = pairs
				}
			}
		}
	}
	for _, cell := range onlyMetric(current.evidence, gate.PlacementMetric) {
		coordinate := key{cell.FanoutWidth, cell.LoadRegime, cell.PrefixRegime, cell.Transport}
		expected, exists := want[coordinate]
		if !exists {
			return fmt.Errorf("Z0-B evidence contains an unregistered placement cell")
		}
		if cell.Pairs != expected {
			return fmt.Errorf("Z0-B placement cell has %d pairs, want %d", cell.Pairs, expected)
		}
		delete(want, coordinate)
	}
	if len(want) != 0 {
		return fmt.Errorf("Z0-B evidence is missing %d preregistered placement cells", len(want))
	}
	return nil
}

type workloadCoordinate struct {
	arm               evidence.Arm
	width             uint32
	prefixSourceCount uint32
	ownerRotation     uint32
	cell              evidence.BenchmarkCell
}

func loadAndVerifyWorkloadMatrix(current bundle) (bench.Profile, []evidence.GroupResult, error) {
	profile, err := bench.LoadProfile(filepath.Join(current.root, "benchmark-profile.yaml"))
	if err != nil {
		return bench.Profile{}, nil, err
	}
	groups, err := jsonl.Read[evidence.GroupResult](filepath.Join(current.root, "groups.jsonl"))
	if err != nil {
		return bench.Profile{}, nil, err
	}
	if err := verifyWorkloadMatrix(profile, groups, current.manifest.RunID); err != nil {
		return bench.Profile{}, nil, err
	}
	return profile, groups, nil
}

// verifyWorkloadMatrix binds every raw group to one exact expanded profile
// coordinate. Aggregate gate-cell counts cannot detect a duplicated coordinate
// that replaces a different output, skew, cache, replica, or repetition.
func verifyWorkloadMatrix(profile bench.Profile, groups []evidence.GroupResult, runID string) error {
	requests, err := bench.ExpandProfile(profile)
	if err != nil {
		return err
	}
	if len(groups) != len(requests) {
		return fmt.Errorf("raw group count is %d, want complete profile count %d", len(groups), len(requests))
	}
	want := make(map[workloadCoordinate]uint32, len(requests))
	for _, request := range requests {
		coordinate := workloadCoordinate{arm: request.Arm, width: uint32(len(request.Suffixes)), prefixSourceCount: request.PrefixSourceCount, ownerRotation: request.OwnerRotation, cell: request.Cell}
		want[coordinate]++
	}
	seenIDs := make(map[string]struct{}, len(groups))
	for index, group := range groups {
		if group.RunID != runID {
			return fmt.Errorf("raw group %d run ID %q does not match manifest run %q", index+1, group.RunID, runID)
		}
		identity := group.RunID + "\x00" + group.GroupID
		if _, exists := seenIDs[identity]; exists {
			return fmt.Errorf("raw group %d duplicates run/group identity", index+1)
		}
		seenIDs[identity] = struct{}{}
		if group.Condition == nil {
			return fmt.Errorf("raw group %d lacks the condition attestation required to bind owner rotation", index+1)
		}
		coordinate := workloadCoordinate{arm: group.Arm, width: group.FanoutWidth, prefixSourceCount: group.Condition.PrefixSourceCount, ownerRotation: group.Condition.OwnerRotation, cell: group.Cell}
		remaining := want[coordinate]
		if remaining == 0 {
			return fmt.Errorf("raw group %d has a duplicate or unregistered workload coordinate", index+1)
		}
		if remaining == 1 {
			delete(want, coordinate)
		} else {
			want[coordinate] = remaining - 1
		}
	}
	if len(want) != 0 {
		return fmt.Errorf("raw groups are missing %d exact profile coordinates", len(want))
	}
	return nil
}

func verifiedOracleRecords(root string, binding zeroprobe.PreflightBinding) ([]replay.OracleRecord, error) {
	retained, err := jsonl.Read[replay.OracleRecord](filepath.Join(root, "oracle.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("read retained oracle records: %w", err)
	}
	temporary, err := os.MkdirTemp("", "velaserve-gate-oracle-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(temporary)
	placements, err := verifiedPlacements(root, binding)
	if err != nil {
		return nil, err
	}
	groups, err := jsonl.Read[evidence.GroupResult](filepath.Join(root, "groups.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("read groups for cache-index convergence: %w", err)
	}
	if err := verifyPlacementSnapshotConditions(groups, placements); err != nil {
		return nil, err
	}
	placementsPath := filepath.Join(temporary, "placements.jsonl")
	if err := writePlacementEvents(placementsPath, placements); err != nil {
		return nil, err
	}
	output := filepath.Join(temporary, "oracle.jsonl")
	if err := replay.ReplayFile(replay.FileOptions{
		GroupsPath: filepath.Join(root, "groups.jsonl"), PlacementsPath: placementsPath, CalibrationPath: filepath.Join(root, "oracle-calibration.yaml"), OutputPath: output,
	}); err != nil {
		return nil, fmt.Errorf("regenerate oracle from raw groups, Envoy, EPP, and calibration: %w", err)
	}
	regenerated, err := jsonl.Read[replay.OracleRecord](output)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(regenerated, retained) {
		return nil, fmt.Errorf("retained oracle does not match raw groups, Envoy, EPP, and calibration")
	}
	return regenerated, nil
}

func verifyPlacementSnapshotConditions(groups []evidence.GroupResult, placements []evidence.PlacementEvent) error {
	groupsByID := make(map[string]evidence.GroupResult, len(groups))
	for _, group := range groups {
		groupsByID[group.RunID+"\x00"+group.GroupID] = group
	}
	for index, placement := range placements {
		group, exists := groupsByID[placement.RunID+"\x00"+placement.GroupID]
		if !exists || group.Condition == nil {
			return fmt.Errorf("placement %d lacks its condition-attested group", index+1)
		}
		local := make(map[string]struct{})
		sources := make(map[string]struct{})
		for _, endpoint := range placement.Snapshot.Endpoints {
			if endpoint.LocalPrefixTokens >= group.Cell.PrefixTokens {
				local[endpoint.Ref.ID] = struct{}{}
			}
			if endpoint.SourcePrefixTokens >= group.Cell.PrefixTokens {
				sources[endpoint.Ref.ID] = struct{}{}
			}
		}
		if !sameStringSet(local, group.Condition.ObservedState.CachedEndpointIDs) {
			return fmt.Errorf("placement %d EPP local-prefix index does not match the condition-attested cache set", index+1)
		}
		if group.Condition.PrefixSourceCount > 0 && !sameStringSet(sources, group.Condition.ObservedState.PrefixSourceEndpointIDs) {
			return fmt.Errorf("placement %d EPP source-prefix index does not match the condition-attested source set", index+1)
		}
	}
	return nil
}

func sameStringSet(observed map[string]struct{}, expected []string) bool {
	if len(observed) != len(expected) {
		return false
	}
	for _, value := range expected {
		if _, exists := observed[value]; !exists {
			return false
		}
	}
	return true
}

func verifiedSourcePressure(root string, binding zeroprobe.PreflightBinding) ([]placementrecorder.SourcePressureObservation, error) {
	retained, err := jsonl.Read[placementrecorder.SourcePressureObservation](filepath.Join(root, "source-pressure.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("read retained source-pressure records: %w", err)
	}
	temporary, err := os.MkdirTemp("", "velaserve-gate-source-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(temporary)
	placements, err := verifiedPlacements(root, binding)
	if err != nil {
		return nil, err
	}
	placementsPath := filepath.Join(temporary, "placements.jsonl")
	if err := writePlacementEvents(placementsPath, placements); err != nil {
		return nil, err
	}
	normalizedTransfers := filepath.Join(temporary, "p2p-transfers.jsonl")
	regeneratedRuntimePath := filepath.Join(temporary, "vllm-runtime.jsonl")
	if _, err := placementrecorder.NormalizePinnedVLLMLog(filepath.Join(root, "vllm-raw.log"), regeneratedRuntimePath); err != nil {
		return nil, fmt.Errorf("regenerate normalized vLLM runtime from raw streams: %w", err)
	}
	retainedRuntime, err := jsonl.Read[placementrecorder.VLLMRuntimeRecord](filepath.Join(root, "vllm-runtime.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("read retained normalized vLLM runtime: %w", err)
	}
	regeneratedRuntime, err := jsonl.Read[placementrecorder.VLLMRuntimeRecord](regeneratedRuntimePath)
	if err != nil {
		return nil, fmt.Errorf("read regenerated normalized vLLM runtime: %w", err)
	}
	if !reflect.DeepEqual(regeneratedRuntime, retainedRuntime) {
		return nil, fmt.Errorf("retained normalized vLLM runtime does not match raw vLLM streams")
	}
	emitters := make([]placementrecorder.RuntimeEmitterBinding, 0, len(binding.Model.Pods))
	for _, pod := range binding.Model.Pods {
		emitters = append(emitters, placementrecorder.RuntimeEmitterBinding{PodName: pod.Name, PodUID: pod.UID})
	}
	if _, err := placementrecorder.NormalizeVLLMRuntime(
		filepath.Join(root, "groups.jsonl"), placementsPath, regeneratedRuntimePath, normalizedTransfers, emitters,
	); err != nil {
		return nil, fmt.Errorf("regenerate normalized P2P transfers from raw groups, placements, and vLLM runtime: %w", err)
	}
	retainedTransfers, err := jsonl.Read[placementrecorder.P2PTransferEvent](filepath.Join(root, "p2p-transfers.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("read retained normalized P2P transfers: %w", err)
	}
	regeneratedTransfers, err := jsonl.Read[placementrecorder.P2PTransferEvent](normalizedTransfers)
	if err != nil {
		return nil, fmt.Errorf("read regenerated normalized P2P transfers: %w", err)
	}
	if !reflect.DeepEqual(regeneratedTransfers, retainedTransfers) {
		return nil, fmt.Errorf("retained normalized P2P transfers do not match raw vLLM runtime")
	}
	output := filepath.Join(temporary, "source-pressure.jsonl")
	if _, err := placementrecorder.CompileSourcePressure(
		filepath.Join(root, "groups.jsonl"), placementsPath, normalizedTransfers, output,
	); err != nil {
		return nil, fmt.Errorf("regenerate source pressure from raw groups, placements, and vLLM runtime: %w", err)
	}
	regenerated, err := jsonl.Read[placementrecorder.SourcePressureObservation](output)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(regenerated, retained) {
		return nil, fmt.Errorf("retained source pressure does not match raw groups, placements, and vLLM runtime")
	}
	return regenerated, nil
}

func verifiedPlacements(root string, binding zeroprobe.PreflightBinding) ([]evidence.PlacementEvent, error) {
	retained, err := jsonl.Read[evidence.PlacementEvent](filepath.Join(root, "placements.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("read retained placements: %w", err)
	}
	retainedUnmatched, err := jsonl.Read[placementrecorder.UnmatchedRecord](filepath.Join(root, "unmatched.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("read retained unmatched records: %w", err)
	}
	retainedReport, err := readJSON[placementrecorder.IngestReport](filepath.Join(root, "ingest-report.json"))
	if err != nil {
		return nil, fmt.Errorf("read retained ingest report: %w", err)
	}
	temporary, err := os.MkdirTemp("", "velaserve-gate-placement-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(temporary)
	regeneratedEPPPath := filepath.Join(temporary, "epp.jsonl")
	if _, err := placementrecorder.NormalizePinnedEPPLog(filepath.Join(root, "epp-raw.log"), regeneratedEPPPath); err != nil {
		return nil, fmt.Errorf("regenerate normalized EPP from raw stream: %w", err)
	}
	regeneratedEnvoyPath := filepath.Join(temporary, "envoy.jsonl")
	if _, err := placementrecorder.NormalizePinnedEnvoyLog(filepath.Join(root, "envoy-raw.log"), regeneratedEnvoyPath); err != nil {
		return nil, fmt.Errorf("regenerate normalized inner-Envoy from raw stream: %w", err)
	}
	retainedEPP, err := jsonl.Read[placementrecorder.EPPRecord](filepath.Join(root, "epp.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("read retained normalized EPP: %w", err)
	}
	regeneratedEPP, err := jsonl.Read[placementrecorder.EPPRecord](regeneratedEPPPath)
	if err != nil {
		return nil, fmt.Errorf("read regenerated normalized EPP: %w", err)
	}
	if !reflect.DeepEqual(regeneratedEPP, retainedEPP) {
		return nil, fmt.Errorf("retained normalized EPP does not match raw EPP stream")
	}
	retainedEnvoy, err := jsonl.Read[placementrecorder.EnvoyRecord](filepath.Join(root, "envoy.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("read retained normalized inner-Envoy: %w", err)
	}
	regeneratedEnvoy, err := jsonl.Read[placementrecorder.EnvoyRecord](regeneratedEnvoyPath)
	if err != nil {
		return nil, fmt.Errorf("read regenerated normalized inner-Envoy: %w", err)
	}
	if !reflect.DeepEqual(regeneratedEnvoy, retainedEnvoy) {
		return nil, fmt.Errorf("retained normalized inner-Envoy does not match raw inner-Envoy stream")
	}
	if err := verifyPlacementEmitterBindings(regeneratedEPP, regeneratedEnvoy, binding); err != nil {
		return nil, err
	}
	if err := copyEvidenceInput(filepath.Join(root, "groups.jsonl"), filepath.Join(temporary, "groups.jsonl")); err != nil {
		return nil, err
	}
	report, err := placementrecorder.Ingest(context.Background(), placementrecorder.IngestOptions{
		ArtifactRoot: temporary, GroupsPath: "groups.jsonl", EnvoyPath: "envoy.jsonl", EPPPath: "epp.jsonl",
	})
	if err != nil {
		return nil, fmt.Errorf("regenerate placements from raw groups, Envoy, and EPP: %w", err)
	}
	if !report.Complete || report.UnmatchedCount != 0 {
		return nil, fmt.Errorf("raw groups, Envoy, and EPP do not regenerate a complete placement set")
	}
	regenerated, err := jsonl.Read[evidence.PlacementEvent](report.PlacementsPath)
	if err != nil {
		return nil, err
	}
	regeneratedUnmatched, err := jsonl.Read[placementrecorder.UnmatchedRecord](report.UnmatchedPath)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(regenerated, retained) {
		return nil, fmt.Errorf("retained placements do not match raw groups, Envoy, and EPP")
	}
	if len(retainedUnmatched) != 0 || !reflect.DeepEqual(regeneratedUnmatched, retainedUnmatched) {
		return nil, fmt.Errorf("retained unmatched records do not match raw groups, Envoy, and EPP")
	}
	if !retainedReport.Complete || retainedReport.PlacementCount != report.PlacementCount || retainedReport.UnmatchedCount != report.UnmatchedCount {
		return nil, fmt.Errorf("retained ingest report does not match raw groups, Envoy, and EPP")
	}
	return regenerated, nil
}

func verifyPlacementEmitterBindings(eppRecords []placementrecorder.EPPRecord, envoyRecords []placementrecorder.EnvoyRecord, binding zeroprobe.PreflightBinding) error {
	eppPods := make(map[string]string, len(binding.EPP.Pods))
	for _, pod := range binding.EPP.Pods {
		eppPods[pod.Name] = pod.UID
	}
	proxyPods := make(map[string]string, len(binding.EPP.ProxyPods))
	for _, pod := range binding.EPP.ProxyPods {
		proxyPods[pod.Name] = pod.UID
	}
	type emitter struct{ name, uid string }
	eppByRequest := make(map[string]emitter, len(eppRecords))
	for index, record := range eppRecords {
		if eppPods[record.EmitterPodName] != record.EmitterPodUID {
			return fmt.Errorf("normalized EPP record %d emitter %q/%q is not an exact preflight EPP pod", index+1, record.EmitterPodName, record.EmitterPodUID)
		}
		if _, duplicate := eppByRequest[record.RequestID]; duplicate {
			return fmt.Errorf("normalized EPP has duplicate request %q", record.RequestID)
		}
		eppByRequest[record.RequestID] = emitter{record.EmitterPodName, record.EmitterPodUID}
	}
	seenEnvoy := make(map[string]struct{}, len(envoyRecords))
	for index, record := range envoyRecords {
		if proxyPods[record.EmitterPodName] != record.EmitterPodUID {
			return fmt.Errorf("normalized inner-Envoy record %d emitter %q/%q is not an exact preflight EPP proxy pod", index+1, record.EmitterPodName, record.EmitterPodUID)
		}
		if _, duplicate := seenEnvoy[record.RequestID]; duplicate {
			return fmt.Errorf("normalized inner-Envoy has duplicate request %q", record.RequestID)
		}
		seenEnvoy[record.RequestID] = struct{}{}
		if eppEmitter, exists := eppByRequest[record.RequestID]; !exists || eppEmitter != (emitter{record.EmitterPodName, record.EmitterPodUID}) {
			return fmt.Errorf("request %q EPP and inner-Envoy emitter pod identities differ", record.RequestID)
		}
	}
	return nil
}

func copyEvidenceInput(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open raw placement input %q: %w", source, err)
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	success := false
	defer func() {
		_ = output.Close()
		if !success {
			_ = os.Remove(destination)
		}
	}()
	if _, err := io.Copy(output, input); err != nil {
		return err
	}
	if err := output.Sync(); err != nil {
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	success = true
	return nil
}

func writePlacementEvents(path string, placements []evidence.PlacementEvent) error {
	if len(placements) == 0 {
		return fmt.Errorf("regenerated placement set is empty")
	}
	for _, placement := range placements {
		if err := jsonl.Append(path, placement); err != nil {
			return err
		}
	}
	return nil
}

func writeCompiled(options Options, preregistrationHash string, entries []BundleLedgerEntry, cells []evidence.GateEvidenceCell) (evidence.GateDecision, error) {
	output, err := filepath.Abs(options.OutputDirectory)
	if err != nil {
		return evidence.GateDecision{}, err
	}
	if _, err := os.Lstat(output); err == nil {
		return evidence.GateDecision{}, fmt.Errorf("output directory already exists")
	} else if !os.IsNotExist(err) {
		return evidence.GateDecision{}, err
	}
	parent := filepath.Dir(output)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return evidence.GateDecision{}, err
	}
	temporary, err := os.MkdirTemp(parent, ".velaserve-gate-")
	if err != nil {
		return evidence.GateDecision{}, err
	}
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(temporary)
		}
	}()
	preregistration, err := os.ReadFile(options.PreregistrationPath)
	if err != nil {
		return evidence.GateDecision{}, err
	}
	if err := os.WriteFile(filepath.Join(temporary, "preregistration.yaml"), preregistration, 0o644); err != nil {
		return evidence.GateDecision{}, err
	}
	ledgerPath := filepath.Join(temporary, "bundle-ledger.jsonl")
	evidencePath := filepath.Join(temporary, "gate-evidence.jsonl")
	if err := writeJSONL(ledgerPath, entries); err != nil {
		return evidence.GateDecision{}, err
	}
	if err := writeJSONL(evidencePath, cells); err != nil {
		return evidence.GateDecision{}, err
	}
	ledgerHash, err := hashPath(ledgerPath)
	if err != nil {
		return evidence.GateDecision{}, err
	}
	evidenceHash, err := hashPath(evidencePath)
	if err != nil {
		return evidence.GateDecision{}, err
	}
	decision, err := gate.Decide(gate.DecisionInput{
		DecisionID: options.DecisionID, PreregistrationSHA256: preregistrationHash,
		ArtifactLedgerSHA256: ledgerHash, EvidenceSHA256: evidenceHash,
		Threshold: 0.10, Confidence: 0.95, EvidenceComplete: true, Evidence: cells, DecidedAt: options.DecidedAt,
	})
	if err != nil {
		return evidence.GateDecision{}, err
	}
	encoded, err := json.MarshalIndent(decision, "", "  ")
	if err != nil {
		return evidence.GateDecision{}, err
	}
	if err := os.WriteFile(filepath.Join(temporary, "decision.json"), append(encoded, '\n'), 0o644); err != nil {
		return evidence.GateDecision{}, err
	}
	privateEncoded, err := os.ReadFile(options.PrivateKeyPath)
	if err != nil {
		return evidence.GateDecision{}, fmt.Errorf("read compiler signing key: %w", err)
	}
	privateBytes, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(string(privateEncoded)))
	if err != nil || len(privateBytes) != ed25519.PrivateKeySize {
		return evidence.GateDecision{}, fmt.Errorf("compiler signing key is not a valid Ed25519 private key")
	}
	signed, err := gate.Sign(decision, ed25519.PrivateKey(privateBytes))
	if err != nil {
		return evidence.GateDecision{}, fmt.Errorf("sign compiler-derived decision: %w", err)
	}
	signedBytes, err := json.MarshalIndent(signed, "", "  ")
	if err != nil {
		return evidence.GateDecision{}, err
	}
	if err := os.WriteFile(filepath.Join(temporary, "signed-decision.json"), append(signedBytes, '\n'), 0o644); err != nil {
		return evidence.GateDecision{}, err
	}
	if err := os.Rename(temporary, output); err != nil {
		return evidence.GateDecision{}, err
	}
	success = true
	return decision, nil
}

func onlyMetric(cells []evidence.GateEvidenceCell, metric string) []evidence.GateEvidenceCell {
	result := make([]evidence.GateEvidenceCell, 0, len(cells))
	for _, cell := range cells {
		if cell.Metric == metric {
			result = append(result, cell)
		}
	}
	return result
}

func readJSON[T any](path string) (T, error) {
	var value T
	contents, err := os.ReadFile(path)
	if err != nil {
		return value, err
	}
	if err := json.Unmarshal(contents, &value); err != nil {
		return value, err
	}
	return value, nil
}

func writeJSONL[T any](path string, values []T) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	for _, value := range values {
		if err := encoder.Encode(value); err != nil {
			_ = file.Close()
			return err
		}
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func hashPath(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

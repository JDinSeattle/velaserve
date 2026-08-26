package gatecompiler

import (
	"crypto/sha256"
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
}

type BundleLedgerEntry struct {
	SchemaVersion        string          `json:"schema_version"`
	RunID                string          `json:"run_id"`
	Phase                zeroprobe.Phase `json:"phase"`
	PrefixSourceCount    uint32          `json:"prefix_source_count,omitempty"`
	ArtifactLedgerSHA256 string          `json:"artifact_ledger_sha256"`
	GateEvidenceSHA256   string          `json:"gate_evidence_sha256"`
}

type bundle struct {
	root        string
	manifest    zeroprobe.Manifest
	report      zeroprobe.AnalysisReport
	binding     zeroprobe.PreflightBinding
	ledgerHash  string
	evidence    []evidence.GateEvidenceCell
	sourceCount uint32
}

func Compile(options Options) (evidence.GateDecision, error) {
	if len(options.BundleRoots) == 0 {
		return evidence.GateDecision{}, fmt.Errorf("at least one verified artifact bundle is required")
	}
	if strings.TrimSpace(options.DecisionID) == "" {
		return evidence.GateDecision{}, fmt.Errorf("decision ID is required")
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
	sourceBundles := make(map[uint32]bundle)
	for index := range bundles {
		current := bundles[index]
		switch current.manifest.Phase {
		case zeroprobe.PhaseZ0B:
			if placementBundle != nil {
				return evidence.GateDecision{}, fmt.Errorf("exactly one Z0-B bundle is allowed")
			}
			placementBundle = &bundles[index]
		case zeroprobe.PhaseZ0C:
			if _, exists := sourceBundles[current.sourceCount]; exists {
				return evidence.GateDecision{}, fmt.Errorf("duplicate Z0-C source-count %d bundle", current.sourceCount)
			}
			sourceBundles[current.sourceCount] = current
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
		if len(sourceBundles) != 0 {
			return evidence.GateDecision{}, fmt.Errorf("Z0-C bundles are not eligible after the Z0-B placement gate passes")
		}
	} else {
		runs := make([]benchmarkanalysis.SourcePressureRun, 0, 3)
		for _, count := range []uint32{1, 2, 4} {
			current, exists := sourceBundles[count]
			if !exists {
				return evidence.GateDecision{}, fmt.Errorf("Z0-B did not pass; complete Z0-C bundles for source counts 1, 2, and 4 are required")
			}
			groups, err := jsonl.Read[evidence.GroupResult](filepath.Join(current.root, "groups.jsonl"))
			if err != nil {
				return evidence.GateDecision{}, err
			}
			observations, err := jsonl.Read[placementrecorder.SourcePressureObservation](filepath.Join(current.root, "source-pressure.jsonl"))
			if err != nil {
				return evidence.GateDecision{}, err
			}
			runs = append(runs, benchmarkanalysis.SourcePressureRun{CandidateSourceCount: count, Groups: groups, Observations: observations})
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
			PrefixSourceCount: current.sourceCount, ArtifactLedgerSHA256: current.ledgerHash, GateEvidenceSHA256: current.report.EvidenceSHA256,
		})
	}
	sort.Slice(ledgerEntries, func(i, j int) bool {
		if ledgerEntries[i].Phase != ledgerEntries[j].Phase {
			return ledgerEntries[i].Phase < ledgerEntries[j].Phase
		}
		if ledgerEntries[i].PrefixSourceCount != ledgerEntries[j].PrefixSourceCount {
			return ledgerEntries[i].PrefixSourceCount < ledgerEntries[j].PrefixSourceCount
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
	ledgerHash, err := hashPath(filepath.Join(root, artifacts.LedgerName))
	if err != nil {
		return bundle{}, err
	}
	cells, err := jsonl.Read[evidence.GateEvidenceCell](filepath.Join(root, "gate-evidence.jsonl"))
	if err != nil {
		return bundle{}, err
	}
	loaded := bundle{root: root, manifest: manifest, report: report, binding: binding, ledgerHash: ledgerHash, evidence: cells}
	if manifest.Phase == zeroprobe.PhaseZ0C {
		condition, err := readJSON[zeroprobe.ExperimentCondition](filepath.Join(root, "condition.json"))
		if err != nil {
			return bundle{}, err
		}
		loaded.sourceCount = condition.PrefixSourceCount
	}
	groups, err := jsonl.Read[evidence.GroupResult](filepath.Join(root, "groups.jsonl"))
	if err != nil {
		return bundle{}, err
	}
	controllerRevision := ""
	for index, group := range groups {
		if group.Condition == nil {
			return bundle{}, fmt.Errorf("run %q real-GPU group %d lacks condition attestation", manifest.RunID, index+1)
		}
		if group.Condition.PrefixSourceCount != loaded.sourceCount {
			return bundle{}, fmt.Errorf("run %q group %d source-count attestation disagrees with its phase condition", manifest.RunID, index+1)
		}
		if controllerRevision == "" {
			controllerRevision = group.Condition.ControllerRevision
		}
		if group.Condition.ControllerRevision != controllerRevision {
			return bundle{}, fmt.Errorf("run %q mixes condition-controller revisions", manifest.RunID)
		}
	}
	return loaded, nil
}

func verifyPlacementMatrix(current bundle) error {
	profile, err := bench.LoadProfile(filepath.Join(current.root, "benchmark-profile.yaml"))
	if err != nil {
		return err
	}
	if len(profile.Arms) != 1 || profile.Arms[0] != evidence.ArmLoadAwareP2P {
		return fmt.Errorf("Z0-B profile must contain only Arm B")
	}
	groups, err := jsonl.Read[evidence.GroupResult](filepath.Join(current.root, "groups.jsonl"))
	if err != nil {
		return err
	}
	oracles, err := jsonl.Read[replay.OracleRecord](filepath.Join(current.root, "oracle.jsonl"))
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

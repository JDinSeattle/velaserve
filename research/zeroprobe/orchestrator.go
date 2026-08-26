package zeroprobe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	benchmarkanalysis "github.com/JDinSeattle/velaserve/benchmarks/analysis"
	"github.com/JDinSeattle/velaserve/internal/artifacts"
	"github.com/JDinSeattle/velaserve/internal/bench"
	"github.com/JDinSeattle/velaserve/internal/evidence"
	"github.com/JDinSeattle/velaserve/internal/jsonl"
	velametrics "github.com/JDinSeattle/velaserve/internal/metrics"
	velatrace "github.com/JDinSeattle/velaserve/internal/trace"
	gate "github.com/JDinSeattle/velaserve/research/gate-decision"
	replay "github.com/JDinSeattle/velaserve/research/oracle-replay"
	placementrecorder "github.com/JDinSeattle/velaserve/research/placement-recorder"
	"github.com/prometheus/client_golang/prometheus"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

const RunCompletionSchemaVersion = "velaserve.run-completion/v1"

type RunOptions struct {
	PrepareOptions
	BenchmarkProfilePath        string
	Endpoint                    string
	Limit                       uint64
	MaxEventBytes               int
	SimulatorMode               bool
	PrefixSourceCount           uint32
	ConditionControllerEndpoint string
	RequireConditionAttestation bool
	PreflightBindingPath        string
}

const ConditionSchemaVersion = "velaserve.experiment-condition/v1"

type ExperimentCondition struct {
	SchemaVersion     string `json:"schema_version"`
	Phase             Phase  `json:"phase"`
	PrefixSourceCount uint32 `json:"prefix_source_count"`
}

type RunCompletion struct {
	SchemaVersion          string    `json:"schema_version"`
	RunID                  string    `json:"run_id"`
	BenchmarkProfileSHA256 string    `json:"benchmark_profile_sha256"`
	ExpectedGroups         uint64    `json:"expected_groups"`
	SelectedGroups         uint64    `json:"selected_groups"`
	PersistedGroups        uint64    `json:"persisted_groups"`
	FailedGroups           uint64    `json:"failed_groups"`
	CancelledGroups        uint64    `json:"cancelled_groups"`
	Complete               bool      `json:"complete"`
	Failure                string    `json:"failure,omitempty"`
	CompletedAt            time.Time `json:"completed_at"`
}

type AnalyzeOptions struct {
	ArtifactRoot    string
	CalibrationPath string
	EvidenceScope   string
}

const AnalysisReportSchemaVersion = "velaserve.analysis-report/v1"

type AnalysisReport struct {
	SchemaVersion    string                `json:"schema_version"`
	EvidenceScope    string                `json:"evidence_scope"`
	EvidenceSHA256   string                `json:"evidence_sha256"`
	EvidenceCells    uint32                `json:"evidence_cells"`
	EvidenceComplete bool                  `json:"evidence_complete"`
	GatePreview      evidence.GateDecision `json:"gate_preview"`
	Signed           bool                  `json:"signed"`
}

func Run(ctx context.Context, options RunOptions) (RunCompletion, error) {
	if ctx == nil {
		return RunCompletion{}, fmt.Errorf("context is required")
	}
	if options.Phase == PhaseZ0C {
		if options.PrefixSourceCount != 1 && options.PrefixSourceCount != 2 && options.PrefixSourceCount != 4 {
			return RunCompletion{}, fmt.Errorf("Z0-C prefix source count must be 1, 2, or 4")
		}
	} else if options.PrefixSourceCount != 0 {
		return RunCompletion{}, fmt.Errorf("prefix source count is valid only for Z0-C")
	}
	profile, err := bench.LoadProfile(options.BenchmarkProfilePath)
	if err != nil {
		return RunCompletion{}, err
	}
	if options.Phase == PhaseZ0C && (len(profile.CacheStates) != 1 || profile.CacheStates[0] != "distributed-warm") {
		return RunCompletion{}, fmt.Errorf("Z0-C benchmark profile must use only distributed-warm cache state")
	}
	var preflightBinding *PreflightBinding
	if strings.TrimSpace(options.PreflightBindingPath) != "" {
		loaded, err := LoadPreflightBinding(options.PreflightBindingPath)
		if err != nil {
			return RunCompletion{}, fmt.Errorf("load preflight binding: %w", err)
		}
		if err := loaded.ValidateProfile(profile); err != nil {
			return RunCompletion{}, err
		}
		preregistrationHash, err := sha256File(options.PreregistrationPath)
		if err != nil || preregistrationHash != loaded.PreregistrationSHA256 {
			return RunCompletion{}, fmt.Errorf("preflight binding preregistration hash does not match the run input")
		}
		preflightBinding = &loaded
	} else if options.RequireConditionAttestation {
		return RunCompletion{}, fmt.Errorf("real condition-attested runs require a preflight binding")
	}
	requests, err := bench.ExpandProfile(profile)
	if err != nil {
		return RunCompletion{}, err
	}
	manifest, err := Prepare(options.PrepareOptions)
	if err != nil {
		return RunCompletion{}, err
	}
	if preflightBinding != nil {
		if preflightBinding.PreregistrationSHA256 != manifest.PreregistrationSHA256 {
			return RunCompletion{}, fmt.Errorf("preflight binding preregistration hash does not match the run manifest")
		}
		encoded, err := preflightBinding.Marshal()
		if err != nil {
			return RunCompletion{}, err
		}
		if err := writeExclusive(filepath.Join(options.ArtifactRoot, "preflight-binding.json"), encoded); err != nil {
			return RunCompletion{}, err
		}
		if _, err := artifacts.Record(options.ArtifactRoot, "preflight-binding.json"); err != nil {
			return RunCompletion{}, err
		}
	}
	if options.Phase == PhaseZ0C {
		condition := ExperimentCondition{SchemaVersion: ConditionSchemaVersion, Phase: PhaseZ0C, PrefixSourceCount: options.PrefixSourceCount}
		encoded, err := json.MarshalIndent(condition, "", "  ")
		if err != nil {
			return RunCompletion{}, err
		}
		if err := writeExclusive(filepath.Join(options.ArtifactRoot, "condition.json"), append(encoded, '\n')); err != nil {
			return RunCompletion{}, err
		}
		if _, err := artifacts.Record(options.ArtifactRoot, "condition.json"); err != nil {
			return RunCompletion{}, err
		}
	}
	if profile.Seed != manifest.Seed {
		return RunCompletion{}, fmt.Errorf("benchmark seed %d does not match preregistration seed %d", profile.Seed, manifest.Seed)
	}
	root, err := secureExistingRoot(options.ArtifactRoot)
	if err != nil {
		return RunCompletion{}, err
	}
	profileContents, err := readBoundedFile(options.BenchmarkProfilePath, maxPreregistrationSize)
	if err != nil {
		return RunCompletion{}, fmt.Errorf("read benchmark profile: %w", err)
	}
	profileHash := sha256.Sum256(profileContents)
	if err := writeExclusive(filepath.Join(root, "benchmark-profile.yaml"), profileContents); err != nil {
		return RunCompletion{}, err
	}
	if _, err := artifacts.Record(root, "benchmark-profile.yaml"); err != nil {
		return RunCompletion{}, err
	}
	registry := prometheus.NewRegistry()
	runtimeMetrics, err := velametrics.New(registry)
	if err != nil {
		return RunCompletion{}, err
	}
	traceExporter, err := velatrace.NewJSONLExporter(filepath.Join(root, "traces.jsonl"))
	if err != nil {
		return RunCompletion{}, err
	}
	traceProvider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(traceExporter))
	traceRecorder := velatrace.New(traceProvider.Tracer("github.com/JDinSeattle/velaserve/zeroprobe"), velatrace.Limits{MaxAttributeBytes: 256})

	expected := uint64(len(requests))
	if options.Limit > 0 && options.Limit < uint64(len(requests)) {
		requests = requests[:options.Limit]
	}
	completion := RunCompletion{
		SchemaVersion:          RunCompletionSchemaVersion,
		RunID:                  manifest.RunID,
		BenchmarkProfileSHA256: hex.EncodeToString(profileHash[:]),
		ExpectedGroups:         expected,
		SelectedGroups:         uint64(len(requests)),
	}
	groupsPath := filepath.Join(root, "groups.jsonl")
	client := bench.Client{
		Endpoint: options.Endpoint, MaxEventBytes: options.MaxEventBytes, SimulatorMode: options.SimulatorMode,
		ConditionControllerEndpoint: options.ConditionControllerEndpoint,
		RequireConditionAttestation: options.RequireConditionAttestation,
		PrefixSourceCount:           options.PrefixSourceCount,
	}
	for _, request := range requests {
		request.RunID = manifest.RunID
		result, runErr := client.RunGroup(ctx, request)
		if runErr != nil {
			completion.Failure = runErr.Error()
			break
		}
		if appendErr := jsonl.Append(groupsPath, result); appendErr != nil {
			completion.Failure = appendErr.Error()
			break
		}
		runtimeMetrics.ObserveGroup(result)
		requestIDs := make([]string, 0, len(result.Children))
		for _, child := range result.Children {
			requestIDs = append(requestIDs, child.RequestID)
		}
		traceRecorder.RecordLifecycle(ctx, velatrace.GroupTrace{RunID: result.RunID, GroupID: result.GroupID, RequestIDs: requestIDs})
		completion.PersistedGroups++
		switch result.Outcome {
		case evidence.OutcomeFailure:
			completion.FailedGroups++
		case evidence.OutcomeCancelled:
			completion.CancelledGroups++
		}
		if ctx.Err() != nil {
			completion.Failure = ctx.Err().Error()
			break
		}
	}
	if completion.Failure == "" && completion.SelectedGroups != completion.ExpectedGroups {
		completion.Failure = "partial smoke limit selected"
	}
	if completion.Failure == "" && (completion.FailedGroups > 0 || completion.CancelledGroups > 0) {
		completion.Failure = fmt.Sprintf("%d failed groups and %d cancelled groups", completion.FailedGroups, completion.CancelledGroups)
	}
	completion.Complete = completion.Failure == "" && completion.PersistedGroups == completion.SelectedGroups
	runtimeMetrics.SetArtifactComplete(completion.Complete)
	completion.CompletedAt = time.Now().UTC()
	if err := prometheus.WriteToTextfile(filepath.Join(root, "metrics.prom"), registry); err != nil {
		return completion, fmt.Errorf("write runtime metrics: %w", err)
	}
	if err := traceProvider.Shutdown(context.Background()); err != nil {
		return completion, fmt.Errorf("close runtime traces: %w", err)
	}
	for _, relative := range []string{"metrics.prom", "traces.jsonl"} {
		if _, err := artifacts.Record(root, relative); err != nil {
			return completion, err
		}
	}
	if _, err := os.Stat(groupsPath); err == nil {
		if _, err := artifacts.Record(root, "groups.jsonl"); err != nil {
			return completion, err
		}
	} else if !os.IsNotExist(err) {
		return completion, err
	}
	encoded, err := json.MarshalIndent(completion, "", "  ")
	if err != nil {
		return completion, err
	}
	encoded = append(encoded, '\n')
	if err := writeExclusive(filepath.Join(root, "run-completion.json"), encoded); err != nil {
		return completion, err
	}
	if _, err := artifacts.Record(root, "run-completion.json"); err != nil {
		return completion, err
	}
	if !completion.Complete {
		return completion, fmt.Errorf("run incomplete after %d/%d persisted groups: %s", completion.PersistedGroups, completion.ExpectedGroups, completion.Failure)
	}
	return completion, nil
}

func Ingest(ctx context.Context, root string) (placementrecorder.IngestReport, error) {
	report, err := placementrecorder.Ingest(ctx, placementrecorder.IngestOptions{
		ArtifactRoot: root,
		GroupsPath:   "groups.jsonl",
		EnvoyPath:    "envoy.jsonl",
		EPPPath:      "epp.jsonl",
	})
	if err != nil {
		return report, err
	}
	resolved, err := secureExistingRoot(root)
	if err != nil {
		return report, err
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return report, err
	}
	encoded = append(encoded, '\n')
	if err := writeExclusive(filepath.Join(resolved, "ingest-report.json"), encoded); err != nil {
		return report, err
	}
	for _, relative := range []string{"envoy.jsonl", "epp.jsonl", "placements.jsonl", "unmatched.jsonl", "ingest-report.json"} {
		if _, err := artifacts.Record(resolved, relative); err != nil {
			return report, fmt.Errorf("record %s: %w", relative, err)
		}
	}
	if sourcePath := filepath.Join(resolved, "source-pressure.jsonl"); fileExists(sourcePath) {
		transferPath := filepath.Join(resolved, "p2p-transfers.jsonl")
		if err := placementrecorder.ValidateP2PTransferFile(transferPath); err != nil {
			return report, fmt.Errorf("validate raw P2P transfer telemetry: %w", err)
		}
		if err := validateSourceFile(sourcePath); err != nil {
			return report, err
		}
		for _, relative := range []string{"p2p-transfers.jsonl", "source-pressure.jsonl"} {
			if _, err := artifacts.Record(resolved, relative); err != nil {
				return report, err
			}
		}
	}
	return report, nil
}

func Analyze(options AnalyzeOptions) error {
	if options.EvidenceScope != "simulation_only" && options.EvidenceScope != "real_gpu" {
		return fmt.Errorf("evidence scope must be simulation_only or real_gpu")
	}
	root, err := secureExistingRoot(options.ArtifactRoot)
	if err != nil {
		return err
	}
	var realBinding *PreflightBinding
	if options.EvidenceScope == "real_gpu" {
		binding, err := validateRealGPUPreflight(root)
		if err != nil {
			return err
		}
		realBinding = &binding
	}
	calibration, err := readBoundedFile(options.CalibrationPath, maxPreregistrationSize)
	if err != nil {
		return fmt.Errorf("read calibration: %w", err)
	}
	if realBinding != nil {
		digest := sha256.Sum256(calibration)
		if hex.EncodeToString(digest[:]) != realBinding.CalibrationSHA256 {
			return fmt.Errorf("real-GPU calibration does not match the preflight binding")
		}
	}
	calibrationPath := filepath.Join(root, "oracle-calibration.yaml")
	if err := writeExclusive(calibrationPath, calibration); err != nil {
		return err
	}
	if err := replay.ReplayFile(replay.FileOptions{
		PlacementsPath:  filepath.Join(root, "placements.jsonl"),
		CalibrationPath: calibrationPath,
		OutputPath:      filepath.Join(root, "oracle.jsonl"),
	}); err != nil {
		return err
	}
	for _, relative := range []string{"oracle-calibration.yaml", "oracle.jsonl"} {
		if _, err := artifacts.Record(root, relative); err != nil {
			return err
		}
	}
	manifestBytes, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		return err
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return err
	}
	groups, err := jsonl.Read[evidence.GroupResult](filepath.Join(root, "groups.jsonl"))
	if err != nil {
		return fmt.Errorf("read groups for gate analysis: %w", err)
	}
	var registeredSourceCount uint32
	if manifest.Phase == PhaseZ0C {
		conditionBytes, err := os.ReadFile(filepath.Join(root, "condition.json"))
		if err != nil {
			return fmt.Errorf("read Z0-C condition: %w", err)
		}
		var condition ExperimentCondition
		if err := json.Unmarshal(conditionBytes, &condition); err != nil {
			return fmt.Errorf("decode Z0-C condition: %w", err)
		}
		registeredSourceCount = condition.PrefixSourceCount
	}
	if options.EvidenceScope == "real_gpu" {
		for index, group := range groups {
			if group.Condition == nil {
				return fmt.Errorf("real-GPU group %d lacks a condition-controller attestation", index+1)
			}
			if manifest.Phase == PhaseZ0C && group.Condition.PrefixSourceCount != registeredSourceCount {
				return fmt.Errorf("real-GPU Z0-C group %d source-count attestation does not match the run condition", index+1)
			}
			if manifest.Phase != PhaseZ0C && group.Condition.PrefixSourceCount != 0 {
				return fmt.Errorf("real-GPU non-Z0-C group %d unexpectedly attests a source-count condition", index+1)
			}
		}
	}
	oracles, err := jsonl.Read[replay.OracleRecord](filepath.Join(root, "oracle.jsonl"))
	if err != nil {
		return fmt.Errorf("read oracle records for gate analysis: %w", err)
	}
	cells, err := benchmarkanalysis.PlacementGateCells(groups, oracles, int64(manifest.Seed), 10_000, 0.95)
	if err != nil {
		return fmt.Errorf("compile placement gate cells: %w", err)
	}
	evidencePath := filepath.Join(root, "gate-evidence.jsonl")
	if err := writeExclusive(evidencePath, nil); err != nil {
		return err
	}
	for _, cell := range cells {
		if err := jsonl.Append(evidencePath, cell); err != nil {
			return fmt.Errorf("append gate evidence: %w", err)
		}
	}
	if _, err := artifacts.Record(root, "gate-evidence.jsonl"); err != nil {
		return err
	}
	evidenceHash, err := sha256File(evidencePath)
	if err != nil {
		return err
	}
	ledgerHash, err := sha256File(filepath.Join(root, artifacts.LedgerName))
	if err != nil {
		return err
	}
	preview, err := gate.Decide(gate.DecisionInput{
		DecisionID:            "preview-" + manifest.RunID,
		PreregistrationSHA256: manifest.PreregistrationSHA256,
		ArtifactLedgerSHA256:  ledgerHash,
		EvidenceSHA256:        evidenceHash,
		Threshold:             0.10,
		Confidence:            0.95,
		EvidenceComplete:      false,
		Evidence:              cells,
		DecidedAt:             time.Now().UTC(),
	})
	if err != nil {
		return err
	}
	report := AnalysisReport{
		SchemaVersion:    AnalysisReportSchemaVersion,
		EvidenceScope:    options.EvidenceScope,
		EvidenceSHA256:   evidenceHash,
		EvidenceCells:    uint32(len(cells)),
		EvidenceComplete: false,
		GatePreview:      preview,
		Signed:           false,
	}
	reportBytes, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	reportBytes = append(reportBytes, '\n')
	if err := writeExclusive(filepath.Join(root, "analysis-report.json"), reportBytes); err != nil {
		return err
	}
	if _, err := artifacts.Record(root, "analysis-report.json"); err != nil {
		return err
	}
	return nil
}

func Verify(root string) error {
	resolved, err := secureExistingRoot(root)
	if err != nil {
		return err
	}
	if err := artifacts.Verify(resolved); err != nil {
		return err
	}
	completionBytes, err := os.ReadFile(filepath.Join(resolved, "run-completion.json"))
	if err != nil {
		return fmt.Errorf("read run completion: %w", err)
	}
	var completion RunCompletion
	if err := json.Unmarshal(completionBytes, &completion); err != nil {
		return fmt.Errorf("decode run completion: %w", err)
	}
	if completion.SchemaVersion != RunCompletionSchemaVersion || !completion.Complete || completion.ExpectedGroups != completion.PersistedGroups {
		return fmt.Errorf("run completion is absent, partial, or inconsistent")
	}
	manifestBytes, err := os.ReadFile(filepath.Join(resolved, "manifest.json"))
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return fmt.Errorf("decode manifest: %w", err)
	}
	if manifest.Phase == PhaseZ0C {
		conditionBytes, err := os.ReadFile(filepath.Join(resolved, "condition.json"))
		if err != nil {
			return fmt.Errorf("Z0-C condition.json is required: %w", err)
		}
		var condition ExperimentCondition
		if err := json.Unmarshal(conditionBytes, &condition); err != nil {
			return fmt.Errorf("decode Z0-C condition: %w", err)
		}
		if condition.SchemaVersion != ConditionSchemaVersion || condition.Phase != PhaseZ0C || (condition.PrefixSourceCount != 1 && condition.PrefixSourceCount != 2 && condition.PrefixSourceCount != 4) {
			return fmt.Errorf("Z0-C condition is invalid")
		}
		if err := validateSourceFile(filepath.Join(resolved, "source-pressure.jsonl")); err != nil {
			return fmt.Errorf("Z0-C source-pressure evidence: %w", err)
		}
		if err := placementrecorder.ValidateP2PTransferFile(filepath.Join(resolved, "p2p-transfers.jsonl")); err != nil {
			return fmt.Errorf("Z0-C raw P2P transfer telemetry: %w", err)
		}
	}
	groups, err := jsonl.Read[evidence.GroupResult](filepath.Join(resolved, "groups.jsonl"))
	if err != nil {
		return err
	}
	if uint64(len(groups)) != completion.PersistedGroups {
		return fmt.Errorf("groups count %d does not match run completion %d", len(groups), completion.PersistedGroups)
	}
	expectedChildren := 0
	for index, group := range groups {
		if err := evidence.ValidateGroupResult(group); err != nil {
			return fmt.Errorf("group %d: %w", index+1, err)
		}
		expectedChildren += len(group.Children)
	}
	placements, err := jsonl.Read[evidence.PlacementEvent](filepath.Join(resolved, "placements.jsonl"))
	if err != nil {
		return err
	}
	if len(placements) != expectedChildren {
		return fmt.Errorf("placement count %d does not match retained child count %d", len(placements), expectedChildren)
	}
	for index, placement := range placements {
		if err := evidence.ValidatePlacementEvent(placement); err != nil {
			return fmt.Errorf("placement %d: %w", index+1, err)
		}
	}
	unmatched, err := jsonl.Read[placementrecorder.UnmatchedRecord](filepath.Join(resolved, "unmatched.jsonl"))
	if err != nil {
		return err
	}
	if len(unmatched) != 0 {
		return fmt.Errorf("run is incomplete: %d unmatched recorder entries", len(unmatched))
	}
	if !fileExists(filepath.Join(resolved, "oracle.jsonl")) {
		return fmt.Errorf("oracle.jsonl is required before verification")
	}
	reportBytes, err := os.ReadFile(filepath.Join(resolved, "analysis-report.json"))
	if err != nil {
		return fmt.Errorf("analysis-report.json is required before verification: %w", err)
	}
	var report AnalysisReport
	if err := json.Unmarshal(reportBytes, &report); err != nil {
		return fmt.Errorf("decode analysis report: %w", err)
	}
	if report.SchemaVersion != AnalysisReportSchemaVersion || report.Signed || report.EvidenceComplete || report.GatePreview.Branch != evidence.BranchInsufficientEvidence {
		return fmt.Errorf("analysis report must contain an unsigned insufficient-evidence preview")
	}
	if report.EvidenceScope != "simulation_only" && report.EvidenceScope != "real_gpu" {
		return fmt.Errorf("analysis report has invalid evidence scope %q", report.EvidenceScope)
	}
	if report.EvidenceScope == "real_gpu" {
		binding, err := validateRealGPUPreflight(resolved)
		if err != nil {
			return err
		}
		calibrationHash, err := sha256File(filepath.Join(resolved, "oracle-calibration.yaml"))
		if err != nil || calibrationHash != binding.CalibrationSHA256 {
			return fmt.Errorf("real-GPU oracle calibration does not match the preflight binding")
		}
	}
	evidencePath := filepath.Join(resolved, "gate-evidence.jsonl")
	evidenceHash, err := sha256File(evidencePath)
	if err != nil {
		return fmt.Errorf("hash gate evidence: %w", err)
	}
	if evidenceHash != report.EvidenceSHA256 {
		return fmt.Errorf("gate evidence hash does not match analysis report")
	}
	cells, err := jsonl.Read[evidence.GateEvidenceCell](evidencePath)
	if err != nil {
		return fmt.Errorf("read gate evidence: %w", err)
	}
	if uint32(len(cells)) != report.EvidenceCells {
		return fmt.Errorf("gate evidence count does not match analysis report")
	}
	return nil
}

func validateRealGPUPreflight(root string) (PreflightBinding, error) {
	binding, err := LoadPreflightBinding(filepath.Join(root, "preflight-binding.json"))
	if err != nil {
		return PreflightBinding{}, fmt.Errorf("real-GPU preflight binding: %w", err)
	}
	profile, err := bench.LoadProfile(filepath.Join(root, "benchmark-profile.yaml"))
	if err != nil {
		return PreflightBinding{}, fmt.Errorf("real-GPU bound benchmark profile: %w", err)
	}
	if err := binding.ValidateProfile(profile); err != nil {
		return PreflightBinding{}, err
	}
	groups, err := jsonl.Read[evidence.GroupResult](filepath.Join(root, "groups.jsonl"))
	if err != nil {
		return PreflightBinding{}, fmt.Errorf("real-GPU bound groups: %w", err)
	}
	for index, group := range groups {
		if group.Condition == nil || group.Condition.ControllerRevision != binding.Controller.Revision {
			return PreflightBinding{}, fmt.Errorf("real-GPU group %d is not attested by the bound condition-controller revision", index+1)
		}
	}
	return binding, nil
}

func secureExistingRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("artifact root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve artifact root: %w", err)
	}
	return resolved, nil
}

func validateSourceFile(path string) error {
	records, err := jsonl.Read[placementrecorder.SourcePressureObservation](path)
	if err != nil {
		return err
	}
	for index, record := range records {
		if err := placementrecorder.ValidateSourcePressure(record); err != nil {
			return fmt.Errorf("source-pressure record %d: %w", index+1, err)
		}
	}
	return nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func sha256File(path string) (string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(contents)
	return hex.EncodeToString(hash[:]), nil
}

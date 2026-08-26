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

	"github.com/JDinSeattle/velaserve/internal/artifacts"
	"github.com/JDinSeattle/velaserve/internal/bench"
	"github.com/JDinSeattle/velaserve/internal/evidence"
	"github.com/JDinSeattle/velaserve/internal/jsonl"
	replay "github.com/JDinSeattle/velaserve/research/oracle-replay"
	placementrecorder "github.com/JDinSeattle/velaserve/research/placement-recorder"
)

const RunCompletionSchemaVersion = "velaserve.run-completion/v1"

type RunOptions struct {
	PrepareOptions
	BenchmarkProfilePath string
	Endpoint             string
	Limit                uint64
	MaxEventBytes        int
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
}

func Run(ctx context.Context, options RunOptions) (RunCompletion, error) {
	if ctx == nil {
		return RunCompletion{}, fmt.Errorf("context is required")
	}
	profile, err := bench.LoadProfile(options.BenchmarkProfilePath)
	if err != nil {
		return RunCompletion{}, err
	}
	requests, err := bench.ExpandProfile(profile)
	if err != nil {
		return RunCompletion{}, err
	}
	manifest, err := Prepare(options.PrepareOptions)
	if err != nil {
		return RunCompletion{}, err
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
	client := bench.Client{Endpoint: options.Endpoint, MaxEventBytes: options.MaxEventBytes}
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
	completion.Complete = completion.Failure == "" && completion.PersistedGroups == completion.SelectedGroups
	completion.CompletedAt = time.Now().UTC()
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
		if err := validateSourceFile(sourcePath); err != nil {
			return report, err
		}
		if _, err := artifacts.Record(resolved, "source-pressure.jsonl"); err != nil {
			return report, err
		}
	}
	return report, nil
}

func Analyze(options AnalyzeOptions) error {
	root, err := secureExistingRoot(options.ArtifactRoot)
	if err != nil {
		return err
	}
	calibration, err := readBoundedFile(options.CalibrationPath, maxPreregistrationSize)
	if err != nil {
		return fmt.Errorf("read calibration: %w", err)
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
	return nil
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

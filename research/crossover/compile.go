package crossover

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/JDinSeattle/velaserve/internal/bench"
	placementrecorder "github.com/JDinSeattle/velaserve/research/placement-recorder"
)

const (
	BindingFile      = "calibration-binding.json"
	ObservationsFile = "observations.jsonl"
	RuntimeRawFile   = "vllm-raw.log"
	RuntimeFile      = "vllm-runtime.jsonl"
	EvidenceFile     = "crossover-evidence.json"
)

type CompileOptions struct {
	BindingPath      string
	ObservationsPath string
	RuntimeRawPath   string
	OutputDirectory  string
}

func Compile(options CompileOptions) (bench.CrossoverEvidence, error) {
	for name, value := range map[string]string{"binding": options.BindingPath, "observations": options.ObservationsPath, "raw vLLM runtime": options.RuntimeRawPath, "output directory": options.OutputDirectory} {
		if strings.TrimSpace(value) == "" {
			return bench.CrossoverEvidence{}, fmt.Errorf("%s path is required", name)
		}
	}
	if _, err := os.Lstat(options.OutputDirectory); err == nil {
		return bench.CrossoverEvidence{}, fmt.Errorf("output directory already exists")
	} else if !os.IsNotExist(err) {
		return bench.CrossoverEvidence{}, err
	}
	parent := filepath.Dir(options.OutputDirectory)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return bench.CrossoverEvidence{}, err
	}
	temporary, err := os.MkdirTemp(parent, ".crossover-bundle-")
	if err != nil {
		return bench.CrossoverEvidence{}, err
	}
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(temporary)
		}
	}()
	for source, name := range map[string]string{options.BindingPath: BindingFile, options.ObservationsPath: ObservationsFile, options.RuntimeRawPath: RuntimeRawFile} {
		if err := copyRegular(source, filepath.Join(temporary, name)); err != nil {
			return bench.CrossoverEvidence{}, err
		}
	}
	if _, err := placementrecorder.NormalizePinnedVLLMLog(filepath.Join(temporary, RuntimeRawFile), filepath.Join(temporary, RuntimeFile)); err != nil {
		return bench.CrossoverEvidence{}, fmt.Errorf("normalize raw vLLM calibration stream: %w", err)
	}
	evidence, err := derive(temporary)
	if err != nil {
		return bench.CrossoverEvidence{}, err
	}
	encoded, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return bench.CrossoverEvidence{}, err
	}
	if err := os.WriteFile(filepath.Join(temporary, EvidenceFile), append(encoded, '\n'), 0o600); err != nil {
		return bench.CrossoverEvidence{}, err
	}
	if err := os.Rename(temporary, options.OutputDirectory); err != nil {
		return bench.CrossoverEvidence{}, err
	}
	success = true
	return evidence, nil
}

func VerifyBundle(root string) (bench.CrossoverEvidence, error) {
	for _, name := range []string{BindingFile, ObservationsFile, RuntimeRawFile, RuntimeFile, EvidenceFile} {
		info, err := os.Lstat(filepath.Join(root, name))
		if err != nil || !info.Mode().IsRegular() {
			return bench.CrossoverEvidence{}, fmt.Errorf("crossover bundle artifact %q is missing or not regular", name)
		}
	}
	temporary, err := os.MkdirTemp("", "velaserve-crossover-verify-")
	if err != nil {
		return bench.CrossoverEvidence{}, err
	}
	defer os.RemoveAll(temporary)
	regenerated := filepath.Join(temporary, RuntimeFile)
	if _, err := placementrecorder.NormalizePinnedVLLMLog(filepath.Join(root, RuntimeRawFile), regenerated); err != nil {
		return bench.CrossoverEvidence{}, err
	}
	retainedRuntime, err := os.ReadFile(filepath.Join(root, RuntimeFile))
	if err != nil {
		return bench.CrossoverEvidence{}, err
	}
	regeneratedRuntime, err := os.ReadFile(regenerated)
	if err != nil {
		return bench.CrossoverEvidence{}, err
	}
	if !bytes.Equal(retainedRuntime, regeneratedRuntime) {
		return bench.CrossoverEvidence{}, fmt.Errorf("retained normalized crossover runtime differs from raw vLLM stream")
	}
	derived, err := derive(root)
	if err != nil {
		return bench.CrossoverEvidence{}, err
	}
	retained, _, err := bench.LoadCrossoverEvidenceSource(filepath.Join(root, EvidenceFile))
	if err != nil {
		return bench.CrossoverEvidence{}, err
	}
	if !reflect.DeepEqual(derived, retained) {
		return bench.CrossoverEvidence{}, fmt.Errorf("retained crossover evidence differs from raw observations and runtime")
	}
	return retained, nil
}

func derive(root string) (bench.CrossoverEvidence, error) {
	binding, _, err := LoadBinding(filepath.Join(root, BindingFile))
	if err != nil {
		return bench.CrossoverEvidence{}, err
	}
	observations, err := readObservations(filepath.Join(root, ObservationsFile), binding)
	if err != nil {
		return bench.CrossoverEvidence{}, err
	}
	records, err := placementrecorder.ReadVLLMRuntime(filepath.Join(root, RuntimeFile))
	if err != nil {
		return bench.CrossoverEvidence{}, err
	}
	endpointByID := make(map[string]Endpoint, len(binding.Endpoints))
	for _, endpoint := range binding.Endpoints {
		endpointByID[endpoint.ID] = endpoint
	}
	acquisitions := make(map[string]placementrecorder.VLLMRuntimeRecord)
	transfers := make(map[string][]placementrecorder.VLLMRuntimeRecord)
	requested := make(map[string]struct{}, len(observations))
	for _, observation := range observations {
		requested["chatcmpl-"+observation.RequestID] = struct{}{}
	}
	for index, record := range records {
		if _, relevant := requested[record.EngineRequestID]; !relevant {
			continue
		}
		switch record.SchemaVersion {
		case placementrecorder.VLLMAcquisitionSchemaVersion:
			if _, duplicate := acquisitions[record.EngineRequestID]; duplicate {
				return bench.CrossoverEvidence{}, fmt.Errorf("raw runtime record %d duplicates calibration acquisition", index+1)
			}
			acquisitions[record.EngineRequestID] = record
		case placementrecorder.VLLMP2PTransferSchemaVersion:
			transfers[record.EngineRequestID] = append(transfers[record.EngineRequestID], record)
		}
	}
	trials := make([]bench.CrossoverTrial, 0, len(observations))
	for _, observation := range observations {
		engineID := "chatcmpl-" + observation.RequestID
		acquisition, exists := acquisitions[engineID]
		if !exists || acquisition.EmitterPodName != observation.TargetID || acquisition.EmitterPodUID != observation.TargetPodUID || acquisition.ObservedAt.Before(observation.DispatchedAt) || acquisition.ObservedAt.After(observation.FirstTokenAt) || acquisition.LocalCachedTokens == nil || acquisition.ExternalCachedTokens == nil {
			return bench.CrossoverEvidence{}, fmt.Errorf("observation %q lacks an exact target-bound runtime acquisition", observation.RequestID)
		}
		trial := bench.CrossoverTrial{RunID: observation.RunID, GroupID: "forced-" + observation.Acquisition, RequestID: observation.RequestID, PrefixTokens: observation.PrefixTokens, Acquisition: observation.Acquisition, ObservedAt: acquisition.ObservedAt}
		switch observation.Acquisition {
		case "recompute":
			if acquisition.Source != nil || *acquisition.ExternalCachedTokens != 0 || *acquisition.LocalCachedTokens >= observation.PrefixTokens || len(transfers[engineID]) != 0 {
				return bench.CrossoverEvidence{}, fmt.Errorf("observation %q is not a measured cold recompute", observation.RequestID)
			}
			trial.Seconds = observation.TTFTSeconds
		case "p2p":
			source := endpointByID[observation.SourceID]
			sourceHost, _, err := net.SplitHostPort(source.P2PSourceHostPort)
			if err != nil || acquisition.Source == nil || acquisition.Source.ID != source.ID || acquisition.Source.Model != binding.ModelID || acquisition.SourceHost != sourceHost || acquisition.SourcePort != 7777 || *acquisition.ExternalCachedTokens < observation.PrefixTokens {
				return bench.CrossoverEvidence{}, fmt.Errorf("observation %q runtime source differs from the forced P2P source", observation.RequestID)
			}
			jobs := transfers[engineID]
			if len(jobs) == 0 {
				return bench.CrossoverEvidence{}, fmt.Errorf("observation %q lacks P2P completion telemetry", observation.RequestID)
			}
			var firstSubmitted, lastCompleted time.Time
			for index, job := range jobs {
				if job.EmitterPodName != observation.TargetID || job.EmitterPodUID != observation.TargetPodUID || job.Source == nil || *job.Source != *acquisition.Source || job.TransferBytes == nil || job.TransferSubmittedAt == nil || job.TransferObservedCompletedAt == nil || job.TransferObservedCompletedAt.After(observation.FirstTokenAt) {
					return bench.CrossoverEvidence{}, fmt.Errorf("observation %q P2P job %d is inconsistent", observation.RequestID, index+1)
				}
				trial.TransferBytes += *job.TransferBytes
				if firstSubmitted.IsZero() || job.TransferSubmittedAt.Before(firstSubmitted) {
					firstSubmitted = *job.TransferSubmittedAt
				}
				if lastCompleted.IsZero() || job.TransferObservedCompletedAt.After(lastCompleted) {
					lastCompleted = *job.TransferObservedCompletedAt
				}
			}
			trial.Seconds = lastCompleted.Sub(firstSubmitted).Seconds()
		}
		trials = append(trials, trial)
	}
	sort.Slice(trials, func(i, j int) bool {
		if trials[i].PrefixTokens != trials[j].PrefixTokens {
			return trials[i].PrefixTokens < trials[j].PrefixTokens
		}
		if trials[i].Acquisition != trials[j].Acquisition {
			return trials[i].Acquisition < trials[j].Acquisition
		}
		return trials[i].RequestID < trials[j].RequestID
	})
	samples, err := bench.AggregateCrossoverTrials(trials)
	if err != nil {
		return bench.CrossoverEvidence{}, err
	}
	threshold, err := bench.CrossoverTokens(samples)
	if err != nil {
		return bench.CrossoverEvidence{}, err
	}
	observationsHash, err := fileSHA256(filepath.Join(root, ObservationsFile))
	if err != nil {
		return bench.CrossoverEvidence{}, err
	}
	rawHash, err := fileSHA256(filepath.Join(root, RuntimeRawFile))
	if err != nil {
		return bench.CrossoverEvidence{}, err
	}
	bindingHash, err := fileSHA256(filepath.Join(root, BindingFile))
	if err != nil {
		return bench.CrossoverEvidence{}, err
	}
	evidence := bench.CrossoverEvidence{
		SchemaVersion: bench.CrossoverEvidenceSchemaVersion, ModelID: binding.ModelID, ModelRevision: binding.ModelRevision, Transport: binding.Transport,
		Deployment: binding.Deployment, ObservationsSHA256: observationsHash, RuntimeRawSHA256: rawHash, CalibrationBindingSHA256: bindingHash,
		Trials: trials, Samples: samples, MinCachedTokenDelta: threshold,
	}
	if err := bench.ValidateCrossoverEvidence(evidence); err != nil {
		return bench.CrossoverEvidence{}, err
	}
	return evidence, nil
}

func readObservations(path string, binding Binding) ([]Observation, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 32<<20)
	var observations []Observation
	seen := make(map[string]struct{})
	for scanner.Scan() {
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.DisallowUnknownFields()
		var observation Observation
		if err := decoder.Decode(&observation); err != nil {
			return nil, fmt.Errorf("decode crossover observation %d: %w", len(observations)+1, err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("crossover observation %d has trailing content", len(observations)+1)
		}
		if err := ValidateObservation(observation, binding); err != nil {
			return nil, fmt.Errorf("crossover observation %d: %w", len(observations)+1, err)
		}
		if _, duplicate := seen[observation.RequestID]; duplicate {
			return nil, fmt.Errorf("crossover request ID %q is duplicated", observation.RequestID)
		}
		seen[observation.RequestID] = struct{}{}
		observations = append(observations, observation)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(observations) == 0 {
		return nil, fmt.Errorf("crossover observations are empty")
	}
	return observations, nil
}

func copyRegular(source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("crossover source %q must be a regular file", source)
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return err
	}
	if err := output.Sync(); err != nil {
		_ = output.Close()
		return err
	}
	return output.Close()
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

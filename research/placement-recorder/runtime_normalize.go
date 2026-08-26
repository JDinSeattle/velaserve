package placementrecorder

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"github.com/JDinSeattle/velaserve/internal/evidence"
	"github.com/JDinSeattle/velaserve/internal/jsonl"
)

const (
	VLLMAcquisitionSchemaVersion = "velaserve.vllm-acquisition/v1"
	VLLMP2PTransferSchemaVersion = "velaserve.vllm-p2p-transfer/v1"
)

// VLLMRuntimeRecord is emitted by the pinned vLLM observational patch. The
// acquisition form records the scheduler's cache lookup before execution; the
// transfer form records measured P2P bytes and duration at completion.
type VLLMRuntimeRecord struct {
	SchemaVersion                   string                `json:"schema_version"`
	EngineRequestID                 string                `json:"engine_request_id"`
	EmitterPodName                  string                `json:"emitter_pod_name"`
	EmitterPodUID                   string                `json:"emitter_pod_uid"`
	LocalCachedTokens               *uint64               `json:"local_cached_tokens,omitempty"`
	ExternalCachedTokens            *uint64               `json:"external_cached_tokens,omitempty"`
	Source                          *evidence.EndpointRef `json:"source,omitempty"`
	SourceHost                      string                `json:"source_host,omitempty"`
	SourcePort                      uint32                `json:"source_port,omitempty"`
	TransferBytes                   *uint64               `json:"transfer_bytes,omitempty"`
	TransferSubmittedAt             *time.Time            `json:"transfer_submitted_at,omitempty"`
	TransferObservedCompletedAt     *time.Time            `json:"transfer_observed_completed_at,omitempty"`
	ReportedTransferDurationSeconds *float64              `json:"reported_transfer_duration_seconds,omitempty"`
	ObservedAt                      time.Time             `json:"observed_at"`
}

// RuntimeEmitterBinding is the exact preflight-bound model pod identity that
// may emit vLLM runtime evidence. The log collector adds it outside the pinned
// upstream patch so the patch cannot claim which replica produced a record.
type RuntimeEmitterBinding struct {
	PodName string
	PodUID  string
}

// NormalizeVLLMRuntime joins the patched runtime's engine IDs to the raw
// benchmark and EPP records. It produces the stricter environment-neutral
// P2PTransferEvent contract consumed by source-pressure compilation.
func NormalizeVLLMRuntime(groupsPath, placementsPath, runtimePath, outputPath string, emitterBindings []RuntimeEmitterBinding) (uint64, error) {
	emitterUIDByName := make(map[string]string, len(emitterBindings))
	seenEmitterUIDs := make(map[string]struct{}, len(emitterBindings))
	for index, binding := range emitterBindings {
		if strings.TrimSpace(binding.PodName) == "" || strings.TrimSpace(binding.PodUID) == "" {
			return 0, fmt.Errorf("runtime emitter binding %d is incomplete", index+1)
		}
		if _, exists := emitterUIDByName[binding.PodName]; exists {
			return 0, fmt.Errorf("runtime emitter pod name %q is duplicated", binding.PodName)
		}
		if _, exists := seenEmitterUIDs[binding.PodUID]; exists {
			return 0, fmt.Errorf("runtime emitter pod UID %q is duplicated", binding.PodUID)
		}
		emitterUIDByName[binding.PodName] = binding.PodUID
		seenEmitterUIDs[binding.PodUID] = struct{}{}
	}
	if len(emitterUIDByName) == 0 {
		return 0, fmt.Errorf("runtime emitter bindings are required")
	}
	groups, err := jsonl.Read[evidence.GroupResult](groupsPath)
	if err != nil {
		return 0, err
	}
	benchmarkEngineIDs := make(map[string]struct{})
	for groupIndex, group := range groups {
		if err := evidence.ValidateGroupResult(group); err != nil {
			return 0, fmt.Errorf("group %d: %w", groupIndex+1, err)
		}
		if group.Arm != evidence.ArmLoadAwareP2P || group.Outcome != evidence.OutcomeSuccess || group.Condition == nil || group.Condition.PrefixSourceCount == 0 {
			return 0, fmt.Errorf("group %q is not a successful condition-attested Z0-C Arm-B group", group.GroupID)
		}
		for _, child := range group.Children {
			engineID := "chatcmpl-" + child.RequestID
			if _, exists := benchmarkEngineIDs[engineID]; exists {
				return 0, fmt.Errorf("group %q child %q duplicates a benchmark engine identity", group.GroupID, child.RequestID)
			}
			benchmarkEngineIDs[engineID] = struct{}{}
		}
	}
	placements, err := jsonl.Read[evidence.PlacementEvent](placementsPath)
	if err != nil {
		return 0, err
	}
	placementByKey := make(map[string]evidence.PlacementEvent, len(placements))
	for index, placement := range placements {
		if err := evidence.ValidatePlacementEvent(placement); err != nil {
			return 0, fmt.Errorf("placement event %d: %w", index+1, err)
		}
		key := transferKey(placement.RunID, placement.GroupID, placement.RequestID)
		if _, exists := placementByKey[key]; exists {
			return 0, fmt.Errorf("placement event %d duplicates request identity", index+1)
		}
		placementByKey[key] = placement
	}

	acquisitions := make(map[string]VLLMRuntimeRecord)
	transfers := make(map[string][]VLLMRuntimeRecord)
	records, err := ReadVLLMRuntime(runtimePath)
	if err != nil {
		return 0, err
	}
	seenBenchmarkRecords := make(map[string]struct{})
	for index, record := range records {
		boundUID, bound := emitterUIDByName[record.EmitterPodName]
		if !bound || boundUID != record.EmitterPodUID {
			return 0, fmt.Errorf("runtime record %d emitter is not an exact preflight-bound model pod", index+1)
		}
		// Raw logs span the full run and deliberately retain condition probes and
		// background load. Only exact benchmark engine identities are derivation
		// inputs; ReadVLLMRuntime has already strictly validated every raw line.
		if _, expected := benchmarkEngineIDs[record.EngineRequestID]; !expected {
			continue
		}
		encoded, err := json.Marshal(record)
		if err != nil {
			return 0, fmt.Errorf("encode runtime record %d for duplicate check: %w", index+1, err)
		}
		if _, duplicate := seenBenchmarkRecords[string(encoded)]; duplicate {
			return 0, fmt.Errorf("runtime record %d exactly duplicates a benchmark record", index+1)
		}
		seenBenchmarkRecords[string(encoded)] = struct{}{}
		switch record.SchemaVersion {
		case VLLMAcquisitionSchemaVersion:
			if _, exists := acquisitions[record.EngineRequestID]; exists {
				return 0, fmt.Errorf("runtime record %d duplicates an acquisition decision", index+1)
			}
			acquisitions[record.EngineRequestID] = record
		case VLLMP2PTransferSchemaVersion:
			transfers[record.EngineRequestID] = append(transfers[record.EngineRequestID], record)
		}
	}

	output, err := os.OpenFile(outputPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	success := false
	defer func() {
		_ = output.Close()
		if !success {
			_ = os.Remove(outputPath)
		}
	}()
	encoder := json.NewEncoder(output)
	usedAcquisitions := make(map[string]struct{}, len(acquisitions))
	usedTransfers := make(map[string]struct{}, len(transfers))
	var count uint64
	for groupIndex, group := range groups {
		if err := evidence.ValidateGroupResult(group); err != nil {
			return 0, fmt.Errorf("group %d: %w", groupIndex+1, err)
		}
		if group.Arm != evidence.ArmLoadAwareP2P || group.Outcome != evidence.OutcomeSuccess || group.Condition == nil || group.Condition.PrefixSourceCount == 0 {
			return 0, fmt.Errorf("group %q is not a successful condition-attested Z0-C Arm-B group", group.GroupID)
		}
		for _, child := range group.Children {
			key := transferKey(group.RunID, group.GroupID, child.RequestID)
			placement, exists := placementByKey[key]
			if !exists {
				return 0, fmt.Errorf("group %q child %q lacks placement evidence", group.GroupID, child.RequestID)
			}
			engineID := "chatcmpl-" + child.RequestID
			acquisition, exists := acquisitions[engineID]
			if !exists {
				return 0, fmt.Errorf("group %q child %q lacks a measured vLLM acquisition decision", group.GroupID, child.RequestID)
			}
			usedAcquisitions[engineID] = struct{}{}
			if acquisition.EmitterPodName != placement.Target.ID || acquisition.EmitterPodUID != emitterUIDByName[placement.Target.ID] {
				return 0, fmt.Errorf("group %q child %q vLLM acquisition emitter does not match the EPP-selected target pod", group.GroupID, child.RequestID)
			}
			if child.DispatchedAt == nil || child.FirstTokenAt == nil || acquisition.ObservedAt.Before(*child.DispatchedAt) || acquisition.ObservedAt.After(*child.FirstTokenAt) {
				return 0, fmt.Errorf("group %q child %q acquisition decision is outside dispatch-to-first-token", group.GroupID, child.RequestID)
			}
			if child.CachedTokens == nil || child.PromptTokens == 0 || *child.CachedTokens > child.PromptTokens {
				return 0, fmt.Errorf("group %q child %q lacks prompt cache readback", group.GroupID, child.RequestID)
			}
			if acquisition.LocalCachedTokens == nil || acquisition.ExternalCachedTokens == nil || *acquisition.LocalCachedTokens > math.MaxUint64-*acquisition.ExternalCachedTokens {
				return 0, fmt.Errorf("group %q child %q has incomplete or overflowing runtime cache counts", group.GroupID, child.RequestID)
			}
			totalRuntimeCached := *acquisition.LocalCachedTokens + *acquisition.ExternalCachedTokens
			event := P2PTransferEvent{
				SchemaVersion: P2PTransferSchemaVersion, RunID: group.RunID, GroupID: group.GroupID,
				RequestID: child.RequestID, ObservedAt: acquisition.ObservedAt,
			}
			if placement.SelectedP2PSource != nil {
				if acquisition.Source == nil || *acquisition.Source != placement.SelectedP2PSource.Source || acquisition.SourceHost != placement.SelectedP2PSourceHost || acquisition.SourcePort != placement.SelectedP2PSourcePort || *acquisition.ExternalCachedTokens < group.Cell.PrefixTokens {
					return 0, fmt.Errorf("group %q child %q runtime P2P source or cached-token count disagrees with EPP", group.GroupID, child.RequestID)
				}
				jobs := transfers[engineID]
				if len(jobs) == 0 {
					return 0, fmt.Errorf("group %q child %q lacks measured P2P transfer completion", group.GroupID, child.RequestID)
				}
				usedTransfers[engineID] = struct{}{}
				event.Acquisition = AcquisitionP2P
				source := *acquisition.Source
				event.ChosenSource = &source
				var bytes uint64
				for jobIndex, job := range jobs {
					if job.EmitterPodName != acquisition.EmitterPodName || job.EmitterPodUID != acquisition.EmitterPodUID {
						return 0, fmt.Errorf("group %q child %q P2P job %d emitter does not match the EPP-selected target pod", group.GroupID, child.RequestID, jobIndex+1)
					}
					if job.Source == nil || *job.Source != source || job.SourceHost != acquisition.SourceHost || job.SourcePort != acquisition.SourcePort || job.TransferBytes == nil || bytes > math.MaxUint64-*job.TransferBytes {
						return 0, fmt.Errorf("group %q child %q P2P job %d has inconsistent source or bytes", group.GroupID, child.RequestID, jobIndex+1)
					}
					bytes += *job.TransferBytes
					if event.TransferStartedAt == nil || job.TransferSubmittedAt.Before(*event.TransferStartedAt) {
						event.TransferStartedAt = job.TransferSubmittedAt
					}
					if event.TransferCompletedAt == nil || job.TransferObservedCompletedAt.After(*event.TransferCompletedAt) {
						event.TransferCompletedAt = job.TransferObservedCompletedAt
					}
					if job.ObservedAt.After(event.ObservedAt) {
						event.ObservedAt = job.ObservedAt
					}
				}
				event.TransferBytes = &bytes
			} else if totalRuntimeCached >= group.Cell.PrefixTokens {
				if acquisition.Source != nil || acquisition.SourceHost != "" || acquisition.SourcePort != 0 || len(transfers[engineID]) != 0 {
					return 0, fmt.Errorf("group %q child %q runtime reports a remote source that EPP did not select", group.GroupID, child.RequestID)
				}
				event.Acquisition = AcquisitionLocal
			} else {
				if acquisition.Source != nil || acquisition.SourceHost != "" || acquisition.SourcePort != 0 || len(transfers[engineID]) != 0 {
					return 0, fmt.Errorf("group %q child %q runtime reports a remote source that EPP did not select", group.GroupID, child.RequestID)
				}
				event.Acquisition = AcquisitionRecompute
			}
			if (event.Acquisition == AcquisitionRecompute && *child.CachedTokens >= group.Cell.PrefixTokens) || (event.Acquisition != AcquisitionRecompute && *child.CachedTokens < group.Cell.PrefixTokens) {
				return 0, fmt.Errorf("group %q child %q runtime decision disagrees with response cache readback", group.GroupID, child.RequestID)
			}
			if err := validateP2PTransferEvent(event); err != nil {
				return 0, fmt.Errorf("group %q child %q: %w", group.GroupID, child.RequestID, err)
			}
			if err := encoder.Encode(event); err != nil {
				return 0, err
			}
			count++
		}
	}
	if len(usedAcquisitions) != len(acquisitions) || len(usedTransfers) != len(transfers) {
		return 0, fmt.Errorf("runtime input contains records that do not join one-to-one to benchmark children")
	}
	if count == 0 {
		return 0, fmt.Errorf("runtime input produced no acquisition events")
	}
	if err := output.Sync(); err != nil {
		return 0, err
	}
	if err := output.Close(); err != nil {
		return 0, err
	}
	success = true
	return count, nil
}

// ReadVLLMRuntime strictly decodes a normalized pinned-runtime stream.
func ReadVLLMRuntime(path string) ([]VLLMRuntimeRecord, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), maxRecorderLineBytes)
	var records []VLLMRuntimeRecord
	for scanner.Scan() {
		var record VLLMRuntimeRecord
		if err := decodeStrict(scanner.Bytes(), &record); err != nil {
			return nil, fmt.Errorf("decode vLLM runtime record %d: %w", len(records)+1, err)
		}
		if err := validateVLLMRuntime(record); err != nil {
			return nil, fmt.Errorf("vLLM runtime record %d: %w", len(records)+1, err)
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

// ValidateVLLMRuntimeFile strictly validates every raw observer record while
// leaving benchmark membership to NormalizeVLLMRuntime's group-derived join.
func ValidateVLLMRuntimeFile(path string) error {
	records, err := ReadVLLMRuntime(path)
	if err != nil {
		return err
	}
	if len(records) == 0 {
		return fmt.Errorf("vLLM runtime input is empty")
	}
	return nil
}

func validateVLLMRuntime(record VLLMRuntimeRecord) error {
	if !strings.HasPrefix(record.EngineRequestID, "chatcmpl-") || len(strings.TrimPrefix(record.EngineRequestID, "chatcmpl-")) == 0 || strings.TrimSpace(record.EmitterPodName) == "" || strings.TrimSpace(record.EmitterPodUID) == "" || record.ObservedAt.IsZero() {
		return fmt.Errorf("engine request identity, exact emitter pod identity, and observed time are required")
	}
	sourceSet := record.Source != nil
	hostSet := strings.TrimSpace(record.SourceHost) != ""
	portSet := record.SourcePort != 0
	if sourceSet != hostSet || sourceSet != portSet {
		return fmt.Errorf("source identity, host, and port must be an all-or-none tuple")
	}
	if sourceSet && (strings.TrimSpace(record.Source.ID) == "" || strings.TrimSpace(record.Source.Model) == "") {
		return fmt.Errorf("source identity is incomplete")
	}
	switch record.SchemaVersion {
	case VLLMAcquisitionSchemaVersion:
		if record.LocalCachedTokens == nil || record.ExternalCachedTokens == nil || record.TransferBytes != nil || record.TransferSubmittedAt != nil || record.TransferObservedCompletedAt != nil || record.ReportedTransferDurationSeconds != nil {
			return fmt.Errorf("acquisition record requires cache counts and cannot contain transfer result fields")
		}
		if record.Source != nil && *record.ExternalCachedTokens == 0 {
			return fmt.Errorf("remote acquisition source requires positive external cached tokens")
		}
	case VLLMP2PTransferSchemaVersion:
		if record.Source == nil || strings.TrimSpace(record.SourceHost) == "" || record.SourcePort == 0 || record.TransferBytes == nil || *record.TransferBytes == 0 || record.TransferSubmittedAt == nil || record.TransferObservedCompletedAt == nil || record.ReportedTransferDurationSeconds == nil || *record.ReportedTransferDurationSeconds <= 0 || math.IsNaN(*record.ReportedTransferDurationSeconds) || math.IsInf(*record.ReportedTransferDurationSeconds, 0) || record.TransferSubmittedAt.IsZero() || record.TransferObservedCompletedAt.IsZero() || record.TransferObservedCompletedAt.Before(*record.TransferSubmittedAt) || record.ObservedAt.Before(*record.TransferObservedCompletedAt) || record.TransferObservedCompletedAt.Sub(*record.TransferSubmittedAt).Seconds()+0.001 < *record.ReportedTransferDurationSeconds {
			return fmt.Errorf("P2P transfer record requires source, positive bytes, and valid transfer chronology")
		}
		if record.LocalCachedTokens != nil || record.ExternalCachedTokens != nil {
			return fmt.Errorf("P2P transfer record cannot contain acquisition cache counts")
		}
	default:
		return fmt.Errorf("schema_version is unsupported")
	}
	return nil
}

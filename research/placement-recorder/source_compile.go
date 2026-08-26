package placementrecorder

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/JDinSeattle/velaserve/internal/evidence"
	"github.com/JDinSeattle/velaserve/internal/jsonl"
)

const P2PTransferSchemaVersion = "velaserve.p2p-transfer/v1"

// P2PTransferEvent is the minimal environment-adapter contract for measured
// model-runtime KV acquisition. Candidate-source count and group latency are
// deliberately absent: the compiler derives them from the attested group.
type P2PTransferEvent struct {
	SchemaVersion            string                `json:"schema_version"`
	RunID                    string                `json:"run_id"`
	GroupID                  string                `json:"group_id"`
	RequestID                string                `json:"request_id"`
	Acquisition              Acquisition           `json:"acquisition"`
	ChosenSource             *evidence.EndpointRef `json:"chosen_source,omitempty"`
	TransferBytes            *uint64               `json:"transfer_bytes,omitempty"`
	TransferStartedAt        *time.Time            `json:"transfer_started_at,omitempty"`
	TransferCompletedAt      *time.Time            `json:"transfer_completed_at,omitempty"`
	SourceNICBytesPerSecond  *float64              `json:"source_nic_bytes_per_second,omitempty"`
	SourceCPUTierUtilization *float64              `json:"source_cpu_tier_utilization,omitempty"`
	ObservedAt               time.Time             `json:"observed_at"`
}

// CompileSourcePressure joins raw model-runtime transfer telemetry to every
// attested Arm-B child. It writes a new normalized file only when the join is
// one-to-one and complete.
func CompileSourcePressure(groupsPath, placementsPath, transfersPath, outputPath string) (uint64, error) {
	groups, err := jsonl.Read[evidence.GroupResult](groupsPath)
	if err != nil {
		return 0, err
	}
	events, err := readP2PTransferEvents(transfersPath)
	if err != nil {
		return 0, err
	}
	byRequest := make(map[string]P2PTransferEvent, len(events))
	for index, event := range events {
		key := transferKey(event.RunID, event.GroupID, event.RequestID)
		if _, exists := byRequest[key]; exists {
			return 0, fmt.Errorf("transfer event %d duplicates request identity", index+1)
		}
		byRequest[key] = event
	}
	placements, err := jsonl.Read[evidence.PlacementEvent](placementsPath)
	if err != nil {
		return 0, err
	}
	placementsByRequest := make(map[string]evidence.PlacementEvent, len(placements))
	for index, placement := range placements {
		if err := evidence.ValidatePlacementEvent(placement); err != nil {
			return 0, fmt.Errorf("placement event %d: %w", index+1, err)
		}
		key := transferKey(placement.RunID, placement.GroupID, placement.RequestID)
		if _, exists := placementsByRequest[key]; exists {
			return 0, fmt.Errorf("placement event %d duplicates request identity", index+1)
		}
		placementsByRequest[key] = placement
	}
	pullMeasurements, err := derivePullMeasurements(events)
	if err != nil {
		return 0, err
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
	var count uint64
	used := make(map[string]struct{}, len(events))
	usedPlacements := make(map[string]struct{}, len(placements))
	for index, group := range groups {
		if err := evidence.ValidateGroupResult(group); err != nil {
			return 0, fmt.Errorf("group %d: %w", index+1, err)
		}
		if group.Arm != evidence.ArmLoadAwareP2P || group.Outcome != evidence.OutcomeSuccess || group.Condition == nil {
			return 0, fmt.Errorf("group %q is not a successful condition-attested Arm-B group", group.GroupID)
		}
		sourceCount := group.Condition.PrefixSourceCount
		if sourceCount != 1 && sourceCount != 2 && sourceCount != 4 {
			return 0, fmt.Errorf("group %q lacks a registered Z0-C source-count condition", group.GroupID)
		}
		attestedSources := make(map[string]struct{}, len(group.Condition.ObservedState.PrefixSourceEndpointIDs))
		for _, endpointID := range group.Condition.ObservedState.PrefixSourceEndpointIDs {
			attestedSources[endpointID] = struct{}{}
		}
		for _, child := range group.Children {
			key := transferKey(group.RunID, group.GroupID, child.RequestID)
			placement, exists := placementsByRequest[key]
			if !exists {
				return 0, fmt.Errorf("group %q child %q lacks correlated EPP placement evidence", group.GroupID, child.RequestID)
			}
			if placement.Arm != evidence.ArmLoadAwareP2P || placement.FanoutWidth != group.FanoutWidth {
				return 0, fmt.Errorf("group %q child %q placement does not match its Arm-B group", group.GroupID, child.RequestID)
			}
			if err := validateIndexedSourceSet(group, placement); err != nil {
				return 0, fmt.Errorf("group %q child %q: %w", group.GroupID, child.RequestID, err)
			}
			event, exists := byRequest[key]
			if !exists {
				return 0, fmt.Errorf("group %q child %q lacks measured acquisition telemetry", group.GroupID, child.RequestID)
			}
			if child.DispatchedAt == nil || child.FirstTokenAt == nil || child.CompletedAt == nil || child.DispatchedAt.IsZero() || child.FirstTokenAt.IsZero() || child.CompletedAt.IsZero() || child.FirstTokenAt.Before(*child.DispatchedAt) || child.CompletedAt.Before(*child.FirstTokenAt) {
				return 0, fmt.Errorf("group %q child %q lacks a valid execution interval", group.GroupID, child.RequestID)
			}
			if event.ObservedAt.Before(*child.DispatchedAt) || event.ObservedAt.After(*child.CompletedAt) {
				return 0, fmt.Errorf("group %q child %q acquisition observation is outside its execution interval", group.GroupID, child.RequestID)
			}
			if event.ObservedAt.After(*child.FirstTokenAt) {
				return 0, fmt.Errorf("group %q child %q acquisition was observed after first content", group.GroupID, child.RequestID)
			}
			if event.Acquisition == AcquisitionP2P {
				if _, exists := attestedSources[event.ChosenSource.ID]; !exists {
					return 0, fmt.Errorf("group %q child %q chose source %q outside the condition-attested source set", group.GroupID, child.RequestID, event.ChosenSource.ID)
				}
				if placement.SelectedP2PSource == nil || placement.SelectedP2PSource.Source != *event.ChosenSource {
					return 0, fmt.Errorf("group %q child %q runtime source does not match EPP-selected source", group.GroupID, child.RequestID)
				}
				if event.TransferStartedAt.Before(*child.DispatchedAt) || event.TransferCompletedAt.After(*child.CompletedAt) {
					return 0, fmt.Errorf("group %q child %q transfer interval is outside its execution interval", group.GroupID, child.RequestID)
				}
				if event.TransferCompletedAt.After(*child.FirstTokenAt) {
					return 0, fmt.Errorf("group %q child %q transfer completed after first content", group.GroupID, child.RequestID)
				}
			}
			used[key] = struct{}{}
			usedPlacements[key] = struct{}{}
			measurement := pullMeasurements[key]
			record := SourcePressureObservation{
				SchemaVersion: SourcePressureSchemaVersion, RunID: group.RunID, GroupID: group.GroupID, RequestID: child.RequestID,
				FanoutWidth: group.FanoutWidth, Acquisition: event.Acquisition, ChosenSource: event.ChosenSource,
				CandidateSourceCount: sourceCount, TransferBytes: event.TransferBytes, TransferStartedAt: event.TransferStartedAt,
				TransferCompletedAt: event.TransferCompletedAt, PeakConcurrentPulls: measurement.PeakConcurrentPulls,
				TransferBytesPerSecond: measurement.TransferBytesPerSecond, LastSiblingTTFTSeconds: group.SlowestChildTTFTSeconds,
				SourceNICBytesPerSecond: event.SourceNICBytesPerSecond, SourceCPUTierUtilization: event.SourceCPUTierUtilization, ObservedAt: event.ObservedAt,
			}
			if err := ValidateSourcePressure(record); err != nil {
				return 0, fmt.Errorf("compile request %q: %w", child.RequestID, err)
			}
			if err := encoder.Encode(record); err != nil {
				return 0, err
			}
			count++
		}
	}
	if len(used) != len(events) {
		return 0, fmt.Errorf("transfer input contains %d events that do not join to an attested child", len(events)-len(used))
	}
	if len(usedPlacements) != len(placements) {
		return 0, fmt.Errorf("placement input contains %d events that do not join to an attested child", len(placements)-len(usedPlacements))
	}
	if count == 0 {
		return 0, fmt.Errorf("no source-pressure records were compiled")
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

type pullMeasurement struct {
	PeakConcurrentPulls    *uint32
	TransferBytesPerSecond *float64
}

// derivePullMeasurements publishes the two raw-derived Z0-C measurements that
// were preregistered but are not supplied by the runtime observer itself. Pull
// intervals are treated as half-open [start,end), grouped by run, fanout group,
// and exact chosen source. Every P2P child records that source's peak overlap
// for the group and its own measured transfer throughput.
func derivePullMeasurements(events []P2PTransferEvent) (map[string]pullMeasurement, error) {
	type sourceKey struct {
		runID, groupID, sourceID, sourceModel string
	}
	bySource := make(map[sourceKey][]P2PTransferEvent)
	for _, event := range events {
		if event.Acquisition != AcquisitionP2P {
			continue
		}
		key := sourceKey{event.RunID, event.GroupID, event.ChosenSource.ID, event.ChosenSource.Model}
		bySource[key] = append(bySource[key], event)
	}
	derived := make(map[string]pullMeasurement, len(events))
	for _, sourceEvents := range bySource {
		var peak uint32
		for _, candidate := range sourceEvents {
			var concurrent uint32
			for _, other := range sourceEvents {
				if !other.TransferStartedAt.After(*candidate.TransferStartedAt) && other.TransferCompletedAt.After(*candidate.TransferStartedAt) {
					concurrent++
				}
			}
			if concurrent > peak {
				peak = concurrent
			}
		}
		if peak == 0 {
			return nil, fmt.Errorf("derive concurrent pulls: P2P source has no positive transfer interval")
		}
		for _, event := range sourceEvents {
			duration := event.TransferCompletedAt.Sub(*event.TransferStartedAt).Seconds()
			if duration <= 0 {
				return nil, fmt.Errorf("derive transfer throughput for request %q: transfer duration must be positive", event.RequestID)
			}
			throughput := float64(*event.TransferBytes) / duration
			peakCopy := peak
			throughputCopy := throughput
			derived[transferKey(event.RunID, event.GroupID, event.RequestID)] = pullMeasurement{
				PeakConcurrentPulls: &peakCopy, TransferBytesPerSecond: &throughputCopy,
			}
		}
	}
	return derived, nil
}

func validateIndexedSourceSet(group evidence.GroupResult, placement evidence.PlacementEvent) error {
	expectedLocal := make(map[string]struct{}, len(group.Condition.ObservedState.CachedEndpointIDs))
	for _, id := range group.Condition.ObservedState.CachedEndpointIDs {
		expectedLocal[id] = struct{}{}
	}
	expected := make(map[string]struct{}, len(group.Condition.ObservedState.PrefixSourceEndpointIDs))
	for _, id := range group.Condition.ObservedState.PrefixSourceEndpointIDs {
		expected[id] = struct{}{}
	}
	indexed := make(map[string]struct{})
	indexedLocal := make(map[string]struct{})
	for _, endpoint := range placement.Snapshot.Endpoints {
		if endpoint.LocalPrefixTokens >= group.Cell.PrefixTokens {
			indexedLocal[endpoint.Ref.ID] = struct{}{}
		}
		if endpoint.SourcePrefixTokens >= group.Cell.PrefixTokens {
			indexed[endpoint.Ref.ID] = struct{}{}
		}
	}
	if len(indexedLocal) != len(expectedLocal) {
		return fmt.Errorf("EPP indexed %d full local-prefix caches, want exact attested set of %d", len(indexedLocal), len(expectedLocal))
	}
	for id := range expectedLocal {
		if _, exists := indexedLocal[id]; !exists {
			return fmt.Errorf("EPP local-prefix index is missing attested endpoint %q", id)
		}
	}
	if len(indexed) != len(expected) {
		return fmt.Errorf("EPP indexed %d full-prefix sources, want exact attested set of %d", len(indexed), len(expected))
	}
	for id := range expected {
		if _, exists := indexed[id]; !exists {
			return fmt.Errorf("EPP indexed source set is missing attested endpoint %q", id)
		}
	}
	if placement.SelectedP2PSource != nil && placement.SelectedP2PSource.CachedTokens < group.Cell.PrefixTokens {
		return fmt.Errorf("EPP-selected source exposes only %d/%d prefix tokens", placement.SelectedP2PSource.CachedTokens, group.Cell.PrefixTokens)
	}
	return nil
}

func readP2PTransferEvents(path string) ([]P2PTransferEvent, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), maxRecorderLineBytes)
	var events []P2PTransferEvent
	for scanner.Scan() {
		var event P2PTransferEvent
		if err := decodeStrict(scanner.Bytes(), &event); err != nil {
			return nil, fmt.Errorf("decode P2P transfer event %d: %w", len(events)+1, err)
		}
		if err := validateP2PTransferEvent(event); err != nil {
			return nil, fmt.Errorf("P2P transfer event %d: %w", len(events)+1, err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func ValidateP2PTransferFile(path string) error {
	events, err := readP2PTransferEvents(path)
	if err != nil {
		return err
	}
	if len(events) == 0 {
		return fmt.Errorf("P2P transfer telemetry contains no events")
	}
	return nil
}

func validateP2PTransferEvent(event P2PTransferEvent) error {
	if event.SchemaVersion != P2PTransferSchemaVersion || strings.TrimSpace(event.RunID) == "" || strings.TrimSpace(event.GroupID) == "" || strings.TrimSpace(event.RequestID) == "" || event.ObservedAt.IsZero() {
		return fmt.Errorf("schema, identities, and observed time are required")
	}
	if event.SourceNICBytesPerSecond != nil && !finiteNonNegativeSource(*event.SourceNICBytesPerSecond) {
		return fmt.Errorf("source NIC rate must be finite and non-negative")
	}
	if event.SourceCPUTierUtilization != nil && (!finiteNonNegativeSource(*event.SourceCPUTierUtilization) || *event.SourceCPUTierUtilization > 1) {
		return fmt.Errorf("source CPU-tier utilization must be in [0,1]")
	}
	switch event.Acquisition {
	case AcquisitionP2P:
		if event.ChosenSource == nil || strings.TrimSpace(event.ChosenSource.ID) == "" || strings.TrimSpace(event.ChosenSource.Model) == "" || event.TransferBytes == nil || *event.TransferBytes == 0 || event.TransferStartedAt == nil || event.TransferCompletedAt == nil || event.TransferStartedAt.IsZero() || event.TransferCompletedAt.IsZero() || !event.TransferCompletedAt.After(*event.TransferStartedAt) {
			return fmt.Errorf("P2P acquisition requires a source, positive bytes, and valid transfer timestamps")
		}
		if event.ObservedAt.Before(*event.TransferCompletedAt) {
			return fmt.Errorf("P2P acquisition observation predates transfer completion")
		}
	case AcquisitionLocal, AcquisitionRecompute:
		if event.ChosenSource != nil || event.TransferBytes != nil || event.TransferStartedAt != nil || event.TransferCompletedAt != nil {
			return fmt.Errorf("%s acquisition cannot include transfer evidence", event.Acquisition)
		}
	default:
		return fmt.Errorf("acquisition is unsupported")
	}
	return nil
}

func transferKey(runID, groupID, requestID string) string {
	return runID + "\x00" + groupID + "\x00" + requestID
}

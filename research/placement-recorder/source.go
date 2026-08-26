package placementrecorder

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/JDinSeattle/velaserve/internal/evidence"
)

const SourcePressureSchemaVersion = "velaserve.source-pressure/v1"

type Acquisition string

const (
	AcquisitionLocal     Acquisition = "local"
	AcquisitionP2P       Acquisition = "p2p"
	AcquisitionRecompute Acquisition = "recompute"
)

type SourcePressureObservation struct {
	SchemaVersion            string                `json:"schema_version"`
	RunID                    string                `json:"run_id"`
	GroupID                  string                `json:"group_id"`
	RequestID                string                `json:"request_id"`
	FanoutWidth              uint32                `json:"fanout_width"`
	Acquisition              Acquisition           `json:"acquisition"`
	ChosenSource             *evidence.EndpointRef `json:"chosen_source,omitempty"`
	CandidateSourceCount     uint32                `json:"candidate_source_count"`
	TransferBytes            *uint64               `json:"transfer_bytes,omitempty"`
	TransferStartedAt        *time.Time            `json:"transfer_started_at,omitempty"`
	TransferCompletedAt      *time.Time            `json:"transfer_completed_at,omitempty"`
	LastSiblingTTFTSeconds   float64               `json:"last_sibling_ttft_seconds"`
	SourceNICBytesPerSecond  *float64              `json:"source_nic_bytes_per_second,omitempty"`
	SourceCPUTierUtilization *float64              `json:"source_cpu_tier_utilization,omitempty"`
	ObservedAt               time.Time             `json:"observed_at"`
}

func ParseSourcePressure(line []byte) (SourcePressureObservation, error) {
	var record SourcePressureObservation
	if err := decodeStrict(line, &record); err != nil {
		return SourcePressureObservation{}, fmt.Errorf("decode source-pressure record: %w", err)
	}
	if err := ValidateSourcePressure(record); err != nil {
		return SourcePressureObservation{}, err
	}
	return record, nil
}

func ValidateSourcePressure(record SourcePressureObservation) error {
	if record.SchemaVersion != SourcePressureSchemaVersion {
		return fmt.Errorf("schema_version: got %q, want %q", record.SchemaVersion, SourcePressureSchemaVersion)
	}
	if strings.TrimSpace(record.RunID) == "" || strings.TrimSpace(record.GroupID) == "" || strings.TrimSpace(record.RequestID) == "" {
		return fmt.Errorf("run_id, group_id, and request_id are required")
	}
	switch record.FanoutWidth {
	case 2, 4, 8, 16:
	default:
		return fmt.Errorf("fanout_width: %d is not registered for Z0", record.FanoutWidth)
	}
	if !finiteNonNegativeSource(record.LastSiblingTTFTSeconds) {
		return fmt.Errorf("last_sibling_ttft_seconds must be finite and non-negative")
	}
	if record.ObservedAt.IsZero() {
		return fmt.Errorf("observed_at: required")
	}
	if record.CandidateSourceCount != 1 && record.CandidateSourceCount != 2 && record.CandidateSourceCount != 4 {
		return fmt.Errorf("candidate_source_count must be 1, 2, or 4")
	}
	if record.SourceNICBytesPerSecond != nil && !finiteNonNegativeSource(*record.SourceNICBytesPerSecond) {
		return fmt.Errorf("source_nic_bytes_per_second must be finite and non-negative")
	}
	if record.SourceCPUTierUtilization != nil && (!finiteNonNegativeSource(*record.SourceCPUTierUtilization) || *record.SourceCPUTierUtilization > 1) {
		return fmt.Errorf("source_cpu_tier_utilization must be in [0,1]")
	}
	switch record.Acquisition {
	case AcquisitionP2P:
		if record.ChosenSource == nil || strings.TrimSpace(record.ChosenSource.ID) == "" || strings.TrimSpace(record.ChosenSource.Model) == "" {
			return fmt.Errorf("chosen_source: required for p2p acquisition")
		}
		if record.TransferBytes == nil || *record.TransferBytes == 0 {
			return fmt.Errorf("transfer_bytes: positive measurement required for p2p acquisition")
		}
		if record.TransferStartedAt == nil || record.TransferCompletedAt == nil {
			return fmt.Errorf("transfer timestamps are required for p2p acquisition")
		}
		if record.TransferStartedAt.IsZero() || record.TransferCompletedAt.IsZero() || record.TransferCompletedAt.Before(*record.TransferStartedAt) {
			return fmt.Errorf("transfer timestamps are invalid")
		}
	case AcquisitionLocal, AcquisitionRecompute:
		if record.ChosenSource != nil || record.TransferBytes != nil || record.TransferStartedAt != nil || record.TransferCompletedAt != nil {
			return fmt.Errorf("%s acquisition cannot include transfer evidence", record.Acquisition)
		}
	default:
		return fmt.Errorf("acquisition: unsupported value %q", record.Acquisition)
	}
	return nil
}

func finiteNonNegativeSource(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}

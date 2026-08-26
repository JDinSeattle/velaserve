package placementrecorder

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/JDinSeattle/velaserve/internal/evidence"
)

func TestSourcePressureOmitsUnavailableHardwareCounters(t *testing.T) {
	start := time.Date(2026, 8, 26, 8, 0, 0, 0, time.UTC)
	end := start.Add(50 * time.Millisecond)
	bytesTransferred := uint64(2 << 20)
	record := SourcePressureObservation{
		SchemaVersion:          SourcePressureSchemaVersion,
		RunID:                  "run-source-1",
		GroupID:                "01K39J6FJ4N5W7V57QK9Q0C6CF",
		RequestID:              "request-01",
		FanoutWidth:            8,
		Acquisition:            AcquisitionP2P,
		ChosenSource:           &evidence.EndpointRef{ID: "sim-0", Model: "sim-model"},
		CandidateSourceCount:   2,
		TransferBytes:          &bytesTransferred,
		TransferStartedAt:      &start,
		TransferCompletedAt:    &end,
		LastSiblingTTFTSeconds: 0.4,
		ObservedAt:             end,
	}
	if err := ValidateSourcePressure(record); err != nil {
		t.Fatalf("ValidateSourcePressure() error = %v", err)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "source_nic_bytes_per_second") || strings.Contains(string(encoded), "source_cpu_tier_utilization") {
		t.Fatalf("unavailable counters serialized as data: %s", encoded)
	}
}

func TestSourcePressureRequiresP2PTransferEvidence(t *testing.T) {
	record := SourcePressureObservation{
		SchemaVersion: SourcePressureSchemaVersion,
		RunID:         "run-source-1",
		GroupID:       "group-source-1",
		RequestID:     "request-01",
		FanoutWidth:   8,
		Acquisition:   AcquisitionP2P,
		ObservedAt:    time.Now(),
	}
	if err := ValidateSourcePressure(record); err == nil || !strings.Contains(err.Error(), "chosen_source") {
		t.Fatalf("ValidateSourcePressure() error = %v", err)
	}
}

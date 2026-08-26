package placementrecorder

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JDinSeattle/velaserve/internal/evidence"
)

func TestCorrelateRequiresRequestAndGroupIdentity(t *testing.T) {
	group := recorderGroupResult()
	epp := recorderEPPRecord()
	_, err := Correlate(EnvoyRecord{GroupID: group.GroupID}, epp, group)
	if err == nil || !strings.Contains(err.Error(), "request_id") {
		t.Fatalf("Correlate() error = %v", err)
	}

	envoy := recorderEnvoyRecord()
	envoy.GroupID = "different-group"
	_, err = Correlate(envoy, epp, group)
	if err == nil || !strings.Contains(err.Error(), "group_id") {
		t.Fatalf("Correlate() error = %v", err)
	}
}

func TestIngestReportsEveryUnmatchedLine(t *testing.T) {
	root := t.TempDir()
	writeJSONLines(t, filepath.Join(root, "groups.jsonl"), recorderGroupResult())
	secondEnvoy := recorderEnvoyRecord()
	secondEnvoy.RequestID = "request-02"
	secondEPP := recorderEPPRecord()
	secondEPP.RequestID = "request-02"
	writeJSONLines(t, filepath.Join(root, "envoy.jsonl"), recorderEnvoyLine(recorderEnvoyRecord()), recorderEnvoyLine(secondEnvoy), map[string]any{"unexpected": "shape"})
	writeJSONLines(t, filepath.Join(root, "epp.jsonl"), recorderEPPRecord(), secondEPP)

	report, err := Ingest(context.Background(), IngestOptions{
		ArtifactRoot: root,
		GroupsPath:   "groups.jsonl",
		EnvoyPath:    "envoy.jsonl",
		EPPPath:      "epp.jsonl",
	})
	if err != nil {
		t.Fatalf("Ingest() error = %v", err)
	}
	if report.Complete || report.UnmatchedCount != 1 || report.PlacementCount != 2 {
		t.Fatalf("report = %#v", report)
	}
	contents, err := os.ReadFile(report.UnmatchedPath)
	if err != nil {
		t.Fatalf("read unmatched output: %v", err)
	}
	if !strings.Contains(string(contents), "unexpected") || !strings.Contains(string(contents), "missing request_id") {
		t.Fatalf("unmatched output = %s", contents)
	}
}

func recorderEnvoyRecord() EnvoyRecord {
	return EnvoyRecord{
		StartedAt:    time.Date(2026, 8, 26, 8, 0, 0, 0, time.UTC),
		RequestID:    "request-01",
		GroupID:      "01K39J6FJ4N5W7V57QK9Q0C6CF",
		UpstreamHost: "sim-0",
		ResponseCode: 200,
		DurationMS:   12,
		TraceID:      "trace-01",
		SpanID:       "span-01",
	}
}

func recorderEnvoyLine(record EnvoyRecord) map[string]any {
	return map[string]any{
		"START_TIME":               record.StartedAt.Format(time.RFC3339Nano),
		"REQ(X-REQUEST-ID)":        record.RequestID,
		"REQ(X-VELA-FANOUT-GROUP)": record.GroupID,
		"UPSTREAM_HOST":            record.UpstreamHost,
		"RESPONSE_CODE":            record.ResponseCode,
		"DURATION":                 record.DurationMS,
		"TRACE_ID":                 record.TraceID,
		"SPAN_ID":                  record.SpanID,
	}
}

func recorderEPPRecord() EPPRecord {
	now := time.Date(2026, 8, 26, 8, 0, 0, 0, time.UTC)
	return EPPRecord{
		SchemaVersion: EPPSchedulingSchemaVersion,
		RequestID:     "request-01",
		GroupID:       "01K39J6FJ4N5W7V57QK9Q0C6CF",
		ObservedAt:    now,
		Snapshot: evidence.EndpointSnapshot{
			ObservedAt: now,
			Endpoints: []evidence.EndpointState{{
				Ref:                evidence.EndpointRef{ID: "sim-0", Model: "sim-model"},
				Healthy:            true,
				Compatible:         true,
				LocalPrefixTokens:  4096,
				AvailableAtSeconds: 0.1,
			}},
		},
		Target: evidence.EndpointRef{ID: "sim-0", Model: "sim-model"},
	}
}

func recorderGroupResult() evidence.GroupResult {
	return evidence.GroupResult{
		SchemaVersion: evidence.SchemaVersion,
		RunID:         "run-recorder-1",
		Arm:           evidence.ArmLoadAwareP2P,
		GroupID:       "01K39J6FJ4N5W7V57QK9Q0C6CF",
		FanoutWidth:   2,
		Cell: evidence.BenchmarkCell{
			Repetition: 1, PrefixRegime: "near-crossover", PrefixTokens: 4096,
			OutputRegime: "short", MaxTokens: 8, ArrivalSkewMS: 1,
			LoadRegime: evidence.LoadModerate, CacheState: "warm-owner", Transport: "tcp", EPPReplicas: 2,
		},
		Outcome: evidence.OutcomeSuccess,
		Children: []evidence.ChildResult{
			{RequestID: "request-01", Outcome: evidence.OutcomeSuccess},
			{RequestID: "request-02", Outcome: evidence.OutcomeSuccess},
		},
	}
}

func writeJSONLines(t *testing.T, path string, values ...any) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	for _, value := range values {
		if err := encoder.Encode(value); err != nil {
			t.Fatal(err)
		}
	}
}

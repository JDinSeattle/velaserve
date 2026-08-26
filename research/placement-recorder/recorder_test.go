package placementrecorder

import (
	"bytes"
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

func TestCorrelateRejectsEPPObservationOutsideChildDispatchToFirstToken(t *testing.T) {
	group := recorderGroupResult()
	epp := recorderEPPRecord()
	epp.ObservedAt = group.Children[0].FirstTokenAt.Add(time.Millisecond)
	epp.Snapshot.ObservedAt = epp.ObservedAt
	if _, err := Correlate(recorderEnvoyRecord(), epp, group); err == nil || !strings.Contains(err.Error(), "dispatch-to-first-token") {
		t.Fatalf("Correlate() error = %v", err)
	}
}

func TestCorrelateRejectsFailedOrChronologicallyUnrelatedEnvoyRecord(t *testing.T) {
	group := recorderGroupResult()
	epp := recorderEPPRecord()

	failed := recorderEnvoyRecord()
	failed.ResponseCode = 503
	if _, err := Correlate(failed, epp, group); err == nil || !strings.Contains(err.Error(), "2xx") {
		t.Fatalf("Correlate() failed-response error = %v", err)
	}

	stale := recorderEnvoyRecord()
	stale.StartedAt = group.Children[0].DispatchedAt.Add(-2 * time.Second)
	if _, err := Correlate(stale, epp, group); err == nil || !strings.Contains(err.Error(), "request interval") {
		t.Fatalf("Correlate() stale-start error = %v", err)
	}

	late := recorderEnvoyRecord()
	late.StartedAt = group.Children[0].FirstTokenAt.Add(time.Millisecond)
	if _, err := Correlate(late, epp, group); err == nil || !strings.Contains(err.Error(), "request interval") {
		t.Fatalf("Correlate() late-start error = %v", err)
	}

	unfinished := recorderEnvoyRecord()
	unfinished.DurationMS = 1
	if _, err := Correlate(unfinished, epp, group); err == nil || !strings.Contains(err.Error(), "stream completion") {
		t.Fatalf("Correlate() early-completion error = %v", err)
	}

	overlong := recorderEnvoyRecord()
	overlong.DurationMS = 3001
	if _, err := Correlate(overlong, epp, group); err == nil || !strings.Contains(err.Error(), "stream completion") {
		t.Fatalf("Correlate() late-completion error = %v", err)
	}
}

func TestCorrelateRejectsEnvoyUpstreamDifferentFromEPPSelection(t *testing.T) {
	epp := recorderEPPRecord()
	epp.TargetHost = "10.0.0.99:8000"
	if _, err := Correlate(recorderEnvoyRecord(), epp, recorderGroupResult()); err == nil || !strings.Contains(err.Error(), "selected target") {
		t.Fatalf("Correlate() error = %v", err)
	}
}

func TestParseEPPNormalizesPinnedObserverLogPrefix(t *testing.T) {
	line := `2026-08-26T01:00:00Z epp VELASERVE_EPP_RECORD {"schema_version":"velaserve.epp-scheduling/v1","request_id":"request-1","group_id":"group-123456789012","observed_at":"2026-08-26T01:00:00Z","snapshot":{"observed_at":"2026-08-26T01:00:00Z","endpoints":[{"ref":{"id":"pod-a","model":"model"},"healthy":true,"compatible":true,"available_at_seconds":0,"queue_depth":1,"running_requests":2,"local_prefix_tokens":4096,"observed_inflight":2}]},"target":{"id":"pod-a","model":"model"},"target_host":"10.0.0.1:8000"}`
	record, err := ParseEPP([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	if record.RequestID != "request-1" || record.Target.ID != "pod-a" || record.Snapshot.Endpoints[0].RunningRequests != 2 {
		t.Fatalf("record = %#v", record)
	}
}

func TestNormalizePinnedStreamsAttachesCollectorEmitterIdentity(t *testing.T) {
	root := t.TempDir()
	binding := `VELASERVE_STREAM_BINDING {"schema_version":"velaserve.stream-emitter-binding/v1","pod_name":"epp-0","pod_uid":"uid-0"}`
	eppRecord, err := json.Marshal(recorderEPPRecord())
	if err != nil {
		t.Fatal(err)
	}
	eppRaw := binding + "\nVELASERVE_EPP_RECORD " + string(eppRecord) + "\n"
	if err := os.WriteFile(filepath.Join(root, "epp-raw.log"), []byte(eppRaw), 0o600); err != nil {
		t.Fatal(err)
	}
	if count, err := NormalizePinnedEPPLog(filepath.Join(root, "epp-raw.log"), filepath.Join(root, "epp.jsonl")); err != nil || count != 1 {
		t.Fatalf("NormalizePinnedEPPLog() count=%d error=%v", count, err)
	}
	var normalizedEPP EPPRecord
	contents, err := os.ReadFile(filepath.Join(root, "epp.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(contents, &normalizedEPP); err != nil {
		t.Fatal(err)
	}
	if normalizedEPP.EmitterPodName != "epp-0" || normalizedEPP.EmitterPodUID != "uid-0" {
		t.Fatalf("normalized EPP emitter = %q/%q", normalizedEPP.EmitterPodName, normalizedEPP.EmitterPodUID)
	}

	envoyRecord, err := json.Marshal(recorderEnvoyLine(recorderEnvoyRecord()))
	if err != nil {
		t.Fatal(err)
	}
	envoyRaw := binding + "\n[epp-0/inner-envoy] " + string(envoyRecord) + "\n"
	if err := os.WriteFile(filepath.Join(root, "envoy-raw.log"), []byte(envoyRaw), 0o600); err != nil {
		t.Fatal(err)
	}
	if count, err := NormalizePinnedEnvoyLog(filepath.Join(root, "envoy-raw.log"), filepath.Join(root, "envoy.jsonl")); err != nil || count != 1 {
		t.Fatalf("NormalizePinnedEnvoyLog() count=%d error=%v", count, err)
	}
	var normalizedEnvoy EnvoyRecord
	contents, err = os.ReadFile(filepath.Join(root, "envoy.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(contents, &normalizedEnvoy); err != nil {
		t.Fatal(err)
	}
	if normalizedEnvoy.EmitterPodName != "epp-0" || normalizedEnvoy.EmitterPodUID != "uid-0" {
		t.Fatalf("normalized Envoy emitter = %q/%q", normalizedEnvoy.EmitterPodName, normalizedEnvoy.EmitterPodUID)
	}
	if parsed, err := ParseEnvoy(bytes.TrimSpace(contents)); err != nil || parsed != normalizedEnvoy {
		t.Fatalf("ParseEnvoy(normalized) = %#v, %v", parsed, err)
	}
}

func TestNormalizePinnedStreamsRejectsUnboundAndSelfAssertedRecords(t *testing.T) {
	root := t.TempDir()
	eppRecord, err := json.Marshal(recorderEPPRecord())
	if err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(root, "unbound-epp.log")
	if err := os.WriteFile(input, append([]byte("VELASERVE_EPP_RECORD "), append(eppRecord, '\n')...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NormalizePinnedEPPLog(input, filepath.Join(root, "unbound-epp.jsonl")); err == nil || !strings.Contains(err.Error(), "no preceding exact stream emitter binding") {
		t.Fatalf("unbound EPP error = %v", err)
	}

	envoy := recorderEnvoyLine(recorderEnvoyRecord())
	envoy["EMITTER_POD_NAME"] = "forged"
	envoy["EMITTER_POD_UID"] = "forged-uid"
	encoded, err := json.Marshal(envoy)
	if err != nil {
		t.Fatal(err)
	}
	binding := `VELASERVE_STREAM_BINDING {"schema_version":"velaserve.stream-emitter-binding/v1","pod_name":"epp-0","pod_uid":"uid-0"}`
	input = filepath.Join(root, "forged-envoy.log")
	if err := os.WriteFile(input, []byte(binding+"\n"+string(encoded)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NormalizePinnedEnvoyLog(input, filepath.Join(root, "forged-envoy.jsonl")); err == nil || !strings.Contains(err.Error(), "self-assert emitter identity") {
		t.Fatalf("forged Envoy error = %v", err)
	}
}

func TestNormalizePinnedEnvoyRejectsMalformedRequestCandidate(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "malformed-envoy.log")
	contents := "VELASERVE_STREAM_BINDING {\"schema_version\":\"velaserve.stream-emitter-binding/v1\",\"pod_name\":\"epp-0\",\"pod_uid\":\"uid-0\"}\n{\"REQ(X-REQUEST-ID)\":\"request-1\",\"REQ(X-VELA-FANOUT-GROUP)\":\n"
	if err := os.WriteFile(input, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NormalizePinnedEnvoyLog(input, filepath.Join(root, "envoy.jsonl")); err == nil || !strings.Contains(err.Error(), "is malformed") {
		t.Fatalf("malformed Envoy error = %v", err)
	}
}

func TestParseStreamEmitterBindingRejectsTrailingJSON(t *testing.T) {
	line := []byte(`VELASERVE_STREAM_BINDING {"schema_version":"velaserve.stream-emitter-binding/v1","pod_name":"epp-0","pod_uid":"uid-0"} {"extra":true}`)
	if _, err := parseStreamEmitterBinding(line); err == nil || !strings.Contains(err.Error(), "trailing JSON value") {
		t.Fatalf("parseStreamEmitterBinding() error = %v", err)
	}
}

func TestNormalizePinnedVLLMLogAttachesCollectorEmitterIdentity(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "vllm-raw.log")
	contents := `VELASERVE_STREAM_BINDING {"schema_version":"velaserve.stream-emitter-binding/v1","pod_name":"model-0","pod_uid":"model-uid-0"}
[model-0/vllm] VELASERVE_VLLM_RUNTIME {"schema_version":"velaserve.vllm-acquisition/v1","engine_request_id":"chatcmpl-request-1","local_cached_tokens":0,"external_cached_tokens":0,"observed_at":"2026-08-26T08:00:00Z"}
`
	if err := os.WriteFile(input, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "vllm-runtime.jsonl")
	if count, err := NormalizePinnedVLLMLog(input, output); err != nil || count != 1 {
		t.Fatalf("NormalizePinnedVLLMLog() count=%d error=%v", count, err)
	}
	records, err := ReadVLLMRuntime(output)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].EmitterPodName != "model-0" || records[0].EmitterPodUID != "model-uid-0" {
		t.Fatalf("normalized vLLM records = %#v", records)
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
		DurationMS:   2000,
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
		Target:     evidence.EndpointRef{ID: "sim-0", Model: "sim-model"},
		TargetHost: "sim-0",
	}
}

func recorderGroupResult() evidence.GroupResult {
	dispatched := time.Date(2026, 8, 26, 8, 0, 0, 0, time.UTC)
	firstToken := dispatched.Add(time.Second)
	completed := firstToken.Add(time.Second)
	return evidence.GroupResult{
		SchemaVersion:           evidence.SchemaVersion,
		RunID:                   "run-recorder-1",
		Arm:                     evidence.ArmLoadAwareP2P,
		GroupID:                 "01K39J6FJ4N5W7V57QK9Q0C6CF",
		FanoutWidth:             2,
		MakespanSeconds:         2,
		SlowestChildTTFTSeconds: 1,
		Cell: evidence.BenchmarkCell{
			Repetition: 1, PrefixRegime: "near-crossover", PrefixTokens: 4096,
			OutputRegime: "short", MaxTokens: 8, ArrivalSkewMS: 1,
			LoadRegime: evidence.LoadModerate, CacheState: "warm-owner", Transport: "tcp", EPPReplicas: 2,
		},
		Outcome: evidence.OutcomeSuccess,
		Children: []evidence.ChildResult{
			{RequestID: "request-01", TTFTSeconds: 1, LatencySeconds: 2, Outcome: evidence.OutcomeSuccess, DispatchedAt: &dispatched, FirstTokenAt: &firstToken, CompletedAt: &completed},
			{RequestID: "request-02", TTFTSeconds: 1, LatencySeconds: 2, Outcome: evidence.OutcomeSuccess, DispatchedAt: &dispatched, FirstTokenAt: &firstToken, CompletedAt: &completed},
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

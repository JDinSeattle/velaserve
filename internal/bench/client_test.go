package bench

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JDinSeattle/velaserve/internal/evidence"
	"github.com/JDinSeattle/velaserve/internal/fanout/protocol"
)

func TestRunGroupRetainsAllChildrenWhenOneSiblingFails(t *testing.T) {
	var received atomic.Int32
	var mu sync.Mutex
	requestIDs := make([]string, 0, 4)
	groupIDs := make([]string, 0, 4)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		index := received.Add(1)
		mu.Lock()
		requestIDs = append(requestIDs, request.Header.Get(protocol.HeaderRequestID))
		groupIDs = append(groupIDs, request.Header.Get(protocol.HeaderGroup))
		mu.Unlock()
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		if body["stream"] != true || body["model"] != "test-model" {
			http.Error(writer, "wrong OpenAI request", http.StatusBadRequest)
			return
		}
		if index == 1 {
			http.Error(writer, "simulated overload", http.StatusServiceUnavailable)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprint(writer, "data: {\"choices\":[],\"usage\":{\"completion_tokens\":1}}\n\n")
		fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := Client{Endpoint: server.URL, HTTPClient: server.Client(), MaxEventBytes: 1 << 20}
	result, err := client.RunGroup(context.Background(), GroupRequest{
		RunID:        "bench-run-1",
		Arm:          evidence.ArmLoadAwareP2P,
		Model:        "test-model",
		CommonPrefix: "shared prefix: ",
		Suffixes:     []string{"a", "b", "c", "d"},
		MaxTokens:    8,
		MaxWidth:     16,
		Timeout:      2 * time.Second,
		Cell:         benchmarkCell(8, 0),
	})
	if err != nil {
		t.Fatalf("RunGroup() returned transport error = %v", err)
	}
	if received.Load() != 4 || len(result.Children) != 4 {
		t.Fatalf("received=%d children=%d, want all four", received.Load(), len(result.Children))
	}
	if result.Outcome != evidence.OutcomeFailure || result.Failure == "" {
		t.Fatalf("group result = %#v", result)
	}
	if err := evidence.ValidateGroupResult(result); err != nil {
		t.Fatalf("result does not satisfy evidence contract: %v", err)
	}
	sort.Strings(requestIDs)
	for index := 1; index < len(requestIDs); index++ {
		if requestIDs[index] == requestIDs[index-1] {
			t.Fatalf("duplicate request ID %q", requestIDs[index])
		}
	}
	for _, groupID := range groupIDs {
		if groupID == "" || groupID != groupIDs[0] {
			t.Fatalf("group IDs = %v", groupIDs)
		}
	}
}

func TestRunGroupCancellationStillRetainsEveryChild(t *testing.T) {
	var received atomic.Int32
	allArrived := make(chan struct{})
	releaseHandlers := make(chan struct{})
	var closeOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if received.Add(1) == 4 {
			closeOnce.Do(func() { close(allArrived) })
		}
		select {
		case <-request.Context().Done():
		case <-releaseHandlers:
		}
	}))
	t.Cleanup(func() {
		close(releaseHandlers)
		server.Close()
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-allArrived
		cancel()
	}()
	client := Client{Endpoint: server.URL, HTTPClient: server.Client(), MaxEventBytes: 1 << 20}
	result, err := client.RunGroup(ctx, GroupRequest{
		RunID:        "bench-run-cancelled",
		Arm:          evidence.ArmLoadAwareP2P,
		Model:        "test-model",
		CommonPrefix: "shared prefix: ",
		Suffixes:     []string{"a", "b", "c", "d"},
		MaxTokens:    8,
		MaxWidth:     16,
		Timeout:      2 * time.Second,
		Cell:         benchmarkCell(8, 0),
	})
	if err != nil {
		t.Fatalf("RunGroup() returned setup error = %v", err)
	}
	if len(result.Children) != 4 || result.Outcome != evidence.OutcomeCancelled {
		t.Fatalf("result = %#v", result)
	}
	for slot, child := range result.Children {
		if child.Outcome != evidence.OutcomeCancelled || child.Failure == "" {
			t.Fatalf("child %d = %#v", slot, child)
		}
	}
}

func TestRunGroupCancellationBeforeStaggeredDispatchRemainsEvidence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := Client{Endpoint: server.URL, HTTPClient: server.Client(), MaxEventBytes: 1 << 20}
	result, err := client.RunGroup(ctx, GroupRequest{
		RunID: "bench-run-cancelled-before-dispatch", Arm: evidence.ArmLoadAwareP2P, Model: "test-model",
		CommonPrefix: "shared prefix: ", Suffixes: []string{"a", "b", "c", "d"}, MaxTokens: 8, MaxWidth: 16,
		ArrivalSkew: 20 * time.Millisecond, Timeout: 2 * time.Second, Cell: benchmarkCell(8, 20),
	})
	if err != nil {
		t.Fatalf("RunGroup() discarded pre-dispatch cancellation evidence: %v", err)
	}
	missingDispatch := 0
	for _, child := range result.Children {
		if child.DispatchedAt == nil {
			missingDispatch++
		}
	}
	if result.Outcome != evidence.OutcomeCancelled || missingDispatch == 0 {
		t.Fatalf("result = %#v, want retained pre-dispatch cancellations", result)
	}
}

func TestRunGroupRetainsChildrenAndAppliedConditionWhenFinalizeFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/model":
			writer.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(writer, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
			fmt.Fprint(writer, "data: {\"choices\":[],\"usage\":{\"completion_tokens\":1}}\n\n")
			fmt.Fprint(writer, "data: [DONE]\n\n")
		case "/v1/conditions/apply":
			var applied conditionRequest
			if err := json.NewDecoder(request.Body).Decode(&applied); err != nil {
				http.Error(writer, err.Error(), http.StatusBadRequest)
				return
			}
			state := evidence.ConditionObservedState{
				OfferedLoadQPS: 50, AchievedLoadQPS: 50, SaturationQPS: 100,
				LoadProfileSHA256: strings.Repeat("1", 64), LoadProfilesSHA256: strings.Repeat("2", 64), LoadCalibrationSHA256: strings.Repeat("3", 64),
				CachedEndpointIDs: []string{"model-0"}, OwnerEndpointOrder: []string{"model-0", "model-1"}, DrainedEndpointIDs: []string{"model-0", "model-1"},
				DrainStableSamples: 2, OrdinaryTrafficMeanLatencySeconds: .01, MeasurementSource: "load-calibration:" + strings.Repeat("3", 64),
			}
			attestation := evidence.ConditionAttestation{
				SchemaVersion: evidence.ConditionAttestationSchemaVersion, RunID: applied.RunID, GroupID: applied.GroupID,
				LoadRegime: applied.Cell.LoadRegime, CacheState: applied.Cell.CacheState, PrefixSourceCount: applied.PrefixSourceCount,
				OwnerRotation: applied.OwnerRotation, ControllerRevision: strings.Repeat("a", 40), ObservedState: state, AppliedAt: time.Now().UTC().Add(-time.Second),
			}
			attestation.StateSHA256 = conditionStateHashForTest(attestation)
			writer.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(writer).Encode(attestation); err != nil {
				t.Error(err)
			}
		case "/v1/conditions/finalize":
			http.Error(writer, "measured load left registered band", http.StatusConflict)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client := Client{
		Endpoint: server.URL + "/model", HTTPClient: server.Client(), MaxEventBytes: 1 << 20,
		ConditionControllerEndpoint: server.URL + "/v1/conditions/apply", ConditionControlToken: "test-token", RequireConditionAttestation: true,
	}
	result, err := client.RunGroup(context.Background(), GroupRequest{
		RunID: "bench-run-finalize-failure", Arm: evidence.ArmLoadAwareP2P, Model: "test-model",
		CommonPrefix: "shared prefix: ", WarmupContent: "shared prefix: warmup", Suffixes: []string{"a", "b"},
		MaxTokens: 8, MaxWidth: 16, Timeout: 2 * time.Second, Cell: benchmarkCell(8, 0),
	})
	if err == nil || !strings.Contains(err.Error(), "finalize workload condition") {
		t.Fatalf("RunGroup() error = %v, want finalization failure", err)
	}
	if result.GroupID == "" || len(result.Children) != 2 || result.Outcome != evidence.OutcomeFailure || result.Condition == nil || result.Condition.FinalizedAt != nil || result.ConditionFailure == "" {
		t.Fatalf("retained group = %#v", result)
	}
	if validationErr := evidence.ValidateGroupResult(result); validationErr != nil {
		t.Fatalf("retained finalization failure is not valid evidence: %v", validationErr)
	}
}

func conditionStateHashForTest(condition evidence.ConditionAttestation) string {
	state := struct {
		SchemaVersion     string                          `json:"schema_version"`
		LoadRegime        evidence.LoadRegime             `json:"load_regime"`
		CacheState        string                          `json:"cache_state"`
		PrefixSourceCount uint32                          `json:"prefix_source_count,omitempty"`
		OwnerRotation     uint32                          `json:"owner_rotation"`
		ObservedState     evidence.ConditionObservedState `json:"observed_state"`
	}{"velaserve.applied-condition/v1", condition.LoadRegime, condition.CacheState, condition.PrefixSourceCount, condition.OwnerRotation, condition.ObservedState}
	encoded, _ := json.Marshal(state)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func TestSummarizeGroupComputesRecomputedSharedPrefixTokens(t *testing.T) {
	cachedFirst := uint64(1000)
	cachedSecond := uint64(4500)
	result := summarizeGroup(GroupRequest{
		RunID: "run", Arm: evidence.ArmLoadAwareP2P, Cell: benchmarkCell(8, 0),
	}, "group", []evidence.ChildResult{
		{RequestID: "request-a", Outcome: evidence.OutcomeSuccess, PromptTokens: 5000, CachedTokens: &cachedFirst},
		{RequestID: "request-b", Outcome: evidence.OutcomeSuccess, PromptTokens: 5000, CachedTokens: &cachedSecond},
	})
	if result.RecomputedPrefixTokens == nil || *result.RecomputedPrefixTokens != 3096 {
		t.Fatalf("RecomputedPrefixTokens = %v, want 3096", result.RecomputedPrefixTokens)
	}
}

func TestSummarizeGroupLeavesRecomputedTokensUnmeasuredWithoutCacheReadback(t *testing.T) {
	cached := uint64(1000)
	result := summarizeGroup(GroupRequest{
		RunID: "run", Arm: evidence.ArmLoadAwareP2P, Cell: benchmarkCell(8, 0),
	}, "group", []evidence.ChildResult{
		{RequestID: "request-a", Outcome: evidence.OutcomeSuccess, PromptTokens: 5000, CachedTokens: &cached},
		{RequestID: "request-b", Outcome: evidence.OutcomeSuccess, PromptTokens: 5000},
	})
	if result.RecomputedPrefixTokens != nil {
		t.Fatalf("RecomputedPrefixTokens = %v, want unmeasured", *result.RecomputedPrefixTokens)
	}
}

func benchmarkCell(maxTokens, arrivalSkewMS uint32) evidence.BenchmarkCell {
	return evidence.BenchmarkCell{
		Repetition:    1,
		PrefixRegime:  "near-crossover",
		PrefixTokens:  4096,
		OutputRegime:  "short",
		MaxTokens:     maxTokens,
		ArrivalSkewMS: arrivalSkewMS,
		LoadRegime:    evidence.LoadModerate,
		CacheState:    "warm-owner",
		Transport:     "tcp",
		EPPReplicas:   1,
	}
}

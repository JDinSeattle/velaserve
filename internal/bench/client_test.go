package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
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

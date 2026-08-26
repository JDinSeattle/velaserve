package simfleet

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JDinSeattle/velaserve/internal/bench"
	"github.com/JDinSeattle/velaserve/internal/evidence"
)

func TestOpenAISimulatorAcceptsUpstreamRequestsWithoutPrivateSimulationHeaders(t *testing.T) {
	fleet, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"model":"velaserve-simulator","messages":[{"role":"user","content":"hello"}],"max_tokens":8,"stream":true,"stream_options":{"include_usage":true}}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body)
	response := httptest.NewRecorder()
	fleet.ServeHTTP(response, request)
	contents := response.Body.String()
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, contents)
	}
	if response.Header().Get(bench.HeaderSimulatorTarget) != "" {
		t.Fatal("ordinary upstream response leaked the private simulator target header")
	}
	if !strings.Contains(contents, "data: [DONE]") {
		t.Fatalf("response is not a complete OpenAI SSE stream: %s", contents)
	}
	if len(fleet.EPPRecords()) != 0 {
		t.Fatalf("ordinary upstream request created private simulator records: %d", len(fleet.EPPRecords()))
	}
}

func TestOpenAISimulatorExposesBoundedVLLMMetricsForUpstreamEPP(t *testing.T) {
	fleet, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	response := httptest.NewRecorder()
	fleet.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	for _, metric := range []string{"vllm:num_requests_waiting", "vllm:num_requests_running", "vllm:kv_cache_usage_perc"} {
		if !strings.Contains(response.Body.String(), metric) {
			t.Fatalf("metrics response is missing %q: %s", metric, response.Body.String())
		}
	}
}

func TestOpenAISimulatorExposesDeterministicVLLMRenderEndpoints(t *testing.T) {
	fleet, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		path  string
		body  string
		array bool
	}{
		{path: "/v1/chat/completions/render", body: `{"model":"velaserve-simulator","messages":[{"role":"user","content":"shared prefix suffix"}]}`},
		{path: "/v1/completions/render", body: `{"model":"velaserve-simulator","prompt":"shared prefix suffix"}`, array: true},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, test.path, bytes.NewBufferString(test.body))
			response := httptest.NewRecorder()
			fleet.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			var decoded any
			if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
				t.Fatal(err)
			}
			if test.array {
				values, ok := decoded.([]any)
				if !ok || len(values) != 1 {
					t.Fatalf("completion render response = %#v", decoded)
				}
				decoded = values[0]
			}
			value, ok := decoded.(map[string]any)
			if !ok {
				t.Fatalf("render response = %#v", decoded)
			}
			tokenIDs, ok := value["token_ids"].([]any)
			if !ok || len(tokenIDs) == 0 {
				t.Fatalf("render response = %#v", decoded)
			}
		})
	}
}

func TestOpenAISimulatorExposesTargetsOnlyThroughSimulatorContract(t *testing.T) {
	fleet, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(fleet)
	defer server.Close()

	client := bench.Client{
		Endpoint:      server.URL + "/v1/chat/completions",
		HTTPClient:    server.Client(),
		MaxEventBytes: 1 << 20,
		SimulatorMode: true,
	}
	result, err := client.RunGroup(context.Background(), bench.GroupRequest{
		RunID:        "sim-run-1",
		Arm:          evidence.ArmLoadAwareP2P,
		Model:        "velaserve-simulator",
		CommonPrefix: "shared simulated prefix ",
		Suffixes:     []string{"candidate 1", "candidate 2", "candidate 3", "candidate 4"},
		MaxTokens:    8,
		MaxWidth:     16,
		ArrivalSkew:  20 * time.Millisecond,
		Timeout:      time.Second,
		Cell: evidence.BenchmarkCell{
			Repetition: 1, PrefixRegime: "near-crossover", PrefixTokens: 4096,
			OutputRegime: "short", MaxTokens: 8, ArrivalSkewMS: 20,
			LoadRegime: evidence.LoadModerate, CacheState: "warm-owner", Transport: "tcp", EPPReplicas: 1,
		},
	})
	if err != nil {
		t.Fatalf("RunGroup() error = %v", err)
	}
	if result.Outcome != evidence.OutcomeSuccess || len(result.Children) != 4 {
		t.Fatalf("result = %#v", result)
	}
	for slot, child := range result.Children {
		if child.Target == nil || child.Target.ID == "" {
			t.Fatalf("child %d has no simulator target: %#v", slot, child)
		}
	}
	if len(fleet.EPPRecords()) != 4 || len(fleet.EnvoyRecords()) != 4 {
		t.Fatalf("EPP=%d Envoy=%d", len(fleet.EPPRecords()), len(fleet.EnvoyRecords()))
	}
}

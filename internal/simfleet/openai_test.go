package simfleet

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/JDinSeattle/velaserve/internal/bench"
	"github.com/JDinSeattle/velaserve/internal/evidence"
)

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

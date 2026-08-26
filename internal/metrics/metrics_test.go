package metrics

import (
	"testing"

	"github.com/JDinSeattle/velaserve/internal/evidence"
	"github.com/prometheus/client_golang/prometheus"
)

func TestMetricDescriptorsDoNotContainHighCardinalityLabels(t *testing.T) {
	forbidden := map[string]bool{"group_id": true, "request_id": true, "prompt_hash": true, "endpoint_ip": true, "fanout_width": true}
	for _, descriptor := range Descriptors() {
		for _, label := range descriptor.Labels {
			if forbidden[label] {
				t.Fatalf("metric %s contains forbidden label %s", descriptor.Name, label)
			}
		}
	}
}

func TestRunCompletionMetricDoesNotClaimVerifiedArtifactCompletion(t *testing.T) {
	for _, descriptor := range Descriptors() {
		if descriptor.Name == "velaserve_zeroing_artifact_complete" {
			t.Fatal("raw runner must not claim final artifact verification")
		}
	}
}

func TestObserveGroupPublishesSpecNamedMetrics(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := New(registry)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	recomputed := uint64(4096)
	result := evidence.GroupResult{
		FanoutWidth: 2, MakespanSeconds: 1.2, SlowestChildTTFTSeconds: 0.4,
		RecomputedPrefixTokens: &recomputed, Outcome: evidence.OutcomeSuccess,
		Children: []evidence.ChildResult{
			{TTFTSeconds: 0.3, LatencySeconds: 1.0},
			{TTFTSeconds: 0.4, LatencySeconds: 1.2},
		},
	}
	metrics.ObserveGroup(result)
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, family := range families {
		seen[family.GetName()] = true
	}
	for _, name := range []string{
		"fanoutbench_group_makespan_seconds",
		"fanoutbench_slowest_child_ttft_seconds",
		"fanoutbench_child_ttft_seconds",
		"fanoutbench_child_request_latency_seconds",
		"fanoutbench_groups_total",
		"fanoutbench_recomputed_prefix_tokens",
	} {
		if !seen[name] {
			t.Fatalf("metric %s was not gathered; got %v", name, seen)
		}
	}
}

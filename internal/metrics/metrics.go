// Package metrics owns VelaServe's bounded-cardinality Prometheus collectors.
package metrics

import (
	"fmt"

	"github.com/JDinSeattle/velaserve/internal/evidence"
	"github.com/prometheus/client_golang/prometheus"
)

type Descriptor struct {
	Name   string
	Help   string
	Labels []string
}

var descriptors = []Descriptor{
	{Name: "fanoutbench_group_makespan_seconds", Help: "All-of-N group completion time in seconds."},
	{Name: "fanoutbench_slowest_child_ttft_seconds", Help: "Slowest child time to first content in seconds."},
	{Name: "fanoutbench_child_ttft_seconds", Help: "Child time to first content in seconds."},
	{Name: "fanoutbench_child_request_latency_seconds", Help: "Child terminal request latency in seconds."},
	{Name: "fanoutbench_groups_total", Help: "Completed benchmark groups by terminal outcome.", Labels: []string{"outcome"}},
	{Name: "fanoutbench_recomputed_prefix_tokens", Help: "Measured recomputed prefix tokens per group."},
	{Name: "fanoutbench_estimated_prefill_gpu_seconds", Help: "Derived prefill GPU seconds per group; not a hardware counter."},
	{Name: "velaserve_zeroing_oracle_regret_seconds", Help: "Frozen Arm B predicted makespan minus oracle predicted makespan."},
	{Name: "velaserve_zeroing_plan_preview_seconds", Help: "Offline N-aware plan-preview compute time."},
	{Name: "velaserve_zeroing_unmatched_records_total", Help: "Recorder inputs that could not be correlated."},
	{Name: "velaserve_zeroing_artifact_complete", Help: "Whether the latest verified artifact bundle is complete (1 or 0)."},
}

func Descriptors() []Descriptor {
	clone := make([]Descriptor, len(descriptors))
	for index, descriptor := range descriptors {
		clone[index] = descriptor
		clone[index].Labels = append([]string(nil), descriptor.Labels...)
	}
	return clone
}

type Metrics struct {
	groupMakespan    prometheus.Histogram
	slowestChildTTFT prometheus.Histogram
	childTTFT        prometheus.Histogram
	childLatency     prometheus.Histogram
	groups           *prometheus.CounterVec
	recomputedTokens prometheus.Histogram
	estimatedPrefill prometheus.Histogram
	oracleRegret     prometheus.Histogram
	planPreview      prometheus.Histogram
	unmatchedRecords prometheus.Counter
	artifactComplete prometheus.Gauge
}

func New(registerer prometheus.Registerer) (*Metrics, error) {
	if registerer == nil {
		return nil, fmt.Errorf("Prometheus registerer is required")
	}
	metrics := &Metrics{
		groupMakespan:    prometheus.NewHistogram(prometheus.HistogramOpts{Name: descriptors[0].Name, Help: descriptors[0].Help, Buckets: durationBuckets()}),
		slowestChildTTFT: prometheus.NewHistogram(prometheus.HistogramOpts{Name: descriptors[1].Name, Help: descriptors[1].Help, Buckets: durationBuckets()}),
		childTTFT:        prometheus.NewHistogram(prometheus.HistogramOpts{Name: descriptors[2].Name, Help: descriptors[2].Help, Buckets: durationBuckets()}),
		childLatency:     prometheus.NewHistogram(prometheus.HistogramOpts{Name: descriptors[3].Name, Help: descriptors[3].Help, Buckets: durationBuckets()}),
		groups:           prometheus.NewCounterVec(prometheus.CounterOpts{Name: descriptors[4].Name, Help: descriptors[4].Help}, descriptors[4].Labels),
		recomputedTokens: prometheus.NewHistogram(prometheus.HistogramOpts{Name: descriptors[5].Name, Help: descriptors[5].Help, Buckets: prometheus.ExponentialBuckets(256, 2, 10)}),
		estimatedPrefill: prometheus.NewHistogram(prometheus.HistogramOpts{Name: descriptors[6].Name, Help: descriptors[6].Help, Buckets: durationBuckets()}),
		oracleRegret:     prometheus.NewHistogram(prometheus.HistogramOpts{Name: descriptors[7].Name, Help: descriptors[7].Help, Buckets: durationBuckets()}),
		planPreview:      prometheus.NewHistogram(prometheus.HistogramOpts{Name: descriptors[8].Name, Help: descriptors[8].Help, Buckets: prometheus.ExponentialBuckets(0.0001, 2, 16)}),
		unmatchedRecords: prometheus.NewCounter(prometheus.CounterOpts{Name: descriptors[9].Name, Help: descriptors[9].Help}),
		artifactComplete: prometheus.NewGauge(prometheus.GaugeOpts{Name: descriptors[10].Name, Help: descriptors[10].Help}),
	}
	collectors := []prometheus.Collector{
		metrics.groupMakespan, metrics.slowestChildTTFT, metrics.childTTFT, metrics.childLatency,
		metrics.groups, metrics.recomputedTokens, metrics.estimatedPrefill, metrics.oracleRegret,
		metrics.planPreview, metrics.unmatchedRecords, metrics.artifactComplete,
	}
	for _, collector := range collectors {
		if err := registerer.Register(collector); err != nil {
			return nil, fmt.Errorf("register VelaServe metric: %w", err)
		}
	}
	return metrics, nil
}

func (metrics *Metrics) ObserveGroup(result evidence.GroupResult) {
	metrics.groupMakespan.Observe(result.MakespanSeconds)
	metrics.slowestChildTTFT.Observe(result.SlowestChildTTFTSeconds)
	for _, child := range result.Children {
		metrics.childTTFT.Observe(child.TTFTSeconds)
		metrics.childLatency.Observe(child.LatencySeconds)
	}
	switch result.Outcome {
	case evidence.OutcomeSuccess, evidence.OutcomeFailure, evidence.OutcomeCancelled:
		metrics.groups.WithLabelValues(string(result.Outcome)).Inc()
	}
	if result.RecomputedPrefixTokens != nil {
		metrics.recomputedTokens.Observe(float64(*result.RecomputedPrefixTokens))
	}
	if result.EstimatedPrefillSeconds != nil {
		metrics.estimatedPrefill.Observe(*result.EstimatedPrefillSeconds)
	}
}

func (metrics *Metrics) ObserveOracleRegret(seconds float64) {
	if seconds >= 0 {
		metrics.oracleRegret.Observe(seconds)
	}
}

func (metrics *Metrics) ObservePlanPreview(seconds float64) {
	if seconds >= 0 {
		metrics.planPreview.Observe(seconds)
	}
}

func (metrics *Metrics) AddUnmatchedRecords(count uint64) {
	metrics.unmatchedRecords.Add(float64(count))
}

func (metrics *Metrics) SetArtifactComplete(complete bool) {
	if complete {
		metrics.artifactComplete.Set(1)
		return
	}
	metrics.artifactComplete.Set(0)
}

func durationBuckets() []float64 {
	return prometheus.ExponentialBuckets(0.001, 2, 16)
}

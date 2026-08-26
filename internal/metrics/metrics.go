// Package metrics owns VelaServe's bounded-cardinality Prometheus collectors.
package metrics

import (
	"fmt"
	"strconv"

	"github.com/JDinSeattle/velaserve/internal/evidence"
	"github.com/prometheus/client_golang/prometheus"
)

type Descriptor struct {
	Name   string
	Help   string
	Labels []string
}

var descriptors = []Descriptor{
	{Name: "fanoutbench_group_makespan_seconds", Help: "All-of-N group completion time in seconds.", Labels: conditionLabels()},
	{Name: "fanoutbench_slowest_child_ttft_seconds", Help: "Slowest child time to first content in seconds.", Labels: conditionLabels()},
	{Name: "fanoutbench_child_ttft_seconds", Help: "Child time to first content in seconds.", Labels: conditionLabels()},
	{Name: "fanoutbench_child_request_latency_seconds", Help: "Child terminal request latency in seconds.", Labels: conditionLabels()},
	{Name: "fanoutbench_groups_total", Help: "Completed benchmark groups by terminal outcome.", Labels: append(conditionLabels(), "outcome")},
	{Name: "fanoutbench_recomputed_prefix_tokens", Help: "Measured recomputed prefix tokens per group.", Labels: conditionLabels()},
	{Name: "fanoutbench_estimated_prefill_gpu_seconds", Help: "Derived prefill GPU seconds per group; not a hardware counter.", Labels: conditionLabels()},
	{Name: "velaserve_zeroing_run_complete", Help: "Whether the raw benchmark run completed every selected group (1 or 0); this does not claim artifact verification."},
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
	groupMakespan    *prometheus.HistogramVec
	slowestChildTTFT *prometheus.HistogramVec
	childTTFT        *prometheus.HistogramVec
	childLatency     *prometheus.HistogramVec
	groups           *prometheus.CounterVec
	recomputedTokens *prometheus.HistogramVec
	estimatedPrefill *prometheus.HistogramVec
	runComplete      prometheus.Gauge
}

func New(registerer prometheus.Registerer) (*Metrics, error) {
	if registerer == nil {
		return nil, fmt.Errorf("Prometheus registerer is required")
	}
	metrics := &Metrics{
		groupMakespan:    prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: descriptors[0].Name, Help: descriptors[0].Help, Buckets: durationBuckets()}, descriptors[0].Labels),
		slowestChildTTFT: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: descriptors[1].Name, Help: descriptors[1].Help, Buckets: durationBuckets()}, descriptors[1].Labels),
		childTTFT:        prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: descriptors[2].Name, Help: descriptors[2].Help, Buckets: durationBuckets()}, descriptors[2].Labels),
		childLatency:     prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: descriptors[3].Name, Help: descriptors[3].Help, Buckets: durationBuckets()}, descriptors[3].Labels),
		groups:           prometheus.NewCounterVec(prometheus.CounterOpts{Name: descriptors[4].Name, Help: descriptors[4].Help}, descriptors[4].Labels),
		recomputedTokens: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: descriptors[5].Name, Help: descriptors[5].Help, Buckets: prometheus.ExponentialBuckets(256, 2, 16)}, descriptors[5].Labels),
		estimatedPrefill: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: descriptors[6].Name, Help: descriptors[6].Help, Buckets: durationBuckets()}, descriptors[6].Labels),
		runComplete:      prometheus.NewGauge(prometheus.GaugeOpts{Name: descriptors[7].Name, Help: descriptors[7].Help}),
	}
	collectors := []prometheus.Collector{
		metrics.groupMakespan, metrics.slowestChildTTFT, metrics.childTTFT, metrics.childLatency,
		metrics.groups, metrics.recomputedTokens, metrics.estimatedPrefill, metrics.runComplete,
	}
	for _, collector := range collectors {
		if err := registerer.Register(collector); err != nil {
			return nil, fmt.Errorf("register VelaServe metric: %w", err)
		}
	}
	return metrics, nil
}

func (metrics *Metrics) ObserveGroup(result evidence.GroupResult) {
	labels := conditionLabelValues(result)
	metrics.groupMakespan.WithLabelValues(labels...).Observe(result.MakespanSeconds)
	metrics.slowestChildTTFT.WithLabelValues(labels...).Observe(result.SlowestChildTTFTSeconds)
	for _, child := range result.Children {
		metrics.childTTFT.WithLabelValues(labels...).Observe(child.TTFTSeconds)
		metrics.childLatency.WithLabelValues(labels...).Observe(child.LatencySeconds)
	}
	switch result.Outcome {
	case evidence.OutcomeSuccess, evidence.OutcomeFailure, evidence.OutcomeCancelled:
		metrics.groups.WithLabelValues(append(labels, string(result.Outcome))...).Inc()
	}
	if result.RecomputedPrefixTokens != nil {
		metrics.recomputedTokens.WithLabelValues(labels...).Observe(float64(*result.RecomputedPrefixTokens))
	}
	if result.EstimatedPrefillSeconds != nil {
		metrics.estimatedPrefill.WithLabelValues(labels...).Observe(*result.EstimatedPrefillSeconds)
	}
}

func (metrics *Metrics) SetRunComplete(complete bool) {
	if complete {
		metrics.runComplete.Set(1)
		return
	}
	metrics.runComplete.Set(0)
}

func durationBuckets() []float64 {
	return prometheus.ExponentialBuckets(0.001, 2, 19)
}

func conditionLabels() []string {
	return []string{"arm", "width", "load", "prefix", "transport"}
}

func conditionLabelValues(result evidence.GroupResult) []string {
	return []string{string(result.Arm), strconv.FormatUint(uint64(result.FanoutWidth), 10), string(result.Cell.LoadRegime), result.Cell.PrefixRegime, result.Cell.Transport}
}

// Package trace defines bounded OpenTelemetry spans for the Stage 1 research
// lifecycle. Request and group identities are permitted here, never as metric
// labels.
package trace

import (
	"context"
	"time"
	"unicode/utf8"

	"github.com/JDinSeattle/velaserve/internal/evidence"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type Limits struct {
	MaxAttributeBytes int
}

type Recorder struct {
	tracer            trace.Tracer
	maxAttributeBytes int
}

func New(tracer trace.Tracer, limits Limits) Recorder {
	maximum := limits.MaxAttributeBytes
	if maximum <= 0 || maximum > 4096 {
		maximum = 256
	}
	return Recorder{tracer: tracer, maxAttributeBytes: maximum}
}

// RecordGroup exports only intervals that the benchmark actually observed.
// It intentionally does not synthesize planner, queue, or KV-transfer spans.
func (recorder Recorder) RecordGroup(ctx context.Context, result evidence.GroupResult) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	start, end, ok := observedInterval(result.Children)
	if !ok {
		return false
	}
	groupContext, parent := recorder.tracer.Start(ctx, "fanout-group", trace.WithAttributes(
		attribute.String("velaserve.run_id", recorder.bound(result.RunID)),
		attribute.String("velaserve.group_id", recorder.bound(result.GroupID)),
		attribute.String("velaserve.outcome", string(result.Outcome)),
	), trace.WithTimestamp(start))
	for _, childResult := range result.Children {
		if childResult.DispatchedAt == nil || childResult.CompletedAt == nil || childResult.CompletedAt.Before(*childResult.DispatchedAt) {
			continue
		}
		_, child := recorder.tracer.Start(groupContext, "inference-request", trace.WithAttributes(
			attribute.String("velaserve.request_id", recorder.bound(childResult.RequestID)),
			attribute.String("velaserve.outcome", string(childResult.Outcome)),
		), trace.WithTimestamp(childResult.DispatchedAt.UTC()))
		if childResult.FirstTokenAt != nil && !childResult.FirstTokenAt.Before(*childResult.DispatchedAt) && !childResult.FirstTokenAt.After(*childResult.CompletedAt) {
			child.AddEvent("first-content", trace.WithTimestamp(childResult.FirstTokenAt.UTC()))
		}
		child.End(trace.WithTimestamp(childResult.CompletedAt.UTC()))
	}
	parent.End(trace.WithTimestamp(end))
	return true
}

func observedInterval(children []evidence.ChildResult) (time.Time, time.Time, bool) {
	var start, end time.Time
	for _, child := range children {
		if child.DispatchedAt == nil || child.CompletedAt == nil || child.DispatchedAt.IsZero() || child.CompletedAt.IsZero() || child.CompletedAt.Before(*child.DispatchedAt) {
			return time.Time{}, time.Time{}, false
		}
		if start.IsZero() || child.DispatchedAt.Before(start) {
			start = child.DispatchedAt.UTC()
		}
		if end.IsZero() || child.CompletedAt.After(end) {
			end = child.CompletedAt.UTC()
		}
	}
	return start, end, !start.IsZero() && !end.IsZero()
}

func (recorder Recorder) bound(value string) string {
	if len(value) <= recorder.maxAttributeBytes {
		return value
	}
	cut := recorder.maxAttributeBytes
	for cut > 0 && !utf8.ValidString(value[:cut]) {
		cut--
	}
	return value[:cut]
}

// Package trace defines bounded OpenTelemetry spans for the Stage 1 research
// lifecycle. Request and group identities are permitted here, never as metric
// labels.
package trace

import (
	"context"
	"strings"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

var RequiredOperations = []string{
	"plan-preview",
	"scheduling",
	"queue-wait",
	"kv-acquisition",
	"inference",
	"stream-completion",
}

type Limits struct {
	MaxAttributeBytes int
}

type GroupTrace struct {
	RunID      string
	GroupID    string
	RequestIDs []string
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

// RecordLifecycle emits a parent and the Stage 1 child-span topology. Runtime
// integrations may add events and duration around these operations while
// preserving the same bounded attribute contract.
func (recorder Recorder) RecordLifecycle(ctx context.Context, group GroupTrace) {
	if ctx == nil {
		ctx = context.Background()
	}
	requestIDs := strings.Join(group.RequestIDs, ",")
	groupContext, parent := recorder.tracer.Start(ctx, "fanout-group", trace.WithAttributes(
		attribute.String("velaserve.run_id", recorder.bound(group.RunID)),
		attribute.String("velaserve.group_id", recorder.bound(group.GroupID)),
		attribute.String("velaserve.request_ids", recorder.bound(requestIDs)),
	))
	for _, operation := range RequiredOperations {
		_, child := recorder.tracer.Start(groupContext, operation, trace.WithAttributes(
			attribute.String("velaserve.operation", recorder.bound(operation)),
		))
		child.End()
	}
	parent.End()
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

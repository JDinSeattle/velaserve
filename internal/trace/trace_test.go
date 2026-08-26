package trace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JDinSeattle/velaserve/internal/evidence"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestGroupTraceUsesObservedRequestIntervals(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	recorder := New(provider.Tracer("velaserve-test"), Limits{MaxAttributeBytes: 128})
	result := tracedGroup()
	if !recorder.RecordGroup(context.Background(), result) {
		t.Fatal("RecordGroup() rejected complete observed intervals")
	}
	spans := exporter.GetSpans()
	requestSpans := 0
	for _, span := range spans {
		if span.Name == "inference-request" {
			requestSpans++
			if span.EndTime.Sub(span.StartTime) != time.Second {
				t.Fatalf("request span duration = %s, want observed one second", span.EndTime.Sub(span.StartTime))
			}
		}
	}
	if requestSpans != 2 {
		t.Fatalf("request spans = %d, want 2", requestSpans)
	}
}

func TestGroupTraceRefusesMissingRuntimeTimestamps(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	if New(provider.Tracer("test"), Limits{}).RecordGroup(context.Background(), evidence.GroupResult{Children: []evidence.ChildResult{{RequestID: "missing"}}}) {
		t.Fatal("RecordGroup() synthesized an interval without runtime timestamps")
	}
	if len(exporter.GetSpans()) != 0 {
		t.Fatal("missing intervals produced synthetic spans")
	}
}

func TestJSONLExporterWritesRuntimeSpans(t *testing.T) {
	path := filepath.Join(t.TempDir(), "traces.jsonl")
	exporter, err := NewJSONLExporter(path)
	if err != nil {
		t.Fatal(err)
	}
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	New(provider.Tracer("test"), Limits{}).RecordGroup(context.Background(), tracedGroup())
	if err := provider.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), `"name":"fanout-group"`) {
		t.Fatalf("traces = %s", contents)
	}
}

func TestTraceAttributesAreBounded(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	recorder := New(provider.Tracer("velaserve-test"), Limits{MaxAttributeBytes: 8})
	result := tracedGroup()
	result.RunID = "run-long-value"
	result.GroupID = "group-long-value"
	result.Children[0].RequestID = "request-long-value"
	recorder.RecordGroup(context.Background(), result)
	for _, span := range exporter.GetSpans() {
		for _, attribute := range span.Attributes {
			if len(attribute.Value.AsString()) > 8 {
				t.Fatalf("attribute %s was not bounded: %q", attribute.Key, attribute.Value.AsString())
			}
		}
	}
}

func tracedGroup() evidence.GroupResult {
	start := time.Date(2026, 8, 26, 8, 0, 0, 0, time.UTC)
	secondStart := start.Add(time.Millisecond)
	firstToken := start.Add(100 * time.Millisecond)
	firstEnd := start.Add(time.Second)
	secondEnd := secondStart.Add(time.Second)
	return evidence.GroupResult{
		RunID: "run-1", GroupID: "group-1", Outcome: evidence.OutcomeSuccess,
		Children: []evidence.ChildResult{
			{RequestID: "request-1", Outcome: evidence.OutcomeSuccess, DispatchedAt: &start, FirstTokenAt: &firstToken, CompletedAt: &firstEnd},
			{RequestID: "request-2", Outcome: evidence.OutcomeSuccess, DispatchedAt: &secondStart, CompletedAt: &secondEnd},
		},
	}
}

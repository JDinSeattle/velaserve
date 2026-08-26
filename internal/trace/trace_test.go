package trace

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestGroupTraceContainsRequiredChildSpans(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	recorder := New(provider.Tracer("velaserve-test"), Limits{MaxAttributeBytes: 128})
	recorder.RecordLifecycle(context.Background(), GroupTrace{
		RunID:      "run-1",
		GroupID:    "group-1",
		RequestIDs: []string{"request-1", "request-2"},
	})
	spans := exporter.GetSpans()
	names := make([]string, 0, len(spans))
	for _, span := range spans {
		if span.Name != "fanout-group" {
			names = append(names, span.Name)
		}
	}
	sort.Strings(names)
	want := append([]string(nil), RequiredOperations...)
	sort.Strings(want)
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("child spans = %v, want %v", names, want)
	}
}

func TestJSONLExporterWritesRuntimeSpans(t *testing.T) {
	path := filepath.Join(t.TempDir(), "traces.jsonl")
	exporter, err := NewJSONLExporter(path)
	if err != nil {
		t.Fatal(err)
	}
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	New(provider.Tracer("test"), Limits{}).RecordLifecycle(context.Background(), GroupTrace{RunID: "run", GroupID: "group"})
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
	recorder.RecordLifecycle(context.Background(), GroupTrace{RunID: "run-long-value", GroupID: "group-long-value"})
	for _, span := range exporter.GetSpans() {
		for _, attribute := range span.Attributes {
			if len(attribute.Value.AsString()) > 8 {
				t.Fatalf("attribute %s was not bounded: %q", attribute.Key, attribute.Value.AsString())
			}
		}
	}
}

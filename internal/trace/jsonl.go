package trace

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type JSONLExporter struct {
	mu   sync.Mutex
	file *os.File
}

type spanRecord struct {
	TraceID      string            `json:"trace_id"`
	SpanID       string            `json:"span_id"`
	ParentSpanID string            `json:"parent_span_id,omitempty"`
	Name         string            `json:"name"`
	StartedAt    any               `json:"started_at"`
	EndedAt      any               `json:"ended_at"`
	Attributes   map[string]string `json:"attributes,omitempty"`
}

func NewJSONLExporter(path string) (*JSONLExporter, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &JSONLExporter{file: file}, nil
}

func (exporter *JSONLExporter) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	exporter.mu.Lock()
	defer exporter.mu.Unlock()
	if exporter.file == nil {
		return fmt.Errorf("trace exporter is closed")
	}
	encoder := json.NewEncoder(exporter.file)
	for _, span := range spans {
		record := spanRecord{TraceID: span.SpanContext().TraceID().String(), SpanID: span.SpanContext().SpanID().String(), Name: span.Name(), StartedAt: span.StartTime().UTC(), EndedAt: span.EndTime().UTC(), Attributes: map[string]string{}}
		if span.Parent().IsValid() {
			record.ParentSpanID = span.Parent().SpanID().String()
		}
		for _, attribute := range span.Attributes() {
			record.Attributes[string(attribute.Key)] = attribute.Value.Emit()
		}
		if err := encoder.Encode(record); err != nil {
			return err
		}
	}
	return exporter.file.Sync()
}

func (exporter *JSONLExporter) Shutdown(context.Context) error {
	exporter.mu.Lock()
	defer exporter.mu.Unlock()
	if exporter.file == nil {
		return nil
	}
	err := exporter.file.Close()
	exporter.file = nil
	return err
}

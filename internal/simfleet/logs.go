package simfleet

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// WriteLogs exports the normalized recorder inputs once. Existing files are
// never overwritten, preserving failed or interrupted simulator runs.
func (fleet *Fleet) WriteLogs(root string) error {
	if strings.TrimSpace(root) == "" {
		return fmt.Errorf("log directory is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return err
	}
	if err := writeJSONLExclusive(filepath.Join(resolved, "epp.jsonl"), func(encoder *json.Encoder) error {
		for _, record := range fleet.EPPRecords() {
			if err := encoder.Encode(record); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	return writeJSONLExclusive(filepath.Join(resolved, "envoy.jsonl"), func(encoder *json.Encoder) error {
		for _, record := range fleet.EnvoyRecords() {
			line := map[string]any{
				"START_TIME":               record.StartedAt.Format(time.RFC3339Nano),
				"REQ(X-REQUEST-ID)":        record.RequestID,
				"REQ(X-VELA-FANOUT-GROUP)": record.GroupID,
				"UPSTREAM_HOST":            record.UpstreamHost,
				"RESPONSE_CODE":            record.ResponseCode,
				"DURATION":                 record.DurationMS,
				"TRACE_ID":                 record.TraceID,
				"SPAN_ID":                  record.SpanID,
			}
			if err := encoder.Encode(line); err != nil {
				return err
			}
		}
		return nil
	})
}

func writeJSONLExclusive(path string, write func(*json.Encoder) error) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if err := write(json.NewEncoder(file)); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return nil
}

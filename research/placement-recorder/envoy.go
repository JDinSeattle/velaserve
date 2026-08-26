package placementrecorder

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

type EnvoyRecord struct {
	StartedAt    time.Time `json:"started_at"`
	RequestID    string    `json:"request_id"`
	GroupID      string    `json:"group_id"`
	UpstreamHost string    `json:"upstream_host"`
	ResponseCode uint32    `json:"response_code"`
	DurationMS   uint64    `json:"duration_ms"`
	TraceID      string    `json:"trace_id,omitempty"`
	SpanID       string    `json:"span_id,omitempty"`
}

// ParseEnvoy converts the pinned JSON access-log formatter into a stable
// recorder type. Numeric formatter values may be emitted as JSON numbers or
// strings by different Envoy configurations.
func ParseEnvoy(line []byte) (EnvoyRecord, error) {
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.UseNumber()
	var raw map[string]any
	if err := decoder.Decode(&raw); err != nil {
		return EnvoyRecord{}, fmt.Errorf("decode Envoy JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return EnvoyRecord{}, fmt.Errorf("decode Envoy JSON: trailing value")
		}
		return EnvoyRecord{}, fmt.Errorf("decode Envoy trailing JSON: %w", err)
	}

	requestID := stringValue(raw["REQ(X-REQUEST-ID)"])
	if requestID == "" {
		return EnvoyRecord{}, fmt.Errorf("missing request_id: REQ(X-REQUEST-ID)")
	}
	groupID := stringValue(raw["REQ(X-VELA-FANOUT-GROUP)"])
	if groupID == "" {
		return EnvoyRecord{}, fmt.Errorf("group_id: missing REQ(X-VELA-FANOUT-GROUP)")
	}
	startedText := stringValue(raw["START_TIME"])
	startedAt, err := time.Parse(time.RFC3339Nano, startedText)
	if err != nil {
		return EnvoyRecord{}, fmt.Errorf("start_time: %w", err)
	}
	upstreamHost := stringValue(raw["UPSTREAM_HOST"])
	if upstreamHost == "" || upstreamHost == "-" {
		return EnvoyRecord{}, fmt.Errorf("upstream_host: required")
	}
	responseCode, err := uintValue(raw["RESPONSE_CODE"])
	if err != nil || responseCode == 0 || responseCode > 999 {
		return EnvoyRecord{}, fmt.Errorf("response_code: invalid value")
	}
	duration, err := uintValue(raw["DURATION"])
	if err != nil {
		return EnvoyRecord{}, fmt.Errorf("duration_ms: %w", err)
	}
	return EnvoyRecord{
		StartedAt:    startedAt.UTC(),
		RequestID:    requestID,
		GroupID:      groupID,
		UpstreamHost: upstreamHost,
		ResponseCode: uint32(responseCode),
		DurationMS:   duration,
		TraceID:      stringValue(raw["TRACE_ID"]),
		SpanID:       stringValue(raw["SPAN_ID"]),
	}, nil
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return typed.String()
	default:
		return ""
	}
}

func uintValue(value any) (uint64, error) {
	text := stringValue(value)
	if text == "" {
		return 0, fmt.Errorf("required")
	}
	parsed, err := strconv.ParseUint(text, 10, 64)
	if err != nil {
		return 0, err
	}
	return parsed, nil
}

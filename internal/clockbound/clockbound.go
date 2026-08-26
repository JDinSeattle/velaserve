// Package clockbound provides an RTT-bounded wall-clock attestation between
// the local benchmark process and node-local probe pods.
package clockbound

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const (
	TimeResponseSchemaVersion = "velaserve.clock-response/v1"
	ObservationSchemaVersion  = "velaserve.clock-observation/v1"
	maxClockResponseBytes     = 4 << 10
)

type TimeResponse struct {
	SchemaVersion string    `json:"schema_version"`
	UnixNano      int64     `json:"unix_nano"`
	ObservedAt    time.Time `json:"observed_at"`
}

type Observation struct {
	SchemaVersion            string    `json:"schema_version"`
	Samples                  uint32    `json:"samples"`
	BestRTTNanoseconds       int64     `json:"best_rtt_nanoseconds"`
	OffsetMinimumNanoseconds int64     `json:"offset_minimum_nanoseconds"`
	OffsetMaximumNanoseconds int64     `json:"offset_maximum_nanoseconds"`
	MaximumRTTNanoseconds    int64     `json:"maximum_rtt_nanoseconds"`
	MaximumOffsetNanoseconds int64     `json:"maximum_offset_nanoseconds"`
	ObservedAt               time.Time `json:"observed_at"`
}

func Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = writer.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /v1/time", func(writer http.ResponseWriter, _ *http.Request) {
		writeTimeResponse(writer, time.Now().UTC())
	})
	return mux
}

func writeTimeResponse(writer http.ResponseWriter, observedAt time.Time) {
	observedAt = observedAt.UTC()
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(TimeResponse{
		SchemaVersion: TimeResponseSchemaVersion, UnixNano: observedAt.UnixNano(), ObservedAt: observedAt,
	})
}

// Check takes multiple samples and retains the smallest-RTT interval. If the
// clocks are synchronized, the remote timestamp must lie between the local
// send and receive timestamps. A tolerated offset expands that interval; both
// conservative endpoints must remain within the configured bound.
func Check(ctx context.Context, client *http.Client, endpoint string, samples uint32, maximumRTT, maximumOffset time.Duration) (Observation, error) {
	if ctx == nil {
		return Observation{}, fmt.Errorf("context is required")
	}
	parsed, err := url.ParseRequestURI(endpoint)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return Observation{}, fmt.Errorf("clock endpoint must be an absolute HTTP(S) URL without user information")
	}
	if samples < 3 || samples > 20 || maximumRTT <= 0 || maximumOffset <= 0 {
		return Observation{}, fmt.Errorf("clock check requires 3..20 samples and positive RTT/offset bounds")
	}
	if client == nil {
		client = http.DefaultClient
	}
	result := Observation{
		SchemaVersion: ObservationSchemaVersion, Samples: samples, BestRTTNanoseconds: int64(maximumRTT) + 1,
		MaximumRTTNanoseconds: int64(maximumRTT), MaximumOffsetNanoseconds: int64(maximumOffset),
	}
	for sample := uint32(0); sample < samples; sample++ {
		requestContext, cancel := context.WithTimeout(ctx, maximumRTT)
		startedAt := time.Now().UTC()
		request, requestErr := http.NewRequestWithContext(requestContext, http.MethodGet, endpoint, nil)
		if requestErr != nil {
			cancel()
			return Observation{}, requestErr
		}
		response, requestErr := client.Do(request)
		completedAt := time.Now().UTC()
		cancel()
		if requestErr != nil {
			return Observation{}, fmt.Errorf("clock sample %d: %w", sample+1, requestErr)
		}
		contents, readErr := io.ReadAll(io.LimitReader(response.Body, maxClockResponseBytes+1))
		closeErr := response.Body.Close()
		if readErr != nil {
			return Observation{}, fmt.Errorf("clock sample %d: %w", sample+1, readErr)
		}
		if closeErr != nil {
			return Observation{}, fmt.Errorf("clock sample %d close response: %w", sample+1, closeErr)
		}
		if response.StatusCode != http.StatusOK || len(contents) > maxClockResponseBytes {
			return Observation{}, fmt.Errorf("clock sample %d returned status %s or an oversized body", sample+1, response.Status)
		}
		decoder := json.NewDecoder(bytes.NewReader(contents))
		decoder.DisallowUnknownFields()
		var remote TimeResponse
		if err := decoder.Decode(&remote); err != nil {
			return Observation{}, fmt.Errorf("clock sample %d response: %w", sample+1, err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			return Observation{}, fmt.Errorf("clock sample %d response contains trailing JSON", sample+1)
		}
		if remote.SchemaVersion != TimeResponseSchemaVersion || remote.UnixNano <= 0 || remote.ObservedAt.IsZero() || remote.UnixNano != remote.ObservedAt.UnixNano() {
			return Observation{}, fmt.Errorf("clock sample %d response is internally inconsistent", sample+1)
		}
		rtt := completedAt.Sub(startedAt)
		if rtt <= 0 || rtt > maximumRTT {
			return Observation{}, fmt.Errorf("clock sample %d RTT %s exceeds %s", sample+1, rtt, maximumRTT)
		}
		if int64(rtt) < result.BestRTTNanoseconds {
			result.BestRTTNanoseconds = int64(rtt)
			result.OffsetMinimumNanoseconds = remote.UnixNano - completedAt.UnixNano()
			result.OffsetMaximumNanoseconds = remote.UnixNano - startedAt.UnixNano()
			result.ObservedAt = completedAt
		}
	}
	if result.OffsetMinimumNanoseconds < -int64(maximumOffset) || result.OffsetMaximumNanoseconds > int64(maximumOffset) {
		return Observation{}, fmt.Errorf("remote clock offset interval [%d,%d]ns exceeds +/-%dns", result.OffsetMinimumNanoseconds, result.OffsetMaximumNanoseconds, maximumOffset)
	}
	return result, nil
}

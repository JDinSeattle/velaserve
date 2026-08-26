package placementrecorder

import (
	"strings"
	"testing"
)

func TestParseEnvoyAcceptsPinnedJSONAccessLogFields(t *testing.T) {
	line := []byte(`{"START_TIME":"2026-08-26T08:00:00Z","REQ(X-REQUEST-ID)":"request-01","REQ(X-VELA-FANOUT-GROUP)":"01K39J6FJ4N5W7V57QK9Q0C6CF","UPSTREAM_HOST":"sim-0","RESPONSE_CODE":200,"DURATION":17,"TRACE_ID":"trace-01","SPAN_ID":"span-01"}`)
	record, err := ParseEnvoy(line)
	if err != nil {
		t.Fatalf("ParseEnvoy() error = %v", err)
	}
	if record.RequestID != "request-01" || record.GroupID == "" || record.DurationMS != 17 || record.UpstreamHost != "sim-0" {
		t.Fatalf("record = %#v", record)
	}
}

func TestParseEnvoyRejectsMissingRequestID(t *testing.T) {
	_, err := ParseEnvoy([]byte(`{"START_TIME":"2026-08-26T08:00:00Z"}`))
	if err == nil || !strings.Contains(err.Error(), "request_id") {
		t.Fatalf("ParseEnvoy() error = %v", err)
	}
}

func TestParseEnvoyRejectsMalformedTrailingJSON(t *testing.T) {
	line := []byte(`{"START_TIME":"2026-08-26T08:00:00Z","REQ(X-REQUEST-ID)":"request-01","REQ(X-VELA-FANOUT-GROUP)":"01K39J6FJ4N5W7V57QK9Q0C6CF","UPSTREAM_HOST":"sim-0","RESPONSE_CODE":200,"DURATION":17} {`)
	if _, err := ParseEnvoy(line); err == nil {
		t.Fatal("ParseEnvoy() accepted malformed trailing JSON")
	}
}

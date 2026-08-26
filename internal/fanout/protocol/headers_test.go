package protocol

import (
	"bytes"
	"net/http"
	"testing"
	"time"
)

func TestNewGroupProducesMinimalVersionedHeadersAndDistinctRequestIDs(t *testing.T) {
	entropy := bytes.NewReader(bytes.Repeat([]byte{0x4d}, 16*9))
	group, err := NewGroupWithEntropy(8, 16, time.Date(2026, 8, 25, 20, 0, 0, 0, time.UTC), entropy)
	if err != nil {
		t.Fatalf("NewGroupWithEntropy() error = %v", err)
	}
	if len(group.GroupID) != 26 {
		t.Fatalf("group ID length = %d, want 26", len(group.GroupID))
	}
	seen := map[string]struct{}{}
	for slot := uint32(0); slot < group.Width; slot++ {
		headers, err := group.Headers(slot)
		if err != nil {
			t.Fatal(err)
		}
		if headers.Get(HeaderVersion) != "1" || headers.Get(HeaderGroup) != group.GroupID || headers.Get(HeaderWidth) != "8" {
			t.Fatalf("slot %d headers = %#v", slot, headers)
		}
		requestID := headers.Get(HeaderRequestID)
		if _, exists := seen[requestID]; exists {
			t.Fatalf("duplicate request ID %q", requestID)
		}
		seen[requestID] = struct{}{}
		if len(headers) != 4 {
			t.Fatalf("header count = %d, want three Vela headers plus X-Request-ID", len(headers))
		}
	}
}

func TestParseHeadersEnablesValidFanoutContext(t *testing.T) {
	headers := http.Header{
		HeaderVersion:   []string{"1"},
		HeaderGroup:     []string{"01K39J6FJ4N5W7V57QK9Q0C6CF"},
		HeaderWidth:     []string{"8"},
		HeaderRequestID: []string{"request-8"},
	}
	deadline := time.Date(2026, 8, 25, 20, 0, 10, 0, time.UTC)
	result := ParseHeaders(headers, "prefix-sha256", 16, deadline)
	if result.Context == nil || result.Reason != "" {
		t.Fatalf("ParseHeaders() = %#v", result)
	}
	if result.Context.Width != 8 || result.Context.Protocol != 1 || result.Context.PrefixKey != "prefix-sha256" {
		t.Fatalf("context = %#v", result.Context)
	}
}

func TestParseHeadersDisablesInvalidMetadataWithoutRequestError(t *testing.T) {
	headers := http.Header{
		HeaderVersion:   []string{"1"},
		HeaderGroup:     []string{"guessable spaces"},
		HeaderWidth:     []string{"128"},
		HeaderRequestID: []string{"request-1"},
	}
	result := ParseHeaders(headers, "prefix", 16, time.Now().Add(time.Second))
	if result.Context != nil {
		t.Fatalf("invalid metadata produced context %#v", result.Context)
	}
	if result.Reason == "" {
		t.Fatal("invalid metadata did not record a fallback reason")
	}
}

func TestParseHeadersRejectsShortGuessableGroupID(t *testing.T) {
	headers := http.Header{
		HeaderVersion:   []string{"1"},
		HeaderGroup:     []string{"group-1"},
		HeaderWidth:     []string{"2"},
		HeaderRequestID: []string{"request-1"},
	}
	result := ParseHeaders(headers, "prefix", 16, time.Now().Add(time.Second))
	if result.Context != nil || result.Reason == "" {
		t.Fatalf("short group ID should fail open, got %#v", result)
	}
}

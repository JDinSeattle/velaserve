package protocol

import (
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

const (
	HeaderVersion   = "X-Vela-Fanout-Version"
	HeaderGroup     = "X-Vela-Fanout-Group"
	HeaderWidth     = "X-Vela-Fanout-Width"
	HeaderRequestID = "X-Request-ID"

	protocolVersion = uint16(1)
)

var (
	opaqueGroupIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)
	opaqueRequestPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
)

// Group is an immutable identity shared by every sibling in an all-of-N
// benchmark request. Request IDs are generated up front so retries cannot
// accidentally reuse a sibling's identity.
type Group struct {
	GroupID   string
	Width     uint32
	requestID []string
}

// NewGroup creates a group using cryptographically secure entropy.
func NewGroup(width, maxWidth uint32) (Group, error) {
	return NewGroupWithEntropy(width, maxWidth, time.Now().UTC(), rand.Reader)
}

// NewGroupWithEntropy is the deterministic test seam for NewGroup.
func NewGroupWithEntropy(width, maxWidth uint32, now time.Time, entropy io.Reader) (Group, error) {
	if maxWidth == 0 {
		return Group{}, fmt.Errorf("max width must be positive")
	}
	if width == 0 || width > maxWidth {
		return Group{}, fmt.Errorf("fan-out width %d is outside [1,%d]", width, maxWidth)
	}
	if now.IsZero() {
		return Group{}, fmt.Errorf("timestamp is required")
	}
	if entropy == nil {
		return Group{}, fmt.Errorf("entropy source is required")
	}

	monotonic := ulid.Monotonic(entropy, 0)
	newID := func() (string, error) {
		id, err := ulid.New(ulid.Timestamp(now), monotonic)
		if err != nil {
			return "", fmt.Errorf("generate ULID: %w", err)
		}
		return id.String(), nil
	}

	groupID, err := newID()
	if err != nil {
		return Group{}, err
	}
	requestIDs := make([]string, width)
	for slot := range requestIDs {
		requestIDs[slot], err = newID()
		if err != nil {
			return Group{}, err
		}
	}
	return Group{GroupID: groupID, Width: width, requestID: requestIDs}, nil
}

// Headers returns the exact versioned fan-out metadata for one sibling.
func (group Group) Headers(slot uint32) (http.Header, error) {
	if slot >= group.Width || int(slot) >= len(group.requestID) {
		return nil, fmt.Errorf("slot %d is outside group width %d", slot, group.Width)
	}
	if !opaqueGroupIDPattern.MatchString(group.GroupID) {
		return nil, fmt.Errorf("group ID is invalid")
	}
	if !opaqueRequestPattern.MatchString(group.requestID[slot]) {
		return nil, fmt.Errorf("request ID for slot %d is invalid", slot)
	}

	headers := make(http.Header, 4)
	headers.Set(HeaderVersion, strconv.FormatUint(uint64(protocolVersion), 10))
	headers.Set(HeaderGroup, group.GroupID)
	headers.Set(HeaderWidth, strconv.FormatUint(uint64(group.Width), 10))
	headers.Set(HeaderRequestID, group.requestID[slot])
	return headers, nil
}

// FanoutContext contains trusted request-scoped metadata. It is constructed
// only after all external header values have passed the fail-open parser.
type FanoutContext struct {
	GroupID   string
	Width     uint32
	PrefixKey string
	RequestID string
	Deadline  time.Time
	Protocol  uint16
}

// ParseResult deliberately has no error return. Invalid client metadata must
// disable Vela-specific coordination without rejecting an otherwise valid
// inference request.
type ParseResult struct {
	Context *FanoutContext
	Reason  string
}

// ParseHeaders validates the minimal external protocol and either enables a
// fan-out context or records why ordinary routing should be used instead.
func ParseHeaders(headers http.Header, prefixKey string, maxWidth uint32, deadline time.Time) ParseResult {
	if headers == nil {
		return disabled("fan-out headers are absent")
	}
	versionText, ok := singleHeader(headers, HeaderVersion)
	if !ok {
		return disabled("fan-out protocol version must appear exactly once")
	}
	version, err := strconv.ParseUint(versionText, 10, 16)
	if err != nil || uint16(version) != protocolVersion {
		return disabled("unsupported fan-out protocol version")
	}
	groupID, ok := singleHeader(headers, HeaderGroup)
	if !ok || !opaqueGroupIDPattern.MatchString(groupID) {
		return disabled("invalid fan-out group ID")
	}
	widthText, ok := singleHeader(headers, HeaderWidth)
	if !ok {
		return disabled("fan-out width must appear exactly once")
	}
	width, err := strconv.ParseUint(widthText, 10, 32)
	if err != nil || width == 0 || maxWidth == 0 || width > uint64(maxWidth) {
		return disabled("fan-out width is outside the configured limit")
	}
	requestID, ok := singleHeader(headers, HeaderRequestID)
	if !ok || !opaqueRequestPattern.MatchString(requestID) {
		return disabled("invalid request ID")
	}
	prefixKey = strings.TrimSpace(prefixKey)
	if prefixKey == "" || len(prefixKey) > 512 {
		return disabled("invalid prefix key")
	}
	if deadline.IsZero() {
		return disabled("request deadline is unavailable")
	}

	return ParseResult{Context: &FanoutContext{
		GroupID:   groupID,
		Width:     uint32(width),
		PrefixKey: prefixKey,
		RequestID: requestID,
		Deadline:  deadline,
		Protocol:  uint16(version),
	}}
}

func singleHeader(headers http.Header, name string) (string, bool) {
	// Real net/http traffic canonicalizes field names, while tests and adapters
	// may construct Header maps directly. HTTP field names are case-insensitive,
	// so collect exact semantic matches without relying on map canonicalization.
	values := make([]string, 0, 1)
	for candidate, candidateValues := range headers {
		if strings.EqualFold(candidate, name) {
			values = append(values, candidateValues...)
		}
	}
	if len(values) != 1 {
		return "", false
	}
	value := strings.TrimSpace(values[0])
	return value, value != ""
}

func disabled(reason string) ParseResult {
	return ParseResult{Reason: reason}
}

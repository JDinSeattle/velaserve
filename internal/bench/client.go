// Package bench drives OpenAI-compatible all-of-N benchmark groups and
// produces failure-preserving evidence records.
package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/JDinSeattle/velaserve/internal/evidence"
	"github.com/JDinSeattle/velaserve/internal/fanout/protocol"
	openaiwire "github.com/JDinSeattle/velaserve/internal/openai"
)

const maxErrorBodyBytes = 64 << 10

const (
	HeaderSimulatorArm           = "X-Sim-Arm"
	HeaderSimulatorSlot          = "X-Sim-Slot"
	HeaderSimulatorArrivalSkewMS = "X-Sim-Arrival-Skew-Ms"
	HeaderSimulatorTarget        = "X-Sim-Target"
)

type Client struct {
	Endpoint      string
	HTTPClient    *http.Client
	MaxEventBytes int
	Now           func() time.Time
	SimulatorMode bool
}

type GroupRequest struct {
	RunID        string
	Arm          evidence.Arm
	Model        string
	CommonPrefix string
	Suffixes     []string
	MaxTokens    uint32
	MaxWidth     uint32
	ArrivalSkew  time.Duration
	Timeout      time.Duration
	Cell         evidence.BenchmarkCell
}

// RunGroup sends every sibling and waits for every terminal result. Per-child
// failures are data, not method errors; callers still receive a schema-valid
// group record that can be appended to the experiment ledger.
func (client Client) RunGroup(ctx context.Context, request GroupRequest) (evidence.GroupResult, error) {
	if err := client.validate(request); err != nil {
		return evidence.GroupResult{}, err
	}
	if ctx == nil {
		return evidence.GroupResult{}, fmt.Errorf("context is required")
	}

	group, err := protocol.NewGroup(uint32(len(request.Suffixes)), request.MaxWidth)
	if err != nil {
		return evidence.GroupResult{}, fmt.Errorf("create fan-out group: %w", err)
	}
	children := make([]evidence.ChildResult, len(request.Suffixes))

	var waitGroup sync.WaitGroup
	waitGroup.Add(len(children))
	for slot, suffix := range request.Suffixes {
		slot := slot
		suffix := suffix
		go func() {
			defer waitGroup.Done()
			if delay := time.Duration(slot) * request.ArrivalSkew; delay > 0 {
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					if !timer.Stop() {
						<-timer.C
					}
					children[slot] = client.cancelledBeforeDispatch(group, uint32(slot), ctx.Err())
					return
				case <-timer.C:
				}
			}
			children[slot] = client.runChild(ctx, group, uint32(slot), request, suffix)
		}()
	}
	waitGroup.Wait()

	result := summarizeGroup(request, group.GroupID, children)
	if err := evidence.ValidateGroupResult(result); err != nil {
		return evidence.GroupResult{}, fmt.Errorf("internal group evidence is invalid: %w", err)
	}
	return result, nil
}

func (client Client) validate(request GroupRequest) error {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(client.Endpoint))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return fmt.Errorf("endpoint must be an absolute HTTP(S) URL without user information")
	}
	if strings.TrimSpace(request.RunID) == "" {
		return fmt.Errorf("run ID is required")
	}
	if strings.TrimSpace(string(request.Arm)) == "" {
		return fmt.Errorf("arm is required")
	}
	if strings.TrimSpace(request.Model) == "" {
		return fmt.Errorf("model is required")
	}
	if strings.TrimSpace(request.CommonPrefix) == "" {
		return fmt.Errorf("common prefix is required")
	}
	width := uint32(len(request.Suffixes))
	if width == 0 || request.MaxWidth == 0 || width > request.MaxWidth {
		return fmt.Errorf("fan-out width %d is outside [1,%d]", width, request.MaxWidth)
	}
	switch width {
	case 1, 2, 4, 8, 16:
	default:
		return fmt.Errorf("fan-out width %d is outside the preregistered matrix", width)
	}
	for slot, suffix := range request.Suffixes {
		if strings.TrimSpace(suffix) == "" {
			return fmt.Errorf("suffix %d is empty", slot)
		}
	}
	if request.MaxTokens == 0 {
		return fmt.Errorf("max tokens must be positive")
	}
	if request.Timeout <= 0 {
		return fmt.Errorf("per-child timeout must be positive")
	}
	if request.ArrivalSkew < 0 {
		return fmt.Errorf("arrival skew must not be negative")
	}
	if request.Cell.MaxTokens != request.MaxTokens {
		return fmt.Errorf("benchmark cell max_tokens %d does not match request max_tokens %d", request.Cell.MaxTokens, request.MaxTokens)
	}
	if request.ArrivalSkew%time.Millisecond != 0 || request.Cell.ArrivalSkewMS != uint32(request.ArrivalSkew/time.Millisecond) {
		return fmt.Errorf("benchmark cell arrival_skew_ms does not match request arrival skew")
	}
	switch request.ArrivalSkew {
	case 0, time.Millisecond, 5 * time.Millisecond, 20 * time.Millisecond:
	default:
		return fmt.Errorf("arrival skew %s is outside the preregistered matrix", request.ArrivalSkew)
	}
	if client.MaxEventBytes < 0 {
		return fmt.Errorf("maximum event size must not be negative")
	}
	return nil
}

func (client Client) runChild(
	parent context.Context,
	group protocol.Group,
	slot uint32,
	groupRequest GroupRequest,
	suffix string,
) evidence.ChildResult {
	headers, err := group.Headers(slot)
	if err != nil {
		return failureWithoutDispatch("unknown", fmt.Errorf("build fan-out headers: %w", err), client.now())
	}
	requestID := headers.Get(protocol.HeaderRequestID)
	payload, err := json.Marshal(chatCompletionRequest{
		Model: groupRequest.Model,
		Messages: []chatMessage{{
			Role:    "user",
			Content: groupRequest.CommonPrefix + suffix,
		}},
		MaxTokens: groupRequest.MaxTokens,
		Stream:    true,
		StreamOptions: streamOptions{
			IncludeUsage: true,
		},
	})
	if err != nil {
		return failureWithoutDispatch(requestID, fmt.Errorf("encode OpenAI request: %w", err), client.now())
	}

	childContext, cancel := context.WithTimeout(parent, groupRequest.Timeout)
	defer cancel()
	dispatchedAt := client.now()
	httpRequest, err := http.NewRequestWithContext(childContext, http.MethodPost, client.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return failureAt(requestID, fmt.Errorf("build OpenAI request: %w", err), dispatchedAt, client.now(), false)
	}
	for name, values := range headers {
		for _, value := range values {
			httpRequest.Header.Add(name, value)
		}
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "text/event-stream")
	if client.SimulatorMode {
		httpRequest.Header.Set(HeaderSimulatorArm, string(groupRequest.Arm))
		httpRequest.Header.Set(HeaderSimulatorSlot, fmt.Sprintf("%d", slot))
		httpRequest.Header.Set(HeaderSimulatorArrivalSkewMS, fmt.Sprintf("%d", groupRequest.Cell.ArrivalSkewMS))
	}

	response, err := client.httpClient().Do(httpRequest)
	if err != nil {
		completedAt := client.now()
		cancelled := errors.Is(err, context.Canceled) && errors.Is(parent.Err(), context.Canceled)
		return failureAt(requestID, fmt.Errorf("send OpenAI request: %w", err), dispatchedAt, completedAt, cancelled)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		completedAt := client.now()
		body, readErr := io.ReadAll(io.LimitReader(response.Body, maxErrorBodyBytes+1))
		if readErr != nil {
			return failureAt(requestID, fmt.Errorf("OpenAI status %s; read error body: %w", response.Status, readErr), dispatchedAt, completedAt, false)
		}
		if len(body) > maxErrorBodyBytes {
			body = body[:maxErrorBodyBytes]
		}
		detail := strings.TrimSpace(string(body))
		if detail == "" {
			detail = "empty response body"
		}
		return failureAt(requestID, fmt.Errorf("OpenAI status %s: %s", response.Status, detail), dispatchedAt, completedAt, false)
	}
	if mediaType := response.Header.Get("Content-Type"); !strings.Contains(strings.ToLower(mediaType), "text/event-stream") {
		return failureAt(requestID, fmt.Errorf("OpenAI response content type %q is not text/event-stream", mediaType), dispatchedAt, client.now(), false)
	}
	var target *evidence.EndpointRef
	if client.SimulatorMode {
		targetID := strings.TrimSpace(response.Header.Get(HeaderSimulatorTarget))
		if targetID == "" {
			return failureAt(requestID, fmt.Errorf("simulator response omitted %s", HeaderSimulatorTarget), dispatchedAt, client.now(), false)
		}
		target = &evidence.EndpointRef{ID: targetID, Model: groupRequest.Model}
	}

	stream, err := openaiwire.ReadStream(response.Body, openaiwire.StreamOptions{
		StartedAt:     dispatchedAt,
		MaxEventBytes: client.MaxEventBytes,
	})
	if err != nil {
		completedAt := client.now()
		cancelled := errors.Is(childContext.Err(), context.Canceled) && errors.Is(parent.Err(), context.Canceled)
		return failureAt(requestID, fmt.Errorf("consume OpenAI stream: %w", err), dispatchedAt, completedAt, cancelled)
	}
	completedAt := stream.CompletedAt
	if completedAt.IsZero() {
		completedAt = client.now()
	}
	return evidence.ChildResult{
		RequestID:      requestID,
		Target:         target,
		TTFTSeconds:    stream.TTFT.Seconds(),
		LatencySeconds: stream.Latency.Seconds(),
		OutputTokens:   stream.OutputTokens,
		Outcome:        evidence.OutcomeSuccess,
		DispatchedAt:   timePointer(dispatchedAt),
		FirstTokenAt:   stream.FirstTokenAt,
		CompletedAt:    timePointer(completedAt),
	}
}

func (client Client) cancelledBeforeDispatch(group protocol.Group, slot uint32, cause error) evidence.ChildResult {
	headers, err := group.Headers(slot)
	requestID := "unknown"
	if err == nil {
		requestID = headers.Get(protocol.HeaderRequestID)
	}
	if cause == nil {
		cause = context.Canceled
	}
	return evidence.ChildResult{
		RequestID:   requestID,
		Outcome:     evidence.OutcomeCancelled,
		Failure:     cause.Error(),
		CompletedAt: timePointer(client.now()),
	}
}

func failureWithoutDispatch(requestID string, err error, completedAt time.Time) evidence.ChildResult {
	return evidence.ChildResult{
		RequestID:   requestID,
		Outcome:     evidence.OutcomeFailure,
		Failure:     err.Error(),
		CompletedAt: timePointer(completedAt),
	}
}

func failureAt(requestID string, err error, dispatchedAt, completedAt time.Time, cancelled bool) evidence.ChildResult {
	outcome := evidence.OutcomeFailure
	if cancelled {
		outcome = evidence.OutcomeCancelled
	}
	latency := completedAt.Sub(dispatchedAt)
	if latency < 0 {
		latency = 0
	}
	return evidence.ChildResult{
		RequestID:      requestID,
		LatencySeconds: latency.Seconds(),
		Outcome:        outcome,
		Failure:        err.Error(),
		DispatchedAt:   timePointer(dispatchedAt),
		CompletedAt:    timePointer(completedAt),
	}
}

func summarizeGroup(request GroupRequest, groupID string, children []evidence.ChildResult) evidence.GroupResult {
	result := evidence.GroupResult{
		SchemaVersion: evidence.SchemaVersion,
		RunID:         request.RunID,
		Arm:           request.Arm,
		GroupID:       groupID,
		FanoutWidth:   uint32(len(children)),
		Cell:          request.Cell,
		Outcome:       evidence.OutcomeSuccess,
		Children:      children,
	}

	var firstDispatch, lastTerminal time.Time
	failures := make([]string, 0)
	cancelled := 0
	for slot, child := range children {
		if child.DispatchedAt != nil && (firstDispatch.IsZero() || child.DispatchedAt.Before(firstDispatch)) {
			firstDispatch = *child.DispatchedAt
		}
		if child.CompletedAt != nil && child.CompletedAt.After(lastTerminal) {
			lastTerminal = *child.CompletedAt
		}
		if child.TTFTSeconds > result.SlowestChildTTFTSeconds {
			result.SlowestChildTTFTSeconds = child.TTFTSeconds
		}
		if child.Outcome != evidence.OutcomeSuccess {
			failures = append(failures, fmt.Sprintf("slot %d: %s", slot, child.Failure))
			if child.Outcome == evidence.OutcomeCancelled {
				cancelled++
			}
		}
	}
	if !firstDispatch.IsZero() && !lastTerminal.IsZero() {
		makespan := lastTerminal.Sub(firstDispatch)
		if makespan > 0 {
			result.MakespanSeconds = makespan.Seconds()
		}
	}
	if len(failures) > 0 {
		result.Outcome = evidence.OutcomeFailure
		if cancelled == len(failures) {
			result.Outcome = evidence.OutcomeCancelled
		}
		sort.Strings(failures)
		result.Failure = strings.Join(failures, "; ")
	}
	return result
}

func (client Client) httpClient() *http.Client {
	if client.HTTPClient != nil {
		return client.HTTPClient
	}
	return http.DefaultClient
}

func (client Client) now() time.Time {
	if client.Now != nil {
		return client.Now().UTC()
	}
	return time.Now().UTC()
}

func timePointer(value time.Time) *time.Time {
	copy := value
	return &copy
}

type chatCompletionRequest struct {
	Model         string        `json:"model"`
	Messages      []chatMessage `json:"messages"`
	MaxTokens     uint32        `json:"max_tokens"`
	Stream        bool          `json:"stream"`
	StreamOptions streamOptions `json:"stream_options"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

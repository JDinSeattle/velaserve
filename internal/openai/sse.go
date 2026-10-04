// Package openai implements the narrow OpenAI-compatible streaming surface
// needed by the benchmark recorder. It intentionally does not model the full
// API.
package openai

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

const defaultMaxEventBytes = 8 << 20

type Clock interface {
	Now() time.Time
}

type StreamOptions struct {
	StartedAt     time.Time
	Clock         Clock
	MaxEventBytes int
	// DiscardText retains timing and usage while avoiding accumulated body text.
	// The default preserves the full-text API contract.
	DiscardText bool
}

type StreamResult struct {
	TTFT         time.Duration
	Latency      time.Duration
	Text         string
	OutputTokens uint64
	PromptTokens uint64
	CachedTokens *uint64
	FirstTokenAt *time.Time
	CompletedAt  time.Time
}

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now().UTC() }

// ReadStream consumes a complete Server-Sent Events response. A role-only
// delta is deliberately not counted as a token; TTFT begins at the first
// content, legacy text, or tool-call delta.
func ReadStream(reader io.Reader, options StreamOptions) (result StreamResult, err error) {
	if reader == nil {
		return StreamResult{}, fmt.Errorf("stream reader is required")
	}
	if options.StartedAt.IsZero() {
		return StreamResult{}, fmt.Errorf("stream start time is required")
	}
	if options.Clock == nil {
		options.Clock = wallClock{}
	}
	if options.MaxEventBytes == 0 {
		options.MaxEventBytes = defaultMaxEventBytes
	}
	if options.MaxEventBytes < 0 || options.MaxEventBytes > int(^uint(0)>>1)-8 {
		return StreamResult{}, fmt.Errorf("maximum event size must be positive and leave room for SSE framing")
	}

	scanner := bufio.NewScanner(reader)
	// MaxEventBytes limits data payload, excluding "data: " and CRLF.
	maxLineBytes := options.MaxEventBytes + 8
	scanner.Buffer(make([]byte, min(64*1024, maxLineBytes)), maxLineBytes)
	payload := make([]byte, 0, 4096)
	dataLines := 0
	eventBytes := 0
	var text strings.Builder
	defer func() { result.Text = text.String() }()

	dispatch := func() (bool, error) {
		if dataLines == 0 {
			return false, nil
		}
		defer func() { payload = payload[:0]; dataLines = 0; eventBytes = 0 }()
		eventAt := options.Clock.Now().UTC()
		if !utf8.Valid(payload) {
			return false, fmt.Errorf("OpenAI stream event contains invalid UTF-8")
		}
		if bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]")) {
			if result.FirstTokenAt == nil {
				return false, fmt.Errorf("OpenAI stream reached [DONE] before semantic output")
			}
			result.Latency = nonNegativeDuration(eventAt.Sub(options.StartedAt))
			result.CompletedAt = eventAt
			result.Text = text.String()
			return true, nil
		}

		var frame streamFrame
		if err := json.Unmarshal(payload, &frame); err != nil {
			return false, fmt.Errorf("decode OpenAI stream event: %w", err)
		}
		if frame.Error != nil {
			message := strings.TrimSpace(frame.Error.Message)
			if message == "" {
				message = "unspecified streaming error"
			}
			return false, fmt.Errorf("OpenAI stream error: %s", message)
		}
		if frame.Usage != nil {
			// A usage frame is one snapshot. Validate all fields before replacing
			// prior observations so malformed tails cannot poison failure evidence.
			var cachedTokens *uint64
			if len(frame.Usage.PromptTokensDetails) > 0 &&
				!bytes.Equal(bytes.TrimSpace(frame.Usage.PromptTokensDetails), []byte("null")) {
				var details struct {
					CachedTokens *uint64 `json:"cached_tokens"`
				}
				if err := json.Unmarshal(frame.Usage.PromptTokensDetails, &details); err != nil {
					return false, fmt.Errorf("decode OpenAI prompt token details: %w", err)
				}
				cachedTokens = details.CachedTokens
			}
			if cachedTokens != nil && (frame.Usage.PromptTokens == 0 || *cachedTokens > frame.Usage.PromptTokens) {
				return false, fmt.Errorf("OpenAI cached tokens require a positive prompt count and cannot exceed it")
			}
			result.OutputTokens = frame.Usage.CompletionTokens
			result.PromptTokens = frame.Usage.PromptTokens
			result.CachedTokens = cachedTokens
		}

		semanticDelta := false
		for _, choice := range frame.Choices {
			if choice.Delta.Content != "" {
				if !options.DiscardText {
					text.WriteString(choice.Delta.Content)
				}
				semanticDelta = true
			}
			if choice.Text != "" {
				if !options.DiscardText {
					text.WriteString(choice.Text)
				}
				semanticDelta = true
			}
			if len(choice.Delta.ToolCalls) > 0 {
				semanticDelta = true
			}
		}
		if semanticDelta && result.FirstTokenAt == nil {
			firstTokenAt := eventAt
			result.FirstTokenAt = &firstTokenAt
			result.TTFT = nonNegativeDuration(eventAt.Sub(options.StartedAt))
		}
		return false, nil
	}

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			done, err := dispatch()
			if err != nil {
				return result, err
			}
			if done {
				return result, nil
			}
			continue
		}
		if line[0] == ':' {
			continue
		}
		field, value, found := bytes.Cut(line, []byte(":"))
		if !found || !bytes.Equal(field, []byte("data")) {
			continue
		}
		if len(value) > 0 && value[0] == ' ' {
			value = value[1:]
		}
		eventBytes += len(value)
		if dataLines > 0 {
			eventBytes++
		}
		if eventBytes > options.MaxEventBytes {
			return result, fmt.Errorf("OpenAI stream event exceeds %d bytes", options.MaxEventBytes)
		}
		if dataLines > 0 {
			payload = append(payload, '\n')
		}
		payload = append(payload, value...)
		dataLines++
	}
	if err := scanner.Err(); err != nil {
		return result, fmt.Errorf("read OpenAI stream: %w", err)
	}
	if dataLines > 0 {
		if done, err := dispatch(); err != nil {
			return result, err
		} else if done {
			return result, nil
		}
	}
	return result, fmt.Errorf("OpenAI stream ended before [DONE]")
}

type streamFrame struct {
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
	Choices []struct {
		Delta struct {
			Role      string            `json:"role"`
			Content   string            `json:"content"`
			ToolCalls []json.RawMessage `json:"tool_calls"`
		} `json:"delta"`
		Text string `json:"text"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens        uint64          `json:"prompt_tokens"`
		CompletionTokens    uint64          `json:"completion_tokens"`
		PromptTokensDetails json.RawMessage `json:"prompt_tokens_details"`
	} `json:"usage"`
}

func nonNegativeDuration(value time.Duration) time.Duration {
	if value < 0 {
		return 0
	}
	return value
}

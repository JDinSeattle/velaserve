package openai

import (
	"strings"
	"testing"
	"time"
)

func TestReadStreamCountsTTFTAtFirstContentDelta(t *testing.T) {
	start := time.Date(2026, 8, 25, 20, 0, 0, 0, time.UTC)
	clock := &sequenceClock{times: []time.Time{
		start.Add(100 * time.Millisecond),
		start.Add(250 * time.Millisecond),
		start.Add(500 * time.Millisecond),
	}}
	stream := strings.NewReader("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n" +
		"data: [DONE]\n\n")

	got, err := ReadStream(stream, StreamOptions{StartedAt: start, Clock: clock, MaxEventBytes: 1024})
	if err != nil {
		t.Fatalf("ReadStream() error = %v", err)
	}
	if got.TTFT != 250*time.Millisecond {
		t.Fatalf("TTFT = %v, want 250ms", got.TTFT)
	}
	if got.Latency != 500*time.Millisecond || got.Text != "hello" {
		t.Fatalf("result = %#v", got)
	}
}

func TestReadStreamSupportsCRLFCommentsAndMultilineData(t *testing.T) {
	start := time.Date(2026, 8, 25, 20, 0, 0, 0, time.UTC)
	stream := strings.NewReader(": keepalive\r\n" +
		"data: {\"choices\":[{\"delta\":\r\n" +
		"data: {\"content\":\"ok\"}}]}\r\n\r\n" +
		"data: [DONE]\r\n\r\n")
	clock := &sequenceClock{times: []time.Time{start.Add(time.Millisecond), start.Add(2 * time.Millisecond)}}
	got, err := ReadStream(stream, StreamOptions{StartedAt: start, Clock: clock, MaxEventBytes: 1024})
	if err != nil {
		t.Fatalf("ReadStream() error = %v", err)
	}
	if got.Text != "ok" {
		t.Fatalf("Text = %q, want ok", got.Text)
	}
}

func TestReadStreamReturnsOpenAIErrorFrame(t *testing.T) {
	start := time.Now()
	stream := strings.NewReader("data: {\"error\":{\"message\":\"engine overloaded\"}}\n\n")
	_, err := ReadStream(stream, StreamOptions{StartedAt: start, Clock: &sequenceClock{times: []time.Time{start}}, MaxEventBytes: 1024})
	if err == nil || !strings.Contains(err.Error(), "engine overloaded") {
		t.Fatalf("ReadStream() error = %v", err)
	}
}

func TestReadStreamRejectsEOFBeforeDone(t *testing.T) {
	start := time.Now()
	stream := strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
	_, err := ReadStream(stream, StreamOptions{StartedAt: start, Clock: &sequenceClock{times: []time.Time{start}}, MaxEventBytes: 1024})
	if err == nil || !strings.Contains(err.Error(), "before [DONE]") {
		t.Fatalf("ReadStream() error = %v", err)
	}
}

func TestReadStreamRejectsDoneWithoutSemanticOutput(t *testing.T) {
	start := time.Now()
	stream := strings.NewReader("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"completion_tokens\":0}}\n\n" +
		"data: [DONE]\n\n")
	clock := &sequenceClock{times: []time.Time{start, start.Add(time.Millisecond), start.Add(2 * time.Millisecond)}}
	_, err := ReadStream(stream, StreamOptions{StartedAt: start, Clock: clock, MaxEventBytes: 1024})
	if err == nil || !strings.Contains(err.Error(), "before semantic output") {
		t.Fatalf("ReadStream() error = %v", err)
	}
}

func TestReadStreamRejectsOversizedEvent(t *testing.T) {
	start := time.Now()
	stream := strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"too large\"}}]}\n\n")
	_, err := ReadStream(stream, StreamOptions{StartedAt: start, Clock: &sequenceClock{times: []time.Time{start}}, MaxEventBytes: 16})
	if err == nil {
		t.Fatal("ReadStream() accepted an event above MaxEventBytes")
	}
}

type sequenceClock struct {
	times []time.Time
	index int
}

func (clock *sequenceClock) Now() time.Time {
	if clock.index >= len(clock.times) {
		return clock.times[len(clock.times)-1]
	}
	value := clock.times[clock.index]
	clock.index++
	return value
}

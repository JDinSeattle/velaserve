package openai

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

const measuredPrefix = "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n" +
	"data: {\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":2}}\n\n"

func TestMeasurementRetainsObservedFactsOnFailure(t *testing.T) {
	for _, discard := range []bool{false, true} {
		for name, suffix := range map[string]string{
			"missing-done": "", "invalid-utf8": "data: {\"x\":\"\xff\"}\n\n",
			"invalid-json": "data: {broken}\n\n", "oversized": "data: " + strings.Repeat("x", 2048) + "\n\n",
		} {
			t.Run(name, func(t *testing.T) {
				start := time.Now()
				got, err := ReadStream(strings.NewReader(measuredPrefix+suffix), StreamOptions{
					StartedAt: start, Clock: &sequenceClock{times: []time.Time{start.Add(time.Millisecond)}},
					MaxEventBytes: 1024, DiscardText: discard,
				})
				if err == nil || got.FirstTokenAt == nil || got.TTFT != time.Millisecond || got.OutputTokens != 2 || got.PromptTokens != 7 {
					t.Fatalf("lost partial observations: %+v, %v", got, err)
				}
				wantText := "hello"
				if discard {
					wantText = ""
				}
				if got.Text != wantText || !got.CompletedAt.IsZero() {
					t.Fatalf("incorrect partial result: %+v", got)
				}
			})
		}
	}
}

func TestMeasurementWithoutSemanticTokenDoesNotInventTTFT(t *testing.T) {
	got, err := ReadStream(strings.NewReader("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n"), StreamOptions{StartedAt: time.Now(), DiscardText: true})
	if err == nil || got.FirstTokenAt != nil || got.TTFT != 0 {
		t.Fatalf("%+v, %v", got, err)
	}
}

func TestMeasurementDoneAtEOFAndFullTextControl(t *testing.T) {
	for _, discard := range []bool{false, true} {
		got, err := ReadStream(strings.NewReader(measuredPrefix+"data: [DONE]"), StreamOptions{StartedAt: time.Now(), DiscardText: discard})
		if err != nil || got.FirstTokenAt == nil || got.CompletedAt.IsZero() || got.OutputTokens != 2 {
			t.Fatalf("%+v, %v", got, err)
		}
		want := "hello"
		if discard {
			want = ""
		}
		if got.Text != want {
			t.Fatalf("text %q", got.Text)
		}
	}
}

func TestMeasurementPropagatesCancelledReader(t *testing.T) {
	stopped := errors.New("cancelled reader")
	got, err := ReadStream(io.MultiReader(strings.NewReader(measuredPrefix), failingReader{stopped}), StreamOptions{StartedAt: time.Now(), DiscardText: true})
	if !errors.Is(err, stopped) || got.FirstTokenAt == nil || got.OutputTokens != 2 {
		t.Fatalf("%+v, %v", got, err)
	}
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

// Both modes run identical boundary, UTF-8, JSON and usage validation.
func BenchmarkStreamRetention(b *testing.B) {
	payload := strings.Repeat("data: {\"choices\":[{\"delta\":{\"content\":\""+strings.Repeat("x", 4096)+"\"}}]}\n\n", 256) +
		"data: {\"usage\":{\"completion_tokens\":256}}\n\ndata: [DONE]\n\n"
	for _, discard := range []bool{false, true} {
		name := "full-text"
		if discard {
			name = "measurement"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				result, err := ReadStream(strings.NewReader(payload), StreamOptions{StartedAt: time.Now(), DiscardText: discard})
				if err != nil || result.OutputTokens != 256 {
					b.Fatal(err)
				}
			}
		})
	}
}

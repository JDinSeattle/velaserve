package openai

import (
	"strings"
	"testing"
	"time"
)

func TestEventLimitCountsPayloadWithoutSSEFraming(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n"} {
		for _, size := range []int{65536, 100000} {
			prefix := `{"choices":[{"delta":{"content":"`
			suffix := `"}}]}`
			payload := prefix + strings.Repeat("x", size-len(prefix)-len(suffix)) + suffix
			stream := "data: " + payload + newline + newline + "data: [DONE]" + newline + newline
			got, err := ReadStream(strings.NewReader(stream), StreamOptions{StartedAt: time.Now(), MaxEventBytes: size, DiscardText: true})
			if err != nil || got.FirstTokenAt == nil {
				t.Fatalf("size=%d newline=%q: %+v %v", size, newline, got, err)
			}
			_, err = ReadStream(strings.NewReader(stream), StreamOptions{StartedAt: time.Now(), MaxEventBytes: size - 1})
			if err == nil {
				t.Fatalf("accepted payload exceeding %d bytes", size-1)
			}
		}
	}
}

func TestMalformedUsageDoesNotReplacePreviousObservation(t *testing.T) {
	prefix := "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n" +
		"data: {\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"prompt_tokens_details\":{\"cached_tokens\":8}}}\n\n"
	for _, bad := range []string{
		`{"usage":{"prompt_tokens":1,"completion_tokens":99,"prompt_tokens_details":{"cached_tokens":"bad"}}}`,
		`{"usage":{"prompt_tokens":1,"completion_tokens":99,"prompt_tokens_details":{"cached_tokens":2}}}`,
	} {
		got, err := ReadStream(strings.NewReader(prefix+"data: "+bad+"\n\n"), StreamOptions{StartedAt: time.Now()})
		if err == nil || got.PromptTokens != 10 || got.OutputTokens != 2 || got.CachedTokens == nil || *got.CachedTokens != 8 {
			t.Fatalf("invalid usage replaced accepted observation: %+v %v", got, err)
		}
	}
}

func TestUsageSnapshotWithoutCacheDetailsClearsPreviousCache(t *testing.T) {
	stream := "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n" +
		"data: {\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":1,\"prompt_tokens_details\":{\"cached_tokens\":8}}}\n\n" +
		"data: {\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n"
	got, err := ReadStream(strings.NewReader(stream), StreamOptions{StartedAt: time.Now()})
	if err != nil || got.PromptTokens != 1 || got.OutputTokens != 2 || got.CachedTokens != nil {
		t.Fatalf("stale cache: %+v %v", got, err)
	}
}

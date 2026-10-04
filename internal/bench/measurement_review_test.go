package bench

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/JDinSeattle/velaserve/internal/evidence"
)

type reviewTransport func(*http.Request) (*http.Response, error)

func (f reviewTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type cancelAfterPrefix struct {
	prefix *strings.Reader
	cancel context.CancelFunc
}

func (r *cancelAfterPrefix) Read(p []byte) (int, error) {
	n, err := r.prefix.Read(p)
	if err == io.EOF {
		r.cancel()
		return 0, context.Canceled
	}
	return n, err
}

func TestRunGroupRetainsValidFactsAcrossMalformedUsageAndCancellation(t *testing.T) {
	const prefix = "data: {\"choices\":[{\"delta\":{\"content\":\"observed\"}}]}\n\n" +
		"data: {\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"prompt_tokens_details\":{\"cached_tokens\":8}}}\n\n"
	for _, name := range []string{"invalid-cache", "invalid-details", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var body io.Reader
			switch name {
			case "invalid-cache":
				body = strings.NewReader(prefix + "data: {\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":99,\"prompt_tokens_details\":{\"cached_tokens\":2}}}\n\n")
			case "invalid-details":
				body = strings.NewReader(prefix + "data: {\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":99,\"prompt_tokens_details\":{\"cached_tokens\":\"bad\"}}}\n\n")
			case "cancelled":
				body = &cancelAfterPrefix{strings.NewReader(prefix), cancel}
			}
			client := Client{Endpoint: "https://example.invalid/v1/chat/completions", HTTPClient: &http.Client{Transport: reviewTransport(func(request *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(body), Request: request}, nil
			})}}
			result, err := client.RunGroup(ctx, GroupRequest{RunID: "review-partial", Arm: evidence.ArmLoadAwareP2P, Model: "test-model", CommonPrefix: "prefix", Suffixes: []string{"suffix"}, MaxTokens: 8, MaxWidth: 16, Timeout: time.Second, Cell: benchmarkCell(8, 0)})
			if err != nil {
				t.Fatalf("discarded failed-group evidence: %v", err)
			}
			expected := evidence.OutcomeFailure
			if name == "cancelled" {
				expected = evidence.OutcomeCancelled
			}
			child := result.Children[0]
			if result.Outcome != expected || child.Outcome != expected || child.FirstTokenAt == nil || child.PromptTokens != 10 || child.OutputTokens != 2 || child.CachedTokens == nil || *child.CachedTokens != 8 {
				t.Fatalf("partial observations: %+v", result)
			}
			if err := evidence.ValidateGroupResult(result); err != nil {
				t.Fatal(err)
			}
		})
	}
}

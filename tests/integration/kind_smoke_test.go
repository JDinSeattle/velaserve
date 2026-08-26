package integration

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JDinSeattle/velaserve/internal/bench"
	"github.com/JDinSeattle/velaserve/internal/fanout/protocol"
)

func TestKindUpstreamSSE(t *testing.T) {
	endpointPath := filepath.Join(repositoryRoot(t), ".tools", "kind-endpoint")
	contents, err := os.ReadFile(endpointPath)
	if os.IsNotExist(err) {
		t.Skip("Kind endpoint is absent; run hack/kind-up.sh first")
	}
	if err != nil {
		t.Fatal(err)
	}
	endpoint := strings.TrimSpace(string(contents))
	if endpoint == "" {
		t.Fatal("Kind endpoint file is empty")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	body := bytes.NewBufferString(`{"model":"velaserve-simulator","messages":[{"role":"user","content":"integration smoke"}],"max_tokens":8,"stream":true,"stream_options":{"include_usage":true}}`)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	group, err := protocol.NewGroup(2, 16)
	if err != nil {
		t.Fatal(err)
	}
	headers, err := group.Headers(0)
	if err != nil {
		t.Fatal(err)
	}
	for name, values := range headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	stream, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.StatusCode, stream)
	}
	if response.Header.Get(bench.HeaderSimulatorTarget) != "" {
		t.Fatal("upstream route leaked a simulator-only target header")
	}
	if !strings.Contains(string(stream), "data: [DONE]") {
		t.Fatalf("upstream route returned an incomplete SSE stream: %s", stream)
	}
}

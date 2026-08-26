package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/JDinSeattle/velaserve/internal/bench"
	openaiwire "github.com/JDinSeattle/velaserve/internal/openai"
	"github.com/JDinSeattle/velaserve/research/crossover"
)

const maxResponseBytes = 32 << 20

type tokenizeResponse struct {
	Count       uint64   `json:"count"`
	MaxModelLen uint64   `json:"max_model_len"`
	Tokens      []uint32 `json:"tokens"`
}

func main() {
	bindingPath := flag.String("binding", "", "crossover calibration binding JSON")
	templatePath := flag.String("template", "benchmarks/profiles/aws-z0-template.yaml", "profile template supplying the frozen repeat text and suffixes")
	outputPath := flag.String("output", "", "new raw crossover observations JSONL")
	authorizationPath := flag.String("authorization-file", "", "optional file containing an Authorization header value")
	trials := flag.Uint("trials", 3, "trials per acquisition and prefix")
	timeout := flag.Duration("timeout", 5*time.Minute, "timeout per request")
	flag.Parse()
	if err := run(*bindingPath, *templatePath, *outputPath, *authorizationPath, *trials, *timeout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

func run(bindingPath, templatePath, outputPath, authorizationPath string, trials uint, timeout time.Duration) error {
	if strings.TrimSpace(bindingPath) == "" || strings.TrimSpace(outputPath) == "" || trials < 3 || timeout <= 0 {
		return fmt.Errorf("binding, output, at least three trials, and a positive timeout are required")
	}
	binding, _, err := crossover.LoadBinding(bindingPath)
	if err != nil {
		return err
	}
	profile, err := bench.LoadProfile(templatePath)
	if err != nil {
		return err
	}
	if len(profile.Prefixes) == 0 || len(profile.Suffixes) < 2 || strings.TrimSpace(profile.Prefixes[0].RepeatText) == "" {
		return fmt.Errorf("profile template lacks repeat text and two calibration suffixes")
	}
	authorization := ""
	if authorizationPath != "" {
		contents, err := os.ReadFile(authorizationPath)
		if err != nil {
			return err
		}
		authorization = strings.TrimSpace(string(contents))
	}
	output, err := os.OpenFile(outputPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	success := false
	defer func() {
		_ = output.Close()
		if !success {
			_ = os.Remove(outputPath)
		}
	}()
	encoder := json.NewEncoder(output)
	client := &http.Client{}
	runID, err := randomID("crossover")
	if err != nil {
		return err
	}
	ctx := context.Background()
	targets := []uint64{64, 128, 256, 512, 1024, 2048, 4096, 8192}
	for prefixIndex, targetTokens := range targets {
		prefix, exactTokens, err := calibratePrefix(ctx, client, binding.Endpoints[0].TokenizeURL, authorization, binding.ModelID, profile.Prefixes[0].RepeatText, []string{profile.Suffixes[0], profile.Suffixes[1]}, targetTokens, timeout)
		if err != nil {
			return fmt.Errorf("calibrate %d-token prefix: %w", targetTokens, err)
		}
		cacheableTokens := exactTokens - exactTokens%bench.CacheBlockTokens
		if cacheableTokens == 0 {
			return fmt.Errorf("calibrated prefix for target %d covers no cache block", targetTokens)
		}
		prompt := prefix + profile.Suffixes[0]
		for acquisitionIndex, acquisition := range []string{"recompute", "p2p"} {
			for trialIndex := uint(0); trialIndex < trials; trialIndex++ {
				targetIndex := (prefixIndex + acquisitionIndex + int(trialIndex)) % len(binding.Endpoints)
				sourceIndex := (targetIndex + 1) % len(binding.Endpoints)
				target := binding.Endpoints[targetIndex]
				source := binding.Endpoints[sourceIndex]
				if err := resetAll(ctx, client, binding.Endpoints, authorization, timeout); err != nil {
					return err
				}
				if acquisition == "p2p" {
					for warm := 0; warm < 2; warm++ {
						warmID, err := randomID("warm")
						if err != nil {
							return err
						}
						result, err := chat(ctx, client, source.EngineChatURL, authorization, binding.ModelID, prompt, warmID, nil, timeout)
						if err != nil {
							return fmt.Errorf("warm source %q: %w", source.ID, err)
						}
						if warm == 1 && result.CachedTokens < cacheableTokens {
							return fmt.Errorf("source %q did not retain the %d-token prefix", source.ID, cacheableTokens)
						}
					}
				}
				requestID, err := randomID("probe")
				if err != nil {
					return err
				}
				headers := map[string]string{}
				if acquisition == "p2p" {
					headers["x-kv-cache-source-host-port"] = source.P2PSourceHostPort
					headers["X-VelaServe-P2P-Source-ID"] = source.ID
					headers["X-VelaServe-P2P-Source-Model"] = binding.ModelID
				}
				result, err := chat(ctx, client, target.ProxyChatURL, authorization, binding.ModelID, prompt, requestID, headers, timeout)
				if err != nil {
					return fmt.Errorf("%s target %q: %w", acquisition, target.ID, err)
				}
				observation := crossover.Observation{
					SchemaVersion: crossover.ObservationSchemaVersion, RunID: runID, RequestID: requestID,
					PrefixTokens: cacheableTokens, Prompt: prompt, Acquisition: acquisition, TargetID: target.ID, TargetPodUID: target.PodUID,
					PromptTokens: result.PromptTokens, CachedTokens: result.CachedTokens, DispatchedAt: result.DispatchedAt,
					FirstTokenAt: result.FirstTokenAt, CompletedAt: result.CompletedAt, TTFTSeconds: result.FirstTokenAt.Sub(result.DispatchedAt).Seconds(),
				}
				if acquisition == "p2p" {
					observation.SourceID = source.ID
					observation.SourcePodUID = source.PodUID
				}
				if err := crossover.ValidateObservation(observation, binding); err != nil {
					return err
				}
				if err := encoder.Encode(observation); err != nil {
					return err
				}
			}
		}
	}
	if err := output.Sync(); err != nil {
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	success = true
	fmt.Printf("crossover-calibrate: retained %d forced raw observations in %s\n", len(targets)*2*int(trials), outputPath)
	return nil
}

type chatResult struct {
	PromptTokens uint64
	CachedTokens uint64
	DispatchedAt time.Time
	FirstTokenAt time.Time
	CompletedAt  time.Time
}

func chat(parent context.Context, client *http.Client, endpoint, authorization, model, prompt, requestID string, headers map[string]string, timeout time.Duration) (chatResult, error) {
	payload, err := json.Marshal(map[string]any{"model": model, "messages": []map[string]string{{"role": "user", "content": prompt}}, "max_tokens": 1, "stream": true, "stream_options": map[string]bool{"include_usage": true}})
	if err != nil {
		return chatResult{}, err
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	dispatchedAt := time.Now().UTC()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return chatResult{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("X-Request-ID", requestID)
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := client.Do(request)
	if err != nil {
		return chatResult{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		return chatResult{}, fmt.Errorf("HTTP %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	stream, err := openaiwire.ReadStream(response.Body, openaiwire.StreamOptions{StartedAt: dispatchedAt, MaxEventBytes: maxResponseBytes})
	if err != nil {
		return chatResult{}, err
	}
	if stream.FirstTokenAt == nil || stream.CachedTokens == nil || stream.PromptTokens == 0 {
		return chatResult{}, fmt.Errorf("response lacks first-token or prompt-cache usage evidence")
	}
	return chatResult{PromptTokens: stream.PromptTokens, CachedTokens: *stream.CachedTokens, DispatchedAt: dispatchedAt, FirstTokenAt: *stream.FirstTokenAt, CompletedAt: stream.CompletedAt}, nil
}

func resetAll(parent context.Context, client *http.Client, endpoints []crossover.Endpoint, authorization string, timeout time.Duration) error {
	for _, endpoint := range endpoints {
		parsed, err := url.ParseRequestURI(endpoint.ResetURL)
		if err != nil {
			return err
		}
		query := parsed.Query()
		query.Set("reset_external", "true")
		parsed.RawQuery = query.Encode()
		ctx, cancel := context.WithTimeout(parent, timeout)
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, parsed.String(), nil)
		if err != nil {
			cancel()
			return err
		}
		if authorization != "" {
			request.Header.Set("Authorization", authorization)
		}
		response, err := client.Do(request)
		if err != nil {
			cancel()
			return err
		}
		contents, readErr := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		_ = response.Body.Close()
		cancel()
		if readErr != nil || response.StatusCode < 200 || response.StatusCode >= 300 {
			return fmt.Errorf("reset endpoint %q failed", endpoint.ID)
		}
		var result struct {
			Success bool `json:"success"`
		}
		if json.Unmarshal(contents, &result) != nil || !result.Success {
			return fmt.Errorf("reset endpoint %q did not confirm success", endpoint.ID)
		}
	}
	return nil
}

func calibratePrefix(parent context.Context, client *http.Client, endpoint, authorization, model, repeatText string, suffixes []string, target uint64, timeout time.Duration) (string, uint64, error) {
	measure := func(repeats uint32) (uint64, error) {
		tokenizations := make([][]uint32, 0, len(suffixes))
		for _, suffix := range suffixes {
			payload, _ := json.Marshal(map[string]any{"model": model, "messages": []map[string]string{{"role": "user", "content": strings.Repeat(repeatText, int(repeats)) + suffix}}, "add_generation_prompt": true})
			ctx, cancel := context.WithTimeout(parent, timeout)
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
			if err != nil {
				cancel()
				return 0, err
			}
			request.Header.Set("Content-Type", "application/json")
			if authorization != "" {
				request.Header.Set("Authorization", authorization)
			}
			response, err := client.Do(request)
			if err != nil {
				cancel()
				return 0, err
			}
			contents, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
			_ = response.Body.Close()
			cancel()
			if readErr != nil || response.StatusCode < 200 || response.StatusCode >= 300 || len(contents) > maxResponseBytes {
				return 0, fmt.Errorf("tokenize request failed")
			}
			var result tokenizeResponse
			if json.Unmarshal(contents, &result) != nil || result.Count != uint64(len(result.Tokens)) || result.MaxModelLen == 0 {
				return 0, fmt.Errorf("tokenize response is invalid")
			}
			tokenizations = append(tokenizations, result.Tokens)
		}
		return bench.LongestCommonTokenPrefix(tokenizations), nil
	}
	high := uint32(1)
	for {
		shared, err := measure(high)
		if err != nil {
			return "", 0, err
		}
		if shared >= target {
			break
		}
		if high > 1<<27 {
			return "", 0, fmt.Errorf("cannot bracket token target")
		}
		high *= 2
	}
	low := high / 2
	if low == 0 {
		low = 1
	}
	bestRepeats, bestShared := high, uint64(^uint64(0))
	for low <= high {
		mid := low + (high-low)/2
		shared, err := measure(mid)
		if err != nil {
			return "", 0, err
		}
		if delta(shared, target) < delta(bestShared, target) {
			bestRepeats, bestShared = mid, shared
		}
		if shared < target {
			low = mid + 1
		} else if mid == 0 {
			break
		} else {
			high = mid - 1
		}
	}
	return strings.Repeat(repeatText, int(bestRepeats)), bestShared, nil
}

func delta(left, right uint64) uint64 {
	if left > right {
		return left - right
	}
	return right - left
}

func randomID(prefix string) (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return prefix + "-" + hex.EncodeToString(bytes), nil
}

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/JDinSeattle/velaserve/internal/bench"
	"github.com/JDinSeattle/velaserve/internal/evidence"
	"github.com/JDinSeattle/velaserve/research/crossover"
)

const maxHTTPResponseBytes = 32 << 20

func main() {
	if len(os.Args) < 2 {
		fatal(fmt.Errorf("usage: profile-freeze generate|verify-live [flags]"))
	}
	var err error
	switch os.Args[1] {
	case "generate":
		err = generate(os.Args[2:])
	case "verify-live":
		err = verifyLive(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fatal(err)
	}
}

func generate(args []string) error {
	flags := flag.NewFlagSet("generate", flag.ContinueOnError)
	templatePath := flags.String("template", "benchmarks/profiles/aws-z0-template.yaml", "AWS Z0 profile template")
	crossoverPath := flags.String("crossover-evidence", "", "measured crossover evidence JSON")
	crossoverBundle := flags.String("crossover-bundle", "", "sealed raw crossover calibration bundle (preferred)")
	tokenizeURL := flags.String("tokenize-url", "", "pinned model /tokenize endpoint")
	tokenizerInfoURL := flags.String("tokenizer-info-url", "", "pinned model /tokenizer_info endpoint")
	model := flags.String("model", "", "exact model ID")
	revision := flags.String("revision", "", "exact model/tokenizer revision")
	phase := flags.String("phase", "", "z0-a, z0-b, or z0-c")
	arm := flags.String("arm", "", "frozen active arm")
	eppReplicas := flags.Uint("epp-replicas", 0, "bound EPP replica count")
	transport := flags.String("transport", "tcp", "bound transport")
	profileOutput := flags.String("output-profile", "", "new generated profile YAML")
	calibrationOutput := flags.String("output-calibration", "", "new raw tokenizer/crossover calibration JSON")
	routerValuesOutput := flags.String("output-router-values", "", "new Helm values YAML carrying the measured P2P threshold")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if anyBlank(*tokenizeURL, *tokenizerInfoURL, *model, *revision, *phase, *arm, *transport, *profileOutput, *calibrationOutput, *routerValuesOutput) || (*crossoverPath == "" && *crossoverBundle == "") || (*crossoverPath != "" && *crossoverBundle != "") || (*eppReplicas != 1 && *eppReplicas != 2) {
		return fmt.Errorf("all generation flags and EPP replicas 1 or 2 are required")
	}
	profile, err := bench.LoadProfile(*templatePath)
	if err != nil {
		return err
	}
	var crossoverEvidence bench.CrossoverEvidence
	if *crossoverBundle != "" {
		crossoverEvidence, err = crossover.VerifyBundle(*crossoverBundle)
	} else {
		crossoverEvidence, _, err = bench.LoadCrossoverEvidenceSource(*crossoverPath)
	}
	if err != nil {
		return fmt.Errorf("load measured crossover: %w", err)
	}
	if crossoverEvidence.ModelID != *model || crossoverEvidence.ModelRevision != *revision || crossoverEvidence.Transport != *transport {
		return fmt.Errorf("crossover evidence does not match the requested model revision and transport")
	}
	activeArm := evidence.Arm(*arm)
	if activeArm != evidence.ArmAffinityP2P && activeArm != evidence.ArmLoadAwareP2P {
		return fmt.Errorf("active arm is not a frozen upstream baseline")
	}
	profile.Model = *model
	profile.Arms = []evidence.Arm{activeArm}
	profile.EPPReplicas = []uint32{uint32(*eppReplicas)}
	profile.Transports = []string{*transport}
	switch *phase {
	case "z0-a", "z0-b":
		profile.CacheStates = []string{"warm-owner", "distributed-warm", "cold"}
		profile.PrefixSourceCounts = nil
	case "z0-c":
		profile.CacheStates = []string{"distributed-warm"}
		profile.PrefixSourceCounts = []uint32{1, 2, 4}
	default:
		return fmt.Errorf("phase must be z0-a, z0-b, or z0-c")
	}
	crossover, err := bench.CrossoverTokens(crossoverEvidence.Samples)
	if err != nil {
		return err
	}
	if crossover > math.MaxUint64/2 {
		return fmt.Errorf("crossover token count overflows the above-crossover target")
	}
	canonicalCrossover, err := json.Marshal(crossoverEvidence)
	if err != nil {
		return err
	}
	crossoverDigest := sha256.Sum256(canonicalCrossover)
	targets := []uint64{max(1, crossover/2), crossover, crossover * 2}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	client := &http.Client{Timeout: 30 * time.Second}
	tokenizerInfo, err := fetchTokenizerInfo(ctx, client, *tokenizerInfoURL)
	if err != nil {
		return err
	}
	calibration := bench.ProfileCalibration{
		SchemaVersion: bench.ProfileCalibrationSchemaVersion, ModelID: *model, ModelRevision: *revision,
		Transport: *transport, TokenizerInfo: tokenizerInfo, CrossoverEvidenceSHA256: hex.EncodeToString(crossoverDigest[:]), CrossoverEvidence: crossoverEvidence,
		MinCachedTokenDelta: crossover, CrossoverSamples: append([]bench.CrossoverSample(nil), crossoverEvidence.Samples...),
	}
	for index := range profile.Prefixes {
		prefix := &profile.Prefixes[index]
		calibrationSuffixes := append([]string{bench.CalibrationWarmupSuffix}, profile.Suffixes[:profile.MaxWidth]...)
		repeatCount, exactTokens, tokenizations, err := calibratePrefix(ctx, client, *tokenizeURL, *model, *prefix, calibrationSuffixes, targets[index])
		if err != nil {
			return fmt.Errorf("calibrate prefix %q: %w", prefix.ID, err)
		}
		prefix.RepeatCount = repeatCount
		cacheableTokens := exactTokens - exactTokens%bench.CacheBlockTokens
		if cacheableTokens == 0 {
			return fmt.Errorf("calibrate prefix %q: exact common prefix does not cover one 64-token cache block", prefix.ID)
		}
		prefix.EstimatedTokens = cacheableTokens
		rendered := strings.Repeat(prefix.RepeatText, int(repeatCount))
		digest := sha256.Sum256([]byte(rendered))
		warmupDigest := sha256.Sum256([]byte(rendered + bench.CalibrationWarmupSuffix))
		calibration.Prefixes = append(calibration.Prefixes, bench.PrefixTokenCalibration{
			ID: prefix.ID, RenderedPrefixSHA256: hex.EncodeToString(digest[:]), WarmupContentSHA256: hex.EncodeToString(warmupDigest[:]),
			ExactSharedTokens: exactTokens, CacheableSharedTokens: cacheableTokens, Tokenizations: tokenizations,
		})
	}
	if err := bench.ValidateAWSZ0Profile(profile, *phase); err != nil {
		return err
	}
	if err := bench.ValidateProfileCalibration(profile, calibration); err != nil {
		return err
	}
	// JSON is a YAML 1.2 subset and preserves leading-newline suffix strings
	// without relying on emitter-specific block-scalar indentation behavior.
	profileBytes, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return err
	}
	profileBytes = append(profileBytes, '\n')
	calibrationBytes, err := json.MarshalIndent(calibration, "", "  ")
	if err != nil {
		return err
	}
	routerValuesBytes := []byte(fmt.Sprintf("model:\n  minCachedTokenDelta: %d\n", crossover))
	if err := writeExclusive(*profileOutput, profileBytes); err != nil {
		return err
	}
	if err := writeExclusive(*calibrationOutput, append(calibrationBytes, '\n')); err != nil {
		_ = os.Remove(*profileOutput)
		return err
	}
	if err := writeExclusive(*routerValuesOutput, routerValuesBytes); err != nil {
		_ = os.Remove(*profileOutput)
		_ = os.Remove(*calibrationOutput)
		return err
	}
	fmt.Printf("profile-freeze: generated %s, %s, and %s from live tokenizer and measured %d-token crossover\n", *profileOutput, *calibrationOutput, *routerValuesOutput, crossover)
	return nil
}

func verifyLive(args []string) error {
	flags := flag.NewFlagSet("verify-live", flag.ContinueOnError)
	profilePath := flags.String("profile", "", "generated profile YAML")
	calibrationPath := flags.String("calibration", "", "profile calibration JSON")
	tokenizeURL := flags.String("tokenize-url", "", "live /tokenize endpoint")
	tokenizerInfoURL := flags.String("tokenizer-info-url", "", "live /tokenizer_info endpoint")
	phase := flags.String("phase", "", "z0-a, z0-b, or z0-c")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if anyBlank(*profilePath, *calibrationPath, *tokenizeURL, *tokenizerInfoURL, *phase) {
		return fmt.Errorf("all verify-live flags are required")
	}
	profile, err := bench.LoadProfile(*profilePath)
	if err != nil {
		return err
	}
	calibration, err := bench.LoadProfileCalibration(*calibrationPath)
	if err != nil {
		return err
	}
	if err := bench.ValidateAWSZ0Profile(profile, *phase); err != nil {
		return err
	}
	if err := bench.ValidateProfileCalibration(profile, calibration); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	client := &http.Client{Timeout: 30 * time.Second}
	liveInfo, err := fetchTokenizerInfo(ctx, client, *tokenizerInfoURL)
	if err != nil {
		return err
	}
	liveCalibration := calibration
	liveCalibration.TokenizerInfo = liveInfo
	liveHash, err := bench.TokenizerInfoSHA256(liveCalibration)
	if err != nil {
		return err
	}
	calibratedHash, err := bench.TokenizerInfoSHA256(calibration)
	if err != nil {
		return err
	}
	if liveHash != calibratedHash {
		return fmt.Errorf("live tokenizer_info does not match the calibrated tokenizer/chat template")
	}
	for prefixIndex, prefix := range profile.Prefixes {
		rendered := strings.Repeat(prefix.RepeatText, int(prefix.RepeatCount))
		calibrationSuffixes := append([]string{bench.CalibrationWarmupSuffix}, profile.Suffixes[:profile.MaxWidth]...)
		for suffixIndex, suffix := range calibrationSuffixes {
			response, err := fetchTokens(ctx, client, *tokenizeURL, profile.Model, rendered+suffix)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(response.Tokens, calibration.Prefixes[prefixIndex].Tokenizations[suffixIndex]) {
				return fmt.Errorf("live tokenization drifted for prefix %q calibration prompt %d", prefix.ID, suffixIndex+1)
			}
		}
	}
	fmt.Println("profile-freeze: live tokenizer, chat template, exact shared-token counts, and crossover binding verified")
	return nil
}

type tokenizeResponse struct {
	Count       uint64   `json:"count"`
	MaxModelLen uint64   `json:"max_model_len"`
	Tokens      []uint32 `json:"tokens"`
}

func calibratePrefix(ctx context.Context, client *http.Client, endpoint, model string, prefix bench.PrefixProfile, suffixes []string, target uint64) (uint32, uint64, [][]uint32, error) {
	type candidate struct {
		repeats uint32
		shared  uint64
		tokens  [][]uint32
	}
	measure := func(repeats uint32) (candidate, error) {
		if repeats == 0 || uint64(len(prefix.RepeatText))*uint64(repeats) > 8<<20 {
			return candidate{}, fmt.Errorf("rendered prefix exceeds the 8 MiB safety bound")
		}
		rendered := strings.Repeat(prefix.RepeatText, int(repeats))
		result := candidate{repeats: repeats, tokens: make([][]uint32, 0, len(suffixes))}
		for _, suffix := range suffixes {
			response, err := fetchTokens(ctx, client, endpoint, model, rendered+suffix)
			if err != nil {
				return candidate{}, err
			}
			if response.MaxModelLen == 0 || response.Count != uint64(len(response.Tokens)) || response.Count+128 > response.MaxModelLen {
				return candidate{}, fmt.Errorf("tokenizer response is inconsistent or leaves no room for the frozen output")
			}
			result.tokens = append(result.tokens, response.Tokens)
		}
		result.shared = bench.LongestCommonTokenPrefix(result.tokens)
		return result, nil
	}
	best, err := measure(1)
	if err != nil {
		return 0, 0, nil, err
	}
	high := uint32(1)
	for best.shared < target {
		if high > math.MaxUint32/2 {
			return 0, 0, nil, fmt.Errorf("cannot bracket target %d tokens", target)
		}
		high *= 2
		measured, err := measure(high)
		if err != nil {
			return 0, 0, nil, err
		}
		if absDiff(measured.shared, target) < absDiff(best.shared, target) {
			best = measured
		}
		if measured.shared >= target {
			break
		}
	}
	low := high / 2
	if low == 0 {
		low = 1
	}
	for low <= high {
		mid := low + (high-low)/2
		measured, err := measure(mid)
		if err != nil {
			return 0, 0, nil, err
		}
		if absDiff(measured.shared, target) < absDiff(best.shared, target) {
			best = measured
		}
		if measured.shared < target {
			low = mid + 1
		} else {
			if mid == 0 {
				break
			}
			high = mid - 1
		}
	}
	return best.repeats, best.shared, best.tokens, nil
}

func fetchTokens(ctx context.Context, client *http.Client, endpoint, model, content string) (tokenizeResponse, error) {
	payload := map[string]any{"model": model, "messages": []map[string]string{{"role": "user", "content": content}}, "add_generation_prompt": true}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return tokenizeResponse{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return tokenizeResponse{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	var response tokenizeResponse
	if err := doJSON(client, request, &response); err != nil {
		return tokenizeResponse{}, err
	}
	return response, nil
}

func fetchTokenizerInfo(ctx context.Context, client *http.Client, endpoint string) (json.RawMessage, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	var value any
	if err := doJSON(client, request, &value); err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(value)
	return json.RawMessage(canonical), err
}

func doJSON(client *http.Client, request *http.Request, target any) error {
	parsed, err := url.ParseRequestURI(request.URL.String())
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return fmt.Errorf("endpoint must be an absolute HTTP(S) URL without user information")
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("tokenizer endpoint status %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxHTTPResponseBytes+1))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("tokenizer endpoint returned trailing JSON")
	}
	return nil
}

func writeExclusive(path string, contents []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	success := false
	defer func() {
		_ = file.Close()
		if !success {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(contents); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	success = true
	return nil
}

func absDiff(left, right uint64) uint64 {
	if left > right {
		return left - right
	}
	return right - left
}

func anyBlank(values ...string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return true
		}
	}
	return false
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(2)
}

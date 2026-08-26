// Package conditiondriver provides the concrete, fail-closed workload
// conditioner used by the real-GPU benchmark. It resets every configured
// model replica, warms the exact experiment prefix on the declared owners,
// and maintains measured background load through the gateway.
package conditiondriver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/JDinSeattle/velaserve/internal/conditioncontroller"
	"github.com/JDinSeattle/velaserve/internal/evidence"
)

const (
	appliedSchemaVersion = "velaserve.applied-condition/v1"
	loadProfileSchema    = "velaserve.background-load-profile/v1"
	loadGeneratorVersion = "velaserve.unique-leading-nonce/v1"
	maxResponseBytes     = 64 << 10
)

type Endpoint struct {
	ID         string `json:"id"`
	ChatURL    string `json:"chat_url"`
	ResetURL   string `json:"reset_url"`
	MetricsURL string `json:"metrics_url"`
}

type Config struct {
	Model             string
	GatewayChatURL    string
	LoadProfiles      []LoadProfile
	LoadCalibration   LoadCalibration
	Stabilization     time.Duration
	RequestTimeout    time.Duration
	DrainTimeout      time.Duration
	DrainPollInterval time.Duration
	Authorization     string
	Endpoints         []Endpoint
	HTTPClient        *http.Client
	MaxInflightLoads  int
}

type LoadProfile struct {
	PrefixTokens      uint64  `json:"prefix_tokens"`
	MaxTokens         uint32  `json:"max_tokens"`
	SaturationQPS     float64 `json:"saturation_qps"`
	MeasurementSource string  `json:"measurement_source"`
	CalibrationSHA256 string  `json:"calibration_sha256"`
}

type Driver struct {
	config                Config
	loadProfilesSHA256    string
	loadCalibrationSHA256 string

	mu       sync.Mutex
	cancel   context.CancelFunc
	loadDone chan struct{}
	active   *activeCondition
}

type activeCondition struct {
	runID         string
	groupID       string
	state         conditioncontroller.AppliedState
	owners        []Endpoint
	warmupContent string
	prefixTokens  uint64
	loadStartedAt time.Time
	offered       atomic.Uint64
	successes     atomic.Uint64
	dropped       atomic.Uint64
	latencyNS     atomic.Uint64
}

func New(config Config) (*Driver, error) {
	config.Model = strings.TrimSpace(config.Model)
	config.GatewayChatURL = strings.TrimSpace(config.GatewayChatURL)
	config.Authorization = strings.TrimSpace(config.Authorization)
	if config.Stabilization == 0 {
		config.Stabilization = 10 * time.Second
	}
	if config.RequestTimeout == 0 {
		config.RequestTimeout = 30 * time.Second
	}
	if config.DrainTimeout == 0 {
		config.DrainTimeout = 2 * time.Minute
	}
	if config.DrainPollInterval == 0 {
		config.DrainPollInterval = 500 * time.Millisecond
	}
	if config.MaxInflightLoads == 0 {
		config.MaxInflightLoads = 256
	}
	if config.Model == "" || !validHTTPURL(config.GatewayChatURL) {
		return nil, fmt.Errorf("model and absolute gateway chat URL are required")
	}
	if len(config.LoadProfiles) == 0 {
		return nil, fmt.Errorf("at least one measured load profile is required")
	}
	config.LoadProfiles = append([]LoadProfile(nil), config.LoadProfiles...)
	sort.Slice(config.LoadProfiles, func(i, j int) bool {
		if config.LoadProfiles[i].PrefixTokens == config.LoadProfiles[j].PrefixTokens {
			return config.LoadProfiles[i].MaxTokens < config.LoadProfiles[j].MaxTokens
		}
		return config.LoadProfiles[i].PrefixTokens < config.LoadProfiles[j].PrefixTokens
	})
	loadCalibrationSHA256, err := ValidateLoadCalibration(config.LoadCalibration, config.LoadProfiles, config.Model)
	if err != nil {
		return nil, err
	}
	for index, profile := range config.LoadProfiles {
		if profile.PrefixTokens == 0 || profile.MaxTokens == 0 || math.IsNaN(profile.SaturationQPS) || math.IsInf(profile.SaturationQPS, 0) || profile.SaturationQPS <= 0 || profile.SaturationQPS > 100_000 || strings.TrimSpace(profile.MeasurementSource) == "" || !lowerHexSHA256(profile.CalibrationSHA256) {
			return nil, fmt.Errorf("load profile %d is incomplete or invalid", index)
		}
		if index > 0 && profile.PrefixTokens == config.LoadProfiles[index-1].PrefixTokens && profile.MaxTokens == config.LoadProfiles[index-1].MaxTokens {
			return nil, fmt.Errorf("duplicate load profile for prefix_tokens=%d max_tokens=%d", profile.PrefixTokens, profile.MaxTokens)
		}
	}
	loadProfilesSHA256, err := LoadProfilesSHA256(config.LoadProfiles)
	if err != nil {
		return nil, err
	}
	if config.Stabilization <= 0 || config.RequestTimeout <= 0 || config.DrainTimeout <= 0 || config.DrainPollInterval <= 0 || config.DrainPollInterval >= config.DrainTimeout || config.MaxInflightLoads <= 0 {
		return nil, fmt.Errorf("stabilization, request timeout, and maximum inflight load must be positive")
	}
	if len(config.Endpoints) == 0 {
		return nil, fmt.Errorf("at least one model endpoint is required")
	}
	config.Endpoints = append([]Endpoint(nil), config.Endpoints...)
	seen := make(map[string]struct{}, len(config.Endpoints))
	for index := range config.Endpoints {
		endpoint := &config.Endpoints[index]
		endpoint.ID = strings.TrimSpace(endpoint.ID)
		endpoint.ChatURL = strings.TrimSpace(endpoint.ChatURL)
		endpoint.ResetURL = strings.TrimSpace(endpoint.ResetURL)
		endpoint.MetricsURL = strings.TrimSpace(endpoint.MetricsURL)
		if endpoint.ID == "" || !validHTTPURL(endpoint.ChatURL) || !validHTTPURL(endpoint.ResetURL) || !validHTTPURL(endpoint.MetricsURL) {
			return nil, fmt.Errorf("endpoint %d requires an ID and absolute chat/reset/metrics URLs", index)
		}
		if _, exists := seen[endpoint.ID]; exists {
			return nil, fmt.Errorf("duplicate endpoint ID %q", endpoint.ID)
		}
		seen[endpoint.ID] = struct{}{}
	}
	sort.Slice(config.Endpoints, func(i, j int) bool { return config.Endpoints[i].ID < config.Endpoints[j].ID })
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{Timeout: config.RequestTimeout}
	}
	return &Driver{config: config, loadProfilesSHA256: loadProfilesSHA256, loadCalibrationSHA256: loadCalibrationSHA256}, nil
}

func (driver *Driver) Close() {
	driver.mu.Lock()
	defer driver.mu.Unlock()
	driver.stopLoadLocked()
	driver.active = nil
}

func (driver *Driver) Apply(ctx context.Context, request conditioncontroller.Request) (conditioncontroller.AppliedState, error) {
	if ctx == nil {
		return conditioncontroller.AppliedState{}, fmt.Errorf("context is required")
	}
	if request.SchemaVersion != conditioncontroller.RequestSchemaVersion || strings.TrimSpace(request.RunID) == "" || strings.TrimSpace(request.GroupID) == "" || strings.TrimSpace(request.CommonPrefix) == "" || strings.TrimSpace(request.WarmupContent) == "" || !strings.HasPrefix(request.WarmupContent, request.CommonPrefix) {
		return conditioncontroller.AppliedState{}, fmt.Errorf("condition request schema, identity, and common prefix are required")
	}

	driver.mu.Lock()
	defer driver.mu.Unlock()
	if driver.active != nil {
		return conditioncontroller.AppliedState{}, fmt.Errorf("condition %s/%s was not finalized", driver.active.runID, driver.active.groupID)
	}
	drainedEndpointIDs, drainStableSamples, err := driver.waitForDrain(ctx)
	if err != nil {
		return conditioncontroller.AppliedState{}, err
	}

	owners, ownerOrder, err := driver.owners(request.Cell.CacheState, request.PrefixSourceCount, request.OwnerRotation)
	if err != nil {
		return conditioncontroller.AppliedState{}, err
	}
	if request.Cell.PrefixTokens == 0 {
		return conditioncontroller.AppliedState{}, fmt.Errorf("declared prefix token count is required")
	}
	if err := driver.realizeAndVerifyCacheTopology(ctx, request.WarmupContent, request.Cell.PrefixTokens, owners); err != nil {
		return conditioncontroller.AppliedState{}, err
	}

	loadProfile, loadProfileSHA256, err := driver.loadProfile(request.Cell.PrefixTokens, request.Cell.MaxTokens)
	if err != nil {
		return conditioncontroller.AppliedState{}, err
	}
	observed := evidence.ConditionObservedState{
		SaturationQPS:         loadProfile.SaturationQPS,
		LoadProfileSHA256:     loadProfileSHA256,
		LoadProfilesSHA256:    driver.loadProfilesSHA256,
		LoadCalibrationSHA256: driver.loadCalibrationSHA256,
		MeasurementSource:     loadProfile.MeasurementSource,
		DrainedEndpointIDs:    drainedEndpointIDs,
		DrainStableSamples:    drainStableSamples,
		OwnerEndpointOrder:    ownerOrder,
	}
	active := &activeCondition{
		runID: request.RunID, groupID: request.GroupID, owners: append([]Endpoint(nil), owners...),
		warmupContent: request.WarmupContent, prefixTokens: request.Cell.PrefixTokens,
	}
	for _, endpoint := range owners {
		observed.CachedEndpointIDs = append(observed.CachedEndpointIDs, endpoint.ID)
	}
	if request.PrefixSourceCount > 0 {
		observed.PrefixSourceEndpointIDs = append([]string(nil), observed.CachedEndpointIDs...)
	}

	fraction, err := loadFraction(request.Cell.LoadRegime)
	if err != nil {
		return conditioncontroller.AppliedState{}, err
	}
	if fraction > 0 {
		targetQPS := loadProfile.SaturationQPS * fraction
		loadContext, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		driver.cancel = cancel
		driver.loadDone = done
		active.loadStartedAt = time.Now().UTC()
		go driver.runLoad(loadContext, done, targetQPS, request.RunID+"-"+request.GroupID, request.WarmupContent, loadProfile.MaxTokens, &active.offered, &active.successes, &active.dropped, &active.latencyNS)

		timer := time.NewTimer(driver.config.Stabilization)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			driver.stopLoadLocked()
			return conditioncontroller.AppliedState{}, ctx.Err()
		case <-timer.C:
		}
		observed.OfferedLoadQPS = float64(active.offered.Load()) / driver.config.Stabilization.Seconds()
		observed.AchievedLoadQPS = float64(active.successes.Load()) / driver.config.Stabilization.Seconds()
		if successfulLoads := active.successes.Load(); successfulLoads > 0 {
			observed.OrdinaryTrafficMeanLatencySeconds = float64(active.latencyNS.Load()) / float64(successfulLoads) / float64(time.Second)
		}
		observed.DroppedLoadRequests = active.dropped.Load()
		if observed.DroppedLoadRequests != 0 {
			driver.stopLoadLocked()
			return conditioncontroller.AppliedState{}, fmt.Errorf("background load generator dropped %d intended arrivals", observed.DroppedLoadRequests)
		}
		if err := driver.verifyOwnersRemainWarm(ctx, request.WarmupContent, request.Cell.PrefixTokens, owners); err != nil {
			driver.stopLoadLocked()
			return conditioncontroller.AppliedState{}, err
		}
	}

	state := conditioncontroller.AppliedState{
		SchemaVersion: appliedSchemaVersion, LoadRegime: request.Cell.LoadRegime,
		CacheState: request.Cell.CacheState, PrefixSourceCount: request.PrefixSourceCount, OwnerRotation: request.OwnerRotation, ObservedState: observed, AppliedAt: time.Now().UTC(),
	}
	if err := evidence.ValidateConditionObservedState(observed, state.LoadRegime, state.CacheState, state.PrefixSourceCount); err != nil {
		driver.stopLoadLocked()
		return conditioncontroller.AppliedState{}, fmt.Errorf("measured condition does not satisfy the preregistered band: %w", err)
	}
	active.state = state
	driver.active = active
	return state, nil
}

func (driver *Driver) Finalize(ctx context.Context, request conditioncontroller.FinalizeRequest) (conditioncontroller.AppliedState, error) {
	if ctx == nil {
		return conditioncontroller.AppliedState{}, fmt.Errorf("context is required")
	}
	driver.mu.Lock()
	defer driver.mu.Unlock()
	if request.SchemaVersion != conditioncontroller.FinalizeRequestSchemaVersion || strings.TrimSpace(request.RunID) == "" || strings.TrimSpace(request.GroupID) == "" || driver.active == nil || request.RunID != driver.active.runID || request.GroupID != driver.active.groupID {
		return conditioncontroller.AppliedState{}, fmt.Errorf("finalization does not match the active condition")
	}
	active := driver.active
	defer func() { driver.active = nil }()
	driver.stopLoadLocked()
	state := active.state
	if !active.loadStartedAt.IsZero() {
		duration := time.Since(active.loadStartedAt).Seconds()
		if duration <= 0 {
			return conditioncontroller.AppliedState{}, fmt.Errorf("background load measurement duration is invalid")
		}
		state.ObservedState.OfferedLoadQPS = float64(active.offered.Load()) / duration
		state.ObservedState.AchievedLoadQPS = float64(active.successes.Load()) / duration
		state.ObservedState.DroppedLoadRequests = active.dropped.Load()
		if successfulLoads := active.successes.Load(); successfulLoads > 0 {
			state.ObservedState.OrdinaryTrafficMeanLatencySeconds = float64(active.latencyNS.Load()) / float64(successfulLoads) / float64(time.Second)
		}
	}
	if err := driver.verifyOwnersRemainWarm(ctx, active.warmupContent, active.prefixTokens, active.owners); err != nil {
		return conditioncontroller.AppliedState{}, fmt.Errorf("final cache-state verification: %w", err)
	}
	finalizedAt := time.Now().UTC()
	state.FinalizedAt = &finalizedAt
	if err := evidence.ValidateConditionObservedState(state.ObservedState, state.LoadRegime, state.CacheState, state.PrefixSourceCount); err != nil {
		return conditioncontroller.AppliedState{}, fmt.Errorf("full group-window condition does not satisfy the preregistered band: %w", err)
	}
	return state, nil
}

func (driver *Driver) owners(cacheState string, prefixSourceCount, ownerRotation uint32) ([]Endpoint, []string, error) {
	count := 0
	if prefixSourceCount > 0 {
		if cacheState != "distributed-warm" || (prefixSourceCount != 1 && prefixSourceCount != 2 && prefixSourceCount != 4) {
			return nil, nil, fmt.Errorf("Z0-C requires distributed-warm with 1, 2, or 4 prefix sources")
		}
		count = int(prefixSourceCount)
	} else {
		switch cacheState {
		case "cold":
			count = 0
		case "warm-owner":
			count = 1
		case "distributed-warm":
			count = len(driver.config.Endpoints)
			if count < 2 {
				return nil, nil, fmt.Errorf("distributed-warm requires at least two configured endpoints")
			}
		default:
			return nil, nil, fmt.Errorf("unsupported cache state %q", cacheState)
		}
	}
	if count > len(driver.config.Endpoints) {
		return nil, nil, fmt.Errorf("requested %d cache owners but only %d endpoints are configured", count, len(driver.config.Endpoints))
	}
	rotation := int(ownerRotation % uint32(len(driver.config.Endpoints)))
	ordered := append([]Endpoint(nil), driver.config.Endpoints[rotation:]...)
	ordered = append(ordered, driver.config.Endpoints[:rotation]...)
	orderIDs := make([]string, len(ordered))
	for index := range ordered {
		orderIDs[index] = ordered[index].ID
	}
	return append([]Endpoint(nil), ordered[:count]...), orderIDs, nil
}

func (driver *Driver) runLoad(ctx context.Context, done chan<- struct{}, targetQPS float64, namespace, commonPrefix string, maxTokens uint32, offered, successes, dropped, latencyNanoseconds *atomic.Uint64) {
	defer close(done)
	interval := time.Duration(float64(time.Second) / targetQPS)
	if interval < 10*time.Microsecond {
		interval = 10 * time.Microsecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	semaphore := make(chan struct{}, driver.config.MaxInflightLoads)
	var requests sync.WaitGroup
	var sequence atomic.Uint64
	defer requests.Wait()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			offered.Add(1)
			select {
			case semaphore <- struct{}{}:
				requests.Add(1)
				go func(number uint64) {
					defer requests.Done()
					defer func() { <-semaphore }()
					prompt := backgroundLoadPrompt(namespace, number, commonPrefix)
					startedAt := time.Now()
					if _, err := driver.chat(ctx, driver.config.GatewayChatURL, prompt, maxTokens); err == nil {
						successes.Add(1)
						latencyNanoseconds.Add(uint64(time.Since(startedAt)))
					}
				}(sequence.Add(1))
			default:
				dropped.Add(1)
			}
		}
	}
}

func (driver *Driver) stopLoadLocked() {
	if driver.cancel == nil {
		return
	}
	driver.cancel()
	<-driver.loadDone
	driver.cancel = nil
	driver.loadDone = nil
}

func (driver *Driver) waitForDrain(ctx context.Context) ([]string, uint32, error) {
	drainContext, cancel := context.WithTimeout(ctx, driver.config.DrainTimeout)
	defer cancel()
	stable := uint32(0)
	for {
		allIdle := true
		for _, endpoint := range driver.config.Endpoints {
			running, waiting, err := driver.requestCounts(drainContext, endpoint.MetricsURL)
			if err != nil {
				return nil, stable, fmt.Errorf("drain metrics endpoint %q: %w", endpoint.ID, err)
			}
			if running != 0 || waiting != 0 {
				allIdle = false
			}
		}
		if allIdle {
			stable++
			if stable >= 2 {
				ids := make([]string, 0, len(driver.config.Endpoints))
				for _, endpoint := range driver.config.Endpoints {
					ids = append(ids, endpoint.ID)
				}
				return ids, stable, nil
			}
		} else {
			stable = 0
		}
		timer := time.NewTimer(driver.config.DrainPollInterval)
		select {
		case <-drainContext.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, stable, fmt.Errorf("drain timed out before every vLLM endpoint reported zero running and waiting requests for two samples: %w", drainContext.Err())
		case <-timer.C:
		}
	}
}

func (driver *Driver) requestCounts(ctx context.Context, endpoint string) (float64, float64, error) {
	contents, err := driver.get(ctx, endpoint)
	if err != nil {
		return 0, 0, err
	}
	running, foundRunning, err := prometheusMetricSum(contents, "vllm:num_requests_running")
	if err != nil {
		return 0, 0, err
	}
	waiting, foundWaiting, err := prometheusMetricSum(contents, "vllm:num_requests_waiting")
	if err != nil {
		return 0, 0, err
	}
	if !foundRunning || !foundWaiting {
		return 0, 0, fmt.Errorf("required vLLM running/waiting gauges are absent")
	}
	return running, waiting, nil
}

func prometheusMetricSum(contents []byte, metric string) (float64, bool, error) {
	total := 0.0
	found := false
	for _, line := range strings.Split(string(contents), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := fields[0]
		if index := strings.IndexByte(name, '{'); index >= 0 {
			name = name[:index]
		}
		if name != metric {
			continue
		}
		value, err := strconv.ParseFloat(fields[1], 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return 0, false, fmt.Errorf("metric %s has an invalid sample", metric)
		}
		total += value
		found = true
	}
	return total, found, nil
}

type chatObservation struct {
	PromptTokens uint64
	CachedTokens *uint64
}

func (driver *Driver) realizeAndVerifyCacheTopology(ctx context.Context, prefix string, minimumCachedTokens uint64, owners []Endpoint) error {
	for _, endpoint := range driver.config.Endpoints {
		if err := driver.resetEndpoint(ctx, endpoint.ResetURL); err != nil {
			return fmt.Errorf("reset endpoint %q: %w", endpoint.ID, err)
		}
	}
	// A successful reset call is not sufficient evidence. Probe every endpoint,
	// require a per-request zero cached-token readback, then reset it again so
	// the probe itself cannot contaminate the declared experiment state.
	for _, endpoint := range driver.config.Endpoints {
		observation, err := driver.chat(ctx, endpoint.ChatURL, prefix, 1)
		if err != nil {
			return fmt.Errorf("cold-probe endpoint %q: %w", endpoint.ID, err)
		}
		if err := requireCacheReadback(observation, false, minimumCachedTokens); err != nil {
			return fmt.Errorf("cold endpoint %q cached-token readback: %w", endpoint.ID, err)
		}
		if err := driver.resetEndpoint(ctx, endpoint.ResetURL); err != nil {
			return fmt.Errorf("reset endpoint %q after cold probe: %w", endpoint.ID, err)
		}
	}
	for _, endpoint := range owners {
		first, err := driver.chat(ctx, endpoint.ChatURL, prefix, 1)
		if err != nil {
			return fmt.Errorf("warm endpoint %q: %w", endpoint.ID, err)
		}
		if err := requireCacheReadback(first, false, minimumCachedTokens); err != nil {
			return fmt.Errorf("warm endpoint %q initial cached-token readback: %w", endpoint.ID, err)
		}
		second, err := driver.chat(ctx, endpoint.ChatURL, prefix, 1)
		if err != nil {
			return fmt.Errorf("verify warm endpoint %q: %w", endpoint.ID, err)
		}
		if err := requireCacheReadback(second, true, minimumCachedTokens); err != nil {
			return fmt.Errorf("warm endpoint %q cached-token readback: %w", endpoint.ID, err)
		}
	}
	return nil
}

func (driver *Driver) resetEndpoint(ctx context.Context, endpoint string) error {
	parsed, err := url.ParseRequestURI(endpoint)
	if err != nil {
		return err
	}
	query := parsed.Query()
	query.Set("reset_external", "true")
	parsed.RawQuery = query.Encode()
	contents, err := driver.post(ctx, parsed.String(), nil)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	var result struct {
		Success bool `json:"success"`
	}
	if err := decoder.Decode(&result); err != nil || !result.Success {
		return fmt.Errorf("vLLM reset_prefix_cache did not confirm external-cache reset")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("vLLM reset_prefix_cache returned trailing content")
	}
	return nil
}

func (driver *Driver) verifyOwnersRemainWarm(ctx context.Context, prefix string, minimumCachedTokens uint64, owners []Endpoint) error {
	for _, endpoint := range owners {
		observation, err := driver.chat(ctx, endpoint.ChatURL, prefix, 1)
		if err != nil {
			return fmt.Errorf("post-load verify warm endpoint %q: %w", endpoint.ID, err)
		}
		if err := requireCacheReadback(observation, true, minimumCachedTokens); err != nil {
			return fmt.Errorf("post-load warm endpoint %q cached-token readback: %w", endpoint.ID, err)
		}
	}
	return nil
}

func requireCacheReadback(observation chatObservation, wantHit bool, minimumCachedTokens uint64) error {
	if observation.PromptTokens == 0 || observation.CachedTokens == nil || *observation.CachedTokens > observation.PromptTokens {
		return fmt.Errorf("missing or invalid usage.prompt_tokens_details.cached_tokens")
	}
	if wantHit && *observation.CachedTokens < minimumCachedTokens {
		return fmt.Errorf("exact repeated prefix reported %d cached tokens, need at least %d", *observation.CachedTokens, minimumCachedTokens)
	}
	if !wantHit && *observation.CachedTokens != 0 {
		return fmt.Errorf("reset prefix reported %d cached tokens", *observation.CachedTokens)
	}
	return nil
}

func (driver *Driver) chat(ctx context.Context, endpoint, prompt string, maxTokens uint32) (chatObservation, error) {
	payload := struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		MaxTokens uint32 `json:"max_tokens"`
		Stream    bool   `json:"stream"`
	}{Model: driver.config.Model, MaxTokens: maxTokens, Stream: false}
	payload.Messages = append(payload.Messages, struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}{Role: "user", Content: prompt})
	contents, err := driver.post(ctx, endpoint, payload)
	if err != nil {
		return chatObservation{}, err
	}
	var completion struct {
		Choices []json.RawMessage `json:"choices"`
		Usage   json.RawMessage   `json:"usage"`
	}
	if err := json.Unmarshal(contents, &completion); err != nil || len(completion.Choices) == 0 || len(completion.Usage) == 0 || bytes.Equal(bytes.TrimSpace(completion.Usage), []byte("null")) {
		return chatObservation{}, fmt.Errorf("chat response is not a completed OpenAI-compatible response")
	}
	var usage struct {
		PromptTokens        uint64          `json:"prompt_tokens"`
		PromptTokensDetails json.RawMessage `json:"prompt_tokens_details"`
	}
	if err := json.Unmarshal(completion.Usage, &usage); err != nil || len(usage.PromptTokensDetails) == 0 {
		return chatObservation{}, fmt.Errorf("chat response is missing usage.prompt_tokens_details")
	}
	observation := chatObservation{PromptTokens: usage.PromptTokens}
	if bytes.Equal(bytes.TrimSpace(usage.PromptTokensDetails), []byte("null")) {
		return chatObservation{}, fmt.Errorf("chat response is missing usage.prompt_tokens_details.cached_tokens")
	}
	var details struct {
		CachedTokens *uint64 `json:"cached_tokens"`
	}
	if err := json.Unmarshal(usage.PromptTokensDetails, &details); err != nil || details.CachedTokens == nil {
		return chatObservation{}, fmt.Errorf("chat response is missing usage.prompt_tokens_details.cached_tokens")
	}
	observation.CachedTokens = details.CachedTokens
	return observation, nil
}

func (driver *Driver) post(ctx context.Context, endpoint string, payload any) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(encoded)
	}
	requestContext, cancel := context.WithTimeout(ctx, driver.config.RequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodPost, endpoint, body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	if driver.config.Authorization != "" {
		request.Header.Set("Authorization", driver.config.Authorization)
	}
	response, err := driver.config.HTTPClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	contents, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if readErr != nil {
		return nil, readErr
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("status %s: %s", response.Status, strings.TrimSpace(string(contents)))
	}
	if len(contents) > maxResponseBytes {
		return nil, fmt.Errorf("response exceeds %d bytes", maxResponseBytes)
	}
	return contents, nil
}

func (driver *Driver) get(ctx context.Context, endpoint string) ([]byte, error) {
	requestContext, cancel := context.WithTimeout(ctx, driver.config.RequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if driver.config.Authorization != "" {
		request.Header.Set("Authorization", driver.config.Authorization)
	}
	response, err := driver.config.HTTPClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	contents, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if readErr != nil {
		return nil, readErr
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("status %s: %s", response.Status, strings.TrimSpace(string(contents)))
	}
	if len(contents) > maxResponseBytes {
		return nil, fmt.Errorf("response exceeds %d bytes", maxResponseBytes)
	}
	return contents, nil
}

func loadFraction(regime evidence.LoadRegime) (float64, error) {
	switch regime {
	case evidence.LoadIdle:
		return 0, nil
	case evidence.LoadModerate:
		return 0.5, nil
	case evidence.LoadNearSaturation:
		return 0.9, nil
	default:
		return 0, fmt.Errorf("unsupported load regime %q", regime)
	}
}

func (driver *Driver) loadProfile(prefixTokens uint64, maxTokens uint32) (LoadProfile, string, error) {
	for _, profile := range driver.config.LoadProfiles {
		if profile.PrefixTokens == prefixTokens && profile.MaxTokens == maxTokens {
			digest, err := LoadProfileSHA256(profile)
			return profile, digest, err
		}
	}
	return LoadProfile{}, "", fmt.Errorf("no measured load profile for prefix_tokens=%d max_tokens=%d", prefixTokens, maxTokens)
}

// LoadProfilesSHA256 returns the canonical binding used both by the live
// driver and cloud preflight. Input order does not affect the digest.
func LoadProfilesSHA256(profiles []LoadProfile) (string, error) {
	profiles = append([]LoadProfile(nil), profiles...)
	sort.Slice(profiles, func(i, j int) bool {
		if profiles[i].PrefixTokens == profiles[j].PrefixTokens {
			return profiles[i].MaxTokens < profiles[j].MaxTokens
		}
		return profiles[i].PrefixTokens < profiles[j].PrefixTokens
	})
	bound := struct {
		SchemaVersion string        `json:"schema_version"`
		Generator     string        `json:"generator"`
		Profiles      []LoadProfile `json:"profiles"`
	}{SchemaVersion: loadProfileSchema, Generator: loadGeneratorVersion, Profiles: profiles}
	encoded, err := json.Marshal(bound)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", digest), nil
}

func LoadProfileSHA256(profile LoadProfile) (string, error) {
	bound := struct {
		SchemaVersion string      `json:"schema_version"`
		Generator     string      `json:"generator"`
		Profile       LoadProfile `json:"profile"`
	}{SchemaVersion: loadProfileSchema, Generator: loadGeneratorVersion, Profile: profile}
	encoded, err := json.Marshal(bound)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", digest), nil
}

func lowerHexSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validHTTPURL(raw string) bool {
	parsed, err := url.ParseRequestURI(raw)
	return err == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.User == nil
}

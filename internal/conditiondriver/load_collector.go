package conditiondriver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/JDinSeattle/velaserve/internal/bench"
	"github.com/JDinSeattle/velaserve/internal/evidence"
	"github.com/JDinSeattle/velaserve/internal/fanout/protocol"
)

const maxCalibrationRequestsPerTrial = uint64(1_000_000)

// LoadCalibrationCollectionOptions binds an executable open-loop saturation
// sweep to the exact model deployment and tokenizer-calibrated benchmark
// profile. AWS orchestration is deliberately outside this collector.
type LoadCalibrationCollectionOptions struct {
	Endpoint                 string
	GatewayChatURL           string
	Authorization            string
	Profile                  bench.Profile
	ProfileCalibrationSHA256 string
	ModelRevision            string
	ModelImage               string
	InstanceType             string
	GPUModel                 string
	GPUDriverVersion         string
	ReplicaCount             uint32
	Transport                string
	ActiveArm                evidence.Arm
	RoutingSHA256            string
	RouterConfigSHA256       string
	Endpoints                []LoadCalibrationEndpoint
	// MetricsAccessURLs optionally supplies operator-local tunnels while the
	// retained Endpoints continue to bind stable in-cluster URLs.
	MetricsAccessURLs map[string]string
	RatesQPS          []float64
	WarmupDuration    time.Duration
	TrialDuration     time.Duration
	RequestTimeout    time.Duration
	DrainTimeout      time.Duration
	DrainPollInterval time.Duration
	MaxInflight       int
	HTTPClient        *http.Client
	Now               func() time.Time
}

type loadTrialOptions struct {
	Endpoint       string
	Authorization  string
	Model          string
	CommonPrefix   string
	PrefixTokens   uint64
	MaxTokens      uint32
	RateQPS        float64
	Duration       time.Duration
	RequestTimeout time.Duration
	MaxInflight    int
	HTTPClient     *http.Client
}

// CollectLoadCalibration runs every prefix/output/rate combination
// sequentially. HTTP failures remain measured trial outcomes; cancellation or
// an invalid collection contract is returned as an operational error.
func CollectLoadCalibration(ctx context.Context, options LoadCalibrationCollectionOptions) (LoadCalibration, error) {
	if ctx == nil {
		return LoadCalibration{}, fmt.Errorf("context is required")
	}
	if err := validateLoadCalibrationCollectionOptions(options); err != nil {
		return LoadCalibration{}, err
	}
	now := time.Now
	if options.Now != nil {
		now = options.Now
	}
	boundGatewayChatURL := strings.TrimSpace(options.GatewayChatURL)
	if boundGatewayChatURL == "" {
		boundGatewayChatURL = strings.TrimSpace(options.Endpoint)
	}
	calibration := LoadCalibration{
		SchemaVersion: LoadCalibrationSchemaVersion, ModelID: options.Profile.Model, ModelRevision: options.ModelRevision,
		ModelImage: options.ModelImage, InstanceType: options.InstanceType, GPUModel: options.GPUModel,
		GPUDriverVersion: options.GPUDriverVersion, ReplicaCount: options.ReplicaCount, GPUsPerReplica: 1,
		Transport: options.Transport, ActiveArm: options.ActiveArm, RoutingSHA256: options.RoutingSHA256,
		RouterConfigSHA256: options.RouterConfigSHA256, ProfileCalibrationSHA256: options.ProfileCalibrationSHA256,
		GatewayChatURL: boundGatewayChatURL,
		Endpoints:      append([]LoadCalibrationEndpoint(nil), options.Endpoints...), MeasuredAt: now().UTC(),
	}
	sort.Slice(calibration.Endpoints, func(i, j int) bool { return calibration.Endpoints[i].ID < calibration.Endpoints[j].ID })
	measurementEndpoints, err := loadCalibrationMeasurementEndpoints(calibration.Endpoints, options.MetricsAccessURLs)
	if err != nil {
		return LoadCalibration{}, err
	}
	for prefixIndex, prefix := range options.Profile.Prefixes {
		// Match Driver.Apply exactly: production background load is generated
		// from WarmupContent, including the boundary-sensitive calibration
		// suffix, rather than the bare common prefix.
		commonPrefix := calibrationWorkloadContent(prefix)
		for outputIndex, output := range options.Profile.Outputs {
			for rateIndex, rate := range options.RatesQPS {
				trialID := fmt.Sprintf("load-p%02d-o%02d-r%02d", prefixIndex+1, outputIndex+1, rateIndex+1)
				trial := LoadCalibrationTrial{
					TrialID: trialID, PrefixTokens: prefix.EstimatedTokens, MaxTokens: output.MaxTokens,
					WarmupDurationSeconds: options.WarmupDuration.Seconds(), DurationSeconds: options.TrialDuration.Seconds(),
				}
				preDrain, err := waitForLoadCalibrationDrain(ctx, measurementEndpoints, options.HTTPClient, options.DrainTimeout, options.DrainPollInterval)
				trial.PreDrain = preDrain
				if err != nil {
					calibration.Trials = append(calibration.Trials, trial)
					return calibration, fmt.Errorf("collect %s pre-drain: %w", trialID, err)
				}
				warmup, err := runLoadCalibrationTrial(ctx, loadTrialOptions{
					Endpoint: options.Endpoint, Authorization: options.Authorization, Model: options.Profile.Model,
					CommonPrefix: commonPrefix, PrefixTokens: prefix.EstimatedTokens, MaxTokens: output.MaxTokens,
					RateQPS: rate, Duration: options.WarmupDuration, RequestTimeout: options.RequestTimeout,
					MaxInflight: options.MaxInflight, HTTPClient: options.HTTPClient,
				}, trialID+"-warmup")
				copyWarmupResult(&trial, warmup)
				if err != nil {
					calibration.Trials = append(calibration.Trials, trial)
					return calibration, fmt.Errorf("collect %s warmup: %w", trialID, err)
				}
				warmupDrain, err := waitForLoadCalibrationDrain(ctx, measurementEndpoints, options.HTTPClient, options.DrainTimeout, options.DrainPollInterval)
				trial.WarmupDrain = warmupDrain
				if err != nil {
					calibration.Trials = append(calibration.Trials, trial)
					return calibration, fmt.Errorf("collect %s warmup drain: %w", trialID, err)
				}
				measured, measureErr := runLoadCalibrationTrial(ctx, loadTrialOptions{
					Endpoint: options.Endpoint, Authorization: options.Authorization, Model: options.Profile.Model,
					CommonPrefix: commonPrefix, PrefixTokens: prefix.EstimatedTokens, MaxTokens: output.MaxTokens,
					RateQPS: rate, Duration: options.TrialDuration, RequestTimeout: options.RequestTimeout,
					MaxInflight: options.MaxInflight, HTTPClient: options.HTTPClient,
				}, trialID)
				copyMeasurementResult(&trial, measured)
				postDrain, drainErr := waitForLoadCalibrationDrain(ctx, measurementEndpoints, options.HTTPClient, options.DrainTimeout, options.DrainPollInterval)
				trial.PostDrain = postDrain
				calibration.Trials = append(calibration.Trials, trial)
				if measureErr != nil {
					return calibration, fmt.Errorf("collect %s measurement: %w", trialID, measureErr)
				}
				if drainErr != nil {
					return calibration, fmt.Errorf("collect %s post-drain: %w", trialID, drainErr)
				}
			}
		}
	}
	return calibration, nil
}

func copyWarmupResult(destination *LoadCalibrationTrial, source LoadCalibrationTrial) {
	destination.WarmupStartedAt = source.MeasurementStartedAt
	destination.WarmupCompletedAt = source.MeasurementCompletedAt
	destination.WarmupOfferedRequests = source.OfferedRequests
	destination.WarmupSuccessfulRequests = source.SuccessfulRequests
	destination.WarmupLateSuccessfulRequests = source.LateSuccessfulRequests
	destination.WarmupFailedRequests = source.FailedRequests
	destination.WarmupDroppedArrivals = source.DroppedArrivals
}

func copyMeasurementResult(destination *LoadCalibrationTrial, source LoadCalibrationTrial) {
	destination.MeasurementStartedAt = source.MeasurementStartedAt
	destination.MeasurementSchedulingEndedAt = source.MeasurementSchedulingEndedAt
	destination.MeasurementCompletedAt = source.MeasurementCompletedAt
	destination.OfferedRequests = source.OfferedRequests
	destination.SuccessfulRequests = source.SuccessfulRequests
	destination.LateSuccessfulRequests = source.LateSuccessfulRequests
	destination.FailedRequests = source.FailedRequests
	destination.DroppedArrivals = source.DroppedArrivals
	destination.MeanLatencySeconds = source.MeanLatencySeconds
}

func calibrationWorkloadContent(prefix bench.PrefixProfile) string {
	return strings.Repeat(prefix.RepeatText, int(prefix.RepeatCount)) + bench.CalibrationWarmupSuffix
}

func validateLoadCalibrationCollectionOptions(options LoadCalibrationCollectionOptions) error {
	boundGatewayChatURL := strings.TrimSpace(options.GatewayChatURL)
	if boundGatewayChatURL == "" {
		boundGatewayChatURL = strings.TrimSpace(options.Endpoint)
	}
	if !validHTTPURL(strings.TrimSpace(options.Endpoint)) || !validHTTPURL(boundGatewayChatURL) || strings.TrimSpace(options.Profile.Model) == "" ||
		!lowerHexCommit(options.ModelRevision) || !digestAddressedImage(options.ModelImage) ||
		strings.TrimSpace(options.InstanceType) == "" || strings.TrimSpace(options.GPUModel) == "" ||
		strings.TrimSpace(options.GPUDriverVersion) == "" || options.ReplicaCount < 2 ||
		!lowerHexSHA256(options.ProfileCalibrationSHA256) || !lowerHexSHA256(options.RoutingSHA256) || !lowerHexSHA256(options.RouterConfigSHA256) ||
		(options.ActiveArm != evidence.ArmAffinityP2P && options.ActiveArm != evidence.ArmLoadAwareP2P) {
		return fmt.Errorf("load calibration collection identity is incomplete or invalid")
	}
	if options.Transport != "tcp" && options.Transport != "efa" {
		return fmt.Errorf("load calibration transport must be tcp or efa")
	}
	if len(options.Profile.Transports) != 1 || options.Profile.Transports[0] != options.Transport {
		return fmt.Errorf("load calibration transport must match the benchmark profile")
	}
	if len(options.Profile.Prefixes) == 0 || len(options.Profile.Outputs) == 0 {
		return fmt.Errorf("load calibration profile must contain prefixes and outputs")
	}
	if options.TrialDuration < 30*time.Second {
		return fmt.Errorf("load calibration trial duration must be at least 30 seconds")
	}
	if options.RequestTimeout <= 0 || options.MaxInflight <= 0 {
		return fmt.Errorf("load calibration request timeout and maximum inflight count must be positive")
	}
	if options.WarmupDuration < 5*time.Second || options.DrainTimeout < 30*time.Second || options.DrainTimeout > 10*time.Minute || options.DrainPollInterval < 50*time.Millisecond || options.DrainPollInterval > 5*time.Second || options.DrainPollInterval >= options.DrainTimeout {
		return fmt.Errorf("load calibration requires >=5s warmup and a 30s..10m drain timeout with a 50ms..5s poll interval")
	}
	if _, err := validateLoadCalibrationEndpoints(options.Endpoints, options.ReplicaCount); err != nil {
		return err
	}
	if _, err := loadCalibrationMeasurementEndpoints(options.Endpoints, options.MetricsAccessURLs); err != nil {
		return err
	}
	if len(options.RatesQPS) < 5 {
		return fmt.Errorf("load calibration requires at least five offered rates")
	}
	previous := 0.0
	for index, rate := range options.RatesQPS {
		if !finitePositive(rate) || rate <= previous {
			return fmt.Errorf("load calibration rates must be finite, positive, and strictly increasing")
		}
		planned := math.Ceil(rate * options.TrialDuration.Seconds())
		if planned > float64(maxCalibrationRequestsPerTrial) {
			return fmt.Errorf("load calibration rate %d schedules more than %d requests per trial", index+1, maxCalibrationRequestsPerTrial)
		}
		previous = rate
	}
	return nil
}

func loadCalibrationMeasurementEndpoints(bound []LoadCalibrationEndpoint, access map[string]string) ([]LoadCalibrationEndpoint, error) {
	result := append([]LoadCalibrationEndpoint(nil), bound...)
	if len(access) == 0 {
		return result, nil
	}
	if len(access) != len(bound) {
		return nil, fmt.Errorf("metrics access URLs must cover every bound endpoint")
	}
	seen := make(map[string]struct{}, len(bound))
	for index := range result {
		url, exists := access[result[index].ID]
		if !exists || !validHTTPURL(url) {
			return nil, fmt.Errorf("metrics access URL for endpoint %q is missing or invalid", result[index].ID)
		}
		result[index].MetricsURL = url
		seen[result[index].ID] = struct{}{}
	}
	for id := range access {
		if _, exists := seen[id]; !exists {
			return nil, fmt.Errorf("metrics access URL names unknown endpoint %q", id)
		}
	}
	return result, nil
}

func runLoadCalibrationTrial(ctx context.Context, options loadTrialOptions, trialID string) (LoadCalibrationTrial, error) {
	trial := LoadCalibrationTrial{
		TrialID: trialID, PrefixTokens: options.PrefixTokens, MaxTokens: options.MaxTokens,
		DurationSeconds: options.Duration.Seconds(),
	}
	if ctx == nil || strings.TrimSpace(trialID) == "" || !validHTTPURL(options.Endpoint) || strings.TrimSpace(options.Model) == "" ||
		strings.TrimSpace(options.CommonPrefix) == "" || options.PrefixTokens == 0 || options.MaxTokens == 0 ||
		!finitePositive(options.RateQPS) || options.Duration <= 0 || options.RequestTimeout <= 0 || options.MaxInflight <= 0 {
		return trial, fmt.Errorf("load calibration trial options are incomplete or invalid")
	}
	client := options.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	plannedFloat := math.Ceil(options.RateQPS * options.Duration.Seconds())
	if plannedFloat <= 0 || plannedFloat > float64(maxCalibrationRequestsPerTrial) {
		return trial, fmt.Errorf("load calibration trial request count is outside the safety limit")
	}
	planned := uint64(plannedFloat)
	trialContext, cancel := context.WithTimeout(ctx, options.Duration)
	defer cancel()
	startedAt := time.Now()
	measurementDeadline := startedAt.Add(options.Duration)
	trial.MeasurementStartedAt = startedAt.UTC()
	semaphore := make(chan struct{}, options.MaxInflight)
	var requests sync.WaitGroup
	var successes atomic.Uint64
	var lateSuccesses atomic.Uint64
	var failures atomic.Uint64
	var dropped atomic.Uint64
	var latencyNanoseconds atomic.Uint64
	finish := func() {
		if trial.MeasurementSchedulingEndedAt.IsZero() {
			trial.MeasurementSchedulingEndedAt = time.Now().UTC()
		}
		requests.Wait()
		trial.MeasurementCompletedAt = time.Now().UTC()
		populateLoadCalibrationTrial(&trial, successes.Load(), lateSuccesses.Load(), failures.Load(), dropped.Load(), latencyNanoseconds.Load())
	}
schedule:
	for sequence := uint64(0); sequence < planned; sequence++ {
		select {
		case <-ctx.Done():
			finish()
			return trial, ctx.Err()
		case <-trialContext.Done():
			break schedule
		default:
		}
		offset := time.Duration(float64(sequence) / options.RateQPS * float64(time.Second))
		if offset >= options.Duration {
			break
		}
		wait := time.Until(startedAt.Add(offset))
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				finish()
				return trial, ctx.Err()
			case <-trialContext.Done():
				if !timer.Stop() {
					<-timer.C
				}
				break schedule
			case <-timer.C:
			}
		}
		trial.OfferedRequests++
		select {
		case semaphore <- struct{}{}:
			requests.Add(1)
			go func(number uint64) {
				defer requests.Done()
				defer func() { <-semaphore }()
				requestStartedAt := time.Now()
				err := executeLoadCalibrationRequest(ctx, client, options, trialID, number)
				elapsed := time.Since(requestStartedAt)
				if elapsed > 0 {
					latencyNanoseconds.Add(uint64(elapsed))
				}
				if err != nil {
					failures.Add(1)
					return
				}
				if time.Now().After(measurementDeadline) {
					lateSuccesses.Add(1)
				} else {
					successes.Add(1)
				}
			}(sequence + 1)
		default:
			dropped.Add(1)
		}
	}
	trial.MeasurementSchedulingEndedAt = time.Now().UTC()
	finish()
	if err := ctx.Err(); err != nil {
		return trial, err
	}
	return trial, nil
}

func populateLoadCalibrationTrial(trial *LoadCalibrationTrial, successes, lateSuccesses, failures, dropped, latencyNanoseconds uint64) {
	trial.SuccessfulRequests = successes
	trial.LateSuccessfulRequests = lateSuccesses
	trial.FailedRequests = failures
	trial.DroppedArrivals = dropped
	completed := successes + lateSuccesses + failures
	if completed > 0 {
		trial.MeanLatencySeconds = float64(latencyNanoseconds) / float64(completed) / float64(time.Second)
	}
}

func waitForLoadCalibrationDrain(ctx context.Context, endpoints []LoadCalibrationEndpoint, client *http.Client, timeout, poll time.Duration) (LoadCalibrationDrainProof, error) {
	proof := LoadCalibrationDrainProof{StartedAt: time.Now().UTC()}
	if ctx == nil || len(endpoints) == 0 || timeout <= 0 || poll <= 0 || poll >= timeout {
		return proof, fmt.Errorf("drain context, endpoints, timeout, and poll interval are required")
	}
	if client == nil {
		client = http.DefaultClient
	}
	ordered := append([]LoadCalibrationEndpoint(nil), endpoints...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	drainContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stable := uint32(0)
	for {
		sample := LoadCalibrationDrainSample{ObservedAt: time.Now().UTC()}
		allIdle := true
		for _, endpoint := range ordered {
			running, waiting, err := loadCalibrationRequestCounts(drainContext, client, endpoint.MetricsURL)
			if err != nil {
				proof.CompletedAt = time.Now().UTC()
				return proof, fmt.Errorf("endpoint %q metrics: %w", endpoint.ID, err)
			}
			sample.Endpoints = append(sample.Endpoints, LoadCalibrationDrainEndpointObservation{ID: endpoint.ID, PodUID: endpoint.PodUID, Running: running, Waiting: waiting})
			if running != 0 || waiting != 0 {
				allIdle = false
			}
		}
		proof.Samples = append(proof.Samples, sample)
		if allIdle {
			stable++
			if stable == 2 {
				proof.StableSamples = stable
				proof.CompletedAt = time.Now().UTC()
				return proof, nil
			}
		} else {
			stable = 0
		}
		timer := time.NewTimer(poll)
		select {
		case <-drainContext.Done():
			if !timer.Stop() {
				<-timer.C
			}
			proof.StableSamples = stable
			proof.CompletedAt = time.Now().UTC()
			return proof, fmt.Errorf("drain timed out before every bound endpoint reported zero running and waiting requests for two samples: %w", drainContext.Err())
		case <-timer.C:
		}
	}
}

func loadCalibrationRequestCounts(ctx context.Context, client *http.Client, endpoint string) (float64, float64, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, 0, err
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, 0, err
	}
	defer response.Body.Close()
	contents, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return 0, 0, err
	}
	if response.StatusCode != http.StatusOK || len(contents) > maxResponseBytes {
		return 0, 0, fmt.Errorf("status %s or oversized metrics response", response.Status)
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

func executeLoadCalibrationRequest(ctx context.Context, client *http.Client, options loadTrialOptions, trialID string, sequence uint64) error {
	payload := struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		MaxTokens uint32 `json:"max_tokens"`
		Stream    bool   `json:"stream"`
	}{Model: options.Model, MaxTokens: options.MaxTokens, Stream: false}
	payload.Messages = append(payload.Messages, struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}{Role: "user", Content: backgroundLoadPrompt(trialID, sequence, options.CommonPrefix)})
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	requestContext, cancel := context.WithTimeout(ctx, options.RequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodPost, options.Endpoint, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set(protocol.HeaderRequestID, fmt.Sprintf("cal-%s-%020d", trialID, sequence))
	if strings.TrimSpace(options.Authorization) != "" {
		request.Header.Set("Authorization", strings.TrimSpace(options.Authorization))
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	contents, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return err
	}
	if len(contents) > maxResponseBytes {
		return fmt.Errorf("response exceeds %d bytes", maxResponseBytes)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("status %s", response.Status)
	}
	var completion struct {
		Choices []json.RawMessage `json:"choices"`
		Usage   json.RawMessage   `json:"usage"`
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	if err := decoder.Decode(&completion); err != nil || len(completion.Choices) == 0 || len(completion.Usage) == 0 || bytes.Equal(bytes.TrimSpace(completion.Usage), []byte("null")) {
		return fmt.Errorf("response is not a completed OpenAI-compatible response")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("response contains trailing JSON")
	}
	return nil
}

func backgroundLoadPrompt(namespace string, sequence uint64, commonPrefix string) string {
	return fmt.Sprintf("VelaServe background nonce %s-%020d. Isolated cache chain.\n%s", namespace, sequence, commonPrefix)
}

// DeriveLoadProfiles creates one saturation profile per measured shape and
// then sends the result through the same fail-closed validator used by the
// deployed condition driver.
func DeriveLoadProfiles(calibration LoadCalibration) ([]LoadProfile, error) {
	digest, err := LoadCalibrationSHA256(calibration)
	if err != nil {
		return nil, err
	}
	byShape := make(map[loadShape][]LoadCalibrationTrial)
	for _, trial := range calibration.Trials {
		shape := loadShape{prefixTokens: trial.PrefixTokens, maxTokens: trial.MaxTokens}
		byShape[shape] = append(byShape[shape], trial)
	}
	shapes := make([]loadShape, 0, len(byShape))
	for shape := range byShape {
		shapes = append(shapes, shape)
	}
	sort.Slice(shapes, func(i, j int) bool {
		if shapes[i].prefixTokens == shapes[j].prefixTokens {
			return shapes[i].maxTokens < shapes[j].maxTokens
		}
		return shapes[i].prefixTokens < shapes[j].prefixTokens
	})
	profiles := make([]LoadProfile, 0, len(shapes))
	for _, shape := range shapes {
		maximumAchieved := 0.0
		for _, trial := range byShape[shape] {
			maximumAchieved = math.Max(maximumAchieved, achievedQPS(trial))
		}
		profiles = append(profiles, LoadProfile{
			PrefixTokens: shape.prefixTokens, MaxTokens: shape.maxTokens, SaturationQPS: maximumAchieved,
			MeasurementSource: "load-calibration:" + digest, CalibrationSHA256: digest,
		})
	}
	if _, err := ValidateLoadCalibration(calibration, profiles, calibration.ModelID); err != nil {
		return nil, err
	}
	return profiles, nil
}

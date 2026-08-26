package conditiondriver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JDinSeattle/velaserve/internal/conditioncontroller"
	"github.com/JDinSeattle/velaserve/internal/evidence"
)

func TestApplyResetsEveryEndpointAndWarmsExactZ0CSourceSet(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	reset := map[string]int{}
	warmed := map[string][]string{}
	cached := map[string]bool{}
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		if strings.HasSuffix(request.URL.Path, "/metrics") {
			return idleMetricsResponse(), nil
		}
		parts := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
		if len(parts) != 2 {
			return testResponse(http.StatusNotFound, "not found"), nil
		}
		switch parts[1] {
		case "reset":
			if request.URL.Query().Get("reset_external") != "true" {
				return testResponse(http.StatusBadRequest, "external reset required"), nil
			}
			reset[parts[0]]++
			cached[parts[0]] = false
			return testResponse(http.StatusOK, `{"success":true}`), nil
		case "chat":
			var body struct {
				Messages []struct {
					Content string `json:"content"`
				} `json:"messages"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil || len(body.Messages) != 1 {
				return testResponse(http.StatusBadRequest, "bad request"), nil
			}
			warmed[parts[0]] = append(warmed[parts[0]], body.Messages[0].Content)
			wasCached := cached[parts[0]]
			cached[parts[0]] = true
			if !wasCached {
				return testResponse(http.StatusOK, `{"choices":[{}],"usage":{"prompt_tokens":4096,"prompt_tokens_details":{"cached_tokens":0}}}`), nil
			}
			return testResponse(http.StatusOK, `{"choices":[{}],"usage":{"prompt_tokens":4096,"prompt_tokens_details":{"cached_tokens":4096}}}`), nil
		default:
			return testResponse(http.StatusNotFound, "not found"), nil
		}
		return testResponse(http.StatusOK, `{"success":true}`), nil
	})

	driver, err := newTestDriver(Config{
		Model: "model", GatewayChatURL: "http://model/gateway/chat", LoadProfiles: testLoadProfiles(4096, 32, 100),
		Stabilization: 10 * time.Millisecond,
		HTTPClient:    &http.Client{Transport: transport},
		Endpoints: []Endpoint{
			testEndpoint("model-0"),
			testEndpoint("model-1"),
			testEndpoint("model-2"),
		},
		DrainPollInterval: time.Microsecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(driver.Close)

	state, err := driver.Apply(context.Background(), conditioncontroller.Request{
		SchemaVersion: conditioncontroller.RequestSchemaVersion, RunID: "run", GroupID: "group", FanoutWidth: 2,
		CommonPrefix: "the exact shared prefix", WarmupContent: "the exact shared prefix warmup", PrefixSourceCount: 2, OwnerRotation: 1,
		Cell: evidence.BenchmarkCell{PrefixTokens: 4096, MaxTokens: 32, LoadRegime: evidence.LoadIdle, CacheState: "distributed-warm"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(reset) != 3 || reset["model-0"] != 2 || reset["model-1"] != 2 || reset["model-2"] != 2 {
		t.Fatalf("reset calls = %#v", reset)
	}
	if len(warmed["model-0"]) != 1 || len(warmed["model-1"]) != 3 || len(warmed["model-2"]) != 3 {
		t.Fatalf("warm calls = %#v", warmed)
	}
	if warmed["model-1"][0] != "the exact shared prefix warmup" || warmed["model-2"][0] != "the exact shared prefix warmup" {
		t.Fatalf("warm prompts = %#v", warmed)
	}
	if strings.Join(state.ObservedState.CachedEndpointIDs, ",") != "model-1,model-2" || strings.Join(state.ObservedState.PrefixSourceEndpointIDs, ",") != "model-1,model-2" || strings.Join(state.ObservedState.OwnerEndpointOrder, ",") != "model-1,model-2,model-0" || state.OwnerRotation != 1 {
		t.Fatalf("observed cache state = %#v", state.ObservedState)
	}
}

func TestApplyMeasuresBackgroundLoadAndLeavesItRunning(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	loadCalls := 0
	loadMaxTokens := uint32(0)
	loadPrompt := ""
	cached := false
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if strings.HasSuffix(request.URL.Path, "/metrics") {
			return idleMetricsResponse(), nil
		}
		if strings.HasSuffix(request.URL.Path, "/reset") {
			mu.Lock()
			cached = false
			mu.Unlock()
			return testResponse(http.StatusOK, `{"success":true}`), nil
		}
		if request.URL.Path == "/gateway/chat" {
			var payload struct {
				Messages []struct {
					Content string `json:"content"`
				} `json:"messages"`
				MaxTokens uint32 `json:"max_tokens"`
			}
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil || len(payload.Messages) != 1 {
				return testResponse(http.StatusBadRequest, "bad load request"), nil
			}
			mu.Lock()
			loadCalls++
			loadMaxTokens = payload.MaxTokens
			loadPrompt = payload.Messages[0].Content
			mu.Unlock()
		}
		mu.Lock()
		hit := uint64(0)
		if cached {
			hit = 1
		}
		cached = true
		mu.Unlock()
		return testResponse(http.StatusOK, fmt.Sprintf(`{"choices":[{}],"usage":{"prompt_tokens":8,"prompt_tokens_details":{"cached_tokens":%d}}}`, hit)), nil
	})
	driver, err := newTestDriver(Config{
		Model: "model", GatewayChatURL: "http://model/gateway/chat", LoadProfiles: testLoadProfiles(1, 32, 1000),
		Stabilization:     20 * time.Millisecond,
		HTTPClient:        &http.Client{Transport: transport},
		Endpoints:         []Endpoint{testEndpoint("model-0")},
		DrainPollInterval: time.Microsecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(driver.Close)
	state, err := driver.Apply(context.Background(), conditioncontroller.Request{
		SchemaVersion: conditioncontroller.RequestSchemaVersion, RunID: "run", GroupID: "group", FanoutWidth: 2,
		CommonPrefix: "prefix", WarmupContent: "prefix warmup", Cell: evidence.BenchmarkCell{PrefixTokens: 1, MaxTokens: 32, LoadRegime: evidence.LoadModerate, CacheState: "warm-owner"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := evidence.ValidateConditionObservedState(state.ObservedState, evidence.LoadModerate, "warm-owner", 0); err != nil {
		t.Fatalf("invalid measured state: %v (%#v)", err, state.ObservedState)
	}
	mu.Lock()
	before := loadCalls
	observedMaxTokens := loadMaxTokens
	observedPrompt := loadPrompt
	mu.Unlock()
	if observedMaxTokens != 32 || !strings.HasSuffix(observedPrompt, "\nprefix warmup") || strings.HasPrefix(observedPrompt, "prefix") {
		t.Fatalf("background profile mismatch: max_tokens=%d prompt=%q", observedMaxTokens, observedPrompt)
	}
	if len(state.ObservedState.LoadProfileSHA256) != 64 || len(state.ObservedState.LoadProfilesSHA256) != 64 {
		t.Fatalf("load profile hashes were not attested: %#v", state.ObservedState)
	}
	time.Sleep(10 * time.Millisecond)
	mu.Lock()
	after := loadCalls
	mu.Unlock()
	if before == 0 || after <= before {
		t.Fatalf("background load did not continue: before=%d after=%d", before, after)
	}
	finalized, err := driver.Finalize(context.Background(), conditioncontroller.FinalizeRequest{SchemaVersion: conditioncontroller.FinalizeRequestSchemaVersion, RunID: "run", GroupID: "group"})
	if err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	if finalized.FinalizedAt == nil || finalized.ObservedState.DroppedLoadRequests != 0 {
		t.Fatalf("finalized state = %#v", finalized)
	}
	mu.Lock()
	stoppedAt := loadCalls
	mu.Unlock()
	time.Sleep(5 * time.Millisecond)
	mu.Lock()
	stoppedAfter := loadCalls
	mu.Unlock()
	if stoppedAfter != stoppedAt {
		t.Fatalf("background load continued after finalization: %d -> %d", stoppedAt, stoppedAfter)
	}
}

func TestApplyFailsClosedWhenResetFails(t *testing.T) {
	t.Parallel()
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if strings.HasSuffix(request.URL.Path, "/metrics") {
			return idleMetricsResponse(), nil
		}
		return testResponse(http.StatusInternalServerError, "no"), nil
	})
	driver, err := newTestDriver(Config{
		Model: "model", GatewayChatURL: "http://model/chat", LoadProfiles: testLoadProfiles(1, 32, 100), Stabilization: time.Millisecond,
		HTTPClient:        &http.Client{Transport: transport},
		Endpoints:         []Endpoint{{ID: "model-0", ChatURL: "http://model/chat", ResetURL: "http://model/reset", MetricsURL: "http://model/metrics"}},
		DrainPollInterval: time.Microsecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(driver.Close)
	_, err = driver.Apply(context.Background(), conditioncontroller.Request{SchemaVersion: conditioncontroller.RequestSchemaVersion, RunID: "run", GroupID: "group", CommonPrefix: "prefix", WarmupContent: "prefix warmup", Cell: evidence.BenchmarkCell{PrefixTokens: 1, MaxTokens: 32, LoadRegime: evidence.LoadIdle, CacheState: "cold"}})
	if err == nil || !strings.Contains(err.Error(), "reset") {
		t.Fatalf("Apply() error = %v", err)
	}
}

func TestApplyRejectsResetSuccessFalse(t *testing.T) {
	t.Parallel()
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if strings.HasSuffix(request.URL.Path, "/metrics") {
			return idleMetricsResponse(), nil
		}
		return testResponse(http.StatusOK, `{"success":false}`), nil
	})
	driver, err := newTestDriver(Config{
		Model: "model", GatewayChatURL: "http://model/chat", LoadProfiles: testLoadProfiles(1, 32, 100), Stabilization: time.Millisecond,
		HTTPClient: &http.Client{Transport: transport}, Endpoints: []Endpoint{testEndpoint("model-0")}, DrainPollInterval: time.Microsecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(driver.Close)
	_, err = driver.Apply(context.Background(), conditioncontroller.Request{SchemaVersion: conditioncontroller.RequestSchemaVersion, RunID: "run", GroupID: "group", CommonPrefix: "prefix", WarmupContent: "prefix warmup", Cell: evidence.BenchmarkCell{PrefixTokens: 1, MaxTokens: 32, LoadRegime: evidence.LoadIdle, CacheState: "cold"}})
	if err == nil || !strings.Contains(err.Error(), "did not confirm") {
		t.Fatalf("Apply() error = %v", err)
	}
}

func TestApplyWaitsForTwoStableIdleMetricSamples(t *testing.T) {
	t.Parallel()
	metricCalls := 0
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if strings.HasSuffix(request.URL.Path, "/metrics") {
			metricCalls++
			if metricCalls == 1 {
				return testResponse(http.StatusOK, "vllm:num_requests_running 1\nvllm:num_requests_waiting 0\n"), nil
			}
			return idleMetricsResponse(), nil
		}
		if strings.HasSuffix(request.URL.Path, "/reset") {
			return testResponse(http.StatusOK, `{"success":true}`), nil
		}
		return testResponse(http.StatusOK, `{"choices":[{}],"usage":{"prompt_tokens":8,"prompt_tokens_details":{"cached_tokens":0}}}`), nil
	})
	driver, err := newTestDriver(Config{
		Model: "model", GatewayChatURL: "http://model/chat", LoadProfiles: testLoadProfiles(1, 32, 100), Stabilization: time.Millisecond,
		HTTPClient: &http.Client{Transport: transport}, Endpoints: []Endpoint{testEndpoint("model-0")}, DrainPollInterval: time.Microsecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(driver.Close)
	state, err := driver.Apply(context.Background(), conditioncontroller.Request{SchemaVersion: conditioncontroller.RequestSchemaVersion, RunID: "run", GroupID: "group", CommonPrefix: "prefix", WarmupContent: "prefix warmup", Cell: evidence.BenchmarkCell{PrefixTokens: 1, MaxTokens: 32, LoadRegime: evidence.LoadIdle, CacheState: "cold"}})
	if err != nil {
		t.Fatal(err)
	}
	if metricCalls != 3 || state.ObservedState.DrainStableSamples != 2 || strings.Join(state.ObservedState.DrainedEndpointIDs, ",") != "model-0" {
		t.Fatalf("drain observation = calls=%d state=%#v", metricCalls, state.ObservedState)
	}
}

func TestApplyRejectsColdProbeWithoutCachedTokenReadback(t *testing.T) {
	t.Parallel()
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if strings.HasSuffix(request.URL.Path, "/metrics") {
			return idleMetricsResponse(), nil
		}
		if strings.HasSuffix(request.URL.Path, "/reset") {
			return testResponse(http.StatusOK, `{"success":true}`), nil
		}
		return testResponse(http.StatusOK, `{"choices":[{}],"usage":{"prompt_tokens":4096,"prompt_tokens_details":null}}`), nil
	})
	driver, err := newTestDriver(Config{
		Model: "model", GatewayChatURL: "http://model/gateway/chat", LoadProfiles: testLoadProfiles(4096, 32, 100), Stabilization: time.Millisecond,
		HTTPClient:        &http.Client{Transport: transport},
		Endpoints:         []Endpoint{testEndpoint("model-0")},
		DrainPollInterval: time.Microsecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(driver.Close)
	_, err = driver.Apply(context.Background(), conditioncontroller.Request{
		SchemaVersion: conditioncontroller.RequestSchemaVersion, RunID: "run", GroupID: "group", CommonPrefix: "prefix", WarmupContent: "prefix warmup",
		Cell: evidence.BenchmarkCell{PrefixTokens: 4096, MaxTokens: 32, LoadRegime: evidence.LoadIdle, CacheState: "cold"},
	})
	if err == nil || !strings.Contains(err.Error(), "cached_tokens") {
		t.Fatalf("Apply() error = %v, want missing cold cached-token readback rejection", err)
	}
}

func TestApplyRejectsWarmOwnerWithoutCachedTokenReadback(t *testing.T) {
	t.Parallel()
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if strings.HasSuffix(request.URL.Path, "/metrics") {
			return idleMetricsResponse(), nil
		}
		if strings.HasSuffix(request.URL.Path, "/reset") {
			return testResponse(http.StatusOK, `{"success":true}`), nil
		}
		return testResponse(http.StatusOK, `{"choices":[{}],"usage":{"prompt_tokens":4096,"prompt_tokens_details":{"cached_tokens":0}}}`), nil
	})
	driver, err := newTestDriver(Config{
		Model: "model", GatewayChatURL: "http://model/gateway/chat", LoadProfiles: testLoadProfiles(4096, 32, 100), Stabilization: time.Millisecond,
		HTTPClient:        &http.Client{Transport: transport},
		Endpoints:         []Endpoint{testEndpoint("model-0")},
		DrainPollInterval: time.Microsecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(driver.Close)
	_, err = driver.Apply(context.Background(), conditioncontroller.Request{
		SchemaVersion: conditioncontroller.RequestSchemaVersion, RunID: "run", GroupID: "group", CommonPrefix: "prefix", WarmupContent: "prefix warmup",
		Cell: evidence.BenchmarkCell{PrefixTokens: 4096, MaxTokens: 32, LoadRegime: evidence.LoadIdle, CacheState: "warm-owner"},
	})
	if err == nil || !strings.Contains(err.Error(), "cached-token readback") {
		t.Fatalf("Apply() error = %v, want cached-token readback rejection", err)
	}
}

func TestApplyRejectsPartialPrefixCacheReadback(t *testing.T) {
	t.Parallel()
	cached := false
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if strings.HasSuffix(request.URL.Path, "/metrics") {
			return idleMetricsResponse(), nil
		}
		if strings.HasSuffix(request.URL.Path, "/reset") {
			cached = false
			return testResponse(http.StatusOK, `{"success":true}`), nil
		}
		hit := uint64(0)
		if cached {
			hit = 16
		}
		cached = true
		return testResponse(http.StatusOK, fmt.Sprintf(`{"choices":[{}],"usage":{"prompt_tokens":4096,"prompt_tokens_details":{"cached_tokens":%d}}}`, hit)), nil
	})
	driver, err := newTestDriver(Config{
		Model: "model", GatewayChatURL: "http://model/gateway/chat", LoadProfiles: testLoadProfiles(4096, 32, 100), Stabilization: time.Millisecond,
		HTTPClient:        &http.Client{Transport: transport},
		Endpoints:         []Endpoint{testEndpoint("model-0")},
		DrainPollInterval: time.Microsecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(driver.Close)
	_, err = driver.Apply(context.Background(), conditioncontroller.Request{
		SchemaVersion: conditioncontroller.RequestSchemaVersion, RunID: "run", GroupID: "group", CommonPrefix: "prefix", WarmupContent: "prefix warmup",
		Cell: evidence.BenchmarkCell{PrefixTokens: 4096, MaxTokens: 32, LoadRegime: evidence.LoadIdle, CacheState: "warm-owner"},
	})
	if err == nil || !strings.Contains(err.Error(), "need at least 4096") {
		t.Fatalf("Apply() error = %v, want full-prefix cache rejection", err)
	}
}

func TestApplyRejectsOwnerEvictedDuringLoadStabilization(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	cached := false
	loadObserved := false
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		if strings.HasSuffix(request.URL.Path, "/metrics") {
			return idleMetricsResponse(), nil
		}
		if strings.HasSuffix(request.URL.Path, "/reset") {
			cached = false
			return testResponse(http.StatusOK, `{"success":true}`), nil
		}
		if request.URL.Path == "/gateway/chat" {
			loadObserved = true
			return testResponse(http.StatusOK, `{"choices":[{}]}`), nil
		}
		hit := uint64(0)
		if cached && !loadObserved {
			hit = 1
		}
		cached = true
		return testResponse(http.StatusOK, fmt.Sprintf(`{"choices":[{}],"usage":{"prompt_tokens":8,"prompt_tokens_details":{"cached_tokens":%d}}}`, hit)), nil
	})
	driver, err := newTestDriver(Config{
		Model: "model", GatewayChatURL: "http://model/gateway/chat", LoadProfiles: testLoadProfiles(1, 32, 1000), Stabilization: 20 * time.Millisecond,
		HTTPClient:        &http.Client{Transport: transport},
		Endpoints:         []Endpoint{testEndpoint("model-0")},
		DrainPollInterval: time.Microsecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(driver.Close)
	_, err = driver.Apply(context.Background(), conditioncontroller.Request{
		SchemaVersion: conditioncontroller.RequestSchemaVersion, RunID: "run", GroupID: "group", CommonPrefix: "prefix", WarmupContent: "prefix warmup",
		Cell: evidence.BenchmarkCell{PrefixTokens: 1, MaxTokens: 32, LoadRegime: evidence.LoadModerate, CacheState: "warm-owner"},
	})
	if err == nil || !strings.Contains(err.Error(), "post-load warm endpoint") {
		t.Fatalf("Apply() error = %v, want post-load eviction rejection", err)
	}
}

func TestApplyRejectsCellWithoutExactMeasuredLoadProfile(t *testing.T) {
	driver, err := newTestDriver(Config{
		Model: "model", GatewayChatURL: "http://model/gateway/chat", LoadProfiles: testLoadProfiles(1024, 32, 100),
		HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			if strings.HasSuffix(request.URL.Path, "/metrics") {
				return idleMetricsResponse(), nil
			}
			if strings.HasSuffix(request.URL.Path, "/reset") {
				return testResponse(http.StatusOK, `{"success":true}`), nil
			}
			return testResponse(http.StatusOK, `{"choices":[{}],"usage":{"prompt_tokens":4096,"prompt_tokens_details":{"cached_tokens":0}}}`), nil
		})},
		Endpoints:         []Endpoint{testEndpoint("model-0")},
		DrainPollInterval: time.Microsecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(driver.Close)
	_, err = driver.Apply(context.Background(), conditioncontroller.Request{
		SchemaVersion: conditioncontroller.RequestSchemaVersion, RunID: "run", GroupID: "group", CommonPrefix: "prefix", WarmupContent: "prefix warmup",
		Cell: evidence.BenchmarkCell{PrefixTokens: 4096, MaxTokens: 128, LoadRegime: evidence.LoadIdle, CacheState: "cold"},
	})
	if err == nil || !strings.Contains(err.Error(), "no measured load profile") {
		t.Fatalf("Apply() error = %v", err)
	}
}

func TestLoadProfilesSHA256IsOrderIndependent(t *testing.T) {
	profiles := append(testLoadProfiles(4096, 128, 80), testLoadProfiles(1024, 32, 120)...)
	left, err := LoadProfilesSHA256(profiles)
	if err != nil {
		t.Fatal(err)
	}
	profiles[0], profiles[1] = profiles[1], profiles[0]
	right, err := LoadProfilesSHA256(profiles)
	if err != nil {
		t.Fatal(err)
	}
	if left != right || len(left) != 64 {
		t.Fatalf("profile digests = %q and %q", left, right)
	}
}

func testLoadProfiles(prefixTokens uint64, maxTokens uint32, saturationQPS float64) []LoadProfile {
	return []LoadProfile{{
		PrefixTokens: prefixTokens, MaxTokens: maxTokens, SaturationQPS: saturationQPS,
		MeasurementSource: "fixture-saturation-run", CalibrationSHA256: strings.Repeat("a", 64),
	}}
}

func newTestDriver(config Config) (*Driver, error) {
	calibration := LoadCalibration{
		SchemaVersion: LoadCalibrationSchemaVersion, ModelID: config.Model,
		ModelRevision: strings.Repeat("a", 40), ModelImage: "example.invalid/model@sha256:" + strings.Repeat("b", 64),
		InstanceType: "fixture.gpu", GPUModel: "fixture-gpu", GPUDriverVersion: "fixture-driver",
		ReplicaCount: 2, GPUsPerReplica: 1, Transport: "tcp", ProfileCalibrationSHA256: strings.Repeat("c", 64),
		ActiveArm: evidence.ArmLoadAwareP2P, RoutingSHA256: strings.Repeat("d", 64), RouterConfigSHA256: strings.Repeat("e", 64),
		GatewayChatURL: config.GatewayChatURL,
		MeasuredAt:     time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC), Endpoints: testLoadCalibrationEndpoints(2),
	}
	for profileIndex, profile := range config.LoadProfiles {
		for trialIndex, fraction := range []float64{0.20, 0.50, 1.00, 1.10, 1.20} {
			offered := uint64(profile.SaturationQPS * fraction * 100)
			successful := offered
			if successful > uint64(profile.SaturationQPS*100) {
				successful = uint64(profile.SaturationQPS * 100)
			}
			trial := LoadCalibrationTrial{
				TrialID: fmt.Sprintf("shape-%d-trial-%d", profileIndex, trialIndex), PrefixTokens: profile.PrefixTokens, MaxTokens: profile.MaxTokens,
				WarmupDurationSeconds: 5, WarmupOfferedRequests: 5, WarmupSuccessfulRequests: 5,
				DurationSeconds: 100, OfferedRequests: offered, SuccessfulRequests: successful, FailedRequests: offered - successful, MeanLatencySeconds: .01,
			}
			populateLoadCalibrationTrialProofs(&trial, calibration.Endpoints, calibration.MeasuredAt.Add(time.Duration(profileIndex*10+trialIndex)*time.Minute))
			calibration.Trials = append(calibration.Trials, trial)
		}
	}
	digest, err := LoadCalibrationSHA256(calibration)
	if err != nil {
		return nil, err
	}
	for index := range config.LoadProfiles {
		config.LoadProfiles[index].CalibrationSHA256 = digest
		config.LoadProfiles[index].MeasurementSource = "load-calibration:" + digest
	}
	config.LoadCalibration = calibration
	return New(config)
}

func testEndpoint(id string) Endpoint {
	return Endpoint{
		ID:         id,
		ChatURL:    "http://model/" + id + "/chat",
		ResetURL:   "http://model/" + id + "/reset",
		MetricsURL: "http://model/" + id + "/metrics",
	}
}

func idleMetricsResponse() *http.Response {
	return testResponse(http.StatusOK, "vllm:num_requests_running 0\nvllm:num_requests_waiting 0\n")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func testResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

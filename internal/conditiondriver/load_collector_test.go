package conditiondriver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JDinSeattle/velaserve/internal/bench"
	"github.com/JDinSeattle/velaserve/internal/evidence"
)

func TestRunLoadCalibrationTrialProducesMeasuredRequestCounts(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	prompts := make([]string, 0)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer request.Body.Close()
		var payload struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			MaxTokens uint32 `json:"max_tokens"`
			Stream    bool   `json:"stream"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		if payload.Model != "model" || payload.MaxTokens != 8 || payload.Stream || len(payload.Messages) != 1 || payload.Messages[0].Role != "user" {
			t.Errorf("unexpected calibration request: %+v", payload)
		}
		mu.Lock()
		prompts = append(prompts, payload.Messages[0].Content)
		mu.Unlock()
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":64,"completion_tokens":1}}`))
	}))
	t.Cleanup(server.Close)

	trial, err := runLoadCalibrationTrial(context.Background(), loadTrialOptions{
		Endpoint: server.URL, Model: "model", CommonPrefix: "shared prefix ", PrefixTokens: 64, MaxTokens: 8,
		RateQPS: 20, Duration: 120 * time.Millisecond, RequestTimeout: time.Second, MaxInflight: 8, HTTPClient: server.Client(),
	}, "trial-001")
	if err != nil {
		t.Fatal(err)
	}
	if trial.OfferedRequests < 2 || trial.SuccessfulRequests != trial.OfferedRequests || trial.FailedRequests != 0 || trial.DroppedArrivals != 0 || trial.MeanLatencySeconds <= 0 {
		t.Fatalf("unexpected trial: %+v", trial)
	}
	mu.Lock()
	defer mu.Unlock()
	if uint64(len(prompts)) != trial.SuccessfulRequests {
		t.Fatalf("saw %d prompts for %d successes", len(prompts), trial.SuccessfulRequests)
	}
	seen := make(map[string]struct{}, len(prompts))
	for _, prompt := range prompts {
		if !strings.HasPrefix(prompt, "VelaServe background nonce ") || !strings.HasSuffix(prompt, "\nshared prefix ") {
			t.Fatalf("prompt does not use the production background-load generator: %q", prompt)
		}
		if _, exists := seen[prompt]; exists {
			t.Fatalf("duplicate prompt %q", prompt)
		}
		seen[prompt] = struct{}{}
	}
}

func TestCalibrationUsesExactProductionWarmupContent(t *testing.T) {
	t.Parallel()
	prefix := bench.PrefixProfile{RepeatText: "shared ", RepeatCount: 2}
	want := "shared shared " + bench.CalibrationWarmupSuffix
	if got := calibrationWorkloadContent(prefix); got != want {
		t.Fatalf("calibration content %q, want production warmup %q", got, want)
	}
}

func TestRunLoadCalibrationTrialDoesNotOfferAfterCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	trial, err := runLoadCalibrationTrial(ctx, loadTrialOptions{
		Endpoint: "http://127.0.0.1:1/v1/chat/completions", Model: "model", CommonPrefix: "prefix", PrefixTokens: 64,
		MaxTokens: 8, RateQPS: 1_000_000, Duration: time.Second, RequestTimeout: time.Second, MaxInflight: 1,
	}, "cancelled")
	if err == nil || trial.OfferedRequests != 0 || trial.SuccessfulRequests != 0 || trial.FailedRequests != 0 || trial.DroppedArrivals != 0 {
		t.Fatalf("cancelled trial launched work: trial=%+v err=%v", trial, err)
	}
}

func TestRunLoadCalibrationTrialRetainsLateCompletionOutsideMeasurementWindow(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		time.Sleep(40 * time.Millisecond)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{}],"usage":{"prompt_tokens":64,"completion_tokens":1}}`))
	}))
	t.Cleanup(server.Close)
	trial, err := runLoadCalibrationTrial(context.Background(), loadTrialOptions{
		Endpoint: server.URL, Model: "model", CommonPrefix: "prefix", PrefixTokens: 64, MaxTokens: 8,
		RateQPS: 100, Duration: 10 * time.Millisecond, RequestTimeout: time.Second, MaxInflight: 2, HTTPClient: server.Client(),
	}, "late")
	if err != nil {
		t.Fatal(err)
	}
	if trial.OfferedRequests != 1 || trial.SuccessfulRequests != 0 || trial.LateSuccessfulRequests != 1 || trial.MeasurementCompletedAt.Before(trial.MeasurementSchedulingEndedAt) {
		t.Fatalf("late completion was not separated from measured goodput: %+v", trial)
	}
}

func TestLoadCalibrationDrainRetainsEveryExactReplicaSample(t *testing.T) {
	t.Parallel()
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls++
		running := 0
		if calls == 1 {
			running = 1
		}
		fmt.Fprintf(writer, "vllm:num_requests_running %d\nvllm:num_requests_waiting 0\n", running)
	}))
	t.Cleanup(server.Close)
	endpoint := LoadCalibrationEndpoint{ID: "model-0", PodUID: "uid-0", MetricsURL: server.URL}
	proof, err := waitForLoadCalibrationDrain(context.Background(), []LoadCalibrationEndpoint{endpoint}, server.Client(), time.Second, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if proof.StableSamples != 2 || len(proof.Samples) != 3 || proof.Samples[0].Endpoints[0].Running != 1 || proof.Samples[1].Endpoints[0].PodUID != endpoint.PodUID {
		t.Fatalf("drain proof = %+v", proof)
	}
}

func TestDeriveLoadProfilesBindsRawCalibrationDigest(t *testing.T) {
	t.Parallel()
	calibration := validCollectedLoadCalibration()
	profiles, err := DeriveLoadProfiles(calibration)
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 1 {
		t.Fatalf("got %d profiles", len(profiles))
	}
	digest, err := LoadCalibrationSHA256(calibration)
	if err != nil {
		t.Fatal(err)
	}
	if profiles[0].SaturationQPS != float64(100)/30 || profiles[0].CalibrationSHA256 != digest || profiles[0].MeasurementSource != "load-calibration:"+digest {
		t.Fatalf("profile is not exactly derived and bound: %+v", profiles[0])
	}
	if _, err := ValidateLoadCalibration(calibration, profiles, calibration.ModelID); err != nil {
		t.Fatalf("derived profiles must validate: %v", err)
	}
}

func TestDeriveLoadProfilesRejectsRampWithoutOverload(t *testing.T) {
	t.Parallel()
	calibration := validCollectedLoadCalibration()
	for index := range calibration.Trials {
		calibration.Trials[index].SuccessfulRequests = calibration.Trials[index].OfferedRequests
		calibration.Trials[index].FailedRequests = 0
	}
	if _, err := DeriveLoadProfiles(calibration); err == nil || !strings.Contains(err.Error(), "overload") {
		t.Fatalf("expected overload error, got %v", err)
	}
}

func validCollectedLoadCalibration() LoadCalibration {
	calibration := LoadCalibration{
		SchemaVersion: LoadCalibrationSchemaVersion, ModelID: "model", ModelRevision: strings.Repeat("a", 40),
		ModelImage: "registry/model@sha256:" + strings.Repeat("b", 64), InstanceType: "g6e.xlarge", GPUModel: "NVIDIA L40S",
		GPUDriverVersion: "580.82.07", ReplicaCount: 6, GPUsPerReplica: 1, Transport: "tcp",
		ActiveArm: evidence.ArmLoadAwareP2P, RoutingSHA256: strings.Repeat("d", 64), RouterConfigSHA256: strings.Repeat("e", 64),
		ProfileCalibrationSHA256: strings.Repeat("c", 64), GatewayChatURL: "https://gateway.example/v1/chat/completions", MeasuredAt: time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC),
		Endpoints: testLoadCalibrationEndpoints(6),
	}
	offered := []uint64{30, 60, 90, 120, 300}
	successful := []uint64{30, 60, 90, 100, 95}
	for index := range offered {
		trial := LoadCalibrationTrial{
			TrialID: "trial-00" + string(rune('1'+index)), PrefixTokens: 64, MaxTokens: 8, DurationSeconds: 30,
			WarmupDurationSeconds: 5, WarmupOfferedRequests: 5, WarmupSuccessfulRequests: 5,
			OfferedRequests: offered[index], SuccessfulRequests: successful[index], FailedRequests: offered[index] - successful[index], MeanLatencySeconds: .1,
		}
		populateLoadCalibrationTrialProofs(&trial, calibration.Endpoints, calibration.MeasuredAt.Add(time.Duration(index)*time.Minute))
		calibration.Trials = append(calibration.Trials, trial)
	}
	return calibration
}

func testLoadCalibrationEndpoints(count int) []LoadCalibrationEndpoint {
	endpoints := make([]LoadCalibrationEndpoint, 0, count)
	for index := 0; index < count; index++ {
		endpoints = append(endpoints, LoadCalibrationEndpoint{ID: fmt.Sprintf("model-%d", index), PodUID: fmt.Sprintf("pod-uid-%d", index), MetricsURL: fmt.Sprintf("http://model-%d.example.invalid:8200/metrics", index)})
	}
	return endpoints
}

func populateLoadCalibrationTrialProofs(trial *LoadCalibrationTrial, endpoints []LoadCalibrationEndpoint, startedAt time.Time) {
	trial.PreDrain = testLoadCalibrationDrainProof(endpoints, startedAt)
	trial.WarmupStartedAt = trial.PreDrain.CompletedAt.Add(time.Millisecond)
	trial.WarmupCompletedAt = trial.WarmupStartedAt.Add(time.Duration(trial.WarmupDurationSeconds * float64(time.Second)))
	trial.WarmupDrain = testLoadCalibrationDrainProof(endpoints, trial.WarmupCompletedAt.Add(time.Millisecond))
	trial.MeasurementStartedAt = trial.WarmupDrain.CompletedAt.Add(time.Millisecond)
	trial.MeasurementSchedulingEndedAt = trial.MeasurementStartedAt.Add(time.Duration(trial.DurationSeconds * float64(time.Second)))
	trial.MeasurementCompletedAt = trial.MeasurementSchedulingEndedAt.Add(time.Second)
	trial.PostDrain = testLoadCalibrationDrainProof(endpoints, trial.MeasurementCompletedAt.Add(time.Millisecond))
}

func testLoadCalibrationDrainProof(endpoints []LoadCalibrationEndpoint, startedAt time.Time) LoadCalibrationDrainProof {
	proof := LoadCalibrationDrainProof{StartedAt: startedAt.UTC(), StableSamples: 2}
	for sampleIndex := 0; sampleIndex < 2; sampleIndex++ {
		sample := LoadCalibrationDrainSample{ObservedAt: startedAt.Add(time.Duration(sampleIndex+1) * time.Millisecond).UTC()}
		for _, endpoint := range endpoints {
			sample.Endpoints = append(sample.Endpoints, LoadCalibrationDrainEndpointObservation{ID: endpoint.ID, PodUID: endpoint.PodUID})
		}
		proof.Samples = append(proof.Samples, sample)
	}
	proof.CompletedAt = proof.Samples[len(proof.Samples)-1].ObservedAt.Add(time.Millisecond)
	return proof
}

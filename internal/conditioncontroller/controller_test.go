package conditioncontroller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JDinSeattle/velaserve/internal/evidence"
)

type fakeDriver struct{ state AppliedState }

const testControlToken = "test-control-token"

func (driver fakeDriver) Apply(context.Context, Request) (AppliedState, error) {
	return driver.state, nil
}

func (driver fakeDriver) Finalize(context.Context, FinalizeRequest) (AppliedState, error) {
	return driver.state, nil
}

func TestControllerRejectsDriverStateThatDoesNotMatchRequestedCell(t *testing.T) {
	controller := Controller{Revision: "git-deadbeef", ControlToken: testControlToken, Driver: fakeDriver{state: AppliedState{SchemaVersion: "velaserve.applied-condition/v1", LoadRegime: evidence.LoadIdle, CacheState: "cold", ObservedState: observedState(0, 0)}}}
	request := httptest.NewRequest(http.MethodPost, "/v1/conditions/apply", strings.NewReader(`{"schema_version":"velaserve.condition-request/v2","run_id":"run","group_id":"group","arm":"arm-b-load-aware-p2p","fanout_width":2,"common_prefix":"shared prefix ","warmup_content":"shared prefix warmup","cell":{"repetition":1,"prefix_regime":"near","prefix_tokens":4096,"output_regime":"short","max_tokens":32,"arrival_skew_ms":0,"load_regime":"moderate","cache_state":"warm-owner","transport":"tcp","epp_replicas":1}}`))
	request.Header.Set("Authorization", "Bearer "+testControlToken)
	response := httptest.NewRecorder()
	controller.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", response.Code)
	}
}

func TestControllerProducesHashBoundAttestation(t *testing.T) {
	appliedAt := time.Date(2026, 8, 26, 1, 0, 0, 0, time.UTC)
	state := observedState(50, 50)
	state.CachedEndpointIDs = []string{"model-0", "model-1"}
	state.PrefixSourceEndpointIDs = []string{"model-0", "model-1"}
	controller := Controller{Revision: "git-deadbeef", ControlToken: testControlToken, Now: func() time.Time { return appliedAt }, Driver: fakeDriver{state: AppliedState{SchemaVersion: "velaserve.applied-condition/v1", LoadRegime: evidence.LoadModerate, CacheState: "distributed-warm", PrefixSourceCount: 2, ObservedState: state}}}
	request := httptest.NewRequest(http.MethodPost, "/v1/conditions/apply", strings.NewReader(`{"schema_version":"velaserve.condition-request/v2","run_id":"run","group_id":"group","arm":"arm-b-load-aware-p2p","fanout_width":2,"common_prefix":"shared prefix ","warmup_content":"shared prefix warmup","prefix_source_count":2,"cell":{"repetition":1,"prefix_regime":"near","prefix_tokens":4096,"output_regime":"short","max_tokens":32,"arrival_skew_ms":0,"load_regime":"moderate","cache_state":"distributed-warm","transport":"tcp","epp_replicas":1}}`))
	request.Header.Set("Authorization", "Bearer "+testControlToken)
	response := httptest.NewRecorder()
	controller.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	var attestation evidence.ConditionAttestation
	if err := json.Unmarshal(response.Body.Bytes(), &attestation); err != nil {
		t.Fatal(err)
	}
	if attestation.StateSHA256 == "" || len(attestation.ObservedState.PrefixSourceEndpointIDs) != 2 || attestation.PrefixSourceCount != 2 || attestation.AppliedAt != appliedAt {
		t.Fatalf("attestation = %#v", attestation)
	}
}

func TestControllerRejectsMislabeledMeasuredLoad(t *testing.T) {
	state := observedState(90, 90)
	state.CachedEndpointIDs = []string{"model-0"}
	controller := Controller{Revision: "git-deadbeef", ControlToken: testControlToken, Driver: fakeDriver{state: AppliedState{SchemaVersion: "velaserve.applied-condition/v1", LoadRegime: evidence.LoadModerate, CacheState: "warm-owner", ObservedState: state}}}
	request := httptest.NewRequest(http.MethodPost, "/v1/conditions/apply", strings.NewReader(`{"schema_version":"velaserve.condition-request/v2","run_id":"run","group_id":"group","arm":"arm-b-load-aware-p2p","fanout_width":2,"common_prefix":"shared prefix ","warmup_content":"shared prefix warmup","cell":{"repetition":1,"prefix_regime":"near","prefix_tokens":4096,"output_regime":"short","max_tokens":32,"arrival_skew_ms":0,"load_regime":"moderate","cache_state":"warm-owner","transport":"tcp","epp_replicas":1}}`))
	request.Header.Set("Authorization", "Bearer "+testControlToken)
	response := httptest.NewRecorder()
	controller.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", response.Code)
	}
}

func TestControllerRejectsUnauthenticatedMutation(t *testing.T) {
	controller := Controller{Revision: "git-deadbeef", ControlToken: testControlToken, Driver: fakeDriver{}}
	request := httptest.NewRequest(http.MethodPost, "/v1/conditions/apply", strings.NewReader(`{}`))
	response := httptest.NewRecorder()
	controller.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.Code)
	}
}

func TestHTTPDriverDefaultTimeoutCoversSequentialFleetTransition(t *testing.T) {
	if got := (HTTPDriver{}).httpClient().Timeout; got != DefaultApplyTimeout || got <= 2*time.Minute {
		t.Fatalf("default apply timeout = %s, want %s and greater than two minutes", got, DefaultApplyTimeout)
	}
	configured := HTTPDriver{Timeout: 11 * time.Minute}
	if got := configured.httpClient().Timeout; got != 11*time.Minute {
		t.Fatalf("configured apply timeout = %s, want 11m", got)
	}
}

func observedState(offered, achieved float64) evidence.ConditionObservedState {
	latency := 0.0
	if offered > 0 {
		latency = 0.01
	}
	return evidence.ConditionObservedState{
		OfferedLoadQPS: offered, AchievedLoadQPS: achieved, SaturationQPS: 100,
		LoadProfileSHA256: strings.Repeat("1", 64), LoadProfilesSHA256: strings.Repeat("2", 64), LoadCalibrationSHA256: strings.Repeat("3", 64), MeasurementSource: "load-calibration:" + strings.Repeat("3", 64),
		DrainedEndpointIDs: []string{"model-0", "model-1"}, DrainStableSamples: 2,
		OwnerEndpointOrder:                []string{"model-0", "model-1"},
		OrdinaryTrafficMeanLatencySeconds: latency,
	}
}

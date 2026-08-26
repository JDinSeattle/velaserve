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

func (driver fakeDriver) Apply(context.Context, Request) (AppliedState, error) {
	return driver.state, nil
}

func TestControllerRejectsDriverStateThatDoesNotMatchRequestedCell(t *testing.T) {
	controller := Controller{Revision: "git-deadbeef", Driver: fakeDriver{state: AppliedState{SchemaVersion: "velaserve.applied-condition/v1", LoadRegime: evidence.LoadIdle, CacheState: "cold", ObservedState: evidence.ConditionObservedState{SaturationQPS: 100, MeasurementSource: "driver"}}}}
	request := httptest.NewRequest(http.MethodPost, "/v1/conditions/apply", strings.NewReader(`{"schema_version":"velaserve.condition-request/v1","run_id":"run","group_id":"group","arm":"arm-b-load-aware-p2p","fanout_width":2,"cell":{"repetition":1,"prefix_regime":"near","prefix_tokens":4096,"output_regime":"short","max_tokens":32,"arrival_skew_ms":0,"load_regime":"moderate","cache_state":"warm-owner","transport":"tcp","epp_replicas":1}}`))
	response := httptest.NewRecorder()
	controller.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", response.Code)
	}
}

func TestControllerProducesHashBoundAttestation(t *testing.T) {
	appliedAt := time.Date(2026, 8, 26, 1, 0, 0, 0, time.UTC)
	controller := Controller{Revision: "git-deadbeef", Now: func() time.Time { return appliedAt }, Driver: fakeDriver{state: AppliedState{SchemaVersion: "velaserve.applied-condition/v1", LoadRegime: evidence.LoadModerate, CacheState: "distributed-warm", PrefixSourceCount: 2, ObservedState: evidence.ConditionObservedState{OfferedLoadQPS: 50, AchievedLoadQPS: 50, SaturationQPS: 100, CachedEndpointIDs: []string{"model-0", "model-1"}, PrefixSourceEndpointIDs: []string{"model-0", "model-1"}, MeasurementSource: "driver"}}}}
	request := httptest.NewRequest(http.MethodPost, "/v1/conditions/apply", strings.NewReader(`{"schema_version":"velaserve.condition-request/v1","run_id":"run","group_id":"group","arm":"arm-b-load-aware-p2p","fanout_width":2,"prefix_source_count":2,"cell":{"repetition":1,"prefix_regime":"near","prefix_tokens":4096,"output_regime":"short","max_tokens":32,"arrival_skew_ms":0,"load_regime":"moderate","cache_state":"distributed-warm","transport":"tcp","epp_replicas":1}}`))
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
	controller := Controller{Revision: "git-deadbeef", Driver: fakeDriver{state: AppliedState{SchemaVersion: "velaserve.applied-condition/v1", LoadRegime: evidence.LoadModerate, CacheState: "warm-owner", ObservedState: evidence.ConditionObservedState{OfferedLoadQPS: 90, AchievedLoadQPS: 90, SaturationQPS: 100, CachedEndpointIDs: []string{"model-0"}, MeasurementSource: "driver"}}}}
	request := httptest.NewRequest(http.MethodPost, "/v1/conditions/apply", strings.NewReader(`{"schema_version":"velaserve.condition-request/v1","run_id":"run","group_id":"group","arm":"arm-b-load-aware-p2p","fanout_width":2,"cell":{"repetition":1,"prefix_regime":"near","prefix_tokens":4096,"output_regime":"short","max_tokens":32,"arrival_skew_ms":0,"load_regime":"moderate","cache_state":"warm-owner","transport":"tcp","epp_replicas":1}}`))
	response := httptest.NewRecorder()
	controller.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", response.Code)
	}
}

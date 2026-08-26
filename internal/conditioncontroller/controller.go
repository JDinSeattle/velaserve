package conditioncontroller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/JDinSeattle/velaserve/internal/evidence"
)

const RequestSchemaVersion = "velaserve.condition-request/v1"

type Request struct {
	SchemaVersion     string                 `json:"schema_version"`
	RunID             string                 `json:"run_id"`
	GroupID           string                 `json:"group_id"`
	Arm               evidence.Arm           `json:"arm"`
	FanoutWidth       uint32                 `json:"fanout_width"`
	Cell              evidence.BenchmarkCell `json:"cell"`
	PrefixSourceCount uint32                 `json:"prefix_source_count,omitempty"`
}

type AppliedState struct {
	SchemaVersion     string                          `json:"schema_version"`
	LoadRegime        evidence.LoadRegime             `json:"load_regime"`
	CacheState        string                          `json:"cache_state"`
	PrefixSourceCount uint32                          `json:"prefix_source_count,omitempty"`
	ObservedState     evidence.ConditionObservedState `json:"observed_state"`
}

type Driver interface {
	Apply(context.Context, Request) (AppliedState, error)
}

type Controller struct {
	Driver   Driver
	Revision string
	Now      func() time.Time
}

func (controller Controller) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost || request.URL.Path != "/v1/conditions/apply" {
		http.NotFound(writer, request)
		return
	}
	if controller.Driver == nil || strings.TrimSpace(controller.Revision) == "" {
		http.Error(writer, "controller is not configured", http.StatusServiceUnavailable)
		return
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	var desired Request
	if err := decoder.Decode(&desired); err != nil {
		http.Error(writer, "invalid condition request", http.StatusBadRequest)
		return
	}
	if err := validateRequest(desired); err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	state, err := controller.Driver.Apply(request.Context(), desired)
	if err != nil {
		http.Error(writer, "condition driver failed", http.StatusBadGateway)
		return
	}
	if state.SchemaVersion != "velaserve.applied-condition/v1" || state.LoadRegime != desired.Cell.LoadRegime || state.CacheState != desired.Cell.CacheState || state.PrefixSourceCount != desired.PrefixSourceCount || evidence.ValidateConditionObservedState(state.ObservedState, state.LoadRegime, state.CacheState, state.PrefixSourceCount) != nil {
		http.Error(writer, "condition driver did not attest the requested state", http.StatusConflict)
		return
	}
	canonical, err := json.Marshal(state)
	if err != nil {
		http.Error(writer, "cannot encode applied state", http.StatusInternalServerError)
		return
	}
	digest := sha256.Sum256(canonical)
	now := time.Now().UTC()
	if controller.Now != nil {
		now = controller.Now().UTC()
	}
	attestation := evidence.ConditionAttestation{
		SchemaVersion: evidence.ConditionAttestationSchemaVersion, RunID: desired.RunID, GroupID: desired.GroupID,
		LoadRegime: state.LoadRegime, CacheState: state.CacheState, PrefixSourceCount: state.PrefixSourceCount,
		ControllerRevision: controller.Revision, StateSHA256: hex.EncodeToString(digest[:]), ObservedState: state.ObservedState, AppliedAt: now,
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(attestation)
}

type HTTPDriver struct {
	Endpoint string
	Client   *http.Client
}

func (driver HTTPDriver) Apply(ctx context.Context, request Request) (AppliedState, error) {
	if !strings.HasPrefix(driver.Endpoint, "http://") && !strings.HasPrefix(driver.Endpoint, "https://") {
		return AppliedState{}, fmt.Errorf("driver endpoint must be HTTP(S)")
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return AppliedState{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, driver.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return AppliedState{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	client := driver.Client
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	response, err := client.Do(httpRequest)
	if err != nil {
		return AppliedState{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return AppliedState{}, fmt.Errorf("driver status %s", response.Status)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	decoder.DisallowUnknownFields()
	var state AppliedState
	if err := decoder.Decode(&state); err != nil {
		return AppliedState{}, err
	}
	return state, nil
}

func validateRequest(request Request) error {
	if request.SchemaVersion != RequestSchemaVersion || strings.TrimSpace(request.RunID) == "" || strings.TrimSpace(request.GroupID) == "" {
		return fmt.Errorf("condition request schema and identity are required")
	}
	if request.PrefixSourceCount != 0 && request.PrefixSourceCount != 1 && request.PrefixSourceCount != 2 && request.PrefixSourceCount != 4 {
		return fmt.Errorf("unregistered prefix source count")
	}
	return nil
}

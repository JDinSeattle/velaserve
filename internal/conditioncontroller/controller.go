package conditioncontroller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/JDinSeattle/velaserve/internal/evidence"
)

const RequestSchemaVersion = "velaserve.condition-request/v2"
const FinalizeRequestSchemaVersion = "velaserve.condition-finalize-request/v1"

const DefaultApplyTimeout = 15 * time.Minute

type Request struct {
	SchemaVersion     string                 `json:"schema_version"`
	RunID             string                 `json:"run_id"`
	GroupID           string                 `json:"group_id"`
	Arm               evidence.Arm           `json:"arm"`
	FanoutWidth       uint32                 `json:"fanout_width"`
	Cell              evidence.BenchmarkCell `json:"cell"`
	CommonPrefix      string                 `json:"common_prefix"`
	WarmupContent     string                 `json:"warmup_content"`
	PrefixSourceCount uint32                 `json:"prefix_source_count,omitempty"`
	OwnerRotation     uint32                 `json:"owner_rotation"`
}

type AppliedState struct {
	SchemaVersion     string                          `json:"schema_version"`
	LoadRegime        evidence.LoadRegime             `json:"load_regime"`
	CacheState        string                          `json:"cache_state"`
	PrefixSourceCount uint32                          `json:"prefix_source_count,omitempty"`
	OwnerRotation     uint32                          `json:"owner_rotation"`
	ObservedState     evidence.ConditionObservedState `json:"observed_state"`
	AppliedAt         time.Time                       `json:"applied_at"`
	FinalizedAt       *time.Time                      `json:"finalized_at,omitempty"`
}

type FinalizeRequest struct {
	SchemaVersion string `json:"schema_version"`
	RunID         string `json:"run_id"`
	GroupID       string `json:"group_id"`
}

type Driver interface {
	Apply(context.Context, Request) (AppliedState, error)
	Finalize(context.Context, FinalizeRequest) (AppliedState, error)
}

type Controller struct {
	Driver       Driver
	Revision     string
	ControlToken string
	Now          func() time.Time
}

func (controller Controller) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost || (request.URL.Path != "/v1/conditions/apply" && request.URL.Path != "/v1/conditions/finalize") {
		http.NotFound(writer, request)
		return
	}
	if controller.Driver == nil || strings.TrimSpace(controller.Revision) == "" || strings.TrimSpace(controller.ControlToken) == "" {
		http.Error(writer, "controller is not configured", http.StatusServiceUnavailable)
		return
	}
	if !validBearer(request.Header.Get("Authorization"), controller.ControlToken) {
		writer.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	if request.URL.Path == "/v1/conditions/finalize" {
		controller.finalize(writer, request)
		return
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	var desired Request
	if err := decoder.Decode(&desired); err != nil {
		http.Error(writer, "invalid condition request", http.StatusBadRequest)
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		http.Error(writer, "condition request must contain exactly one JSON object", http.StatusBadRequest)
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
	if state.SchemaVersion != "velaserve.applied-condition/v1" || state.LoadRegime != desired.Cell.LoadRegime || state.CacheState != desired.Cell.CacheState || state.PrefixSourceCount != desired.PrefixSourceCount || state.OwnerRotation != desired.OwnerRotation || evidence.ValidateConditionObservedState(state.ObservedState, state.LoadRegime, state.CacheState, state.PrefixSourceCount) != nil {
		http.Error(writer, "condition driver did not attest the requested state", http.StatusConflict)
		return
	}
	canonical, err := json.Marshal(canonicalState(state))
	if err != nil {
		http.Error(writer, "cannot encode applied state", http.StatusInternalServerError)
		return
	}
	digest := sha256.Sum256(canonical)
	now := state.AppliedAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
		if controller.Now != nil {
			now = controller.Now().UTC()
		}
	}
	attestation := evidence.ConditionAttestation{
		SchemaVersion: evidence.ConditionAttestationSchemaVersion, RunID: desired.RunID, GroupID: desired.GroupID,
		LoadRegime: state.LoadRegime, CacheState: state.CacheState, PrefixSourceCount: state.PrefixSourceCount,
		OwnerRotation: state.OwnerRotation, ControllerRevision: controller.Revision, StateSHA256: hex.EncodeToString(digest[:]), ObservedState: state.ObservedState, AppliedAt: now,
		FinalizedAt: state.FinalizedAt,
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(attestation)
}

func (controller Controller) finalize(writer http.ResponseWriter, request *http.Request) {
	decoder := json.NewDecoder(io.LimitReader(request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	var desired FinalizeRequest
	if err := decoder.Decode(&desired); err != nil || desired.SchemaVersion != FinalizeRequestSchemaVersion || strings.TrimSpace(desired.RunID) == "" || strings.TrimSpace(desired.GroupID) == "" {
		http.Error(writer, "invalid condition finalization request", http.StatusBadRequest)
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		http.Error(writer, "condition finalization request must contain exactly one JSON object", http.StatusBadRequest)
		return
	}
	state, err := controller.Driver.Finalize(request.Context(), desired)
	if err != nil {
		http.Error(writer, "condition driver failed to finalize", http.StatusBadGateway)
		return
	}
	if state.FinalizedAt == nil || state.AppliedAt.IsZero() || evidence.ValidateConditionObservedState(state.ObservedState, state.LoadRegime, state.CacheState, state.PrefixSourceCount) != nil {
		http.Error(writer, "condition driver did not finalize a valid measured state", http.StatusConflict)
		return
	}
	canonical, err := json.Marshal(canonicalState(state))
	if err != nil {
		http.Error(writer, "cannot encode finalized state", http.StatusInternalServerError)
		return
	}
	digest := sha256.Sum256(canonical)
	attestation := evidence.ConditionAttestation{
		SchemaVersion: evidence.ConditionAttestationSchemaVersion, RunID: desired.RunID, GroupID: desired.GroupID,
		LoadRegime: state.LoadRegime, CacheState: state.CacheState, PrefixSourceCount: state.PrefixSourceCount, OwnerRotation: state.OwnerRotation,
		ControllerRevision: controller.Revision, StateSHA256: hex.EncodeToString(digest[:]), ObservedState: state.ObservedState, AppliedAt: state.AppliedAt.UTC(), FinalizedAt: state.FinalizedAt,
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(attestation)
}

func canonicalState(state AppliedState) any {
	return struct {
		SchemaVersion     string                          `json:"schema_version"`
		LoadRegime        evidence.LoadRegime             `json:"load_regime"`
		CacheState        string                          `json:"cache_state"`
		PrefixSourceCount uint32                          `json:"prefix_source_count,omitempty"`
		OwnerRotation     uint32                          `json:"owner_rotation"`
		ObservedState     evidence.ConditionObservedState `json:"observed_state"`
	}{state.SchemaVersion, state.LoadRegime, state.CacheState, state.PrefixSourceCount, state.OwnerRotation, state.ObservedState}
}

type HTTPDriver struct {
	Endpoint string
	Client   *http.Client
	Token    string
	Timeout  time.Duration
}

func (driver HTTPDriver) Apply(ctx context.Context, request Request) (AppliedState, error) {
	return driver.post(ctx, driver.Endpoint, request)
}

func (driver HTTPDriver) Finalize(ctx context.Context, request FinalizeRequest) (AppliedState, error) {
	if !strings.HasSuffix(driver.Endpoint, "/v1/conditions/apply") {
		return AppliedState{}, fmt.Errorf("driver apply endpoint path is invalid")
	}
	endpoint := strings.TrimSuffix(driver.Endpoint, "/apply") + "/finalize"
	return driver.post(ctx, endpoint, request)
}

func (driver HTTPDriver) post(ctx context.Context, endpoint string, payloadValue any) (AppliedState, error) {
	if (!strings.HasPrefix(driver.Endpoint, "http://") && !strings.HasPrefix(driver.Endpoint, "https://")) || strings.TrimSpace(driver.Token) == "" {
		return AppliedState{}, fmt.Errorf("driver endpoint must be HTTP(S)")
	}
	payload, err := json.Marshal(payloadValue)
	if err != nil {
		return AppliedState{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return AppliedState{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Authorization", "Bearer "+driver.Token)
	client := driver.httpClient()
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

func (driver HTTPDriver) httpClient() *http.Client {
	if driver.Client != nil {
		return driver.Client
	}
	timeout := driver.Timeout
	if timeout == 0 {
		timeout = DefaultApplyTimeout
	}
	return &http.Client{Timeout: timeout}
}

func validBearer(header, token string) bool {
	expected := "Bearer " + token
	return subtle.ConstantTimeCompare([]byte(header), []byte(expected)) == 1
}

func validateRequest(request Request) error {
	if request.SchemaVersion != RequestSchemaVersion || strings.TrimSpace(request.RunID) == "" || strings.TrimSpace(request.GroupID) == "" {
		return fmt.Errorf("condition request schema and identity are required")
	}
	if strings.TrimSpace(request.CommonPrefix) == "" || strings.TrimSpace(request.WarmupContent) == "" || !strings.HasPrefix(request.WarmupContent, request.CommonPrefix) {
		return fmt.Errorf("common prefix and its exact warmup content are required")
	}
	if request.PrefixSourceCount != 0 && request.PrefixSourceCount != 1 && request.PrefixSourceCount != 2 && request.PrefixSourceCount != 4 {
		return fmt.Errorf("unregistered prefix source count")
	}
	return nil
}

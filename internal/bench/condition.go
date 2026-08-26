package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/JDinSeattle/velaserve/internal/evidence"
)

const conditionRequestSchemaVersion = "velaserve.condition-request/v2"

type conditionRequest struct {
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

type conditionFinalizeRequest struct {
	SchemaVersion string `json:"schema_version"`
	RunID         string `json:"run_id"`
	GroupID       string `json:"group_id"`
}

func (client Client) applyCondition(ctx context.Context, groupID string, request GroupRequest) (evidence.ConditionAttestation, error) {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(client.ConditionControllerEndpoint))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return evidence.ConditionAttestation{}, fmt.Errorf("condition controller endpoint must be an absolute HTTP(S) URL without user information")
	}
	if strings.TrimSpace(client.ConditionControlToken) == "" {
		return evidence.ConditionAttestation{}, fmt.Errorf("condition control token is required")
	}
	payload, err := json.Marshal(conditionRequest{
		SchemaVersion: conditionRequestSchemaVersion, RunID: request.RunID, GroupID: groupID, Arm: request.Arm,
		FanoutWidth: uint32(len(request.Suffixes)), Cell: request.Cell, CommonPrefix: request.CommonPrefix, WarmupContent: request.WarmupContent, PrefixSourceCount: request.PrefixSourceCount, OwnerRotation: request.OwnerRotation,
	})
	if err != nil {
		return evidence.ConditionAttestation{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, client.ConditionControllerEndpoint, bytes.NewReader(payload))
	if err != nil {
		return evidence.ConditionAttestation{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Authorization", "Bearer "+client.ConditionControlToken)
	response, err := client.httpClient().Do(httpRequest)
	if err != nil {
		return evidence.ConditionAttestation{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBodyBytes))
		return evidence.ConditionAttestation{}, fmt.Errorf("controller status %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxErrorBodyBytes+1))
	decoder.DisallowUnknownFields()
	var attestation evidence.ConditionAttestation
	if err := decoder.Decode(&attestation); err != nil {
		return evidence.ConditionAttestation{}, fmt.Errorf("decode condition attestation: %w", err)
	}
	if attestation.RunID != request.RunID || attestation.GroupID != groupID || attestation.LoadRegime != request.Cell.LoadRegime || attestation.CacheState != request.Cell.CacheState || attestation.PrefixSourceCount != request.PrefixSourceCount || attestation.OwnerRotation != request.OwnerRotation {
		return evidence.ConditionAttestation{}, fmt.Errorf("controller attestation does not match requested workload condition")
	}
	return attestation, nil
}

func (client Client) finalizeCondition(ctx context.Context, groupID string, request GroupRequest) (evidence.ConditionAttestation, error) {
	if !strings.HasSuffix(client.ConditionControllerEndpoint, "/v1/conditions/apply") {
		return evidence.ConditionAttestation{}, fmt.Errorf("condition controller apply endpoint path is invalid")
	}
	endpoint := strings.TrimSuffix(client.ConditionControllerEndpoint, "/v1/conditions/apply") + "/v1/conditions/finalize"
	payload, err := json.Marshal(conditionFinalizeRequest{SchemaVersion: "velaserve.condition-finalize-request/v1", RunID: request.RunID, GroupID: groupID})
	if err != nil {
		return evidence.ConditionAttestation{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return evidence.ConditionAttestation{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Authorization", "Bearer "+client.ConditionControlToken)
	response, err := client.httpClient().Do(httpRequest)
	if err != nil {
		return evidence.ConditionAttestation{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBodyBytes))
		return evidence.ConditionAttestation{}, fmt.Errorf("controller finalize status %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxErrorBodyBytes+1))
	decoder.DisallowUnknownFields()
	var attestation evidence.ConditionAttestation
	if err := decoder.Decode(&attestation); err != nil {
		return evidence.ConditionAttestation{}, fmt.Errorf("decode finalized condition attestation: %w", err)
	}
	if attestation.RunID != request.RunID || attestation.GroupID != groupID || attestation.LoadRegime != request.Cell.LoadRegime || attestation.CacheState != request.Cell.CacheState || attestation.PrefixSourceCount != request.PrefixSourceCount || attestation.OwnerRotation != request.OwnerRotation || attestation.FinalizedAt == nil {
		return evidence.ConditionAttestation{}, fmt.Errorf("controller finalized attestation does not match requested workload condition")
	}
	return attestation, nil
}

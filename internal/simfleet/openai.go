package simfleet

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/JDinSeattle/velaserve/internal/bench"
	"github.com/JDinSeattle/velaserve/internal/evidence"
	"github.com/JDinSeattle/velaserve/internal/fanout/protocol"
	placementrecorder "github.com/JDinSeattle/velaserve/research/placement-recorder"
)

const maxSimulatorRequestBytes = 8 << 20

type simulatorRequest struct {
	Model    string `json:"model"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
	MaxTokens     uint32 `json:"max_tokens"`
	Stream        bool   `json:"stream"`
	StreamOptions struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
}

func (fleet *Fleet) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	startedAt := time.Now().UTC()
	responseCode := http.StatusOK
	requestID := request.Header.Get(protocol.HeaderRequestID)
	groupID := request.Header.Get(protocol.HeaderGroup)
	targetID := "unassigned"
	defer func() {
		fleet.recordEnvoy(startedAt, requestID, groupID, targetID, responseCode)
	}()
	if request.Method != http.MethodPost || request.URL.Path != "/v1/chat/completions" {
		responseCode = http.StatusNotFound
		http.Error(writer, "simfleet exposes POST /v1/chat/completions", responseCode)
		return
	}
	width, err := parseUint32Header(request, protocol.HeaderWidth)
	if err != nil {
		responseCode = http.StatusBadRequest
		http.Error(writer, err.Error(), responseCode)
		return
	}
	if request.Header.Get(protocol.HeaderVersion) != "1" || requestID == "" || groupID == "" {
		responseCode = http.StatusBadRequest
		http.Error(writer, "missing versioned fan-out identity", responseCode)
		return
	}
	slot, err := parseUint32Header(request, bench.HeaderSimulatorSlot)
	if err != nil || slot >= width {
		responseCode = http.StatusBadRequest
		http.Error(writer, "invalid simulator slot", responseCode)
		return
	}
	skewMS, err := parseUint32Header(request, bench.HeaderSimulatorArrivalSkewMS)
	if err != nil {
		responseCode = http.StatusBadRequest
		http.Error(writer, "invalid simulator arrival skew", responseCode)
		return
	}
	arm := evidence.Arm(request.Header.Get(bench.HeaderSimulatorArm))
	input := GroupInput{Arm: arm, Width: width, ArrivalSkew: time.Duration(skewMS) * time.Millisecond}
	vector, err := fleet.planForGroup(groupID, input)
	if err != nil {
		responseCode = http.StatusBadRequest
		http.Error(writer, err.Error(), responseCode)
		return
	}
	targetID = vector[slot]
	target, ok := fleet.endpointByID(targetID)
	if !ok {
		responseCode = http.StatusInternalServerError
		http.Error(writer, "planned target is absent", responseCode)
		return
	}

	decoder := json.NewDecoder(io.LimitReader(request.Body, maxSimulatorRequestBytes+1))
	decoder.DisallowUnknownFields()
	var body simulatorRequest
	if err := decoder.Decode(&body); err != nil {
		responseCode = http.StatusBadRequest
		http.Error(writer, "invalid OpenAI request: "+err.Error(), responseCode)
		return
	}
	if body.Model != fleet.config.Model || !body.Stream || !body.StreamOptions.IncludeUsage || body.MaxTokens == 0 || len(body.Messages) == 0 {
		responseCode = http.StatusBadRequest
		http.Error(writer, "unsupported OpenAI request", responseCode)
		return
	}
	fleet.recordEPP(startedAt, requestID, groupID, target)
	writer.Header().Set(bench.HeaderSimulatorTarget, targetID)
	if configured, fail := fleet.config.FailureSlots[slot]; fail {
		responseCode = configured
		http.Error(writer, "configured simulator failure", responseCode)
		return
	}

	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.WriteHeader(http.StatusOK)
	ttft := fleet.simulatedTTFT(arm, targetID)
	if !waitFor(request, ttft) {
		responseCode = 499
		return
	}
	fmt.Fprintf(writer, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n")
	flush(writer)
	fmt.Fprintf(writer, "data: {\"choices\":[{\"delta\":{\"content\":\"simulated candidate %d\"}}]}\n\n", slot+1)
	flush(writer)
	if !waitFor(request, fleet.config.OutputDelay) {
		responseCode = 499
		return
	}
	fmt.Fprintf(writer, "data: {\"choices\":[],\"usage\":{\"completion_tokens\":%d}}\n\n", minUint32(body.MaxTokens, 8))
	fmt.Fprint(writer, "data: [DONE]\n\n")
	flush(writer)
}

func (fleet *Fleet) planForGroup(groupID string, input GroupInput) ([]string, error) {
	fleet.mu.Lock()
	defer fleet.mu.Unlock()
	if existing, ok := fleet.plans[groupID]; ok {
		if len(existing) != int(input.Width) {
			return nil, fmt.Errorf("group width changed after first sibling")
		}
		return append([]string(nil), existing...), nil
	}
	vector, err := fleet.PlacementVector(input)
	if err != nil {
		return nil, err
	}
	fleet.plans[groupID] = append([]string(nil), vector...)
	return vector, nil
}

func (fleet *Fleet) endpointByID(id string) (evidence.EndpointRef, bool) {
	for _, endpoint := range fleet.endpoints {
		if endpoint.ref.ID == id {
			return endpoint.ref, true
		}
	}
	return evidence.EndpointRef{}, false
}

func (fleet *Fleet) simulatedTTFT(arm evidence.Arm, targetID string) time.Duration {
	if targetID == fleet.endpoints[fleet.config.WarmOwner].ref.ID {
		return fleet.config.LocalTTFT
	}
	if arm == evidence.ArmLoadAwareNoP2P {
		return fleet.config.RecomputeTTFT
	}
	return fleet.config.PullTTFT
}

func (fleet *Fleet) recordEPP(observedAt time.Time, requestID, groupID string, target evidence.EndpointRef) {
	snapshot := fleet.Snapshot(observedAt)
	fleet.mu.Lock()
	defer fleet.mu.Unlock()
	fleet.eppRecords = append(fleet.eppRecords, placementrecorder.EPPRecord{
		SchemaVersion: placementrecorder.EPPSchedulingSchemaVersion,
		RequestID:     requestID,
		GroupID:       groupID,
		ObservedAt:    observedAt,
		Snapshot:      snapshot,
		Target:        target,
	})
}

func (fleet *Fleet) recordEnvoy(startedAt time.Time, requestID, groupID, target string, responseCode int) {
	digest := sha256.Sum256([]byte(requestID))
	fleet.mu.Lock()
	defer fleet.mu.Unlock()
	fleet.envoyRecords = append(fleet.envoyRecords, placementrecorder.EnvoyRecord{
		StartedAt:    startedAt,
		RequestID:    requestID,
		GroupID:      groupID,
		UpstreamHost: target,
		ResponseCode: uint32(responseCode),
		DurationMS:   uint64(maxDuration(time.Since(startedAt), 0) / time.Millisecond),
		TraceID:      hex.EncodeToString(digest[:16]),
		SpanID:       hex.EncodeToString(digest[16:24]),
	})
}

func parseUint32Header(request *http.Request, name string) (uint32, error) {
	value := strings.TrimSpace(request.Header.Get(name))
	parsed, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("%s is invalid", name)
	}
	return uint32(parsed), nil
}

func waitFor(request *http.Request, duration time.Duration) bool {
	if duration <= 0 {
		return true
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-request.Context().Done():
		return false
	case <-timer.C:
		return true
	}
}

func flush(writer http.ResponseWriter) {
	if flusher, ok := writer.(http.Flusher); ok {
		flusher.Flush()
	}
}

func minUint32(left, right uint32) uint32 {
	if left < right {
		return left
	}
	return right
}

func maxDuration(left, right time.Duration) time.Duration {
	if left > right {
		return left
	}
	return right
}

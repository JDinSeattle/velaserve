package crossover

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/JDinSeattle/velaserve/internal/bench"
)

const (
	BindingSchemaVersion     = "velaserve.crossover-calibration-binding/v1"
	ObservationSchemaVersion = "velaserve.crossover-observation/v1"
	maxBindingBytes          = 4 << 20
)

var digestImagePattern = regexp.MustCompile(`^.+@sha256:[0-9a-f]{64}$`)

type Endpoint struct {
	ID                string `json:"id"`
	PodUID            string `json:"pod_uid"`
	EngineChatURL     string `json:"engine_chat_url"`
	ProxyChatURL      string `json:"proxy_chat_url"`
	ResetURL          string `json:"reset_url"`
	TokenizeURL       string `json:"tokenize_url"`
	TokenizerInfoURL  string `json:"tokenizer_info_url"`
	P2PSourceHostPort string `json:"p2p_source_host_port"`
}

type Binding struct {
	SchemaVersion string                           `json:"schema_version"`
	ModelID       string                           `json:"model_id"`
	ModelRevision string                           `json:"model_revision"`
	Transport     string                           `json:"transport"`
	Deployment    bench.CrossoverDeploymentBinding `json:"deployment"`
	Endpoints     []Endpoint                       `json:"endpoints"`
	ObservedAt    time.Time                        `json:"observed_at"`
}

type Observation struct {
	SchemaVersion string    `json:"schema_version"`
	RunID         string    `json:"run_id"`
	RequestID     string    `json:"request_id"`
	PrefixTokens  uint64    `json:"prefix_tokens"`
	Prompt        string    `json:"prompt"`
	Acquisition   string    `json:"acquisition"`
	TargetID      string    `json:"target_id"`
	TargetPodUID  string    `json:"target_pod_uid"`
	SourceID      string    `json:"source_id,omitempty"`
	SourcePodUID  string    `json:"source_pod_uid,omitempty"`
	PromptTokens  uint64    `json:"prompt_tokens"`
	CachedTokens  uint64    `json:"cached_tokens"`
	DispatchedAt  time.Time `json:"dispatched_at"`
	FirstTokenAt  time.Time `json:"first_token_at"`
	CompletedAt   time.Time `json:"completed_at"`
	TTFTSeconds   float64   `json:"ttft_seconds"`
}

func LoadBinding(path string) (Binding, []byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return Binding{}, nil, err
	}
	if !info.Mode().IsRegular() {
		return Binding{}, nil, fmt.Errorf("crossover calibration binding must be a regular file")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return Binding{}, nil, err
	}
	if len(contents) > maxBindingBytes {
		return Binding{}, nil, fmt.Errorf("crossover calibration binding exceeds %d bytes", maxBindingBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	var binding Binding
	if err := decoder.Decode(&binding); err != nil {
		return Binding{}, nil, fmt.Errorf("decode crossover calibration binding: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Binding{}, nil, fmt.Errorf("crossover calibration binding must contain exactly one JSON object")
	}
	if err := ValidateBinding(binding); err != nil {
		return Binding{}, nil, err
	}
	return binding, contents, nil
}

func ValidateBinding(binding Binding) error {
	if binding.SchemaVersion != BindingSchemaVersion || strings.TrimSpace(binding.ModelID) == "" || !lowerHex(binding.ModelRevision, 40, 64) || binding.Transport != "tcp" || binding.ObservedAt.IsZero() {
		return fmt.Errorf("crossover calibration binding identity is incomplete")
	}
	deployment := binding.Deployment
	if !lowerHex(deployment.RepositoryCommit, 40) || !digestImagePattern.MatchString(deployment.ModelImage) || !lowerHex(deployment.ModelSpecSHA256, 64) || strings.TrimSpace(deployment.ActiveArm) == "" || !digestImagePattern.MatchString(deployment.EPPImage) || deployment.EPPReplicas == 0 || deployment.ReplicaCount < 2 || strings.TrimSpace(deployment.GPUModel) == "" || strings.TrimSpace(deployment.GPUDriverVersion) == "" || strings.TrimSpace(deployment.InstanceType) == "" || !lowerHex(deployment.RouterConfigInvariantSHA256, 64) {
		return fmt.Errorf("crossover calibration deployment identity is incomplete")
	}
	if len(binding.Endpoints) != int(deployment.ReplicaCount) {
		return fmt.Errorf("crossover calibration endpoints must bind every model replica")
	}
	seenIDs := make(map[string]struct{}, len(binding.Endpoints))
	seenUIDs := make(map[string]struct{}, len(binding.Endpoints))
	for index, endpoint := range binding.Endpoints {
		if strings.TrimSpace(endpoint.ID) == "" || strings.TrimSpace(endpoint.PodUID) == "" || !validURL(endpoint.EngineChatURL) || !validURL(endpoint.ProxyChatURL) || !validURL(endpoint.ResetURL) || !validURL(endpoint.TokenizeURL) || !validURL(endpoint.TokenizerInfoURL) || strings.TrimSpace(endpoint.P2PSourceHostPort) == "" {
			return fmt.Errorf("crossover calibration endpoint %d is incomplete", index+1)
		}
		if _, exists := seenIDs[endpoint.ID]; exists {
			return fmt.Errorf("crossover calibration endpoint ID %q is duplicated", endpoint.ID)
		}
		if _, exists := seenUIDs[endpoint.PodUID]; exists {
			return fmt.Errorf("crossover calibration pod UID %q is duplicated", endpoint.PodUID)
		}
		seenIDs[endpoint.ID] = struct{}{}
		seenUIDs[endpoint.PodUID] = struct{}{}
	}
	return nil
}

func ValidateObservation(observation Observation, binding Binding) error {
	if observation.SchemaVersion != ObservationSchemaVersion || strings.TrimSpace(observation.RunID) == "" || strings.TrimSpace(observation.RequestID) == "" || observation.PrefixTokens == 0 || strings.TrimSpace(observation.Prompt) == "" || observation.PromptTokens < observation.PrefixTokens || observation.CachedTokens > observation.PromptTokens || observation.DispatchedAt.IsZero() || observation.FirstTokenAt.Before(observation.DispatchedAt) || observation.CompletedAt.Before(observation.FirstTokenAt) || observation.TTFTSeconds <= 0 {
		return fmt.Errorf("crossover observation identity, counts, or chronology is invalid")
	}
	if difference := observation.FirstTokenAt.Sub(observation.DispatchedAt).Seconds() - observation.TTFTSeconds; difference < -1e-6 || difference > 1e-6 {
		return fmt.Errorf("crossover observation TTFT does not match timestamps")
	}
	endpointByID := make(map[string]Endpoint, len(binding.Endpoints))
	for _, endpoint := range binding.Endpoints {
		endpointByID[endpoint.ID] = endpoint
	}
	target, exists := endpointByID[observation.TargetID]
	if !exists || target.PodUID != observation.TargetPodUID {
		return fmt.Errorf("crossover observation target is not an exact bound pod")
	}
	switch observation.Acquisition {
	case "recompute":
		if observation.SourceID != "" || observation.SourcePodUID != "" || observation.CachedTokens >= observation.PrefixTokens {
			return fmt.Errorf("recompute observation unexpectedly reports a source or full cached prefix")
		}
	case "p2p":
		source, exists := endpointByID[observation.SourceID]
		if !exists || source.PodUID != observation.SourcePodUID || source.ID == target.ID || observation.CachedTokens < observation.PrefixTokens {
			return fmt.Errorf("P2P observation lacks an exact distinct source or cached-prefix readback")
		}
	default:
		return fmt.Errorf("crossover observation acquisition %q is unsupported", observation.Acquisition)
	}
	return nil
}

func SortEndpoints(endpoints []Endpoint) {
	sort.Slice(endpoints, func(i, j int) bool { return endpoints[i].ID < endpoints[j].ID })
}

func validURL(raw string) bool {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(raw))
	return err == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.User == nil
}

func lowerHex(value string, lengths ...int) bool {
	validLength := false
	for _, length := range lengths {
		if len(value) == length {
			validLength = true
		}
	}
	return validLength && strings.Trim(value, "0123456789abcdef") == ""
}

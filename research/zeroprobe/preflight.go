package zeroprobe

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/JDinSeattle/velaserve/internal/bench"
	"github.com/JDinSeattle/velaserve/internal/evidence"
)

const PreflightBindingSchemaVersion = "velaserve.preflight-binding/v1"

type PodBinding struct {
	Name         string `json:"name"`
	UID          string `json:"uid"`
	Image        string `json:"image"`
	RestartCount uint32 `json:"restart_count"`
}

type NodeBinding struct {
	Name           string `json:"name"`
	UID            string `json:"uid"`
	InstanceType   string `json:"instance_type"`
	GPUAllocatable uint32 `json:"gpu_allocatable"`
}

type ModelDeploymentBinding struct {
	Selector        string       `json:"selector"`
	Container       string       `json:"container"`
	Workload        string       `json:"workload"`
	ID              string       `json:"id"`
	Revision        string       `json:"revision"`
	Image           string       `json:"image"`
	MinimumReplicas uint32       `json:"minimum_replicas"`
	MaximumReplicas uint32       `json:"maximum_replicas"`
	Pods            []PodBinding `json:"pods"`
}

type EPPDeploymentBinding struct {
	Selector  string       `json:"selector"`
	Container string       `json:"container"`
	Image     string       `json:"image"`
	Replicas  uint32       `json:"replicas"`
	Pods      []PodBinding `json:"pods"`
}

type ControllerDeploymentBinding struct {
	Selector  string       `json:"selector"`
	Container string       `json:"container"`
	Image     string       `json:"image"`
	Revision  string       `json:"revision"`
	Pods      []PodBinding `json:"pods"`
}

type PreflightBinding struct {
	SchemaVersion         string                      `json:"schema_version"`
	RepositoryCommit      string                      `json:"repository_commit"`
	AWSAccountID          string                      `json:"aws_account_id"`
	AWSRegion             string                      `json:"aws_region"`
	ClusterARN            string                      `json:"cluster_arn"`
	Namespace             string                      `json:"namespace"`
	Endpoint              string                      `json:"endpoint"`
	GatewayUID            string                      `json:"gateway_uid"`
	HTTPRouteUID          string                      `json:"http_route_uid"`
	PreregistrationSHA256 string                      `json:"preregistration_sha256"`
	CalibrationSHA256     string                      `json:"calibration_sha256"`
	ActiveArm             evidence.Arm                `json:"active_arm"`
	Transport             string                      `json:"transport"`
	Model                 ModelDeploymentBinding      `json:"model"`
	EPP                   EPPDeploymentBinding        `json:"epp"`
	Controller            ControllerDeploymentBinding `json:"condition_controller"`
	Nodes                 []NodeBinding               `json:"nodes"`
	ObservedAt            time.Time                   `json:"observed_at"`
}

func LoadPreflightBinding(path string) (PreflightBinding, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return PreflightBinding{}, err
	}
	if len(contents) > maxPreregistrationSize {
		return PreflightBinding{}, fmt.Errorf("preflight binding exceeds %d bytes", maxPreregistrationSize)
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	var binding PreflightBinding
	if err := decoder.Decode(&binding); err != nil {
		return PreflightBinding{}, fmt.Errorf("decode preflight binding: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return PreflightBinding{}, fmt.Errorf("decode preflight binding: trailing JSON content")
	}
	if err := binding.Validate(); err != nil {
		return PreflightBinding{}, err
	}
	return binding, nil
}

func (binding PreflightBinding) Marshal() ([]byte, error) {
	if err := binding.Validate(); err != nil {
		return nil, err
	}
	encoded, err := json.MarshalIndent(binding, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func (binding PreflightBinding) Validate() error {
	if binding.SchemaVersion != PreflightBindingSchemaVersion {
		return fmt.Errorf("preflight binding schema is unsupported")
	}
	if !isLowerHex(binding.RepositoryCommit, 40) || binding.Controller.Revision != binding.RepositoryCommit {
		return fmt.Errorf("repository and condition-controller revisions must be the same exact Git commit")
	}
	if len(binding.AWSAccountID) != 12 || strings.Trim(binding.AWSAccountID, "0123456789") != "" || strings.TrimSpace(binding.AWSRegion) == "" || strings.TrimSpace(binding.ClusterARN) == "" || strings.TrimSpace(binding.Namespace) == "" {
		return fmt.Errorf("AWS account, region, cluster ARN, and namespace are required")
	}
	if !strings.HasPrefix(binding.ClusterARN, "arn:aws:eks:"+binding.AWSRegion+":"+binding.AWSAccountID+":cluster/") {
		return fmt.Errorf("cluster ARN does not match the bound AWS account and region")
	}
	endpoint, err := url.ParseRequestURI(binding.Endpoint)
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.User != nil || anyBlank(binding.GatewayUID, binding.HTTPRouteUID) {
		return fmt.Errorf("Envoy Gateway endpoint and route identities are required")
	}
	if !isLowerHex(binding.PreregistrationSHA256, 64) || !isLowerHex(binding.CalibrationSHA256, 64) {
		return fmt.Errorf("preflight preregistration and calibration hashes must be SHA-256")
	}
	if binding.ActiveArm != evidence.ArmAffinityP2P && binding.ActiveArm != evidence.ArmLoadAwareP2P {
		return fmt.Errorf("preflight active arm is not a frozen upstream baseline")
	}
	if binding.Transport != "tcp" && binding.Transport != "efa" {
		return fmt.Errorf("preflight transport must be tcp or efa")
	}
	model := binding.Model
	if anyBlank(model.Selector, model.Container, model.Workload, model.ID, model.Revision) || (len(model.Revision) != 40 && len(model.Revision) != 64) || !isLowerHex(model.Revision, len(model.Revision)) || !digestImage(model.Image) {
		return fmt.Errorf("model deployment binding is incomplete or not digest-addressed")
	}
	if model.MinimumReplicas < 6 || model.MaximumReplicas > 8 || model.MinimumReplicas > model.MaximumReplicas || len(model.Pods) < int(model.MinimumReplicas) || len(model.Pods) > int(model.MaximumReplicas) {
		return fmt.Errorf("model deployment binding is outside the six-to-eight replica contract")
	}
	if err := validatePods("model", model.Pods, model.Image); err != nil {
		return err
	}
	epp := binding.EPP
	if anyBlank(epp.Selector, epp.Container) || !digestImage(epp.Image) || (epp.Replicas != 1 && epp.Replicas != 2) || len(epp.Pods) != int(epp.Replicas) {
		return fmt.Errorf("EPP deployment binding is incomplete")
	}
	if err := validatePods("EPP", epp.Pods, epp.Image); err != nil {
		return err
	}
	controller := binding.Controller
	if anyBlank(controller.Selector, controller.Container) || !digestImage(controller.Image) || len(controller.Pods) != 1 {
		return fmt.Errorf("condition-controller deployment binding is incomplete")
	}
	if err := validatePods("condition controller", controller.Pods, controller.Image); err != nil {
		return err
	}
	expectedRegistry := binding.AWSAccountID + ".dkr.ecr." + binding.AWSRegion + ".amazonaws.com/"
	for _, image := range []string{model.Image, epp.Image, controller.Image} {
		if !strings.HasPrefix(image, expectedRegistry) {
			return fmt.Errorf("bound image %q is outside the expected ECR account and region", image)
		}
	}
	if len(binding.Nodes) == 0 {
		return fmt.Errorf("at least one bound GPU node is required")
	}
	instanceType := binding.Nodes[0].InstanceType
	seenNodes := make(map[string]struct{}, len(binding.Nodes))
	for index, node := range binding.Nodes {
		if anyBlank(node.Name, node.UID, node.InstanceType) || node.GPUAllocatable == 0 || node.InstanceType != instanceType {
			return fmt.Errorf("GPU node %d is incomplete or not homogeneous", index+1)
		}
		if _, exists := seenNodes[node.UID]; exists {
			return fmt.Errorf("GPU node UID %q is duplicated", node.UID)
		}
		seenNodes[node.UID] = struct{}{}
	}
	if binding.ObservedAt.IsZero() {
		return fmt.Errorf("preflight observation time is required")
	}
	return nil
}

func (binding PreflightBinding) ValidateProfile(profile bench.Profile) error {
	if len(profile.Arms) != 1 || profile.Arms[0] != binding.ActiveArm {
		return fmt.Errorf("benchmark profile arm does not match the bound deployment")
	}
	if len(profile.EPPReplicas) != 1 || profile.EPPReplicas[0] != binding.EPP.Replicas {
		return fmt.Errorf("benchmark profile EPP replicas do not match the bound deployment")
	}
	if len(profile.Transports) != 1 || profile.Transports[0] != binding.Transport {
		return fmt.Errorf("benchmark profile transport does not match the bound deployment")
	}
	if profile.Model != binding.Model.ID {
		return fmt.Errorf("benchmark profile model does not match the bound deployment")
	}
	return nil
}

func (binding PreflightBinding) InvariantSHA256() (string, error) {
	if err := binding.Validate(); err != nil {
		return "", err
	}
	normalized := binding
	normalized.ObservedAt = time.Time{}
	normalized.Model.Pods = append([]PodBinding(nil), binding.Model.Pods...)
	normalized.EPP.Pods = append([]PodBinding(nil), binding.EPP.Pods...)
	normalized.Controller.Pods = append([]PodBinding(nil), binding.Controller.Pods...)
	normalized.Nodes = append([]NodeBinding(nil), binding.Nodes...)
	sort.Slice(normalized.Model.Pods, func(i, j int) bool { return normalized.Model.Pods[i].UID < normalized.Model.Pods[j].UID })
	sort.Slice(normalized.EPP.Pods, func(i, j int) bool { return normalized.EPP.Pods[i].UID < normalized.EPP.Pods[j].UID })
	sort.Slice(normalized.Controller.Pods, func(i, j int) bool { return normalized.Controller.Pods[i].UID < normalized.Controller.Pods[j].UID })
	sort.Slice(normalized.Nodes, func(i, j int) bool { return normalized.Nodes[i].UID < normalized.Nodes[j].UID })
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func validatePods(kind string, pods []PodBinding, image string) error {
	seen := make(map[string]struct{}, len(pods))
	for index, pod := range pods {
		if anyBlank(pod.Name, pod.UID) || pod.Image != image || pod.RestartCount != 0 {
			return fmt.Errorf("%s pod %d has wrong identity, image, or restart count", kind, index+1)
		}
		if _, exists := seen[pod.UID]; exists {
			return fmt.Errorf("%s pod UID %q is duplicated", kind, pod.UID)
		}
		seen[pod.UID] = struct{}{}
	}
	return nil
}

func anyBlank(values ...string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return true
		}
	}
	return false
}

func digestImage(value string) bool {
	parts := strings.Split(value, "@sha256:")
	return len(parts) == 2 && strings.TrimSpace(parts[0]) != "" && isLowerHex(parts[1], 64)
}

func isLowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

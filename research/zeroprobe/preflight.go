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
	"github.com/JDinSeattle/velaserve/internal/clockbound"
	"github.com/JDinSeattle/velaserve/internal/evidence"
)

const PreflightBindingSchemaVersion = "velaserve.preflight-binding/v3"

type PodBinding struct {
	Name         string `json:"name"`
	UID          string `json:"uid"`
	Image        string `json:"image"`
	NodeName     string `json:"node_name,omitempty"`
	RestartCount uint32 `json:"restart_count"`
}

type NodeBinding struct {
	Name                    string `json:"name"`
	UID                     string `json:"uid"`
	InstanceType            string `json:"instance_type"`
	GPUModel                string `json:"gpu_model"`
	GPUAllocatable          uint32 `json:"gpu_allocatable"`
	Architecture            string `json:"architecture"`
	OSImage                 string `json:"os_image"`
	KernelVersion           string `json:"kernel_version"`
	ContainerRuntimeVersion string `json:"container_runtime_version"`
	KubeletVersion          string `json:"kubelet_version"`
	GPUDriverVersion        string `json:"gpu_driver_version"`
}

type ClockProbeBinding struct {
	NodeName    string                 `json:"node_name"`
	NodeUID     string                 `json:"node_uid"`
	Pod         PodBinding             `json:"pod"`
	Observation clockbound.Observation `json:"observation"`
}

type ClockAttestationBinding struct {
	Image                    string              `json:"image"`
	Samples                  uint32              `json:"samples"`
	MaximumRTTNanoseconds    int64               `json:"maximum_rtt_nanoseconds"`
	MaximumOffsetNanoseconds int64               `json:"maximum_offset_nanoseconds"`
	Probes                   []ClockProbeBinding `json:"probes"`
}

type ImageProvenanceBinding struct {
	Component      string   `json:"component"`
	UpstreamCommit string   `json:"upstream_commit"`
	PatchSHA256s   []string `json:"patch_sha256s"`
	Dirty          bool     `json:"dirty"`
}

type RepositoryImageProvenanceBinding struct {
	Component        string `json:"component"`
	RepositoryCommit string `json:"repository_commit"`
	Dirty            bool   `json:"dirty"`
}

type GatewayRuntimeBinding struct {
	Namespace  string       `json:"namespace"`
	Selector   string       `json:"selector"`
	Container  string       `json:"container"`
	Image      string       `json:"image"`
	SpecSHA256 string       `json:"spec_sha256"`
	Pods       []PodBinding `json:"pods"`
}

type ModelDeploymentBinding struct {
	Release                  string                 `json:"release"`
	Selector                 string                 `json:"selector"`
	Container                string                 `json:"container"`
	Workload                 string                 `json:"workload"`
	Service                  string                 `json:"service"`
	ServiceUID               string                 `json:"service_uid"`
	ServiceSpecSHA256        string                 `json:"service_spec_sha256"`
	ID                       string                 `json:"id"`
	Revision                 string                 `json:"revision"`
	Image                    string                 `json:"image"`
	ImageProvenance          ImageProvenanceBinding `json:"image_provenance"`
	SidecarContainer         string                 `json:"sidecar_container"`
	SidecarImage             string                 `json:"sidecar_image"`
	SidecarProvenance        ImageProvenanceBinding `json:"sidecar_provenance"`
	MinimumReplicas          uint32                 `json:"minimum_replicas"`
	MaximumReplicas          uint32                 `json:"maximum_replicas"`
	BlockSize                uint32                 `json:"block_size"`
	CPUOffloadBytes          uint64                 `json:"cpu_offload_bytes"`
	EnginePort               uint32                 `json:"engine_port"`
	ProxyPort                uint32                 `json:"proxy_port"`
	P2PPort                  uint32                 `json:"p2p_port"`
	KVEventsPort             uint32                 `json:"kv_events_port"`
	RuntimeContractSHA256    string                 `json:"runtime_contract_sha256"`
	NetworkPolicySHA256      string                 `json:"network_policy_sha256"`
	DeveloperAPIEnabled      bool                   `json:"developer_api_enabled"`
	ResetExternal            bool                   `json:"reset_external"`
	SpecSHA256               string                 `json:"spec_sha256"`
	Pods                     []PodBinding           `json:"pods"`
	SidecarPods              []PodBinding           `json:"sidecar_pods"`
	TokenizerVerifiedPodUIDs []string               `json:"tokenizer_verified_pod_uids"`
}

type ConditionEndpointBinding struct {
	ID         string `json:"id"`
	PodUID     string `json:"pod_uid"`
	DNSName    string `json:"dns_name"`
	ChatURL    string `json:"chat_url"`
	ResetURL   string `json:"reset_url"`
	MetricsURL string `json:"metrics_url"`
}

type EPPDeploymentBinding struct {
	Selector            string                 `json:"selector"`
	Container           string                 `json:"container"`
	Image               string                 `json:"image"`
	ImageProvenance     ImageProvenanceBinding `json:"image_provenance"`
	ProxyContainer      string                 `json:"proxy_container"`
	ProxyImage          string                 `json:"proxy_image"`
	ProxyPods           []PodBinding           `json:"proxy_pods"`
	Replicas            uint32                 `json:"replicas"`
	MinCachedTokenDelta uint64                 `json:"min_cached_token_delta"`
	SpecSHA256          string                 `json:"spec_sha256"`
	Pods                []PodBinding           `json:"pods"`
}

type ControllerDeploymentBinding struct {
	Selector            string                           `json:"selector"`
	Container           string                           `json:"container"`
	Image               string                           `json:"image"`
	ImageProvenance     RepositoryImageProvenanceBinding `json:"image_provenance"`
	Revision            string                           `json:"revision"`
	ApplyTimeoutSeconds uint32                           `json:"apply_timeout_seconds"`
	SpecSHA256          string                           `json:"spec_sha256"`
	Pods                []PodBinding                     `json:"pods"`
}

type ConditionDriverBinding struct {
	Endpoint              string                           `json:"endpoint"`
	GatewayChatURL        string                           `json:"gateway_chat_url"`
	Selector              string                           `json:"selector"`
	Container             string                           `json:"container"`
	Image                 string                           `json:"image"`
	ImageProvenance       RepositoryImageProvenanceBinding `json:"image_provenance"`
	SpecSHA256            string                           `json:"spec_sha256"`
	ConfigSHA256          string                           `json:"config_sha256"`
	LoadProfilesSHA256    string                           `json:"load_profiles_sha256"`
	LoadCalibrationSHA256 string                           `json:"load_calibration_sha256"`
	ControlSecretSHA256   string                           `json:"control_secret_sha256"`
	DrainTimeoutSeconds   uint32                           `json:"drain_timeout_seconds"`
	DrainPollMilliseconds uint32                           `json:"drain_poll_milliseconds"`
	Endpoints             []ConditionEndpointBinding       `json:"endpoints"`
	Pods                  []PodBinding                     `json:"pods"`
}

type PreflightBinding struct {
	SchemaVersion               string                      `json:"schema_version"`
	RepositoryCommit            string                      `json:"repository_commit"`
	AWSAccountID                string                      `json:"aws_account_id"`
	AWSRegion                   string                      `json:"aws_region"`
	ClusterARN                  string                      `json:"cluster_arn"`
	Namespace                   string                      `json:"namespace"`
	Endpoint                    string                      `json:"endpoint"`
	GatewayUID                  string                      `json:"gateway_uid"`
	HTTPRouteUID                string                      `json:"http_route_uid"`
	GatewayDataPlane            GatewayRuntimeBinding       `json:"gateway_data_plane"`
	GatewayController           GatewayRuntimeBinding       `json:"gateway_controller"`
	PreregistrationSHA256       string                      `json:"preregistration_sha256"`
	CalibrationSHA256           string                      `json:"calibration_sha256"`
	BenchmarkProfileSHA256      string                      `json:"benchmark_profile_sha256"`
	ProfileCalibrationSHA256    string                      `json:"profile_calibration_sha256"`
	RoutingSHA256               string                      `json:"routing_sha256"`
	RouterConfigSHA256          string                      `json:"router_config_sha256"`
	RouterConfigInvariantSHA256 string                      `json:"router_config_invariant_sha256"`
	ZeroingContractSHA256       string                      `json:"zeroing_contract_sha256"`
	ActiveArm                   evidence.Arm                `json:"active_arm"`
	Transport                   string                      `json:"transport"`
	Model                       ModelDeploymentBinding      `json:"model"`
	EPP                         EPPDeploymentBinding        `json:"epp"`
	Controller                  ControllerDeploymentBinding `json:"condition_controller"`
	Driver                      ConditionDriverBinding      `json:"condition_driver"`
	Nodes                       []NodeBinding               `json:"nodes"`
	ClockAttestation            ClockAttestationBinding     `json:"clock_attestation"`
	ObservedAt                  time.Time                   `json:"observed_at"`
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
	if !isLowerHex(binding.PreregistrationSHA256, 64) || !isLowerHex(binding.CalibrationSHA256, 64) || !isLowerHex(binding.BenchmarkProfileSHA256, 64) || !isLowerHex(binding.ProfileCalibrationSHA256, 64) || !isLowerHex(binding.RoutingSHA256, 64) || !isLowerHex(binding.RouterConfigSHA256, 64) || !isLowerHex(binding.RouterConfigInvariantSHA256, 64) || !isLowerHex(binding.ZeroingContractSHA256, 64) {
		return fmt.Errorf("preflight preregistration, oracle calibration, benchmark profile, profile calibration, routing, router-config, and zeroing-contract hashes must be SHA-256")
	}
	if err := validateGatewayRuntime("gateway data plane", binding.GatewayDataPlane); err != nil {
		return err
	}
	if err := validateGatewayRuntime("gateway controller", binding.GatewayController); err != nil {
		return err
	}
	if binding.ActiveArm != evidence.ArmAffinityP2P && binding.ActiveArm != evidence.ArmLoadAwareP2P {
		return fmt.Errorf("preflight active arm is not a frozen upstream baseline")
	}
	if binding.Transport != "tcp" {
		return fmt.Errorf("preflight Stage-1 runtime adapter currently requires tcp")
	}
	model := binding.Model
	if anyBlank(model.Release, model.Selector, model.Container, model.Workload, model.Service, model.ServiceUID, model.ID, model.Revision, model.SidecarContainer) || (len(model.Revision) != 40 && len(model.Revision) != 64) || !isLowerHex(model.Revision, len(model.Revision)) || !digestImage(model.Image) || !digestImage(model.SidecarImage) || !isLowerHex(model.ServiceSpecSHA256, 64) || !isLowerHex(model.RuntimeContractSHA256, 64) || !isLowerHex(model.NetworkPolicySHA256, 64) || !model.DeveloperAPIEnabled || !model.ResetExternal || !isLowerHex(model.SpecSHA256, 64) {
		return fmt.Errorf("model deployment binding is incomplete or not digest-addressed")
	}
	if err := validateImageProvenance(model.ImageProvenance, "vllm", 1); err != nil {
		return fmt.Errorf("model image provenance: %w", err)
	}
	if err := validateImageProvenance(model.SidecarProvenance, "routing-sidecar", 2); err != nil {
		return fmt.Errorf("routing-sidecar image provenance: %w", err)
	}
	if model.BlockSize != 64 || model.CPUOffloadBytes == 0 || model.EnginePort != 8200 || model.ProxyPort != 8000 || model.P2PPort != 7777 || model.KVEventsPort != 5556 {
		return fmt.Errorf("model runtime binding does not match the frozen OffloadingConnector contract")
	}
	if model.MinimumReplicas < 6 || model.MaximumReplicas > 8 || model.MinimumReplicas > model.MaximumReplicas || len(model.Pods) < int(model.MinimumReplicas) || len(model.Pods) > int(model.MaximumReplicas) {
		return fmt.Errorf("model deployment binding is outside the six-to-eight replica contract")
	}
	if err := validatePods("model", model.Pods, model.Image); err != nil {
		return err
	}
	modelNodes := make(map[string]struct{}, len(model.Pods))
	for _, pod := range model.Pods {
		if strings.TrimSpace(pod.NodeName) == "" {
			return fmt.Errorf("model pod %q has no bound GPU node", pod.Name)
		}
		if _, exists := modelNodes[pod.NodeName]; exists {
			return fmt.Errorf("multiple model pods are scheduled on GPU node %q", pod.NodeName)
		}
		modelNodes[pod.NodeName] = struct{}{}
	}
	if len(model.SidecarPods) != len(model.Pods) {
		return fmt.Errorf("model routing-sidecar pod count does not match model pod count")
	}
	if err := validatePods("model routing sidecar", model.SidecarPods, model.SidecarImage); err != nil {
		return err
	}
	modelUIDs := make(map[string]string, len(model.Pods))
	for _, pod := range model.Pods {
		modelUIDs[pod.UID] = pod.NodeName
	}
	for _, pod := range model.SidecarPods {
		nodeName, exists := modelUIDs[pod.UID]
		if !exists {
			return fmt.Errorf("model routing-sidecar pod UID %q has no model container", pod.UID)
		}
		if pod.NodeName != nodeName {
			return fmt.Errorf("model routing-sidecar pod UID %q is bound to the wrong node", pod.UID)
		}
	}
	if len(model.TokenizerVerifiedPodUIDs) != len(model.Pods) {
		return fmt.Errorf("model tokenizer verification does not cover every exact model pod")
	}
	verifiedTokenizerUIDs := make(map[string]struct{}, len(model.TokenizerVerifiedPodUIDs))
	for _, uid := range model.TokenizerVerifiedPodUIDs {
		if strings.TrimSpace(uid) == "" {
			return fmt.Errorf("model tokenizer verification contains an empty pod UID")
		}
		if _, duplicate := verifiedTokenizerUIDs[uid]; duplicate {
			return fmt.Errorf("model tokenizer verification repeats pod UID %q", uid)
		}
		if _, exists := modelUIDs[uid]; !exists {
			return fmt.Errorf("model tokenizer verification UID %q is not an exact selected model pod", uid)
		}
		verifiedTokenizerUIDs[uid] = struct{}{}
	}
	epp := binding.EPP
	if anyBlank(epp.Selector, epp.Container, epp.ProxyContainer) || !digestImage(epp.Image) || !digestImage(epp.ProxyImage) || !isLowerHex(epp.SpecSHA256, 64) || (epp.Replicas != 1 && epp.Replicas != 2) || epp.MinCachedTokenDelta == 0 || len(epp.Pods) != int(epp.Replicas) || len(epp.ProxyPods) != int(epp.Replicas) {
		return fmt.Errorf("EPP deployment binding is incomplete")
	}
	if err := validateImageProvenance(epp.ImageProvenance, "epp", 2); err != nil {
		return fmt.Errorf("EPP image provenance: %w", err)
	}
	if err := validatePods("EPP", epp.Pods, epp.Image); err != nil {
		return err
	}
	if err := validatePods("EPP inner Envoy proxy", epp.ProxyPods, epp.ProxyImage); err != nil {
		return err
	}
	for index := range epp.Pods {
		if epp.Pods[index].UID != epp.ProxyPods[index].UID {
			return fmt.Errorf("EPP and inner Envoy pod bindings do not share exact pod UIDs")
		}
	}
	controller := binding.Controller
	if anyBlank(controller.Selector, controller.Container) || !digestImage(controller.Image) || !isLowerHex(controller.SpecSHA256, 64) || controller.ApplyTimeoutSeconds < 600 || controller.ApplyTimeoutSeconds > 3600 || len(controller.Pods) != 1 {
		return fmt.Errorf("condition-controller deployment binding is incomplete")
	}
	if err := validateRepositoryImageProvenance(controller.ImageProvenance, binding.RepositoryCommit); err != nil {
		return fmt.Errorf("condition-controller image provenance: %w", err)
	}
	if err := validatePods("condition controller", controller.Pods, controller.Image); err != nil {
		return err
	}
	driver := binding.Driver
	driverEndpoint, err := url.ParseRequestURI(driver.Endpoint)
	driverGateway, gatewayErr := url.ParseRequestURI(driver.GatewayChatURL)
	if err != nil || driverEndpoint.Host == "" || (driverEndpoint.Scheme != "http" && driverEndpoint.Scheme != "https") || driverEndpoint.User != nil || gatewayErr != nil || driverGateway.Host == "" || (driverGateway.Scheme != "http" && driverGateway.Scheme != "https") || driverGateway.User != nil || driver.GatewayChatURL != binding.Endpoint || anyBlank(driver.Selector, driver.Container) || !digestImage(driver.Image) || driver.Image != controller.Image || !isLowerHex(driver.SpecSHA256, 64) || !isLowerHex(driver.ConfigSHA256, 64) || !isLowerHex(driver.LoadProfilesSHA256, 64) || !isLowerHex(driver.LoadCalibrationSHA256, 64) || !isLowerHex(driver.ControlSecretSHA256, 64) || driver.DrainTimeoutSeconds < 30 || driver.DrainTimeoutSeconds > 600 || driver.DrainPollMilliseconds < 50 || driver.DrainPollMilliseconds > 5000 || driver.DrainPollMilliseconds >= driver.DrainTimeoutSeconds*1000 || len(driver.Pods) != 1 {
		return fmt.Errorf("condition-driver deployment binding is incomplete")
	}
	if err := validateRepositoryImageProvenance(driver.ImageProvenance, binding.RepositoryCommit); err != nil || driver.ImageProvenance != controller.ImageProvenance {
		return fmt.Errorf("condition-driver image provenance is not the exact controller repository build")
	}
	if err := validatePods("condition driver", driver.Pods, driver.Image); err != nil {
		return err
	}
	if len(driver.Endpoints) != len(model.Pods) {
		return fmt.Errorf("condition-driver endpoints must correspond one-to-one with model pods")
	}
	modelByName := make(map[string]PodBinding, len(model.Pods))
	for _, pod := range model.Pods {
		modelByName[pod.Name] = pod
	}
	seenEndpointIDs := make(map[string]struct{}, len(driver.Endpoints))
	for index, endpointBinding := range driver.Endpoints {
		pod, exists := modelByName[endpointBinding.ID]
		if !exists || pod.UID != endpointBinding.PodUID {
			return fmt.Errorf("condition endpoint %d does not bind an exact model pod UID", index+1)
		}
		if _, duplicate := seenEndpointIDs[endpointBinding.ID]; duplicate {
			return fmt.Errorf("condition endpoint ID %q is duplicated", endpointBinding.ID)
		}
		seenEndpointIDs[endpointBinding.ID] = struct{}{}
		expectedDNS := endpointBinding.ID + "." + model.Service + "-headless." + binding.Namespace + ".svc.cluster.local"
		if endpointBinding.DNSName != expectedDNS || !exactConditionEndpointURL(endpointBinding.ChatURL, expectedDNS, model.EnginePort, "/v1/chat/completions") || !exactConditionEndpointURL(endpointBinding.ResetURL, expectedDNS, model.EnginePort, "/reset_prefix_cache") || !exactConditionEndpointURL(endpointBinding.MetricsURL, expectedDNS, model.EnginePort, "/metrics") {
			return fmt.Errorf("condition endpoint %q is not the stable direct-engine pod endpoint", endpointBinding.ID)
		}
	}
	expectedRegistry := binding.AWSAccountID + ".dkr.ecr." + binding.AWSRegion + ".amazonaws.com/"
	for _, image := range []string{model.Image, model.SidecarImage, epp.Image, controller.Image, driver.Image} {
		if !strings.HasPrefix(image, expectedRegistry) {
			return fmt.Errorf("bound image %q is outside the expected ECR account and region", image)
		}
	}
	if len(binding.Nodes) != len(model.Pods) {
		return fmt.Errorf("bound GPU nodes must correspond one-to-one with model pods")
	}
	instanceType := binding.Nodes[0].InstanceType
	gpuModel := binding.Nodes[0].GPUModel
	gpuDriverVersion := binding.Nodes[0].GPUDriverVersion
	seenNodes := make(map[string]struct{}, len(binding.Nodes))
	for index, node := range binding.Nodes {
		if anyBlank(node.Name, node.UID, node.InstanceType, node.GPUModel, node.Architecture, node.OSImage, node.KernelVersion, node.ContainerRuntimeVersion, node.KubeletVersion, node.GPUDriverVersion) || node.GPUAllocatable != 1 || node.InstanceType != instanceType || node.GPUModel != gpuModel || node.GPUDriverVersion != gpuDriverVersion {
			return fmt.Errorf("GPU node %d is incomplete or not homogeneous", index+1)
		}
		if _, exists := seenNodes[node.UID]; exists {
			return fmt.Errorf("GPU node UID %q is duplicated", node.UID)
		}
		seenNodes[node.UID] = struct{}{}
		if _, scheduled := modelNodes[node.Name]; !scheduled {
			return fmt.Errorf("bound GPU node %q has no model pod", node.Name)
		}
	}
	if err := validateClockAttestation(binding); err != nil {
		return err
	}
	if binding.ObservedAt.IsZero() {
		return fmt.Errorf("preflight observation time is required")
	}
	return nil
}

func validateClockAttestation(binding PreflightBinding) error {
	attestation := binding.ClockAttestation
	if attestation.Image != binding.Controller.Image || !digestImage(attestation.Image) || attestation.Samples != 5 || attestation.MaximumRTTNanoseconds != int64(250*time.Millisecond) || attestation.MaximumOffsetNanoseconds != int64(100*time.Millisecond) {
		return fmt.Errorf("clock attestation image and frozen sampling bounds are invalid")
	}
	expectedNodes := make(map[string]struct{})
	addPods := func(kind string, pods []PodBinding) error {
		for _, pod := range pods {
			if strings.TrimSpace(pod.NodeName) == "" {
				return fmt.Errorf("%s pod %q has no node for clock attestation", kind, pod.Name)
			}
			expectedNodes[pod.NodeName] = struct{}{}
		}
		return nil
	}
	for _, item := range []struct {
		name string
		pods []PodBinding
	}{
		{"model", binding.Model.Pods}, {"EPP", binding.EPP.Pods}, {"gateway data plane", binding.GatewayDataPlane.Pods},
		{"condition controller", binding.Controller.Pods}, {"condition driver", binding.Driver.Pods},
	} {
		if err := addPods(item.name, item.pods); err != nil {
			return err
		}
	}
	if len(attestation.Probes) != len(expectedNodes) {
		return fmt.Errorf("clock attestation covers %d nodes, want %d timestamp-producing nodes", len(attestation.Probes), len(expectedNodes))
	}
	seenNodes := make(map[string]struct{}, len(attestation.Probes))
	seenNodeUIDs := make(map[string]struct{}, len(attestation.Probes))
	seenPodUIDs := make(map[string]struct{}, len(attestation.Probes))
	for index, probe := range attestation.Probes {
		if _, exists := expectedNodes[probe.NodeName]; !exists || strings.TrimSpace(probe.NodeUID) == "" || probe.Pod.NodeName != probe.NodeName || probe.Pod.Image != attestation.Image || probe.Pod.RestartCount != 0 || anyBlank(probe.Pod.Name, probe.Pod.UID) {
			return fmt.Errorf("clock probe %d has invalid node, pod, image, or restart binding", index+1)
		}
		if _, duplicate := seenNodes[probe.NodeName]; duplicate {
			return fmt.Errorf("clock probe node %q is duplicated", probe.NodeName)
		}
		if _, duplicate := seenNodeUIDs[probe.NodeUID]; duplicate {
			return fmt.Errorf("clock probe node UID %q is duplicated", probe.NodeUID)
		}
		if _, duplicate := seenPodUIDs[probe.Pod.UID]; duplicate {
			return fmt.Errorf("clock probe pod UID %q is duplicated", probe.Pod.UID)
		}
		seenNodes[probe.NodeName] = struct{}{}
		seenNodeUIDs[probe.NodeUID] = struct{}{}
		seenPodUIDs[probe.Pod.UID] = struct{}{}
		observation := probe.Observation
		if observation.SchemaVersion != clockbound.ObservationSchemaVersion || observation.Samples != attestation.Samples || observation.MaximumRTTNanoseconds != attestation.MaximumRTTNanoseconds || observation.MaximumOffsetNanoseconds != attestation.MaximumOffsetNanoseconds || observation.BestRTTNanoseconds <= 0 || observation.BestRTTNanoseconds > observation.MaximumRTTNanoseconds || observation.OffsetMinimumNanoseconds > observation.OffsetMaximumNanoseconds || observation.OffsetMinimumNanoseconds < -observation.MaximumOffsetNanoseconds || observation.OffsetMaximumNanoseconds > observation.MaximumOffsetNanoseconds || observation.ObservedAt.IsZero() {
			return fmt.Errorf("clock probe %d has an invalid RTT/offset observation", index+1)
		}
	}
	return nil
}

func exactConditionEndpointURL(raw, expectedHost string, expectedPort uint32, expectedPath string) bool {
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Hostname() != expectedHost || parsed.Port() != fmt.Sprintf("%d", expectedPort) || parsed.EscapedPath() != expectedPath || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	return true
}

func validateImageProvenance(provenance ImageProvenanceBinding, component string, patchCount int) error {
	if provenance.Component != component || !isLowerHex(provenance.UpstreamCommit, 40) || provenance.Dirty || len(provenance.PatchSHA256s) != patchCount {
		return fmt.Errorf("component, clean source commit, and exact patch set are required")
	}
	seen := make(map[string]struct{}, len(provenance.PatchSHA256s))
	for _, digest := range provenance.PatchSHA256s {
		if !isLowerHex(digest, 64) {
			return fmt.Errorf("patch digest is not SHA-256")
		}
		if _, exists := seen[digest]; exists {
			return fmt.Errorf("patch digest is duplicated")
		}
		seen[digest] = struct{}{}
	}
	return nil
}

func validateRepositoryImageProvenance(provenance RepositoryImageProvenanceBinding, repositoryCommit string) error {
	if provenance.Component != "velaserve" || provenance.RepositoryCommit != repositoryCommit || !isLowerHex(provenance.RepositoryCommit, 40) || provenance.Dirty {
		return fmt.Errorf("clean VelaServe repository image provenance is required")
	}
	return nil
}

func validateGatewayRuntime(name string, runtime GatewayRuntimeBinding) error {
	if anyBlank(runtime.Namespace, runtime.Selector, runtime.Container) || !digestImage(runtime.Image) || !isLowerHex(runtime.SpecSHA256, 64) || len(runtime.Pods) == 0 {
		return fmt.Errorf("%s binding is incomplete or not digest-addressed", name)
	}
	if err := validatePods(name, runtime.Pods, runtime.Image); err != nil {
		return err
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

func (binding PreflightBinding) ValidateProfileCalibration(calibration bench.ProfileCalibration) error {
	if calibration.ModelRevision != binding.Model.Revision || calibration.MinCachedTokenDelta != binding.EPP.MinCachedTokenDelta {
		return fmt.Errorf("profile calibration model revision or measured minCachedTokenDelta does not match the bound deployment")
	}
	deployment := calibration.CrossoverEvidence.Deployment
	if deployment.RepositoryCommit != binding.RepositoryCommit || deployment.ModelImage != binding.Model.Image || deployment.ModelSpecSHA256 != binding.Model.SpecSHA256 || deployment.ActiveArm != string(binding.ActiveArm) || deployment.EPPImage != binding.EPP.Image || deployment.EPPReplicas != binding.EPP.Replicas || deployment.ReplicaCount != uint32(len(binding.Model.Pods)) || deployment.RouterConfigInvariantSHA256 != binding.RouterConfigInvariantSHA256 {
		return fmt.Errorf("crossover calibration repository, model, EPP, fleet, arm, or routing deployment differs from preflight")
	}
	if len(binding.Nodes) != int(deployment.ReplicaCount) {
		return fmt.Errorf("crossover calibration hardware-node count differs from preflight")
	}
	for _, node := range binding.Nodes {
		if node.GPUModel != deployment.GPUModel || node.GPUDriverVersion != deployment.GPUDriverVersion || node.InstanceType != deployment.InstanceType {
			return fmt.Errorf("crossover calibration hardware differs on node %q", node.Name)
		}
	}
	return nil
}

func (binding PreflightBinding) InvariantSHA256() (string, error) {
	if err := binding.Validate(); err != nil {
		return "", err
	}
	normalized := binding
	normalized.ObservedAt = time.Time{}
	// Z0-B and Z0-C intentionally use different workload profiles. The exact
	// per-bundle hash is verified independently and is not a deployment invariant.
	normalized.BenchmarkProfileSHA256 = ""
	normalized.Model.Pods = append([]PodBinding(nil), binding.Model.Pods...)
	normalized.Model.SidecarPods = append([]PodBinding(nil), binding.Model.SidecarPods...)
	normalized.EPP.Pods = append([]PodBinding(nil), binding.EPP.Pods...)
	normalized.Controller.Pods = append([]PodBinding(nil), binding.Controller.Pods...)
	normalized.Driver.Pods = append([]PodBinding(nil), binding.Driver.Pods...)
	normalized.Nodes = append([]NodeBinding(nil), binding.Nodes...)
	normalized.ClockAttestation.Probes = append([]ClockProbeBinding(nil), binding.ClockAttestation.Probes...)
	for index := range normalized.ClockAttestation.Probes {
		observation := &normalized.ClockAttestation.Probes[index].Observation
		observation.BestRTTNanoseconds = 0
		observation.OffsetMinimumNanoseconds = 0
		observation.OffsetMaximumNanoseconds = 0
		observation.ObservedAt = time.Time{}
	}
	sort.Slice(normalized.Model.Pods, func(i, j int) bool { return normalized.Model.Pods[i].UID < normalized.Model.Pods[j].UID })
	sort.Slice(normalized.Model.SidecarPods, func(i, j int) bool { return normalized.Model.SidecarPods[i].UID < normalized.Model.SidecarPods[j].UID })
	sort.Slice(normalized.EPP.Pods, func(i, j int) bool { return normalized.EPP.Pods[i].UID < normalized.EPP.Pods[j].UID })
	sort.Slice(normalized.Controller.Pods, func(i, j int) bool { return normalized.Controller.Pods[i].UID < normalized.Controller.Pods[j].UID })
	sort.Slice(normalized.Driver.Pods, func(i, j int) bool { return normalized.Driver.Pods[i].UID < normalized.Driver.Pods[j].UID })
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

package zeroprobe

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JDinSeattle/velaserve/internal/bench"
	"github.com/JDinSeattle/velaserve/internal/clockbound"
	"github.com/JDinSeattle/velaserve/internal/evidence"
)

func TestLoadPreflightBindingRejectsProfileMismatch(t *testing.T) {
	binding := validPreflightBinding()
	path := filepath.Join(t.TempDir(), "preflight-binding.json")
	writePreflightBindingForTest(t, path, binding)

	loaded, err := LoadPreflightBinding(path)
	if err != nil {
		t.Fatalf("LoadPreflightBinding() error = %v", err)
	}
	profile := bench.Profile{
		Model:       binding.Model.ID,
		Arms:        []evidence.Arm{binding.ActiveArm},
		EPPReplicas: []uint32{binding.EPP.Replicas},
		Transports:  []string{binding.Transport},
	}
	if err := loaded.ValidateProfile(profile); err != nil {
		t.Fatalf("ValidateProfile() error = %v", err)
	}
	profile.Model = "different-model"
	if err := loaded.ValidateProfile(profile); err == nil || !strings.Contains(err.Error(), "model") {
		t.Fatalf("ValidateProfile() error = %v, want model mismatch", err)
	}
}

func TestPreflightBindingInvariantDetectsDeploymentDriftButIgnoresObservationTime(t *testing.T) {
	left := validPreflightBinding()
	right := left
	right.ObservedAt = right.ObservedAt.Add(time.Hour)
	right.ClockAttestation.Probes = append([]ClockProbeBinding(nil), right.ClockAttestation.Probes...)
	right.ClockAttestation.Probes[0].Observation.ObservedAt = right.ClockAttestation.Probes[0].Observation.ObservedAt.Add(time.Hour)
	right.ClockAttestation.Probes[0].Observation.BestRTTNanoseconds++
	right.BenchmarkProfileSHA256 = strings.Repeat("f", 64)
	leftHash, err := left.InvariantSHA256()
	if err != nil {
		t.Fatal(err)
	}
	rightHash, err := right.InvariantSHA256()
	if err != nil {
		t.Fatal(err)
	}
	if leftHash != rightHash {
		t.Fatal("per-run observation/profile metadata changed the deployment invariant")
	}
	right.Model.Pods = append([]PodBinding(nil), right.Model.Pods...)
	right.Model.SidecarPods = append([]PodBinding(nil), right.Model.SidecarPods...)
	right.Model.Pods[0].UID = "different-uid"
	right.Model.SidecarPods[0].UID = "different-uid"
	right.Model.TokenizerVerifiedPodUIDs = append([]string(nil), right.Model.TokenizerVerifiedPodUIDs...)
	right.Model.TokenizerVerifiedPodUIDs[0] = "different-uid"
	right.Driver.Endpoints = append([]ConditionEndpointBinding(nil), right.Driver.Endpoints...)
	right.Driver.Endpoints[0].PodUID = "different-uid"
	rightHash, err = right.InvariantSHA256()
	if err != nil {
		t.Fatal(err)
	}
	if leftHash == rightHash {
		t.Fatal("pod identity drift did not change the deployment invariant")
	}
}

func TestPreflightBindingRejectsTokenizerVerificationForDifferentPodSet(t *testing.T) {
	binding := validPreflightBinding()
	binding.Model.TokenizerVerifiedPodUIDs[0] = "stale-pod-uid"
	if err := binding.Validate(); err == nil || !strings.Contains(err.Error(), "tokenizer verification") {
		t.Fatalf("Validate() error = %v, want exact tokenizer pod binding rejection", err)
	}
}

func TestPreflightBindingRejectsPackedOrMultiGPUNodes(t *testing.T) {
	binding := validPreflightBinding()
	binding.Model.Pods[1].NodeName = binding.Model.Pods[0].NodeName
	binding.Model.SidecarPods[1].NodeName = binding.Model.SidecarPods[0].NodeName
	if err := binding.Validate(); err == nil || !strings.Contains(err.Error(), "multiple model pods") {
		t.Fatalf("Validate() error = %v, want packed-node rejection", err)
	}
	binding = validPreflightBinding()
	binding.Nodes[0].GPUAllocatable = 2
	if err := binding.Validate(); err == nil || !strings.Contains(err.Error(), "homogeneous") {
		t.Fatalf("Validate() error = %v, want multi-GPU-node rejection", err)
	}
}

func TestPreflightBindingRejectsMissingOrOutOfBoundClockProbe(t *testing.T) {
	binding := validPreflightBinding()
	binding.ClockAttestation.Probes = binding.ClockAttestation.Probes[1:]
	if err := binding.Validate(); err == nil || !strings.Contains(err.Error(), "clock attestation") {
		t.Fatalf("Validate() error = %v, want clock coverage rejection", err)
	}
	binding = validPreflightBinding()
	binding.ClockAttestation.Probes[0].Observation.OffsetMaximumNanoseconds = int64(101 * time.Millisecond)
	if err := binding.Validate(); err == nil || !strings.Contains(err.Error(), "clock probe") {
		t.Fatalf("Validate() error = %v, want clock offset rejection", err)
	}
}

func validPreflightBinding() PreflightBinding {
	modelPods := make([]PodBinding, 6)
	sidecarPods := make([]PodBinding, 6)
	for index := range modelPods {
		nodeName := "node-" + string(rune('a'+index))
		modelPods[index] = PodBinding{Name: "model-" + string(rune('a'+index)), UID: "model-uid-" + string(rune('a'+index)), Image: "123456789012.dkr.ecr.us-west-2.amazonaws.com/model@sha256:" + strings.Repeat("1", 64), NodeName: nodeName}
		sidecarPods[index] = PodBinding{Name: modelPods[index].Name, UID: modelPods[index].UID, Image: "123456789012.dkr.ecr.us-west-2.amazonaws.com/sidecar@sha256:" + strings.Repeat("0", 64), NodeName: nodeName}
	}
	nodes := make([]NodeBinding, len(modelPods))
	conditionEndpoints := make([]ConditionEndpointBinding, len(modelPods))
	for index := range nodes {
		nodes[index] = NodeBinding{Name: modelPods[index].NodeName, UID: "node-uid-" + string(rune('a'+index)), InstanceType: "g6e.xlarge", GPUModel: "NVIDIA L40S", GPUAllocatable: 1, Architecture: "amd64", OSImage: "Amazon Linux 2023", KernelVersion: "6.1", ContainerRuntimeVersion: "containerd://2", KubeletVersion: "v1.34.0", GPUDriverVersion: "570.1"}
		dnsName := modelPods[index].Name + ".model-headless.velaserve-z0.svc.cluster.local"
		conditionEndpoints[index] = ConditionEndpointBinding{ID: modelPods[index].Name, PodUID: modelPods[index].UID, DNSName: dnsName, ChatURL: "http://" + dnsName + ":8200/v1/chat/completions", ResetURL: "http://" + dnsName + ":8200/reset_prefix_cache", MetricsURL: "http://" + dnsName + ":8200/metrics"}
	}
	binding := PreflightBinding{
		SchemaVersion:               PreflightBindingSchemaVersion,
		RepositoryCommit:            strings.Repeat("a", 40),
		AWSAccountID:                "123456789012",
		AWSRegion:                   "us-west-2",
		ClusterARN:                  "arn:aws:eks:us-west-2:123456789012:cluster/velaserve-z0",
		Namespace:                   "velaserve-z0",
		Endpoint:                    "https://gateway.example/v1/chat/completions",
		GatewayUID:                  "gateway-uid",
		HTTPRouteUID:                "route-uid",
		GatewayDataPlane:            GatewayRuntimeBinding{Namespace: "envoy-gateway-system", Selector: "gateway=velaserve", Container: "envoy", Image: "123456789012.dkr.ecr.us-west-2.amazonaws.com/envoy@sha256:" + strings.Repeat("a", 64), SpecSHA256: strings.Repeat("b", 64), Pods: []PodBinding{{Name: "envoy-a", UID: "envoy-uid-a", NodeName: "gateway-node", Image: "123456789012.dkr.ecr.us-west-2.amazonaws.com/envoy@sha256:" + strings.Repeat("a", 64)}}},
		GatewayController:           GatewayRuntimeBinding{Namespace: "envoy-gateway-system", Selector: "app=envoy-gateway", Container: "envoy-gateway", Image: "123456789012.dkr.ecr.us-west-2.amazonaws.com/envoy-gateway@sha256:" + strings.Repeat("b", 64), SpecSHA256: strings.Repeat("c", 64), Pods: []PodBinding{{Name: "envoy-gateway-a", UID: "envoy-gateway-uid-a", Image: "123456789012.dkr.ecr.us-west-2.amazonaws.com/envoy-gateway@sha256:" + strings.Repeat("b", 64)}}},
		PreregistrationSHA256:       strings.Repeat("b", 64),
		CalibrationSHA256:           strings.Repeat("c", 64),
		BenchmarkProfileSHA256:      strings.Repeat("7", 64),
		ProfileCalibrationSHA256:    strings.Repeat("8", 64),
		RoutingSHA256:               strings.Repeat("4", 64),
		RouterConfigSHA256:          strings.Repeat("5", 64),
		RouterConfigInvariantSHA256: strings.Repeat("f", 64),
		ZeroingContractSHA256:       strings.Repeat("6", 64),
		ActiveArm:                   evidence.ArmLoadAwareP2P,
		Transport:                   "tcp",
		Model: ModelDeploymentBinding{
			Release: "velaserve-model", Selector: "app=model", Container: "model", Workload: "model", Service: "model", ServiceUID: "service-uid", ServiceSpecSHA256: strings.Repeat("6", 64), ID: "Qwen/Qwen3-8B",
			Revision: strings.Repeat("d", 40), Image: modelPods[0].Image, ImageProvenance: ImageProvenanceBinding{Component: "vllm", UpstreamCommit: strings.Repeat("d", 40), PatchSHA256s: []string{strings.Repeat("a", 64)}}, SidecarContainer: "routing-proxy", SidecarImage: sidecarPods[0].Image, SidecarProvenance: ImageProvenanceBinding{Component: "routing-sidecar", UpstreamCommit: strings.Repeat("e", 40), PatchSHA256s: []string{strings.Repeat("b", 64), strings.Repeat("c", 64)}}, MinimumReplicas: 6, MaximumReplicas: 8,
			BlockSize: 64, CPUOffloadBytes: 17179869184, EnginePort: 8200, ProxyPort: 8000, P2PPort: 7777, KVEventsPort: 5556,
			RuntimeContractSHA256: strings.Repeat("5", 64), NetworkPolicySHA256: strings.Repeat("4", 64), DeveloperAPIEnabled: true, ResetExternal: true, SpecSHA256: strings.Repeat("7", 64), Pods: modelPods, SidecarPods: sidecarPods,
			TokenizerVerifiedPodUIDs: []string{"model-uid-a", "model-uid-b", "model-uid-c", "model-uid-d", "model-uid-e", "model-uid-f"},
		},
		EPP: EPPDeploymentBinding{
			Selector: "llm-d-router-gateway=velaserve-epp", Container: "epp", ProxyContainer: "envoy-proxy", Replicas: 1, MinCachedTokenDelta: 128,
			Image:           "123456789012.dkr.ecr.us-west-2.amazonaws.com/epp@sha256:" + strings.Repeat("2", 64),
			ProxyImage:      "docker.io/envoyproxy/envoy-distroless@sha256:" + strings.Repeat("4", 64),
			ImageProvenance: ImageProvenanceBinding{Component: "epp", UpstreamCommit: strings.Repeat("e", 40), PatchSHA256s: []string{strings.Repeat("b", 64), strings.Repeat("c", 64)}},
			SpecSHA256:      strings.Repeat("8", 64),
			Pods:            []PodBinding{{Name: "epp-a", UID: "epp-uid-a", NodeName: "epp-node", Image: "123456789012.dkr.ecr.us-west-2.amazonaws.com/epp@sha256:" + strings.Repeat("2", 64)}},
			ProxyPods:       []PodBinding{{Name: "epp-a", UID: "epp-uid-a", NodeName: "epp-node", Image: "docker.io/envoyproxy/envoy-distroless@sha256:" + strings.Repeat("4", 64)}},
		},
		Controller: ControllerDeploymentBinding{
			Selector: "app=controller", Container: "condition-controller", Revision: strings.Repeat("a", 40), ApplyTimeoutSeconds: 900,
			Image: "123456789012.dkr.ecr.us-west-2.amazonaws.com/velaserve@sha256:" + strings.Repeat("3", 64), ImageProvenance: RepositoryImageProvenanceBinding{Component: "velaserve", RepositoryCommit: strings.Repeat("a", 40)},
			SpecSHA256: strings.Repeat("9", 64),
			Pods:       []PodBinding{{Name: "controller-a", UID: "controller-uid-a", NodeName: "control-node", Image: "123456789012.dkr.ecr.us-west-2.amazonaws.com/velaserve@sha256:" + strings.Repeat("3", 64)}},
		},
		Driver: ConditionDriverBinding{
			Endpoint: "http://condition-driver.velaserve-z0.svc.cluster.local:8083/v1/conditions/apply", GatewayChatURL: "https://gateway.example/v1/chat/completions", Selector: "app=condition-driver", Container: "condition-driver",
			Image: "123456789012.dkr.ecr.us-west-2.amazonaws.com/velaserve@sha256:" + strings.Repeat("3", 64), ImageProvenance: RepositoryImageProvenanceBinding{Component: "velaserve", RepositoryCommit: strings.Repeat("a", 40)}, SpecSHA256: strings.Repeat("e", 64), ConfigSHA256: strings.Repeat("f", 64), LoadProfilesSHA256: strings.Repeat("2", 64), LoadCalibrationSHA256: strings.Repeat("3", 64), ControlSecretSHA256: strings.Repeat("c", 64),
			DrainTimeoutSeconds: 120, DrainPollMilliseconds: 500, Endpoints: conditionEndpoints,
			Pods: []PodBinding{{Name: "driver-a", UID: "driver-uid-a", NodeName: "control-node", Image: "123456789012.dkr.ecr.us-west-2.amazonaws.com/velaserve@sha256:" + strings.Repeat("3", 64)}},
		},
		Nodes:      nodes,
		ObservedAt: time.Date(2026, 8, 26, 8, 0, 0, 0, time.UTC),
	}
	clockNodes := []string{"node-a", "node-b", "node-c", "node-d", "node-e", "node-f", "gateway-node", "epp-node", "control-node"}
	clockImage := binding.Controller.Image
	binding.ClockAttestation = ClockAttestationBinding{Image: clockImage, Samples: 5, MaximumRTTNanoseconds: int64(250 * time.Millisecond), MaximumOffsetNanoseconds: int64(100 * time.Millisecond)}
	for index, nodeName := range clockNodes {
		binding.ClockAttestation.Probes = append(binding.ClockAttestation.Probes, ClockProbeBinding{
			NodeName: nodeName, NodeUID: fmt.Sprintf("clock-node-uid-%02d", index),
			Pod:         PodBinding{Name: fmt.Sprintf("clock-%02d", index), UID: fmt.Sprintf("clock-pod-uid-%02d", index), NodeName: nodeName, Image: clockImage},
			Observation: clockbound.Observation{SchemaVersion: clockbound.ObservationSchemaVersion, Samples: 5, BestRTTNanoseconds: int64(time.Millisecond), OffsetMinimumNanoseconds: -int64(time.Millisecond), OffsetMaximumNanoseconds: int64(time.Millisecond), MaximumRTTNanoseconds: int64(250 * time.Millisecond), MaximumOffsetNanoseconds: int64(100 * time.Millisecond), ObservedAt: time.Date(2026, 8, 26, 8, 0, 0, index, time.UTC)},
		})
	}
	return binding
}

func writePreflightBindingForTest(t *testing.T, path string, binding PreflightBinding) {
	t.Helper()
	encoded, err := binding.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

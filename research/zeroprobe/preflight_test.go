package zeroprobe

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JDinSeattle/velaserve/internal/bench"
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
	leftHash, err := left.InvariantSHA256()
	if err != nil {
		t.Fatal(err)
	}
	rightHash, err := right.InvariantSHA256()
	if err != nil {
		t.Fatal(err)
	}
	if leftHash != rightHash {
		t.Fatal("observation time changed the deployment invariant")
	}
	right.Model.Pods = append([]PodBinding(nil), right.Model.Pods...)
	right.Model.Pods[0].UID = "different-uid"
	rightHash, err = right.InvariantSHA256()
	if err != nil {
		t.Fatal(err)
	}
	if leftHash == rightHash {
		t.Fatal("pod identity drift did not change the deployment invariant")
	}
}

func validPreflightBinding() PreflightBinding {
	modelPods := make([]PodBinding, 6)
	for index := range modelPods {
		modelPods[index] = PodBinding{Name: "model-" + string(rune('a'+index)), UID: "model-uid-" + string(rune('a'+index)), Image: "123456789012.dkr.ecr.us-west-2.amazonaws.com/model@sha256:" + strings.Repeat("1", 64)}
	}
	return PreflightBinding{
		SchemaVersion:         PreflightBindingSchemaVersion,
		RepositoryCommit:      strings.Repeat("a", 40),
		AWSAccountID:          "123456789012",
		AWSRegion:             "us-west-2",
		ClusterARN:            "arn:aws:eks:us-west-2:123456789012:cluster/velaserve-z0",
		Namespace:             "velaserve-z0",
		Endpoint:              "https://gateway.example/v1/chat/completions",
		GatewayUID:            "gateway-uid",
		HTTPRouteUID:          "route-uid",
		PreregistrationSHA256: strings.Repeat("b", 64),
		CalibrationSHA256:     strings.Repeat("c", 64),
		ActiveArm:             evidence.ArmLoadAwareP2P,
		Transport:             "tcp",
		Model: ModelDeploymentBinding{
			Selector: "app=model", Container: "model", Workload: "model", ID: "Qwen/Qwen3-8B",
			Revision: strings.Repeat("d", 40), Image: modelPods[0].Image, MinimumReplicas: 6, MaximumReplicas: 8, Pods: modelPods,
		},
		EPP: EPPDeploymentBinding{
			Selector: "llm-d-router-gateway=velaserve-epp", Container: "epp", Replicas: 1,
			Image: "123456789012.dkr.ecr.us-west-2.amazonaws.com/epp@sha256:" + strings.Repeat("2", 64),
			Pods:  []PodBinding{{Name: "epp-a", UID: "epp-uid-a", Image: "123456789012.dkr.ecr.us-west-2.amazonaws.com/epp@sha256:" + strings.Repeat("2", 64)}},
		},
		Controller: ControllerDeploymentBinding{
			Selector: "app=controller", Container: "condition-controller", Revision: strings.Repeat("a", 40),
			Image: "123456789012.dkr.ecr.us-west-2.amazonaws.com/velaserve@sha256:" + strings.Repeat("3", 64),
			Pods:  []PodBinding{{Name: "controller-a", UID: "controller-uid-a", Image: "123456789012.dkr.ecr.us-west-2.amazonaws.com/velaserve@sha256:" + strings.Repeat("3", 64)}},
		},
		Nodes:      []NodeBinding{{Name: "node-a", UID: "node-uid-a", InstanceType: "g6e.xlarge", GPUAllocatable: 1}},
		ObservedAt: time.Date(2026, 8, 26, 8, 0, 0, 0, time.UTC),
	}
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

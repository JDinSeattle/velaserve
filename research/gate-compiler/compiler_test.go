package gatecompiler

import (
	"strings"
	"testing"

	"github.com/JDinSeattle/velaserve/internal/evidence"
	gate "github.com/JDinSeattle/velaserve/research/gate-decision"
	zeroprobe "github.com/JDinSeattle/velaserve/research/zeroprobe"
)

func TestVerifyPlacementMatrixRejectsMissingPreregisteredCell(t *testing.T) {
	current := bundle{root: "testdata/placement", evidence: []evidence.GateEvidenceCell{{
		FanoutWidth: 2, LoadRegime: evidence.LoadModerate, PrefixRegime: "near", Transport: "tcp",
		Metric: gate.PlacementMetric, Pairs: 20, Improvement: evidence.ConfidenceInterval{Estimate: .1, Lower: .05, Upper: .15},
	}}}
	if err := verifyPlacementMatrix(current); err == nil {
		t.Fatal("verifyPlacementMatrix() accepted a partial matrix")
	}
}

func TestVerifyBundleBindingsRejectsDeploymentDrift(t *testing.T) {
	binding, err := zeroprobe.LoadPreflightBinding("../../internal/evidence/testdata/preflight-valid.json")
	if err != nil {
		t.Fatal(err)
	}
	drifted := binding
	drifted.EPP.Pods = append([]zeroprobe.PodBinding(nil), binding.EPP.Pods...)
	drifted.EPP.Pods[0].UID = "different-epp-pod"
	err = verifyBundleBindings([]bundle{
		{manifest: zeroprobe.Manifest{RunID: "z0-a"}, binding: binding},
		{manifest: zeroprobe.Manifest{RunID: "z0-b"}, binding: drifted},
	})
	if err == nil || !strings.Contains(err.Error(), "immutable deployment") {
		t.Fatalf("verifyBundleBindings() error = %v", err)
	}
}

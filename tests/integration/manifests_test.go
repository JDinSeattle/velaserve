package integration

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestStageOneChartContainsNoCoordinationStoreOrSimulatorHeaders(t *testing.T) {
	rendered := renderStageOne(t, "arm-b-values.yaml")
	lower := strings.ToLower(rendered)
	for _, forbidden := range []string{"valkey", "redis", "velaserve_placement_enabled", "x-sim-", "p2p-source-budget", "dispatch-wave"} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("Stage 1 manifest contains forbidden pre-gate mechanism %q", forbidden)
		}
	}
	if !strings.Contains(rendered, "failureMode: FailOpen") {
		t.Fatal("upstream inference pool does not retain FailOpen behavior")
	}
	if !strings.Contains(rendered, "allow-experimental-plugins: true") {
		t.Fatal("the preregistered upstream P2P plugin is not explicitly enabled")
	}
}

func TestArmsPinSameImagesAndDifferOnlyByRoutingProfile(t *testing.T) {
	repository := repositoryRoot(t)
	armA := readYAMLMap(t, filepath.Join(repository, "deploy", "experiments", "arm-a-values.yaml"))
	armB := readYAMLMap(t, filepath.Join(repository, "deploy", "experiments", "arm-b-values.yaml"))
	if armA["routingProfile"] == armB["routingProfile"] {
		t.Fatalf("routing profiles are not distinct: %v", armA["routingProfile"])
	}
	delete(armA, "routingProfile")
	delete(armB, "routingProfile")
	if !reflect.DeepEqual(armA, armB) {
		t.Fatalf("arm values differ outside routingProfile:\nA=%#v\nB=%#v", armA, armB)
	}
	for _, arm := range []string{"arm-a-values.yaml", "arm-b-values.yaml"} {
		rendered := strings.ToLower(renderStageOne(t, arm))
		for _, floating := range []string{":main", ":latest", ":dev"} {
			if strings.Contains(rendered, floating) {
				t.Fatalf("%s contains floating image reference %s", arm, floating)
			}
		}
	}
}

func TestRealGPUValuesRouteOnlyToTheDeclaredModelService(t *testing.T) {
	repository := repositoryRoot(t)
	helm := findHelm(t, repository)
	command := exec.Command(helm,
		"template", "velaserve", filepath.Join(repository, "deploy", "helm", "velaserve"),
		"--namespace", "velaserve-z0",
		"-f", filepath.Join(repository, "deploy", "experiments", "aws-z0-values.yaml"),
		"--set", "images.velaserve.digest=sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"--set-string", "images.upstreamEPP.tag=git-ab723b898f8598ab6631e9848a4cf28accd9b9ea@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, output)
	}
	rendered := string(output)
	for _, required := range []string{
		"url: http://velaserve-model.velaserve-z0.svc.cluster.local:8000",
		"number: 8000",
		"app.kubernetes.io/name: velaserve-model",
	} {
		if !strings.Contains(rendered, required) {
			t.Fatalf("real-GPU render is missing %q", required)
		}
	}
	if strings.Contains(rendered, "velaserve-simfleet") {
		t.Fatal("real-GPU render still targets the local simulator")
	}
}

func TestRealGPUValuesRequireDigestAddressedEPP(t *testing.T) {
	repository := repositoryRoot(t)
	helm := findHelm(t, repository)
	command := exec.Command(helm,
		"template", "velaserve", filepath.Join(repository, "deploy", "helm", "velaserve"),
		"--namespace", "velaserve-z0",
		"-f", filepath.Join(repository, "deploy", "experiments", "aws-z0-values.yaml"),
		"--set", "images.velaserve.digest=sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatal("real-GPU render accepted a tag-only upstream EPP image")
	}
	if !strings.Contains(string(output), "real_gpu evidence requires a digest-addressed upstream EPP image") {
		t.Fatalf("unexpected validation failure: %s", output)
	}
}

func renderStageOne(t *testing.T, arm string) string {
	t.Helper()
	repository := repositoryRoot(t)
	helm := findHelm(t, repository)
	command := exec.Command(helm,
		"template", "velaserve", filepath.Join(repository, "deploy", "helm", "velaserve"),
		"--namespace", "velaserve-z0",
		"-f", filepath.Join(repository, "deploy", "experiments", "kind-values.yaml"),
		"-f", filepath.Join(repository, "deploy", "experiments", arm),
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, output)
	}
	return string(output)
}

func findHelm(t *testing.T, repository string) string {
	t.Helper()
	if configured := os.Getenv("HELM"); configured != "" {
		return configured
	}
	local := filepath.Join(repository, ".tools", "bin", "helm")
	if info, err := os.Stat(local); err == nil && info.Mode().IsRegular() {
		return local
	}
	path, err := exec.LookPath("helm")
	if err != nil {
		t.Fatal("helm is required; run hack/bootstrap-tools.sh")
	}
	return path
}

func readYAMLMap(t *testing.T, path string) map[string]any {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	decoder.KnownFields(false)
	value := map[string]any{}
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

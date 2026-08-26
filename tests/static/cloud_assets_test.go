package static

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestTerraformDefaultsAreScaleToZeroAndBenchmarkSafe(t *testing.T) {
	variables := readRepositoryFile(t, "infra", "terraform", "variables.tf")
	assertVariableDefault(t, variables, "gpu_desired_size", "0")
	assertVariableDefault(t, variables, "gpu_minimum_benchmark_replicas", "6")
	for _, mechanism := range []string{"valkey", "redis", "velaserve_placement_enabled", "p2p-source-budget", "dispatch-wave"} {
		if strings.Contains(strings.ToLower(readRepositoryFile(t, "infra", "terraform", "main.tf")), mechanism) {
			t.Fatalf("Terraform contains forbidden pre-gate mechanism %q", mechanism)
		}
	}
}

func TestCloudScriptsNeverWrapTerraformApplyAndPreflightRealEvidence(t *testing.T) {
	preflight := readRepositoryFile(t, "hack", "cloud-preflight.sh")
	for _, required := range []string{
		"aws sts get-caller-identity",
		"aws service-quotas get-service-quota",
		"VELASERVE_GPU_MINIMUM_BENCHMARK_REPLICAS",
		"VELASERVE_MODEL_REVISION",
		"VELASERVE_PREREGISTRATION_SHA256",
		"@sha256:",
		"kubectl config current-context",
	} {
		if !strings.Contains(preflight, required) {
			t.Fatalf("cloud preflight is missing %q", required)
		}
	}
	for _, name := range []string{"cloud-preflight.sh", "cloud-run-z0.sh", "cloud-collect.sh", "cloud-cleanup.sh"} {
		script := strings.ToLower(readRepositoryFile(t, "hack", name))
		if strings.Contains(script, "terraform apply") || strings.Contains(script, "terraform destroy") {
			t.Fatalf("%s hides an infrastructure mutation", name)
		}
	}
}

func TestCleanupRequiresExactClusterArtifactAndTypedConfirmation(t *testing.T) {
	cleanup := readRepositoryFile(t, "hack", "cloud-cleanup.sh")
	for _, required := range []string{
		"VELASERVE_CLUSTER_NAME",
		"VELASERVE_ARTIFACTS_COLLECTED=true",
		"VELASERVE_CONFIRM_CLEANUP",
		"kubectl config current-context",
	} {
		if !strings.Contains(cleanup, required) {
			t.Fatalf("cloud cleanup is missing %q", required)
		}
	}
}

func TestAWSZ0ValuesRequireRealGPUAndSixReplicas(t *testing.T) {
	values := readRepositoryFile(t, "deploy", "experiments", "aws-z0-values.yaml")
	for _, required := range []string{"evidenceScope: real_gpu", "replicas: 6", "enabled: false"} {
		if !strings.Contains(values, required) {
			t.Fatalf("AWS Z0 values are missing %q", required)
		}
	}
	if strings.Contains(strings.ToLower(values), "x-sim") {
		t.Fatal("AWS Z0 values contain simulator-only headers")
	}
}

func assertVariableDefault(t *testing.T, contents, name, expected string) {
	t.Helper()
	pattern := regexp.MustCompile(`(?s)variable\s+"` + regexp.QuoteMeta(name) + `"\s*\{.*?default\s*=\s*([^\s\n]+)`)
	match := pattern.FindStringSubmatch(contents)
	if len(match) != 2 {
		t.Fatalf("variable %s has no parseable default", name)
	}
	if match[1] != expected {
		t.Fatalf("variable %s default = %s, want %s", name, match[1], expected)
	}
}

func readRepositoryFile(t *testing.T, parts ...string) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate repository")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	contents, err := os.ReadFile(filepath.Join(append([]string{root}, parts...)...))
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

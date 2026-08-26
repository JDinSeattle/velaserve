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
		"VELASERVE_CONDITION_DRIVER_CONFIGMAP_SELECTOR",
		"driver_config_sha256",
		"--enable-prefix-caching",
		"--enable-prompt-tokens-details",
		"--enable-request-id-headers",
		"--enable-tokenizer-info-endpoint",
		"OffloadingConnector",
		"VELASERVE_ROUTING_SIDECAR_IMAGE",
		"driver_load_profiles_sha256",
		"control_secret_sha256",
		"VELASERVE_CONDITION_CONTROL_TOKEN",
		"VELASERVE_CONDITION_APPLY_TIMEOUT_SECONDS",
		"profile-freeze verify-live",
		"kubectl --namespace \"$VELASERVE_NAMESPACE\" port-forward",
		"tokenizer_verified_pod_uids",
		"VELASERVE_PROFILE_CALIBRATION_SHA256",
		"VELASERVE_BENCHMARK_PROFILE",
		"gatewayclass_json",
		"@sha256:",
		"kubectl config current-context",
	} {
		if !strings.Contains(preflight, required) {
			t.Fatalf("cloud preflight is missing %q", required)
		}
	}
	if strings.Contains(preflight, "VELASERVE_TOKENIZE_ENDPOINT") || strings.Contains(preflight, "VELASERVE_TOKENIZER_INFO_ENDPOINT") {
		t.Fatal("cloud preflight must verify every exact model pod, not caller-provided tokenizer URLs")
	}
	for _, name := range []string{"cloud-preflight.sh", "cloud-run-z0.sh", "cloud-export-epp-logs.sh", "cloud-export-envoy-logs.sh", "cloud-export-vllm-runtime.sh", "cloud-collect.sh", "cloud-cleanup.sh"} {
		script := strings.ToLower(readRepositoryFile(t, "hack", name))
		if strings.Contains(script, "terraform apply") || strings.Contains(script, "terraform destroy") {
			t.Fatalf("%s hides an infrastructure mutation", name)
		}
	}
}

func TestEveryHandoffScriptIsExecutable(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate repository")
	}
	paths, err := filepath.Glob(filepath.Join(filepath.Dir(source), "..", "..", "hack", "*.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no handoff scripts found")
	}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Errorf("%s is not executable", filepath.Base(path))
		}
	}
}

func TestGatewayShipsCorrelatableAccessLogsAndExporter(t *testing.T) {
	gateway := readRepositoryFile(t, "deploy", "gateway", "httproute.yaml")
	for _, required := range []string{"kind: EnvoyProxy", "envoyService:", "type: ClusterIP", "type: JSON", "REQ(X-REQUEST-ID)", "REQ(X-VELA-FANOUT-GROUP)", "UPSTREAM_HOST", "RESPONSE_CODE", "DURATION", "path: /dev/stdout"} {
		if !strings.Contains(gateway, required) {
			t.Fatalf("gateway access-log contract is missing %q", required)
		}
	}
	routerPatch := readRepositoryFile(t, "deploy", "upstream-patches", "llm-d-router-stage1-observer.patch")
	for _, required := range []string{"access_log", "REQ(X-REQUEST-ID)", "REQ(X-VELA-FANOUT-GROUP)", "UPSTREAM_HOST", "RESPONSE_CODE", "DURATION", "/dev/stdout"} {
		if !strings.Contains(routerPatch, required) {
			t.Fatalf("inner EPP Envoy access-log patch is missing %q", required)
		}
	}
	exporter := readRepositoryFile(t, "hack", "cloud-export-envoy-logs.sh")
	for _, required := range []string{"--since-time", "VELASERVE_EPP_SELECTOR", "VELASERVE_EPP_REPLICAS", "VELASERVE_EPP_PROXY_CONTAINER_NAME", "restartCount == 0", "REQ(X-VELA-FANOUT-GROUP)", `-c "$VELASERVE_EPP_PROXY_CONTAINER_NAME"`} {
		if !strings.Contains(exporter, required) {
			t.Fatalf("Envoy exporter is missing %q", required)
		}
	}
	if strings.Contains(exporter, "--all-namespaces") || strings.Contains(exporter, "owning-gateway-name") {
		t.Fatal("Envoy evidence exporter still targets the outer gateway instead of the EPP's inner Envoy")
	}
}

func TestObservabilityQueriesMatchLedgeredSnapshotProducers(t *testing.T) {
	dashboard := readRepositoryFile(t, "deploy", "observability", "grafana", "velaserve.json")
	rules := readRepositoryFile(t, "deploy", "observability", "prometheus-rules.yaml")
	producer := readRepositoryFile(t, "internal", "metrics", "analysis.go")
	verifier := readRepositoryFile(t, "research", "zeroprobe", "orchestrator.go")
	for _, metric := range []string{
		"velaserve_placement_target_total",
		"velaserve_oracle_regret_seconds",
		"velaserve_dispatch_to_epp_observation_seconds",
		"velaserve_ordinary_traffic_latency_seconds",
	} {
		if !strings.Contains(dashboard, metric) || !strings.Contains(producer, metric) {
			t.Fatalf("dashboard metric %q lacks an exact analysis producer", metric)
		}
	}
	if !strings.Contains(dashboard, "velaserve_artifact_complete") || !strings.Contains(verifier, "velaserve_artifact_complete 1") {
		t.Fatal("artifact-complete panel lacks its post-verification producer")
	}
	if strings.Contains(dashboard, "rate(") || strings.Contains(rules, "rate(") {
		t.Fatal("run-scoped snapshot metrics cannot use live-series rate() queries")
	}
}

func TestCloudCollectionReattestsDeploymentBeforeIngestAndJoinsPlacements(t *testing.T) {
	collection := readRepositoryFile(t, "hack", "cloud-collect.sh")
	preflight := strings.Index(collection, `"${REPOSITORY_ROOT}/hack/cloud-preflight.sh"`)
	ingest := strings.Index(collection, "go run ./cmd/zeroprobe ingest")
	compile := strings.Index(collection, "go run ./cmd/source-pressure-compile")
	if preflight < 0 || ingest < 0 || compile < 0 || !(preflight < ingest && ingest < compile) {
		t.Fatalf("cloud collection order must be end-preflight -> ingest -> source compile")
	}
	for _, required := range []string{"--placements", "preflight-binding.json", "hash-preflight", "invariant_sha256", "list-objects-v2", "get-object", "ledger_version_id", "expected_sha256", "expected_bytes"} {
		if !strings.Contains(collection, required) {
			t.Fatalf("cloud collection is missing %q", required)
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
		"aws sts get-caller-identity",
		"aws eks describe-cluster",
		"preflight-binding.json",
		".artifacts-collected",
		"ledger_version_id",
		"--version-id",
	} {
		if !strings.Contains(cleanup, required) {
			t.Fatalf("cloud cleanup is missing %q", required)
		}
	}
}

func TestCloudScriptsDeclareEveryNonShellRuntimeDependency(t *testing.T) {
	checks := map[string][]string{
		"cloud-run-z0.sh":              {"command -v go"},
		"cloud-export-epp-logs.sh":     {"for command_name in kubectl jq", "--since-time", ".items[].metadata.name"},
		"cloud-export-envoy-logs.sh":   {"for command_name in kubectl jq", "--since-time", "VELASERVE_EPP_SELECTOR", "VELASERVE_EPP_PROXY_CONTAINER_NAME"},
		"cloud-export-vllm-runtime.sh": {"for command_name in kubectl jq", "--since-time", "VELASERVE_VLLM_RUNTIME"},
		"cloud-collect.sh":             {"for command_name in aws kubectl jq go shasum curl find"},
		"cloud-cleanup.sh": {
			"readonly HELM=\"${HELM:-${REPOSITORY_ROOT}/.tools/bin/helm}\"",
			"for command_name in aws kubectl jq shasum",
			"\"$HELM\" --namespace",
		},
	}
	for script, required := range checks {
		contents := readRepositoryFile(t, "hack", script)
		for _, text := range required {
			if !strings.Contains(contents, text) {
				t.Fatalf("%s does not declare runtime dependency %q", script, text)
			}
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

func TestConditionDriverIsBuiltAndDeployableFromTheSupportChart(t *testing.T) {
	for file, required := range map[string][]string{
		"Dockerfile": {
			"-o /out/condition-driver ./cmd/condition-driver",
		},
		filepath.Join("deploy", "kind", "Dockerfile.velaserve"): {
			"COPY condition-driver /app/condition-driver",
		},
		filepath.Join("deploy", "helm", "velaserve", "templates", "condition-driver.yaml"): {
			"/app/condition-driver",
			"VELASERVE_DRIVER_ENDPOINTS_FILE",
			"velaserve-condition-driver",
			"checksum/condition-driver-config",
			"VELASERVE_DRIVER_LOAD_PROFILES_FILE",
			"VELASERVE_CONDITION_CONTROL_TOKEN",
		},
	} {
		contents := readRepositoryFile(t, strings.Split(file, string(filepath.Separator))...)
		for _, text := range required {
			if !strings.Contains(contents, text) {
				t.Fatalf("%s is missing %q", file, text)
			}
		}
	}
}

func TestModelNetworkPolicyAllowsEPPKVEventSubscription(t *testing.T) {
	template := readRepositoryFile(t, "deploy", "model-runtime", "templates", "runtime.yaml")
	preflight := readRepositoryFile(t, "hack", "cloud-preflight.sh")
	for _, required := range []string{"llm-d-router-gateway", ".Values.offloading.proxyPort", ".Values.offloading.kvEventsPort"} {
		if !strings.Contains(template, required) {
			t.Fatalf("model NetworkPolicy omits %q", required)
		}
	}
	if !strings.Contains(preflight, ".port == 5556") || !strings.Contains(preflight, `podSelector.matchLabels["llm-d-router-gateway"] == "velaserve-epp"`) {
		t.Fatal("cloud preflight does not attest EPP access to vLLM KV-event port 5556")
	}
}

func TestPinnedModelRuntimeIsBuildableRenderedAndCollected(t *testing.T) {
	for file, required := range map[string][]string{
		filepath.Join("hack", "build-pinned-vllm.sh"): {
			"b1fbbc2ade51e3826bc92e4733c9c692ee21d42d", "vllm-stage1-runtime-observer.patch", "--target vllm-openai",
		},
		filepath.Join("deploy", "model-runtime", "templates", "runtime.yaml"): {
			"OffloadingConnector", "TieringOffloadingSpec", "--enable-request-id-headers", "--enable-tokenizer-info-endpoint", "--kv-events-config", "--kv-connector=offloading", "PYTHONHASHSEED", "VLLM_P2P_SIDE_CHANNEL_HOST", "VELASERVE_VLLM_OBSERVER_VERSION", "UCX_TLS", "nvidia.com/gpu",
		},
		filepath.Join("deploy", "model-runtime", "values.yaml"): {"3500m"},
		filepath.Join("hack", "cloud-collect.sh"): {
			"VELASERVE_VLLM_RAW_PATH", "go run ./cmd/vllm-normalize", "go run ./cmd/p2p-runtime-normalize", "vllm-raw.log", "vllm-runtime.jsonl", "p2p-transfers.jsonl",
		},
		"Dockerfile": {"/out/p2p-runtime-normalize ./cmd/p2p-runtime-normalize"},
	} {
		contents := readRepositoryFile(t, strings.Split(file, string(filepath.Separator))...)
		for _, text := range required {
			if !strings.Contains(contents, text) {
				t.Fatalf("%s is missing %q", file, text)
			}
		}
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

#!/usr/bin/env bash
set -euo pipefail

readonly REPOSITORY_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly CHART="${REPOSITORY_ROOT}/deploy/helm/velaserve"
readonly MODEL_CHART="${REPOSITORY_ROOT}/deploy/model-runtime"
readonly HELM="${REPOSITORY_ROOT}/.tools/bin/helm"

if [[ ! -x "$HELM" ]]; then
  "${REPOSITORY_ROOT}/hack/bootstrap-tools.sh"
fi

temporary_directory="$(mktemp -d)"
trap 'rm -rf "$temporary_directory"' EXIT

for arm in arm-a-values.yaml arm-b-values.yaml; do
  "$HELM" lint "$CHART" \
    -f "${REPOSITORY_ROOT}/deploy/experiments/kind-values.yaml" \
    -f "${REPOSITORY_ROOT}/deploy/experiments/${arm}"
  output="${temporary_directory}/${arm}"
  "$HELM" template velaserve-support "$CHART" \
    --namespace velaserve-z0 \
    -f "${REPOSITORY_ROOT}/deploy/experiments/kind-values.yaml" \
    -f "${REPOSITORY_ROOT}/deploy/experiments/${arm}" >"$output"
  if grep -Eqi 'valkey|redis|velaserve_placement_enabled|x-sim-|p2p-source-budget|dispatch-wave' "$output"; then
    echo "$arm contains a forbidden pre-gate mechanism" >&2
    exit 1
  fi
  if grep -Eqi 'image: [^[:space:]]+:(main|latest|dev)([[:space:]]|$)' "$output"; then
    echo "$arm contains a floating runtime image" >&2
    exit 1
  fi
  grep -q 'failureMode: FailOpen' "$output"
  grep -Fq 'type: p2p-source-producer' "$output"
  if [[ "$arm" == "arm-a-values.yaml" ]]; then
    grep -Fq 'type: prefix-cache-scorer' "$output"
    grep -Fq 'type: max-score-picker' "$output"
  else
    grep -Fq 'type: load-aware-scorer' "$output"
    grep -Fq 'threshold: 128' "$output"
    grep -Fq 'type: max-score-picker' "$output"
    if grep -Eq 'type: (weighted-random-picker|queue-scorer|kv-cache-utilization-scorer|prefix-cache-scorer)' "$output"; then
      echo "$arm does not render the frozen upstream load-aware comparator" >&2
      exit 1
    fi
  fi
done

driver_output="${temporary_directory}/condition-driver.yaml"
load_profiles='[{"prefix_tokens":1024,"max_tokens":32,"saturation_qps":100,"measurement_source":"fixture","calibration_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},{"prefix_tokens":1024,"max_tokens":128,"saturation_qps":80,"measurement_source":"fixture","calibration_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},{"prefix_tokens":4096,"max_tokens":32,"saturation_qps":60,"measurement_source":"fixture","calibration_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},{"prefix_tokens":4096,"max_tokens":128,"saturation_qps":40,"measurement_source":"fixture","calibration_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},{"prefix_tokens":8192,"max_tokens":32,"saturation_qps":30,"measurement_source":"fixture","calibration_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},{"prefix_tokens":8192,"max_tokens":128,"saturation_qps":20,"measurement_source":"fixture","calibration_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]'
"$HELM" template velaserve-support "$CHART" \
  --namespace velaserve-z0 \
  -f "${REPOSITORY_ROOT}/deploy/experiments/aws-z0-values.yaml" \
  -f "${REPOSITORY_ROOT}/deploy/experiments/arm-b-values.yaml" \
  --set-string images.velaserve.digest=sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa \
  --set-string images.upstreamEPP.tag=git-ab723b898f8598ab6631e9848a4cf28accd9b9ea@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb \
  --set model.minCachedTokenDelta=128 \
  --set conditionDriver.enabled=true \
  --set-string conditionDriver.model=fixture-model \
  --set-string conditionDriver.gatewayChatURL=http://fixture-gateway/v1/chat/completions \
  --set-json "conditionDriver.loadProfiles=${load_profiles}" \
  --set-json 'conditionDriver.endpoints=[{"id":"model-0","chat_url":"http://model-0:8200/v1/chat/completions","reset_url":"http://model-0:8200/reset_prefix_cache","metrics_url":"http://model-0:8200/metrics"}]' \
  --set conditionController.enabled=true \
  --set-string conditionController.driverURL=http://velaserve-condition-driver:8083/v1/conditions/apply \
  --set-string conditionController.revision=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa \
  --set-string conditionControlSecret.name=velaserve-condition-control >"$driver_output"
grep -Fq 'command: ["/app/condition-driver"]' "$driver_output"
grep -Fq 'path: /healthz' "$driver_output"
grep -Fq 'endpoints.json:' "$driver_output"
grep -Fq 'load-profiles.json:' "$driver_output"
grep -Fq 'checksum/condition-driver-config:' "$driver_output"
grep -Fq 'name: VELASERVE_CONDITION_CONTROL_TOKEN' "$driver_output"

model_output="${temporary_directory}/model-runtime.yaml"
"$HELM" lint "$MODEL_CHART" \
  --set-string model.revision=dddddddddddddddddddddddddddddddddddddddd \
  --set-string images.vllm.repository=example.invalid/vllm \
  --set-string images.vllm.digest=sha256:1111111111111111111111111111111111111111111111111111111111111111 \
  --set-string images.routingSidecar.repository=example.invalid/sidecar \
  --set-string images.routingSidecar.digest=sha256:2222222222222222222222222222222222222222222222222222222222222222
"$HELM" template velaserve-model "$MODEL_CHART" \
  --namespace velaserve-z0 \
  --set-string model.revision=dddddddddddddddddddddddddddddddddddddddd \
  --set-string images.vllm.repository=example.invalid/vllm \
  --set-string images.vllm.digest=sha256:1111111111111111111111111111111111111111111111111111111111111111 \
  --set-string images.routingSidecar.repository=example.invalid/sidecar \
  --set-string images.routingSidecar.digest=sha256:2222222222222222222222222222222222222222222222222222222222222222 >"$model_output"
for required in OffloadingConnector TieringOffloadingSpec --enable-prefix-caching --enable-prompt-tokens-details --enable-request-id-headers --enable-tokenizer-info-endpoint --kv-events-config --kv-connector=offloading VLLM_P2P_SIDE_CHANNEL_HOST PYTHONHASHSEED VELASERVE_VLLM_OBSERVER_VERSION UCX_TLS nvidia.com/gpu 3500m; do
  grep -Fq -- "$required" "$model_output"
done

cd "$REPOSITORY_ROOT"
env \
  HELM="$HELM" \
  GOCACHE="${REPOSITORY_ROOT}/.cache/go-build" \
  GOMODCACHE="${REPOSITORY_ROOT}/.cache/go-mod" \
  go test ./tests/integration -run 'TestStageOneChart|TestArmsPin' -count=1
echo "stage-one Helm render verified for both frozen arms and the real-GPU condition path"

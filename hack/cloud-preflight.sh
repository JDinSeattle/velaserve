#!/usr/bin/env bash
set -euo pipefail

readonly REPOSITORY_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly PREREGISTRATION_PATH="${VELASERVE_PREREGISTRATION_PATH:-${REPOSITORY_ROOT}/research/preregistration/z0-v1.yaml}"
readonly CLOCK_PROBE_SELECTOR="app.kubernetes.io/name=velaserve-clock-probe"
readonly CLOCK_PROBE_CONTAINER="clock-probe"
readonly CLOCK_PROBE_REMOTE_PORT=8084
readonly CLOCK_PROBE_SAMPLES=5
readonly CLOCK_PROBE_MAX_RTT="250ms"
readonly CLOCK_PROBE_MAX_OFFSET="100ms"
readonly CLOCK_PROBE_MAX_RTT_NANOSECONDS=250000000
readonly CLOCK_PROBE_MAX_OFFSET_NANOSECONDS=100000000

fail() {
  echo "cloud-preflight: $*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 is required"
}

require_env() {
  local name="$1"
  [[ -n "${!name:-}" ]] || fail "$name is required"
}

for command_name in aws kubectl jq git shasum curl go docker; do
  require_command "$command_name"
done
for variable_name in \
  VELASERVE_AWS_ACCOUNT_ID \
  VELASERVE_AWS_REGION \
  VELASERVE_CLUSTER_NAME \
  VELASERVE_NAMESPACE \
  VELASERVE_ARTIFACT_BUCKET \
  VELASERVE_GPU_QUOTA_CODE \
  VELASERVE_GPU_QUOTA_REQUIRED \
  VELASERVE_GPU_MINIMUM_BENCHMARK_REPLICAS \
  VELASERVE_GPU_MAXIMUM_BENCHMARK_REPLICAS \
  VELASERVE_GPU_DRIVER_VERSION \
  VELASERVE_MODEL_SELECTOR \
  VELASERVE_MODEL_CONTAINER_NAME \
  VELASERVE_MODEL_WORKLOAD_NAME \
  VELASERVE_MODEL_RELEASE_NAME \
  VELASERVE_MODEL_ID \
  VELASERVE_MODEL_REVISION \
  VELASERVE_MODEL_IMAGE \
  VELASERVE_BENCHMARK_PROFILE \
  VELASERVE_PROFILE_CALIBRATION \
  VELASERVE_CROSSOVER_BUNDLE \
  VELASERVE_LOAD_CALIBRATION \
  VELASERVE_PROFILE_CALIBRATION_SHA256 \
  VELASERVE_Z0_PHASE \
  VELASERVE_MODEL_SERVICE_NAME \
  VELASERVE_ROUTING_SIDECAR_CONTAINER_NAME \
  VELASERVE_ROUTING_SIDECAR_IMAGE \
  VELASERVE_CPU_OFFLOAD_BYTES \
  VELASERVE_EPP_SELECTOR \
  VELASERVE_EPP_CONTAINER_NAME \
  VELASERVE_EPP_PROXY_CONTAINER_NAME \
  VELASERVE_EPP_PROXY_IMAGE \
  VELASERVE_ACTIVE_ARM \
  VELASERVE_EPP_REPLICAS \
  VELASERVE_CONDITION_CONTROLLER_SELECTOR \
  VELASERVE_CONDITION_CONTROLLER_CONTAINER_NAME \
  VELASERVE_CONTROLLER_REVISION \
  VELASERVE_CONDITION_APPLY_TIMEOUT_SECONDS \
  VELASERVE_CONDITION_CONTROLLER_ENDPOINT \
  VELASERVE_CONDITION_DRIVER_SELECTOR \
  VELASERVE_CONDITION_DRIVER_CONTAINER_NAME \
  VELASERVE_CONDITION_DRIVER_CONFIGMAP_SELECTOR \
  VELASERVE_CONDITION_DRIVER_ENDPOINT \
  VELASERVE_CONDITION_DRIVER_IMAGE \
  VELASERVE_CONDITION_DRIVER_DRAIN_TIMEOUT_SECONDS \
  VELASERVE_CONDITION_DRIVER_DRAIN_POLL_MILLISECONDS \
  VELASERVE_CONDITION_CONTROL_SECRET_NAME \
  VELASERVE_CONDITION_CONTROL_SECRET_KEY \
  VELASERVE_CONDITION_CONTROL_TOKEN \
  VELASERVE_GPU_INSTANCE_TYPES \
  VELASERVE_P2P_TRANSPORT \
  VELASERVE_ENDPOINT \
  VELASERVE_GATEWAY_CHAT_URL \
  VELASERVE_VELASERVE_IMAGE \
  VELASERVE_EPP_IMAGE \
  VELASERVE_ENVOY_CONTAINER_NAME \
  VELASERVE_ENVOY_IMAGE \
  VELASERVE_ENVOY_GATEWAY_CONTROLLER_SELECTOR \
  VELASERVE_ENVOY_GATEWAY_CONTROLLER_CONTAINER_NAME \
  VELASERVE_ENVOY_GATEWAY_CONTROLLER_IMAGE \
  VELASERVE_PREREGISTRATION_SHA256 \
  VELASERVE_ORACLE_CALIBRATION \
  VELASERVE_ORACLE_CALIBRATION_SHA256 \
  VELASERVE_INFLIGHT_PUBLICATION_DELAY_MS \
  VELASERVE_AFFINITY_LOAD_GATE_SECONDS; do
  require_env "$variable_name"
done

[[ "$VELASERVE_AWS_ACCOUNT_ID" =~ ^[0-9]{12}$ ]] || fail "VELASERVE_AWS_ACCOUNT_ID must be a 12-digit account"
[[ "$VELASERVE_GPU_MINIMUM_BENCHMARK_REPLICAS" =~ ^[0-9]+$ ]] || fail "GPU replica minimum must be an integer"
[[ "$VELASERVE_GPU_MAXIMUM_BENCHMARK_REPLICAS" =~ ^[0-9]+$ ]] || fail "GPU replica maximum must be an integer"
(( VELASERVE_GPU_MINIMUM_BENCHMARK_REPLICAS >= 6 )) || fail "real-GPU Z0 requires at least six homogeneous replicas"
(( VELASERVE_GPU_MAXIMUM_BENCHMARK_REPLICAS <= 8 && VELASERVE_GPU_MAXIMUM_BENCHMARK_REPLICAS >= VELASERVE_GPU_MINIMUM_BENCHMARK_REPLICAS )) || fail "GPU replica bounds must stay within the preregistered six-to-eight range"
[[ "$VELASERVE_GPU_QUOTA_REQUIRED" =~ ^[0-9]+([.][0-9]+)?$ ]] || fail "VELASERVE_GPU_QUOTA_REQUIRED must be numeric"
[[ "$VELASERVE_MODEL_ID" =~ ^[A-Za-z0-9._/-]+$ ]] || fail "VELASERVE_MODEL_ID contains unsupported characters"
[[ "$VELASERVE_MODEL_REVISION" =~ ^[A-Fa-f0-9]{40,64}$ ]] || fail "VELASERVE_MODEL_REVISION must be an immutable commit or content digest"
[[ "$VELASERVE_CONTROLLER_REVISION" =~ ^[a-f0-9]{40}$ ]] || fail "VELASERVE_CONTROLLER_REVISION must be the exact lowercase Git commit"
[[ "$VELASERVE_INFLIGHT_PUBLICATION_DELAY_MS" =~ ^[0-9]+([.][0-9]+)?$ ]] || fail "inflight publication delay must be finite and non-negative"
[[ "$VELASERVE_AFFINITY_LOAD_GATE_SECONDS" =~ ^[0-9]+([.][0-9]+)?$ ]] && [[ "$VELASERVE_AFFINITY_LOAD_GATE_SECONDS" != "0" && "$VELASERVE_AFFINITY_LOAD_GATE_SECONDS" != "0.0" ]] || fail "affinity load gate must be positive"
[[ "$VELASERVE_CONDITION_APPLY_TIMEOUT_SECONDS" =~ ^[0-9]+$ ]] && (( VELASERVE_CONDITION_APPLY_TIMEOUT_SECONDS >= 600 && VELASERVE_CONDITION_APPLY_TIMEOUT_SECONDS <= 3600 )) || fail "condition apply timeout must be 600..3600 seconds"
[[ "$VELASERVE_CONDITION_DRIVER_DRAIN_TIMEOUT_SECONDS" =~ ^[0-9]+$ ]] && (( VELASERVE_CONDITION_DRIVER_DRAIN_TIMEOUT_SECONDS >= 30 && VELASERVE_CONDITION_DRIVER_DRAIN_TIMEOUT_SECONDS <= 600 )) || fail "driver drain timeout must be 30..600 seconds"
[[ "$VELASERVE_CONDITION_DRIVER_DRAIN_POLL_MILLISECONDS" =~ ^[0-9]+$ ]] && (( VELASERVE_CONDITION_DRIVER_DRAIN_POLL_MILLISECONDS >= 50 && VELASERVE_CONDITION_DRIVER_DRAIN_POLL_MILLISECONDS <= 5000 && VELASERVE_CONDITION_DRIVER_DRAIN_POLL_MILLISECONDS < VELASERVE_CONDITION_DRIVER_DRAIN_TIMEOUT_SECONDS * 1000 )) || fail "driver drain poll interval must be 50..5000 ms below timeout"
[[ "$VELASERVE_MODEL_IMAGE" == *@sha256:* ]] || fail "VELASERVE_MODEL_IMAGE must contain @sha256:"
[[ "$VELASERVE_ROUTING_SIDECAR_IMAGE" == *@sha256:* ]] || fail "VELASERVE_ROUTING_SIDECAR_IMAGE must contain @sha256:"
[[ "$VELASERVE_CPU_OFFLOAD_BYTES" =~ ^[1-9][0-9]*$ ]] || fail "VELASERVE_CPU_OFFLOAD_BYTES must be a positive integer"
[[ "$VELASERVE_VELASERVE_IMAGE" == *@sha256:* ]] || fail "VELASERVE_VELASERVE_IMAGE must contain @sha256:"
[[ "$VELASERVE_EPP_IMAGE" == *@sha256:* ]] || fail "VELASERVE_EPP_IMAGE must contain @sha256:"
[[ "$VELASERVE_ENVOY_IMAGE" == *@sha256:* ]] || fail "VELASERVE_ENVOY_IMAGE must contain @sha256:"
[[ "$VELASERVE_ENVOY_GATEWAY_CONTROLLER_IMAGE" == *@sha256:* ]] || fail "VELASERVE_ENVOY_GATEWAY_CONTROLLER_IMAGE must contain @sha256:"
[[ "$VELASERVE_CONDITION_DRIVER_IMAGE" == *@sha256:* ]] || fail "VELASERVE_CONDITION_DRIVER_IMAGE must contain @sha256:"
case "$VELASERVE_ACTIVE_ARM" in arm-a-affinity-p2p|arm-b-load-aware-p2p) ;; *) fail "VELASERVE_ACTIVE_ARM is not a frozen upstream baseline" ;; esac
case "$VELASERVE_EPP_REPLICAS" in 1|2) ;; *) fail "VELASERVE_EPP_REPLICAS must be 1 or 2" ;; esac
[[ "$VELASERVE_P2P_TRANSPORT" == "tcp" ]] || fail "the Stage-1 runtime adapter currently attests only tcp transport"
case "$VELASERVE_Z0_PHASE" in z0-a|z0-b|z0-c) ;; *) fail "VELASERVE_Z0_PHASE must be z0-a, z0-b, or z0-c" ;; esac

actual_preregistration_hash="$(shasum -a 256 "$PREREGISTRATION_PATH" | awk '{print $1}')"
[[ "$actual_preregistration_hash" == "$VELASERVE_PREREGISTRATION_SHA256" ]] || fail "preregistration SHA-256 mismatch"
git -C "$REPOSITORY_ROOT" diff --quiet HEAD -- "${PREREGISTRATION_PATH#${REPOSITORY_ROOT}/}" || fail "preregistration differs from Git HEAD"
git -C "$REPOSITORY_ROOT" diff --quiet HEAD -- . || fail "tracked repository files differ from Git HEAD"
[[ -z "$(git -C "$REPOSITORY_ROOT" status --porcelain --untracked-files=normal)" ]] || fail "repository contains uncommitted or untracked non-ignored files"
repository_commit="$(git -C "$REPOSITORY_ROOT" rev-parse HEAD)"
[[ "$repository_commit" == "$VELASERVE_CONTROLLER_REVISION" ]] || fail "condition-controller revision must equal the checked-out repository commit"
[[ -f "$VELASERVE_ORACLE_CALIBRATION" && ! -L "$VELASERVE_ORACLE_CALIBRATION" ]] || fail "oracle calibration must be a regular file"
actual_calibration_hash="$(shasum -a 256 "$VELASERVE_ORACLE_CALIBRATION" | awk '{print $1}')"
[[ "$actual_calibration_hash" == "$VELASERVE_ORACLE_CALIBRATION_SHA256" ]] || fail "oracle calibration SHA-256 mismatch"
case "$VELASERVE_ORACLE_CALIBRATION" in
  "${REPOSITORY_ROOT}/"*)
    git -C "$REPOSITORY_ROOT" diff --quiet HEAD -- "${VELASERVE_ORACLE_CALIBRATION#${REPOSITORY_ROOT}/}" || fail "oracle calibration differs from Git HEAD"
    ;;
esac
[[ -f "$VELASERVE_BENCHMARK_PROFILE" && ! -L "$VELASERVE_BENCHMARK_PROFILE" ]] || fail "benchmark profile must be a regular file"
[[ -f "$VELASERVE_PROFILE_CALIBRATION" && ! -L "$VELASERVE_PROFILE_CALIBRATION" ]] || fail "profile calibration must be a regular file"
[[ -d "$VELASERVE_CROSSOVER_BUNDLE" && ! -L "$VELASERVE_CROSSOVER_BUNDLE" ]] || fail "crossover calibration bundle must be a real directory"
go -C "$REPOSITORY_ROOT" run ./cmd/crossover-compile --verify-bundle "$VELASERVE_CROSSOVER_BUNDLE" >/dev/null || fail "crossover calibration bundle does not replay from raw observations and vLLM logs"
jq -S -e --slurpfile evidence "$VELASERVE_CROSSOVER_BUNDLE/crossover-evidence.json" '.crossover_evidence == $evidence[0]' "$VELASERVE_PROFILE_CALIBRATION" >/dev/null || fail "profile calibration crossover evidence differs from the sealed raw bundle"
[[ -f "$VELASERVE_LOAD_CALIBRATION" && ! -L "$VELASERVE_LOAD_CALIBRATION" ]] || fail "load calibration must be a regular file"
actual_benchmark_profile_hash="$(shasum -a 256 "$VELASERVE_BENCHMARK_PROFILE" | awk '{print $1}')"
actual_profile_calibration_hash="$(shasum -a 256 "$VELASERVE_PROFILE_CALIBRATION" | awk '{print $1}')"
[[ "$actual_profile_calibration_hash" == "$VELASERVE_PROFILE_CALIBRATION_SHA256" ]] || fail "profile calibration SHA-256 mismatch"
jq -e --arg model "$VELASERVE_MODEL_ID" --arg revision "$VELASERVE_MODEL_REVISION" --arg transport "$VELASERVE_P2P_TRANSPORT" '
  .model_id == $model and .model_revision == $revision and .transport == $transport
' "$VELASERVE_PROFILE_CALIBRATION" >/dev/null || fail "profile calibration identity does not match the selected model revision and transport"
calibrated_min_cached_token_delta="$(jq -er '.min_cached_token_delta | select(type == "number" and . > 0 and floor == .)' "$VELASERVE_PROFILE_CALIBRATION")" || fail "profile calibration lacks a positive integral min_cached_token_delta"
identity_json="$(aws sts get-caller-identity --region "$VELASERVE_AWS_REGION" --output json)"
actual_account="$(jq -r '.Account' <<<"$identity_json")"
[[ "$actual_account" == "$VELASERVE_AWS_ACCOUNT_ID" ]] || fail "AWS account mismatch: got $actual_account"

cluster_arn="$(aws eks describe-cluster --name "$VELASERVE_CLUSTER_NAME" --region "$VELASERVE_AWS_REGION" --query 'cluster.arn' --output text)"
[[ "$cluster_arn" == "arn:aws:eks:${VELASERVE_AWS_REGION}:${VELASERVE_AWS_ACCOUNT_ID}:cluster/${VELASERVE_CLUSTER_NAME}" ]] || fail "EKS cluster ARN does not match the exact handoff target"

quota_value="$(aws service-quotas get-service-quota --service-code ec2 --quota-code "$VELASERVE_GPU_QUOTA_CODE" --region "$VELASERVE_AWS_REGION" --query 'Quota.Value' --output text)"
awk -v available="$quota_value" -v required="$VELASERVE_GPU_QUOTA_REQUIRED" 'BEGIN { exit !(available + 0 >= required + 0) }' || fail "EC2 GPU quota $quota_value is below required $VELASERVE_GPU_QUOTA_REQUIRED"

aws s3api head-bucket --bucket "$VELASERVE_ARTIFACT_BUCKET" --region "$VELASERVE_AWS_REGION" >/dev/null

verify_ecr_digest() {
  local reference="$1"
  local without_digest="${reference%@sha256:*}"
  local digest="sha256:${reference##*@sha256:}"
  local repository="${without_digest#*/}"
  repository="${repository%:*}"
  [[ "$without_digest" == "${VELASERVE_AWS_ACCOUNT_ID}.dkr.ecr.${VELASERVE_AWS_REGION}.amazonaws.com/"* ]] || fail "image is outside the expected account and region: $reference"
  aws ecr describe-images --region "$VELASERVE_AWS_REGION" --repository-name "$repository" --image-ids "imageDigest=$digest" >/dev/null
}
verify_ecr_digest "$VELASERVE_VELASERVE_IMAGE"
verify_ecr_digest "$VELASERVE_EPP_IMAGE"
verify_ecr_digest "$VELASERVE_MODEL_IMAGE"
verify_ecr_digest "$VELASERVE_ROUTING_SIDECAR_IMAGE"
verify_ecr_digest "$VELASERVE_CONDITION_DRIVER_IMAGE"
[[ "$VELASERVE_EPP_PROXY_IMAGE" =~ ^.+@sha256:[0-9a-f]{64}$ ]] || fail "inner EPP Envoy proxy image must be digest-addressed"
docker pull "$VELASERVE_EPP_PROXY_IMAGE" >/dev/null || fail "cannot resolve the digest-pinned inner EPP Envoy proxy image"
[[ "$VELASERVE_CONDITION_DRIVER_IMAGE" == "$VELASERVE_VELASERVE_IMAGE" ]] || fail "condition controller and driver must use the same exact VelaServe image digest"

readonly VLLM_UPSTREAM_COMMIT="b1fbbc2ade51e3826bc92e4733c9c692ee21d42d"
readonly ROUTER_UPSTREAM_COMMIT="ab723b898f8598ab6631e9848a4cf28accd9b9ea"
vllm_patch_sha256="$(shasum -a 256 "${REPOSITORY_ROOT}/deploy/upstream-patches/vllm-stage1-runtime-observer.patch" | awk '{print $1}')"
epp_observer_patch_sha256="$(shasum -a 256 "${REPOSITORY_ROOT}/deploy/upstream-patches/llm-d-router-stage1-observer.patch" | awk '{print $1}')"
sidecar_patch_sha256="$(shasum -a 256 "${REPOSITORY_ROOT}/deploy/upstream-patches/llm-d-router-stage1-sidecar-evidence.patch" | awk '{print $1}')"

image_label() {
  docker image inspect --format "{{ index .Config.Labels \"$2\" }}" "$1"
}

docker pull "$VELASERVE_VELASERVE_IMAGE" >/dev/null
[[ "$(image_label "$VELASERVE_VELASERVE_IMAGE" io.velaserve.component)" == "velaserve" ]] || fail "VelaServe image lacks component provenance"
[[ "$(image_label "$VELASERVE_VELASERVE_IMAGE" io.velaserve.repository.commit)" == "$repository_commit" ]] || fail "VelaServe image does not contain the exact preflight repository commit"
[[ "$(image_label "$VELASERVE_VELASERVE_IMAGE" io.velaserve.build.dirty)" == "false" ]] || fail "VelaServe image is not attested as a clean-tree build"
[[ "$(image_label "$VELASERVE_VELASERVE_IMAGE" io.velaserve.architecture)" == "amd64" && "$(docker image inspect --format '{{.Architecture}}' "$VELASERVE_VELASERVE_IMAGE")" == "amd64" ]] || fail "VelaServe image is not an attested linux/amd64 build"
velaserve_image_provenance="$(jq -cn --arg commit "$repository_commit" '{component:"velaserve",repository_commit:$commit,dirty:false}')"

verify_upstream_image_provenance() {
  local image="$1"
  local component="$2"
  local commit="$3"
  local first_label="$4"
  local first_sha="$5"
  local second_label="${6:-}"
  local second_sha="${7:-}"
  docker pull "$image" >/dev/null
  [[ "$(image_label "$image" io.velaserve.component)" == "$component" ]] || fail "$image lacks the expected component provenance"
  [[ "$(image_label "$image" io.velaserve.upstream.commit)" == "$commit" ]] || fail "$image lacks the expected upstream commit provenance"
  [[ "$(image_label "$image" io.velaserve.build.dirty)" == "false" ]] || fail "$image is not attested as a clean-tree build"
  [[ "$(image_label "$image" io.velaserve.architecture)" == "amd64" && "$(docker image inspect --format '{{.Architecture}}' "$image")" == "amd64" ]] || fail "$image is not an attested linux/amd64 build"
  [[ "$(image_label "$image" "$first_label")" == "$first_sha" ]] || fail "$image lacks the expected first patch provenance"
  if [[ -n "$second_label" ]]; then
    [[ "$(image_label "$image" "$second_label")" == "$second_sha" ]] || fail "$image lacks the expected second patch provenance"
  fi
}

verify_upstream_image_provenance "$VELASERVE_MODEL_IMAGE" vllm "$VLLM_UPSTREAM_COMMIT" io.velaserve.patch.runtime-observer.sha256 "$vllm_patch_sha256"
verify_upstream_image_provenance "$VELASERVE_EPP_IMAGE" epp "$ROUTER_UPSTREAM_COMMIT" io.velaserve.patch.observer.sha256 "$epp_observer_patch_sha256" io.velaserve.patch.sidecar-evidence.sha256 "$sidecar_patch_sha256"
verify_upstream_image_provenance "$VELASERVE_ROUTING_SIDECAR_IMAGE" routing-sidecar "$ROUTER_UPSTREAM_COMMIT" io.velaserve.patch.observer.sha256 "$epp_observer_patch_sha256" io.velaserve.patch.sidecar-evidence.sha256 "$sidecar_patch_sha256"
vllm_image_provenance="$(jq -cn --arg commit "$VLLM_UPSTREAM_COMMIT" --arg patch "$vllm_patch_sha256" '{component:"vllm",upstream_commit:$commit,patch_sha256s:[$patch],dirty:false}')"
epp_image_provenance="$(jq -cn --arg commit "$ROUTER_UPSTREAM_COMMIT" --arg observer "$epp_observer_patch_sha256" --arg sidecar "$sidecar_patch_sha256" '{component:"epp",upstream_commit:$commit,patch_sha256s:[$observer,$sidecar],dirty:false}')"
sidecar_image_provenance="$(jq -cn --arg commit "$ROUTER_UPSTREAM_COMMIT" --arg observer "$epp_observer_patch_sha256" --arg sidecar "$sidecar_patch_sha256" '{component:"routing-sidecar",upstream_commit:$commit,patch_sha256s:[$observer,$sidecar],dirty:false}')"

current_context="$(kubectl config current-context)"
case "$current_context" in
  "$VELASERVE_CLUSTER_NAME"|*"/${VELASERVE_CLUSTER_NAME}") ;;
  *) fail "kubectl context $current_context does not target $VELASERVE_CLUSTER_NAME" ;;
esac
kubectl auth can-i get pods --namespace "$VELASERVE_NAMESPACE" | grep -Fxq yes || fail "kubectl cannot read experiment pods"
kubectl auth can-i create pods/exec --namespace "$VELASERVE_NAMESPACE" | grep -Fxq yes || fail "kubectl cannot attest the GPU driver through the model container"

route_json="$(kubectl --namespace "$VELASERVE_NAMESPACE" get httproute velaserve -o json)"
jq -e 'any(.status.parents[]?.conditions[]?; .type == "Accepted" and .status == "True") and any(.status.parents[]?.conditions[]?; .type == "ResolvedRefs" and .status == "True")' <<<"$route_json" >/dev/null || fail "velaserve HTTPRoute is not accepted with resolved references"
route_uid="$(jq -r '.metadata.uid' <<<"$route_json")"
gateway_json="$(kubectl --namespace "$VELASERVE_NAMESPACE" get gateway velaserve-gateway -o json)"
jq -e 'any(.status.conditions[]?; .type == "Accepted" and .status == "True") and any(.status.conditions[]?; .type == "Programmed" and .status == "True")' <<<"$gateway_json" >/dev/null || fail "velaserve Envoy Gateway is not accepted and programmed"
gateway_uid="$(jq -r '.metadata.uid' <<<"$gateway_json")"
gatewayclass_json="$(kubectl get gatewayclass envoy -o json)"
jq -e '
  .spec.controllerName == "gateway.envoyproxy.io/gatewayclass-controller" and
  .spec.parametersRef.group == "gateway.envoyproxy.io" and
  .spec.parametersRef.kind == "EnvoyProxy" and
  .spec.parametersRef.name == "velaserve" and
  .spec.parametersRef.namespace == "envoy-gateway-system" and
  any(.status.conditions[]?; .type == "Accepted" and .status == "True")
' <<<"$gatewayclass_json" >/dev/null || fail "GatewayClass does not bind the frozen EnvoyProxy"
envoyproxy_json="$(kubectl --namespace envoy-gateway-system get envoyproxy velaserve -o json)"
gateway_services="$(kubectl get services --all-namespaces -l gateway.envoyproxy.io/owning-gateway-name=velaserve-gateway -o json)"
[[ "$(jq '.items | length' <<<"$gateway_services")" == "1" ]] || fail "exactly one generated Envoy Gateway service is required"
jq -e '.items[0].spec.type == "ClusterIP" and (.items[0].spec.externalIPs // [] | length) == 0 and (.items[0].status.loadBalancer.ingress // [] | length) == 0' <<<"$gateway_services" >/dev/null || fail "Envoy Gateway must remain ClusterIP-only with no external ingress"
envoy_namespace="$(jq -er '.items[0].metadata.namespace' <<<"$gateway_services")"
envoy_selector="$(jq -er '.items[0].spec.selector | to_entries | sort_by(.key) | map(.key + "=" + .value) | join(",") | select(length > 0)' <<<"$gateway_services")"
envoy_pods_json="$(kubectl --namespace "$envoy_namespace" get pods -l "$envoy_selector" -o json)"
jq -e --arg container "$VELASERVE_ENVOY_CONTAINER_NAME" --arg image "$VELASERVE_ENVOY_IMAGE" '
  (.items | length) > 0 and all(.items[]; .status.phase == "Running" and any(.status.conditions[]?; .type == "Ready" and .status == "True") and any(.spec.containers[]; .name == $container and .image == $image) and all(.status.containerStatuses[]; .restartCount == 0))
' <<<"$envoy_pods_json" >/dev/null || fail "generated Envoy data-plane pods must be ready, restart-free, and digest-pinned"
envoy_controller_namespace="envoy-gateway-system"
envoy_controller_pods_json="$(kubectl --namespace "$envoy_controller_namespace" get pods -l "$VELASERVE_ENVOY_GATEWAY_CONTROLLER_SELECTOR" -o json)"
jq -e --arg container "$VELASERVE_ENVOY_GATEWAY_CONTROLLER_CONTAINER_NAME" --arg image "$VELASERVE_ENVOY_GATEWAY_CONTROLLER_IMAGE" '
  (.items | length) > 0 and all(.items[]; .status.phase == "Running" and any(.status.conditions[]?; .type == "Ready" and .status == "True") and any(.spec.containers[]; .name == $container and .image == $image) and all(.status.containerStatuses[]; .restartCount == 0))
' <<<"$envoy_controller_pods_json" >/dev/null || fail "Envoy Gateway controller pods must be ready, restart-free, and digest-pinned"
routing_sha256="$(jq -S -c -n --argjson route "$route_json" --argjson gateway "$gateway_json" --argjson gatewayclass "$gatewayclass_json" --argjson envoyproxy "$envoyproxy_json" --argjson services "$gateway_services" --argjson data_plane "$envoy_pods_json" --argjson controller "$envoy_controller_pods_json" '{route: {uid: $route.metadata.uid, spec: $route.spec}, gateway: {uid: $gateway.metadata.uid, spec: $gateway.spec}, gatewayclass: {uid: $gatewayclass.metadata.uid, spec: $gatewayclass.spec}, envoyproxy: {uid: $envoyproxy.metadata.uid, spec: $envoyproxy.spec}, services: [$services.items[] | {uid: .metadata.uid, spec: .spec}] | sort_by(.uid), data_plane: [$data_plane.items[] | {uid: .metadata.uid, spec: .spec}] | sort_by(.uid), controller: [$controller.items[] | {uid: .metadata.uid, spec: .spec}] | sort_by(.uid)}' | shasum -a 256 | awk '{print $1}')"

ready_pods_json="$(kubectl --namespace "$VELASERVE_NAMESPACE" get pods -l "$VELASERVE_MODEL_SELECTOR" -o json)"
ready_replicas="$(jq '[.items[] | select(.status.phase == "Running") | select(any(.status.conditions[]?; .type == "Ready" and .status == "True"))] | length' <<<"$ready_pods_json")"
(( ready_replicas >= VELASERVE_GPU_MINIMUM_BENCHMARK_REPLICAS )) || fail "only $ready_replicas ready model replicas; need $VELASERVE_GPU_MINIMUM_BENCHMARK_REPLICAS"
(( ready_replicas <= VELASERVE_GPU_MAXIMUM_BENCHMARK_REPLICAS )) || fail "$ready_replicas ready model replicas exceed the declared benchmark maximum"
jq -e --arg revision "$VELASERVE_MODEL_REVISION" 'all(.items[]; .metadata.labels["velaserve.ai/model-revision"] == $revision)' <<<"$ready_pods_json" >/dev/null || fail "model pod revision labels do not match VELASERVE_MODEL_REVISION"
model_workload_json="$(kubectl --namespace "$VELASERVE_NAMESPACE" get statefulset "$VELASERVE_MODEL_WORKLOAD_NAME" -o json)"
jq -e --arg release "$VELASERVE_MODEL_RELEASE_NAME" '.metadata.annotations["meta.helm.sh/release-name"] == $release and .metadata.annotations["meta.helm.sh/release-namespace"] == .metadata.namespace' <<<"$model_workload_json" >/dev/null || fail "model StatefulSet is not owned by the declared Helm release"
jq -e \
  --arg container "$VELASERVE_MODEL_CONTAINER_NAME" \
  --arg image "$VELASERVE_MODEL_IMAGE" \
  --arg sidecar "$VELASERVE_ROUTING_SIDECAR_CONTAINER_NAME" \
  --arg sidecar_image "$VELASERVE_ROUTING_SIDECAR_IMAGE" \
  --arg model "$VELASERVE_MODEL_ID" \
  --arg revision "$VELASERVE_MODEL_REVISION" \
  --argjson cpu_bytes "$VELASERVE_CPU_OFFLOAD_BYTES" '
  def arg_after($args; $name): ($args | index($name)) as $index | if $index == null or ($index + 1) >= ($args | length) then null else $args[$index + 1] end;
  all(.items[];
    (.spec.containers[] | select(.name == $container)) as $engine |
    (.spec.initContainers[] | select(.name == $sidecar)) as $proxy |
    (arg_after($engine.args; "--kv-transfer-config") | fromjson) as $kv |
    (arg_after($engine.args; "--kv-events-config") | fromjson) as $events |
    $engine.image == $image and
    $engine.resources.requests["nvidia.com/gpu"] == "1" and $engine.resources.limits["nvidia.com/gpu"] == "1" and
    any($engine.args[]?; . == $model) and
    any($engine.args[]?; . == ("--revision=" + $revision)) and
    any($engine.args[]?; . == "--port=8200") and
    any($engine.args[]?; . == "--block-size=64") and
    any($engine.args[]?; . == "--enable-prefix-caching") and
    any($engine.args[]?; . == "--enable-prompt-tokens-details") and
    any($engine.args[]?; . == "--enable-request-id-headers") and
    any($engine.args[]?; . == "--enable-tokenizer-info-endpoint") and
    $kv.kv_connector == "OffloadingConnector" and $kv.kv_role == "kv_both" and
    $kv.kv_connector_extra_config.spec_name == "TieringOffloadingSpec" and
    $kv.kv_connector_extra_config.cpu_bytes_to_use == $cpu_bytes and
    $kv.kv_connector_extra_config.offload_prompt_only == false and
    $kv.kv_connector_extra_config.secondary_tiers == [{"type":"p2p","host":"$(POD_IP)","port":7777}] and
    $events.enable_kv_cache_events == true and $events.publisher == "zmq" and
    $events.endpoint == "$(KV_EVENTS_ENDPOINT)" and $events.topic == ("kv@$(POD_IP):$(POD_PORT)@" + $model) and
    any($engine.env[]?; .name == "POD_IP" and .valueFrom.fieldRef.fieldPath == "status.podIP") and
    any($engine.env[]?; .name == "VLLM_P2P_SIDE_CHANNEL_HOST" and .valueFrom.fieldRef.fieldPath == "status.podIP") and
    any($engine.env[]?; .name == "PYTHONHASHSEED" and .value == "0") and
    any($engine.env[]?; .name == "KV_EVENTS_ENDPOINT" and .value == "tcp://*:5556") and
    any($engine.env[]?; .name == "POD_PORT" and .value == "8000") and
    any($engine.env[]?; .name == "UCX_TLS" and .value == "tcp") and
    any($engine.env[]?; .name == "VELASERVE_VLLM_OBSERVER_VERSION" and .value == "velaserve.vllm-runtime-observer/v1") and
    any($engine.env[]?; .name == "VLLM_SERVER_DEV_MODE" and .value == "1") and
    any($engine.ports[]?; .name == "engine" and .containerPort == 8200 and .protocol == "TCP") and
    any($engine.ports[]?; .name == "p2p" and .containerPort == 7777 and .protocol == "TCP") and
    any($engine.ports[]?; .name == "kv-events" and .containerPort == 5556 and .protocol == "TCP") and
    $proxy.image == $sidecar_image and $proxy.restartPolicy == "Always" and
    (["--port=8000","--vllm-port=8200","--kv-connector=offloading","--p2p-connector-port=7777","--secure-proxy=false"] - $proxy.args | length) == 0 and
    any($proxy.env[]?; .name == "POD_IP" and .valueFrom.fieldRef.fieldPath == "status.podIP") and
    any($proxy.ports[]?; .name == "proxy" and .containerPort == 8000 and .protocol == "TCP") and
    all(.status.containerStatuses[]; .restartCount == 0) and
    all(.status.initContainerStatuses[]; .restartCount == 0)
  )' <<<"$ready_pods_json" >/dev/null || fail "model pods do not match the frozen vLLM OffloadingConnector, request-ID, KV-events, TCP, observer, and routing-sidecar contract"

# Verify the frozen tokenizer/profile against every selected model pod. A
# caller-provided Service URL is insufficient evidence because it may route to
# only one replica or to a different workload.
verify_model_pod_tokenizer() {
  local pod="$1"
  local local_port=18200
  local log_file
  local forward_pid
  local ready=0
  local status=0
  log_file="$(mktemp)"
  kubectl --namespace "$VELASERVE_NAMESPACE" port-forward --address 127.0.0.1 "pod/$pod" "${local_port}:8200" >"$log_file" 2>&1 &
  forward_pid="$!"
  for _ in {1..50}; do
    if ! kill -0 "$forward_pid" 2>/dev/null; then
      status=1
      break
    fi
    if curl --fail --silent --show-error "http://127.0.0.1:${local_port}/get_tokenizer_info" >/dev/null 2>&1; then
      ready=1
      break
    fi
    sleep 0.2
  done
  if (( ready == 0 )); then
    status=1
  elif ! go -C "$REPOSITORY_ROOT" run ./cmd/profile-freeze verify-live \
    --profile "$VELASERVE_BENCHMARK_PROFILE" \
    --calibration "$VELASERVE_PROFILE_CALIBRATION" \
    --tokenize-url "http://127.0.0.1:${local_port}/tokenize" \
    --tokenizer-info-url "http://127.0.0.1:${local_port}/get_tokenizer_info" \
    --phase "$VELASERVE_Z0_PHASE"; then
    status=1
  fi
  kill "$forward_pid" 2>/dev/null || true
  wait "$forward_pid" 2>/dev/null || true
  if (( status != 0 )); then
    sed -n '1,40p' "$log_file" >&2
  fi
  rm -f "$log_file"
  return "$status"
}

while IFS= read -r model_pod; do
  [[ -n "$model_pod" ]] || continue
  verify_model_pod_tokenizer "$model_pod" || fail "live tokenizer/profile calibration verification failed for exact model pod $model_pod"
done < <(jq -r '.items[] | [.metadata.uid, .metadata.name] | @tsv' <<<"$ready_pods_json" | sort | cut -f2)
tokenizer_verified_pod_uids="$(jq -c '[.items[].metadata.uid] | sort' <<<"$ready_pods_json")"

model_service_json="$(kubectl --namespace "$VELASERVE_NAMESPACE" get service "$VELASERVE_MODEL_SERVICE_NAME" -o json)"
jq -e '
  .spec.selector["app.kubernetes.io/name"] == "velaserve-model" and
  ([.spec.ports[] | select(.port == 8000 and .targetPort == "proxy" and .protocol == "TCP")] | length) == 1
' <<<"$model_service_json" >/dev/null || fail "model Service must select the frozen fleet and route TCP 8000 to the routing proxy"
model_service_uid="$(jq -r '.metadata.uid' <<<"$model_service_json")"
model_service_spec_sha256="$(jq -S -c '{uid: .metadata.uid, spec: .spec}' <<<"$model_service_json" | shasum -a 256 | awk '{print $1}')"
model_network_policy_json="$(kubectl --namespace "$VELASERVE_NAMESPACE" get networkpolicy velaserve-model-engine-isolation -o json)"
jq -e '
  .spec.podSelector.matchLabels["app.kubernetes.io/name"] == "velaserve-model" and
  .spec.policyTypes == ["Ingress"] and
  ([.spec.ingress[] | .ports[]? | select(.port == 8200)] | length) == 1 and
  any(.spec.ingress[]; any(.ports[]?; .port == 8200) and any(.from[]?; .podSelector.matchLabels["app.kubernetes.io/name"] == "velaserve-condition-driver")) and
  any(.spec.ingress[]; any(.ports[]?; .port == 8000) and any(.from[]?; .podSelector.matchLabels["llm-d-router-gateway"] == "velaserve-epp")) and
  any(.spec.ingress[]; any(.ports[]?; .port == 5556) and any(.from[]?; .podSelector.matchLabels["llm-d-router-gateway"] == "velaserve-epp")) and
  any(.spec.ingress[]; any(.ports[]?; .port == 7777) and any(.from[]?; .podSelector.matchLabels["app.kubernetes.io/name"] == "velaserve-model")) and
  all(.spec.ingress[]; all(.from[]?; (has("podSelector") or has("namespaceSelector")) and (has("ipBlock") | not)))
' <<<"$model_network_policy_json" >/dev/null || fail "model dev API must be isolated to the condition driver by NetworkPolicy"
model_network_policy_sha256="$(jq -S -c '{uid: .metadata.uid, spec: .spec}' <<<"$model_network_policy_json" | shasum -a 256 | awk '{print $1}')"
gpu_model_names=""
while IFS= read -r model_pod; do
  [[ -n "$model_pod" ]] || continue
  observed_gpu_identity="$(kubectl --namespace "$VELASERVE_NAMESPACE" exec "$model_pod" -c "$VELASERVE_MODEL_CONTAINER_NAME" -- nvidia-smi --query-gpu=name,driver_version --format=csv,noheader,nounits | sed '/^$/d')"
  [[ "$(wc -l <<<"$observed_gpu_identity" | tr -d '[:space:]')" == "1" ]] || fail "model pod $model_pod must expose exactly one GPU"
  observed_gpu_name="$(cut -d, -f1 <<<"$observed_gpu_identity" | sed 's/[[:space:]]*$//')"
  observed_driver_versions="$(cut -d, -f2- <<<"$observed_gpu_identity" | sed 's/^[[:space:]]*//' | sort -u)"
	[[ "$observed_driver_versions" == "$VELASERVE_GPU_DRIVER_VERSION" ]] || fail "model pod $model_pod reports GPU driver '$observed_driver_versions', want '$VELASERVE_GPU_DRIVER_VERSION'"
  gpu_model_names="${gpu_model_names}${observed_gpu_name}\n"
done < <(jq -r '.items[].metadata.name' <<<"$ready_pods_json" | sort)
observed_gpu_model="$(printf '%b' "$gpu_model_names" | sort -u | sed '/^$/d')"
[[ -n "$observed_gpu_model" && "$(wc -l <<<"$observed_gpu_model" | tr -d '[:space:]')" == "1" ]] || fail "model pods do not expose one homogeneous GPU model"

hpa_json="$(kubectl --namespace "$VELASERVE_NAMESPACE" get horizontalpodautoscalers -o json)"
jq -e --arg workload "$VELASERVE_MODEL_WORKLOAD_NAME" 'all(.items[]; .spec.scaleTargetRef.name != $workload)' <<<"$hpa_json" >/dev/null || fail "model workload has an active HPA; freeze replica count before Z0"

epp_pods_json="$(kubectl --namespace "$VELASERVE_NAMESPACE" get pods -l "$VELASERVE_EPP_SELECTOR" -o json)"
ready_epp_replicas="$(jq '[.items[] | select(.status.phase == "Running") | select(any(.status.conditions[]?; .type == "Ready" and .status == "True"))] | length' <<<"$epp_pods_json")"
[[ "$ready_epp_replicas" == "$VELASERVE_EPP_REPLICAS" ]] || fail "ready EPP replica count $ready_epp_replicas does not match active condition $VELASERVE_EPP_REPLICAS"
jq -e --arg container "$VELASERVE_EPP_CONTAINER_NAME" --arg image "$VELASERVE_EPP_IMAGE" --arg proxy_container "$VELASERVE_EPP_PROXY_CONTAINER_NAME" --arg proxy_image "$VELASERVE_EPP_PROXY_IMAGE" 'all(.items[]; any(.spec.containers[]; .name == $container and .image == $image) and any(.spec.containers[]; .name == $proxy_container and .image == $proxy_image) and all(.status.containerStatuses[]; .restartCount == 0))' <<<"$epp_pods_json" >/dev/null || fail "EPP and inner Envoy pods do not match both bound image digests or have restarted"
jq -e 'all(.items[]; .metadata.labels["llm-d-router-gateway"] == "velaserve-epp")' <<<"$epp_pods_json" >/dev/null || fail "EPP pods must carry llm-d-router-gateway=velaserve-epp for NetworkPolicy"

controller_pods_json="$(kubectl --namespace "$VELASERVE_NAMESPACE" get pods -l "$VELASERVE_CONDITION_CONTROLLER_SELECTOR" -o json)"
jq -e --arg container "$VELASERVE_CONDITION_CONTROLLER_CONTAINER_NAME" --arg image "$VELASERVE_VELASERVE_IMAGE" --arg revision "$VELASERVE_CONTROLLER_REVISION" --arg driver "$VELASERVE_CONDITION_DRIVER_ENDPOINT" --arg timeout "$VELASERVE_CONDITION_APPLY_TIMEOUT_SECONDS" --arg secret_name "$VELASERVE_CONDITION_CONTROL_SECRET_NAME" --arg secret_key "$VELASERVE_CONDITION_CONTROL_SECRET_KEY" '
  ([.items[] | select(.status.phase == "Running") | select(any(.status.conditions[]?; .type == "Ready" and .status == "True"))] | length) == 1 and
  all(.items[];
    any(.spec.containers[]; .name == $container and .image == $image and any(.env[]?; .name == "VELASERVE_CONTROLLER_REVISION" and .value == $revision) and any(.env[]?; .name == "VELASERVE_CONDITION_DRIVER_URL" and .value == $driver) and any(.env[]?; .name == "VELASERVE_CONDITION_APPLY_TIMEOUT_SECONDS" and .value == $timeout) and any(.env[]?; .name == "VELASERVE_CONDITION_CONTROL_TOKEN" and .valueFrom.secretKeyRef.name == $secret_name and .valueFrom.secretKeyRef.key == $secret_key)) and
    all(.status.containerStatuses[]; .restartCount == 0)
  )' <<<"$controller_pods_json" >/dev/null || fail "exactly one digest-bound, revision-bound, restart-free condition controller must be ready"

driver_pods_json="$(kubectl --namespace "$VELASERVE_NAMESPACE" get pods -l "$VELASERVE_CONDITION_DRIVER_SELECTOR" -o json)"
jq -e --arg container "$VELASERVE_CONDITION_DRIVER_CONTAINER_NAME" --arg image "$VELASERVE_CONDITION_DRIVER_IMAGE" --arg secret_name "$VELASERVE_CONDITION_CONTROL_SECRET_NAME" --arg secret_key "$VELASERVE_CONDITION_CONTROL_SECRET_KEY" --arg drain_timeout "$VELASERVE_CONDITION_DRIVER_DRAIN_TIMEOUT_SECONDS" --arg drain_poll "$VELASERVE_CONDITION_DRIVER_DRAIN_POLL_MILLISECONDS" '
  ([.items[] | select(.status.phase == "Running") | select(any(.status.conditions[]?; .type == "Ready" and .status == "True"))] | length) == 1 and
  all(.items[]; any(.spec.containers[]; .name == $container and .image == $image and any(.env[]?; .name == "VELASERVE_CONDITION_CONTROL_TOKEN" and .valueFrom.secretKeyRef.name == $secret_name and .valueFrom.secretKeyRef.key == $secret_key) and any(.env[]?; .name == "VELASERVE_DRIVER_DRAIN_TIMEOUT_SECONDS" and .value == $drain_timeout) and any(.env[]?; .name == "VELASERVE_DRIVER_DRAIN_POLL_MILLISECONDS" and .value == $drain_poll)) and all(.status.containerStatuses[]; .restartCount == 0))
  ' <<<"$driver_pods_json" >/dev/null || fail "exactly one digest-bound, restart-free condition driver must be ready"
driver_gateway_chat_url="$(jq -er --arg container "$VELASERVE_CONDITION_DRIVER_CONTAINER_NAME" '
  [.items[].spec.containers[] | select(.name == $container) | .env[]? | select(.name == "VELASERVE_DRIVER_GATEWAY_CHAT_URL") | .value] |
  if length == 1 then .[0] else error("condition-driver gateway URL cardinality") end
' <<<"$driver_pods_json")" || fail "condition driver must expose exactly one literal gateway chat URL"
[[ "$driver_gateway_chat_url" == "$VELASERVE_GATEWAY_CHAT_URL" ]] || fail "condition driver gateway chat URL differs from the stable in-cluster gateway URL"
control_secret_json="$(kubectl --namespace "$VELASERVE_NAMESPACE" get secret "$VELASERVE_CONDITION_CONTROL_SECRET_NAME" -o json)"
local_control_token_base64="$(printf '%s' "$VELASERVE_CONDITION_CONTROL_TOKEN" | jq -Rrs '@base64')"
jq -e --arg key "$VELASERVE_CONDITION_CONTROL_SECRET_KEY" --arg local "$local_control_token_base64" '.type == "Opaque" and (.data[$key] | type == "string" and length > 0) and .data[$key] == $local' <<<"$control_secret_json" >/dev/null || fail "local condition-control token does not exactly match the non-empty Opaque Secret key"
control_secret_sha256="$(jq -S -c --arg key "$VELASERVE_CONDITION_CONTROL_SECRET_KEY" '{name: .metadata.name, uid: .metadata.uid, resource_version: .metadata.resourceVersion, type: .type, key: $key}' <<<"$control_secret_json" |
  shasum -a 256 | awk '{print $1}')"
driver_config_json="$(kubectl --namespace "$VELASERVE_NAMESPACE" get configmaps -l "$VELASERVE_CONDITION_DRIVER_CONFIGMAP_SELECTOR" -o json)"
[[ "$(jq '.items | length' <<<"$driver_config_json")" == "1" ]] || fail "condition-driver config selector must match exactly one ConfigMap"
driver_config_sha256="$(jq -S -c '[.items[] | {name: .metadata.name, uid: .metadata.uid, data: .data, binary_data: .binaryData}]' <<<"$driver_config_json" | shasum -a 256 | awk '{print $1}')"
driver_endpoints_json="$(jq -er '.items[0].data["endpoints.json"] | fromjson' <<<"$driver_config_json")"
jq -e --arg namespace "$VELASERVE_NAMESPACE" --arg service "$VELASERVE_MODEL_SERVICE_NAME" --argjson pods "$ready_pods_json" '
  def dns($id): ($id + "." + $service + "-headless." + $namespace + ".svc.cluster.local");
  length == ($pods.items | length) and
  ([.[].id] | unique | length) == length and
  all(.[];
    .id as $id |
    any($pods.items[]; .metadata.name == $id) and
    .chat_url == ("http://" + dns($id) + ":8200/v1/chat/completions") and
    .reset_url == ("http://" + dns($id) + ":8200/reset_prefix_cache") and
    .metrics_url == ("http://" + dns($id) + ":8200/metrics")
  )
' <<<"$driver_endpoints_json" >/dev/null || fail "condition-driver endpoints must bind every model pod through stable direct-engine DNS URLs"
load_profiles_json="$(jq -er '.items[0].data["load-profiles.json"] | fromjson' <<<"$driver_config_json")"
load_calibration_json="$(jq -er '.items[0].data["load-calibration.json"] | fromjson' <<<"$driver_config_json")"
load_calibration_file="$(mktemp)"
trap 'rm -f "$load_calibration_file"' EXIT
printf '%s\n' "$load_calibration_json" >"$load_calibration_file"
driver_load_calibration_sha256="$(go -C "$REPOSITORY_ROOT" run ./cmd/condition-driver hash-load-calibration "$load_calibration_file")"
local_load_calibration_sha256="$(go -C "$REPOSITORY_ROOT" run ./cmd/condition-driver hash-load-calibration "$VELASERVE_LOAD_CALIBRATION")"
[[ "$local_load_calibration_sha256" == "$driver_load_calibration_sha256" ]] || fail "local retained load calibration does not match the driver ConfigMap"
jq -e --arg model "$VELASERVE_MODEL_ID" --arg revision "$VELASERVE_MODEL_REVISION" --arg image "$VELASERVE_MODEL_IMAGE" --arg transport "$VELASERVE_P2P_TRANSPORT" --arg arm "$VELASERVE_ACTIVE_ARM" --arg routing "$routing_sha256" --arg gpu_model "$observed_gpu_model" --arg gpu_driver "$VELASERVE_GPU_DRIVER_VERSION" --arg profile_calibration "$actual_profile_calibration_hash" --arg gateway_chat_url "$VELASERVE_GATEWAY_CHAT_URL" --arg namespace "$VELASERVE_NAMESPACE" --arg service "$VELASERVE_MODEL_SERVICE_NAME" --argjson pods "$ready_pods_json" --argjson replicas "$ready_replicas" '
  def dns($id): ($id + "." + $service + "-headless." + $namespace + ".svc.cluster.local");
  .schema_version == "velaserve.load-calibration/v4" and .model_id == $model and .model_revision == $revision and .model_image == $image and .transport == $transport and .active_arm == $arm and .routing_sha256 == $routing and .gpu_model == $gpu_model and .gpu_driver_version == $gpu_driver and .profile_calibration_sha256 == $profile_calibration and .gateway_chat_url == $gateway_chat_url and .replica_count == $replicas and .gpus_per_replica == 1 and
  (.endpoints | sort_by(.id)) == ([$pods.items[] | {id:.metadata.name,pod_uid:.metadata.uid,metrics_url:("http://" + dns(.metadata.name) + ":8200/metrics")}] | sort_by(.id)) and
  (.trials | length) >= 30 and all(.trials[]; .pre_drain.stable_samples == 2 and .warmup_drain.stable_samples == 2 and .post_drain.stable_samples == 2)
' <<<"$load_calibration_json" >/dev/null || fail "load calibration does not bind the exact model, fleet, hardware, transport, routing, and profile calibration"
expected_load_shapes="$(jq -c '[.prefixes[].cacheable_shared_tokens as $prefix | [$prefix,32],[$prefix,128]] | sort' "$VELASERVE_PROFILE_CALIBRATION")"
jq -e --argjson expected "$expected_load_shapes" --arg calibration "$driver_load_calibration_sha256" '
  length == 6 and
  ([.[] | [.prefix_tokens, .max_tokens]] | sort) == $expected and
  all(.[]; (.saturation_qps | type) == "number" and .saturation_qps > 0 and .measurement_source == ("load-calibration:" + $calibration) and .calibration_sha256 == $calibration)
' <<<"$load_profiles_json" >/dev/null || fail "condition-driver load profiles do not cover the frozen six prompt/output shapes"
load_profiles_file="$(mktemp)"
trap 'rm -f "$load_calibration_file" "$load_profiles_file"' EXIT
printf '%s\n' "$load_profiles_json" >"$load_profiles_file"
go -C "$REPOSITORY_ROOT" run ./cmd/condition-driver validate-load-calibration "$load_calibration_file" "$load_profiles_file" "$VELASERVE_MODEL_ID" || fail "raw load calibration does not derive the configured saturation profiles"
go -C "$REPOSITORY_ROOT" run ./cmd/oracle-calibration \
  --profile "$VELASERVE_BENCHMARK_PROFILE" \
  --profile-calibration "$VELASERVE_PROFILE_CALIBRATION" \
  --crossover-bundle "$VELASERVE_CROSSOVER_BUNDLE" \
  --load-calibration "$VELASERVE_LOAD_CALIBRATION" \
  --inflight-publication-delay-ms "$VELASERVE_INFLIGHT_PUBLICATION_DELAY_MS" \
  --affinity-load-gate-seconds "$VELASERVE_AFFINITY_LOAD_GATE_SECONDS" \
  --verify "$VELASERVE_ORACLE_CALIBRATION" >/dev/null || fail "oracle calibration is not derived from the sealed raw crossover and load observations"
driver_load_profiles_sha256="$(go -C "$REPOSITORY_ROOT" run ./cmd/condition-driver hash-load-profiles "$load_profiles_file")"
rm -f "$load_calibration_file" "$load_profiles_file"
trap - EXIT

epp_configmap_name_sets="$(jq -c '[.items[] | [.spec.volumes[]? | .configMap.name] | sort | unique] | unique' <<<"$epp_pods_json")"
jq -e 'length == 1 and (.[0] | length) > 0' <<<"$epp_configmap_name_sets" >/dev/null || fail "all EPP pods must reference one identical non-empty ConfigMap set"
router_config_json='[]'
while IFS= read -r configmap_name; do
  [[ -n "$configmap_name" ]] || continue
  configmap_json="$(kubectl --namespace "$VELASERVE_NAMESPACE" get configmap "$configmap_name" -o json)"
  router_config_json="$(jq -c --argjson item "$configmap_json" '. + [$item]' <<<"$router_config_json")"
done < <(jq -r '.[0][]' <<<"$epp_configmap_name_sets")
jq -e 'length > 0 and all(.[]; (.metadata.uid | type) == "string" and (.metadata.uid | length) > 0 and (((.data // {}) | length) + ((.binaryData // {}) | length) > 0))' <<<"$router_config_json" >/dev/null || fail "EPP-referenced ConfigMaps must exist with UID-bound configuration data"
jq -e 'any(.[]; any((.data // {})[]; contains("REQ(X-REQUEST-ID)") and contains("REQ(X-VELA-FANOUT-GROUP)") and contains("UPSTREAM_HOST") and contains("DURATION")))' <<<"$router_config_json" >/dev/null || fail "inner EPP Envoy config must emit the exact request/group/model-target access-log evidence"
router_config_sha256="$(jq -S -c '[.[] | {name: .metadata.name, uid: .metadata.uid, data: .data, binary_data: .binaryData}] | sort_by(.uid)' <<<"$router_config_json" | shasum -a 256 | awk '{print $1}')"
router_config_invariant_sha256="$(jq -S -c '[.[] | {name: .metadata.name, uid: .metadata.uid, data: .data, binary_data: .binaryData}] | sort_by(.uid) | walk(if type == "string" then gsub("minCachedTokenDelta:[[:space:]]*[0-9]+"; "minCachedTokenDelta: <MEASURED>") else . end)' <<<"$router_config_json" | shasum -a 256 | awk '{print $1}')"
jq -e --arg threshold "$calibrated_min_cached_token_delta" 'any(.[]; any((.data // {})[]; test("minCachedTokenDelta:[[:space:]]*" + $threshold + "([[:space:]]|$)")))' <<<"$router_config_json" >/dev/null || fail "live EPP minCachedTokenDelta does not match the measured crossover calibration"
jq -e --arg router_config "$router_config_sha256" '.router_config_sha256 == $router_config' <<<"$load_calibration_json" >/dev/null || fail "load calibration router-config binding does not match the exact live EPP ConfigMaps"

contract="$(kubectl --namespace "$VELASERVE_NAMESPACE" get configmap velaserve-zeroing-contract -o 'jsonpath={.data.contract\.yaml}')"
grep -Fq "evidence_scope: \"real_gpu\"" <<<"$contract" || fail "deployed zeroing contract is not real_gpu"
grep -Fq "routing_profile: \"${VELASERVE_ACTIVE_ARM}\"" <<<"$contract" || fail "deployed routing profile does not match VELASERVE_ACTIVE_ARM"
grep -Fq "epp_replicas: ${VELASERVE_EPP_REPLICAS}" <<<"$contract" || fail "deployed EPP replica contract does not match"
grep -Fq "coordination_store: absent" <<<"$contract" || fail "pre-gate deployment unexpectedly declares a coordination store"
zeroing_contract_sha256="$(printf '%s' "$contract" | shasum -a 256 | awk '{print $1}')"

node_names="$(jq -r '.items[] | select(.status.phase == "Running") | .spec.nodeName' <<<"$ready_pods_json" | sort -u)"
[[ -n "$node_names" ]] || fail "ready model pods have no nodes"
instance_types=""
node_bindings='[]'
while IFS= read -r node_name; do
  [[ -n "$node_name" ]] || continue
  instance_type="$(kubectl get node "$node_name" -o 'jsonpath={.metadata.labels.node\.kubernetes\.io/instance-type}')"
  case ",${VELASERVE_GPU_INSTANCE_TYPES}," in
    *",${instance_type},"*) ;;
    *) fail "node $node_name uses unapproved instance type $instance_type" ;;
  esac
  instance_types="${instance_types}${instance_type}\n"
  gpu_allocatable="$(kubectl get node "$node_name" -o 'jsonpath={.status.allocatable.nvidia\.com/gpu}')"
  [[ "$gpu_allocatable" == "1" ]] || fail "node $node_name must expose exactly one allocatable NVIDIA GPU"
  node_json="$(kubectl get node "$node_name" -o json)"
  node_bindings="$(jq -c --argjson node "$node_json" --arg gpu_model "$observed_gpu_model" --arg gpu_driver "$VELASERVE_GPU_DRIVER_VERSION" '. + [{name: $node.metadata.name, uid: $node.metadata.uid, instance_type: $node.metadata.labels["node.kubernetes.io/instance-type"], gpu_model: $gpu_model, gpu_allocatable: ($node.status.allocatable["nvidia.com/gpu"] | tonumber), architecture: $node.status.nodeInfo.architecture, os_image: $node.status.nodeInfo.osImage, kernel_version: $node.status.nodeInfo.kernelVersion, container_runtime_version: $node.status.nodeInfo.containerRuntimeVersion, kubelet_version: $node.status.nodeInfo.kubeletVersion, gpu_driver_version: $gpu_driver}] | sort_by(.uid)' <<<"$node_bindings")"
done <<<"$node_names"
unique_instance_types="$(printf '%b' "$instance_types" | sort -u | sed '/^$/d' | wc -l | tr -d '[:space:]')"
[[ "$unique_instance_types" == "1" ]] || fail "GPU model pods are not homogeneous"
observed_instance_type="$(printf '%b' "$instance_types" | sort -u | sed '/^$/d')"
jq -e --arg instance_type "$observed_instance_type" '.instance_type == $instance_type' <<<"$load_calibration_json" >/dev/null || fail "load calibration instance type does not match the bound GPU nodes"
[[ "$(jq 'length' <<<"$node_bindings")" == "$ready_replicas" ]] || fail "model pods must occupy one single-GPU node each"

endpoint_status="$(curl --silent --show-error --connect-timeout 10 --max-time 20 --output /dev/null --write-out '%{http_code}' "$VELASERVE_ENDPOINT")"
[[ "$endpoint_status" != "000" ]] || fail "VELASERVE_ENDPOINT is unreachable"
controller_status="$(curl --silent --show-error --connect-timeout 10 --max-time 20 --output /dev/null --write-out '%{http_code}' "$VELASERVE_CONDITION_CONTROLLER_ENDPOINT")"
[[ "$controller_status" != "000" ]] || fail "VELASERVE_CONDITION_CONTROLLER_ENDPOINT is unreachable"

mkdir -p "${REPOSITORY_ROOT}/.tools"
model_pod_bindings="$(jq -c --arg container "$VELASERVE_MODEL_CONTAINER_NAME" '[.items[] as $pod | ($pod.spec.containers[] | select(.name == $container)) as $spec | ($pod.status.containerStatuses[] | select(.name == $container)) as $status | {name: $pod.metadata.name, uid: $pod.metadata.uid, image: $spec.image, node_name: $pod.spec.nodeName, restart_count: $status.restartCount}] | sort_by(.uid)' <<<"$ready_pods_json")"
envoy_pod_bindings="$(jq -c --arg container "$VELASERVE_ENVOY_CONTAINER_NAME" '[.items[] as $pod | ($pod.spec.containers[] | select(.name == $container)) as $spec | ($pod.status.containerStatuses[] | select(.name == $container)) as $status | {name: $pod.metadata.name, uid: $pod.metadata.uid, image: $spec.image, node_name: $pod.spec.nodeName, restart_count: $status.restartCount}] | sort_by(.uid)' <<<"$envoy_pods_json")"
envoy_controller_pod_bindings="$(jq -c --arg container "$VELASERVE_ENVOY_GATEWAY_CONTROLLER_CONTAINER_NAME" '[.items[] as $pod | ($pod.spec.containers[] | select(.name == $container)) as $spec | ($pod.status.containerStatuses[] | select(.name == $container)) as $status | {name: $pod.metadata.name, uid: $pod.metadata.uid, image: $spec.image, node_name: $pod.spec.nodeName, restart_count: $status.restartCount}] | sort_by(.uid)' <<<"$envoy_controller_pods_json")"
driver_endpoint_bindings="$(jq -c --arg namespace "$VELASERVE_NAMESPACE" --arg service "$VELASERVE_MODEL_SERVICE_NAME" --argjson pods "$ready_pods_json" '
  [.[] as $endpoint | ($pods.items[] | select(.metadata.name == $endpoint.id)) as $pod |
    {id: $endpoint.id, pod_uid: $pod.metadata.uid, dns_name: ($endpoint.id + "." + $service + "-headless." + $namespace + ".svc.cluster.local"), chat_url: $endpoint.chat_url, reset_url: $endpoint.reset_url, metrics_url: $endpoint.metrics_url}] | sort_by(.id)
' <<<"$driver_endpoints_json")"
sidecar_pod_bindings="$(jq -c --arg container "$VELASERVE_ROUTING_SIDECAR_CONTAINER_NAME" '[.items[] as $pod | ($pod.spec.initContainers[] | select(.name == $container)) as $spec | ($pod.status.initContainerStatuses[] | select(.name == $container)) as $status | {name: $pod.metadata.name, uid: $pod.metadata.uid, image: $spec.image, node_name: $pod.spec.nodeName, restart_count: $status.restartCount}] | sort_by(.uid)' <<<"$ready_pods_json")"
epp_pod_bindings="$(jq -c --arg container "$VELASERVE_EPP_CONTAINER_NAME" '[.items[] as $pod | ($pod.spec.containers[] | select(.name == $container)) as $spec | ($pod.status.containerStatuses[] | select(.name == $container)) as $status | {name: $pod.metadata.name, uid: $pod.metadata.uid, image: $spec.image, node_name: $pod.spec.nodeName, restart_count: $status.restartCount}] | sort_by(.uid)' <<<"$epp_pods_json")"
epp_proxy_pod_bindings="$(jq -c --arg container "$VELASERVE_EPP_PROXY_CONTAINER_NAME" '[.items[] as $pod | ($pod.spec.containers[] | select(.name == $container)) as $spec | ($pod.status.containerStatuses[] | select(.name == $container)) as $status | {name: $pod.metadata.name, uid: $pod.metadata.uid, image: $spec.image, node_name: $pod.spec.nodeName, restart_count: $status.restartCount}] | sort_by(.uid)' <<<"$epp_pods_json")"
controller_pod_bindings="$(jq -c --arg container "$VELASERVE_CONDITION_CONTROLLER_CONTAINER_NAME" '[.items[] as $pod | ($pod.spec.containers[] | select(.name == $container)) as $spec | ($pod.status.containerStatuses[] | select(.name == $container)) as $status | {name: $pod.metadata.name, uid: $pod.metadata.uid, image: $spec.image, node_name: $pod.spec.nodeName, restart_count: $status.restartCount}] | sort_by(.uid)' <<<"$controller_pods_json")"
driver_pod_bindings="$(jq -c --arg container "$VELASERVE_CONDITION_DRIVER_CONTAINER_NAME" '[.items[] as $pod | ($pod.spec.containers[] | select(.name == $container)) as $spec | ($pod.status.containerStatuses[] | select(.name == $container)) as $status | {name: $pod.metadata.name, uid: $pod.metadata.uid, image: $spec.image, node_name: $pod.spec.nodeName, restart_count: $status.restartCount}] | sort_by(.uid)' <<<"$driver_pods_json")"

clock_pods_json="$(kubectl --namespace "$VELASERVE_NAMESPACE" get pods -l "$CLOCK_PROBE_SELECTOR" -o json)"
timestamp_node_names="$(jq -nr \
  --argjson model "$ready_pods_json" \
  --argjson epp "$epp_pods_json" \
  --argjson gateway "$envoy_pods_json" \
  --argjson controller "$controller_pods_json" \
  --argjson driver "$driver_pods_json" \
  '[$model.items[], $epp.items[], $gateway.items[], $controller.items[], $driver.items[] | .spec.nodeName] | unique[]')"
[[ -n "$timestamp_node_names" ]] || fail "timestamp-producing pods have no nodes"
jq -e \
  --arg image "$VELASERVE_VELASERVE_IMAGE" \
  --arg container "$CLOCK_PROBE_CONTAINER" \
  --argjson expected "$(jq -Rsc 'split("\n") | map(select(length > 0)) | sort' <<<"$timestamp_node_names")" '
  [.items[] | select(.spec.nodeName as $node | $expected | index($node))] as $probes |
  ($probes | length) == ($expected | length) and
  ([$probes[].spec.nodeName] | sort) == $expected and
  all($probes[];
    .status.phase == "Running" and
    any(.status.conditions[]?; .type == "Ready" and .status == "True") and
    any(.spec.containers[]; .name == $container and .image == $image) and
    any(.status.containerStatuses[]; .name == $container and .restartCount == 0)
  )
' <<<"$clock_pods_json" >/dev/null || fail "one ready, restart-free, digest-bound clock probe is required on every timestamp-producing node"

clock_attestation_probes='[]'
clock_forward_pid=""
clock_forward_log=""
cleanup_clock_forward() {
  if [[ -n "$clock_forward_pid" ]]; then
    kill "$clock_forward_pid" 2>/dev/null || true
    wait "$clock_forward_pid" 2>/dev/null || true
  fi
  if [[ -n "$clock_forward_log" ]]; then
    rm -f "$clock_forward_log"
  fi
}
trap cleanup_clock_forward EXIT
while IFS= read -r clock_node_name; do
  [[ -n "$clock_node_name" ]] || continue
  clock_pod_json="$(jq -ec --arg node "$clock_node_name" '[.items[] | select(.spec.nodeName == $node)] | if length == 1 then .[0] else error("clock pod cardinality") end' <<<"$clock_pods_json")" || fail "clock probe cardinality changed on node $clock_node_name"
  clock_pod_name="$(jq -er '.metadata.name' <<<"$clock_pod_json")"
  clock_pod_uid="$(jq -er '.metadata.uid' <<<"$clock_pod_json")"
  clock_restart_count="$(jq -er --arg container "$CLOCK_PROBE_CONTAINER" '.status.containerStatuses[] | select(.name == $container) | .restartCount' <<<"$clock_pod_json")"
  clock_node_uid="$(kubectl get node "$clock_node_name" -o 'jsonpath={.metadata.uid}')"
  [[ -n "$clock_node_uid" ]] || fail "clock probe node $clock_node_name has no UID"

  clock_forward_log="$(mktemp)"
  kubectl --namespace "$VELASERVE_NAMESPACE" port-forward --address 127.0.0.1 "pod/${clock_pod_name}" ":${CLOCK_PROBE_REMOTE_PORT}" >"$clock_forward_log" 2>&1 &
  clock_forward_pid=$!
  clock_local_port=""
  for _ in $(seq 1 50); do
    clock_local_port="$(sed -n 's/^Forwarding from 127\.0\.0\.1:\([0-9][0-9]*\) -> .*/\1/p' "$clock_forward_log" | head -n 1)"
    [[ -n "$clock_local_port" ]] && break
    kill -0 "$clock_forward_pid" 2>/dev/null || fail "clock probe port-forward exited for node $clock_node_name"
    sleep 0.1
  done
  [[ -n "$clock_local_port" ]] || fail "clock probe port-forward was not ready for node $clock_node_name"
  clock_health=""
  for _ in $(seq 1 25); do
    if clock_health="$(curl --fail --silent --show-error --connect-timeout 1 --max-time 1 "http://127.0.0.1:${clock_local_port}/healthz" 2>/dev/null)"; then
      break
    fi
    sleep 0.1
  done
  [[ "$clock_health" == "ok" ]] || fail "clock probe health check failed for node $clock_node_name"
  clock_observation="$(go -C "$REPOSITORY_ROOT" run ./cmd/clock-probe check \
    --url "http://127.0.0.1:${clock_local_port}/v1/time" \
    --samples "$CLOCK_PROBE_SAMPLES" \
    --max-rtt "$CLOCK_PROBE_MAX_RTT" \
    --max-offset "$CLOCK_PROBE_MAX_OFFSET")" || fail "clock attestation failed for node $clock_node_name"
  clock_attestation_probes="$(jq -c \
    --arg node_name "$clock_node_name" \
    --arg node_uid "$clock_node_uid" \
    --arg pod_name "$clock_pod_name" \
    --arg pod_uid "$clock_pod_uid" \
    --arg image "$VELASERVE_VELASERVE_IMAGE" \
    --argjson restart_count "$clock_restart_count" \
    --argjson observation "$clock_observation" \
    '. + [{node_name:$node_name,node_uid:$node_uid,pod:{name:$pod_name,uid:$pod_uid,image:$image,node_name:$node_name,restart_count:$restart_count},observation:$observation}] | sort_by(.node_uid)' <<<"$clock_attestation_probes")"
  cleanup_clock_forward
  clock_forward_pid=""
  clock_forward_log=""
done <<<"$timestamp_node_names"
trap - EXIT
clock_attestation="$(jq -cn \
  --arg image "$VELASERVE_VELASERVE_IMAGE" \
  --argjson samples "$CLOCK_PROBE_SAMPLES" \
  --argjson maximum_rtt_nanoseconds "$CLOCK_PROBE_MAX_RTT_NANOSECONDS" \
  --argjson maximum_offset_nanoseconds "$CLOCK_PROBE_MAX_OFFSET_NANOSECONDS" \
  --argjson probes "$clock_attestation_probes" \
  '{image:$image,samples:$samples,maximum_rtt_nanoseconds:$maximum_rtt_nanoseconds,maximum_offset_nanoseconds:$maximum_offset_nanoseconds,probes:$probes}')"
model_spec_sha256="$(jq -S -c '[.items[] | {uid: .metadata.uid, spec: .spec}] | sort_by(.uid)' <<<"$ready_pods_json" | shasum -a 256 | awk '{print $1}')"
envoy_spec_sha256="$(jq -S -c '[.items[] | {uid: .metadata.uid, spec: .spec}] | sort_by(.uid)' <<<"$envoy_pods_json" | shasum -a 256 | awk '{print $1}')"
envoy_controller_spec_sha256="$(jq -S -c '[.items[] | {uid: .metadata.uid, spec: .spec}] | sort_by(.uid)' <<<"$envoy_controller_pods_json" | shasum -a 256 | awk '{print $1}')"
model_runtime_contract_sha256="$(jq -c -n --arg transport "$VELASERVE_P2P_TRANSPORT" --arg model "$VELASERVE_MODEL_ID" --arg revision "$VELASERVE_MODEL_REVISION" --arg model_image "$VELASERVE_MODEL_IMAGE" --arg sidecar_image "$VELASERVE_ROUTING_SIDECAR_IMAGE" --arg model_spec_sha256 "$model_spec_sha256" --arg service_uid "$model_service_uid" --arg service_spec_sha256 "$model_service_spec_sha256" --arg network_policy_sha256 "$model_network_policy_sha256" --argjson cpu_offload_bytes "$VELASERVE_CPU_OFFLOAD_BYTES" '{schema_version:"velaserve.vllm-offloading-runtime/v1", transport:$transport, model:$model, revision:$revision, model_image:$model_image, sidecar_image:$sidecar_image, block_size:64, cpu_offload_bytes:$cpu_offload_bytes, engine_port:8200, proxy_port:8000, p2p_port:7777, kv_events_port:5556, developer_api_enabled:true, reset_external:true, network_policy_sha256:$network_policy_sha256, model_spec_sha256:$model_spec_sha256, service_uid:$service_uid, service_spec_sha256:$service_spec_sha256}' | shasum -a 256 | awk '{print $1}')"
epp_spec_sha256="$(jq -S -c '[.items[] | {uid: .metadata.uid, spec: .spec}] | sort_by(.uid)' <<<"$epp_pods_json" | shasum -a 256 | awk '{print $1}')"
controller_spec_sha256="$(jq -S -c '[.items[] | {uid: .metadata.uid, spec: .spec}] | sort_by(.uid)' <<<"$controller_pods_json" | shasum -a 256 | awk '{print $1}')"
driver_spec_sha256="$(jq -S -c '[.items[] | {uid: .metadata.uid, spec: .spec}] | sort_by(.uid)' <<<"$driver_pods_json" | shasum -a 256 | awk '{print $1}')"
binding_path="${REPOSITORY_ROOT}/.tools/cloud-preflight-binding.json"
temporary_binding="$(mktemp "${binding_path}.tmp.XXXXXX")"
jq -n \
  --arg schema_version "velaserve.preflight-binding/v3" \
  --arg repository_commit "$repository_commit" \
  --arg aws_account_id "$VELASERVE_AWS_ACCOUNT_ID" \
  --arg aws_region "$VELASERVE_AWS_REGION" \
  --arg cluster_arn "$cluster_arn" \
  --arg namespace "$VELASERVE_NAMESPACE" \
  --arg endpoint "$VELASERVE_GATEWAY_CHAT_URL" \
  --arg gateway_uid "$gateway_uid" \
  --arg http_route_uid "$route_uid" \
  --arg gateway_data_plane_namespace "$envoy_namespace" \
  --arg gateway_data_plane_selector "$envoy_selector" \
  --arg gateway_data_plane_container "$VELASERVE_ENVOY_CONTAINER_NAME" \
  --arg gateway_data_plane_image "$VELASERVE_ENVOY_IMAGE" \
  --arg gateway_data_plane_spec_sha256 "$envoy_spec_sha256" \
  --argjson gateway_data_plane_pods "$envoy_pod_bindings" \
  --arg gateway_controller_namespace "$envoy_controller_namespace" \
  --arg gateway_controller_selector "$VELASERVE_ENVOY_GATEWAY_CONTROLLER_SELECTOR" \
  --arg gateway_controller_container "$VELASERVE_ENVOY_GATEWAY_CONTROLLER_CONTAINER_NAME" \
  --arg gateway_controller_image "$VELASERVE_ENVOY_GATEWAY_CONTROLLER_IMAGE" \
  --arg gateway_controller_spec_sha256 "$envoy_controller_spec_sha256" \
  --argjson gateway_controller_pods "$envoy_controller_pod_bindings" \
  --arg preregistration_sha256 "$actual_preregistration_hash" \
  --arg calibration_sha256 "$actual_calibration_hash" \
  --arg benchmark_profile_sha256 "$actual_benchmark_profile_hash" \
  --arg profile_calibration_sha256 "$actual_profile_calibration_hash" \
  --arg routing_sha256 "$routing_sha256" \
  --arg router_config_sha256 "$router_config_sha256" \
  --arg zeroing_contract_sha256 "$zeroing_contract_sha256" \
  --arg active_arm "$VELASERVE_ACTIVE_ARM" \
  --arg transport "$VELASERVE_P2P_TRANSPORT" \
  --arg model_selector "$VELASERVE_MODEL_SELECTOR" \
  --arg model_release "$VELASERVE_MODEL_RELEASE_NAME" \
  --arg model_container "$VELASERVE_MODEL_CONTAINER_NAME" \
  --arg model_workload "$VELASERVE_MODEL_WORKLOAD_NAME" \
  --arg model_service "$VELASERVE_MODEL_SERVICE_NAME" \
  --arg model_service_uid "$model_service_uid" \
  --arg model_service_spec_sha256 "$model_service_spec_sha256" \
  --arg model_id "$VELASERVE_MODEL_ID" \
  --arg model_revision "$VELASERVE_MODEL_REVISION" \
  --arg model_image "$VELASERVE_MODEL_IMAGE" \
  --argjson model_image_provenance "$vllm_image_provenance" \
  --arg model_sidecar_container "$VELASERVE_ROUTING_SIDECAR_CONTAINER_NAME" \
  --arg model_sidecar_image "$VELASERVE_ROUTING_SIDECAR_IMAGE" \
  --argjson model_sidecar_provenance "$sidecar_image_provenance" \
  --argjson model_minimum "$VELASERVE_GPU_MINIMUM_BENCHMARK_REPLICAS" \
  --argjson model_maximum "$VELASERVE_GPU_MAXIMUM_BENCHMARK_REPLICAS" \
  --argjson model_cpu_offload_bytes "$VELASERVE_CPU_OFFLOAD_BYTES" \
  --arg model_runtime_contract_sha256 "$model_runtime_contract_sha256" \
  --arg model_network_policy_sha256 "$model_network_policy_sha256" \
  --arg model_spec_sha256 "$model_spec_sha256" \
  --argjson model_pods "$model_pod_bindings" \
  --argjson model_sidecar_pods "$sidecar_pod_bindings" \
  --argjson model_tokenizer_verified_pod_uids "$tokenizer_verified_pod_uids" \
  --arg epp_selector "$VELASERVE_EPP_SELECTOR" \
  --arg epp_container "$VELASERVE_EPP_CONTAINER_NAME" \
  --arg epp_image "$VELASERVE_EPP_IMAGE" \
  --argjson epp_image_provenance "$epp_image_provenance" \
  --arg epp_proxy_container "$VELASERVE_EPP_PROXY_CONTAINER_NAME" \
  --arg epp_proxy_image "$VELASERVE_EPP_PROXY_IMAGE" \
  --argjson epp_proxy_pods "$epp_proxy_pod_bindings" \
  --argjson epp_replicas "$VELASERVE_EPP_REPLICAS" \
  --argjson epp_min_cached_token_delta "$calibrated_min_cached_token_delta" \
  --arg epp_spec_sha256 "$epp_spec_sha256" \
  --argjson epp_pods "$epp_pod_bindings" \
  --arg controller_selector "$VELASERVE_CONDITION_CONTROLLER_SELECTOR" \
  --arg controller_container "$VELASERVE_CONDITION_CONTROLLER_CONTAINER_NAME" \
  --arg controller_image "$VELASERVE_VELASERVE_IMAGE" \
  --argjson controller_image_provenance "$velaserve_image_provenance" \
  --arg controller_revision "$VELASERVE_CONTROLLER_REVISION" \
  --argjson controller_apply_timeout_seconds "$VELASERVE_CONDITION_APPLY_TIMEOUT_SECONDS" \
  --arg controller_spec_sha256 "$controller_spec_sha256" \
  --argjson controller_pods "$controller_pod_bindings" \
  --arg driver_endpoint "$VELASERVE_CONDITION_DRIVER_ENDPOINT" \
  --arg driver_selector "$VELASERVE_CONDITION_DRIVER_SELECTOR" \
  --arg driver_container "$VELASERVE_CONDITION_DRIVER_CONTAINER_NAME" \
  --arg driver_image "$VELASERVE_CONDITION_DRIVER_IMAGE" \
  --argjson driver_image_provenance "$velaserve_image_provenance" \
  --arg driver_spec_sha256 "$driver_spec_sha256" \
  --arg driver_config_sha256 "$driver_config_sha256" \
  --arg driver_load_profiles_sha256 "$driver_load_profiles_sha256" \
  --arg driver_load_calibration_sha256 "$driver_load_calibration_sha256" \
  --arg driver_control_secret_sha256 "$control_secret_sha256" \
  --argjson driver_drain_timeout_seconds "$VELASERVE_CONDITION_DRIVER_DRAIN_TIMEOUT_SECONDS" \
  --argjson driver_drain_poll_milliseconds "$VELASERVE_CONDITION_DRIVER_DRAIN_POLL_MILLISECONDS" \
  --argjson driver_endpoints "$driver_endpoint_bindings" \
  --argjson driver_pods "$driver_pod_bindings" \
  --argjson nodes "$node_bindings" \
  --argjson clock_attestation "$clock_attestation" \
  --arg observed_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  '{schema_version: $schema_version, repository_commit: $repository_commit, aws_account_id: $aws_account_id, aws_region: $aws_region, cluster_arn: $cluster_arn, namespace: $namespace, endpoint: $endpoint, gateway_uid: $gateway_uid, http_route_uid: $http_route_uid, gateway_data_plane: {namespace: $gateway_data_plane_namespace, selector: $gateway_data_plane_selector, container: $gateway_data_plane_container, image: $gateway_data_plane_image, spec_sha256: $gateway_data_plane_spec_sha256, pods: $gateway_data_plane_pods}, gateway_controller: {namespace: $gateway_controller_namespace, selector: $gateway_controller_selector, container: $gateway_controller_container, image: $gateway_controller_image, spec_sha256: $gateway_controller_spec_sha256, pods: $gateway_controller_pods}, preregistration_sha256: $preregistration_sha256, calibration_sha256: $calibration_sha256, benchmark_profile_sha256: $benchmark_profile_sha256, profile_calibration_sha256: $profile_calibration_sha256, routing_sha256: $routing_sha256, router_config_sha256: $router_config_sha256, zeroing_contract_sha256: $zeroing_contract_sha256, active_arm: $active_arm, transport: $transport, model: {release: $model_release, selector: $model_selector, container: $model_container, workload: $model_workload, service: $model_service, service_uid: $model_service_uid, service_spec_sha256: $model_service_spec_sha256, id: $model_id, revision: $model_revision, image: $model_image, image_provenance: $model_image_provenance, sidecar_container: $model_sidecar_container, sidecar_image: $model_sidecar_image, sidecar_provenance: $model_sidecar_provenance, minimum_replicas: $model_minimum, maximum_replicas: $model_maximum, block_size: 64, cpu_offload_bytes: $model_cpu_offload_bytes, engine_port: 8200, proxy_port: 8000, p2p_port: 7777, kv_events_port: 5556, runtime_contract_sha256: $model_runtime_contract_sha256, network_policy_sha256: $model_network_policy_sha256, developer_api_enabled: true, reset_external: true, spec_sha256: $model_spec_sha256, pods: $model_pods, sidecar_pods: $model_sidecar_pods, tokenizer_verified_pod_uids: $model_tokenizer_verified_pod_uids}, epp: {selector: $epp_selector, container: $epp_container, image: $epp_image, image_provenance: $epp_image_provenance, proxy_container: $epp_proxy_container, proxy_image: $epp_proxy_image, proxy_pods: $epp_proxy_pods, replicas: $epp_replicas, min_cached_token_delta: $epp_min_cached_token_delta, spec_sha256: $epp_spec_sha256, pods: $epp_pods}, condition_controller: {selector: $controller_selector, container: $controller_container, image: $controller_image, image_provenance: $controller_image_provenance, revision: $controller_revision, apply_timeout_seconds: $controller_apply_timeout_seconds, spec_sha256: $controller_spec_sha256, pods: $controller_pods}, condition_driver: {endpoint: $driver_endpoint, selector: $driver_selector, container: $driver_container, image: $driver_image, image_provenance: $driver_image_provenance, spec_sha256: $driver_spec_sha256, config_sha256: $driver_config_sha256, load_profiles_sha256: $driver_load_profiles_sha256, load_calibration_sha256: $driver_load_calibration_sha256, control_secret_sha256: $driver_control_secret_sha256, drain_timeout_seconds: $driver_drain_timeout_seconds, drain_poll_milliseconds: $driver_drain_poll_milliseconds, endpoints: $driver_endpoints, pods: $driver_pods}, nodes: $nodes, clock_attestation: $clock_attestation, observed_at: $observed_at}' >"$temporary_binding"
temporary_gateway_binding="${temporary_binding}.gateway"
jq --arg gateway_chat_url "$driver_gateway_chat_url" --arg router_config_invariant_sha256 "$router_config_invariant_sha256" '.condition_driver.gateway_chat_url = $gateway_chat_url | .router_config_invariant_sha256 = $router_config_invariant_sha256' "$temporary_binding" >"$temporary_gateway_binding"
mv "$temporary_gateway_binding" "$temporary_binding"
go -C "$REPOSITORY_ROOT" run ./cmd/zeroprobe validate-preflight --path "$temporary_binding" || fail "generated preflight binding is invalid"
mv "$temporary_binding" "$binding_path"
preflight_fingerprint="$(shasum -a 256 "$binding_path" | awk '{print $1}')"
printf '%s %s\n' "$preflight_fingerprint" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"${REPOSITORY_ROOT}/.tools/cloud-preflight.ok"
echo "cloud-preflight: PASS account=$actual_account region=$VELASERVE_AWS_REGION cluster=$VELASERVE_CLUSTER_NAME ready_replicas=$ready_replicas quota=$quota_value binding=$binding_path"

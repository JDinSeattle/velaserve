#!/usr/bin/env bash
set -euo pipefail

readonly REPOSITORY_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly PREREGISTRATION_PATH="${VELASERVE_PREREGISTRATION_PATH:-${REPOSITORY_ROOT}/research/preregistration/z0-v1.yaml}"

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

for command_name in aws kubectl jq git shasum curl; do
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
  VELASERVE_MODEL_SELECTOR \
  VELASERVE_MODEL_CONTAINER_NAME \
  VELASERVE_MODEL_WORKLOAD_NAME \
  VELASERVE_MODEL_ID \
  VELASERVE_MODEL_REVISION \
  VELASERVE_MODEL_IMAGE \
  VELASERVE_EPP_SELECTOR \
  VELASERVE_EPP_CONTAINER_NAME \
  VELASERVE_ACTIVE_ARM \
  VELASERVE_EPP_REPLICAS \
  VELASERVE_CONDITION_CONTROLLER_SELECTOR \
  VELASERVE_CONDITION_CONTROLLER_CONTAINER_NAME \
  VELASERVE_CONTROLLER_REVISION \
  VELASERVE_CONDITION_CONTROLLER_ENDPOINT \
  VELASERVE_GPU_INSTANCE_TYPES \
  VELASERVE_P2P_TRANSPORT \
  VELASERVE_ENDPOINT \
  VELASERVE_VELASERVE_IMAGE \
  VELASERVE_EPP_IMAGE \
  VELASERVE_PREREGISTRATION_SHA256 \
  VELASERVE_ORACLE_CALIBRATION \
  VELASERVE_ORACLE_CALIBRATION_SHA256; do
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
[[ "$VELASERVE_MODEL_IMAGE" == *@sha256:* ]] || fail "VELASERVE_MODEL_IMAGE must contain @sha256:"
[[ "$VELASERVE_VELASERVE_IMAGE" == *@sha256:* ]] || fail "VELASERVE_VELASERVE_IMAGE must contain @sha256:"
[[ "$VELASERVE_EPP_IMAGE" == *@sha256:* ]] || fail "VELASERVE_EPP_IMAGE must contain @sha256:"
case "$VELASERVE_ACTIVE_ARM" in arm-a-affinity-p2p|arm-b-load-aware-p2p) ;; *) fail "VELASERVE_ACTIVE_ARM is not a frozen upstream baseline" ;; esac
case "$VELASERVE_EPP_REPLICAS" in 1|2) ;; *) fail "VELASERVE_EPP_REPLICAS must be 1 or 2" ;; esac
case "$VELASERVE_P2P_TRANSPORT" in
  tcp|efa) ;;
  *) fail "VELASERVE_P2P_TRANSPORT must be tcp or efa" ;;
esac

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

current_context="$(kubectl config current-context)"
case "$current_context" in
  "$VELASERVE_CLUSTER_NAME"|*"/${VELASERVE_CLUSTER_NAME}") ;;
  *) fail "kubectl context $current_context does not target $VELASERVE_CLUSTER_NAME" ;;
esac
kubectl auth can-i get pods --namespace "$VELASERVE_NAMESPACE" | grep -Fxq yes || fail "kubectl cannot read experiment pods"

route_json="$(kubectl --namespace "$VELASERVE_NAMESPACE" get httproute velaserve -o json)"
jq -e 'any(.status.parents[]?.conditions[]?; .type == "Accepted" and .status == "True") and any(.status.parents[]?.conditions[]?; .type == "ResolvedRefs" and .status == "True")' <<<"$route_json" >/dev/null || fail "velaserve HTTPRoute is not accepted with resolved references"
route_uid="$(jq -r '.metadata.uid' <<<"$route_json")"
gateway_json="$(kubectl --namespace "$VELASERVE_NAMESPACE" get gateway velaserve-gateway -o json)"
jq -e 'any(.status.conditions[]?; .type == "Accepted" and .status == "True") and any(.status.conditions[]?; .type == "Programmed" and .status == "True")' <<<"$gateway_json" >/dev/null || fail "velaserve Envoy Gateway is not accepted and programmed"
gateway_uid="$(jq -r '.metadata.uid' <<<"$gateway_json")"
gateway_services="$(kubectl get services --all-namespaces -l gateway.envoyproxy.io/owning-gateway-name=velaserve-gateway -o json)"
[[ "$(jq '.items | length' <<<"$gateway_services")" == "1" ]] || fail "exactly one generated Envoy Gateway service is required"

ready_pods_json="$(kubectl --namespace "$VELASERVE_NAMESPACE" get pods -l "$VELASERVE_MODEL_SELECTOR" -o json)"
ready_replicas="$(jq '[.items[] | select(.status.phase == "Running") | select(any(.status.conditions[]?; .type == "Ready" and .status == "True"))] | length' <<<"$ready_pods_json")"
(( ready_replicas >= VELASERVE_GPU_MINIMUM_BENCHMARK_REPLICAS )) || fail "only $ready_replicas ready model replicas; need $VELASERVE_GPU_MINIMUM_BENCHMARK_REPLICAS"
(( ready_replicas <= VELASERVE_GPU_MAXIMUM_BENCHMARK_REPLICAS )) || fail "$ready_replicas ready model replicas exceed the declared benchmark maximum"
jq -e --arg revision "$VELASERVE_MODEL_REVISION" 'all(.items[]; .metadata.labels["velaserve.ai/model-revision"] == $revision)' <<<"$ready_pods_json" >/dev/null || fail "model pod revision labels do not match VELASERVE_MODEL_REVISION"
jq -e --arg container "$VELASERVE_MODEL_CONTAINER_NAME" --arg image "$VELASERVE_MODEL_IMAGE" '
  all(.items[];
    any(.spec.containers[]; .name == $container and .image == $image and .resources.requests["nvidia.com/gpu"] == "1" and .resources.limits["nvidia.com/gpu"] == "1") and
    all(.status.containerStatuses[]; .restartCount == 0)
  )' <<<"$ready_pods_json" >/dev/null || fail "model containers must use the bound digest, request/limit one GPU, and have zero restarts"

hpa_json="$(kubectl --namespace "$VELASERVE_NAMESPACE" get horizontalpodautoscalers -o json)"
jq -e --arg workload "$VELASERVE_MODEL_WORKLOAD_NAME" 'all(.items[]; .spec.scaleTargetRef.name != $workload)' <<<"$hpa_json" >/dev/null || fail "model workload has an active HPA; freeze replica count before Z0"

epp_pods_json="$(kubectl --namespace "$VELASERVE_NAMESPACE" get pods -l "$VELASERVE_EPP_SELECTOR" -o json)"
ready_epp_replicas="$(jq '[.items[] | select(.status.phase == "Running") | select(any(.status.conditions[]?; .type == "Ready" and .status == "True"))] | length' <<<"$epp_pods_json")"
[[ "$ready_epp_replicas" == "$VELASERVE_EPP_REPLICAS" ]] || fail "ready EPP replica count $ready_epp_replicas does not match active condition $VELASERVE_EPP_REPLICAS"
jq -e --arg container "$VELASERVE_EPP_CONTAINER_NAME" --arg image "$VELASERVE_EPP_IMAGE" 'all(.items[]; any(.spec.containers[]; .name == $container and .image == $image) and all(.status.containerStatuses[]; .restartCount == 0))' <<<"$epp_pods_json" >/dev/null || fail "EPP pods do not match the bound image digest or have restarted"

controller_pods_json="$(kubectl --namespace "$VELASERVE_NAMESPACE" get pods -l "$VELASERVE_CONDITION_CONTROLLER_SELECTOR" -o json)"
jq -e --arg container "$VELASERVE_CONDITION_CONTROLLER_CONTAINER_NAME" --arg image "$VELASERVE_VELASERVE_IMAGE" --arg revision "$VELASERVE_CONTROLLER_REVISION" '[.items[] | select(.status.phase == "Running") | select(any(.status.conditions[]?; .type == "Ready" and .status == "True"))] | length == 1 and all(.items[]; any(.spec.containers[]; .name == $container and .image == $image and any(.env[]?; .name == "VELASERVE_CONTROLLER_REVISION" and .value == $revision)) and all(.status.containerStatuses[]; .restartCount == 0))' <<<"$controller_pods_json" >/dev/null || fail "exactly one digest-bound, revision-bound, restart-free condition controller must be ready"

contract="$(kubectl --namespace "$VELASERVE_NAMESPACE" get configmap velaserve-zeroing-contract -o 'jsonpath={.data.contract\.yaml}')"
grep -Fq "evidence_scope: \"real_gpu\"" <<<"$contract" || fail "deployed zeroing contract is not real_gpu"
grep -Fq "routing_profile: \"${VELASERVE_ACTIVE_ARM}\"" <<<"$contract" || fail "deployed routing profile does not match VELASERVE_ACTIVE_ARM"
grep -Fq "epp_replicas: ${VELASERVE_EPP_REPLICAS}" <<<"$contract" || fail "deployed EPP replica contract does not match"
grep -Fq "coordination_store: absent" <<<"$contract" || fail "pre-gate deployment unexpectedly declares a coordination store"

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
  [[ "$gpu_allocatable" =~ ^[1-9][0-9]*$ ]] || fail "node $node_name exposes no allocatable NVIDIA GPU"
  node_json="$(kubectl get node "$node_name" -o json)"
  node_bindings="$(jq -c --argjson node "$node_json" '. + [{name: $node.metadata.name, uid: $node.metadata.uid, instance_type: $node.metadata.labels["node.kubernetes.io/instance-type"], gpu_allocatable: ($node.status.allocatable["nvidia.com/gpu"] | tonumber)}] | sort_by(.uid)' <<<"$node_bindings")"
done <<<"$node_names"
unique_instance_types="$(printf '%b' "$instance_types" | sort -u | sed '/^$/d' | wc -l | tr -d '[:space:]')"
[[ "$unique_instance_types" == "1" ]] || fail "GPU model pods are not homogeneous"

if [[ "$VELASERVE_P2P_TRANSPORT" == "efa" ]]; then
  while IFS= read -r node_name; do
    [[ -n "$node_name" ]] || continue
    efa_count="$(kubectl get node "$node_name" -o 'jsonpath={.status.allocatable.vpc\.amazonaws\.com/efa}')"
    [[ -n "$efa_count" && "$efa_count" != "0" ]] || fail "node $node_name exposes no EFA resource"
  done <<<"$node_names"
fi

endpoint_status="$(curl --silent --show-error --connect-timeout 10 --max-time 20 --output /dev/null --write-out '%{http_code}' "$VELASERVE_ENDPOINT")"
[[ "$endpoint_status" != "000" ]] || fail "VELASERVE_ENDPOINT is unreachable"
controller_status="$(curl --silent --show-error --connect-timeout 10 --max-time 20 --output /dev/null --write-out '%{http_code}' "$VELASERVE_CONDITION_CONTROLLER_ENDPOINT")"
[[ "$controller_status" != "000" ]] || fail "VELASERVE_CONDITION_CONTROLLER_ENDPOINT is unreachable"

mkdir -p "${REPOSITORY_ROOT}/.tools"
model_pod_bindings="$(jq -c --arg container "$VELASERVE_MODEL_CONTAINER_NAME" '[.items[] as $pod | ($pod.spec.containers[] | select(.name == $container)) as $spec | ($pod.status.containerStatuses[] | select(.name == $container)) as $status | {name: $pod.metadata.name, uid: $pod.metadata.uid, image: $spec.image, restart_count: $status.restartCount}] | sort_by(.uid)' <<<"$ready_pods_json")"
epp_pod_bindings="$(jq -c --arg container "$VELASERVE_EPP_CONTAINER_NAME" '[.items[] as $pod | ($pod.spec.containers[] | select(.name == $container)) as $spec | ($pod.status.containerStatuses[] | select(.name == $container)) as $status | {name: $pod.metadata.name, uid: $pod.metadata.uid, image: $spec.image, restart_count: $status.restartCount}] | sort_by(.uid)' <<<"$epp_pods_json")"
controller_pod_bindings="$(jq -c --arg container "$VELASERVE_CONDITION_CONTROLLER_CONTAINER_NAME" '[.items[] as $pod | ($pod.spec.containers[] | select(.name == $container)) as $spec | ($pod.status.containerStatuses[] | select(.name == $container)) as $status | {name: $pod.metadata.name, uid: $pod.metadata.uid, image: $spec.image, restart_count: $status.restartCount}] | sort_by(.uid)' <<<"$controller_pods_json")"
binding_path="${REPOSITORY_ROOT}/.tools/cloud-preflight-binding.json"
temporary_binding="$(mktemp "${binding_path}.tmp.XXXXXX")"
jq -n \
  --arg schema_version "velaserve.preflight-binding/v1" \
  --arg repository_commit "$repository_commit" \
  --arg aws_account_id "$VELASERVE_AWS_ACCOUNT_ID" \
  --arg aws_region "$VELASERVE_AWS_REGION" \
  --arg cluster_arn "$cluster_arn" \
  --arg namespace "$VELASERVE_NAMESPACE" \
  --arg endpoint "$VELASERVE_ENDPOINT" \
  --arg gateway_uid "$gateway_uid" \
  --arg http_route_uid "$route_uid" \
  --arg preregistration_sha256 "$actual_preregistration_hash" \
  --arg calibration_sha256 "$actual_calibration_hash" \
  --arg active_arm "$VELASERVE_ACTIVE_ARM" \
  --arg transport "$VELASERVE_P2P_TRANSPORT" \
  --arg model_selector "$VELASERVE_MODEL_SELECTOR" \
  --arg model_container "$VELASERVE_MODEL_CONTAINER_NAME" \
  --arg model_workload "$VELASERVE_MODEL_WORKLOAD_NAME" \
  --arg model_id "$VELASERVE_MODEL_ID" \
  --arg model_revision "$VELASERVE_MODEL_REVISION" \
  --arg model_image "$VELASERVE_MODEL_IMAGE" \
  --argjson model_minimum "$VELASERVE_GPU_MINIMUM_BENCHMARK_REPLICAS" \
  --argjson model_maximum "$VELASERVE_GPU_MAXIMUM_BENCHMARK_REPLICAS" \
  --argjson model_pods "$model_pod_bindings" \
  --arg epp_selector "$VELASERVE_EPP_SELECTOR" \
  --arg epp_container "$VELASERVE_EPP_CONTAINER_NAME" \
  --arg epp_image "$VELASERVE_EPP_IMAGE" \
  --argjson epp_replicas "$VELASERVE_EPP_REPLICAS" \
  --argjson epp_pods "$epp_pod_bindings" \
  --arg controller_selector "$VELASERVE_CONDITION_CONTROLLER_SELECTOR" \
  --arg controller_container "$VELASERVE_CONDITION_CONTROLLER_CONTAINER_NAME" \
  --arg controller_image "$VELASERVE_VELASERVE_IMAGE" \
  --arg controller_revision "$VELASERVE_CONTROLLER_REVISION" \
  --argjson controller_pods "$controller_pod_bindings" \
  --argjson nodes "$node_bindings" \
  --arg observed_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  '{schema_version: $schema_version, repository_commit: $repository_commit, aws_account_id: $aws_account_id, aws_region: $aws_region, cluster_arn: $cluster_arn, namespace: $namespace, endpoint: $endpoint, gateway_uid: $gateway_uid, http_route_uid: $http_route_uid, preregistration_sha256: $preregistration_sha256, calibration_sha256: $calibration_sha256, active_arm: $active_arm, transport: $transport, model: {selector: $model_selector, container: $model_container, workload: $model_workload, id: $model_id, revision: $model_revision, image: $model_image, minimum_replicas: $model_minimum, maximum_replicas: $model_maximum, pods: $model_pods}, epp: {selector: $epp_selector, container: $epp_container, image: $epp_image, replicas: $epp_replicas, pods: $epp_pods}, condition_controller: {selector: $controller_selector, container: $controller_container, image: $controller_image, revision: $controller_revision, pods: $controller_pods}, nodes: $nodes, observed_at: $observed_at}' >"$temporary_binding"
mv "$temporary_binding" "$binding_path"
preflight_fingerprint="$(printf '%s\n' \
  "$VELASERVE_AWS_ACCOUNT_ID" "$VELASERVE_AWS_REGION" "$VELASERVE_CLUSTER_NAME" \
  "$VELASERVE_ARTIFACT_BUCKET" "$VELASERVE_MODEL_ID" "$VELASERVE_MODEL_REVISION" \
  "$VELASERVE_MODEL_IMAGE" "$VELASERVE_VELASERVE_IMAGE" "$VELASERVE_EPP_IMAGE" "$VELASERVE_ACTIVE_ARM" "$VELASERVE_EPP_REPLICAS" "$VELASERVE_CONTROLLER_REVISION" "$actual_preregistration_hash" \
  "$actual_calibration_hash" | shasum -a 256 | awk '{print $1}')"
printf '%s %s\n' "$preflight_fingerprint" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"${REPOSITORY_ROOT}/.tools/cloud-preflight.ok"
echo "cloud-preflight: PASS account=$actual_account region=$VELASERVE_AWS_REGION cluster=$VELASERVE_CLUSTER_NAME ready_replicas=$ready_replicas quota=$quota_value binding=$binding_path"

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
  VELASERVE_MODEL_SELECTOR \
  VELASERVE_MODEL_ID \
  VELASERVE_MODEL_REVISION \
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
(( VELASERVE_GPU_MINIMUM_BENCHMARK_REPLICAS >= 6 )) || fail "real-GPU Z0 requires at least six homogeneous replicas"
[[ "$VELASERVE_GPU_QUOTA_REQUIRED" =~ ^[0-9]+([.][0-9]+)?$ ]] || fail "VELASERVE_GPU_QUOTA_REQUIRED must be numeric"
[[ "$VELASERVE_MODEL_ID" =~ ^[A-Za-z0-9._/-]+$ ]] || fail "VELASERVE_MODEL_ID contains unsupported characters"
[[ "$VELASERVE_MODEL_REVISION" =~ ^[A-Fa-f0-9]{40,64}$ ]] || fail "VELASERVE_MODEL_REVISION must be an immutable commit or content digest"
[[ "$VELASERVE_VELASERVE_IMAGE" == *@sha256:* ]] || fail "VELASERVE_VELASERVE_IMAGE must contain @sha256:"
[[ "$VELASERVE_EPP_IMAGE" == *@sha256:* ]] || fail "VELASERVE_EPP_IMAGE must contain @sha256:"
case "$VELASERVE_P2P_TRANSPORT" in
  tcp|efa) ;;
  *) fail "VELASERVE_P2P_TRANSPORT must be tcp or efa" ;;
esac

actual_preregistration_hash="$(shasum -a 256 "$PREREGISTRATION_PATH" | awk '{print $1}')"
[[ "$actual_preregistration_hash" == "$VELASERVE_PREREGISTRATION_SHA256" ]] || fail "preregistration SHA-256 mismatch"
git -C "$REPOSITORY_ROOT" diff --quiet HEAD -- "${PREREGISTRATION_PATH#${REPOSITORY_ROOT}/}" || fail "preregistration differs from Git HEAD"
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
  [[ "$without_digest" == "${VELASERVE_AWS_ACCOUNT_ID}.dkr.ecr.${VELASERVE_AWS_REGION}.amazonaws.com/"* ]] || fail "image is outside the expected account and region: $reference"
  aws ecr describe-images --region "$VELASERVE_AWS_REGION" --repository-name "$repository" --image-ids "imageDigest=$digest" >/dev/null
}
verify_ecr_digest "$VELASERVE_VELASERVE_IMAGE"
verify_ecr_digest "$VELASERVE_EPP_IMAGE"

current_context="$(kubectl config current-context)"
case "$current_context" in
  "$VELASERVE_CLUSTER_NAME"|*"/${VELASERVE_CLUSTER_NAME}") ;;
  *) fail "kubectl context $current_context does not target $VELASERVE_CLUSTER_NAME" ;;
esac
kubectl auth can-i get pods --namespace "$VELASERVE_NAMESPACE" | grep -Fxq yes || fail "kubectl cannot read experiment pods"

ready_pods_json="$(kubectl --namespace "$VELASERVE_NAMESPACE" get pods -l "$VELASERVE_MODEL_SELECTOR" -o json)"
ready_replicas="$(jq '[.items[] | select(.status.phase == "Running") | select(any(.status.conditions[]?; .type == "Ready" and .status == "True"))] | length' <<<"$ready_pods_json")"
(( ready_replicas >= VELASERVE_GPU_MINIMUM_BENCHMARK_REPLICAS )) || fail "only $ready_replicas ready model replicas; need $VELASERVE_GPU_MINIMUM_BENCHMARK_REPLICAS"

node_names="$(jq -r '.items[] | select(.status.phase == "Running") | .spec.nodeName' <<<"$ready_pods_json" | sort -u)"
[[ -n "$node_names" ]] || fail "ready model pods have no nodes"
instance_types=""
while IFS= read -r node_name; do
  [[ -n "$node_name" ]] || continue
  instance_type="$(kubectl get node "$node_name" -o 'jsonpath={.metadata.labels.node\.kubernetes\.io/instance-type}')"
  case ",${VELASERVE_GPU_INSTANCE_TYPES}," in
    *",${instance_type},"*) ;;
    *) fail "node $node_name uses unapproved instance type $instance_type" ;;
  esac
  instance_types="${instance_types}${instance_type}\n"
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

mkdir -p "${REPOSITORY_ROOT}/.tools"
preflight_fingerprint="$(printf '%s\n' \
  "$VELASERVE_AWS_ACCOUNT_ID" "$VELASERVE_AWS_REGION" "$VELASERVE_CLUSTER_NAME" \
  "$VELASERVE_ARTIFACT_BUCKET" "$VELASERVE_MODEL_ID" "$VELASERVE_MODEL_REVISION" \
  "$VELASERVE_VELASERVE_IMAGE" "$VELASERVE_EPP_IMAGE" "$actual_preregistration_hash" \
  "$actual_calibration_hash" | shasum -a 256 | awk '{print $1}')"
printf '%s %s\n' "$preflight_fingerprint" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"${REPOSITORY_ROOT}/.tools/cloud-preflight.ok"
echo "cloud-preflight: PASS account=$actual_account region=$VELASERVE_AWS_REGION cluster=$VELASERVE_CLUSTER_NAME ready_replicas=$ready_replicas quota=$quota_value"

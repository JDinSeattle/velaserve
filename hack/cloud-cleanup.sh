#!/usr/bin/env bash
set -euo pipefail

readonly REPOSITORY_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly HELM="${HELM:-${REPOSITORY_ROOT}/.tools/bin/helm}"

require_env() {
  local name="$1"
  [[ -n "${!name:-}" ]] || { echo "cloud-cleanup: $name is required" >&2; exit 1; }
}

for command_name in aws kubectl jq shasum; do
  command -v "$command_name" >/dev/null 2>&1 || { echo "cloud-cleanup: $command_name is required" >&2; exit 1; }
done
[[ -x "$HELM" ]] || { echo "cloud-cleanup: pinned Helm is required at $HELM; run hack/bootstrap-tools.sh or set HELM" >&2; exit 1; }

for variable_name in \
  VELASERVE_CLUSTER_NAME \
  VELASERVE_NAMESPACE \
  VELASERVE_ARTIFACT_ROOT \
  VELASERVE_ARTIFACT_BUCKET \
  VELASERVE_AWS_REGION \
  VELASERVE_ARTIFACTS_COLLECTED \
  VELASERVE_CONFIRM_CLEANUP; do
  require_env "$variable_name"
done
[[ "$VELASERVE_ARTIFACTS_COLLECTED" == "true" ]] || { echo "cloud-cleanup: set VELASERVE_ARTIFACTS_COLLECTED=true only after cloud-collect succeeds" >&2; exit 1; }
[[ -f "$VELASERVE_ARTIFACT_ROOT/.artifacts-collected" && ! -L "$VELASERVE_ARTIFACT_ROOT/.artifacts-collected" ]] || { echo "cloud-cleanup: local collection marker is absent or unsafe" >&2; exit 1; }
[[ -f "$VELASERVE_ARTIFACT_ROOT/preflight-binding.json" && ! -L "$VELASERVE_ARTIFACT_ROOT/preflight-binding.json" ]] || { echo "cloud-cleanup: immutable preflight binding is absent" >&2; exit 1; }
run_id="$(jq -r '.run_id' "$VELASERVE_ARTIFACT_ROOT/manifest.json")"
expected_confirmation="${VELASERVE_CLUSTER_NAME}/${run_id}"
[[ "$VELASERVE_CONFIRM_CLEANUP" == "$expected_confirmation" ]] || { echo "cloud-cleanup: confirmation must exactly equal $expected_confirmation" >&2; exit 1; }

binding="$VELASERVE_ARTIFACT_ROOT/preflight-binding.json"
bound_account="$(jq -er '.aws_account_id | select(type == "string" and test("^[0-9]{12}$"))' "$binding")"
bound_region="$(jq -er '.aws_region | select(type == "string" and length > 0)' "$binding")"
bound_cluster_arn="$(jq -er '.cluster_arn | select(type == "string" and length > 0)' "$binding")"
bound_namespace="$(jq -er '.namespace | select(type == "string" and length > 0)' "$binding")"
bound_model_release="$(jq -er '.model.release | select(type == "string" and length > 0)' "$binding")"
[[ "$bound_region" == "$VELASERVE_AWS_REGION" ]] || { echo "cloud-cleanup: requested region differs from the run binding" >&2; exit 1; }
[[ "$bound_namespace" == "$VELASERVE_NAMESPACE" ]] || { echo "cloud-cleanup: requested namespace differs from the run binding" >&2; exit 1; }
expected_destination="s3://${VELASERVE_ARTIFACT_BUCKET}/runs/${run_id}"
marker="$VELASERVE_ARTIFACT_ROOT/.artifacts-collected"
marker_destination="$(jq -er '.destination | select(type == "string" and length > 0)' "$marker")"
marker_run_id="$(jq -er '.run_id | select(type == "string" and length > 0)' "$marker")"
marker_ledger_sha256="$(jq -er '.ledger_sha256 | select(type == "string" and test("^[0-9a-f]{64}$"))' "$marker")"
marker_ledger_bytes="$(jq -er '.ledger_bytes | select(type == "number" and floor == . and . > 0)' "$marker")"
marker_ledger_version_id="$(jq -er '.ledger_version_id | select(type == "string" and length > 0)' "$marker")"
jq -e '.schema_version == "velaserve.artifacts-collected/v1" and (.verified_at | type == "string" and length > 0)' "$marker" >/dev/null || { echo "cloud-cleanup: artifact marker schema is invalid" >&2; exit 1; }
[[ "$marker_destination" == "$expected_destination" && "$marker_run_id" == "$run_id" ]] || { echo "cloud-cleanup: artifact marker does not bind the requested bucket and run" >&2; exit 1; }
local_ledger_sha256="$(shasum -a 256 "$VELASERVE_ARTIFACT_ROOT/ledger.jsonl" | awk '{print $1}')"
local_ledger_bytes="$(wc -c <"$VELASERVE_ARTIFACT_ROOT/ledger.jsonl" | tr -d '[:space:]')"
[[ "$marker_ledger_sha256" == "$local_ledger_sha256" && "$marker_ledger_bytes" == "$local_ledger_bytes" ]] || { echo "cloud-cleanup: local ledger no longer matches the verified upload marker" >&2; exit 1; }

actual_account="$(aws sts get-caller-identity --region "$VELASERVE_AWS_REGION" --query Account --output text)"
[[ "$actual_account" == "$bound_account" ]] || { echo "cloud-cleanup: active AWS account differs from the run binding" >&2; exit 1; }
actual_cluster_arn="$(aws eks describe-cluster --name "$VELASERVE_CLUSTER_NAME" --region "$VELASERVE_AWS_REGION" --query 'cluster.arn' --output text)"
[[ "$actual_cluster_arn" == "$bound_cluster_arn" ]] || { echo "cloud-cleanup: active EKS cluster ARN differs from the run binding" >&2; exit 1; }

current_context="$(kubectl config current-context)"
case "$current_context" in
  "$VELASERVE_CLUSTER_NAME"|*"/${VELASERVE_CLUSTER_NAME}") ;;
  *) echo "cloud-cleanup: kubectl context $current_context does not target $VELASERVE_CLUSTER_NAME" >&2; exit 1 ;;
esac
remote_ledger="$(mktemp)"
trap 'rm -f "$remote_ledger"' EXIT
aws s3api get-object \
  --bucket "$VELASERVE_ARTIFACT_BUCKET" \
  --key "runs/${run_id}/ledger.jsonl" \
  --version-id "$marker_ledger_version_id" \
  --region "$VELASERVE_AWS_REGION" \
  "$remote_ledger" >/dev/null
remote_ledger_sha256="$(shasum -a 256 "$remote_ledger" | awk '{print $1}')"
remote_ledger_bytes="$(wc -c <"$remote_ledger" | tr -d '[:space:]')"
[[ "$remote_ledger_sha256" == "$marker_ledger_sha256" && "$remote_ledger_bytes" == "$marker_ledger_bytes" ]] || { echo "cloud-cleanup: exact uploaded ledger version no longer matches the verified marker" >&2; exit 1; }
rm -f "$remote_ledger"
trap - EXIT

for release_name in "$bound_model_release" velaserve-support velaserve; do
  if "$HELM" --namespace "$VELASERVE_NAMESPACE" status "$release_name" >/dev/null 2>&1; then
    "$HELM" --namespace "$VELASERVE_NAMESPACE" uninstall "$release_name"
  fi
done
kubectl --namespace "$VELASERVE_NAMESPACE" delete jobs -l app.kubernetes.io/part-of=velaserve --ignore-not-found=true

if [[ "${VELASERVE_DELETE_GPU_NODEPOOL:-false}" == "true" ]]; then
  kubectl delete nodepool.karpenter.sh velaserve-gpu-z0 --ignore-not-found=true
  kubectl delete ec2nodeclass.karpenter.k8s.aws velaserve-gpu-z0 --ignore-not-found=true
fi

echo "cloud-cleanup: exact VelaServe workloads removed; Terraform-managed VPC, EKS, ECR, S3, and IAM were intentionally retained"

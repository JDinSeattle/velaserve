#!/usr/bin/env bash
set -euo pipefail

require_env() {
  local name="$1"
  [[ -n "${!name:-}" ]] || { echo "cloud-cleanup: $name is required" >&2; exit 1; }
}

for command_name in aws kubectl jq helm; do
  command -v "$command_name" >/dev/null 2>&1 || { echo "cloud-cleanup: $command_name is required" >&2; exit 1; }
done

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
[[ -f "$VELASERVE_ARTIFACT_ROOT/.artifacts-collected" ]] || { echo "cloud-cleanup: local collection marker is absent" >&2; exit 1; }
run_id="$(jq -r '.run_id' "$VELASERVE_ARTIFACT_ROOT/manifest.json")"
expected_confirmation="${VELASERVE_CLUSTER_NAME}/${run_id}"
[[ "$VELASERVE_CONFIRM_CLEANUP" == "$expected_confirmation" ]] || { echo "cloud-cleanup: confirmation must exactly equal $expected_confirmation" >&2; exit 1; }

current_context="$(kubectl config current-context)"
case "$current_context" in
  "$VELASERVE_CLUSTER_NAME"|*"/${VELASERVE_CLUSTER_NAME}") ;;
  *) echo "cloud-cleanup: kubectl context $current_context does not target $VELASERVE_CLUSTER_NAME" >&2; exit 1 ;;
esac
aws s3api head-object \
  --bucket "$VELASERVE_ARTIFACT_BUCKET" \
  --key "runs/${run_id}/ledger.jsonl" \
  --region "$VELASERVE_AWS_REGION" >/dev/null

for release_name in velaserve-support velaserve; do
  if helm --namespace "$VELASERVE_NAMESPACE" status "$release_name" >/dev/null 2>&1; then
    helm --namespace "$VELASERVE_NAMESPACE" uninstall "$release_name"
  fi
done
kubectl --namespace "$VELASERVE_NAMESPACE" delete jobs -l app.kubernetes.io/part-of=velaserve --ignore-not-found=true

if [[ "${VELASERVE_DELETE_GPU_NODEPOOL:-false}" == "true" ]]; then
  kubectl delete nodepool.karpenter.sh velaserve-gpu-z0 --ignore-not-found=true
  kubectl delete ec2nodeclass.karpenter.k8s.aws velaserve-gpu-z0 --ignore-not-found=true
fi

echo "cloud-cleanup: exact VelaServe workloads removed; Terraform-managed VPC, EKS, ECR, S3, and IAM were intentionally retained"

#!/usr/bin/env bash
set -euo pipefail

readonly REPOSITORY_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

require_env() {
  local name="$1"
  [[ -n "${!name:-}" ]] || { echo "cloud-collect: $name is required" >&2; exit 1; }
}

for variable_name in \
  VELASERVE_ARTIFACT_ROOT \
  VELASERVE_ARTIFACT_BUCKET \
  VELASERVE_AWS_REGION \
  VELASERVE_CLUSTER_NAME \
  VELASERVE_NAMESPACE \
  VELASERVE_EPP_RECORDS_PATH \
  VELASERVE_ENVOY_RECORDS_PATH \
  VELASERVE_ORACLE_CALIBRATION; do
  require_env "$variable_name"
done
for command_name in aws kubectl jq go shasum curl find; do
  command -v "$command_name" >/dev/null 2>&1 || { echo "cloud-collect: $command_name is required" >&2; exit 1; }
done
[[ -d "$VELASERVE_ARTIFACT_ROOT" && ! -L "$VELASERVE_ARTIFACT_ROOT" ]] || { echo "cloud-collect: artifact root must be an existing real directory" >&2; exit 1; }
[[ -f "$VELASERVE_EPP_RECORDS_PATH" && ! -L "$VELASERVE_EPP_RECORDS_PATH" ]] || { echo "cloud-collect: normalized EPP JSONL is required" >&2; exit 1; }
[[ -f "$VELASERVE_ENVOY_RECORDS_PATH" && ! -L "$VELASERVE_ENVOY_RECORDS_PATH" ]] || { echo "cloud-collect: normalized Envoy JSONL is required" >&2; exit 1; }
[[ ! -e "$VELASERVE_ARTIFACT_ROOT/.artifacts-collected" ]] || { echo "cloud-collect: bundle was already uploaded" >&2; exit 1; }

cp "$VELASERVE_EPP_RECORDS_PATH" "$VELASERVE_ARTIFACT_ROOT/epp.jsonl"
cp "$VELASERVE_ENVOY_RECORDS_PATH" "$VELASERVE_ARTIFACT_ROOT/envoy.jsonl"
if [[ -n "${VELASERVE_SOURCE_PRESSURE_PATH:-}" ]]; then
  [[ -f "$VELASERVE_SOURCE_PRESSURE_PATH" && ! -L "$VELASERVE_SOURCE_PRESSURE_PATH" ]] || { echo "cloud-collect: source-pressure path is not a regular file" >&2; exit 1; }
  cp "$VELASERVE_SOURCE_PRESSURE_PATH" "$VELASERVE_ARTIFACT_ROOT/source-pressure.jsonl"
fi

cd "$REPOSITORY_ROOT"
go run ./cmd/zeroprobe ingest --artifact-root "$VELASERVE_ARTIFACT_ROOT"
go run ./cmd/zeroprobe analyze \
  --artifact-root "$VELASERVE_ARTIFACT_ROOT" \
  --calibration "$VELASERVE_ORACLE_CALIBRATION" \
  --evidence-scope real_gpu

snapshot_directory="$VELASERVE_ARTIFACT_ROOT/cloud-snapshot"
mkdir -p "$snapshot_directory"
kubectl --namespace "$VELASERVE_NAMESPACE" get pods,services,deployments,jobs -o yaml >"$snapshot_directory/workloads.yaml"
kubectl get inferencepools.inference.networking.k8s.io --all-namespaces -o yaml >"$snapshot_directory/inferencepools.yaml"
kubectl get nodepools.karpenter.sh,ec2nodeclasses.karpenter.k8s.aws -o yaml >"$snapshot_directory/karpenter.yaml"
kubectl --namespace "$VELASERVE_NAMESPACE" logs -l app.kubernetes.io/part-of=velaserve --all-containers --prefix --tail=-1 >"$snapshot_directory/velaserve.log" 2>&1
aws eks describe-cluster --name "$VELASERVE_CLUSTER_NAME" --region "$VELASERVE_AWS_REGION" --output json >"$snapshot_directory/eks-cluster.json"
if [[ -n "${VELASERVE_METRICS_URL:-}" ]]; then
  curl --fail --silent --show-error "$VELASERVE_METRICS_URL" >"$snapshot_directory/metrics.txt"
fi
printf 'cluster=%s\nregion=%s\nnamespace=%s\ncollected_at=%s\n' \
  "$VELASERVE_CLUSTER_NAME" "$VELASERVE_AWS_REGION" "$VELASERVE_NAMESPACE" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"$snapshot_directory/environment.txt"

while IFS= read -r snapshot_file; do
  relative_path="${snapshot_file#${VELASERVE_ARTIFACT_ROOT}/}"
  go run ./cmd/artifact-record --artifact-root "$VELASERVE_ARTIFACT_ROOT" --relative "$relative_path"
done < <(find "$snapshot_directory" -type f -print | sort)

go run ./cmd/zeroprobe verify --artifact-root "$VELASERVE_ARTIFACT_ROOT"
run_id="$(jq -r '.run_id' "$VELASERVE_ARTIFACT_ROOT/manifest.json")"
[[ "$run_id" =~ ^z0-[a-f0-9]{32}$ ]] || { echo "cloud-collect: invalid run ID" >&2; exit 1; }
destination="s3://${VELASERVE_ARTIFACT_BUCKET}/runs/${run_id}"
if aws s3 ls "${destination}/" --region "$VELASERVE_AWS_REGION" | grep -q .; then
  echo "cloud-collect: destination already contains objects" >&2
  exit 1
fi
aws s3 sync "$VELASERVE_ARTIFACT_ROOT" "${destination}/" --region "$VELASERVE_AWS_REGION" --only-show-errors
aws s3api head-object \
  --bucket "$VELASERVE_ARTIFACT_BUCKET" \
  --key "runs/${run_id}/ledger.jsonl" \
  --region "$VELASERVE_AWS_REGION" >/dev/null
printf '%s\n' "$destination" >"$VELASERVE_ARTIFACT_ROOT/.artifacts-collected"
echo "cloud-collect: uploaded and verified $destination"
echo "export VELASERVE_ARTIFACTS_COLLECTED=true"

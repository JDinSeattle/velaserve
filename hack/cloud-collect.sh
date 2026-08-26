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
  VELASERVE_EPP_LOG_PATH \
  VELASERVE_ENVOY_RAW_PATH \
  VELASERVE_ORACLE_CALIBRATION; do
  require_env "$variable_name"
done
for command_name in aws kubectl jq go shasum curl find; do
  command -v "$command_name" >/dev/null 2>&1 || { echo "cloud-collect: $command_name is required" >&2; exit 1; }
done
[[ -d "$VELASERVE_ARTIFACT_ROOT" && ! -L "$VELASERVE_ARTIFACT_ROOT" ]] || { echo "cloud-collect: artifact root must be an existing real directory" >&2; exit 1; }
run_phase="$(jq -r '.phase' "$VELASERVE_ARTIFACT_ROOT/manifest.json")"
if [[ "$run_phase" == "z0-c" && -z "${VELASERVE_VLLM_RAW_PATH:-}" ]]; then
  echo "cloud-collect: Z0-C requires VELASERVE_VLLM_RAW_PATH from the pinned vLLM observer" >&2
  exit 1
fi
[[ -f "$VELASERVE_EPP_LOG_PATH" && ! -L "$VELASERVE_EPP_LOG_PATH" ]] || { echo "cloud-collect: raw log from the pinned observational EPP build is required" >&2; exit 1; }
[[ -f "$VELASERVE_ENVOY_RAW_PATH" && ! -L "$VELASERVE_ENVOY_RAW_PATH" ]] || { echo "cloud-collect: binding-marked raw inner-Envoy log is required" >&2; exit 1; }
[[ ! -e "$VELASERVE_ARTIFACT_ROOT/.artifacts-collected" && ! -L "$VELASERVE_ARTIFACT_ROOT/.artifacts-collected" ]] || { echo "cloud-collect: bundle was already uploaded or has an unsafe marker" >&2; exit 1; }

# Re-attest the live stack before mutating the raw bundle. The validated
# invariant normalizes only per-run profile metadata and dynamic clock samples;
# pod/node/probe identities, restart counts, bounds, specs, routes, images, and
# loaded driver configuration remain exact start/end invariants.
[[ -f "$VELASERVE_ARTIFACT_ROOT/preflight-binding.json" && ! -L "$VELASERVE_ARTIFACT_ROOT/preflight-binding.json" ]] || { echo "cloud-collect: initial preflight binding is required" >&2; exit 1; }
initial_preflight_invariant="$(go -C "$REPOSITORY_ROOT" run ./cmd/zeroprobe hash-preflight --path "$VELASERVE_ARTIFACT_ROOT/preflight-binding.json" | jq -er '.invariant_sha256')"
"${REPOSITORY_ROOT}/hack/cloud-preflight.sh"
final_preflight_invariant="$(go -C "$REPOSITORY_ROOT" run ./cmd/zeroprobe hash-preflight --path "${REPOSITORY_ROOT}/.tools/cloud-preflight-binding.json" | jq -er '.invariant_sha256')"
[[ "$initial_preflight_invariant" == "$final_preflight_invariant" ]] || {
    echo "cloud-collect: live deployment drifted or restarted after the initial preflight" >&2
    exit 1
  }

cd "$REPOSITORY_ROOT"
[[ ! -e "$VELASERVE_ARTIFACT_ROOT/epp-raw.log" && ! -L "$VELASERVE_ARTIFACT_ROOT/epp-raw.log" ]] || { echo "cloud-collect: epp-raw.log destination already exists" >&2; exit 1; }
cp "$VELASERVE_EPP_LOG_PATH" "$VELASERVE_ARTIFACT_ROOT/epp-raw.log"
[[ ! -e "$VELASERVE_ARTIFACT_ROOT/epp.jsonl" && ! -L "$VELASERVE_ARTIFACT_ROOT/epp.jsonl" ]] || { echo "cloud-collect: epp.jsonl destination already exists" >&2; exit 1; }
go run ./cmd/epp-normalize --input "$VELASERVE_ARTIFACT_ROOT/epp-raw.log" --output "$VELASERVE_ARTIFACT_ROOT/epp.jsonl"
[[ ! -e "$VELASERVE_ARTIFACT_ROOT/envoy-raw.log" && ! -L "$VELASERVE_ARTIFACT_ROOT/envoy-raw.log" ]] || { echo "cloud-collect: envoy-raw.log destination already exists" >&2; exit 1; }
cp "$VELASERVE_ENVOY_RAW_PATH" "$VELASERVE_ARTIFACT_ROOT/envoy-raw.log"
[[ ! -e "$VELASERVE_ARTIFACT_ROOT/envoy.jsonl" && ! -L "$VELASERVE_ARTIFACT_ROOT/envoy.jsonl" ]] || { echo "cloud-collect: envoy.jsonl destination already exists" >&2; exit 1; }
go run ./cmd/envoy-normalize --input "$VELASERVE_ARTIFACT_ROOT/envoy-raw.log" --output "$VELASERVE_ARTIFACT_ROOT/envoy.jsonl"
if [[ -n "${VELASERVE_VLLM_RAW_PATH:-}" ]]; then
  [[ "$run_phase" == "z0-c" ]] || { echo "cloud-collect: vLLM runtime telemetry is valid only for Z0-C" >&2; exit 1; }
	  [[ -f "$VELASERVE_VLLM_RAW_PATH" && ! -L "$VELASERVE_VLLM_RAW_PATH" ]] || { echo "cloud-collect: raw vLLM runtime path is not a regular file" >&2; exit 1; }
	  [[ ! -e "$VELASERVE_ARTIFACT_ROOT/vllm-raw.log" && ! -L "$VELASERVE_ARTIFACT_ROOT/vllm-raw.log" ]] || { echo "cloud-collect: vllm-raw.log destination already exists" >&2; exit 1; }
	  cp "$VELASERVE_VLLM_RAW_PATH" "$VELASERVE_ARTIFACT_ROOT/vllm-raw.log"
  [[ ! -e "$VELASERVE_ARTIFACT_ROOT/vllm-runtime.jsonl" && ! -L "$VELASERVE_ARTIFACT_ROOT/vllm-runtime.jsonl" ]] || { echo "cloud-collect: vLLM runtime destination already exists" >&2; exit 1; }
	  go run ./cmd/vllm-normalize --input "$VELASERVE_ARTIFACT_ROOT/vllm-raw.log" --output "$VELASERVE_ARTIFACT_ROOT/vllm-runtime.jsonl"
fi

go run ./cmd/zeroprobe ingest --artifact-root "$VELASERVE_ARTIFACT_ROOT"
for raw_relative_path in epp-raw.log envoy-raw.log; do
  go run ./cmd/artifact-record --artifact-root "$VELASERVE_ARTIFACT_ROOT" --relative "$raw_relative_path"
done
if [[ "$run_phase" == "z0-c" ]]; then
	  go run ./cmd/artifact-record --artifact-root "$VELASERVE_ARTIFACT_ROOT" --relative vllm-raw.log
  go run ./cmd/p2p-runtime-normalize \
    --groups "$VELASERVE_ARTIFACT_ROOT/groups.jsonl" \
    --placements "$VELASERVE_ARTIFACT_ROOT/placements.jsonl" \
    --runtime "$VELASERVE_ARTIFACT_ROOT/vllm-runtime.jsonl" \
    --preflight-binding "$VELASERVE_ARTIFACT_ROOT/preflight-binding.json" \
    --output "$VELASERVE_ARTIFACT_ROOT/p2p-transfers.jsonl"
  go run ./cmd/source-pressure-compile \
    --groups "$VELASERVE_ARTIFACT_ROOT/groups.jsonl" \
    --placements "$VELASERVE_ARTIFACT_ROOT/placements.jsonl" \
    --transfers "$VELASERVE_ARTIFACT_ROOT/p2p-transfers.jsonl" \
    --output "$VELASERVE_ARTIFACT_ROOT/source-pressure.jsonl"
  for relative_path in vllm-runtime.jsonl p2p-transfers.jsonl source-pressure.jsonl; do
    go run ./cmd/artifact-record --artifact-root "$VELASERVE_ARTIFACT_ROOT" --relative "$relative_path"
  done
fi
go run ./cmd/zeroprobe analyze \
  --artifact-root "$VELASERVE_ARTIFACT_ROOT" \
  --calibration "$VELASERVE_ORACLE_CALIBRATION" \
  --evidence-scope real_gpu

snapshot_directory="$VELASERVE_ARTIFACT_ROOT/cloud-snapshot"
[[ ! -e "$snapshot_directory" && ! -L "$snapshot_directory" ]] || { echo "cloud-collect: cloud snapshot destination already exists" >&2; exit 1; }
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

go run ./cmd/zeroprobe seal --artifact-root "$VELASERVE_ARTIFACT_ROOT"
go run ./cmd/zeroprobe verify --artifact-root "$VELASERVE_ARTIFACT_ROOT"
run_id="$(jq -r '.run_id' "$VELASERVE_ARTIFACT_ROOT/manifest.json")"
[[ "$run_id" =~ ^z0-[a-f0-9]{32}$ ]] || { echo "cloud-collect: invalid run ID" >&2; exit 1; }
destination="s3://${VELASERVE_ARTIFACT_BUCKET}/runs/${run_id}"
if aws s3 ls "${destination}/" --region "$VELASERVE_AWS_REGION" | grep -q .; then
  echo "cloud-collect: destination already contains objects" >&2
  exit 1
fi
aws s3 sync "$VELASERVE_ARTIFACT_ROOT" "${destination}/" --region "$VELASERVE_AWS_REGION" --only-show-errors
verification_directory="$(mktemp -d)"
trap 'rm -rf "$verification_directory"' EXIT
ledger_key="runs/${run_id}/ledger.jsonl"
ledger_metadata="$(aws s3api get-object \
  --bucket "$VELASERVE_ARTIFACT_BUCKET" \
  --key "$ledger_key" \
  --region "$VELASERVE_AWS_REGION" \
  "$verification_directory/ledger.jsonl")"
ledger_version_id="$(jq -er '.VersionId | select(type == "string" and length > 0)' <<<"$ledger_metadata")"
local_ledger_sha256="$(shasum -a 256 "$VELASERVE_ARTIFACT_ROOT/ledger.jsonl" | awk '{print $1}')"
remote_ledger_sha256="$(shasum -a 256 "$verification_directory/ledger.jsonl" | awk '{print $1}')"
local_ledger_bytes="$(wc -c <"$VELASERVE_ARTIFACT_ROOT/ledger.jsonl" | tr -d '[:space:]')"
remote_ledger_bytes="$(wc -c <"$verification_directory/ledger.jsonl" | tr -d '[:space:]')"
[[ "$remote_ledger_sha256" == "$local_ledger_sha256" && "$remote_ledger_bytes" == "$local_ledger_bytes" ]] || { echo "cloud-collect: remote ledger bytes do not match the sealed local ledger" >&2; exit 1; }

remote_keys_json="$(aws s3api list-objects-v2 \
  --bucket "$VELASERVE_ARTIFACT_BUCKET" \
  --prefix "runs/${run_id}/" \
  --region "$VELASERVE_AWS_REGION" \
  --query 'Contents[].Key' \
  --output json)"
jq -n -e \
  --argjson actual "$remote_keys_json" \
  --slurpfile ledger "$VELASERVE_ARTIFACT_ROOT/ledger.jsonl" \
  --arg prefix "runs/${run_id}/" \
  '(([$ledger[].relative_path | $prefix + .] + [$prefix + "ledger.jsonl"]) | sort) == (($actual // []) | sort)' >/dev/null || {
    echo "cloud-collect: remote run prefix is not the exact ledgered object set" >&2
    exit 1
  }

while IFS=$'\t' read -r relative_path expected_sha256 expected_bytes; do
  rm -f "$verification_directory/object"
  aws s3api get-object \
    --bucket "$VELASERVE_ARTIFACT_BUCKET" \
    --key "runs/${run_id}/${relative_path}" \
    --region "$VELASERVE_AWS_REGION" \
    "$verification_directory/object" >/dev/null
  actual_sha256="$(shasum -a 256 "$verification_directory/object" | awk '{print $1}')"
  actual_bytes="$(wc -c <"$verification_directory/object" | tr -d '[:space:]')"
  [[ "$actual_sha256" == "$expected_sha256" && "$actual_bytes" == "$expected_bytes" ]] || {
    echo "cloud-collect: remote object $relative_path does not match its sealed ledger entry" >&2
    exit 1
  }
done < <(jq -r '[.relative_path, .sha256, (.bytes | tostring)] | @tsv' "$VELASERVE_ARTIFACT_ROOT/ledger.jsonl")

marker_tmp="$(mktemp "${VELASERVE_ARTIFACT_ROOT}/.artifacts-collected.tmp.XXXXXX")"
jq -n \
  --arg schema_version "velaserve.artifacts-collected/v1" \
  --arg destination "$destination" \
  --arg run_id "$run_id" \
  --arg ledger_sha256 "$local_ledger_sha256" \
  --arg ledger_version_id "$ledger_version_id" \
  --argjson ledger_bytes "$local_ledger_bytes" \
  --arg verified_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  '{schema_version:$schema_version,destination:$destination,run_id:$run_id,ledger_sha256:$ledger_sha256,ledger_bytes:$ledger_bytes,ledger_version_id:$ledger_version_id,verified_at:$verified_at}' >"$marker_tmp"
mv "$marker_tmp" "$VELASERVE_ARTIFACT_ROOT/.artifacts-collected"
rm -rf "$verification_directory"
trap - EXIT
echo "cloud-collect: uploaded and verified $destination"
echo "export VELASERVE_ARTIFACTS_COLLECTED=true"

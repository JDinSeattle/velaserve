#!/usr/bin/env bash
set -euo pipefail

readonly REPOSITORY_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly PROFILE_TEMPLATE="${REPOSITORY_ROOT}/benchmarks/profiles/aws-z0-template.yaml"

require_env() {
  local name="$1"
  [[ -n "${!name:-}" ]] || { echo "cloud-run-z0: $name is required" >&2; exit 1; }
}

command -v go >/dev/null 2>&1 || { echo "cloud-run-z0: go is required" >&2; exit 1; }

for variable_name in VELASERVE_ARTIFACT_ROOT VELASERVE_ACTIVE_ARM VELASERVE_EPP_REPLICAS VELASERVE_MODEL_ID VELASERVE_ENDPOINT; do
  require_env "$variable_name"
done
case "$VELASERVE_ACTIVE_ARM" in
  arm-a-affinity-p2p|arm-b-load-aware-p2p) ;;
  *) echo "cloud-run-z0: active arm must be a frozen Stage 1 upstream P2P baseline" >&2; exit 1 ;;
esac
case "$VELASERVE_EPP_REPLICAS" in
  1|2) ;;
  *) echo "cloud-run-z0: EPP replicas must be 1 or 2" >&2; exit 1 ;;
esac
[[ "$VELASERVE_MODEL_ID" =~ ^[A-Za-z0-9._/-]+$ ]] || { echo "cloud-run-z0: invalid model ID" >&2; exit 1; }
[[ ! -e "$VELASERVE_ARTIFACT_ROOT" ]] || { echo "cloud-run-z0: artifact root already exists" >&2; exit 1; }

"${REPOSITORY_ROOT}/hack/cloud-preflight.sh"

phase="${VELASERVE_Z0_PHASE:-z0-a}"
case "$phase" in
  z0-a|z0-b|z0-c) ;;
  *) echo "cloud-run-z0: VELASERVE_Z0_PHASE must be z0-a, z0-b, or z0-c" >&2; exit 1 ;;
esac
group_limit="${VELASERVE_GROUP_LIMIT:-0}"
[[ "$group_limit" =~ ^[0-9]+$ ]] || { echo "cloud-run-z0: group limit must be an integer" >&2; exit 1; }

temporary_directory="$(mktemp -d)"
trap 'rm -rf "$temporary_directory"' EXIT
generated_profile="${temporary_directory}/aws-z0-${VELASERVE_ACTIVE_ARM}-epp-${VELASERVE_EPP_REPLICAS}.yaml"
awk \
  -v model="$VELASERVE_MODEL_ID" \
  -v arm="$VELASERVE_ACTIVE_ARM" \
  -v epp="$VELASERVE_EPP_REPLICAS" \
  -v transport="$VELASERVE_P2P_TRANSPORT" '
    /^model:/ { print "model: " model; next }
    /^arms:/ { print "arms: [" arm "]"; next }
    /^epp_replicas:/ { print "epp_replicas: [" epp "]"; next }
    /^transports:/ { print "transports: [" transport "]"; next }
    { print }
  ' "$PROFILE_TEMPLATE" >"$generated_profile"

cd "$REPOSITORY_ROOT"
go run ./cmd/zeroprobe run \
  --phase "$phase" \
  --profile "${VELASERVE_PREREGISTRATION_PATH:-research/preregistration/z0-v1.yaml}" \
  --benchmark-profile "$generated_profile" \
  --artifact-root "$VELASERVE_ARTIFACT_ROOT" \
  --endpoint "$VELASERVE_ENDPOINT" \
  --limit "$group_limit"

mkdir -p "${REPOSITORY_ROOT}/.tools"
printf '%s\n' "$VELASERVE_ARTIFACT_ROOT" >"${REPOSITORY_ROOT}/.tools/last-cloud-artifact-root"
echo "cloud-run-z0: raw run retained at $VELASERVE_ARTIFACT_ROOT; collect normalized EPP and Envoy records before analysis"

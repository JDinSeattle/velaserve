#!/usr/bin/env bash
set -euo pipefail

readonly REPOSITORY_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly CHART="${REPOSITORY_ROOT}/deploy/helm/velaserve"
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
done

cd "$REPOSITORY_ROOT"
env \
  HELM="$HELM" \
  GOCACHE="${REPOSITORY_ROOT}/.cache/go-build" \
  GOMODCACHE="${REPOSITORY_ROOT}/.cache/go-mod" \
  go test ./tests/integration -run 'TestStageOneChart|TestArmsPin' -count=1
echo "stage-one Helm render verified for both frozen arms"

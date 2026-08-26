#!/usr/bin/env bash
set -euo pipefail

readonly REPOSITORY_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
fail() { echo "cloud-freeze-profile: $*" >&2; exit 1; }
require_env() { [[ -n "${!1:-}" ]] || fail "$1 is required"; }

for command_name in kubectl jq go sed git; do command -v "$command_name" >/dev/null 2>&1 || fail "$command_name is required"; done
for variable_name in \
  VELASERVE_NAMESPACE \
  VELASERVE_MODEL_SELECTOR \
  VELASERVE_MODEL_ID \
  VELASERVE_MODEL_REVISION \
  VELASERVE_Z0_PHASE \
  VELASERVE_ACTIVE_ARM \
  VELASERVE_EPP_REPLICAS \
  VELASERVE_P2P_TRANSPORT \
  VELASERVE_CROSSOVER_BUNDLE \
  VELASERVE_BENCHMARK_PROFILE \
  VELASERVE_PROFILE_CALIBRATION \
  VELASERVE_ROUTER_CALIBRATION_VALUES; do
  require_env "$variable_name"
done
git -C "$REPOSITORY_ROOT" diff --quiet HEAD -- . || fail "tracked repository files differ from Git HEAD"
[[ -z "$(git -C "$REPOSITORY_ROOT" status --porcelain --untracked-files=normal)" ]] || fail "repository must be clean before profile freeze"

pod="$(kubectl --namespace "$VELASERVE_NAMESPACE" get pods -l "$VELASERVE_MODEL_SELECTOR" -o json | jq -er '[.items[] | select(.status.phase=="Running") | select(any(.status.conditions[]?;.type=="Ready" and .status=="True"))] | sort_by(.metadata.uid) | .[0].metadata.name')"
work_root="$(mktemp -d)"
forward_pid=''
cleanup() {
  [[ -z "$forward_pid" ]] || kill "$forward_pid" 2>/dev/null || true
  [[ -z "$forward_pid" ]] || wait "$forward_pid" 2>/dev/null || true
  rm -rf "$work_root"
}
trap cleanup EXIT INT TERM
forward_log="$work_root/forward.log"
kubectl --namespace "$VELASERVE_NAMESPACE" port-forward --address 127.0.0.1 "pod/$pod" :8200 >"$forward_log" 2>&1 &
forward_pid="$!"
local_port=''
for _ in {1..100}; do
  local_port="$(sed -n 's/^Forwarding from 127\.0\.0\.1:\([0-9][0-9]*\) -> .*/\1/p' "$forward_log" | head -n 1)"
  [[ -n "$local_port" ]] && break
  kill -0 "$forward_pid" 2>/dev/null || fail "tokenizer port-forward exited"
  sleep 0.1
done
[[ -n "$local_port" ]] || fail "tokenizer port-forward did not become ready"

go -C "$REPOSITORY_ROOT" run ./cmd/profile-freeze generate \
  --crossover-bundle "$VELASERVE_CROSSOVER_BUNDLE" \
  --tokenize-url "http://127.0.0.1:${local_port}/tokenize" \
  --tokenizer-info-url "http://127.0.0.1:${local_port}/get_tokenizer_info" \
  --model "$VELASERVE_MODEL_ID" \
  --revision "$VELASERVE_MODEL_REVISION" \
  --phase "$VELASERVE_Z0_PHASE" \
  --arm "$VELASERVE_ACTIVE_ARM" \
  --epp-replicas "$VELASERVE_EPP_REPLICAS" \
  --transport "$VELASERVE_P2P_TRANSPORT" \
  --output-profile "$VELASERVE_BENCHMARK_PROFILE" \
  --output-calibration "$VELASERVE_PROFILE_CALIBRATION" \
  --output-router-values "$VELASERVE_ROUTER_CALIBRATION_VALUES"

trap - EXIT
cleanup
echo "cloud-freeze-profile: generated benchmark profile, calibration, and measured router threshold"

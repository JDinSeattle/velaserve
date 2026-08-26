#!/usr/bin/env bash
set -euo pipefail

readonly REPOSITORY_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

require_env() {
  local name="$1"
  [[ -n "${!name:-}" ]] || { echo "cloud-run-z0: $name is required" >&2; exit 1; }
}

command -v go >/dev/null 2>&1 || { echo "cloud-run-z0: go is required" >&2; exit 1; }

for variable_name in VELASERVE_ARTIFACT_ROOT VELASERVE_ACTIVE_ARM VELASERVE_EPP_REPLICAS VELASERVE_MODEL_ID VELASERVE_ENDPOINT VELASERVE_CONDITION_CONTROLLER_ENDPOINT VELASERVE_CONDITION_CONTROL_TOKEN VELASERVE_BENCHMARK_PROFILE VELASERVE_PROFILE_CALIBRATION VELASERVE_LOAD_CALIBRATION VELASERVE_PROFILE_CALIBRATION_SHA256 VELASERVE_CROSSOVER_BUNDLE; do
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

phase="${VELASERVE_Z0_PHASE:-z0-a}"
case "$phase" in
  z0-a|z0-b|z0-c) ;;
  *) echo "cloud-run-z0: VELASERVE_Z0_PHASE must be z0-a, z0-b, or z0-c" >&2; exit 1 ;;
esac
VELASERVE_Z0_PHASE="$phase" "${REPOSITORY_ROOT}/hack/cloud-preflight.sh"
group_limit="${VELASERVE_GROUP_LIMIT:-0}"
[[ "$group_limit" =~ ^[0-9]+$ ]] || { echo "cloud-run-z0: group limit must be an integer" >&2; exit 1; }

for command_name in kubectl jq; do
  command -v "$command_name" >/dev/null 2>&1 || { echo "cloud-run-z0: $command_name is required for durable evidence streaming" >&2; exit 1; }
done
for variable_name in VELASERVE_NAMESPACE VELASERVE_EPP_CONTAINER_NAME VELASERVE_EPP_PROXY_CONTAINER_NAME VELASERVE_EPP_LOG_PATH VELASERVE_ENVOY_RAW_PATH VELASERVE_ENVOY_RECORDS_PATH; do
  require_env "$variable_name"
done
[[ ! -e "$VELASERVE_EPP_LOG_PATH" && ! -L "$VELASERVE_EPP_LOG_PATH" ]] || { echo "cloud-run-z0: VELASERVE_EPP_LOG_PATH already exists" >&2; exit 1; }
[[ ! -e "$VELASERVE_ENVOY_RAW_PATH" && ! -L "$VELASERVE_ENVOY_RAW_PATH" ]] || { echo "cloud-run-z0: VELASERVE_ENVOY_RAW_PATH already exists" >&2; exit 1; }
[[ ! -e "$VELASERVE_ENVOY_RECORDS_PATH" && ! -L "$VELASERVE_ENVOY_RECORDS_PATH" ]] || { echo "cloud-run-z0: VELASERVE_ENVOY_RECORDS_PATH already exists" >&2; exit 1; }
if [[ "$phase" == "z0-c" ]]; then
  for variable_name in VELASERVE_MODEL_CONTAINER_NAME VELASERVE_VLLM_RAW_PATH VELASERVE_VLLM_RUNTIME_PATH; do
    require_env "$variable_name"
  done
  [[ ! -e "$VELASERVE_VLLM_RUNTIME_PATH" && ! -L "$VELASERVE_VLLM_RUNTIME_PATH" ]] || { echo "cloud-run-z0: VELASERVE_VLLM_RUNTIME_PATH already exists" >&2; exit 1; }
  [[ ! -e "$VELASERVE_VLLM_RAW_PATH" && ! -L "$VELASERVE_VLLM_RAW_PATH" ]] || { echo "cloud-run-z0: VELASERVE_VLLM_RAW_PATH already exists" >&2; exit 1; }
fi

preflight_path="${REPOSITORY_ROOT}/.tools/cloud-preflight-binding.json"
[[ -f "$preflight_path" && ! -L "$preflight_path" ]] || { echo "cloud-run-z0: validated preflight binding is missing" >&2; exit 1; }
preflight_json="$(jq -ec 'select(.schema_version == "velaserve.preflight-binding/v3")' "$preflight_path")"
epp_stream_bindings="$(jq -c '.epp.pods | sort_by(.uid)' <<<"$preflight_json")"
[[ "$(jq 'length' <<<"$epp_stream_bindings")" == "$VELASERVE_EPP_REPLICAS" ]] || { echo "cloud-run-z0: preflight EPP pod set does not match the declared replica count" >&2; exit 1; }
model_stream_bindings='[]'
if [[ "$phase" == "z0-c" ]]; then
  model_stream_bindings="$(jq -c '.model.pods | sort_by(.uid)' <<<"$preflight_json")"
  (( $(jq 'length' <<<"$model_stream_bindings") > 0 )) || { echo "cloud-run-z0: preflight has no model pods for runtime streaming" >&2; exit 1; }
fi

stream_root="$(mktemp -d)"
mkdir -p "$stream_root/epp" "$stream_root/envoy" "$stream_root/vllm"
stream_pids=()
stream_temporaries=()
cleanup_streams() {
  for cleanup_pid in "${stream_pids[@]}"; do
    kill "$cleanup_pid" 2>/dev/null || true
  done
  for cleanup_pid in "${stream_pids[@]}"; do
    wait "$cleanup_pid" 2>/dev/null || true
  done
  for cleanup_temporary in "${stream_temporaries[@]}"; do
    [[ ! -e "$cleanup_temporary" ]] || rm -f "$cleanup_temporary"
  done
  [[ -z "$stream_root" || ! -d "$stream_root" ]] || rm -rf "$stream_root"
}

require_live_pod_binding() {
  local pod_name="$1"
  local pod_uid="$2"
  local container_name="$3"
  local expected_image="$4"
  local live_pod
  live_pod="$(kubectl --namespace "$VELASERVE_NAMESPACE" get pod "$pod_name" -o json)"
  jq -e \
    --arg uid "$pod_uid" \
    --arg container "$container_name" \
    --arg image "$expected_image" '
    .metadata.uid == $uid and
    .status.phase == "Running" and
    any(.status.conditions[]?; .type == "Ready" and .status == "True") and
    any(.spec.containers[]; .name == $container and .image == $image) and
    any(.status.containerStatuses[]; .name == $container and .restartCount == 0)
  ' <<<"$live_pod" >/dev/null || { echo "cloud-run-z0: live pod $pod_name no longer matches its preflight UID/container/image binding" >&2; exit 1; }
}

while IFS=$'\t' read -r pod pod_uid epp_image proxy_image; do
  [[ -n "$pod" && -n "$pod_uid" && -n "$epp_image" && -n "$proxy_image" ]] || continue
  require_live_pod_binding "$pod" "$pod_uid" "$VELASERVE_EPP_CONTAINER_NAME" "$epp_image"
  require_live_pod_binding "$pod" "$pod_uid" "$VELASERVE_EPP_PROXY_CONTAINER_NAME" "$proxy_image"
  kubectl --namespace "$VELASERVE_NAMESPACE" logs --follow "$pod" -c "$VELASERVE_EPP_CONTAINER_NAME" --since=1s --timestamps --prefix >"$stream_root/epp/$pod.log" 2>&1 &
  stream_pids+=("$!")
  kubectl --namespace "$VELASERVE_NAMESPACE" logs --follow "$pod" -c "$VELASERVE_EPP_PROXY_CONTAINER_NAME" --since=1s --timestamps --prefix >"$stream_root/envoy/$pod.log" 2>&1 &
  stream_pids+=("$!")
done < <(jq -r '. as $root | $root.epp.pods[] | [.name,.uid,.image,$root.epp.proxy_image] | @tsv' <<<"$preflight_json")
if [[ "$phase" == "z0-c" ]]; then
  while IFS=$'\t' read -r pod pod_uid model_image; do
    [[ -n "$pod" && -n "$pod_uid" && -n "$model_image" ]] || continue
    require_live_pod_binding "$pod" "$pod_uid" "$VELASERVE_MODEL_CONTAINER_NAME" "$model_image"
    kubectl --namespace "$VELASERVE_NAMESPACE" logs --follow "$pod" -c "$VELASERVE_MODEL_CONTAINER_NAME" --since=1s --timestamps --prefix >"$stream_root/vllm/$pod.log" 2>&1 &
    stream_pids+=("$!")
  done < <(jq -r '.model.pods[] | [.name,.uid,.image] | @tsv' <<<"$preflight_json")
fi
sleep 2
for stream_pid in "${stream_pids[@]}"; do
  kill -0 "$stream_pid" 2>/dev/null || { echo "cloud-run-z0: a durable evidence stream exited before benchmark start" >&2; exit 1; }
done
trap cleanup_streams EXIT INT TERM

cd "$REPOSITORY_ROOT"
run_status=0
go run ./cmd/zeroprobe run \
  --phase "$phase" \
  --profile "${VELASERVE_PREREGISTRATION_PATH:-research/preregistration/z0-v1.yaml}" \
  --benchmark-profile "$VELASERVE_BENCHMARK_PROFILE" \
  --profile-calibration "$VELASERVE_PROFILE_CALIBRATION" \
  --load-calibration "$VELASERVE_LOAD_CALIBRATION" \
  --crossover-bundle "$VELASERVE_CROSSOVER_BUNDLE" \
  --artifact-root "$VELASERVE_ARTIFACT_ROOT" \
  --endpoint "$VELASERVE_ENDPOINT" \
  --limit "$group_limit" \
  --condition-controller "$VELASERVE_CONDITION_CONTROLLER_ENDPOINT" \
  --preflight-binding "${REPOSITORY_ROOT}/.tools/cloud-preflight-binding.json" \
  --require-condition-attestation || run_status=$?

stream_failed=0
for stream_pid in "${stream_pids[@]}"; do
  kill -0 "$stream_pid" 2>/dev/null || stream_failed=1
done
for stream_pid in "${stream_pids[@]}"; do
  kill "$stream_pid" 2>/dev/null || true
done
for stream_pid in "${stream_pids[@]}"; do
  wait "$stream_pid" 2>/dev/null || true
done
stream_pids=()
(( stream_failed == 0 )) || { echo "cloud-run-z0: a durable evidence stream ended during the benchmark; evidence is incomplete" >&2; exit 1; }

mkdir -p "$(dirname "$VELASERVE_EPP_LOG_PATH")" "$(dirname "$VELASERVE_ENVOY_RAW_PATH")" "$(dirname "$VELASERVE_ENVOY_RECORDS_PATH")"
epp_temporary="$(mktemp "${VELASERVE_EPP_LOG_PATH}.tmp.XXXXXX")"
envoy_raw_temporary="$(mktemp "${VELASERVE_ENVOY_RAW_PATH}.tmp.XXXXXX")"
stream_temporaries+=("$epp_temporary" "$envoy_raw_temporary")
while IFS=$'\t' read -r pod pod_uid; do
  [[ -n "$pod" && -n "$pod_uid" ]] || continue
  jq -cn --arg pod_name "$pod" --arg pod_uid "$pod_uid" '{schema_version:"velaserve.stream-emitter-binding/v1",pod_name:$pod_name,pod_uid:$pod_uid}' | sed 's/^/VELASERVE_STREAM_BINDING /' >>"$epp_temporary"
  cat "$stream_root/epp/$pod.log" >>"$epp_temporary"
  jq -cn --arg pod_name "$pod" --arg pod_uid "$pod_uid" '{schema_version:"velaserve.stream-emitter-binding/v1",pod_name:$pod_name,pod_uid:$pod_uid}' | sed 's/^/VELASERVE_STREAM_BINDING /' >>"$envoy_raw_temporary"
  cat "$stream_root/envoy/$pod.log" >>"$envoy_raw_temporary"
done < <(jq -r '.[] | [.name,.uid] | @tsv' <<<"$epp_stream_bindings")
grep -Fq "VELASERVE_EPP_RECORD " "$epp_temporary" || { echo "cloud-run-z0: durable EPP streams contain no observer records" >&2; exit 1; }
grep -Fq 'REQ(X-REQUEST-ID)' "$envoy_raw_temporary" || { echo "cloud-run-z0: durable inner-Envoy streams contain no request records" >&2; exit 1; }
mv "$epp_temporary" "$VELASERVE_EPP_LOG_PATH"
mv "$envoy_raw_temporary" "$VELASERVE_ENVOY_RAW_PATH"
stream_temporaries=()
go -C "$REPOSITORY_ROOT" run ./cmd/envoy-normalize --input "$VELASERVE_ENVOY_RAW_PATH" --output "$VELASERVE_ENVOY_RECORDS_PATH"

if [[ "$phase" == "z0-c" ]]; then
  mkdir -p "$(dirname "$VELASERVE_VLLM_RAW_PATH")" "$(dirname "$VELASERVE_VLLM_RUNTIME_PATH")"
  vllm_raw_temporary="$(mktemp "${VELASERVE_VLLM_RAW_PATH}.tmp.XXXXXX")"
  stream_temporaries+=("$vllm_raw_temporary")
  while IFS=$'\t' read -r pod pod_uid; do
    [[ -n "$pod" && -n "$pod_uid" ]] || continue
    jq -cn --arg pod_name "$pod" --arg pod_uid "$pod_uid" '{schema_version:"velaserve.stream-emitter-binding/v1",pod_name:$pod_name,pod_uid:$pod_uid}' | sed 's/^/VELASERVE_STREAM_BINDING /' >>"$vllm_raw_temporary"
    cat "$stream_root/vllm/$pod.log" >>"$vllm_raw_temporary"
  done < <(jq -r '.[] | [.name,.uid] | @tsv' <<<"$model_stream_bindings")
  grep -Fq "VELASERVE_VLLM_RUNTIME " "$vllm_raw_temporary" || { echo "cloud-run-z0: durable streams contain no vLLM observer records" >&2; exit 1; }
  mv "$vllm_raw_temporary" "$VELASERVE_VLLM_RAW_PATH"
  stream_temporaries=()
  go -C "$REPOSITORY_ROOT" run ./cmd/vllm-normalize --input "$VELASERVE_VLLM_RAW_PATH" --output "$VELASERVE_VLLM_RUNTIME_PATH"
fi
rm -rf "$stream_root"
stream_root=""
trap - EXIT
(( run_status == 0 )) || exit "$run_status"

mkdir -p "${REPOSITORY_ROOT}/.tools"
printf '%s\n' "$VELASERVE_ARTIFACT_ROOT" >"${REPOSITORY_ROOT}/.tools/last-cloud-artifact-root"
echo "cloud-run-z0: raw run retained at $VELASERVE_ARTIFACT_ROOT; collect normalized EPP and Envoy records before analysis"

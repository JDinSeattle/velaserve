#!/usr/bin/env bash
set -euo pipefail

readonly REPOSITORY_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

fail() {
  echo "cloud-calibrate-crossover: $*" >&2
  exit 1
}

require_env() {
  local name="$1"
  [[ -n "${!name:-}" ]] || fail "$name is required"
}

for command_name in kubectl jq git go shasum; do
  command -v "$command_name" >/dev/null 2>&1 || fail "$command_name is required"
done
for variable_name in \
  VELASERVE_NAMESPACE \
  VELASERVE_MODEL_SELECTOR \
  VELASERVE_MODEL_CONTAINER_NAME \
  VELASERVE_ROUTING_SIDECAR_CONTAINER_NAME \
  VELASERVE_MODEL_ID \
  VELASERVE_MODEL_REVISION \
  VELASERVE_MODEL_IMAGE \
  VELASERVE_EPP_SELECTOR \
  VELASERVE_EPP_CONTAINER_NAME \
  VELASERVE_EPP_IMAGE \
  VELASERVE_EPP_REPLICAS \
  VELASERVE_ACTIVE_ARM \
  VELASERVE_CROSSOVER_BUNDLE; do
  require_env "$variable_name"
done
[[ "$VELASERVE_MODEL_IMAGE" =~ ^.+@sha256:[0-9a-f]{64}$ ]] || fail "model image must be digest-addressed"
[[ "$VELASERVE_EPP_IMAGE" =~ ^.+@sha256:[0-9a-f]{64}$ ]] || fail "EPP image must be digest-addressed"
case "$VELASERVE_EPP_REPLICAS" in 1|2) ;; *) fail "EPP replicas must be 1 or 2" ;; esac
case "$VELASERVE_ACTIVE_ARM" in arm-a-affinity-p2p|arm-b-load-aware-p2p) ;; *) fail "active arm is invalid" ;; esac
[[ ! -e "$VELASERVE_CROSSOVER_BUNDLE" && ! -L "$VELASERVE_CROSSOVER_BUNDLE" ]] || fail "crossover bundle output already exists"

git -C "$REPOSITORY_ROOT" diff --quiet HEAD -- . || fail "tracked repository files differ from Git HEAD"
[[ -z "$(git -C "$REPOSITORY_ROOT" status --porcelain --untracked-files=normal)" ]] || fail "repository must be clean before cloud calibration"
repository_commit="$(git -C "$REPOSITORY_ROOT" rev-parse HEAD)"

model_pods_json="$(kubectl --namespace "$VELASERVE_NAMESPACE" get pods -l "$VELASERVE_MODEL_SELECTOR" -o json)"
replica_count="$(jq '[.items[] | select(.status.phase == "Running") | select(any(.status.conditions[]?; .type == "Ready" and .status == "True"))] | length' <<<"$model_pods_json")"
(( replica_count >= 2 )) || fail "at least two ready model pods are required"
jq -e --arg engine "$VELASERVE_MODEL_CONTAINER_NAME" --arg sidecar "$VELASERVE_ROUTING_SIDECAR_CONTAINER_NAME" --arg image "$VELASERVE_MODEL_IMAGE" '
  (.items | length) >= 2 and all(.items[];
    .status.phase == "Running" and
    any(.status.conditions[]?; .type == "Ready" and .status == "True") and
    any(.spec.containers[]; .name == $engine and .image == $image) and
    any(.spec.initContainers[]; .name == $sidecar) and
    any(.status.containerStatuses[]; .name == $engine and .restartCount == 0) and
    any(.status.initContainerStatuses[]; .name == $sidecar and .restartCount == 0)
  )
' <<<"$model_pods_json" >/dev/null || fail "model pods do not match the pinned engine/sidecar contract"
[[ "$replica_count" == "$(jq '.items | length' <<<"$model_pods_json")" ]] || fail "every selected model pod must be ready"

epp_pods_json="$(kubectl --namespace "$VELASERVE_NAMESPACE" get pods -l "$VELASERVE_EPP_SELECTOR" -o json)"
[[ "$(jq '.items | length' <<<"$epp_pods_json")" == "$VELASERVE_EPP_REPLICAS" ]] || fail "EPP replica count differs"
jq -e --arg container "$VELASERVE_EPP_CONTAINER_NAME" --arg image "$VELASERVE_EPP_IMAGE" 'all(.items[]; any(.spec.containers[]; .name == $container and .image == $image) and any(.status.containerStatuses[]; .name == $container and .restartCount == 0))' <<<"$epp_pods_json" >/dev/null || fail "EPP image or restart count differs"

router_config_names="$(jq -r '[.items[] | .spec.volumes[]? | .configMap.name] | unique[]' <<<"$epp_pods_json")"
[[ -n "$router_config_names" ]] || fail "EPP pods reference no ConfigMaps"
router_config_json='[]'
while IFS= read -r config_name; do
  [[ -n "$config_name" ]] || continue
  config_json="$(kubectl --namespace "$VELASERVE_NAMESPACE" get configmap "$config_name" -o json)"
  router_config_json="$(jq -c --argjson item "$config_json" '. + [$item]' <<<"$router_config_json")"
done <<<"$router_config_names"
router_config_invariant_sha256="$(jq -S -c '[.[] | {name: .metadata.name, uid: .metadata.uid, data: .data, binary_data: .binaryData}] | sort_by(.uid) | walk(if type == "string" then gsub("minCachedTokenDelta:[[:space:]]*[0-9]+"; "minCachedTokenDelta: <MEASURED>") else . end)' <<<"$router_config_json" | shasum -a 256 | awk '{print $1}')"
model_spec_sha256="$(jq -S -c '[.items[] | {uid: .metadata.uid, spec: .spec}] | sort_by(.uid)' <<<"$model_pods_json" | shasum -a 256 | awk '{print $1}')"

instance_types=''
gpu_models=''
gpu_drivers=''
while IFS=$'\t' read -r pod node; do
  [[ -n "$pod" && -n "$node" ]] || continue
  instance_type="$(kubectl get node "$node" -o 'jsonpath={.metadata.labels.node\.kubernetes\.io/instance-type}')"
  gpu_identity="$(kubectl --namespace "$VELASERVE_NAMESPACE" exec "$pod" -c "$VELASERVE_MODEL_CONTAINER_NAME" -- nvidia-smi --query-gpu=name,driver_version --format=csv,noheader)"
  gpu_model="$(cut -d, -f1 <<<"$gpu_identity" | sed 's/[[:space:]]*$//' | sort -u)"
  gpu_driver="$(cut -d, -f2- <<<"$gpu_identity" | sed 's/^[[:space:]]*//' | sort -u)"
  instance_types="${instance_types}${instance_type}"$'\n'
  gpu_models="${gpu_models}${gpu_model}"$'\n'
  gpu_drivers="${gpu_drivers}${gpu_driver}"$'\n'
done < <(jq -r '.items[] | [.metadata.name,.spec.nodeName] | @tsv' <<<"$model_pods_json")
instance_type="$(sort -u <<<"$instance_types" | sed '/^$/d')"
gpu_model="$(sort -u <<<"$gpu_models" | sed '/^$/d')"
gpu_driver="$(sort -u <<<"$gpu_drivers" | sed '/^$/d')"
[[ "$(wc -l <<<"$instance_type" | tr -d '[:space:]')" == "1" && "$(wc -l <<<"$gpu_model" | tr -d '[:space:]')" == "1" && "$(wc -l <<<"$gpu_driver" | tr -d '[:space:]')" == "1" ]] || fail "model fleet hardware is not homogeneous"

work_root="$(mktemp -d)"
stream_pids=()
forward_pids=()
cleanup() {
  for pid in "${stream_pids[@]}"; do kill "$pid" 2>/dev/null || true; done
  for pid in "${stream_pids[@]}"; do wait "$pid" 2>/dev/null || true; done
  for pid in "${forward_pids[@]}"; do kill "$pid" 2>/dev/null || true; done
  for pid in "${forward_pids[@]}"; do wait "$pid" 2>/dev/null || true; done
  rm -rf "$work_root"
}
trap cleanup EXIT INT TERM

forwarded_port=''
start_forward() {
  local pod="$1"
  local remote_port="$2"
  local label="$3"
  local log_path="$work_root/forward-${pod}-${label}.log"
  local pid
  kubectl --namespace "$VELASERVE_NAMESPACE" port-forward --address 127.0.0.1 "pod/$pod" ":$remote_port" >"$log_path" 2>&1 &
  pid="$!"
  forward_pids+=("$pid")
  forwarded_port=''
  for _ in {1..100}; do
    forwarded_port="$(sed -n 's/^Forwarding from 127\.0\.0\.1:\([0-9][0-9]*\) -> .*/\1/p' "$log_path" | head -n 1)"
    [[ -n "$forwarded_port" ]] && return 0
    kill -0 "$pid" 2>/dev/null || fail "port-forward for $pod $label exited: $(tr '\n' ' ' <"$log_path")"
    sleep 0.1
  done
  fail "port-forward for $pod $label did not become ready"
}

endpoints='[]'
while IFS=$'\t' read -r pod pod_uid pod_ip; do
  [[ -n "$pod" && -n "$pod_uid" && -n "$pod_ip" ]] || fail "model endpoint identity is incomplete"
  start_forward "$pod" 8200 engine
  engine_port="$forwarded_port"
  start_forward "$pod" 8000 proxy
  proxy_port="$forwarded_port"
  endpoint="$(jq -cn \
    --arg id "$pod" \
    --arg pod_uid "$pod_uid" \
    --arg engine "http://127.0.0.1:$engine_port" \
    --arg proxy "http://127.0.0.1:$proxy_port" \
    --arg p2p_source_host_port "$pod_ip:8000" \
    '{id:$id,pod_uid:$pod_uid,engine_chat_url:($engine+"/v1/chat/completions"),proxy_chat_url:($proxy+"/v1/chat/completions"),reset_url:($engine+"/reset_prefix_cache"),tokenize_url:($engine+"/tokenize"),tokenizer_info_url:($engine+"/get_tokenizer_info"),p2p_source_host_port:$p2p_source_host_port}')"
  endpoints="$(jq -c --argjson endpoint "$endpoint" '. + [$endpoint]' <<<"$endpoints")"
done < <(jq -r '.items | sort_by(.metadata.name)[] | [.metadata.name,.metadata.uid,.status.podIP] | @tsv' <<<"$model_pods_json")

binding_path="$work_root/calibration-binding.json"
observations_path="$work_root/observations.jsonl"
raw_path="$work_root/vllm-raw.log"
jq -n \
  --arg schema_version "velaserve.crossover-calibration-binding/v1" \
  --arg model_id "$VELASERVE_MODEL_ID" \
  --arg model_revision "$VELASERVE_MODEL_REVISION" \
  --arg repository_commit "$repository_commit" \
  --arg model_image "$VELASERVE_MODEL_IMAGE" \
  --arg model_spec_sha256 "$model_spec_sha256" \
  --arg active_arm "$VELASERVE_ACTIVE_ARM" \
  --arg epp_image "$VELASERVE_EPP_IMAGE" \
  --argjson epp_replicas "$VELASERVE_EPP_REPLICAS" \
  --argjson replica_count "$replica_count" \
  --arg gpu_model "$gpu_model" \
  --arg gpu_driver_version "$gpu_driver" \
  --arg instance_type "$instance_type" \
  --arg router_config_invariant_sha256 "$router_config_invariant_sha256" \
  --argjson endpoints "$endpoints" \
  '{schema_version:$schema_version,model_id:$model_id,model_revision:$model_revision,transport:"tcp",deployment:{repository_commit:$repository_commit,model_image:$model_image,model_spec_sha256:$model_spec_sha256,active_arm:$active_arm,epp_image:$epp_image,epp_replicas:$epp_replicas,replica_count:$replica_count,gpu_model:$gpu_model,gpu_driver_version:$gpu_driver_version,instance_type:$instance_type,router_config_invariant_sha256:$router_config_invariant_sha256},endpoints:$endpoints,observed_at:(now|todateiso8601)}' >"$binding_path"

mkdir -p "$work_root/streams"
while IFS=$'\t' read -r pod pod_uid; do
  kubectl --namespace "$VELASERVE_NAMESPACE" logs --follow "$pod" -c "$VELASERVE_MODEL_CONTAINER_NAME" --since=1s --timestamps --prefix >"$work_root/streams/$pod.log" 2>&1 &
  stream_pids+=("$!")
done < <(jq -r '.items[] | [.metadata.name,.metadata.uid] | @tsv' <<<"$model_pods_json")
sleep 2
for pid in "${stream_pids[@]}"; do kill -0 "$pid" 2>/dev/null || fail "a vLLM evidence stream exited before calibration"; done

calibrate_args=(--binding "$binding_path" --output "$observations_path")
if [[ -n "${VELASERVE_AUTHORIZATION_FILE:-}" ]]; then calibrate_args+=(--authorization-file "$VELASERVE_AUTHORIZATION_FILE"); fi
go -C "$REPOSITORY_ROOT" run ./cmd/crossover-calibrate "${calibrate_args[@]}"
for pid in "${stream_pids[@]}"; do kill "$pid" 2>/dev/null || true; done
for pid in "${stream_pids[@]}"; do wait "$pid" 2>/dev/null || true; done
stream_pids=()

while IFS=$'\t' read -r pod pod_uid; do
  jq -cn --arg pod_name "$pod" --arg pod_uid "$pod_uid" '{schema_version:"velaserve.stream-emitter-binding/v1",pod_name:$pod_name,pod_uid:$pod_uid}' | sed 's/^/VELASERVE_STREAM_BINDING /' >>"$raw_path"
  cat "$work_root/streams/$pod.log" >>"$raw_path"
done < <(jq -r '.items[] | [.metadata.name,.metadata.uid] | @tsv' <<<"$model_pods_json")
grep -Fq 'VELASERVE_VLLM_RUNTIME ' "$raw_path" || fail "calibration streams contain no pinned vLLM runtime records"
go -C "$REPOSITORY_ROOT" run ./cmd/crossover-compile --binding "$binding_path" --observations "$observations_path" --vllm-raw "$raw_path" --output-dir "$VELASERVE_CROSSOVER_BUNDLE"
trap - EXIT
cleanup
echo "cloud-calibrate-crossover: sealed bundle at $VELASERVE_CROSSOVER_BUNDLE"

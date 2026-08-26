#!/usr/bin/env bash
set -euo pipefail

readonly REPOSITORY_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

fail() { echo "cloud-calibrate-load: $*" >&2; exit 1; }
require_env() { [[ -n "${!1:-}" ]] || fail "$1 is required"; }

for command_name in kubectl jq go shasum git sed; do
  command -v "$command_name" >/dev/null 2>&1 || fail "$command_name is required"
done
for variable_name in \
  VELASERVE_NAMESPACE \
  VELASERVE_MODEL_SELECTOR \
  VELASERVE_MODEL_CONTAINER_NAME \
  VELASERVE_MODEL_SERVICE_NAME \
  VELASERVE_MODEL_ID \
  VELASERVE_MODEL_REVISION \
  VELASERVE_MODEL_IMAGE \
  VELASERVE_BENCHMARK_PROFILE \
  VELASERVE_PROFILE_CALIBRATION \
  VELASERVE_Z0_PHASE \
  VELASERVE_ACTIVE_ARM \
  VELASERVE_P2P_TRANSPORT \
  VELASERVE_ENDPOINT \
  VELASERVE_GATEWAY_CHAT_URL \
  VELASERVE_EPP_SELECTOR \
  VELASERVE_ENVOY_GATEWAY_CONTROLLER_SELECTOR \
  VELASERVE_LOAD_RATES_QPS \
  VELASERVE_LOAD_CALIBRATION \
  VELASERVE_LOAD_PROFILES; do
  require_env "$variable_name"
done
[[ ! -e "$VELASERVE_LOAD_CALIBRATION" && ! -L "$VELASERVE_LOAD_CALIBRATION" ]] || fail "load calibration output already exists"
[[ ! -e "$VELASERVE_LOAD_PROFILES" && ! -L "$VELASERVE_LOAD_PROFILES" ]] || fail "load profiles output already exists"
git -C "$REPOSITORY_ROOT" diff --quiet HEAD -- . || fail "tracked repository files differ from Git HEAD"
[[ -z "$(git -C "$REPOSITORY_ROOT" status --porcelain --untracked-files=normal)" ]] || fail "repository must be clean before cloud calibration"

route_json="$(kubectl --namespace "$VELASERVE_NAMESPACE" get httproute velaserve -o json)"
gateway_json="$(kubectl --namespace "$VELASERVE_NAMESPACE" get gateway velaserve-gateway -o json)"
gatewayclass_json="$(kubectl get gatewayclass envoy -o json)"
envoyproxy_json="$(kubectl --namespace envoy-gateway-system get envoyproxy velaserve -o json)"
gateway_services="$(kubectl get services --all-namespaces -l gateway.envoyproxy.io/owning-gateway-name=velaserve-gateway -o json)"
[[ "$(jq '.items | length' <<<"$gateway_services")" == "1" ]] || fail "exactly one generated Envoy Gateway service is required"
envoy_namespace="$(jq -er '.items[0].metadata.namespace' <<<"$gateway_services")"
envoy_selector="$(jq -er '.items[0].spec.selector | to_entries | sort_by(.key) | map(.key + "=" + .value) | join(",") | select(length > 0)' <<<"$gateway_services")"
envoy_pods_json="$(kubectl --namespace "$envoy_namespace" get pods -l "$envoy_selector" -o json)"
envoy_controller_pods_json="$(kubectl --namespace envoy-gateway-system get pods -l "$VELASERVE_ENVOY_GATEWAY_CONTROLLER_SELECTOR" -o json)"
routing_sha256="$(jq -S -c -n --argjson route "$route_json" --argjson gateway "$gateway_json" --argjson gatewayclass "$gatewayclass_json" --argjson envoyproxy "$envoyproxy_json" --argjson services "$gateway_services" --argjson data_plane "$envoy_pods_json" --argjson controller "$envoy_controller_pods_json" '{route:{uid:$route.metadata.uid,spec:$route.spec},gateway:{uid:$gateway.metadata.uid,spec:$gateway.spec},gatewayclass:{uid:$gatewayclass.metadata.uid,spec:$gatewayclass.spec},envoyproxy:{uid:$envoyproxy.metadata.uid,spec:$envoyproxy.spec},services:[$services.items[]|{uid:.metadata.uid,spec:.spec}]|sort_by(.uid),data_plane:[$data_plane.items[]|{uid:.metadata.uid,spec:.spec}]|sort_by(.uid),controller:[$controller.items[]|{uid:.metadata.uid,spec:.spec}]|sort_by(.uid)}' | shasum -a 256 | awk '{print $1}')"

epp_pods_json="$(kubectl --namespace "$VELASERVE_NAMESPACE" get pods -l "$VELASERVE_EPP_SELECTOR" -o json)"
config_name_sets="$(jq -c '[.items[] | [.spec.volumes[]? | .configMap.name] | sort | unique] | unique' <<<"$epp_pods_json")"
jq -e 'length == 1 and (.[0] | length) > 0' <<<"$config_name_sets" >/dev/null || fail "EPP pods do not share one ConfigMap set"
router_config_json='[]'
while IFS= read -r config_name; do
  [[ -n "$config_name" ]] || continue
  config_json="$(kubectl --namespace "$VELASERVE_NAMESPACE" get configmap "$config_name" -o json)"
  router_config_json="$(jq -c --argjson item "$config_json" '. + [$item]' <<<"$router_config_json")"
done < <(jq -r '.[0][]' <<<"$config_name_sets")
router_config_sha256="$(jq -S -c '[.[] | {name:.metadata.name,uid:.metadata.uid,data:.data,binary_data:.binaryData}] | sort_by(.uid)' <<<"$router_config_json" | shasum -a 256 | awk '{print $1}')"

model_pods_json="$(kubectl --namespace "$VELASERVE_NAMESPACE" get pods -l "$VELASERVE_MODEL_SELECTOR" -o json)"
replica_count="$(jq '.items | length' <<<"$model_pods_json")"
(( replica_count >= 2 )) || fail "at least two model replicas are required"
jq -e --arg container "$VELASERVE_MODEL_CONTAINER_NAME" --arg image "$VELASERVE_MODEL_IMAGE" 'all(.items[];.status.phase=="Running" and any(.status.conditions[]?;.type=="Ready" and .status=="True") and any(.spec.containers[];.name==$container and .image==$image) and any(.status.containerStatuses[];.name==$container and .restartCount==0))' <<<"$model_pods_json" >/dev/null || fail "model fleet is not ready, restart-free, and image-pinned"

instance_types=''
gpu_models=''
gpu_drivers=''
while IFS=$'\t' read -r pod node; do
  instance_type="$(kubectl get node "$node" -o 'jsonpath={.metadata.labels.node\.kubernetes\.io/instance-type}')"
  gpu_identity="$(kubectl --namespace "$VELASERVE_NAMESPACE" exec "$pod" -c "$VELASERVE_MODEL_CONTAINER_NAME" -- nvidia-smi --query-gpu=name,driver_version --format=csv,noheader,nounits)"
  instance_types="${instance_types}${instance_type}"$'\n'
  gpu_models="${gpu_models}$(cut -d, -f1 <<<"$gpu_identity" | sed 's/[[:space:]]*$//')"$'\n'
  gpu_drivers="${gpu_drivers}$(cut -d, -f2- <<<"$gpu_identity" | sed 's/^[[:space:]]*//')"$'\n'
done < <(jq -r '.items[] | [.metadata.name,.spec.nodeName] | @tsv' <<<"$model_pods_json")
instance_type="$(sort -u <<<"$instance_types" | sed '/^$/d')"
gpu_model="$(sort -u <<<"$gpu_models" | sed '/^$/d')"
gpu_driver="$(sort -u <<<"$gpu_drivers" | sed '/^$/d')"
[[ "$(wc -l <<<"$instance_type" | tr -d '[:space:]')" == "1" && "$(wc -l <<<"$gpu_model" | tr -d '[:space:]')" == "1" && "$(wc -l <<<"$gpu_driver" | tr -d '[:space:]')" == "1" ]] || fail "model fleet hardware is not homogeneous"

work_root="$(mktemp -d)"
forward_pids=()
cleanup() {
  for pid in "${forward_pids[@]}"; do kill "$pid" 2>/dev/null || true; done
  for pid in "${forward_pids[@]}"; do wait "$pid" 2>/dev/null || true; done
  rm -rf "$work_root"
}
trap cleanup EXIT INT TERM
forwarded_port=''
start_forward() {
  local pod="$1"
  local log_path="$work_root/forward-$pod.log"
  kubectl --namespace "$VELASERVE_NAMESPACE" port-forward --address 127.0.0.1 "pod/$pod" :8200 >"$log_path" 2>&1 &
  local pid="$!"
  forward_pids+=("$pid")
  forwarded_port=''
  for _ in {1..100}; do
    forwarded_port="$(sed -n 's/^Forwarding from 127\.0\.0\.1:\([0-9][0-9]*\) -> .*/\1/p' "$log_path" | head -n 1)"
    [[ -n "$forwarded_port" ]] && return 0
    kill -0 "$pid" 2>/dev/null || fail "metrics port-forward for $pod exited"
    sleep 0.1
  done
  fail "metrics port-forward for $pod did not become ready"
}

endpoints='[]'
metrics_access='{}'
while IFS=$'\t' read -r pod pod_uid; do
  start_forward "$pod"
  stable_url="http://${pod}.${VELASERVE_MODEL_SERVICE_NAME}-headless.${VELASERVE_NAMESPACE}.svc.cluster.local:8200/metrics"
  endpoints="$(jq -c --arg id "$pod" --arg uid "$pod_uid" --arg url "$stable_url" '. + [{id:$id,pod_uid:$uid,metrics_url:$url}]' <<<"$endpoints")"
  metrics_access="$(jq -c --arg id "$pod" --arg url "http://127.0.0.1:${forwarded_port}/metrics" '. + {($id):$url}' <<<"$metrics_access")"
done < <(jq -r '.items | sort_by(.metadata.name)[] | [.metadata.name,.metadata.uid] | @tsv' <<<"$model_pods_json")
endpoints_path="$work_root/endpoints.json"
access_path="$work_root/metrics-access.json"
printf '%s\n' "$endpoints" >"$endpoints_path"
printf '%s\n' "$metrics_access" >"$access_path"

args=(
  --endpoint "$VELASERVE_ENDPOINT"
  --gateway-chat-url "$VELASERVE_GATEWAY_CHAT_URL"
  --profile "$VELASERVE_BENCHMARK_PROFILE"
  --profile-calibration "$VELASERVE_PROFILE_CALIBRATION"
  --phase "$VELASERVE_Z0_PHASE"
  --model-image "$VELASERVE_MODEL_IMAGE"
  --instance-type "$instance_type"
  --gpu-model "$gpu_model"
  --gpu-driver-version "$gpu_driver"
  --replica-count "$replica_count"
  --transport "$VELASERVE_P2P_TRANSPORT"
  --active-arm "$VELASERVE_ACTIVE_ARM"
  --routing-sha256 "$routing_sha256"
  --router-config-sha256 "$router_config_sha256"
  --endpoints "$endpoints_path"
  --metrics-access "$access_path"
  --rates "$VELASERVE_LOAD_RATES_QPS"
  --warmup-duration "${VELASERVE_LOAD_WARMUP_DURATION:-5s}"
  --trial-duration "${VELASERVE_LOAD_TRIAL_DURATION:-30s}"
  --request-timeout "${VELASERVE_LOAD_REQUEST_TIMEOUT:-180s}"
  --drain-timeout "${VELASERVE_LOAD_DRAIN_TIMEOUT:-2m}"
  --drain-poll-interval "${VELASERVE_LOAD_DRAIN_POLL_INTERVAL:-500ms}"
  --max-inflight "${VELASERVE_LOAD_MAX_INFLIGHT:-512}"
  --output-calibration "$VELASERVE_LOAD_CALIBRATION"
  --output-profiles "$VELASERVE_LOAD_PROFILES"
)
if [[ -n "${VELASERVE_AUTHORIZATION_FILE:-}" ]]; then args+=(--authorization-file "$VELASERVE_AUTHORIZATION_FILE"); fi
go -C "$REPOSITORY_ROOT" run ./cmd/load-calibration "${args[@]}"
trap - EXIT
cleanup
echo "cloud-calibrate-load: retained $VELASERVE_LOAD_CALIBRATION and $VELASERVE_LOAD_PROFILES"

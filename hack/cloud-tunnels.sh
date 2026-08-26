#!/usr/bin/env bash
set -euo pipefail

namespace="${VELASERVE_NAMESPACE:-velaserve-z0}"
gateway_port="${VELASERVE_GATEWAY_LOCAL_PORT:-18080}"
controller_port="${VELASERVE_CONTROLLER_LOCAL_PORT:-18082}"
for command_name in kubectl jq; do command -v "$command_name" >/dev/null 2>&1 || { echo "cloud-tunnels: $command_name is required" >&2; exit 1; }; done
gateway_service="$(kubectl get services --all-namespaces -l gateway.envoyproxy.io/owning-gateway-name=velaserve-gateway -o json | jq -er 'select((.items|length)==1) | .items[0] | [.metadata.namespace,.metadata.name] | @tsv')"
IFS=$'\t' read -r gateway_namespace gateway_name <<<"$gateway_service"
pids=()
cleanup() {
  for pid in "${pids[@]}"; do kill "$pid" 2>/dev/null || true; done
  for pid in "${pids[@]}"; do wait "$pid" 2>/dev/null || true; done
}
trap cleanup EXIT INT TERM
kubectl --namespace "$gateway_namespace" port-forward --address 127.0.0.1 "service/$gateway_name" "${gateway_port}:80" &
pids+=("$!")
kubectl --namespace "$namespace" port-forward --address 127.0.0.1 service/velaserve-condition-controller "${controller_port}:8082" &
pids+=("$!")
echo "cloud-tunnels: local_gateway=http://127.0.0.1:${gateway_port}/v1/chat/completions in_cluster_gateway=http://${gateway_name}.${gateway_namespace}.svc.cluster.local/v1/chat/completions controller=http://127.0.0.1:${controller_port}/v1/conditions/apply"
while kill -0 "${pids[0]}" 2>/dev/null && kill -0 "${pids[1]}" 2>/dev/null; do sleep 1; done
exit 1

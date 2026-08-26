#!/usr/bin/env bash
set -euo pipefail

readonly REPOSITORY_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly CLUSTER_NAME="velaserve-z0"
readonly CONTEXT="kind-${CLUSTER_NAME}"
readonly NAMESPACE="velaserve-z0"
readonly ROUTER_COMMIT="ab723b898f8598ab6631e9848a4cf28accd9b9ea"
readonly VELASERVE_IMAGE="docker.io/jdinseattle/velaserve:v0.1.0"
readonly EPP_IMAGE="docker.io/velaserve-llm-d-router:git-${ROUTER_COMMIT}"
readonly ENVOY_GATEWAY_VERSION="v1.9.0"
readonly INFERENCE_EXTENSION_VERSION="v1.5.0"
readonly KIND_NODE_IMAGE="kindest/node:v1.35.0@sha256:452d707d4862f52530247495d180205e029056831160e22870e37e3f6c1ac31f"
readonly BUILD_OUTPUT="${REPOSITORY_ROOT}/dist/kind"
readonly ANONYMOUS_DOCKER_CONFIG="${REPOSITORY_ROOT}/.tools/docker-anonymous"

arm="${1:-arm-b}"
case "$arm" in
  arm-a) arm_values="${REPOSITORY_ROOT}/deploy/experiments/arm-a-values.yaml" ;;
  arm-b) arm_values="${REPOSITORY_ROOT}/deploy/experiments/arm-b-values.yaml" ;;
  *) echo "usage: hack/kind-up.sh [arm-a|arm-b]" >&2; exit 2 ;;
esac

for required in docker kubectl curl jq tar git; do
  if ! command -v "$required" >/dev/null 2>&1; then
    echo "$required is required" >&2
    exit 1
  fi
done
mkdir -p "$ANONYMOUS_DOCKER_CONFIG"
if [[ ! -f "${ANONYMOUS_DOCKER_CONFIG}/config.json" ]]; then
  printf '%s\n' '{"auths":{}}' >"${ANONYMOUS_DOCKER_CONFIG}/config.json"
fi
export DOCKER_CONFIG="$ANONYMOUS_DOCKER_CONFIG"
if ! docker info >/dev/null 2>&1; then
  echo "Docker Engine is not available" >&2
  exit 1
fi

"${REPOSITORY_ROOT}/hack/bootstrap-tools.sh"
"${REPOSITORY_ROOT}/hack/fetch-upstream.sh"
"${REPOSITORY_ROOT}/hack/verify-render.sh"

readonly KIND="${REPOSITORY_ROOT}/.tools/bin/kind"
readonly HELM="${REPOSITORY_ROOT}/.tools/bin/helm"
if "$KIND" get clusters | grep -Fxq "$CLUSTER_NAME"; then
  echo "Kind cluster $CLUSTER_NAME already exists; run hack/kind-down.sh first" >&2
  exit 1
fi

case "$(uname -m)" in
  arm64|aarch64) target_arch="arm64" ;;
  x86_64|amd64) target_arch="amd64" ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac
mkdir -p "$BUILD_OUTPUT"
cd "$REPOSITORY_ROOT"
for command_name in simfleet fanoutbench zeroprobe velaserve-gate oracle-replay schema-check condition-controller condition-driver clock-probe epp-normalize envoy-normalize vllm-normalize p2p-runtime-normalize source-pressure-compile; do
  env \
    CGO_ENABLED=0 \
    GOOS=linux \
    GOARCH="$target_arch" \
    GOCACHE="${REPOSITORY_ROOT}/.cache/go-build" \
    GOMODCACHE="${REPOSITORY_ROOT}/.cache/go-mod" \
    go build -trimpath -ldflags="-s -w" -o "${BUILD_OUTPUT}/${command_name}" "./cmd/${command_name}"
done
"${REPOSITORY_ROOT}/hack/build-pinned-epp.sh" "${BUILD_OUTPUT}/epp" linux "$target_arch"

docker build --tag "$VELASERVE_IMAGE" --file "${REPOSITORY_ROOT}/deploy/kind/Dockerfile.velaserve" "$BUILD_OUTPUT"
docker build --tag "$EPP_IMAGE" --file "${REPOSITORY_ROOT}/deploy/kind/Dockerfile.epp" "$BUILD_OUTPUT"

"$KIND" create cluster --name "$CLUSTER_NAME" --image "$KIND_NODE_IMAGE" --config "${REPOSITORY_ROOT}/deploy/kind/cluster.yaml"
"$KIND" load docker-image --name "$CLUSTER_NAME" "$VELASERVE_IMAGE" "$EPP_IMAGE"

kubectl --context "$CONTEXT" apply --server-side -f "https://github.com/envoyproxy/gateway/releases/download/${ENVOY_GATEWAY_VERSION}/install.yaml"
kubectl --context "$CONTEXT" wait --timeout=5m --namespace envoy-gateway-system deployment/envoy-gateway --for=condition=Available
kubectl --context "$CONTEXT" apply --server-side -f "https://github.com/kubernetes-sigs/gateway-api-inference-extension/releases/download/${INFERENCE_EXTENSION_VERSION}/manifests.yaml"
kubectl --context "$CONTEXT" create namespace "$NAMESPACE"

"$HELM" upgrade --install velaserve-support "${REPOSITORY_ROOT}/deploy/helm/velaserve" \
  --kube-context "$CONTEXT" \
  --namespace "$NAMESPACE" \
  -f "${REPOSITORY_ROOT}/deploy/experiments/kind-values.yaml" \
  -f "$arm_values"
kubectl --context "$CONTEXT" --namespace "$NAMESPACE" rollout status deployment/velaserve-simfleet --timeout=5m

temporary_directory="$(mktemp -d)"
trap 'rm -rf "$temporary_directory"' EXIT
router_values="${temporary_directory}/router-values.yaml"
kubectl --context "$CONTEXT" --namespace "$NAMESPACE" get configmap velaserve-upstream-router-values -o 'jsonpath={.data.values\.yaml}' >"$router_values"
readonly ROUTER_CHART="${REPOSITORY_ROOT}/.tools/upstream/llm-d-router/config/charts/llm-d-router-standalone"
"$HELM" dependency build "$ROUTER_CHART"
"$HELM" upgrade --install velaserve "$ROUTER_CHART" \
  --kube-context "$CONTEXT" \
  --namespace "$NAMESPACE" \
  -f "$router_values"
kubectl --context "$CONTEXT" --namespace "$NAMESPACE" rollout status deployment/velaserve-epp --timeout=5m
kubectl --context "$CONTEXT" apply -f "${REPOSITORY_ROOT}/deploy/gateway/httproute.yaml"

route_ready=0
for attempt in $(seq 1 60); do
  route_json="$(kubectl --context "$CONTEXT" --namespace "$NAMESPACE" get httproute velaserve -o json)"
  if jq -e 'any(.status.parents[]?.conditions[]?; .type == "Accepted" and .status == "True") and any(.status.parents[]?.conditions[]?; .type == "ResolvedRefs" and .status == "True")' <<<"$route_json" >/dev/null; then
    route_ready=1
    break
  fi
  sleep 2
done
[[ "$route_ready" == "1" ]] || { echo "HTTPRoute was not accepted with resolved references" >&2; exit 1; }

gateway_services="$(kubectl --context "$CONTEXT" get services --all-namespaces -l gateway.envoyproxy.io/owning-gateway-name=velaserve-gateway -o json)"
gateway_service_count="$(jq '.items | length' <<<"$gateway_services")"
[[ "$gateway_service_count" == "1" ]] || { echo "expected one generated Envoy Gateway service, found $gateway_service_count" >&2; exit 1; }
gateway_namespace="$(jq -r '.items[0].metadata.namespace' <<<"$gateway_services")"
gateway_service="$(jq -r '.items[0].metadata.name' <<<"$gateway_services")"
gateway_port="$(jq -r '.items[0].spec.ports[] | select(.port == 80) | .port' <<<"$gateway_services")"
[[ "$gateway_port" == "80" ]] || { echo "generated Envoy Gateway service has no HTTP port 80" >&2; exit 1; }
gateway_selector="$(jq -r '.items[0].spec.selector | to_entries | map("\(.key)=\(.value)") | join(",")' <<<"$gateway_services")"
[[ -n "$gateway_selector" && "$gateway_selector" != "null" ]] || { echo "generated Envoy Gateway service has no pod selector" >&2; exit 1; }
kubectl --context "$CONTEXT" --namespace "$gateway_namespace" wait --timeout=5m --for=condition=Ready pod -l "$gateway_selector"

port_forward_log="${REPOSITORY_ROOT}/.tools/kind-port-forward.log"
nohup kubectl --context "$CONTEXT" --namespace "$gateway_namespace" port-forward "service/${gateway_service}" 18081:80 </dev/null >"$port_forward_log" 2>&1 &
port_forward_pid=$!
echo "$port_forward_pid" >"${REPOSITORY_ROOT}/.tools/kind-port-forward.pid"
echo "http://127.0.0.1:18081/v1/chat/completions" >"${REPOSITORY_ROOT}/.tools/kind-endpoint"

port_forward_ready=0
for attempt in $(seq 1 30); do
  if ! kill -0 "$port_forward_pid" 2>/dev/null; then
    sed -n '1,120p' "$port_forward_log" >&2
    echo "Envoy Gateway port-forward exited before becoming ready" >&2
    exit 1
  fi
  if grep -Fq 'Forwarding from 127.0.0.1:18081' "$port_forward_log"; then
    port_forward_ready=1
    break
  fi
  sleep 1
done
[[ "$port_forward_ready" == "1" ]] || { sed -n '1,120p' "$port_forward_log" >&2; echo "Envoy Gateway port-forward did not become ready" >&2; exit 1; }

for attempt in $(seq 1 30); do
  if curl --fail --silent --show-error \
    -H 'content-type: application/json' \
    --data '{"model":"velaserve-simulator","messages":[{"role":"user","content":"kind smoke"}],"max_tokens":8,"stream":true,"stream_options":{"include_usage":true}}' \
    http://127.0.0.1:18081/v1/chat/completions | grep -q 'data: \[DONE\]'; then
    env \
      GOCACHE="${REPOSITORY_ROOT}/.cache/go-build" \
      GOMODCACHE="${REPOSITORY_ROOT}/.cache/go-mod" \
      go test ./tests/integration -run TestKindUpstreamSSE -count=1
    kubectl --context "$CONTEXT" --namespace "$NAMESPACE" logs deployment/velaserve-epp --all-containers --tail=-1 | grep -Fq 'VELASERVE_EPP_RECORD ' || { echo "pinned EPP observer produced no scheduling record" >&2; exit 1; }
    echo "Kind smoke passed through Envoy Gateway: http://127.0.0.1:18081/v1/chat/completions ($arm)"
    exit 0
  fi
  sleep 2
done

if kill -0 "$port_forward_pid" 2>/dev/null; then
  kill "$port_forward_pid"
fi
echo "Kind endpoint did not produce a complete SSE stream; inspect $port_forward_log" >&2
sed -n '1,120p' "$port_forward_log" >&2
exit 1

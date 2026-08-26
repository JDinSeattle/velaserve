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

for required in docker kubectl curl; do
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
for command_name in simfleet fanoutbench zeroprobe velaserve-gate oracle-replay schema-check; do
  env \
    CGO_ENABLED=0 \
    GOOS=linux \
    GOARCH="$target_arch" \
    GOCACHE="${REPOSITORY_ROOT}/.cache/go-build" \
    GOMODCACHE="${REPOSITORY_ROOT}/.cache/go-mod" \
    go build -trimpath -ldflags="-s -w" -o "${BUILD_OUTPUT}/${command_name}" "./cmd/${command_name}"
done
env \
  CGO_ENABLED=0 \
  GOOS=linux \
  GOARCH="$target_arch" \
  GOCACHE="${REPOSITORY_ROOT}/.cache/upstream-go-build" \
  GOMODCACHE="${REPOSITORY_ROOT}/.cache/upstream-go-mod" \
  go -C "${REPOSITORY_ROOT}/.tools/upstream/llm-d-router" build -trimpath -ldflags="-s -w -X github.com/llm-d/llm-d-router/version.CommitSHA=${ROUTER_COMMIT}" \
    -o "${BUILD_OUTPUT}/epp" \
    ./cmd/epp

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

port_forward_log="${REPOSITORY_ROOT}/.tools/kind-port-forward.log"
nohup kubectl --context "$CONTEXT" --namespace "$NAMESPACE" port-forward service/velaserve-epp 18081:8081 </dev/null >"$port_forward_log" 2>&1 &
port_forward_pid=$!
echo "$port_forward_pid" >"${REPOSITORY_ROOT}/.tools/kind-port-forward.pid"
echo "http://127.0.0.1:18081/v1/chat/completions" >"${REPOSITORY_ROOT}/.tools/kind-endpoint"

for attempt in $(seq 1 30); do
  if curl --fail --silent --show-error \
    -H 'content-type: application/json' \
    --data '{"model":"velaserve-simulator","messages":[{"role":"user","content":"kind smoke"}],"max_tokens":8,"stream":true,"stream_options":{"include_usage":true}}' \
    http://127.0.0.1:18081/v1/chat/completions | grep -q 'data: \[DONE\]'; then
    env \
      GOCACHE="${REPOSITORY_ROOT}/.cache/go-build" \
      GOMODCACHE="${REPOSITORY_ROOT}/.cache/go-mod" \
      go test ./tests/integration -run TestKindUpstreamSSE -count=1
    echo "Kind smoke passed: http://127.0.0.1:18081/v1/chat/completions ($arm)"
    exit 0
  fi
  sleep 2
done

if kill -0 "$port_forward_pid" 2>/dev/null; then
  kill "$port_forward_pid"
fi
echo "Kind endpoint did not produce a complete SSE stream; inspect $port_forward_log" >&2
exit 1

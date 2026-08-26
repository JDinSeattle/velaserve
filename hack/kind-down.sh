#!/usr/bin/env bash
set -euo pipefail

readonly REPOSITORY_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly CLUSTER_NAME="velaserve-z0"
readonly PID_FILE="${REPOSITORY_ROOT}/.tools/kind-port-forward.pid"
readonly KIND="${REPOSITORY_ROOT}/.tools/bin/kind"

if [[ -f "$PID_FILE" ]]; then
  port_forward_pid="$(tr -d '[:space:]' <"$PID_FILE")"
  if [[ "$port_forward_pid" =~ ^[0-9]+$ ]] && ps -p "$port_forward_pid" -o command= 2>/dev/null | grep -q 'kubectl.*port-forward.*service/'; then
    kill "$port_forward_pid"
  fi
fi

if [[ -x "$KIND" ]] && "$KIND" get clusters | grep -Fxq "$CLUSTER_NAME"; then
  "$KIND" delete cluster --name "$CLUSTER_NAME"
fi

for generated in kind-port-forward.pid kind-port-forward.log kind-endpoint; do
  if [[ -f "${REPOSITORY_ROOT}/.tools/${generated}" ]]; then
    rm "${REPOSITORY_ROOT}/.tools/${generated}"
  fi
done
echo "removed exact local cluster state for $CLUSTER_NAME"

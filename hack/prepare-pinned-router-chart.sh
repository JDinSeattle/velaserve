#!/usr/bin/env bash
set -euo pipefail

readonly REPOSITORY_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly ROUTER_COMMIT="ab723b898f8598ab6631e9848a4cf28accd9b9ea"
readonly OBSERVER_PATCH="${REPOSITORY_ROOT}/deploy/upstream-patches/llm-d-router-stage1-observer.patch"
readonly SIDECAR_PATCH="${REPOSITORY_ROOT}/deploy/upstream-patches/llm-d-router-stage1-sidecar-evidence.patch"
output="${1:-}"
[[ -n "$output" ]] || { echo "usage: hack/prepare-pinned-router-chart.sh NEW_OUTPUT_DIRECTORY" >&2; exit 2; }
[[ ! -e "$output" && ! -L "$output" ]] || { echo "prepare-pinned-router-chart: output already exists" >&2; exit 1; }
for command_name in git tar; do
  command -v "$command_name" >/dev/null 2>&1 || { echo "prepare-pinned-router-chart: $command_name is required" >&2; exit 1; }
done
"${REPOSITORY_ROOT}/hack/fetch-upstream.sh" >/dev/null
upstream="${REPOSITORY_ROOT}/.tools/upstream/llm-d-router"
[[ "$(git -C "$upstream" rev-parse HEAD)" == "$ROUTER_COMMIT" ]] || { echo "prepare-pinned-router-chart: checkout mismatch" >&2; exit 1; }
[[ -z "$(git -C "$upstream" status --porcelain --untracked-files=no)" ]] || { echo "prepare-pinned-router-chart: checkout is dirty" >&2; exit 1; }
temporary="$(mktemp -d)"
trap 'rm -rf "$temporary"' EXIT
git -C "$upstream" archive "$ROUTER_COMMIT" | tar -x -C "$temporary"
git -C "$temporary" apply --check "$OBSERVER_PATCH" "$SIDECAR_PATCH"
git -C "$temporary" apply "$OBSERVER_PATCH" "$SIDECAR_PATCH"
mkdir -p "$(dirname "$output")"
mv "$temporary" "$output"
trap - EXIT
echo "prepare-pinned-router-chart: prepared patched chart at $output/config/charts/llm-d-router-standalone"

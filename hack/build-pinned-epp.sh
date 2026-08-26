#!/usr/bin/env bash
set -euo pipefail

readonly REPOSITORY_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly ROUTER_COMMIT="ab723b898f8598ab6631e9848a4cf28accd9b9ea"
readonly PATCH_PATH="${REPOSITORY_ROOT}/deploy/upstream-patches/llm-d-router-stage1-observer.patch"
readonly SIDECAR_PATCH_PATH="${REPOSITORY_ROOT}/deploy/upstream-patches/llm-d-router-stage1-sidecar-evidence.patch"

output="${1:-}"
[[ -n "$output" ]] || { echo "usage: hack/build-pinned-epp.sh OUTPUT [GOOS] [GOARCH]" >&2; exit 2; }
target_os="${2:-$(go env GOOS)}"
target_arch="${3:-$(go env GOARCH)}"

"${REPOSITORY_ROOT}/hack/fetch-upstream.sh" >/dev/null
upstream="${REPOSITORY_ROOT}/.tools/upstream/llm-d-router"
[[ "$(git -C "$upstream" rev-parse HEAD)" == "$ROUTER_COMMIT" ]] || { echo "pinned llm-d-router checkout mismatch" >&2; exit 1; }
[[ -z "$(git -C "$upstream" status --porcelain --untracked-files=no)" ]] || { echo "pinned llm-d-router checkout is dirty" >&2; exit 1; }
build_root="$(mktemp -d)"
trap 'rm -rf "$build_root"' EXIT
git -C "$upstream" archive "$ROUTER_COMMIT" | tar -x -C "$build_root"
git -C "$build_root" apply --check "$PATCH_PATH"
git -C "$build_root" apply "$PATCH_PATH"
git -C "$build_root" apply --check "$SIDECAR_PATCH_PATH"
git -C "$build_root" apply "$SIDECAR_PATCH_PATH"
mkdir -p "$(dirname "$output")"
env CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" \
  GOCACHE="${REPOSITORY_ROOT}/.cache/upstream-go-build" \
  GOMODCACHE="${REPOSITORY_ROOT}/.cache/upstream-go-mod" \
  go -C "$build_root" build -trimpath -ldflags="-s -w -X github.com/llm-d/llm-d-router/version.CommitSHA=${ROUTER_COMMIT}" -o "$output" ./cmd/epp

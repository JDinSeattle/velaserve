#!/usr/bin/env bash
set -euo pipefail

readonly REPOSITORY_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly ROUTER_COMMIT="ab723b898f8598ab6631e9848a4cf28accd9b9ea"
readonly OBSERVER_PATCH="${REPOSITORY_ROOT}/deploy/upstream-patches/llm-d-router-stage1-observer.patch"
readonly SIDECAR_PATCH="${REPOSITORY_ROOT}/deploy/upstream-patches/llm-d-router-stage1-sidecar-evidence.patch"

image="${1:-}"
[[ -n "$image" ]] || { echo "usage: hack/build-pinned-sidecar-image.sh LOCAL_IMAGE [GOARCH]" >&2; exit 2; }
target_arch="${2:-amd64}"
for command_name in docker go shasum; do
  command -v "$command_name" >/dev/null 2>&1 || { echo "build-pinned-sidecar-image: $command_name is required" >&2; exit 1; }
done
build_root="$(mktemp -d)"
trap 'rm -rf "$build_root"' EXIT
"${REPOSITORY_ROOT}/hack/build-pinned-sidecar.sh" "$build_root/pd-sidecar" linux "$target_arch"
observer_sha256="$(shasum -a 256 "$OBSERVER_PATCH" | awk '{print $1}')"
sidecar_sha256="$(shasum -a 256 "$SIDECAR_PATCH" | awk '{print $1}')"
docker buildx build --platform linux/amd64 --load \
  --file "${REPOSITORY_ROOT}/deploy/kind/Dockerfile.sidecar" \
  --label "io.velaserve.component=routing-sidecar" \
  --label "io.velaserve.upstream.commit=${ROUTER_COMMIT}" \
  --label "io.velaserve.patch.observer.sha256=${observer_sha256}" \
  --label "io.velaserve.patch.sidecar-evidence.sha256=${sidecar_sha256}" \
  --label "io.velaserve.build.dirty=false" \
  --label "io.velaserve.architecture=amd64" \
  --tag "$image" \
  "$build_root"

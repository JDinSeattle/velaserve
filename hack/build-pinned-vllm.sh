#!/usr/bin/env bash
set -euo pipefail

readonly REPOSITORY_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly VLLM_COMMIT="b1fbbc2ade51e3826bc92e4733c9c692ee21d42d"
readonly PATCH_PATH="${REPOSITORY_ROOT}/deploy/upstream-patches/vllm-stage1-runtime-observer.patch"

image="${1:-}"
[[ -n "$image" ]] || { echo "usage: hack/build-pinned-vllm.sh LOCAL_IMAGE" >&2; exit 2; }
for command_name in docker git python3 tar; do
  command -v "$command_name" >/dev/null 2>&1 || { echo "build-pinned-vllm: $command_name is required" >&2; exit 1; }
done

"${REPOSITORY_ROOT}/hack/fetch-pinned-vllm.sh" >/dev/null
upstream="${REPOSITORY_ROOT}/.tools/upstream/vllm"
[[ "$(git -C "$upstream" rev-parse HEAD)" == "$VLLM_COMMIT" ]] || { echo "build-pinned-vllm: checkout mismatch" >&2; exit 1; }
[[ -z "$(git -C "$upstream" status --porcelain --untracked-files=no)" ]] || { echo "build-pinned-vllm: pinned checkout is dirty" >&2; exit 1; }
patch_sha256="$(shasum -a 256 "$PATCH_PATH" | awk '{print $1}')"
build_root="$(mktemp -d)"
trap 'rm -rf "$build_root"' EXIT
git -C "$upstream" archive "$VLLM_COMMIT" | tar -x -C "$build_root"
git -C "$build_root" apply --check "$PATCH_PATH"
git -C "$build_root" apply "$PATCH_PATH"
python3 -m py_compile \
  "$build_root/vllm/distributed/kv_transfer/kv_connector/v1/offloading/common.py" \
  "$build_root/vllm/distributed/kv_transfer/kv_connector/v1/offloading/scheduler.py" \
  "$build_root/vllm/distributed/kv_transfer/kv_connector/v1/offloading/worker.py"
docker buildx build --platform linux/amd64 --load \
  --file "$build_root/docker/Dockerfile" \
  --target vllm-openai \
  --label "io.velaserve.component=vllm" \
  --label "io.velaserve.upstream.commit=${VLLM_COMMIT}" \
  --label "io.velaserve.patch.runtime-observer.sha256=${patch_sha256}" \
  --label "io.velaserve.build.dirty=false" \
  --label "io.velaserve.architecture=amd64" \
  --tag "$image" \
  "$build_root"
echo "build-pinned-vllm: built $image from $VLLM_COMMIT with the Stage-1 runtime observer"

#!/usr/bin/env bash
set -euo pipefail

readonly REPOSITORY_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly VLLM_COMMIT="b1fbbc2ade51e3826bc92e4733c9c692ee21d42d"
readonly VLLM_URL="https://github.com/vllm-project/vllm.git"
readonly DESTINATION="${REPOSITORY_ROOT}/.tools/upstream/vllm"

mkdir -p "$(dirname "$DESTINATION")"
if [[ ! -d "$DESTINATION/.git" ]]; then
  git clone --filter=blob:none --no-checkout "$VLLM_URL" "$DESTINATION"
fi
[[ "$(git -C "$DESTINATION" remote get-url origin)" == "$VLLM_URL" ]] || {
  echo "fetch-pinned-vllm: checkout has an unexpected origin" >&2
  exit 1
}
if ! git -C "$DESTINATION" cat-file -e "${VLLM_COMMIT}^{commit}" 2>/dev/null; then
  git -C "$DESTINATION" fetch --depth=1 origin "$VLLM_COMMIT"
fi
git -C "$DESTINATION" checkout --detach --quiet "$VLLM_COMMIT"
[[ "$(git -C "$DESTINATION" rev-parse HEAD)" == "$VLLM_COMMIT" ]] || {
  echo "fetch-pinned-vllm: frozen commit did not resolve" >&2
  exit 1
}
[[ -z "$(git -C "$DESTINATION" status --porcelain --untracked-files=normal)" ]] || {
  echo "fetch-pinned-vllm: checkout is dirty" >&2
  exit 1
}
echo "vllm=$VLLM_COMMIT"

#!/usr/bin/env bash
set -euo pipefail

readonly REPOSITORY_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly UPSTREAM_ROOT="${REPOSITORY_ROOT}/.tools/upstream"
readonly LLM_D_ROUTER_COMMIT="ab723b898f8598ab6631e9848a4cf28accd9b9ea"
readonly LLM_D_COMMIT="3243fcf1191348b55c7811267a98117f8b7a6910"

mkdir -p "$UPSTREAM_ROOT"

checkout_exact() {
  local name="$1"
  local url="$2"
  local commit="$3"
  local destination="${UPSTREAM_ROOT}/${name}"
  if [[ ! -d "${destination}/.git" ]]; then
    git clone --filter=blob:none --no-checkout "$url" "$destination"
  fi
  if [[ "$(git -C "$destination" remote get-url origin)" != "$url" ]]; then
    echo "$destination has an unexpected origin" >&2
    exit 1
  fi
  if ! git -C "$destination" cat-file -e "${commit}^{commit}" 2>/dev/null; then
    git -C "$destination" fetch --depth=1 origin "$commit"
  fi
  git -C "$destination" checkout --detach --quiet "$commit"
  if [[ "$(git -C "$destination" rev-parse HEAD)" != "$commit" ]]; then
    echo "$name did not resolve to the frozen commit" >&2
    exit 1
  fi
  echo "$name=$commit"
}

checkout_exact "llm-d-router" "https://github.com/llm-d/llm-d-router.git" "$LLM_D_ROUTER_COMMIT"
checkout_exact "llm-d" "https://github.com/llm-d/llm-d.git" "$LLM_D_COMMIT"

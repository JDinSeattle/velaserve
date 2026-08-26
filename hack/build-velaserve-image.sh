#!/usr/bin/env bash
set -euo pipefail

readonly REPOSITORY_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
image="${1:-}"
[[ -n "$image" ]] || { echo "usage: hack/build-velaserve-image.sh LOCAL_IMAGE" >&2; exit 2; }
for command_name in docker git; do
  command -v "$command_name" >/dev/null 2>&1 || { echo "build-velaserve-image: $command_name is required" >&2; exit 1; }
done
git -C "$REPOSITORY_ROOT" diff --quiet --exit-code
git -C "$REPOSITORY_ROOT" diff --cached --quiet --exit-code
[[ -z "$(git -C "$REPOSITORY_ROOT" ls-files --others --exclude-standard)" ]] || { echo "build-velaserve-image: untracked files make the source tree dirty" >&2; exit 1; }
repository_commit="$(git -C "$REPOSITORY_ROOT" rev-parse HEAD)"
[[ "$repository_commit" =~ ^[0-9a-f]{40}$ ]] || { echo "build-velaserve-image: invalid repository commit" >&2; exit 1; }
docker buildx build --platform linux/amd64 --load \
  --build-arg "VELASERVE_REPOSITORY_COMMIT=${repository_commit}" \
  --build-arg "VELASERVE_BUILD_DIRTY=false" \
  --label "io.velaserve.architecture=amd64" \
  --tag "$image" \
  "$REPOSITORY_ROOT"

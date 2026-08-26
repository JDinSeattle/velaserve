#!/usr/bin/env bash
set -euo pipefail

readonly HELM_VERSION="3.21.4"
readonly KIND_VERSION="0.32.0"
readonly REPOSITORY_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly BIN_DIR="${REPOSITORY_ROOT}/.tools/bin"

case "$(uname -s)" in
  Darwin) platform_os="darwin" ;;
  Linux) platform_os="linux" ;;
  *) echo "unsupported operating system: $(uname -s)" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  arm64|aarch64) platform_arch="arm64" ;;
  x86_64|amd64) platform_arch="amd64" ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

mkdir -p "$BIN_DIR"
temporary_directory="$(mktemp -d)"
trap 'rm -rf "$temporary_directory"' EXIT

checksum() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

download_verified() {
  local url="$1"
  local destination="$2"
  local checksum_url="$3"
  local checksum_file="${temporary_directory}/checksum"
  curl --fail --location --silent --show-error "$url" --output "$destination"
  curl --fail --location --silent --show-error "$checksum_url" --output "$checksum_file"
  local expected
  expected="$(awk 'NR == 1 {print $1}' "$checksum_file")"
  local actual
  actual="$(checksum "$destination")"
  if [[ -z "$expected" || "$actual" != "$expected" ]]; then
    echo "checksum mismatch for $url" >&2
    exit 1
  fi
}

if [[ ! -x "${BIN_DIR}/helm" ]] || ! "${BIN_DIR}/helm" version --short | grep -q "v${HELM_VERSION}"; then
  helm_archive="${temporary_directory}/helm.tar.gz"
  helm_url="https://get.helm.sh/helm-v${HELM_VERSION}-${platform_os}-${platform_arch}.tar.gz"
  download_verified "$helm_url" "$helm_archive" "${helm_url}.sha256sum"
  tar -xzf "$helm_archive" -C "$temporary_directory"
  install -m 0755 "${temporary_directory}/${platform_os}-${platform_arch}/helm" "${BIN_DIR}/helm"
fi

if [[ ! -x "${BIN_DIR}/kind" ]] || ! "${BIN_DIR}/kind" version | grep -q "v${KIND_VERSION}"; then
  kind_binary="${temporary_directory}/kind"
  kind_url="https://github.com/kubernetes-sigs/kind/releases/download/v${KIND_VERSION}/kind-${platform_os}-${platform_arch}"
  download_verified "$kind_url" "$kind_binary" "${kind_url}.sha256sum"
  install -m 0755 "$kind_binary" "${BIN_DIR}/kind"
fi

echo "helm=$(${BIN_DIR}/helm version --short)"
echo "kind=$(${BIN_DIR}/kind version)"

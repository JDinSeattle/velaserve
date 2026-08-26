#!/usr/bin/env bash
set -euo pipefail

readonly REPOSITORY_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly VERIFICATION_PATH="${REPOSITORY_ROOT}/.tools/verification.json"
export GOCACHE="${REPOSITORY_ROOT}/.cache/go-build"
export GOMODCACHE="${REPOSITORY_ROOT}/.cache/go-mod"

fail() {
  echo "verify-stage1: $*" >&2
  exit 1
}

require_file() {
  [[ -s "${REPOSITORY_ROOT}/$1" ]] || fail "required public artifact is missing or empty: $1"
}

contains_fixed() {
  local path="$1"
  local expected="$2"
  grep -Fq "$expected" "${REPOSITORY_ROOT}/${path}" || fail "$path is missing required text: $expected"
}

echo "verify-stage1: public artifact topology"
for path in \
  README.md \
  LICENSE \
  CONTRIBUTING.md \
  SECURITY.md \
  docs/architecture.md \
  docs/state-consistency.md \
  docs/benchmark-methodology.md \
  docs/failure-analysis.md \
  docs/performance-report.md \
  docs/cloud-handoff.md \
  docs/upstream-version-matrix.md \
  docs/gate-status.md \
  deploy/upstream-patches/llm-d-router-stage1-observer.patch \
  hack/build-pinned-epp.sh \
  scripts/security-audit.sh \
  .github/workflows/ci.yml; do
  require_file "$path"
done
contains_fixed README.md "gate status: NOT RUN ON REAL GPU"
contains_fixed README.md "paired-p95 bootstrap"
contains_fixed docs/gate-status.md "No production placement or source-pressure coordination code exists"
contains_fixed docs/performance-report.md "awaiting real-GPU Z0"
contains_fixed docs/cloud-handoff.md "cloud-preflight-binding.json"
contains_fixed docs/cloud-handoff.md "velaserve-gate compile"
for job in go-test race-and-property schemas-and-local-zeroing render-manifests terraform-validate security stage1-audit; do
  grep -Eq "^  ${job}:$" "${REPOSITORY_ROOT}/.github/workflows/ci.yml" || fail "CI job is missing: $job"
done

cd "$REPOSITORY_ROOT"
mkdir -p .tools

echo "verify-stage1: pinned tools, formatting, vet, tests, and schemas"
./hack/bootstrap-tools.sh
test -z "$(gofmt -l $(find . -name '*.go' -not -path './.cache/*' -not -path './.tools/*'))" || fail "gofmt reported unformatted Go files"
go vet ./...
go test ./... -count=1
go test ./... -race -count=1
go run ./cmd/schema-check
go test ./tests/integration -run TestLocalZeroingProducesVerifiedSimulationOnlyBundle -count=1

echo "verify-stage1: observability and deployment rendering"
command -v jq >/dev/null 2>&1 || fail "jq is required"
jq empty deploy/observability/grafana/velaserve.json
./hack/verify-render.sh

echo "verify-stage1: Terraform formatting and validation"
.tools/bin/terraform -chdir=infra/terraform fmt -check -recursive
.tools/bin/terraform -chdir=infra/terraform init -backend=false -input=false
.tools/bin/terraform -chdir=infra/terraform validate

echo "verify-stage1: public-source safety audit"
./scripts/security-audit.sh

docker_status="executed"
kind_attempted=0
cleanup_kind() {
  if [[ "$kind_attempted" == "1" ]]; then
    ./hack/kind-down.sh
  fi
}
trap 'cleanup_kind' EXIT

if [[ "${VELASERVE_SKIP_DOCKER:-0}" == "1" ]]; then
  [[ "${VELASERVE_ALLOW_NO_DOCKER:-0}" == "1" ]] || fail "Docker/Kind skip requires VELASERVE_ALLOW_NO_DOCKER=1"
  docker_status="waived-by-operator"
elif ! command -v docker >/dev/null 2>&1 || ! docker info >/dev/null 2>&1; then
  [[ "${VELASERVE_ALLOW_NO_DOCKER:-0}" == "1" ]] || fail "Docker Engine is unavailable; set VELASERVE_ALLOW_NO_DOCKER=1 only for an explicit recorded waiver"
  docker_status="waived-engine-unavailable"
else
  echo "verify-stage1: clean Kind Arm-B upstream SSE smoke"
  kind_attempted=1
  ./hack/kind-up.sh arm-b
  ./hack/kind-down.sh
  kind_attempted=0
fi

commit="$(git rev-parse HEAD)"
timestamp="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
temporary_verification="${VERIFICATION_PATH}.tmp"
printf '{\n  "schema_version": "velaserve.verification/v1",\n  "commit": "%s",\n  "verified_at": "%s",\n  "non_container_checks": "passed",\n  "docker_kind": "%s",\n  "aws_calls": "not-run"\n}\n' \
  "$commit" "$timestamp" "$docker_status" >"$temporary_verification"
mv "$temporary_verification" "$VERIFICATION_PATH"
jq empty "$VERIFICATION_PATH"
echo "verify-stage1: PASS; record=$VERIFICATION_PATH docker_kind=$docker_status aws_calls=not-run"

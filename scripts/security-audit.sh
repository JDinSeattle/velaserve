#!/usr/bin/env bash
set -euo pipefail

readonly REPOSITORY_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPOSITORY_ROOT"

fail() {
  echo "security-audit: $*" >&2
  exit 1
}

source_paths="$(mktemp)"
trap 'rm -f "$source_paths"' EXIT
while IFS= read -r -d '' path; do
  case "$path" in
    docs/superpowers/*|infra/terraform/.terraform/*) continue ;;
  esac
  [[ -f "$path" ]] && printf '%s\0' "$path"
done < <(git ls-files --cached --others --exclude-standard -z) >"$source_paths"

placeholder_pattern="T""BD|T""ODO|implement ""later|fill in ""details"
if xargs -0 grep -nE "$placeholder_pattern" <"$source_paths"; then
  fail "placeholder text remains in a public artifact"
fi

credential_pattern="AK""IA[0-9A-Z]{16}|AS""IA[0-9A-Z]{16}|-----BEGIN ([A-Z0-9 ]+ )?PRIVATE ""KEY-----"
if xargs -0 grep -nE "$credential_pattern" <"$source_paths"; then
  fail "probable credential material is present"
fi

if find cmd internal research benchmarks -name '*.go' -not -name '*_test.go' -print0 \
  | xargs -0 grep -nEi 'Valkey|CreatePlan|ClaimSlot|ReservePull'; then
  fail "gate-forbidden production coordination code is present"
fi

git diff --check
echo "security-audit: PASS"

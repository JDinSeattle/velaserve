#!/usr/bin/env bash
set -euo pipefail

output="${1:-}"
[[ -n "$output" ]] || { echo "usage: cloud-export-epp-logs.sh NEW_OUTPUT" >&2; exit 2; }
for name in VELASERVE_ARTIFACT_ROOT VELASERVE_NAMESPACE VELASERVE_EPP_SELECTOR VELASERVE_EPP_CONTAINER_NAME VELASERVE_EPP_REPLICAS; do
  [[ -n "${!name:-}" ]] || { echo "cloud-export-epp-logs: $name is required" >&2; exit 1; }
done
for command_name in kubectl jq; do
  command -v "$command_name" >/dev/null 2>&1 || { echo "cloud-export-epp-logs: $command_name is required" >&2; exit 1; }
done
[[ -f "$VELASERVE_ARTIFACT_ROOT/manifest.json" && ! -L "$VELASERVE_ARTIFACT_ROOT/manifest.json" ]] || { echo "cloud-export-epp-logs: verified run manifest is required" >&2; exit 1; }
[[ ! -e "$output" && ! -L "$output" ]] || { echo "cloud-export-epp-logs: output already exists" >&2; exit 1; }

created_at="$(jq -er '.created_at | select(type == "string" and length > 0)' "$VELASERVE_ARTIFACT_ROOT/manifest.json")"
pods_json="$(kubectl --namespace "$VELASERVE_NAMESPACE" get pods -l "$VELASERVE_EPP_SELECTOR" -o json)"
[[ "$(jq '.items | length' <<<"$pods_json")" == "$VELASERVE_EPP_REPLICAS" ]] || { echo "cloud-export-epp-logs: live EPP pod count does not match the run" >&2; exit 1; }

mkdir -p "$(dirname "$output")"
temporary="$(mktemp "${output}.tmp.XXXXXX")"
trap 'rm -f "$temporary"' EXIT
while IFS= read -r pod; do
  [[ -n "$pod" ]] || continue
  kubectl --namespace "$VELASERVE_NAMESPACE" logs "$pod" -c "$VELASERVE_EPP_CONTAINER_NAME" --since-time "$created_at" --timestamps --prefix >>"$temporary"
done < <(jq -r '.items[].metadata.name' <<<"$pods_json" | sort)
grep -Fq "VELASERVE_EPP_RECORD " "$temporary" || { echo "cloud-export-epp-logs: no observer records found across all EPP replicas" >&2; exit 1; }
mv "$temporary" "$output"
trap - EXIT
echo "cloud-export-epp-logs: exported all $VELASERVE_EPP_REPLICAS replicas to $output"

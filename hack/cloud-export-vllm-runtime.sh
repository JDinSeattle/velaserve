#!/usr/bin/env bash
set -euo pipefail

output="${1:-}"
[[ -n "$output" ]] || { echo "usage: cloud-export-vllm-runtime.sh NEW_OUTPUT" >&2; exit 2; }
for name in VELASERVE_ARTIFACT_ROOT VELASERVE_NAMESPACE VELASERVE_MODEL_SELECTOR VELASERVE_MODEL_CONTAINER_NAME; do
  [[ -n "${!name:-}" ]] || { echo "cloud-export-vllm-runtime: $name is required" >&2; exit 1; }
done
for command_name in kubectl jq; do
  command -v "$command_name" >/dev/null 2>&1 || { echo "cloud-export-vllm-runtime: $command_name is required" >&2; exit 1; }
done
[[ -f "$VELASERVE_ARTIFACT_ROOT/manifest.json" && ! -L "$VELASERVE_ARTIFACT_ROOT/manifest.json" ]] || { echo "cloud-export-vllm-runtime: verified run manifest is required" >&2; exit 1; }
[[ ! -e "$output" && ! -L "$output" ]] || { echo "cloud-export-vllm-runtime: output already exists" >&2; exit 1; }

created_at="$(jq -er '.created_at | select(type == "string" and length > 0)' "$VELASERVE_ARTIFACT_ROOT/manifest.json")"
pods_json="$(kubectl --namespace "$VELASERVE_NAMESPACE" get pods -l "$VELASERVE_MODEL_SELECTOR" -o json)"
(( $(jq '.items | length' <<<"$pods_json") > 0 )) || { echo "cloud-export-vllm-runtime: no model pods found" >&2; exit 1; }

mkdir -p "$(dirname "$output")"
raw="$(mktemp "${output}.raw.XXXXXX")"
matching="$(mktemp "${output}.matching.XXXXXX")"
temporary="$(mktemp "${output}.tmp.XXXXXX")"
trap 'rm -f "$raw" "$matching" "$temporary"' EXIT
while IFS= read -r pod; do
  [[ -n "$pod" ]] || continue
  kubectl --namespace "$VELASERVE_NAMESPACE" logs "$pod" -c "$VELASERVE_MODEL_CONTAINER_NAME" --since-time "$created_at" --timestamps --prefix >>"$raw"
done < <(jq -r '.items[].metadata.name' <<<"$pods_json" | sort)
grep -F "VELASERVE_VLLM_RUNTIME " "$raw" >"$matching" || { echo "cloud-export-vllm-runtime: no observer records found" >&2; exit 1; }
jq -R -c '
  (index("VELASERVE_VLLM_RUNTIME ") // error("observer marker missing")) as $offset
  | .[$offset + ("VELASERVE_VLLM_RUNTIME " | length):]
  | fromjson
  | select(type == "object")
' "$matching" >"$temporary"
[[ "$(wc -l <"$matching" | tr -d '[:space:]')" == "$(wc -l <"$temporary" | tr -d '[:space:]')" ]] || { echo "cloud-export-vllm-runtime: malformed observer record was dropped" >&2; exit 1; }
mv "$temporary" "$output"
rm -f "$raw" "$matching"
trap - EXIT
echo "cloud-export-vllm-runtime: exported $(wc -l <"$output" | tr -d '[:space:]') records to $output"

#!/usr/bin/env bash
set -euo pipefail

output="${1:-}"
[[ -n "$output" ]] || { echo "usage: cloud-export-envoy-logs.sh NEW_OUTPUT" >&2; exit 2; }
for name in VELASERVE_ARTIFACT_ROOT VELASERVE_NAMESPACE VELASERVE_EPP_SELECTOR VELASERVE_EPP_REPLICAS VELASERVE_EPP_PROXY_CONTAINER_NAME; do
  [[ -n "${!name:-}" ]] || { echo "cloud-export-envoy-logs: $name is required" >&2; exit 1; }
done
for command_name in kubectl jq; do
  command -v "$command_name" >/dev/null 2>&1 || { echo "cloud-export-envoy-logs: $command_name is required" >&2; exit 1; }
done
[[ -f "$VELASERVE_ARTIFACT_ROOT/manifest.json" && ! -L "$VELASERVE_ARTIFACT_ROOT/manifest.json" ]] || { echo "cloud-export-envoy-logs: verified run manifest is required" >&2; exit 1; }
[[ ! -e "$output" && ! -L "$output" ]] || { echo "cloud-export-envoy-logs: output already exists" >&2; exit 1; }

created_at="$(jq -er '.created_at | select(type == "string" and length > 0)' "$VELASERVE_ARTIFACT_ROOT/manifest.json")"
pods_json="$(kubectl --namespace "$VELASERVE_NAMESPACE" get pods -l "$VELASERVE_EPP_SELECTOR" -o json)"
[[ "$(jq '.items | length' <<<"$pods_json")" == "$VELASERVE_EPP_REPLICAS" ]] || { echo "cloud-export-envoy-logs: exact EPP pod set was not found" >&2; exit 1; }
jq -e --arg container "$VELASERVE_EPP_PROXY_CONTAINER_NAME" 'all(.items[]; any(.spec.containers[]; .name == $container) and any(.status.containerStatuses[]; .name == $container and .restartCount == 0))' <<<"$pods_json" >/dev/null || { echo "cloud-export-envoy-logs: inner Envoy sidecars are missing or restarted" >&2; exit 1; }

mkdir -p "$(dirname "$output")"
raw="$(mktemp "${output}.raw.XXXXXX")"
temporary="$(mktemp "${output}.tmp.XXXXXX")"
trap 'rm -f "$raw" "$temporary"' EXIT
while IFS= read -r pod; do
	[[ -n "$pod" ]] || continue
	kubectl --namespace "$VELASERVE_NAMESPACE" logs "$pod" -c "$VELASERVE_EPP_PROXY_CONTAINER_NAME" --since-time "$created_at" >>"$raw"
done < <(jq -r '.items[].metadata.name' <<<"$pods_json" | sort)

jq -R -c '
  fromjson?
  | select(type == "object")
  | select(."REQ(X-REQUEST-ID)" != null and ."REQ(X-REQUEST-ID)" != "-")
  | select(."REQ(X-VELA-FANOUT-GROUP)" != null and ."REQ(X-VELA-FANOUT-GROUP)" != "-")
  | {
      "START_TIME": .START_TIME,
      "REQ(X-REQUEST-ID)": ."REQ(X-REQUEST-ID)",
      "REQ(X-VELA-FANOUT-GROUP)": ."REQ(X-VELA-FANOUT-GROUP)",
      "UPSTREAM_HOST": .UPSTREAM_HOST,
      "RESPONSE_CODE": .RESPONSE_CODE,
      "DURATION": .DURATION,
      "TRACE_ID": (.TRACE_ID // ""),
      "SPAN_ID": (.SPAN_ID // "")
    }
' "$raw" >"$temporary"
[[ -s "$temporary" ]] || { echo "cloud-export-envoy-logs: no VelaServe request records were found" >&2; exit 1; }
mv "$temporary" "$output"
rm -f "$raw"
trap - EXIT
echo "cloud-export-envoy-logs: exported $(wc -l <"$output" | tr -d '[:space:]') records to $output"

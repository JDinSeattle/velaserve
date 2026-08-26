#!/usr/bin/env bash
set -euo pipefail

namespace="${VELASERVE_NAMESPACE:-velaserve-z0}"
selector="${VELASERVE_MODEL_SELECTOR:-app.kubernetes.io/name=velaserve-model}"
service="${VELASERVE_MODEL_SERVICE_NAME:-velaserve-model}"
for command_name in kubectl jq; do command -v "$command_name" >/dev/null 2>&1 || { echo "cloud-condition-endpoints: $command_name is required" >&2; exit 1; }; done
pods="$(kubectl --namespace "$namespace" get pods -l "$selector" -o json)"
jq -e '(.items|length)>=2 and all(.items[];.status.phase=="Running" and any(.status.conditions[]?;.type=="Ready" and .status=="True"))' <<<"$pods" >/dev/null || { echo "cloud-condition-endpoints: every selected model pod must be ready" >&2; exit 1; }
jq -c --arg namespace "$namespace" --arg service "$service" '
  [.items[] | .metadata.name as $id | ($id+"."+$service+"-headless."+$namespace+".svc.cluster.local:8200") as $host |
    {id:$id,chat_url:("http://"+$host+"/v1/chat/completions"),reset_url:("http://"+$host+"/reset_prefix_cache"),metrics_url:("http://"+$host+"/metrics")}] | sort_by(.id)
' <<<"$pods"

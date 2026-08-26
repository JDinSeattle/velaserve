{{- define "velaserve.labels" -}}
app.kubernetes.io/part-of: velaserve
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | quote }}
{{- end -}}

{{- define "velaserve.image" -}}
{{ printf "%s:%s" .Values.images.velaserve.repository .Values.images.velaserve.tag }}
{{- end -}}

{{- define "velaserve.validate" -}}
{{- $profiles := list "arm-a-affinity-p2p" "arm-b-load-aware-p2p" -}}
{{- if not (has .Values.routingProfile $profiles) -}}
{{- fail (printf "routingProfile %q is not a frozen Stage 1 arm" .Values.routingProfile) -}}
{{- end -}}
{{- if and (gt (int .Values.eppReplicas) 1) (not .Values.activeActiveObservation) -}}
{{- fail "eppReplicas > 1 requires activeActiveObservation=true and remains dispersion-only" -}}
{{- end -}}
{{- end -}}

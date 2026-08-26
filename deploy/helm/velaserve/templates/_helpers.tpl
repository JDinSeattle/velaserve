{{- define "velaserve.labels" -}}
app.kubernetes.io/part-of: velaserve
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | quote }}
{{- end -}}

{{- define "velaserve.image" -}}
{{- if .Values.images.velaserve.digest -}}
{{ printf "%s@%s" .Values.images.velaserve.repository .Values.images.velaserve.digest }}
{{- else -}}
{{ printf "%s:%s" .Values.images.velaserve.repository .Values.images.velaserve.tag }}
{{- end -}}
{{- end -}}

{{- define "velaserve.validate" -}}
{{- $profiles := list "arm-a-affinity-p2p" "arm-b-load-aware-p2p" -}}
{{- if not (has .Values.routingProfile $profiles) -}}
{{- fail (printf "routingProfile %q is not a frozen Stage 1 arm" .Values.routingProfile) -}}
{{- end -}}
{{- if and (gt (int .Values.eppReplicas) 1) (not .Values.activeActiveObservation) -}}
{{- fail "eppReplicas > 1 requires activeActiveObservation=true and remains dispersion-only" -}}
{{- end -}}
{{- if and (eq .Values.evidenceScope "real_gpu") (not (regexMatch "^sha256:[a-f0-9]{64}$" .Values.images.velaserve.digest)) -}}
{{- fail "real_gpu evidence requires images.velaserve.digest with an exact sha256 digest" -}}
{{- end -}}
{{- if and (eq .Values.evidenceScope "real_gpu") (not (regexMatch "^.+@sha256:[a-f0-9]{64}$" .Values.images.upstreamEPP.tag)) -}}
{{- fail "real_gpu evidence requires a digest-addressed upstream EPP image in images.upstreamEPP.tag" -}}
{{- end -}}
{{- if and (eq .Values.evidenceScope "real_gpu") (not .Values.clockProbe.enabled) -}}
{{- fail "real_gpu evidence requires the node-local clock probe" -}}
{{- end -}}
{{- end -}}

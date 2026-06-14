{{- define "silkstrand-agent.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "silkstrand-agent.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name (include "silkstrand-agent.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "silkstrand-agent.labels" -}}
app.kubernetes.io/name: {{ include "silkstrand-agent.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: silkstrand
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
{{- end -}}

{{- define "silkstrand-agent.selectorLabels" -}}
app.kubernetes.io/name: {{ include "silkstrand-agent.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "silkstrand-agent.image" -}}
{{- printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag) -}}
{{- end -}}

{{/* Name of the Secret holding enrollment creds (existing or chart-created). */}}
{{- define "silkstrand-agent.secretName" -}}
{{- if .Values.auth.existingSecret -}}
{{- .Values.auth.existingSecret -}}
{{- else -}}
{{- include "silkstrand-agent.fullname" . -}}
{{- end -}}
{{- end -}}

{{/* Validate that EXACTLY one enrollment method is supplied (unless existingSecret). */}}
{{- define "silkstrand-agent.validateAuth" -}}
{{- if not .Values.auth.existingSecret -}}
{{- $tok := .Values.auth.installToken -}}
{{- $id := .Values.auth.agentId -}}
{{- $key := .Values.auth.agentKey -}}
{{- $pair := and $id $key -}}
{{- if and (not $tok) (not $pair) -}}
{{- fail "silkstrand-agent: set auth.installToken OR (auth.agentId + auth.agentKey) OR auth.existingSecret" -}}
{{- end -}}
{{- if and $tok $pair -}}
{{- fail "silkstrand-agent: set auth.installToken OR auth.agentId+agentKey, not both (explicit id/key would override the bootstrapped identity)" -}}
{{- end -}}
{{- if or (and $id (not $key)) (and $key (not $id)) -}}
{{- fail "silkstrand-agent: auth.agentId and auth.agentKey must be set together" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/* Single identity + RWO creds: >1 replica would share creds / dup the WSS
     session. Pools require per-agent identity + pool-join tokens (ADR 016). */}}
{{- define "silkstrand-agent.validateReplicas" -}}
{{- if gt (int .Values.replicaCount) 1 -}}
{{- fail "silkstrand-agent: replicaCount > 1 is unsupported (one identity + a RWO creds PVC). Horizontal pools need per-agent identity + pool-join tokens — see ADR 016." -}}
{{- end -}}
{{- end -}}

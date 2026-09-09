{{- define "polardbPg.componentDefinitionName" -}}
{{- $name := required "componentDefinition.name is required" .Values.componentDefinition.name -}}
{{- if gt (len $name) 35 -}}
{{- fail "componentDefinition.name must be 35 characters or fewer" -}}
{{- end -}}
{{- $name -}}
{{- end -}}

{{- define "polardbPg.resourceName" -}}
{{- printf "%s-%s" (include "polardbPg.componentDefinitionName" .) .suffix | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "polardbPg.image" -}}
{{- $image := printf "%s/%s" (.Values.image.registry | default "docker.io") .Values.image.repository -}}
{{- if .Values.image.digest -}}
{{- printf "%s@%s" $image .Values.image.digest -}}
{{- else -}}
{{- printf "%s:%s" $image .Values.image.tag -}}
{{- end -}}
{{- end -}}

{{- define "polardbPg.labels" -}}
app.kubernetes.io/name: polardb-pg
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end -}}

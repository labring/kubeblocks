{{- define "polardbPgStackOps.controlComponentDefinitionName" -}}
{{- $name := required "controlComponentDefinition.name is required" .Values.controlComponentDefinition.name -}}
{{- if gt (len $name) 22 -}}
{{- fail "controlComponentDefinition.name must be 22 characters or fewer for KubeBlocks 0.8 Cluster componentDef references" -}}
{{- end -}}
{{- $name -}}
{{- end -}}

{{- define "polardbPgStackOps.image" -}}
{{- $image := printf "%s/%s" (.Values.image.registry | default "docker.io") .Values.image.repository -}}
{{- if not .Values.image.digest -}}
{{- fail "image.digest is required; Stack control Operations must use an immutable image" -}}
{{- end -}}
{{- printf "%s@%s" $image .Values.image.digest -}}
{{- end -}}

{{- define "polardbPgStackOps.labels" -}}
app.kubernetes.io/name: polardb-pg-stack-ops
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end -}}

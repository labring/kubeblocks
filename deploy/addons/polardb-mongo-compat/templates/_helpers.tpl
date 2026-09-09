{{- define "polardbMongoCompat.labels" -}}
app.kubernetes.io/name: polardb-mongo-compat
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end -}}

{{- define "polardbMongoCompat.resourceName" -}}
{{- printf "%s-%s" .Release.Name .suffix | trunc 63 | trimSuffix "-" -}}
{{- end -}}

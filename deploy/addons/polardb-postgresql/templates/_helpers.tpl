{{/*
Expand the name of the chart.
*/}}
{{- define "postgresql.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "postgresql.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
The ComponentDefinition name identifies an immutable HA implementation. All
definition-scoped resources use this prefix so releases can retain an older
definition while new clusters use the next one.
*/}}
{{- define "polardbPostgresql.componentDefinitionName" -}}
{{- $name := required "ha.componentDefinition.name is required" .Values.ha.componentDefinition.name -}}
{{- if gt (len $name) 35 -}}
{{- fail "ha.componentDefinition.name must be 35 characters or fewer" -}}
{{- end -}}
{{- $name -}}
{{- end }}

{{- define "polardbPostgresql.componentResourceName" -}}
{{- printf "%s-%s" (include "polardbPostgresql.componentDefinitionName" .) .suffix | trunc 63 | trimSuffix "-" -}}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "postgresql.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "postgresql.labels" -}}
helm.sh/chart: {{ include "postgresql.chart" . }}
{{ include "postgresql.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "postgresql.selectorLabels" -}}
app.kubernetes.io/name: {{ include "postgresql.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "postgresql.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "postgresql.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Return true if a configmap object should be created for PostgreSQL primary with the configuration
*/}}
{{- define "postgresql.primary.createConfigmap" -}}
{{- if and (or .Values.primary.configuration .Values.primary.pgHbaConfiguration) (not .Values.primary.existingConfigmap) }}
    {{- true -}}
{{- else -}}
{{- end -}}
{{- end -}}

{{/*
Return PostgreSQL service port
*/}}
{{- define "postgresql.service.port" -}}
{{- .Values.primary.service.ports.postgresql -}}
{{- end -}}

{{/*
Return the name for a custom database to create
*/}}
{{- define "postgresql.database" -}}
{{- .Values.auth.database -}}
{{- end -}}

{{/*
Get the password key.
*/}}
{{/* TODO: use $(RANDOM_PASSWD) instead */}}
{{- define "postgresql.postgresPassword" -}}
{{- if or (.Release.IsInstall) (not (lookup "apps.kubeblocks.io/v1alpha1" "ClusterDefinition" "" "postgresql")) -}}
{{ .Values.auth.postgresPassword | default (randAlphaNum 10) }}
{{- else -}}
{{ index (lookup "apps.kubeblocks.io/v1alpha1" "ClusterDefinition" "" "postgresql").spec.connectionCredential "password"}}
{{- end }}
{{- end }}

{{/*
Generate scripts configmap
*/}}
{{- define "postgresql.extend.scripts" -}}
{{- range $path, $_ :=  $.Files.Glob "scripts/**" }}
{{ $path | base }}: |-
{{- $.Files.Get $path | nindent 2 }}
{{- end }}
{{- end }}

{{/*
Check if cluster version is enabled, if enabledClusterVersions is empty, return true,
otherwise, check if the cluster version is in the enabledClusterVersions list, if yes, return true,
else return false.
Parameters: cvName, values
*/}}
{{- define "postgresql.isClusterVersionEnabled" -}}
{{- $cvName := .cvName -}}
{{- $enabledClusterVersions := .values.enabledClusterVersions -}}
{{- if eq (len $enabledClusterVersions) 0 -}}
    {{- true -}}
{{- else -}}
    {{- range $enabledClusterVersions -}}
        {{- if eq $cvName . -}}
            {{- true -}}
        {{- end -}}
    {{- end -}}
{{- end -}}
{{- end -}}

{{/*
Define image
*/}}
{{- define "polardbPostgresql.image" -}}
{{- $image := printf "%s/%s" (.registry | default "docker.io") .repository -}}
{{- if .digest -}}
{{- printf "%s@%s" $image .digest -}}
{{- else -}}
{{- printf "%s:%s" $image .tag -}}
{{- end -}}
{{- end -}}

{{- define "postgresql.image.major12.minor140" -}}
{{ include "polardbPostgresql.image" (dict "registry" .Values.image.registry "repository" .Values.image.repository "tag" .Values.image.tags.major12.minor140 "digest" .Values.image.digest) }}
{{- end }}

{{- define "postgresql.image.major12.minor141" -}}
{{ include "polardbPostgresql.image" (dict "registry" .Values.image.registry "repository" .Values.image.repository "tag" .Values.image.tags.major12.minor141 "digest" .Values.image.digest) }}
{{- end }}

{{- define "postgresql.image.major12.minor150" -}}
{{ include "polardbPostgresql.image" (dict "registry" .Values.image.registry "repository" .Values.image.repository "tag" .Values.image.tags.major12.minor150 "digest" .Values.image.digest) }}
{{- end }}

{{- define "postgresql.image.major14.minor072" -}}
{{ include "polardbPostgresql.image" (dict "registry" .Values.image.registry "repository" .Values.image.repository "tag" .Values.image.tags.major14.minor072 "digest" .Values.image.digest) }}
{{- end }}

{{- define "postgresql.image.major14.minor080" -}}
{{ include "polardbPostgresql.image" (dict "registry" .Values.image.registry "repository" .Values.image.repository "tag" .Values.image.tags.major14.minor080 "digest" .Values.image.digest) }}
{{- end }}

{{- define "postgresql.image.major15.minor070" -}}
{{ include "polardbPostgresql.image" (dict "registry" .Values.image.registry "repository" .Values.image.repository "tag" .Values.image.tags.major15.minor070 "digest" .Values.image.digest) }}
{{- end }}

{{- define "postgresql.image.major16.minor040" -}}
{{ include "polardbPostgresql.image" (dict "registry" .Values.image.registry "repository" .Values.image.repository "tag" .Values.image.tags.major16.minor040 "digest" .Values.image.digest) }}
{{- end }}

{{- define "pgbouncer.image" -}}
{{ include "polardbPostgresql.image" (dict "registry" (.Values.pgbouncer.image.registry | default (.Values.image.registry | default "docker.io")) "repository" .Values.pgbouncer.image.repository "tag" .Values.pgbouncer.image.tag "digest" .Values.pgbouncer.image.digest) }}
{{- end }}

{{- define "metrics.image" -}}
{{ include "polardbPostgresql.image" (dict "registry" (.Values.metrics.image.registry | default (.Values.image.registry | default "docker.io")) "repository" .Values.metrics.image.repository "tag" .Values.metrics.image.tag "digest" .Values.metrics.image.digest) }}
{{- end }}

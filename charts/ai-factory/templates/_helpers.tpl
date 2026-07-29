{{/*
Expand the name of the chart.
*/}}
{{- define "ai-factory.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "ai-factory.fullname" -}}
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
Create chart name and version as used by the chart label.
*/}}
{{- define "ai-factory.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "ai-factory.labels" -}}
helm.sh/chart: {{ include "ai-factory.chart" . }}
{{ include "ai-factory.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "ai-factory.selectorLabels" -}}
app.kubernetes.io/name: {{ include "ai-factory.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Server image
*/}}
{{- define "ai-factory.serverImage" -}}
{{- printf "%s:%s" .Values.server.image.repository (default .Chart.AppVersion .Values.server.image.tag) }}
{{- end }}

{{/*
Sandbox image
*/}}
{{- define "ai-factory.sandboxImage" -}}
{{- printf "%s:%s" .Values.sandbox.image.repository (default "latest" .Values.sandbox.image.tag) }}
{{- end }}

{{/*
Namespace
*/}}
{{- define "ai-factory.namespace" -}}
{{- default .Release.Namespace .Values.namespace }}
{{- end }}

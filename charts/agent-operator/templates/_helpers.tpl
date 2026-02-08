{{/*
Expand the name of the chart.
*/}}
{{- define "agent-operator.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "agent-operator.fullname" -}}
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
{{- define "agent-operator.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "agent-operator.labels" -}}
helm.sh/chart: {{ include "agent-operator.chart" . }}
{{ include "agent-operator.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "agent-operator.selectorLabels" -}}
app.kubernetes.io/name: {{ include "agent-operator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
control-plane: controller-manager
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "agent-operator.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "agent-operator.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Operator image
*/}}
{{- define "agent-operator.operatorImage" -}}
{{- $tag := .Values.operatorImage.tag | default .Chart.AppVersion }}
{{- printf "%s:%s" .Values.operatorImage.repository $tag }}
{{- end }}

{{/*
Agent runner image
*/}}
{{- define "agent-operator.agentRunnerImage" -}}
{{- $tag := .Values.agentRunnerImage.tag | default .Chart.AppVersion }}
{{- printf "%s:%s" .Values.agentRunnerImage.repository $tag }}
{{- end }}

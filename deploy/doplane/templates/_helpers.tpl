{{/*
Expand the name of the chart.
*/}}
{{- define "doplane.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "doplane.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "doplane.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Common labels.
*/}}
{{- define "doplane.labels" -}}
helm.sh/chart: {{ include "doplane.chart" . }}
{{ include "doplane.selectorLabels" . }}
app.kubernetes.io/part-of: doplane
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
{{- end -}}

{{/*
Selector labels.
*/}}
{{- define "doplane.selectorLabels" -}}
app.kubernetes.io/name: {{ include "doplane.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
Manager selector labels.
*/}}
{{- define "doplane.managerSelectorLabels" -}}
{{ include "doplane.selectorLabels" . }}
app.kubernetes.io/component: manager
control-plane: controller-manager
{{- end -}}

{{/*
Create the service account name to use.
*/}}
{{- define "doplane.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "doplane.controllerManagerName" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "doplane.controllerManagerName" -}}
{{- printf "%s-controller-manager" (include "doplane.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "doplane.metricsServiceName" -}}
{{- printf "%s-metrics" (include "doplane.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "doplane.metricsMonitorName" -}}
{{- printf "%s-metrics-monitor" (include "doplane.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Build an image reference from fullName, repository, tag, and digest fields.
*/}}
{{- define "doplane.imageRef" -}}
{{- if .fullName -}}
{{- .fullName -}}
{{- else if .digest -}}
{{- printf "%s@%s" .repository .digest -}}
{{- else -}}
{{- printf "%s:%s" .repository .tag -}}
{{- end -}}
{{- end -}}

{{- define "doplane.managerImage" -}}
{{- include "doplane.imageRef" .Values.image -}}
{{- end -}}

{{- define "doplane.runnerImage" -}}
{{- include "doplane.imageRef" .Values.runnerImage -}}
{{- end -}}

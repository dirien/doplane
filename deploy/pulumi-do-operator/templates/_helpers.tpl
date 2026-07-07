{{/*
Expand the name of the chart.
*/}}
{{- define "pulumi-do-operator.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "pulumi-do-operator.fullname" -}}
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
{{- define "pulumi-do-operator.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Common labels.
*/}}
{{- define "pulumi-do-operator.labels" -}}
helm.sh/chart: {{ include "pulumi-do-operator.chart" . }}
{{ include "pulumi-do-operator.selectorLabels" . }}
app.kubernetes.io/part-of: pulumi-do-operator
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
{{- end -}}

{{/*
Selector labels.
*/}}
{{- define "pulumi-do-operator.selectorLabels" -}}
app.kubernetes.io/name: {{ include "pulumi-do-operator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
Manager selector labels.
*/}}
{{- define "pulumi-do-operator.managerSelectorLabels" -}}
{{ include "pulumi-do-operator.selectorLabels" . }}
app.kubernetes.io/component: manager
control-plane: controller-manager
{{- end -}}

{{/*
Create the service account name to use.
*/}}
{{- define "pulumi-do-operator.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "pulumi-do-operator.controllerManagerName" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "pulumi-do-operator.controllerManagerName" -}}
{{- printf "%s-controller-manager" (include "pulumi-do-operator.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "pulumi-do-operator.metricsServiceName" -}}
{{- printf "%s-metrics" (include "pulumi-do-operator.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "pulumi-do-operator.metricsMonitorName" -}}
{{- printf "%s-metrics-monitor" (include "pulumi-do-operator.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Build an image reference from fullName, repository, tag, and digest fields.
*/}}
{{- define "pulumi-do-operator.imageRef" -}}
{{- if .fullName -}}
{{- .fullName -}}
{{- else if .digest -}}
{{- printf "%s@%s" .repository .digest -}}
{{- else -}}
{{- printf "%s:%s" .repository .tag -}}
{{- end -}}
{{- end -}}

{{- define "pulumi-do-operator.managerImage" -}}
{{- include "pulumi-do-operator.imageRef" .Values.image -}}
{{- end -}}

{{- define "pulumi-do-operator.runnerImage" -}}
{{- include "pulumi-do-operator.imageRef" .Values.runnerImage -}}
{{- end -}}

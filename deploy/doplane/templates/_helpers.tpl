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
Name of the plugin cache PVC runner Jobs mount.
*/}}
{{- define "doplane.pluginCacheClaimName" -}}
{{- if .Values.pluginCache.existingClaim -}}
{{- .Values.pluginCache.existingClaim -}}
{{- else -}}
{{- printf "%s-plugin-cache" (include "doplane.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{/*
Manager permissions on namespaced resources. Rendered into the manager
ClusterRole (cluster-wide install) or into a Role per watched namespace
(namespace-scoped install) — keep both shapes fed from this single list.
*/}}
{{- define "doplane.managerNamespacedRules" -}}
- apiGroups:
    - ""
  resources:
    - events
  verbs:
    - create
    - patch
- apiGroups:
    - ""
  resources:
    - pods
  verbs:
    - get
    - list
    - watch
- apiGroups:
    - ""
  resources:
    - pods/log
  verbs:
    - get
- apiGroups:
    - ""
  resources:
    - secrets
  verbs:
    - get
    - create
    - update
- apiGroups:
    - ""
  resources:
    - persistentvolumeclaims
  verbs:
    - get
- apiGroups:
    - batch
  resources:
    - jobs
  verbs:
    - create
    - delete
    - get
    - list
    - watch
- apiGroups:
    - do.pulumi.com
  resources:
    - docomposites/status
    - doproviderconfigs/status
    - doresources/status
  verbs:
    - get
    - patch
    - update
- apiGroups:
    - do.pulumi.com
  resources:
    - doproviderconfigs
    - dousages
  verbs:
    - get
    - list
    - watch
{{- range prepend .Values.compositeApiGroups "typed.do.pulumi.com" | uniq }}
- apiGroups:
    - {{ . }}
  resources:
    - '*'
  verbs:
    - get
    - list
    - watch
    - update
    - patch
- apiGroups:
    - {{ . }}
  resources:
    - '*/status'
  verbs:
    - get
    - update
    - patch
{{- end }}
- apiGroups:
    - do.pulumi.com
  resources:
    - docomposites
    - doresources
  verbs:
    - create
    - delete
    - get
    - list
    - patch
    - update
    - watch
- apiGroups:
    - do.pulumi.com
  resources:
    - docomposites/finalizers
    - doresources/finalizers
  verbs:
    - update
{{- end -}}

{{/*
Manager permissions on cluster-scoped resources (DoCompositeDefinition) —
required in both install shapes.
*/}}
{{- define "doplane.managerClusterRules" -}}
- apiGroups:
    - apiextensions.k8s.io
  resources:
    - customresourcedefinitions
  verbs:
    - get
    - create
    - update
    - delete
- apiGroups:
    - apiextensions.k8s.io
  resources:
    - customresourcedefinitions/status
  verbs:
    - update
- apiGroups:
    - do.pulumi.com
  resources:
    - docompositedefinitionrevisions
  verbs:
    - get
    - list
    - watch
    - create
    - delete
- apiGroups:
    - do.pulumi.com
  resources:
    - docompositedefinitions
  verbs:
    - get
    - list
    - watch
    - update
    - patch
- apiGroups:
    - do.pulumi.com
  resources:
    - docompositedefinitions/finalizers
  verbs:
    - update
- apiGroups:
    - do.pulumi.com
  resources:
    - doproviders
  verbs:
    - get
    - list
    - watch
- apiGroups:
    - do.pulumi.com
  resources:
    - docompositedefinitions/status
    - doproviders/status
  verbs:
    - get
    - patch
    - update
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
{{- if and (not .Values.image.fullName) (not .Values.image.digest) (not .Values.image.tag) -}}
{{- printf "%s:%s" .Values.image.repository .Chart.AppVersion -}}
{{- else -}}
{{- include "doplane.imageRef" .Values.image -}}
{{- end -}}
{{- end -}}

{{- define "doplane.runnerImage" -}}
{{- if and (not .Values.runnerImage.fullName) (not .Values.runnerImage.digest) (not .Values.runnerImage.tag) -}}
{{- printf "%s:%s" .Values.runnerImage.repository .Chart.AppVersion -}}
{{- else -}}
{{- include "doplane.imageRef" .Values.runnerImage -}}
{{- end -}}
{{- end -}}

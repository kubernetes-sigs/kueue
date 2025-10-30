{{- /*
Copyright The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/ -}}

{{/*
Expand the name of the chart.
*/}}
{{- define "kueue.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "kueue.fullname" -}}
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
{{- define "kueue.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "kueue.labels" -}}
helm.sh/chart: {{ include "kueue.chart" . }}
{{ include "kueue.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "kueue.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kueue.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
control-plane: controller-manager
{{- end }}

{{/*
Labels for metrics service
*/}}
{{- define "kueue.metricsService.labels" -}}
{{ include "kueue.labels" . }}
app.kubernetes.io/component: metrics
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "kueue.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "kueue.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
FeatureGates
*/}}
{{- define "kueue.featureGates" -}}
{{- $features := "" }}
{{- range .Values.controllerManager.featureGates }}
{{- $str := printf "%s=%t," .name .enabled }}
{{- $features = print $features $str }}
{{- end }}
{{- with .Values.controllerManager.featureGates }}
- --feature-gates={{ $features | trimSuffix "," }}
{{- end }}
{{- end }}

{{/*
IsFeatureGateEnabled - outputs true if the feature gate .Feature is enabled in the .List
Usage:
  {{- if include "kueue.isFeatureGateEnabled" (dict "List" .Values.controllerManager.featureGates "Feature" "VisibilityOnDemand") }}
*/}}
{{- define "kueue.isFeatureGateEnabled" -}}
{{- $feature := .Feature }}
{{- $enabled := false }}
{{- range .List }}
{{- if (and (eq .name $feature) (eq .enabled true)) }}
{{- $enabled = true }}
{{- end }}
{{- end }}
{{- if $enabled }}
{{- $enabled -}}
{{- end }}
{{- end }}

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
app.kubernetes.io/component: metrics-service
{{- end }}

{{/*
Labels for webhook service
*/}}
{{- define "kueue.webhookService.labels" -}}
{{ include "kueue.labels" . }}
app.kubernetes.io/component: webhook-service
{{- end }}

{{/*
Labels for visibility service
*/}}
{{- define "kueue.visibilityService.labels" -}}
{{ include "kueue.labels" . }}
app.kubernetes.io/component: visibility-service
{{- end }}

{{/*
Labels for controller-manager
*/}}
{{- define "kueue.controllerManager.labels" -}}
{{ include "kueue.labels" . }}
app.kubernetes.io/component: controller
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

{{/*
Cert-manager issuerRef for the chart-managed certificates.
*/}}
{{- define "kueue.certManager.issuerRef" -}}
{{- if .Values.certManager.issuerRef }}
{{- toYaml .Values.certManager.issuerRef }}
{{- else }}
kind: Issuer
name: '{{ include "kueue.fullname" . }}-selfsigned-issuer'
{{- end }}
{{- end }}

{{/*
Apply structured manager configuration to the defaults in place.
Maps merge recursively, lists and scalars replace, and null removes a key.
*/}}
{{- define "kueue.mergeManagerConfig" -}}
{{- range $key, $value := .source }}
{{- if eq $value nil }}
{{- $_ := unset $.target $key }}
{{- else if kindIs "map" $value }}
{{- if not (kindIs "map" (get $.target $key)) }}
{{- $_ := set $.target $key (dict) }}
{{- end }}
{{- $_ := include "kueue.mergeManagerConfig" (dict "target" (get $.target $key) "source" $value) }}
{{- else }}
{{- $_ := set $.target $key (deepCopy $value) }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Resolve manager configuration once for every consumer.
An explicit mode selects the input. Otherwise, a supplied YAML string takes
precedence, including an empty string; merge structured overrides when YAML is absent.
*/}}
{{- define "kueue.managerConfig" -}}
{{- $values := dict }}
{{- if hasKey .Values "managerConfig" }}
{{- $values = .Values.managerConfig }}
{{- end }}
{{- if not (kindIs "map" $values) }}
{{- fail "managerConfig must be a map" }}
{{- end }}
{{- $mode := default "" $values.mode }}
{{- if and (ne $mode "structured") (hasKey $values "controllerManagerConfigYaml") }}
{{- $values.controllerManagerConfigYaml | fromYaml | toYaml }}
{{- else }}
{{- if hasKey $values "structuredOverrides" }}
{{- if not (kindIs "map" $values.structuredOverrides) }}
{{- fail "managerConfig.structuredOverrides must be a map" }}
{{- end }}
{{- end }}
{{- $config := include "kueue.managerConfigDefaults" . | fromYaml }}
{{- if and (ne $mode "yaml") (hasKey $values "structuredOverrides") }}
{{- $_ := include "kueue.mergeManagerConfig" (dict "target" $config "source" $values.structuredOverrides) }}
{{- end }}
{{- toYaml $config }}
{{- end }}
{{- end }}

{{/* ControllerManager ingress ports follow its configuration rather than chart defaults. */}}
{{- define "kueue.networkPolicy.managerIngress" -}}
{{- $config := include "kueue.managerConfig" . | fromYaml -}}
{{- $metricsAddress := get (get $config "metrics" | default dict) "bindAddress" | default ":8443" -}}
{{- $healthAddress := get (get $config "health" | default dict) "healthProbeBindAddress" | default ":8081" -}}
{{- $webhookPort := get (get $config "webhook" | default dict) "port" | default 9443 | int -}}
{{- $visibility := get $config "visibilityServer" | default dict -}}
{{- $visibilityPort := 8082 -}}
{{- if and (hasKey $visibility "bindPort") (ne $visibility.bindPort nil) -}}
{{- $visibilityPort = int $visibility.bindPort -}}
{{- end -}}
{{- with .Values.networkPolicy.extraIngress }}
{{ toYaml . }}
{{- end }}
- ports:
{{- if gt $webhookPort 0 }}
    - protocol: TCP
      port: {{ $webhookPort }}
{{- end }}
    - protocol: TCP
      port: {{ $visibilityPort }}
{{- if ne $metricsAddress "0" }}
- ports:
    - protocol: TCP
      port: {{ last (splitList ":" $metricsAddress) | int }}
{{- end }}
{{- if ne $healthAddress "0" }}
- ports:
    - protocol: TCP
      port: {{ last (splitList ":" $healthAddress) | int }}
{{- end }}
{{- end }}

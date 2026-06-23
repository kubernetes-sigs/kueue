{{ define "packages" -}}

{{- range $idx, $val := .packages -}}
{{/* Special handling for config */}}
  {{- if .IsMain -}}
---
title: {{ .Title }}
content_type: tool-reference
package: {{ .DisplayName }}
auto_generated: true
description: Generated API reference documentation for {{ if ne .GroupName "" -}} {{ .DisplayName }}{{ else -}} Kueue Configuration{{- end -}}.
---
{{ .GetComment -}}
  {{- end -}}
{{- end }}

## Resource Types 

{{ range .packages -}}
  {{ $isConfig := (eq .GroupName "") }}
  {{- range .VisibleTypes -}}
    {{- if or .IsExported (and $isConfig (eq .DisplayName "Configuration")) }}
- [{{ .DisplayName }}]({{ .Link }})
    {{- end -}}
  {{- end -}}
{{- end -}}

{{ range .packages }}
  {{ if ne .GroupName "" -}}
    {{/* For package with a group name, list all type definitions in it. */}}
    {{- range .VisibleTypes }}
      {{- if or .Referenced .IsExported -}}
{{ template "type" . }}
      {{- end -}}
    {{ end }}
  {{ else }}
    {{/* For package w/o group name, list only types referenced. */}}
    {{ $isConfig := (eq .GroupName "") }}
    {{- range .VisibleTypes -}}
      {{- if or .Referenced $isConfig -}}
{{ template "type" . }}
      {{- end -}}
    {{- end }}
  {{- end }}
{{- end }}
{{- end }}

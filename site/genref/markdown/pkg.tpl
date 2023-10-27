{{ define "packages" -}}

{{- range $idx, $val := .packages -}}
{{/* Special handling for kubeconfig */}}
  {{- if and .IsMain (ne .GroupName "") -}}
---
title: {{ .Title }}
content_type: tool-reference
package: {{ .DisplayName }}
auto_generated: true
description: Generated API reference documentation for {{ .DisplayName }}.
---
{{ .GetComment -}}
  {{- end -}}
{{- end }}

## Resource Types 

{{ range .packages -}}
  {{- range .VisibleTypes -}}
    {{- if .IsExported }}
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
    {{ $pkgTitle := .Title }}
    {{- range .VisibleTypes -}}
      {{- if .Referenced -}}
{{ template "type" . }}
      {{- end -}}
    {{- end }}
  {{- end }}
{{- end }}
{{- end }}

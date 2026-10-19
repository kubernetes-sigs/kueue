{{/*
Kubectl image used by Helm hooks and tests.
*/}}
{{- define "kueue-populator.kubectlImage" -}}
registry.k8s.io/kubectl:v1.33.6
{{- end -}}

{{/*
Match structured configuration's null removal when the Kueue subchart is disabled.
Lists are replacements and are kept unchanged, as in kueue.mergeManagerConfig.
*/}}
{{- define "kueue-populator.withoutNullKeys" -}}
{{- $config := deepCopy . -}}
{{- range $key, $value := $config -}}
{{- if eq $value nil -}}
{{- $_ := unset $config $key -}}
{{- else if kindIs "map" $value -}}
{{- $_ := set $config $key (include "kueue-populator.withoutNullKeys" $value | fromYaml) -}}
{{- end -}}
{{- end -}}
{{- toYaml $config -}}
{{- end -}}

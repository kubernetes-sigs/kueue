{{/*
Kubectl image used by Helm hooks and tests.
*/}}
{{- define "kueue-populator.kubectlImage" -}}
registry.k8s.io/kubectl:v1.33.6
{{- end -}}

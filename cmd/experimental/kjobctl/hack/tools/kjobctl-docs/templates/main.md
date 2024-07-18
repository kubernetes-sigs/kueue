{{ html `<!--
The file is auto-generated from the Go source code of the component using the
[generator](https://github.com/kubernetes-sigs/kueue/tree/main/cmd/experimental/kjobctl/hack/tools/kjobctl-docs).
-->`}}

# {{.Title}}


## Synopsis


{{.Description}}
{{- if .UseLine}}

```
{{.UseLine}}
```
{{- end}}
{{- if .Example}}


## Examples

```
{{.Example}}
```
{{- end}}
{{- if .Options}}


## Options

{{template "options" .Options}}
{{- end}}
{{- if .InheritedOptions}}


## Options inherited from parent commands

{{- template "options" .InheritedOptions}}
{{- end}}
{{- if .Links}}


## See Also
{{range .Links}}
* [{{.Name}}]({{.Path}}){{"\t"}} - {{.Description}}
{{- end}}
{{- end}}


/*
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
*/

package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"
)

type Metric struct {
	FullName  string
	Type      string
	Help      string
	Labels    []string
	Group     string
	LabelDocs map[string]string
}

var (
	inPackagePath string
	outFile       string
)

func main() {
	flag.StringVar(&inPackagePath, "metrics-package", "pkg/metrics", "path to the metrics package directory (preferred)")
	flag.StringVar(&outFile, "out", "site/content/en/docs/reference/metrics.md", "output markdown file to update in-place")
	flag.Parse()

	metrics, err := extractMetricsFromPackage(inPackagePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "extract: %v\n", err)
		os.Exit(1)
	}

	content, err := os.ReadFile(outFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read out: %v\n", err)
		os.Exit(1)
	}
	rendered := renderTables(metrics)

	out := string(content)
	// Replace only content between markers per group key
	for key, table := range rendered {
		pat := regexp.MustCompile(fmt.Sprintf(`(?s)<!--\s*BEGIN GENERATED TABLE:\s*%s\s*-->.*?<!--\s*END GENERATED TABLE:\s*%s\s*-->`, regexp.QuoteMeta(key), regexp.QuoteMeta(key)))
		block := fmt.Sprintf("<!-- BEGIN GENERATED TABLE: %s -->\n%s<!-- END GENERATED TABLE: %s -->", key, table, key)
		if pat.MatchString(out) {
			out = pat.ReplaceAllString(out, block)
		}
	}

	if err := os.WriteFile(outFile, []byte(out), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "write: %v\n", err)
		os.Exit(1)
	}
}

func extractMetricsFromPackage(dir string) ([]Metric, error) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, nil, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	var all []Metric
	var missingGroups []string
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			ast.Inspect(f, func(n ast.Node) bool {
				vs, ok := n.(*ast.ValueSpec)
				if !ok || len(vs.Values) != 1 {
					return true
				}
				groupFromComment, labelDocs := parseMarkers(vs)
				call, ok := vs.Values[0].(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkgIdent, ok := sel.X.(*ast.Ident)
				if !ok || pkgIdent.Name != "prometheus" {
					return true
				}
				fun := sel.Sel.Name
				if fun != "NewCounterVec" && fun != "NewGaugeVec" && fun != "NewHistogramVec" {
					return true
				}
				if len(call.Args) < 2 {
					return true
				}
				opts, ok := call.Args[0].(*ast.CompositeLit)
				if !ok {
					return true
				}
				var name, help string
				for _, elt := range opts.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					var keyName string
					switch k := kv.Key.(type) {
					case *ast.Ident:
						keyName = k.Name
					case *ast.SelectorExpr:
						keyName = k.Sel.Name
					default:
						keyName = exprToString(kv.Key)
					}
					switch keyName {
					case "Name":
						name = stringLiteral(kv.Value)
					case "Help":
						help = stringLiteral(kv.Value)
					}
				}
				labels := parseLabels(call.Args[1])
				m := Metric{
					FullName:  "kueue_" + name,
					Type:      map[string]string{"NewCounterVec": "Counter", "NewGaugeVec": "Gauge", "NewHistogramVec": "Histogram"}[fun],
					Help:      normalizeHelp(help),
					Labels:    labels,
					LabelDocs: labelDocs,
				}
				if groupFromComment == "" {
					varName := ""
					if len(vs.Names) > 0 && vs.Names[0] != nil {
						varName = vs.Names[0].Name
					}
					missingGroups = append(missingGroups, varName)
					return true
				}
				m.Group = groupFromComment
				all = append(all, m)
				return true
			})
		}
	}
	if len(missingGroups) > 0 {
		return nil, fmt.Errorf("missing metricsdoc:group marker on metrics: %s", strings.Join(missingGroups, ", "))
	}
	// stable sort within package groups is applied later in render
	return all, nil
}

func parseLabels(arg ast.Expr) []string {
	var labels []string
	cl, ok := arg.(*ast.CompositeLit)
	if !ok {
		return labels
	}
	for _, elt := range cl.Elts {
		labels = append(labels, stringLiteral(elt))
	}
	return labels
}

func stringLiteral(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.BasicLit:
		s := v.Value
		s = strings.Trim(s, "`")
		s = strings.Trim(s, "\"")
		return s
	case *ast.BinaryExpr:
		// Handle concatenation with +
		return stringLiteral(v.X) + stringLiteral(v.Y)
	default:
		return exprToString(e)
	}
}

func exprToString(e ast.Expr) string {
	var buf bytes.Buffer
	ast.Fprint(&buf, token.NewFileSet(), e, nil)
	return buf.String()
}

func renderTables(ms []Metric) map[string]string {
	by := map[string][]Metric{}
	for _, m := range ms {
		by[m.Group] = append(by[m.Group], m)
	}
	for k := range by {
		slices.SortFunc(by[k], func(a, b Metric) int {
			if a.FullName < b.FullName {
				return -1
			} else if a.FullName > b.FullName {
				return 1
			}
			return 0
		})
	}
	out := map[string]string{}
	for key, list := range by {
		var b strings.Builder
		writeHeader(&b)
		for _, m := range list {
			desc := sanitizeForTable(m.Help)
			labels := formatLabels(m.Labels, m.LabelDocs)
			fmt.Fprintf(&b, "| `%s` | %s | %s | %s |\n", m.FullName, m.Type, desc, labels)
		}
		out[key] = b.String()
	}
	return out
}

func writeHeader(b *strings.Builder) {
	b.WriteString("| Metric name | Type | Description | Labels |\n")
	b.WriteString("| --- | --- | --- | --- |\n")
}

func sanitizeForTable(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\n", "<br>")
	return s
}

func normalizeHelp(s string) string { return strings.TrimSpace(s) }

func formatLabels(labels []string, docs map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	var parts []string
	for _, l := range labels {
		if docs != nil {
			if desc, ok := docs[l]; ok && desc != "" {
				parts = append(parts, fmt.Sprintf("`%s`: %s", l, desc))
				continue
			}
		}
		parts = append(parts, fmt.Sprintf("`%s`", l))
	}
	return strings.Join(parts, "<br> ")
}

var (
	groupMarker = regexp.MustCompile(`(?i)\+\s*metricsdoc:group\s*=\s*([a-z_]+)`)
	labelMarker = regexp.MustCompile(`(?i)\+\s*metricsdoc:labels\s*=\s*([^\n\r]*)`)
)

func parseMarkers(vs *ast.ValueSpec) (string, map[string]string) {
	var group string
	labelDocs := map[string]string{}
	for _, text := range commentTexts(vs) {
		if group == "" {
			if m := groupMarker.FindStringSubmatch(text); len(m) == 2 {
				group = strings.ToLower(m[1])
			}
		}
		matches := labelMarker.FindAllStringSubmatch(text, -1)
		for _, match := range matches {
			if len(match) != 2 {
				continue
			}
			maps.Copy(labelDocs, parseLabelAnnotations(match[1]))
		}
	}
	if len(labelDocs) == 0 {
		return group, nil
	}
	return group, labelDocs
}

func commentTexts(vs *ast.ValueSpec) []string {
	var out []string
	if vs.Doc != nil {
		for _, c := range vs.Doc.List {
			if c != nil {
				out = append(out, c.Text)
			}
		}
	}
	if vs.Comment != nil {
		for _, c := range vs.Comment.List {
			if c != nil {
				out = append(out, c.Text)
			}
		}
	}
	return out
}

func parseLabelAnnotations(body string) map[string]string {
	body = strings.TrimSpace(body)
	body = strings.TrimSuffix(body, "*/")
	body = strings.TrimSpace(body)
	out := map[string]string{}
	for _, pair := range splitLabelPairs(body) {
		if pair == "" {
			continue
		}
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) != 2 {
			continue
		}
		key := stripQuotes(strings.TrimSpace(kv[0]))
		val := stripQuotes(strings.TrimSpace(kv[1]))
		if key == "" || val == "" {
			continue
		}
		out[key] = val
	}
	return out
}

func splitLabelPairs(body string) []string {
	var parts []string
	var current strings.Builder
	var quote rune
	for _, r := range body {
		switch r {
		case '"', '\'', '`':
			switch quote {
			case 0:
				quote = r
			case r:
				quote = 0
			}
			current.WriteRune(r)
		case ',':
			if quote != 0 {
				current.WriteRune(r)
				continue
			}
			part := strings.TrimSpace(current.String())
			if part != "" {
				parts = append(parts, part)
			}
			current.Reset()
		default:
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		part := strings.TrimSpace(current.String())
		if part != "" {
			parts = append(parts, part)
		}
	}
	return parts
}

func stripQuotes(s string) string {
	if len(s) < 2 {
		return s
	}
	first := s[0]
	last := s[len(s)-1]
	if (first == last) && (first == '"' || first == '\'' || first == '`') {
		return s[1 : len(s)-1]
	}
	return s
}

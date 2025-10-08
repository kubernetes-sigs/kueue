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
	"os"
	"regexp"
	"slices"
	"strings"
)

type Metric struct {
	FullName string
	Type     string
	Help     string
	Labels   []string
	Group    string
}

var (
	inFile  string
	outFile string
)

func main() {
	flag.StringVar(&inFile, "metrics-file", "pkg/metrics/metrics.go", "path to metrics.go")
	flag.StringVar(&outFile, "out", "site/content/en/docs/reference/metrics.md", "output markdown file to update in-place")
	flag.Parse()

	metrics, err := extractMetrics(inFile)
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

func extractMetrics(path string) ([]Metric, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	var result []Metric
	ast.Inspect(f, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok || len(vs.Values) != 1 {
			return true
		}
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
			FullName: "kueue_" + name,
			Type:     map[string]string{"NewCounterVec": "Counter", "NewGaugeVec": "Gauge", "NewHistogramVec": "Histogram"}[fun],
			Help:     normalizeHelp(help),
			Labels:   labels,
		}
		m.Group = groupFor(m)
		result = append(result, m)
		return true
	})
	return result, nil
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

func groupFor(m Metric) string {
	name := strings.TrimPrefix(m.FullName, "kueue_")
	lbls := toSet(m.Labels)

	if strings.HasPrefix(name, "cluster_queue_resource_") ||
		name == "cluster_queue_nominal_quota" ||
		name == "cluster_queue_borrowing_limit" ||
		name == "cluster_queue_lending_limit" ||
		name == "cluster_queue_weighted_share" {
		return "optional_clusterqueue_resources"
	}
	if name == "ready_wait_time_seconds" ||
		name == "admitted_until_ready_wait_time_seconds" ||
		name == "local_queue_ready_wait_time_seconds" ||
		name == "local_queue_admitted_until_ready_wait_time_seconds" {
		return "optional_wait_for_pods_ready"
	}
	if lbls["result"] {
		return "health"
	}
	if lbls["name"] && lbls["namespace"] {
		return "localqueue"
	}
	if lbls["cohort"] {
		return "cohort"
	}
	return "clusterqueue"
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
			labels := formatLabels(m.Labels)
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
func formatLabels(labels []string) string {
	if len(labels) == 0 {
		return ""
	}
	var parts []string
	for _, l := range labels {
		if d := labelDoc(l); d != "" {
			parts = append(parts, fmt.Sprintf("`%s`: %s", l, d))
		} else {
			parts = append(parts, fmt.Sprintf("`%s`", l))
		}
	}
	return strings.Join(parts, "<br> ")
}
func labelDoc(l string) string {
	switch l {
	case "result":
		return "possible values are `success` or `inadmissible`"
	case "cluster_queue":
		return "the name of the ClusterQueue"
	case "status":
		return "status label (varies by metric)"
	case "priority_class":
		return "the priority class name"
	case "reason":
		return "eviction or preemption reason"
	case "underlying_cause":
		return "root cause for eviction"
	case "detailed_reason":
		return "finer-grained eviction cause"
	case "preempting_cluster_queue":
		return "the ClusterQueue executing preemption"
	case "name":
		return "the name of the LocalQueue"
	case "namespace":
		return "the namespace of the LocalQueue"
	case "active":
		return "one of `True`, `False`, or `Unknown`"
	case "flavor":
		return "the resource flavor name"
	case "resource":
		return "the resource name"
	case "cohort":
		return "the name of the Cohort"
	default:
		return ""
	}
}
func toSet(ss []string) map[string]bool {
	m := map[string]bool{}
	for _, s := range ss {
		m[s] = true
	}
	return m
}

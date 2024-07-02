/*
Copyright 2024 The Kubernetes Authors.

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
package describe

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/cli-runtime/pkg/printers"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/apis/v1alpha1"
)

// Each level has 2 spaces for PrefixWriter
const (
	IndentLevelZero = iota
	IndentLevelOne
	IndentLevelTwo
)

var (
	maxAnnotationLen = 140
	// globally skipped annotations
	skipAnnotations = sets.NewString(corev1.LastAppliedConfigAnnotation)
)

// PrefixWriter can write text at various indentation levels.
type PrefixWriter interface {
	// Write writes text with the specified indentation level.
	Write(level int, format string, a ...interface{})
	// WriteLine writes an entire line with no indentation level.
	WriteLine(a ...interface{})
	// Flush forces indentation to be reset.
	Flush()
}

// prefixWriter implements PrefixWriter
type prefixWriter struct {
	out io.Writer
}

var _ PrefixWriter = &prefixWriter{}

// NewPrefixWriter creates a new PrefixWriter.
func NewPrefixWriter(out io.Writer) PrefixWriter {
	return &prefixWriter{out: out}
}

type flusher interface {
	Flush()
}

func tabbedString(f func(io.Writer) error) (string, error) {
	out := new(tabwriter.Writer)
	buf := &bytes.Buffer{}
	out.Init(buf, 0, 8, 2, ' ', 0)

	err := f(out)
	if err != nil {
		return "", err
	}

	out.Flush()
	return buf.String(), nil
}

func (pw *prefixWriter) Write(level int, format string, a ...interface{}) {
	levelSpace := "  "
	prefix := ""
	for i := 0; i < level; i++ {
		prefix += levelSpace
	}
	output := fmt.Sprintf(prefix+format, a...)
	// nolint:errcheck
	printers.WriteEscaped(pw.out, output)
}

func (pw *prefixWriter) WriteLine(a ...interface{}) {
	output := fmt.Sprintln(a...)
	// nolint:errcheck
	printers.WriteEscaped(pw.out, output)
}

func (pw *prefixWriter) Flush() {
	if f, ok := pw.out.(flusher); ok {
		f.Flush()
	}
}

// printLabelsMultiline prints multiple labels with a proper alignment.
func printLabelsMultiline(w PrefixWriter, title string, labels map[string]string) {
	printLabelsMultilineWithIndent(w, "", title, "\t", labels, sets.New[string]())
}

// printLabelsMultiline prints multiple labels with a user-defined alignment.
func printLabelsMultilineWithIndent(w PrefixWriter, initialIndent, title, innerIndent string, labels map[string]string, skip sets.Set[string]) {
	w.Write(IndentLevelZero, "%s%s:%s", initialIndent, title, innerIndent)

	if len(labels) == 0 {
		w.WriteLine("<none>")
		return
	}

	// to print labels in the sorted order
	keys := make([]string, 0, len(labels))
	for key := range labels {
		if skip.Has(key) {
			continue
		}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		w.WriteLine("<none>")
		return
	}
	sort.Strings(keys)

	for i, key := range keys {
		if i != 0 {
			w.Write(IndentLevelZero, "%s", initialIndent)
			w.Write(IndentLevelZero, "%s", innerIndent)
		}
		w.Write(IndentLevelZero, "%s=%s\n", key, labels[key])
	}
}

// printAnnotationsMultiline prints multiple annotations with a proper alignment.
// If annotation string is too long, we omit chars more than 200 length.
func printAnnotationsMultiline(w PrefixWriter, title string, annotations map[string]string) {
	w.Write(IndentLevelZero, "%s:\t", title)

	// to print labels in the sorted order
	keys := make([]string, 0, len(annotations))
	for key := range annotations {
		if skipAnnotations.Has(key) {
			continue
		}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		w.WriteLine("<none>")
		return
	}
	sort.Strings(keys)
	indent := "\t"
	for i, key := range keys {
		if i != 0 {
			w.Write(IndentLevelZero, indent)
		}
		value := strings.TrimSuffix(annotations[key], "\n")
		if (len(value)+len(key)+2) > maxAnnotationLen || strings.Contains(value, "\n") {
			w.Write(IndentLevelZero, "%s:\n", key)
			for _, s := range strings.Split(value, "\n") {
				w.Write(IndentLevelZero, "%s  %s\n", indent, shorten(s, maxAnnotationLen-2))
			}
		} else {
			w.Write(IndentLevelZero, "%s: %s\n", key, value)
		}
	}
}

func printController(controllee metav1.Object) string {
	if controllerRef := metav1.GetControllerOf(controllee); controllerRef != nil {
		return fmt.Sprintf("%s/%s", controllerRef.Kind, controllerRef.Name)
	}
	return ""
}

func shorten(s string, maxLength int) string {
	if len(s) > maxLength {
		return s[:maxLength] + "..."
	}
	return s
}

func parseAppProfileModeName(s string) ([]string, error) {
	seg := strings.Split(s, "/")
	if len(seg) != 2 {
		if len(seg) > 2 {
			return nil, errors.New("arguments in mode/name form may not have more than one slash")
		}
		return nil, errors.New("argument must be in mode/name form")
	}

	return seg, nil
}

func resourceFor(mode string) string {
	if strings.EqualFold(mode, string(v1alpha1.InteractiveMode)) {
		return "pod"
	}

	return strings.ToLower(mode)
}

func gvkFor(mapper meta.RESTMapper, resource string) (schema.GroupVersionKind, error) {
	var gvk schema.GroupVersionKind
	var err error

	fullySpecifiedGVR, groupResource := schema.ParseResourceArg(strings.ToLower(resource))
	gvr := schema.GroupVersionResource{}
	if fullySpecifiedGVR != nil {
		gvr, _ = mapper.ResourceFor(*fullySpecifiedGVR)
	}
	if gvr.Empty() {
		gvr, err = mapper.ResourceFor(groupResource.WithVersion(""))
		if err != nil {
			return gvk, err
		}
	}

	gvk, err = mapper.KindFor(gvr)
	if err != nil {
		return gvk, err
	}

	return gvk, err
}

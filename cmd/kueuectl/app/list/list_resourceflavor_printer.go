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

package list

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/cli-runtime/pkg/printers"
	"k8s.io/utils/clock"

	"sigs.k8s.io/kueue/apis/kueue/v1beta1"
)

type listResourceFlavorPrinter struct {
	clock        clock.Clock
	printOptions printers.PrintOptions
}

var _ printers.ResourcePrinter = (*listResourceFlavorPrinter)(nil)

func (p *listResourceFlavorPrinter) PrintObj(obj runtime.Object, out io.Writer) error {
	printer := printers.NewTablePrinter(p.printOptions)

	list, ok := obj.(*v1beta1.ResourceFlavorList)
	if !ok {
		return errors.New("invalid object type")
	}

	table := &metav1.Table{
		ColumnDefinitions: []metav1.TableColumnDefinition{
			{Name: "Name", Type: "string", Format: "name"},
			{Name: "Node Labels", Type: "string"},
			{Name: "Age", Type: "string"},
		},
		Rows: p.printResourceFlavorList(list),
	}

	return printer.PrintObj(table, out)
}

func (p *listResourceFlavorPrinter) WithHeaders(f bool) *listResourceFlavorPrinter {
	p.printOptions.NoHeaders = !f
	return p
}

func (p *listResourceFlavorPrinter) WithClock(c clock.Clock) *listResourceFlavorPrinter {
	p.clock = c
	return p
}

func newResourceFlavorTablePrinter() *listResourceFlavorPrinter {
	return &listResourceFlavorPrinter{
		clock: clock.RealClock{},
	}
}

func (p *listResourceFlavorPrinter) printResourceFlavorList(list *v1beta1.ResourceFlavorList) []metav1.TableRow {
	rows := make([]metav1.TableRow, len(list.Items))
	for index := range list.Items {
		rows[index] = p.printResourceFlavor(&list.Items[index])
	}
	return rows
}

func (p *listResourceFlavorPrinter) printResourceFlavor(resourceFlavor *v1beta1.ResourceFlavor) metav1.TableRow {
	row := metav1.TableRow{Object: runtime.RawExtension{Object: resourceFlavor}}
	nodeLabels := make([]string, 0, len(resourceFlavor.Spec.NodeLabels))
	for key, value := range resourceFlavor.Spec.NodeLabels {
		nodeLabels = append(nodeLabels, fmt.Sprintf("%s=%s", key, value))
	}
	slices.Sort(nodeLabels)
	row.Cells = []any{
		resourceFlavor.Name,
		strings.Join(nodeLabels, ", "),
		duration.HumanDuration(p.clock.Since(resourceFlavor.CreationTimestamp.Time)),
	}
	return row
}

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
	"io"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/cli-runtime/pkg/printers"
)

type listPodPrinter struct {
	printOptions printers.PrintOptions
}

var _ printers.ResourcePrinter = (*listPodPrinter)(nil)

func (p *listPodPrinter) PrintObj(obj runtime.Object, out io.Writer) error {
	printer := printers.NewTablePrinter(p.printOptions)

	list, ok := obj.(*corev1.PodList)
	if !ok {
		return errors.New("invalid object type")
	}

	table := &metav1.Table{
		ColumnDefinitions: []metav1.TableColumnDefinition{
			{Name: "Name", Type: "string", Format: "name"},
			{Name: "Status", Type: "string", Format: "status"},
			{Name: "Age", Type: "string"},
		},
		Rows: printPodList(list),
	}

	return printer.PrintObj(table, out)
}

func (p *listPodPrinter) WithNamespace() *listPodPrinter {
	p.printOptions.WithNamespace = true
	return p
}

func newPodTablePrinter() *listPodPrinter {
	return &listPodPrinter{}
}

func printPodList(list *corev1.PodList) []metav1.TableRow {
	rows := make([]metav1.TableRow, len(list.Items))
	for index := range list.Items {
		rows[index] = printPod(&list.Items[index])
	}
	return rows
}

func printPod(pod *corev1.Pod) metav1.TableRow {
	row := metav1.TableRow{
		Object: runtime.RawExtension{Object: pod},
	}
	row.Cells = []any{
		pod.Name,
		pod.Status.Phase,
		duration.HumanDuration(time.Since(pod.CreationTimestamp.Time)),
	}
	return row
}

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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/cli-runtime/pkg/printers"
	"k8s.io/utils/clock"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/constants"
)

type listInteractivePrinter struct {
	clock        clock.Clock
	printOptions printers.PrintOptions
}

var _ printers.ResourcePrinter = (*listInteractivePrinter)(nil)

func (p *listInteractivePrinter) PrintObj(obj runtime.Object, out io.Writer) error {
	printer := printers.NewTablePrinter(p.printOptions)

	list, ok := obj.(*corev1.PodList)
	if !ok {
		return errors.New("invalid object type")
	}

	table := &metav1.Table{
		ColumnDefinitions: []metav1.TableColumnDefinition{
			{Name: "Name", Type: "string", Format: "name"},
			{Name: "Profile", Type: "string"},
			{Name: "Status", Type: "string"},
			{Name: "Age", Type: "string"},
		},
		Rows: p.printPodList(list),
	}

	return printer.PrintObj(table, out)
}

func (p *listInteractivePrinter) WithNamespace(f bool) *listInteractivePrinter {
	p.printOptions.WithNamespace = f
	return p
}

func (p *listInteractivePrinter) WithHeaders(f bool) *listInteractivePrinter {
	p.printOptions.NoHeaders = !f
	return p
}

func (p *listInteractivePrinter) WithClock(c clock.Clock) *listInteractivePrinter {
	p.clock = c
	return p
}

func newInteractiveTablePrinter() *listInteractivePrinter {
	return &listInteractivePrinter{
		clock: clock.RealClock{},
	}
}

func (p *listInteractivePrinter) printPodList(list *corev1.PodList) []metav1.TableRow {
	rows := make([]metav1.TableRow, len(list.Items))
	for index := range list.Items {
		rows[index] = p.printPod(&list.Items[index])
	}
	return rows
}

func (p *listInteractivePrinter) printPod(pod *corev1.Pod) metav1.TableRow {
	row := metav1.TableRow{
		Object: runtime.RawExtension{Object: pod},
	}

	row.Cells = []any{
		pod.Name,
		pod.ObjectMeta.Labels[constants.ProfileLabel],
		pod.Status.Phase,
		duration.HumanDuration(p.clock.Since(pod.CreationTimestamp.Time)),
	}
	return row
}

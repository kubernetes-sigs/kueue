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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/cli-runtime/pkg/printers"

	"sigs.k8s.io/kueue/apis/kueue/v1beta1"
)

type listLocalQueuePrinter struct {
	printOptions printers.PrintOptions
}

var _ printers.ResourcePrinter = (*listLocalQueuePrinter)(nil)

func (p *listLocalQueuePrinter) PrintObj(obj runtime.Object, out io.Writer) error {
	printer := printers.NewTablePrinter(p.printOptions)

	list, ok := obj.(*v1beta1.LocalQueueList)
	if !ok {
		return errors.New("invalid object type")
	}

	table := &metav1.Table{
		ColumnDefinitions: []metav1.TableColumnDefinition{
			{Name: "Name", Type: "string", Format: "name"},
			{Name: "ClusterQueue", Type: "string"},
			{Name: "Pending Workloads", Type: "integer"},
			{Name: "Admitted Workloads", Type: "integer"},
			{Name: "Age", Type: "string"},
		},
		Rows: printLocalQueueList(list),
	}

	return printer.PrintObj(table, out)
}

func (p *listLocalQueuePrinter) WithNamespace() *listLocalQueuePrinter {
	p.printOptions.WithNamespace = true
	return p
}

func newLocalQueueTablePrinter() *listLocalQueuePrinter {
	return &listLocalQueuePrinter{}
}

func printLocalQueueList(list *v1beta1.LocalQueueList) []metav1.TableRow {
	rows := make([]metav1.TableRow, len(list.Items))
	for index := range list.Items {
		rows[index] = printLocalQueue(&list.Items[index])
	}
	return rows
}

func printLocalQueue(localQueue *v1beta1.LocalQueue) metav1.TableRow {
	row := metav1.TableRow{
		Object: runtime.RawExtension{Object: localQueue},
	}
	row.Cells = []any{
		localQueue.Name,
		localQueue.Spec.ClusterQueue,
		localQueue.Status.PendingWorkloads,
		localQueue.Status.AdmittedWorkloads,
		duration.HumanDuration(time.Since(localQueue.CreationTimestamp.Time)),
	}
	return row
}

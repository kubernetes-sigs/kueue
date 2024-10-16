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

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/cli-runtime/pkg/printers"
	"k8s.io/utils/clock"
	kueueconstants "sigs.k8s.io/kueue/pkg/controller/constants"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/constants"
)

type listRayClusterPrinter struct {
	clock        clock.Clock
	printOptions printers.PrintOptions
}

var _ printers.ResourcePrinter = (*listRayClusterPrinter)(nil)

func (p *listRayClusterPrinter) PrintObj(obj runtime.Object, out io.Writer) error {
	printer := printers.NewTablePrinter(p.printOptions)

	list, ok := obj.(*rayv1.RayClusterList)
	if !ok {
		return errors.New("invalid object type")
	}

	table := &metav1.Table{
		ColumnDefinitions: []metav1.TableColumnDefinition{
			{Name: "Name", Type: "string", Format: "name"},
			{Name: "Profile", Type: "string"},
			{Name: "Local Queue", Type: "string"},
			{Name: "Desired Workers", Type: "string"},
			{Name: "Available Workers", Type: "string"},
			{Name: "CPUs", Type: "string"},
			{Name: "Memory", Type: "string"},
			{Name: "GPUs", Type: "string"},
			{Name: "Status", Type: "string"},
			{Name: "Age", Type: "string"},
		},
		Rows: p.printRayClusterList(list),
	}

	return printer.PrintObj(table, out)
}

func (p *listRayClusterPrinter) WithNamespace(f bool) *listRayClusterPrinter {
	p.printOptions.WithNamespace = f
	return p
}

func (p *listRayClusterPrinter) WithHeaders(f bool) *listRayClusterPrinter {
	p.printOptions.NoHeaders = !f
	return p
}

func (p *listRayClusterPrinter) WithClock(c clock.Clock) *listRayClusterPrinter {
	p.clock = c
	return p
}

func newRayClusterTablePrinter() *listRayClusterPrinter {
	return &listRayClusterPrinter{
		clock: clock.RealClock{},
	}
}

func (p *listRayClusterPrinter) printRayClusterList(list *rayv1.RayClusterList) []metav1.TableRow {
	rows := make([]metav1.TableRow, len(list.Items))
	for index := range list.Items {
		rows[index] = p.printRayCluster(&list.Items[index])
	}
	return rows
}

func (p *listRayClusterPrinter) printRayCluster(rayCluster *rayv1.RayCluster) metav1.TableRow {
	row := metav1.TableRow{
		Object: runtime.RawExtension{Object: rayCluster},
	}
	row.Cells = []any{
		rayCluster.Name,
		rayCluster.ObjectMeta.Labels[constants.ProfileLabel],
		rayCluster.ObjectMeta.Labels[kueueconstants.QueueLabel],
		rayCluster.Status.DesiredWorkerReplicas,
		rayCluster.Status.AvailableWorkerReplicas,
		rayCluster.Status.DesiredCPU.String(),
		rayCluster.Status.DesiredMemory.String(),
		rayCluster.Status.DesiredGPU.String(),
		rayCluster.Status.State,
		duration.HumanDuration(p.clock.Since(rayCluster.CreationTimestamp.Time)),
	}
	return row
}

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
	"strings"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/cli-runtime/pkg/printers"
	"k8s.io/utils/clock"

	"sigs.k8s.io/kueue/apis/kueue/v1beta1"
	visibility "sigs.k8s.io/kueue/apis/visibility/v1beta1"
	"sigs.k8s.io/kueue/pkg/workload"
)

const crdTypeMaxLength = 20

type listWorkloadResources struct {
	localQueues      map[string]*v1beta1.LocalQueue
	pendingWorkloads map[string]*visibility.PendingWorkload
	apiResourceLists map[string]*metav1.APIResourceList
}

func newListWorkloadResources() *listWorkloadResources {
	return &listWorkloadResources{
		localQueues:      make(map[string]*v1beta1.LocalQueue),
		pendingWorkloads: make(map[string]*visibility.PendingWorkload),
		apiResourceLists: make(map[string]*metav1.APIResourceList),
	}
}

type listWorkloadPrinter struct {
	clock        clock.Clock
	printOptions printers.PrintOptions
	resources    *listWorkloadResources
}

var _ printers.ResourcePrinter = (*listWorkloadPrinter)(nil)

func (p *listWorkloadPrinter) PrintObj(obj runtime.Object, out io.Writer) error {
	printer := printers.NewTablePrinter(p.printOptions)

	list, ok := obj.(*v1beta1.WorkloadList)
	if !ok {
		return errors.New("invalid object type")
	}

	table := &metav1.Table{
		ColumnDefinitions: []metav1.TableColumnDefinition{
			{Name: "Name", Type: "string", Format: "name"},
			{Name: "Job Type", Type: "string"},
			{Name: "Job Name", Type: "string"},
			{Name: "LocalQueue", Type: "string"},
			{Name: "ClusterQueue", Type: "string"},
			{Name: "Status", Type: "string"},
			{Name: "Position in Queue", Type: "string"},
			{Name: "Exec Time", Type: "string"},
			{Name: "Age", Type: "string"},
		},
		Rows: p.printWorkloadList(list),
	}

	return printer.PrintObj(table, out)
}

func (p *listWorkloadPrinter) WithNamespace(f bool) *listWorkloadPrinter {
	p.printOptions.WithNamespace = f
	return p
}

func (p *listWorkloadPrinter) WithHeaders(f bool) *listWorkloadPrinter {
	p.printOptions.NoHeaders = !f
	return p
}

func (p *listWorkloadPrinter) WithResources(r *listWorkloadResources) *listWorkloadPrinter {
	if r == nil {
		r = newListWorkloadResources()
	}
	p.resources = r
	return p
}

func (p *listWorkloadPrinter) WithClock(c clock.Clock) *listWorkloadPrinter {
	p.clock = c
	return p
}

func newWorkloadTablePrinter() *listWorkloadPrinter {
	return &listWorkloadPrinter{
		clock:     clock.RealClock{},
		resources: newListWorkloadResources(),
	}
}

func (p *listWorkloadPrinter) printWorkloadList(list *v1beta1.WorkloadList) []metav1.TableRow {
	rows := make([]metav1.TableRow, len(list.Items))
	for index := range list.Items {
		rows[index] = p.printWorkload(&list.Items[index])
	}
	return rows
}

func (p *listWorkloadPrinter) printWorkload(wl *v1beta1.Workload) metav1.TableRow {
	row := metav1.TableRow{
		Object: runtime.RawExtension{Object: wl},
	}

	var clusterQueueName string
	if wl.Status.Admission != nil && len(wl.Status.Admission.ClusterQueue) > 0 {
		clusterQueueName = string(wl.Status.Admission.ClusterQueue)
	} else if lq := p.resources.localQueues[localQueueKeyForWorkload(wl)]; lq != nil {
		clusterQueueName = string(lq.Spec.ClusterQueue)
	}

	var positionInQueue string
	if pendingWorkload, ok := p.resources.pendingWorkloads[workload.Key(wl)]; ok {
		positionInQueue = fmt.Sprintf("%d", pendingWorkload.PositionInLocalQueue)
	}

	var execTime string
	if admittedCond := apimeta.FindStatusCondition(wl.Status.Conditions, v1beta1.WorkloadAdmitted); admittedCond != nil &&
		admittedCond.Status == metav1.ConditionTrue {
		finishedTime := p.clock.Now()

		if finishedCond := apimeta.FindStatusCondition(wl.Status.Conditions, v1beta1.WorkloadFinished); finishedCond != nil &&
			finishedCond.Status == metav1.ConditionTrue {
			finishedTime = finishedCond.LastTransitionTime.Time
		}

		execTime = duration.HumanDuration(finishedTime.Sub(admittedCond.LastTransitionTime.Time))
	}

	row.Cells = []any{
		wl.Name,
		strings.Join(p.crdTypes(wl), ", "),
		strings.Join(p.crdNames(wl), ", "),
		wl.Spec.QueueName,
		clusterQueueName,
		strings.ToUpper(workload.Status(wl)),
		positionInQueue,
		execTime,
		duration.HumanDuration(p.clock.Since(wl.CreationTimestamp.Time)),
	}

	return row
}

func (p *listWorkloadPrinter) crdTypes(wl *v1beta1.Workload) []string {
	crdTypes := sets.New[string]()

	for _, ref := range wl.ObjectMeta.OwnerReferences {
		var apiResource *metav1.APIResource

		if resourceList := p.resources.apiResourceLists[ref.APIVersion]; resourceList != nil {
			for _, r := range resourceList.APIResources {
				if r.Kind == ref.Kind && len(r.SingularName) > 0 {
					apiResource = &r
					break
				}
			}
		}

		if apiResource != nil {
			crdTypes.Insert(p.apiResourceType(apiResource))
		}
	}

	return crdTypes.UnsortedList()
}

func (p *listWorkloadPrinter) crdNames(wl *v1beta1.Workload) []string {
	crdNames := sets.New[string]()

	for _, ref := range wl.ObjectMeta.OwnerReferences {
		crdNames.Insert(ref.Name)
	}

	return crdNames.UnsortedList()
}

func (p *listWorkloadPrinter) apiResourceType(resource *metav1.APIResource) string {
	crdTypeParts := make([]string, 0, 2)

	if len(resource.SingularName) > 0 {
		crdTypeParts = append(crdTypeParts, resource.SingularName)
	}

	if len(resource.Group) > 0 {
		crdTypeParts = append(crdTypeParts, resource.Group)
	}

	crdType := strings.Join(crdTypeParts, ".")
	if len(crdType) > crdTypeMaxLength {
		crdType = fmt.Sprintf("%s...", crdType[:crdTypeMaxLength])
	}

	return crdType
}

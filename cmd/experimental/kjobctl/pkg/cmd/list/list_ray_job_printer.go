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

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/cli-runtime/pkg/printers"
	"k8s.io/utils/clock"
	kueueconstants "sigs.k8s.io/kueue/pkg/controller/constants"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/constants"
)

type listRayJobPrinter struct {
	clock        clock.Clock
	printOptions printers.PrintOptions
}

var _ printers.ResourcePrinter = (*listRayJobPrinter)(nil)

func (p *listRayJobPrinter) PrintObj(obj runtime.Object, out io.Writer) error {
	printer := printers.NewTablePrinter(p.printOptions)

	list, ok := obj.(*rayv1.RayJobList)
	if !ok {
		return errors.New("invalid object type")
	}

	table := &metav1.Table{
		ColumnDefinitions: []metav1.TableColumnDefinition{
			{Name: "Name", Type: "string", Format: "name"},
			{Name: "Profile", Type: "string"},
			{Name: "Local Queue", Type: "string"},
			{Name: "Ray Cluster Name", Type: "string"},
			{Name: "Job Status", Type: "string"},
			{Name: "Deployment Status", Type: "string"},
			{Name: "Start Time", Type: "dateTime"},
			{Name: "End Time", Type: "string"},
			{Name: "Age", Type: "string"},
		},
		Rows: p.printRayJobList(list),
	}

	return printer.PrintObj(table, out)
}

func (p *listRayJobPrinter) WithNamespace(f bool) *listRayJobPrinter {
	p.printOptions.WithNamespace = f
	return p
}

func (p *listRayJobPrinter) WithHeaders(f bool) *listRayJobPrinter {
	p.printOptions.NoHeaders = !f
	return p
}

func (p *listRayJobPrinter) WithClock(c clock.Clock) *listRayJobPrinter {
	p.clock = c
	return p
}

func newRayJobTablePrinter() *listRayJobPrinter {
	return &listRayJobPrinter{
		clock: clock.RealClock{},
	}
}

func (p *listRayJobPrinter) printRayJobList(list *rayv1.RayJobList) []metav1.TableRow {
	rows := make([]metav1.TableRow, len(list.Items))
	for index := range list.Items {
		rows[index] = p.printRayJob(&list.Items[index])
	}
	return rows
}

func (p *listRayJobPrinter) printRayJob(rayJob *rayv1.RayJob) metav1.TableRow {
	row := metav1.TableRow{
		Object: runtime.RawExtension{Object: rayJob},
	}
	var startTime, endTime string
	if rayJob.Status.StartTime != nil {
		startTime = rayJob.Status.StartTime.Format(time.DateTime)
	}
	if rayJob.Status.EndTime != nil {
		endTime = rayJob.Status.EndTime.Format(time.DateTime)
	}
	row.Cells = []any{
		rayJob.Name,
		rayJob.ObjectMeta.Labels[constants.ProfileLabel],
		rayJob.ObjectMeta.Labels[kueueconstants.QueueLabel],
		rayJob.Status.RayClusterName,
		rayJob.Status.JobStatus,
		rayJob.Status.JobDeploymentStatus,
		startTime,
		endTime,
		duration.HumanDuration(p.clock.Since(rayJob.CreationTimestamp.Time)),
	}
	return row
}

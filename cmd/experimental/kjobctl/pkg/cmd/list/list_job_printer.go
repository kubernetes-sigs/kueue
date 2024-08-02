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
	"time"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/cli-runtime/pkg/printers"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/constants"
	kueueconstants "sigs.k8s.io/kueue/pkg/controller/constants"
)

type listJobPrinter struct {
	clock        clock.Clock
	printOptions printers.PrintOptions
}

var _ printers.ResourcePrinter = (*listJobPrinter)(nil)

func (p *listJobPrinter) PrintObj(obj runtime.Object, out io.Writer) error {
	printer := printers.NewTablePrinter(p.printOptions)

	list, ok := obj.(*batchv1.JobList)
	if !ok {
		return errors.New("invalid object type")
	}

	table := &metav1.Table{
		ColumnDefinitions: []metav1.TableColumnDefinition{
			{Name: "Name", Type: "string", Format: "name"},
			{Name: "Profile", Type: "string"},
			{Name: "Local Queue", Type: "string"},
			{Name: "Completions", Type: "string"},
			{Name: "Duration", Type: "string"},
			{Name: "Age", Type: "string"},
		},
		Rows: p.printJobList(list),
	}

	return printer.PrintObj(table, out)
}

func (p *listJobPrinter) WithNamespace(f bool) *listJobPrinter {
	p.printOptions.WithNamespace = f
	return p
}

func (p *listJobPrinter) WithHeaders(f bool) *listJobPrinter {
	p.printOptions.NoHeaders = !f
	return p
}

func (p *listJobPrinter) WithClock(c clock.Clock) *listJobPrinter {
	p.clock = c
	return p
}

func newJobTablePrinter() *listJobPrinter {
	return &listJobPrinter{
		clock: clock.RealClock{},
	}
}

func (p *listJobPrinter) printJobList(list *batchv1.JobList) []metav1.TableRow {
	rows := make([]metav1.TableRow, len(list.Items))
	for index := range list.Items {
		rows[index] = p.printJob(&list.Items[index])
	}
	return rows
}

func (p *listJobPrinter) printJob(job *batchv1.Job) metav1.TableRow {
	row := metav1.TableRow{
		Object: runtime.RawExtension{Object: job},
	}
	var durationStr string
	if job.Status.StartTime != nil {
		completionTime := time.Now()
		if job.Status.CompletionTime != nil {
			completionTime = job.Status.CompletionTime.Time
		}
		durationStr = duration.HumanDuration(completionTime.Sub(job.Status.StartTime.Time))
	}
	row.Cells = []any{
		job.Name,
		job.ObjectMeta.Labels[constants.ProfileLabel],
		job.ObjectMeta.Labels[kueueconstants.QueueLabel],
		fmt.Sprintf("%d/%d", job.Status.Succeeded, ptr.Deref(job.Spec.Completions, 1)),
		durationStr,
		duration.HumanDuration(p.clock.Since(job.CreationTimestamp.Time)),
	}
	return row
}

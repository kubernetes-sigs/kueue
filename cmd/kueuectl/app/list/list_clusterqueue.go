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
	"context"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/cli-runtime/pkg/printers"

	"sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/client-go/clientset/versioned"
	"sigs.k8s.io/kueue/client-go/clientset/versioned/scheme"
	kueuev1beta1 "sigs.k8s.io/kueue/client-go/clientset/versioned/typed/kueue/v1beta1"
	"sigs.k8s.io/kueue/cmd/kueuectl/app/util"
)

const (
	cqShort   = `List ClusterQueues`
	cqLong    = `Lists all ClusterQueues, potentially limiting output to those that are active/inactive and matching the label selector.`
	cqExample = `  # List ClusterQueue
  kueuectl list clusterqueue`
)

type ClusterQueueOptions struct {
	// PrintFlags holds options necessary for obtaining a printer
	PrintFlags *genericclioptions.PrintFlags

	LabelSelector string
	FieldSelector string

	// Kueuectl flags
	// Active is an optional flag to filter true/false (active/inactive) cluster queues
	// Active means the cluster queue has kueue.ClusterQueueActive condition with status=metav1.ConditionTrue
	Active []bool

	Client                       kueuev1beta1.KueueV1beta1Interface
	clusterQueueListRequestLimit int64

	PrintObj printers.ResourcePrinterFunc

	genericiooptions.IOStreams
}

func NewClusterQueueOptions(streams genericiooptions.IOStreams) *ClusterQueueOptions {
	return &ClusterQueueOptions{
		PrintFlags:                   genericclioptions.NewPrintFlags("").WithTypeSetter(scheme.Scheme),
		Active:                       make([]bool, 0),
		clusterQueueListRequestLimit: 300,
		IOStreams:                    streams,
	}
}

func NewClusterQueueCmd(clientGetter util.ClientGetter, streams genericiooptions.IOStreams) *cobra.Command {
	o := NewClusterQueueOptions(streams)

	cmd := &cobra.Command{
		Use:                   "clusterqueue [--selector key1=value1] [--field-selector key1=value1] [--active=true|false]",
		DisableFlagsInUseLine: true,
		Aliases:               []string{"cq"},
		Short:                 cqShort,
		Long:                  cqLong,
		Example:               cqExample,
		Run: func(cmd *cobra.Command, args []string) {
			cobra.CheckErr(o.Complete(clientGetter, cmd, args))
			cobra.CheckErr(o.Validate())
			cobra.CheckErr(o.Run(cmd.Context()))
		},
	}

	o.PrintFlags.AddFlags(cmd)

	addFieldSelectorFlagVar(cmd, &o.FieldSelector)
	addLabelSelectorFlagVar(cmd, &o.LabelSelector)
	addActiveFilterFlagVar(cmd, &o.Active)

	return cmd
}

// Complete takes the command arguments and infers any remaining options.
func (o *ClusterQueueOptions) Complete(clientGetter util.ClientGetter, cmd *cobra.Command, args []string) error {
	var err error

	config, err := clientGetter.ToRESTConfig()
	if err != nil {
		return err
	}

	clientset, err := versioned.NewForConfig(config)
	if err != nil {
		return err
	}

	o.Client = clientset.KueueV1beta1()

	if !o.PrintFlags.OutputFlagSpecified() {
		o.PrintObj = printClusterQueueTable
	} else {
		printer, err := o.PrintFlags.ToPrinter()
		if err != nil {
			return err
		}
		o.PrintObj = printer.PrintObj
	}

	if len(args) > 0 {
		activeFlag, err := cmd.Flags().GetBoolSlice("active")
		if err != nil {
			return err
		}
		o.Active = activeFlag
	}

	return nil
}

func (o *ClusterQueueOptions) Validate() error {
	if !o.validActiveFlagOptionProvided() {
		return fmt.Errorf("only one active flag can be provided")
	}
	return nil
}

func (o *ClusterQueueOptions) validActiveFlagOptionProvided() bool {
	return len(o.Active) == 0 || len(o.Active) == 1
}

// Run prints the cluster queues.
func (o *ClusterQueueOptions) Run(ctx context.Context) error {
	continueToken := ""
	for {
		cql, err := o.Client.ClusterQueues().List(ctx, metav1.ListOptions{
			LabelSelector: o.LabelSelector,
			FieldSelector: o.FieldSelector,
			Limit:         o.clusterQueueListRequestLimit,
			Continue:      continueToken,
		})
		if err != nil {
			return err
		}
		if len(cql.Items) == 0 {
			return nil
		}

		o.applyActiveFilter(cql)

		if err := o.PrintObj(cql, o.Out); err != nil {
			return err
		}

		if cql.Continue == "" {
			return nil
		}
		continueToken = cql.Continue
	}
}

func (o *ClusterQueueOptions) applyActiveFilter(cql *v1beta1.ClusterQueueList) {
	if o.Active == nil || len(o.Active) == 0 {
		return
	}

	filtered := make([]v1beta1.ClusterQueue, 0, len(cql.Items))
	for _, cq := range cql.Items {
		if o.Active[0] && isActiveStatus(&cq) {
			filtered = append(filtered, cq)
		}
		if !o.Active[0] && !isActiveStatus(&cq) {
			filtered = append(filtered, cq)
		}
	}
	cql.Items = filtered
}

// printClusterQueueTable is a printer function for ClusterQueueList objects.
var _ printers.ResourcePrinterFunc = printClusterQueueTable

func printClusterQueueTable(obj runtime.Object, out io.Writer) error {
	tp := printers.NewTablePrinter(printers.PrintOptions{})
	a := &metav1.Table{
		ColumnDefinitions: []metav1.TableColumnDefinition{
			{Name: "Name", Type: "string", Format: "name"},
			{Name: "Cohort", Type: "string"},
			{Name: "Pending Workloads", Type: "integer"},
			{Name: "Admitted Workloads", Type: "integer"},
			{Name: "Active", Type: "boolean"},
			{Name: "Age", Type: "string"},
		},
		Rows: toTableRows(obj.(*v1beta1.ClusterQueueList)),
	}
	return tp.PrintObj(a, out)
}

func toTableRows(list *v1beta1.ClusterQueueList) []metav1.TableRow {
	rows := make([]metav1.TableRow, len(list.Items))
	for index := range list.Items {
		rows[index] = toTableRow(&list.Items[index])
	}
	return rows
}

func toTableRow(cq *v1beta1.ClusterQueue) metav1.TableRow {
	return metav1.TableRow{
		Object: runtime.RawExtension{Object: cq},
		Cells: []interface{}{
			cq.Name,
			cq.Spec.Cohort,
			cq.Status.PendingWorkloads,
			cq.Status.AdmittedWorkloads,
			isActiveStatus(cq),
			duration.HumanDuration(time.Since(cq.CreationTimestamp.Time)),
		},
	}
}

func isActiveStatus(cq *v1beta1.ClusterQueue) bool {
	return meta.IsStatusConditionTrue(cq.Status.Conditions, v1beta1.ClusterQueueActive)
}

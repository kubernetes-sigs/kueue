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
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/cli-runtime/pkg/printers"
	"k8s.io/kubectl/pkg/util/templates"
	"k8s.io/utils/clock"

	"sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/client-go/clientset/versioned/scheme"
	kueuev1beta1 "sigs.k8s.io/kueue/client-go/clientset/versioned/typed/kueue/v1beta1"
	"sigs.k8s.io/kueue/cmd/kueuectl/app/util"
)

var (
	cqLong = templates.LongDesc(`
		Lists all ClusterQueues, potentially limiting output to those 
		that are active/inactive and matching the label selector.
	`)
	cqExample = templates.Examples(`
		# List ClusterQueue
  		kueuectl list clusterqueue
	`)
)

type ClusterQueueOptions struct {
	Clock clock.Clock
	// PrintFlags holds options necessary for obtaining a printer
	PrintFlags *genericclioptions.PrintFlags

	Limit         int64
	LabelSelector string
	FieldSelector string

	// Kueuectl flags
	// Active is an optional flag to filter true/false (active/inactive) cluster queues
	// Active means the cluster queue has kueue.ClusterQueueActive condition with status=metav1.ConditionTrue
	Active []bool

	Client kueuev1beta1.KueueV1beta1Interface

	genericiooptions.IOStreams
}

func NewClusterQueueOptions(streams genericiooptions.IOStreams, clock clock.Clock) *ClusterQueueOptions {
	return &ClusterQueueOptions{
		PrintFlags: genericclioptions.NewPrintFlags("").WithTypeSetter(scheme.Scheme),
		IOStreams:  streams,
		Clock:      clock,
	}
}

func NewClusterQueueCmd(clientGetter util.ClientGetter, streams genericiooptions.IOStreams, clock clock.Clock) *cobra.Command {
	o := NewClusterQueueOptions(streams, clock)

	cmd := &cobra.Command{
		Use:                   "clusterqueue [--selector key1=value1] [--field-selector key1=value1] [--active=true|false]",
		DisableFlagsInUseLine: true,
		Aliases:               []string{"cq"},
		Short:                 "List ClusterQueues",
		Long:                  cqLong,
		Example:               cqExample,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			err := o.Complete(clientGetter, cmd, args)
			if err != nil {
				return err
			}
			err = o.Validate()
			if err != nil {
				return err
			}
			return o.Run(cmd.Context())
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

	clientset, err := clientGetter.KueueClientSet()
	if err != nil {
		return err
	}

	o.Client = clientset.KueueV1beta1()

	if len(args) > 0 {
		activeFlag, err := cmd.Flags().GetBoolSlice("active")
		if err != nil {
			return err
		}
		o.Active = activeFlag
	}

	return nil
}

func (o *ClusterQueueOptions) ToPrinter(headers bool) (printers.ResourcePrinterFunc, error) {
	if !o.PrintFlags.OutputFlagSpecified() {
		printer := newClusterQueueTablePrinter().
			WithHeaders(headers).
			WithClock(o.Clock)
		return printer.PrintObj, nil
	}

	printer, err := o.PrintFlags.ToPrinter()
	if err != nil {
		return nil, err
	}

	return printer.PrintObj, nil
}

func (o *ClusterQueueOptions) Validate() error {
	if !o.validActiveFlagOptionProvided() {
		return errors.New("only one active flag can be provided")
	}
	return nil
}

func (o *ClusterQueueOptions) validActiveFlagOptionProvided() bool {
	return len(o.Active) == 0 || len(o.Active) == 1
}

// Run prints the cluster queues.
func (o *ClusterQueueOptions) Run(ctx context.Context) error {
	var totalCount int

	opts := metav1.ListOptions{
		LabelSelector: o.LabelSelector,
		FieldSelector: o.FieldSelector,
		Limit:         o.Limit,
	}

	tabWriter := printers.GetNewTabWriter(o.Out)

	for {
		headers := totalCount == 0

		list, err := o.Client.ClusterQueues().List(ctx, opts)
		if err != nil {
			return err
		}

		o.filterList(list)

		totalCount += len(list.Items)

		printer, err := o.ToPrinter(headers)
		if err != nil {
			return err
		}

		if err := printer.PrintObj(list, tabWriter); err != nil {
			return err
		}

		if list.Continue != "" {
			opts.Continue = list.Continue
			continue
		}

		if totalCount == 0 {
			fmt.Fprintln(o.ErrOut, "No resources found")
			return nil
		}

		if err := tabWriter.Flush(); err != nil {
			return err
		}

		return nil
	}
}

func (o *ClusterQueueOptions) filterList(cql *v1beta1.ClusterQueueList) {
	if len(o.Active) == 0 {
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

func isActiveStatus(cq *v1beta1.ClusterQueue) bool {
	return meta.IsStatusConditionTrue(cq.Status.Conditions, v1beta1.ClusterQueueActive)
}

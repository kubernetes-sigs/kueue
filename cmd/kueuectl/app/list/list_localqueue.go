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

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/cli-runtime/pkg/printers"

	"sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/client-go/clientset/versioned/scheme"
	kueuev1beta1 "sigs.k8s.io/kueue/client-go/clientset/versioned/typed/kueue/v1beta1"
	"sigs.k8s.io/kueue/cmd/kueuectl/app/util"
)

const (
	lqLong = `Lists LocalQueues that match the given criteria: point to a specific CQ, 
being active/inactive, belonging to the specified namespace, matching 
the label selector or the field selector.`
	lqExample = `  # List LocalQueue
  kueuectl list localqueue`
)

type LocalQueueOptions struct {
	PrintFlags *genericclioptions.PrintFlags

	AllNamespaces      bool
	Namespace          string
	FieldSelector      string
	LabelSelector      string
	ClusterQueueFilter string

	Client kueuev1beta1.KueueV1beta1Interface

	PrintObj printers.ResourcePrinterFunc

	genericiooptions.IOStreams
}

func NewLocalQueueOptions(streams genericiooptions.IOStreams) *LocalQueueOptions {
	return &LocalQueueOptions{
		PrintFlags: genericclioptions.NewPrintFlags("").WithTypeSetter(scheme.Scheme),
		IOStreams:  streams,
	}
}

func NewLocalQueueCmd(clientGetter util.ClientGetter, streams genericiooptions.IOStreams) *cobra.Command {
	o := NewLocalQueueOptions(streams)

	cmd := &cobra.Command{
		Use: "localqueue [-–clusterqueue CLUSTER_QUEUE_NAME] [--selector key1=value1] [--field-selector key1=value1] [--all-namespaces]",
		// To do not add "[flags]" suffix on the end of usage line
		DisableFlagsInUseLine: true,
		Aliases:               []string{"lq"},
		Short:                 "List LocalQueue",
		Long:                  lqLong,
		Example:               lqExample,
		Run: func(cmd *cobra.Command, args []string) {
			cobra.CheckErr(o.Complete(clientGetter, cmd, args))
			cobra.CheckErr(o.Run(cmd.Context()))
		},
	}

	o.PrintFlags.AddFlags(cmd)

	addAllNamespacesFlagVar(cmd, &o.AllNamespaces)
	addFieldSelectorFlagVar(cmd, &o.FieldSelector)
	addLabelSelectorFlagVar(cmd, &o.LabelSelector)
	addClusterQueueFilterFlagVar(cmd, &o.ClusterQueueFilter)

	return cmd
}

// Complete completes all the required options
func (o *LocalQueueOptions) Complete(clientGetter util.ClientGetter, cmd *cobra.Command, args []string) error {
	var err error

	o.Namespace, _, err = clientGetter.ToRawKubeConfigLoader().Namespace()
	if err != nil {
		return err
	}

	clientset, err := clientGetter.KueueClientSet()
	if err != nil {
		return err
	}

	o.Client = clientset.KueueV1beta1()

	if !o.PrintFlags.OutputFlagSpecified() {
		printer := newLocalQueueTablePrinter()
		if o.AllNamespaces {
			printer.WithNamespace()
		}
		o.PrintObj = printer.PrintObj
	} else {
		printer, err := o.PrintFlags.ToPrinter()
		if err != nil {
			return err
		}
		o.PrintObj = printer.PrintObj
	}

	return nil
}

// Run performs the list operation.
func (o *LocalQueueOptions) Run(ctx context.Context) error {
	namespace := o.Namespace
	if o.AllNamespaces {
		namespace = ""
	}

	opts := metav1.ListOptions{LabelSelector: o.LabelSelector, FieldSelector: o.FieldSelector}
	list, err := o.Client.LocalQueues(namespace).List(ctx, opts)
	if err != nil {
		return err
	}

	o.filterList(list)

	if len(list.Items) == 0 {
		if !o.AllNamespaces {
			fmt.Fprintf(o.ErrOut, "No resources found in %s namespace.\n", o.Namespace)
		} else {
			fmt.Fprintln(o.ErrOut, "No resources found")
		}
		return nil
	}

	return o.PrintObj(list, o.Out)
}

func (o *LocalQueueOptions) filterList(list *v1beta1.LocalQueueList) {
	if len(o.ClusterQueueFilter) > 0 {
		filteredItems := make([]v1beta1.LocalQueue, 0, len(o.ClusterQueueFilter))
		for _, lq := range list.Items {
			if lq.Spec.ClusterQueue == v1beta1.ClusterQueueReference(o.ClusterQueueFilter) {
				filteredItems = append(filteredItems, lq)
			}
		}
		list.Items = filteredItems
	}
}

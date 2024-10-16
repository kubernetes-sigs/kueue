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

package stop

import (
	"context"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/cli-runtime/pkg/printers"
	"k8s.io/kubectl/pkg/util/templates"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/client-go/clientset/versioned/scheme"
	kueuev1beta1 "sigs.k8s.io/kueue/client-go/clientset/versioned/typed/kueue/v1beta1"
	"sigs.k8s.io/kueue/cmd/kueuectl/app/completion"
	"sigs.k8s.io/kueue/cmd/kueuectl/app/util"
)

var (
	cqLong    = templates.LongDesc(`Puts the given ClusterQueue on hold.`)
	cqExample = templates.Examples(`
		# Stop the clusterqueue
		kueuectl stop clusterqueue my-clusterqueue
	`)
)

type ClusterQueueOptions struct {
	ClusterQueueName   string
	KeepAlreadyRunning bool
	Client             kueuev1beta1.KueueV1beta1Interface
	PrintFlags         *genericclioptions.PrintFlags
	PrintObj           printers.ResourcePrinterFunc
	genericiooptions.IOStreams
}

func NewClusterQueueOptions(streams genericiooptions.IOStreams) *ClusterQueueOptions {
	return &ClusterQueueOptions{
		PrintFlags: genericclioptions.NewPrintFlags("").WithTypeSetter(scheme.Scheme),
		IOStreams:  streams,
	}
}

func NewClusterQueueCmd(clientGetter util.ClientGetter, streams genericiooptions.IOStreams) *cobra.Command {
	o := NewClusterQueueOptions(streams)

	cmd := &cobra.Command{
		Use:                   "clusterqueue NAME [--keep-already-running]",
		DisableFlagsInUseLine: true,
		Aliases:               []string{"cq"},
		Short:                 "Stop the ClusterQueue",
		Long:                  cqLong,
		Example:               cqExample,
		Args:                  cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		ValidArgsFunction:     completion.ClusterQueueNameFunc(clientGetter, ptr.To(true)),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			err := o.Complete(clientGetter, args)
			if err != nil {
				return err
			}
			return o.Run(cmd.Context())
		},
	}

	o.PrintFlags.AddFlags(cmd)

	addKeepAlreadyRunningFlagVar(cmd, &o.KeepAlreadyRunning)

	return cmd
}

// Complete completes all the required options
func (o *ClusterQueueOptions) Complete(clientGetter util.ClientGetter, args []string) error {
	o.ClusterQueueName = args[0]

	clientset, err := clientGetter.KueueClientSet()
	if err != nil {
		return err
	}

	o.Client = clientset.KueueV1beta1()

	printer, err := o.PrintFlags.ToPrinter()
	if err != nil {
		return err
	}

	o.PrintObj = printer.PrintObj

	return nil
}

// Run executes the command
func (o *ClusterQueueOptions) Run(ctx context.Context) error {
	cq, err := o.Client.ClusterQueues().Get(ctx, o.ClusterQueueName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	if cq == nil {
		return nil
	}

	cqOriginal := cq.DeepCopy()
	o.stopClusterQueue(cq)

	opts := metav1.PatchOptions{}
	patch := client.MergeFrom(cqOriginal)
	data, err := patch.Data(cq)
	if err != nil {
		return err
	}
	cq, err = o.Client.ClusterQueues().
		Patch(ctx, o.ClusterQueueName, types.MergePatchType, data, opts)
	if err != nil {
		return err
	}

	return o.PrintObj(cq, o.Out)
}

func (o *ClusterQueueOptions) stopClusterQueue(cq *v1beta1.ClusterQueue) {
	if o.KeepAlreadyRunning {
		cq.Spec.StopPolicy = ptr.To(v1beta1.Hold)
	} else {
		cq.Spec.StopPolicy = ptr.To(v1beta1.HoldAndDrain)
	}
}

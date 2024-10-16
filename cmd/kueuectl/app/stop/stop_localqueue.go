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
	lqLong    = templates.LongDesc(`Puts the given LocalQueue on hold.`)
	lqExample = templates.Examples(`
		# Stop the localqueue
		kueuectl stop localqueue my-localqueue
	`)
)

type LocalQueueOptions struct {
	PrintFlags *genericclioptions.PrintFlags
	PrintObj   printers.ResourcePrinterFunc

	LocalQueueName     string
	Namespace          string
	KeepAlreadyRunning bool

	Client kueuev1beta1.KueueV1beta1Interface

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
		Use:                   "localqueue NAME [--keep-already-running]",
		DisableFlagsInUseLine: true,
		Aliases:               []string{"lq"},
		Short:                 "Stop the LocalQueue",
		Long:                  lqLong,
		Example:               lqExample,
		Args:                  cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		ValidArgsFunction:     completion.LocalQueueNameFunc(clientGetter, ptr.To(true)),
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
func (o *LocalQueueOptions) Complete(clientGetter util.ClientGetter, args []string) error {
	o.LocalQueueName = args[0]

	namespace, _, err := clientGetter.ToRawKubeConfigLoader().Namespace()
	if err != nil {
		return err
	}

	o.Namespace = namespace

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
func (o *LocalQueueOptions) Run(ctx context.Context) error {
	lq, err := o.Client.LocalQueues(o.Namespace).Get(ctx, o.LocalQueueName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	if lq == nil {
		return nil
	}

	lqOriginal := lq.DeepCopy()
	o.stopLocalQueue(lq)

	opts := metav1.PatchOptions{}
	patch := client.MergeFrom(lqOriginal)
	data, err := patch.Data(lq)
	if err != nil {
		return err
	}
	lq, err = o.Client.LocalQueues(o.Namespace).
		Patch(ctx, o.LocalQueueName, types.MergePatchType, data, opts)
	if err != nil {
		return err
	}

	return o.PrintObj(lq, o.Out)
}

func (o *LocalQueueOptions) stopLocalQueue(lq *v1beta1.LocalQueue) {
	if o.KeepAlreadyRunning {
		lq.Spec.StopPolicy = ptr.To(v1beta1.Hold)
	} else {
		lq.Spec.StopPolicy = ptr.To(v1beta1.HoldAndDrain)
	}
}

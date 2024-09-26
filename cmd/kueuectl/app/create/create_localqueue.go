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

package create

import (
	"context"
	"errors"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/cli-runtime/pkg/printers"
	"k8s.io/kubectl/pkg/util/templates"

	"sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/client-go/clientset/versioned/scheme"
	kueuev1beta1 "sigs.k8s.io/kueue/client-go/clientset/versioned/typed/kueue/v1beta1"
	"sigs.k8s.io/kueue/cmd/kueuectl/app/completion"
	"sigs.k8s.io/kueue/cmd/kueuectl/app/util"
)

var (
	lqLong    = templates.LongDesc(`Create a local queue with the given name in the specified namespace.`)
	lqExample = templates.Examples(`
		# Create a local queue
  		kueuectl create localqueue my-local-queue -c my-cluster-queue
  
  		# Create a local queue with unknown cluster queue
  		kueuectl create localqueue my-local-queue -c my-cluster-queue -i
	`)
)

type LocalQueueOptions struct {
	PrintFlags *genericclioptions.PrintFlags

	DryRunStrategy   util.DryRunStrategy
	Name             string
	Namespace        string
	EnforceNamespace bool
	ClusterQueue     v1beta1.ClusterQueueReference
	IgnoreUnknownCq  bool

	UserSpecifiedClusterQueue string

	Client kueuev1beta1.KueueV1beta1Interface

	PrintObj printers.ResourcePrinterFunc

	genericiooptions.IOStreams
}

func NewLocalQueueOptions(streams genericiooptions.IOStreams) *LocalQueueOptions {
	return &LocalQueueOptions{
		PrintFlags: genericclioptions.NewPrintFlags("created").WithTypeSetter(scheme.Scheme),
		IOStreams:  streams,
	}
}

func NewLocalQueueCmd(clientGetter util.ClientGetter, streams genericiooptions.IOStreams) *cobra.Command {
	o := NewLocalQueueOptions(streams)

	cmd := &cobra.Command{
		Use: "localqueue NAME -c CLUSTER_QUEUE_NAME [--ignore-unknown-cq] [--dry-run STRATEGY]",
		// To do not add "[flags]" suffix on the end of usage line
		DisableFlagsInUseLine: true,
		Aliases:               []string{"lq"},
		Short:                 "Creates a localqueue",
		Long:                  lqLong,
		Example:               lqExample,
		Args:                  cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			cmd.SilenceUsage = true
			err := o.Complete(clientGetter, cmd, args)
			if err != nil {
				return err
			}
			err = o.Validate(ctx)
			if err != nil {
				return err
			}
			return o.Run(cmd.Context())
		},
	}

	o.PrintFlags.AddFlags(cmd)

	cmd.Flags().StringVarP(&o.UserSpecifiedClusterQueue, "clusterqueue", "c", "",
		"The cluster queue name which will be associated with the local queue (required).")
	cmd.Flags().BoolVarP(&o.IgnoreUnknownCq, "ignore-unknown-cq", "i", false,
		"Ignore unknown cluster queue.")

	cobra.CheckErr(cmd.RegisterFlagCompletionFunc("clusterqueue", completion.ClusterQueueNameFunc(clientGetter, nil)))

	_ = cmd.MarkFlagRequired("clusterqueue")

	return cmd
}

// Complete completes all the required options
func (o *LocalQueueOptions) Complete(clientGetter util.ClientGetter, cmd *cobra.Command, args []string) error {
	o.Name = args[0]

	var err error
	o.Namespace, o.EnforceNamespace, err = clientGetter.ToRawKubeConfigLoader().Namespace()
	if err != nil {
		return err
	}

	o.ClusterQueue = v1beta1.ClusterQueueReference(o.UserSpecifiedClusterQueue)

	clientset, err := clientGetter.KueueClientSet()
	if err != nil {
		return err
	}

	o.Client = clientset.KueueV1beta1()

	o.DryRunStrategy, err = util.GetDryRunStrategy(cmd)
	if err != nil {
		return err
	}

	err = util.PrintFlagsWithDryRunStrategy(o.PrintFlags, o.DryRunStrategy)
	if err != nil {
		return err
	}

	printer, err := o.PrintFlags.ToPrinter()
	if err != nil {
		return err
	}

	o.PrintObj = printer.PrintObj

	return nil
}

// Validate validates required fields are set to support structured generation
func (o *LocalQueueOptions) Validate(ctx context.Context) error {
	if len(o.Name) == 0 {
		return errors.New("name must be specified")
	}
	if len(o.ClusterQueue) == 0 {
		return errors.New("clusterqueue must be specified")
	}
	if len(o.Namespace) == 0 {
		return errors.New("namespace must be specified")
	}
	if !o.IgnoreUnknownCq {
		_, err := o.Client.ClusterQueues().Get(ctx, o.UserSpecifiedClusterQueue, metav1.GetOptions{})
		if err != nil {
			return err
		}
	}
	return nil
}

// Run create localqueue
func (o *LocalQueueOptions) Run(ctx context.Context) error {
	lq := o.createLocalQueue()
	if o.DryRunStrategy != util.DryRunClient {
		var (
			createOptions metav1.CreateOptions
			err           error
		)
		if o.DryRunStrategy == util.DryRunServer {
			createOptions.DryRun = []string{metav1.DryRunAll}
		}
		lq, err = o.Client.LocalQueues(o.Namespace).Create(ctx, lq, createOptions)
		if err != nil {
			return err
		}
	}
	return o.PrintObj(lq, o.Out)
}

func (o *LocalQueueOptions) createLocalQueue() *v1beta1.LocalQueue {
	return &v1beta1.LocalQueue{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1beta1.SchemeGroupVersion.String(), Kind: "LocalQueue"},
		ObjectMeta: metav1.ObjectMeta{Name: o.Name, Namespace: o.Namespace},
		Spec:       v1beta1.LocalQueueSpec{ClusterQueue: o.ClusterQueue},
	}
}

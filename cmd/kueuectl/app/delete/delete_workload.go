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

package delete

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/cli-runtime/pkg/printers"
	"k8s.io/client-go/dynamic"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/client-go/clientset/versioned/scheme"
	kueuev1beta1 "sigs.k8s.io/kueue/client-go/clientset/versioned/typed/kueue/v1beta1"
	"sigs.k8s.io/kueue/cmd/kueuectl/app/completion"
	"sigs.k8s.io/kueue/cmd/kueuectl/app/util"
)

const (
	wlExample = `  # Delete the Workload
  kueuectl delete workload my-workload`
)

type WorkloadOptions struct {
	PrintFlags *genericclioptions.PrintFlags

	Names     []string
	Namespace string

	CascadeStrategy metav1.DeletionPropagation
	DryRunStrategy  util.DryRunStrategy

	Client        kueuev1beta1.KueueV1beta1Interface
	DynamicClient dynamic.Interface
	RestMapper    meta.RESTMapper

	PrintObj printers.ResourcePrinterFunc

	genericiooptions.IOStreams
}

func NewWorkloadOptions(streams genericiooptions.IOStreams) *WorkloadOptions {
	return &WorkloadOptions{
		PrintFlags: genericclioptions.NewPrintFlags("deleted").WithTypeSetter(scheme.Scheme),
		IOStreams:  streams,
	}
}

func NewWorkloadCmd(clientGetter util.ClientGetter, streams genericiooptions.IOStreams) *cobra.Command {
	o := NewWorkloadOptions(streams)

	cmd := &cobra.Command{
		Use:                   "workload NAME [--cascade STRATEGY] [--dry-run STRATEGY]",
		DisableFlagsInUseLine: true,
		Aliases:               []string{"wl"},
		Short:                 "Delete the given Workload and corresponding to it Job",
		Example:               wlExample,
		Args:                  cobra.MinimumNArgs(1),
		ValidArgsFunction:     completion.WorkloadNameFunc(clientGetter, ptr.To(true)),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true

			err := o.Complete(clientGetter, cmd, args)
			if err != nil {
				return err
			}

			return o.Run(cmd.Context())
		},
	}

	addCascadingFlag(cmd)
	util.AddDryRunFlag(cmd)

	o.PrintFlags.AddFlags(cmd)

	return cmd
}

// Complete completes all the required options
func (o *WorkloadOptions) Complete(clientGetter util.ClientGetter, cmd *cobra.Command, args []string) error {
	o.Names = args

	var err error

	o.Namespace, _, err = clientGetter.ToRawKubeConfigLoader().Namespace()
	if err != nil {
		return err
	}

	o.DryRunStrategy, err = util.GetDryRunStrategy(cmd)
	if err != nil {
		return err
	}

	err = util.PrintFlagsWithDryRunStrategy(o.PrintFlags, o.DryRunStrategy)
	if err != nil {
		return err
	}

	o.CascadeStrategy, err = getCascadingStrategy(cmd)
	if err != nil {
		return err
	}

	printer, err := o.PrintFlags.ToPrinter()
	if err != nil {
		return err
	}

	o.PrintObj = printer.PrintObj

	clientset, err := clientGetter.KueueClientSet()
	if err != nil {
		return err
	}

	o.Client = clientset.KueueV1beta1()

	o.DynamicClient, err = clientGetter.DynamicClient()
	if err != nil {
		return err
	}

	o.RestMapper, err = clientGetter.ToRESTMapper()
	if err != nil {
		return err
	}

	return nil
}

// Run delete a resource
func (o *WorkloadOptions) Run(ctx context.Context) error {
	for _, name := range o.Names {
		wl, err := o.Client.Workloads(o.Namespace).Get(ctx, name, metav1.GetOptions{})
		if client.IgnoreNotFound(err) != nil {
			return err
		}
		if err != nil {
			fmt.Fprintln(o.ErrOut, err)
			continue
		}

		if o.DryRunStrategy != util.DryRunClient {
			deleteOptions := metav1.DeleteOptions{
				PropagationPolicy: ptr.To(o.CascadeStrategy),
			}

			if o.DryRunStrategy == util.DryRunServer {
				deleteOptions.DryRun = []string{metav1.DryRunAll}
			}

			for _, or := range wl.OwnerReferences {
				gv, err := schema.ParseGroupVersion(or.APIVersion)
				if err != nil {
					return err
				}

				mapping, err := o.RestMapper.RESTMapping(gv.WithKind(or.Kind).GroupKind(), gv.Version)
				if err != nil {
					return err
				}

				if err = o.DynamicClient.Resource(mapping.Resource).Namespace(o.Namespace).
					Delete(ctx, or.Name, deleteOptions); client.IgnoreNotFound(err) != nil {
					return err
				}
			}

			if err := o.Client.Workloads(o.Namespace).Delete(ctx, name, deleteOptions); client.IgnoreNotFound(err) != nil {
				return err
			}
		}

		if err := o.PrintObj(wl, o.Out); err != nil {
			return err
		}
	}

	return nil
}

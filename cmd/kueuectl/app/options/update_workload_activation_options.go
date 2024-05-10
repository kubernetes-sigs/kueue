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

package options

import (
	"context"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/cli-runtime/pkg/printers"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/client-go/clientset/versioned/scheme"
	kueuev1beta1 "sigs.k8s.io/kueue/client-go/clientset/versioned/typed/kueue/v1beta1"
	"sigs.k8s.io/kueue/cmd/kueuectl/app/util"
)

type UpdateWorkloadActivationOptions struct {
	PrintFlags *genericclioptions.PrintFlags

	Active           bool
	DryRunStrategy   util.DryRunStrategy
	Name             string
	Namespace        string
	EnforceNamespace bool

	Client kueuev1beta1.KueueV1beta1Interface

	PrintObj printers.ResourcePrinterFunc

	genericiooptions.IOStreams
}

func NewUpdateWorkloadActivationOptions(streams genericiooptions.IOStreams, operation string, active bool) *UpdateWorkloadActivationOptions {
	return &UpdateWorkloadActivationOptions{
		PrintFlags: genericclioptions.NewPrintFlags(operation).WithTypeSetter(scheme.Scheme),
		Active:     active,
		IOStreams:  streams,
	}
}

// Complete completes all the required options
func (o *UpdateWorkloadActivationOptions) Complete(clientGetter util.ClientGetter, cmd *cobra.Command, args []string) error {
	o.Name = args[0]

	var err error
	o.Namespace, o.EnforceNamespace, err = clientGetter.ToRawKubeConfigLoader().Namespace()
	if err != nil {
		return err
	}

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

// Run performs to update a Workload status operation.
func (o *UpdateWorkloadActivationOptions) Run(ctx context.Context) error {
	wl, err := o.Client.Workloads(o.Namespace).Get(ctx, o.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	wlOriginal := wl.DeepCopy()
	wl.Spec.Active = ptr.To(o.Active)

	if o.DryRunStrategy != util.DryRunClient {
		opts := metav1.PatchOptions{}
		if o.DryRunStrategy == util.DryRunServer {
			opts.DryRun = []string{metav1.DryRunAll}
		}
		patch := client.MergeFrom(wlOriginal)
		data, err := patch.Data(wl)
		if err != nil {
			return err
		}
		wl, err = o.Client.Workloads(o.Namespace).Patch(ctx, wl.Name, types.MergePatchType, data, opts)
		if err != nil {
			return err
		}
	}

	return o.PrintObj(wl, o.Out)
}

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

	"sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/client-go/clientset/versioned/scheme"
	kueuev1beta1 "sigs.k8s.io/kueue/client-go/clientset/versioned/typed/kueue/v1beta1"
	"sigs.k8s.io/kueue/cmd/kueuectl/app/util"
)

const (
	rfLong    = `Create a resource flavor with the given name.`
	rfExample = `  # Create a resource flavor 
  kueuectl create flavor my-resource-flavor`
)

var (
	nameMustBeSpecifiedErr = errors.New("name must be specified")
)

type ResourceFlavorOptions struct {
	PrintFlags *genericclioptions.PrintFlags

	DryRunStrategy util.DryRunStrategy
	Name           string

	Client kueuev1beta1.KueueV1beta1Interface

	PrintObj printers.ResourcePrinterFunc

	genericiooptions.IOStreams
}

func NewResourceFlavorOptions(streams genericiooptions.IOStreams) *ResourceFlavorOptions {
	return &ResourceFlavorOptions{
		PrintFlags: genericclioptions.NewPrintFlags("created").WithTypeSetter(scheme.Scheme),
		IOStreams:  streams,
	}
}

func NewResourceFlavorCmd(clientGetter util.ClientGetter, streams genericiooptions.IOStreams) *cobra.Command {
	o := NewResourceFlavorOptions(streams)

	cmd := &cobra.Command{
		Use:                   "resourceflavor NAME",
		DisableFlagsInUseLine: true,
		Aliases:               []string{"rf"},
		Short:                 "Creates a resource flavor",
		Long:                  rfLong,
		Example:               rfExample,
		Args:                  cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		Run: func(cmd *cobra.Command, args []string) {
			ctx := cmd.Context()
			cobra.CheckErr(o.Complete(clientGetter, cmd, args))
			cobra.CheckErr(o.Validate())
			cobra.CheckErr(o.Run(ctx))
		},
	}

	o.PrintFlags.AddFlags(cmd)

	return cmd
}

// Complete completes all the required options
func (o *ResourceFlavorOptions) Complete(clientGetter util.ClientGetter, cmd *cobra.Command, args []string) error {
	o.Name = args[0]

	var err error

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
func (o *ResourceFlavorOptions) Validate() error {
	if len(o.Name) == 0 {
		return nameMustBeSpecifiedErr
	}
	return nil
}

// Run create a resource
func (o *ResourceFlavorOptions) Run(ctx context.Context) error {
	rf := o.createResourceFlavor()
	if o.DryRunStrategy != util.DryRunClient {
		var (
			createOptions metav1.CreateOptions
			err           error
		)
		if o.DryRunStrategy == util.DryRunServer {
			createOptions.DryRun = []string{metav1.DryRunAll}
		}
		rf, err = o.Client.ResourceFlavors().Create(ctx, rf, createOptions)
		if err != nil {
			return err
		}
	}
	return o.PrintObj(rf, o.Out)
}

func (o *ResourceFlavorOptions) createResourceFlavor() *v1beta1.ResourceFlavor {
	return &v1beta1.ResourceFlavor{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1beta1.SchemeGroupVersion.String(), Kind: "ResourceFlavor"},
		ObjectMeta: metav1.ObjectMeta{Name: o.Name},
		Spec:       v1beta1.ResourceFlavorSpec{},
	}
}

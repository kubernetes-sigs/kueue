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

	"github.com/ray-project/kuberay/ray-operator/pkg/client/clientset/versioned/scheme"
	rayv1 "github.com/ray-project/kuberay/ray-operator/pkg/client/clientset/versioned/typed/ray/v1"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/cli-runtime/pkg/printers"
	"k8s.io/kubectl/pkg/util/templates"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd/completion"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd/util"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/constants"
)

var (
	rayJobExample = templates.Examples(`
		# Delete RayJob 
  		kjobctl delete rayjob my-application-profile-rayjob-k2wzd
	`)
)

type RayJobOptions struct {
	PrintFlags *genericclioptions.PrintFlags

	RayJobNames []string
	Namespace   string

	CascadeStrategy metav1.DeletionPropagation
	DryRunStrategy  util.DryRunStrategy

	Client rayv1.RayV1Interface

	PrintObj printers.ResourcePrinterFunc

	genericiooptions.IOStreams
}

func NewRayJobOptions(streams genericiooptions.IOStreams) *RayJobOptions {
	return &RayJobOptions{
		PrintFlags: genericclioptions.NewPrintFlags("deleted").WithTypeSetter(scheme.Scheme),
		IOStreams:  streams,
	}
}

func NewRayJobCmd(clientGetter util.ClientGetter, streams genericiooptions.IOStreams) *cobra.Command {
	o := NewRayJobOptions(streams)

	cmd := &cobra.Command{
		Use:                   "rayjob NAME [--cascade STRATEGY] [--dry-run STRATEGY]",
		DisableFlagsInUseLine: true,
		Short:                 "Delete RayJob",
		Example:               rayJobExample,
		Args:                  cobra.MinimumNArgs(1),
		ValidArgsFunction:     completion.RayJobNameFunc(clientGetter),
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

func (o *RayJobOptions) Complete(clientGetter util.ClientGetter, cmd *cobra.Command, args []string) error {
	o.RayJobNames = args

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

	clientset, err := clientGetter.RayClientset()
	if err != nil {
		return err
	}

	o.Client = clientset.RayV1()

	return nil
}

func (o *RayJobOptions) Run(ctx context.Context) error {
	for _, rayJobName := range o.RayJobNames {
		rayJob, err := o.Client.RayJobs(o.Namespace).Get(ctx, rayJobName, metav1.GetOptions{})
		if client.IgnoreNotFound(err) != nil {
			return err
		}
		if err != nil {
			fmt.Fprintln(o.ErrOut, err)
			continue
		}
		if _, ok := rayJob.Labels[constants.ProfileLabel]; !ok {
			fmt.Fprintf(o.ErrOut, "rayjobs.ray.io \"%s\" not created via kjob\n", rayJob.Name)
			continue
		}

		if o.DryRunStrategy != util.DryRunClient {
			deleteOptions := metav1.DeleteOptions{
				PropagationPolicy: ptr.To(o.CascadeStrategy),
			}

			if o.DryRunStrategy == util.DryRunServer {
				deleteOptions.DryRun = []string{metav1.DryRunAll}
			}

			if err := o.Client.RayJobs(o.Namespace).Delete(ctx, rayJobName, deleteOptions); err != nil {
				return err
			}
		}

		if err := o.PrintObj(rayJob, o.Out); err != nil {
			return err
		}
	}

	return nil
}

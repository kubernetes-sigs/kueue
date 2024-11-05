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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/cli-runtime/pkg/printers"
	"k8s.io/client-go/kubernetes/scheme"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/kubectl/pkg/util/templates"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd/completion"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd/util"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/constants"
)

var (
	interactiveExample = templates.Examples(`
		# Delete interactive shell 
  		kjobctl delete interactive my-application-profile-interactive-k2wzd
	`)
)

type InteractiveOptions struct {
	PrintFlags *genericclioptions.PrintFlags

	InteractiveNames []string
	Namespace        string

	CascadeStrategy metav1.DeletionPropagation
	DryRunStrategy  util.DryRunStrategy

	Client corev1.CoreV1Interface

	PrintObj printers.ResourcePrinterFunc

	genericiooptions.IOStreams
}

func NewInteractiveOptions(streams genericiooptions.IOStreams) *InteractiveOptions {
	return &InteractiveOptions{
		PrintFlags: genericclioptions.NewPrintFlags("deleted").WithTypeSetter(scheme.Scheme),
		IOStreams:  streams,
	}
}

func NewInteractiveCmd(clientGetter util.ClientGetter, streams genericiooptions.IOStreams) *cobra.Command {
	o := NewInteractiveOptions(streams)

	cmd := &cobra.Command{
		Use:                   "interactive NAME [--cascade STRATEGY] [--dry-run STRATEGY]",
		DisableFlagsInUseLine: true,
		Short:                 "Delete interactive shell",
		Example:               interactiveExample,
		Args:                  cobra.MinimumNArgs(1),
		ValidArgsFunction:     completion.PodNameFunc(clientGetter),
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

func (o *InteractiveOptions) Complete(clientGetter util.ClientGetter, cmd *cobra.Command, args []string) error {
	o.InteractiveNames = args

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

	clientset, err := clientGetter.K8sClientset()
	if err != nil {
		return err
	}

	o.Client = clientset.CoreV1()

	return nil
}

func (o *InteractiveOptions) Run(ctx context.Context) error {
	for _, podName := range o.InteractiveNames {
		pod, err := o.Client.Pods(o.Namespace).Get(ctx, podName, metav1.GetOptions{})
		if client.IgnoreNotFound(err) != nil {
			return err
		}
		if err != nil {
			fmt.Fprintln(o.ErrOut, err)
			continue
		}
		if _, ok := pod.Labels[constants.ProfileLabel]; !ok {
			fmt.Fprintf(o.ErrOut, "pods \"%s\" not created via kjob\n", pod.Name)
			continue
		}

		if o.DryRunStrategy != util.DryRunClient {
			deleteOptions := metav1.DeleteOptions{
				PropagationPolicy: ptr.To(o.CascadeStrategy),
			}

			if o.DryRunStrategy == util.DryRunServer {
				deleteOptions.DryRun = []string{metav1.DryRunAll}
			}

			if err := o.Client.Pods(o.Namespace).Delete(ctx, podName, deleteOptions); err != nil {
				return err
			}
		}

		if err := o.PrintObj(pod, o.Out); err != nil {
			return err
		}
	}

	return nil
}

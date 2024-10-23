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
	k8s "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/kubectl/pkg/util/templates"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/apis/v1alpha1"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd/completion"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd/util"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/constants"
)

var (
	slurmExample = templates.Examples(`  
		# Delete Slurm 
  		kjobctl delete slurm my-application-profile-slurm-k2wzd
	`)
)

type SlurmOptions struct {
	PrintFlags *genericclioptions.PrintFlags

	JobNames  []string
	Namespace string

	CascadeStrategy metav1.DeletionPropagation
	DryRunStrategy  util.DryRunStrategy

	Clientset k8s.Interface

	PrintObj printers.ResourcePrinterFunc

	genericiooptions.IOStreams
}

func NewSlurmOptions(streams genericiooptions.IOStreams) *SlurmOptions {
	return &SlurmOptions{
		PrintFlags: genericclioptions.NewPrintFlags("deleted").WithTypeSetter(scheme.Scheme),
		IOStreams:  streams,
	}
}

func NewSlurmCmd(clientGetter util.ClientGetter, streams genericiooptions.IOStreams) *cobra.Command {
	o := NewSlurmOptions(streams)

	cmd := &cobra.Command{
		Use:                   "slurm NAME [--cascade STRATEGY] [--dry-run STRATEGY]",
		DisableFlagsInUseLine: true,
		Short:                 "Delete Slurm",
		Example:               slurmExample,
		Args:                  cobra.MinimumNArgs(1),
		ValidArgsFunction:     completion.JobNameFunc(clientGetter, v1alpha1.SlurmMode),
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

func (o *SlurmOptions) Complete(clientGetter util.ClientGetter, cmd *cobra.Command, args []string) error {
	o.JobNames = args

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

	o.Clientset, err = clientGetter.K8sClientset()
	if err != nil {
		return err
	}

	return nil
}

func (o *SlurmOptions) Run(ctx context.Context) error {
	for _, jobName := range o.JobNames {
		if err := o.deleteJob(ctx, jobName); err != nil {
			return err
		}
	}

	return nil
}

func (o *SlurmOptions) deleteJob(ctx context.Context, jobName string) error {
	job, err := o.Clientset.BatchV1().Jobs(o.Namespace).Get(ctx, jobName, metav1.GetOptions{})
	if client.IgnoreNotFound(err) != nil {
		return err
	}
	if err != nil {
		fmt.Fprintln(o.ErrOut, err)
		return nil
	}
	if _, ok := job.Labels[constants.ProfileLabel]; !ok {
		fmt.Fprintf(o.ErrOut, "jobs.batch \"%s\" not created via kjob\n", job.Name)
		return nil
	}
	if job.Labels[constants.ModeLabel] != string(v1alpha1.SlurmMode) {
		fmt.Fprintf(o.ErrOut, "jobs.batch \"%s\" created in \"%s\" mode. Switch to the correct mode to delete it\n",
			job.Name, job.Labels[constants.ModeLabel])
		return nil
	}

	if o.DryRunStrategy != util.DryRunClient {
		deleteOptions := metav1.DeleteOptions{
			PropagationPolicy: ptr.To(o.CascadeStrategy),
		}

		if o.DryRunStrategy == util.DryRunServer {
			deleteOptions.DryRun = []string{metav1.DryRunAll}
		}

		if err := o.Clientset.BatchV1().Jobs(o.Namespace).Delete(ctx, jobName, deleteOptions); err != nil {
			return err
		}
	}

	return o.PrintObj(job, o.Out)
}

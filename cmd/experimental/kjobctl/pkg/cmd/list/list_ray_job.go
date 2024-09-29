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

	"github.com/ray-project/kuberay/ray-operator/pkg/client/clientset/versioned/scheme"
	rayv1 "github.com/ray-project/kuberay/ray-operator/pkg/client/clientset/versioned/typed/ray/v1"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/cli-runtime/pkg/printers"
	"k8s.io/kubectl/pkg/util/templates"
	"k8s.io/utils/clock"
	kueueconstants "sigs.k8s.io/kueue/pkg/controller/constants"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd/completion"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd/util"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/constants"
)

var (
	rayJobExample = templates.Examples(`
		# List RayJob
  		kjobctl list rayjob
  
  		# List RayJob with profile filter
  		kjobctl list rayjob --profile my-profile
	`)
)

type RayJobOptions struct {
	Clock      clock.Clock
	PrintFlags *genericclioptions.PrintFlags

	Limit            int64
	AllNamespaces    bool
	Namespace        string
	ProfileFilter    string
	LocalQueueFilter string
	FieldSelector    string
	LabelSelector    string

	Client rayv1.RayV1Interface

	genericiooptions.IOStreams
}

func NewRayJobOptions(streams genericiooptions.IOStreams, clock clock.Clock) *RayJobOptions {
	return &RayJobOptions{
		PrintFlags: genericclioptions.NewPrintFlags("").WithTypeSetter(scheme.Scheme),
		IOStreams:  streams,
		Clock:      clock,
	}
}

func NewRayJobCmd(clientGetter util.ClientGetter, streams genericiooptions.IOStreams, clock clock.Clock) *cobra.Command {
	o := NewRayJobOptions(streams, clock)

	cmd := &cobra.Command{
		Use: "rayjob" +
			" [--profile PROFILE_NAME]" +
			" [--localqueue LOCALQUEUE_NAME]" +
			" [--selector key1=value1]" +
			" [--field-selector key1=value1]" +
			" [--all-namespaces]",
		DisableFlagsInUseLine: true,
		Short:                 "List RayJob",
		Example:               rayJobExample,
		Args:                  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			err := o.Complete(clientGetter)
			if err != nil {
				return err
			}
			return o.Run(cmd.Context())
		},
	}

	o.PrintFlags.AddFlags(cmd)

	util.AddAllNamespacesFlagVar(cmd, &o.AllNamespaces)
	util.AddFieldSelectorFlagVar(cmd, &o.FieldSelector)
	util.AddLabelSelectorFlagVar(cmd, &o.LabelSelector)
	util.AddProfileFlagVar(cmd, &o.ProfileFilter)
	util.AddLocalQueueFlagVar(cmd, &o.LocalQueueFilter)

	cobra.CheckErr(cmd.RegisterFlagCompletionFunc("profile", completion.ApplicationProfileNameFunc(clientGetter)))
	cobra.CheckErr(cmd.RegisterFlagCompletionFunc("localqueue", completion.LocalQueueNameFunc(clientGetter)))

	return cmd
}

// Complete completes all the required options
func (o *RayJobOptions) Complete(clientGetter util.ClientGetter) error {
	var err error

	o.Limit, err = listRequestLimit()
	if err != nil {
		return err
	}

	o.Namespace, _, err = clientGetter.ToRawKubeConfigLoader().Namespace()
	if err != nil {
		return err
	}

	clientset, err := clientGetter.RayClientset()
	if err != nil {
		return err
	}

	o.Client = clientset.RayV1()

	return nil
}

func (o *RayJobOptions) ToPrinter(headers bool) (printers.ResourcePrinterFunc, error) {
	if !o.PrintFlags.OutputFlagSpecified() {
		printer := newRayJobTablePrinter().
			WithNamespace(o.AllNamespaces).
			WithHeaders(headers).
			WithClock(o.Clock)
		return printer.PrintObj, nil
	}

	printer, err := o.PrintFlags.ToPrinter()
	if err != nil {
		return nil, err
	}

	return printer.PrintObj, nil
}

// Run performs the list operation.
func (o *RayJobOptions) Run(ctx context.Context) error {
	var totalCount int

	namespace := o.Namespace
	if o.AllNamespaces {
		namespace = ""
	}

	opts := metav1.ListOptions{
		FieldSelector: o.FieldSelector,
		Limit:         o.Limit,
	}

	if len(o.ProfileFilter) > 0 {
		opts.LabelSelector = fmt.Sprintf("%s=%s", constants.ProfileLabel, o.ProfileFilter)
	} else {
		opts.LabelSelector = constants.ProfileLabel
	}
	if len(o.LocalQueueFilter) > 0 {
		opts.LabelSelector = fmt.Sprintf("%s,%s=%s", opts.LabelSelector, kueueconstants.QueueLabel, o.LocalQueueFilter)
	}
	if len(o.LabelSelector) > 0 {
		opts.LabelSelector = fmt.Sprintf("%s,%s", opts.LabelSelector, o.LabelSelector)
	}

	tabWriter := printers.GetNewTabWriter(o.Out)

	for {
		headers := totalCount == 0

		list, err := o.Client.RayJobs(namespace).List(ctx, opts)
		if err != nil {
			return err
		}

		totalCount += len(list.Items)

		printer, err := o.ToPrinter(headers)
		if err != nil {
			return err
		}

		if err := printer.PrintObj(list, tabWriter); err != nil {
			return err
		}

		if list.Continue != "" {
			opts.Continue = list.Continue
			continue
		}

		if totalCount == 0 {
			if !o.AllNamespaces {
				fmt.Fprintf(o.ErrOut, "No resources found in %s namespace.\n", o.Namespace)
			} else {
				fmt.Fprintln(o.ErrOut, "No resources found")
			}
			return nil
		}

		if err := tabWriter.Flush(); err != nil {
			return err
		}

		return nil
	}
}

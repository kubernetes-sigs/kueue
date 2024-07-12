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

package cmd

import (
	"os"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/utils/clock"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd/completion"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd/create"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd/list"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd/util"
)

type KjobctlOptions struct {
	Clock       clock.Clock
	ConfigFlags *genericclioptions.ConfigFlags

	genericiooptions.IOStreams
}

func defaultConfigFlags() *genericclioptions.ConfigFlags {
	return genericclioptions.NewConfigFlags(true).WithDiscoveryQPS(50.0)
}

func NewDefaultKjobctlCmd() *cobra.Command {
	ioStreams := genericiooptions.IOStreams{In: os.Stdin, Out: os.Stdout, ErrOut: os.Stderr}
	return NewKjobctlCmd(KjobctlOptions{
		ConfigFlags: defaultConfigFlags().WithWarningPrinter(ioStreams),
		IOStreams:   ioStreams,
	})
}

func NewKjobctlCmd(o KjobctlOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "kjobctl",
		Short: "ML/AI/Batch Jobs Made Easy",
	}

	if o.Clock == nil {
		o.Clock = clock.RealClock{}
	}

	flags := cmd.PersistentFlags()

	configFlags := o.ConfigFlags
	if configFlags == nil {
		configFlags = defaultConfigFlags().WithWarningPrinter(o.IOStreams)
	}
	configFlags.AddFlags(flags)

	clientGetter := util.NewClientGetter(configFlags)

	cobra.CheckErr(cmd.RegisterFlagCompletionFunc("namespace", completion.NamespaceNameFunc(clientGetter)))
	cobra.CheckErr(cmd.RegisterFlagCompletionFunc("context", completion.ContextsFunc(clientGetter)))
	cobra.CheckErr(cmd.RegisterFlagCompletionFunc("cluster", completion.ClustersFunc(clientGetter)))
	cobra.CheckErr(cmd.RegisterFlagCompletionFunc("user", completion.UsersFunc(clientGetter)))

	cmd.AddCommand(create.NewCreateCmd(clientGetter, o.IOStreams, o.Clock))
	cmd.AddCommand(list.NewListCmd(clientGetter, o.IOStreams, o.Clock))

	return cmd
}

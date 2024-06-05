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

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd/util"
)

type KjobctlOptions struct {
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

	flags := cmd.PersistentFlags()

	configFlags := o.ConfigFlags
	if configFlags == nil {
		configFlags = defaultConfigFlags().WithWarningPrinter(o.IOStreams)
	}
	configFlags.AddFlags(flags)

	_ = util.NewClientGetter(configFlags)

	return cmd
}

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

package app

import (
	"os"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"

	"sigs.k8s.io/kueue/cmd/kueuectl/app/create"
	"sigs.k8s.io/kueue/cmd/kueuectl/app/resume"
	"sigs.k8s.io/kueue/cmd/kueuectl/app/stop"
)

type KueuectlOptions struct {
	ConfigFlags *genericclioptions.ConfigFlags

	genericiooptions.IOStreams
}

func defaultConfigFlags() *genericclioptions.ConfigFlags {
	return genericclioptions.NewConfigFlags(true).WithDiscoveryQPS(50.0)
}

func NewDefaultKueuectlCmd() *cobra.Command {
	ioStreams := genericiooptions.IOStreams{In: os.Stdin, Out: os.Stdout, ErrOut: os.Stderr}
	return NewKueuectlCmd(KueuectlOptions{
		ConfigFlags: defaultConfigFlags().WithWarningPrinter(ioStreams),
		IOStreams:   ioStreams,
	})
}

func NewKueuectlCmd(o KueuectlOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "kueue",
		Short: "Controls Kueue queueing manager",
	}

	flags := cmd.PersistentFlags()

	configFlags := o.ConfigFlags
	if configFlags == nil {
		configFlags = defaultConfigFlags().WithWarningPrinter(o.IOStreams)
	}
	configFlags.AddFlags(flags)

	cmd.AddCommand(create.NewCreateCmd(configFlags, o.IOStreams))
	cmd.AddCommand(resume.NewResumeCmd(configFlags, o.IOStreams))
	cmd.AddCommand(stop.NewStopCmd(configFlags, o.IOStreams))

	return cmd
}

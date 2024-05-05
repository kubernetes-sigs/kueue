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

package stop

import (
	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"

	"sigs.k8s.io/kueue/cmd/kueuectl/app/options"
)

const (
	wlLong = `Puts the given Workload on hold. The Workload will not be admitted and 
if it is already admitted it will be put back to queue just as if it 
was preempted (using .spec.active field).`
	wlExample = `  # Stop the workload 
  kueuectl stop workload my-workload`
)

func NewWorkloadCmd(clientGetter genericclioptions.RESTClientGetter, streams genericiooptions.IOStreams) *cobra.Command {
	o := options.NewUpdateWorkloadActivationOptions(streams, "stopped", false)

	cmd := &cobra.Command{
		Use: "workload NAME [--namespace NAMESPACE] [--dry-run STRATEGY]",
		// To do not add "[flags]" suffix on the end of usage line
		DisableFlagsInUseLine: true,
		Aliases:               []string{"wl"},
		Short:                 "Stop the Workload",
		Long:                  wlLong,
		Example:               wlExample,
		Args:                  cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		Run: func(cmd *cobra.Command, args []string) {
			cobra.CheckErr(o.Complete(clientGetter, cmd, args))
			cobra.CheckErr(o.Run(cmd.Context()))
		},
	}

	o.PrintFlags.AddFlags(cmd)

	return cmd
}

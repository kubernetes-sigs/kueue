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
	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/kubectl/pkg/util/templates"
	"k8s.io/utils/clock"

	"sigs.k8s.io/kueue/cmd/kueuectl/app/util"
)

var (
	listExample = templates.Examples(`
		# List LocalQueue
  		kueuectl list localqueue
	`)
)

func NewListCmd(clientGetter util.ClientGetter, streams genericiooptions.IOStreams, clock clock.Clock) *cobra.Command {
	cmd := &cobra.Command{
		Use:        "list",
		Short:      "Display resources",
		Example:    listExample,
		SuggestFor: []string{"ps"},
	}

	cmd.AddCommand(NewLocalQueueCmd(clientGetter, streams, clock))
	cmd.AddCommand(NewClusterQueueCmd(clientGetter, streams, clock))
	cmd.AddCommand(NewWorkloadCmd(clientGetter, streams, clock))
	cmd.AddCommand(NewResourceFlavorCmd(clientGetter, streams, clock))
	cmd.AddCommand(NewPodCmd(clientGetter, streams))

	return cmd
}

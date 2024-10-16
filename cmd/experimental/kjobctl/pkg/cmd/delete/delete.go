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
	"fmt"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/kubectl/pkg/util/templates"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd/util"
)

var (
	deleteExample = templates.Examples(
		fmt.Sprintf("%s\n\n%s\n\n%s\n\n%s", interactiveExample, jobExample, rayJobExample, rayClusterExample),
	)
)

func NewDeleteCmd(clientGetter util.ClientGetter, streams genericiooptions.IOStreams) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "delete",
		Short:   "Delete resources",
		Example: deleteExample,
	}

	cmd.AddCommand(NewInteractiveCmd(clientGetter, streams))
	cmd.AddCommand(NewJobCmd(clientGetter, streams))
	cmd.AddCommand(NewRayJobCmd(clientGetter, streams))
	cmd.AddCommand(NewRayClusterCmd(clientGetter, streams))
	cmd.AddCommand(NewSlurmCmd(clientGetter, streams))

	return cmd
}

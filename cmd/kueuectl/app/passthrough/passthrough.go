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

package passthrough

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericiooptions"

	"sigs.k8s.io/kueue/cmd/kueuectl/app/delete"
	"sigs.k8s.io/kueue/cmd/kueuectl/app/util"
)

type passThroughCommand struct {
	name  string
	short string
}

type passThroughType struct {
	name    string
	aliases []string
}

var (
	passThroughCommands = []passThroughCommand{
		{name: "get", short: "Display a resource"},
		{name: "delete", short: "Delete a resource"},
		{name: "edit", short: "Edit a resource on the server"},
		{name: "describe", short: "Show details of a resource"},
		{name: "patch", short: "Update fields of a resource"},
	}

	passThroughTypes = []passThroughType{
		{name: "workload", aliases: []string{"wl"}},
		{name: "clusterqueue", aliases: []string{"cq"}},
		{name: "localqueue", aliases: []string{"lq"}},
		{name: "resourceflavor", aliases: []string{"rf"}},
	}
)

func NewCommands(clientGetter util.ClientGetter, streams genericiooptions.IOStreams) []*cobra.Command {
	commands := make([]*cobra.Command, len(passThroughCommands))
	for i, ptCmd := range passThroughCommands {
		commands[i] = newCommand(clientGetter, streams, ptCmd, passThroughTypes)
	}
	return commands
}

func newCommand(
	clientGetter util.ClientGetter,
	streams genericiooptions.IOStreams,
	command passThroughCommand,
	ptTypes []passThroughType,
) *cobra.Command {
	cmd := &cobra.Command{
		Use:   fmt.Sprintf("%s [command]", command.name),
		Short: command.short,
	}
	for _, ptType := range ptTypes {
		if command.name == "delete" && ptType.name == "workload" {
			cmd.AddCommand(delete.NewWorkloadCmd(clientGetter, streams))
		} else {
			cmd.AddCommand(newSubcommand(command, ptType))
		}
	}

	return cmd
}

func newSubcommand(command passThroughCommand, ptType passThroughType) *cobra.Command {
	cmd := &cobra.Command{
		Use:                ptType.name,
		Aliases:            ptType.aliases,
		Short:              fmt.Sprintf("Pass-through \"%s %s\" to kubectl", command.name, ptType.name),
		FParseErrWhitelist: cobra.FParseErrWhitelist{UnknownFlags: true},
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true

			kubectlPath, err := exec.LookPath("kubectl")
			if err != nil {
				return fmt.Errorf("pass-through command are not available: %w, PATH=%q", err, os.Getenv("PATH"))
			}

			// prepare the args
			args := os.Args
			args[0] = kubectlPath

			// go in kubectl
			return syscall.Exec(kubectlPath, args, os.Environ())
		},
	}
	return cmd
}

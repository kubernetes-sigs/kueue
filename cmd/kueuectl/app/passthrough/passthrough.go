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
)

type passThroughType struct {
	name    string
	aliases []string
}

var (
	passThroughCmds  = []string{"get", "delete", "edit", "describe", "patch"}
	passThroughTypes = []passThroughType{
		{name: "workload", aliases: []string{"wl"}},
		{name: "clusterqueue", aliases: []string{"cq"}},
		{name: "localqueue", aliases: []string{"lq"}}}
)

func NewCommands() ([]*cobra.Command, error) {
	kubectlPath, err := exec.LookPath("kubectl")
	if err != nil {
		return nil, fmt.Errorf("pass-through commands are not available: %w, PATH=%q", err, os.Getenv("PATH"))
	}

	commands := make([]*cobra.Command, len(passThroughCmds))
	for i, pCmd := range passThroughCmds {
		commands[i] = newCmd(kubectlPath, pCmd, passThroughTypes)
	}
	return commands, nil
}

func newCmd(kubectlPath string, command string, ptTypes []passThroughType) *cobra.Command {
	cmd := &cobra.Command{
		Use:   command,
		Short: fmt.Sprintf("Pass-through %q to kubectl", command),
	}

	for _, ptType := range ptTypes {
		cmd.AddCommand(newSubcommand(kubectlPath, command, ptType))
	}

	return cmd
}

func newSubcommand(kubectlPath string, command string, ptType passThroughType) *cobra.Command {
	cmd := &cobra.Command{
		Use:                ptType.name,
		Aliases:            ptType.aliases,
		Short:              fmt.Sprintf("Pass-through \"%s  %s\" to kubectl", command, ptType),
		FParseErrWhitelist: cobra.FParseErrWhitelist{UnknownFlags: true},
		RunE: func(_ *cobra.Command, _ []string) error {
			// prepare the args
			args := os.Args
			args[0] = kubectlPath

			// go in kubectl
			return syscall.Exec(kubectlPath, args, os.Environ())
		},
	}
	return cmd
}

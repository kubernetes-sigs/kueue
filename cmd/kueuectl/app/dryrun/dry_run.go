/*
Copyright The Kubernetes Authors.

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

package dryrun

import (
	"fmt"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"
)

type Strategy int

const (
	// None indicates the client will make all mutating calls
	None Strategy = iota

	// Client or client-side dry-run, indicates the client will prevent
	// making mutating calls such as CREATE, PATCH, and DELETE
	Client

	// Server or server-side dry-run, indicates the client will send
	// mutating calls to the APIServer with the dry-run parameter to prevent
	// persisting changes.
	//
	// Note that clients sending server-side dry-run calls should verify that
	// the APIServer and the resource supports server-side dry-run, and otherwise
	// clients should fail early.
	//
	// If a client sends a server-side dry-run call to an APIServer that doesn't
	// support server-side dry-run, then the APIServer will persist changes inadvertently.
	Server
)

func GetStrategy(cmd *cobra.Command) (Strategy, error) {
	dryRunFlag, err := cmd.Flags().GetString("dry-run")
	if err != nil {
		return None, err
	}
	switch dryRunFlag {
	case "client":
		return Client, nil
	case "server":
		return Server, nil
	case "none":
		return None, nil
	default:
		return None, fmt.Errorf(`invalid dry-run value (%v). Must be "none", "server", or "client"`, dryRunFlag)
	}
}

// PrintFlagsWithStrategy sets a success message at print time for the dry run strategy
func PrintFlagsWithStrategy(printFlags *genericclioptions.PrintFlags, strategy Strategy) error {
	switch strategy {
	case Client:
		if err := printFlags.Complete("%s (client dry run)"); err != nil {
			return err
		}
	case Server:
		if err := printFlags.Complete("%s (server dry run)"); err != nil {
			return err
		}
	}
	return nil
}

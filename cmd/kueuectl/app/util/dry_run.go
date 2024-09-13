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

package util

import (
	"fmt"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"
)

type DryRunStrategy int

const (
	// DryRunNone indicates the client will make all mutating calls
	DryRunNone DryRunStrategy = iota

	// DryRunClient or client-side dry-run, indicates the client will prevent
	// making mutating calls such as CREATE, PATCH, and DELETE
	DryRunClient

	// DryRunServer or server-side dry-run, indicates the client will send
	// mutating calls to the APIServer with the dry-run parameter to prevent
	// persisting changes.
	//
	// Note that clients sending server-side dry-run calls should verify that
	// the APIServer and the resource supports server-side dry-run, and otherwise
	// clients should fail early.
	//
	// If a client sends a server-side dry-run call to an APIServer that doesn't
	// support server-side dry-run, then the APIServer will persist changes inadvertently.
	DryRunServer
)

// AddDryRunFlag adds dry-run flag to a command.
func AddDryRunFlag(cmd *cobra.Command) {
	cmd.PersistentFlags().String(
		"dry-run",
		"none",
		`Must be "none", "server", or "client". If client strategy, only print the object that would be sent, without sending it. If server strategy, submit server-side request without persisting the resource.`,
	)
}

func GetDryRunStrategy(cmd *cobra.Command) (DryRunStrategy, error) {
	dryRunFlag, err := cmd.Flags().GetString("dry-run")
	if err != nil {
		return DryRunNone, err
	}
	switch dryRunFlag {
	case "client":
		return DryRunClient, nil
	case "server":
		return DryRunServer, nil
	case "none":
		return DryRunNone, nil
	default:
		return DryRunNone, fmt.Errorf(`Invalid dry-run value (%v). Must be "none", "server", or "client".`, dryRunFlag)
	}
}

// PrintFlagsWithDryRunStrategy sets a success message at print time for the dry run strategy
func PrintFlagsWithDryRunStrategy(printFlags *genericclioptions.PrintFlags, dryRunStrategy DryRunStrategy) error {
	switch dryRunStrategy {
	case DryRunClient:
		if err := printFlags.Complete("%s (client dry run)"); err != nil {
			return err
		}
	case DryRunServer:
		if err := printFlags.Complete("%s (server dry run)"); err != nil {
			return err
		}
	}
	return nil
}

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

package version

import (
	"fmt"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/client-go/discovery"

	"sigs.k8s.io/kueue/pkg/version"
)

const (
	versionExample = `  # Prints the client version
  kueuectl version`
)

// Options is a struct to support version command
type Options struct {
	DiscoveryClient discovery.CachedDiscoveryInterface

	genericiooptions.IOStreams
}

// NewOptions returns initialized Options
func NewOptions(ioStreams genericiooptions.IOStreams) *Options {
	return &Options{
		IOStreams: ioStreams,
	}
}

// NewVersionCmd returns a new cobra.Command for fetching version
func NewVersionCmd(streams genericiooptions.IOStreams) *cobra.Command {
	o := NewOptions(streams)

	cmd := &cobra.Command{
		Use:     "version",
		Short:   "Prints the client version",
		Example: versionExample,
		Args:    cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			cobra.CheckErr(o.Run())
		},
	}

	return cmd
}

// Run executes version command
func (o *Options) Run() error {
	fmt.Fprintf(o.Out, "Client Version: %s\n", version.GitVersion)
	return nil
}

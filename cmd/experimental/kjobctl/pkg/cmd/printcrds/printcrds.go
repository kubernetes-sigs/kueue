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

package printcrds

import (
	"bytes"
	"compress/gzip"
	"io"

	"github.com/spf13/cobra"
	"k8s.io/kubectl/pkg/util/templates"

	_ "embed"
)

var (
	crdsExample = templates.Examples(`
		# Install or update the kjobctl CRDs 
  		kjobctl printcrds | kubectl apply --server-side -f -

		# Remove the kjobctl CRDs
  		kjobctl printcrds | kubectl delete --ignore-not-found=true -f -
	`)
)

//go:embed embed/manifest.gz
var crds []byte

func NewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "printcrds",
		Short:        "Print the kjobctl CRDs",
		Example:      crdsExample,
		SilenceUsage: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			reader, err := gzip.NewReader(bytes.NewReader(crds))
			if err != nil {
				return err
			}
			_, err = io.Copy(cmd.OutOrStdout(), reader)
			return err
		},
	}
	return cmd
}

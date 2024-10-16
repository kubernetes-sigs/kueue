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
	"context"
	"fmt"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	k8s "k8s.io/client-go/kubernetes"
	"k8s.io/kubectl/pkg/util/templates"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/cmd/kueuectl/app/util"
	"sigs.k8s.io/kueue/pkg/version"
)

const (
	kueueNamespace             = "kueue-system"
	kueueControllerManagerName = "kueue-controller-manager"
	kueueContainerName         = "manager"
)

var (
	versionExample = templates.Examples(`
		# Prints the client version and the kueue controller manager image, if installed
  		kueuectl version
	`)
)

// VersionOptions is a struct to support version command
type VersionOptions struct {
	K8sClientset k8s.Interface

	genericiooptions.IOStreams
}

// NewOptions returns initialized Options
func NewOptions(streams genericiooptions.IOStreams) *VersionOptions {
	return &VersionOptions{
		IOStreams: streams,
	}
}

// NewVersionCmd returns a new cobra.Command for fetching version
func NewVersionCmd(clientGetter util.ClientGetter, streams genericiooptions.IOStreams) *cobra.Command {
	o := NewOptions(streams)

	cmd := &cobra.Command{
		Use:                   "version",
		Short:                 "Prints the client version and the kueue controller manager image, if installed",
		Example:               versionExample,
		Args:                  cobra.NoArgs,
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			err := o.Complete(clientGetter)
			if err != nil {
				return err
			}
			return o.Run(cmd.Context())
		},
	}

	return cmd
}

func (o *VersionOptions) Complete(clientGetter util.ClientGetter) error {
	var err error

	o.K8sClientset, err = clientGetter.K8sClientSet()
	if err != nil {
		return err
	}

	return nil
}

// Run executes version command
func (o *VersionOptions) Run(ctx context.Context) error {
	fmt.Fprintf(o.Out, "Client Version: %s\n", version.GitVersion)

	deployment, err := o.K8sClientset.AppsV1().Deployments(kueueNamespace).Get(ctx, kueueControllerManagerName, metav1.GetOptions{})
	if err != nil {
		return client.IgnoreNotFound(err)
	}

	for _, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name == kueueContainerName {
			fmt.Fprintf(o.Out, "Kueue Controller Manager Image: %s\n", container.Image)
			break
		}
	}

	return nil
}

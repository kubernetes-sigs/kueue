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

package v1beta2

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func TestClusterProfileCredentialsProviderAlias(t *testing.T) {
	acceptAccessProviders := func(providers []ClusterProfileAccessProvider) []ClusterProfileAccessProvider {
		return providers
	}
	acceptCredentialsProviders := func(providers []ClusterProfileCredentialsProvider) []ClusterProfileCredentialsProvider {
		return providers
	}

	credentialsProviders := []ClusterProfileCredentialsProvider{
		{
			Name: "secretreader",
			ExecConfig: clientcmdapi.ExecConfig{
				Command: "command",
			},
		},
	}
	accessProviders := acceptAccessProviders(credentialsProviders)
	credentialsProviders = acceptCredentialsProviders(accessProviders)

	if diff := cmp.Diff([]ClusterProfileAccessProvider{
		{
			Name: "secretreader",
			ExecConfig: clientcmdapi.ExecConfig{
				Command: "command",
			},
		},
	}, credentialsProviders); diff != "" {
		t.Errorf("Unexpected providers (-want,+got):\n%s", diff)
	}
}

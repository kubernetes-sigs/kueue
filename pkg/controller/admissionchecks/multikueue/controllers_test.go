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

package multikueue

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
)

func TestClusterProfileAccessProviders(t *testing.T) {
	accessProvider := func(name, command string) configapi.ClusterProfileAccessProvider {
		return configapi.ClusterProfileAccessProvider{
			Name: name,
			ExecConfig: clientcmdapi.ExecConfig{
				Command: command,
			},
		}
	}
	credentialsProvider := func(name, command string) configapi.ClusterProfileCredentialsProvider { //nolint:staticcheck //SA1019: CredentialsProviders is tested for backward compatibility.
		provider := accessProvider(name, command)
		return configapi.ClusterProfileCredentialsProvider(provider) //nolint:staticcheck //SA1019: CredentialsProviders is tested for backward compatibility.
	}

	testCases := map[string]struct {
		clusterProfileConfig *configapi.ClusterProfile
		want                 []configapi.ClusterProfileAccessProvider
	}{
		"nil": {},
		"only deprecated credentialsProviders": {
			clusterProfileConfig: &configapi.ClusterProfile{
				CredentialsProviders: []configapi.ClusterProfileCredentialsProvider{ //nolint:staticcheck //SA1019: CredentialsProviders is tested for backward compatibility.
					credentialsProvider("secretreader", "deprecated-command"),
				},
			},
			want: []configapi.ClusterProfileAccessProvider{
				accessProvider("secretreader", "deprecated-command"),
			},
		},
		"accessProviders take precedence": {
			clusterProfileConfig: &configapi.ClusterProfile{
				AccessProviders: []configapi.ClusterProfileAccessProvider{
					accessProvider("secretreader", "access-command"),
					accessProvider("kubelogin", "kubelogin-command"),
				},
				CredentialsProviders: []configapi.ClusterProfileCredentialsProvider{ //nolint:staticcheck //SA1019: CredentialsProviders is tested for backward compatibility.
					credentialsProvider("secretreader", "deprecated-command"),
					credentialsProvider("legacy", "legacy-command"),
				},
			},
			want: []configapi.ClusterProfileAccessProvider{
				accessProvider("secretreader", "access-command"),
				accessProvider("kubelogin", "kubelogin-command"),
				accessProvider("legacy", "legacy-command"),
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, clusterProfileAccessProviders(tc.clusterProfileConfig)); diff != "" {
				t.Errorf("Unexpected providers (-want,+got):\n%s", diff)
			}
		})
	}
}

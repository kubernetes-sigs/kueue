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

func TestClusterProfileAccessProviders(t *testing.T) {
	accessProvider := func(name, command string) ClusterProfileAccessProvider {
		return ClusterProfileAccessProvider{
			Name: name,
			ExecConfig: clientcmdapi.ExecConfig{
				Command: command,
			},
		}
	}
	credentialsProvider := func(name, command string) ClusterProfileCredentialsProvider {
		return accessProvider(name, command)
	}

	testCases := map[string]struct {
		clusterProfileConfig *ClusterProfile
		want                 []ClusterProfileAccessProvider
	}{
		"nil": {},
		"only deprecated credentialsProviders": {
			clusterProfileConfig: &ClusterProfile{
				CredentialsProviders: []ClusterProfileCredentialsProvider{
					credentialsProvider("secretreader", "deprecated-command"),
				},
			},
			want: []ClusterProfileAccessProvider{
				accessProvider("secretreader", "deprecated-command"),
			},
		},
		"accessProviders take precedence": {
			clusterProfileConfig: &ClusterProfile{
				AccessProviders: []ClusterProfileAccessProvider{
					accessProvider("secretreader", "access-command"),
					accessProvider("kubelogin", "kubelogin-command"),
				},
				CredentialsProviders: []ClusterProfileCredentialsProvider{
					credentialsProvider("secretreader", "deprecated-command"),
					credentialsProvider("legacy", "legacy-command"),
				},
			},
			want: []ClusterProfileAccessProvider{
				accessProvider("secretreader", "access-command"),
				accessProvider("kubelogin", "kubelogin-command"),
				accessProvider("legacy", "legacy-command"),
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, ClusterProfileAccessProviders(tc.clusterProfileConfig)); diff != "" {
				t.Errorf("Unexpected providers (-want,+got):\n%s", diff)
			}
		})
	}
}

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

func TestSetDefaultsConfigurationClusterProfileAccessProvidersIdempotent(t *testing.T) {
	cfg := &Configuration{
		MultiKueue: &MultiKueue{
			ClusterProfile: &ClusterProfile{
				AccessProviders: []ClusterProfileAccessProvider{
					{
						Name: "shared",
					},
				},
				CredentialsProviders: []ClusterProfileCredentialsProvider{
					{
						Name: "shared",
					},
					{
						Name: "legacy",
					},
				},
			},
		},
	}

	SetDefaults_Configuration(cfg)
	SetDefaults_Configuration(cfg)

	want := []ClusterProfileAccessProvider{
		{
			Name: "shared",
		},
		{
			Name: "legacy",
		},
	}
	if diff := cmp.Diff(want, cfg.MultiKueue.ClusterProfile.AccessProviders); diff != "" {
		t.Errorf("Unexpected providers (-want,+got):\n%s", diff)
	}
}

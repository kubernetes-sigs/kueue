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

package visibility

import (
	"net"
	"testing"

	"github.com/google/go-cmp/cmp"
	genericoptions "k8s.io/apiserver/pkg/server/options"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
)

func TestApplyVisibilityServerSecureServingOptions(t *testing.T) {
	bindAddress := "127.0.0.1"
	bindPort := int32(9444)

	tests := map[string]struct {
		cfg         *configapi.Configuration
		wantOptions *genericoptions.SecureServingOptionsWithLoopback
	}{
		"default visibility server config": {
			cfg:         &configapi.Configuration{},
			wantOptions: wantSecureServingOptions("", int(configapi.DefaultVisibilityBindPort)),
		},
		"custom bind port": {
			cfg: &configapi.Configuration{
				VisibilityServer: &configapi.VisibilityServerConfiguration{
					BindPort: &bindPort,
				},
			},
			wantOptions: wantSecureServingOptions("", 9444),
		},
		"custom bind address": {
			cfg: &configapi.Configuration{
				VisibilityServer: &configapi.VisibilityServerConfiguration{
					BindAddress: &bindAddress,
				},
			},
			wantOptions: wantSecureServingOptions("127.0.0.1", int(configapi.DefaultVisibilityBindPort)),
		},
		"custom bind address and port": {
			cfg: &configapi.Configuration{
				VisibilityServer: &configapi.VisibilityServerConfiguration{
					BindAddress: &bindAddress,
					BindPort:    &bindPort,
				},
			},
			wantOptions: wantSecureServingOptions("127.0.0.1", 9444),
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			options := genericoptions.NewSecureServingOptions().WithLoopback()

			applyVisibilityServerSecureServingOptions(options, tc.cfg)

			if diff := cmp.Diff(tc.wantOptions, options); diff != "" {
				t.Errorf("Unexpected options (-want,+got):\n%s", diff)
			}
		})
	}
}

func wantSecureServingOptions(bindAddress string, bindPort int) *genericoptions.SecureServingOptionsWithLoopback {
	options := genericoptions.NewSecureServingOptions().WithLoopback()
	options.BindPort = bindPort

	if bindAddress != "" {
		options.BindAddress = net.ParseIP(bindAddress)
	}
	return options
}

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
	"strings"
	"testing"

	"k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func TestValidateRestConfigSecurityBoundaries(t *testing.T) {
	tests := map[string]struct {
		config  *rest.Config
		opts    validateRestConfigOptions
		wantErr string
	}{
		"nil config": {
			wantErr: "REST config is nil",
		},
		"HTTP endpoint": {
			config:  &rest.Config{Host: "http://api.example.com"},
			wantErr: "untrusted server endpoint",
		},
		"endpoint with user info": {
			config:  &rest.Config{Host: "https://user:password@api.example.com"},
			wantErr: "untrusted server endpoint",
		},
		"invalid endpoint hostname": {
			config:  &rest.Config{Host: "https://invalid_host.example.com"},
			wantErr: "untrusted server endpoint",
		},
		"insecure TLS": {
			config: &rest.Config{
				Host: "https://api.example.com",
				TLSClientConfig: rest.TLSClientConfig{
					Insecure: true,
				},
			},
			wantErr: "insecure TLS verification is not allowed",
		},
		"CA file": {
			config: &rest.Config{
				Host: "https://api.example.com",
				TLSClientConfig: rest.TLSClientConfig{
					CAFile: "/tmp/ca.crt",
				},
			},
			wantErr: "CAFile is not allowed",
		},
		"client certificate file": {
			config: &rest.Config{
				Host: "https://api.example.com",
				TLSClientConfig: rest.TLSClientConfig{
					CertFile: "/tmp/client.crt",
				},
			},
			wantErr: "CertFile is not allowed",
		},
		"client key file": {
			config: &rest.Config{
				Host: "https://api.example.com",
				TLSClientConfig: rest.TLSClientConfig{
					KeyFile: "/tmp/client.key",
				},
			},
			wantErr: "KeyFile is not allowed",
		},
		"inline token and TLS credentials": {
			config: &rest.Config{
				Host:        "HTTPS://API.EXAMPLE.COM:6443",
				BearerToken: "token",
				TLSClientConfig: rest.TLSClientConfig{
					CAData:   []byte("ca"),
					CertData: []byte("certificate"),
					KeyData:  []byte("key"),
				},
			},
		},
		"HTTPS IPv6 endpoint": {
			config: &rest.Config{Host: "https://[2001:db8::1]:6443"},
		},
		"allowed exec provider": {
			config: &rest.Config{
				Host: "https://api.example.com",
				ExecProvider: &clientcmdapi.ExecConfig{
					Command: "credential-provider",
				},
			},
			opts: validateRestConfigOptions{allowExecProvider: true},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			err := validateRestConfig(tc.config, tc.opts)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validateRestConfig() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validateRestConfig() error = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

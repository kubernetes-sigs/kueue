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
	"flag"
	"os"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	genericoptions "k8s.io/apiserver/pkg/server/options"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
)

func TestCreateVisibilityServerOptions(t *testing.T) {
	enableInternalCertManagement := true
	disableInternalCertManagement := false
	bindAddress := "127.0.0.1"
	bindPort := int32(8080)

	tests := []struct {
		name string
		cfg  *configapi.Configuration
		want wantVisibilityServerOptions
	}{
		{
			name: "default visibility server config",
			cfg: &configapi.Configuration{
				InternalCertManagement: &configapi.InternalCertManagement{
					Enable: &enableInternalCertManagement,
				},
			},
			want: wantVisibilityServerOptions{
				BindAddress:   "0.0.0.0",
				BindPort:      int(configapi.DefaultVisibilityBindPort),
				CertDirectory: true,
			},
		},
		{
			name: "custom bind address with default port",
			cfg: &configapi.Configuration{
				InternalCertManagement: &configapi.InternalCertManagement{
					Enable: &enableInternalCertManagement,
				},
				VisibilityServer: &configapi.VisibilityServerConfiguration{
					BindAddress: &bindAddress,
				},
			},
			want: wantVisibilityServerOptions{
				BindAddress:   "127.0.0.1",
				BindPort:      int(configapi.DefaultVisibilityBindPort),
				CertDirectory: true,
			},
		},
		{
			name: "custom bind port",
			cfg: &configapi.Configuration{
				InternalCertManagement: &configapi.InternalCertManagement{
					Enable: &enableInternalCertManagement,
				},
				VisibilityServer: &configapi.VisibilityServerConfiguration{
					BindPort: &bindPort,
				},
			},
			want: wantVisibilityServerOptions{
				BindAddress:   "0.0.0.0",
				BindPort:      int(bindPort),
				CertDirectory: true,
			},
		},
		{
			name: "custom bind address",
			cfg: &configapi.Configuration{
				InternalCertManagement: &configapi.InternalCertManagement{
					Enable: &enableInternalCertManagement,
				},
				VisibilityServer: &configapi.VisibilityServerConfiguration{
					BindAddress: &bindAddress,
					BindPort:    &bindPort,
				},
			},
			want: wantVisibilityServerOptions{
				BindAddress:   "127.0.0.1",
				BindPort:      int(bindPort),
				CertDirectory: true,
			},
		},
		{
			name: "external certificate files",
			cfg: &configapi.Configuration{
				InternalCertManagement: &configapi.InternalCertManagement{
					Enable: &disableInternalCertManagement,
				},
				VisibilityServer: &configapi.VisibilityServerConfiguration{
					BindAddress: &bindAddress,
					BindPort:    &bindPort,
				},
			},
			want: wantVisibilityServerOptions{
				BindAddress:   "127.0.0.1",
				BindPort:      int(bindPort),
				CertDirectory: false,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setVisibilityServerTestGlobals(t)

			gotOptions := createVisibilityServerOptions(tc.cfg)
			got := collectVisibilityServerOptions(gotOptions)

			if tc.want.CertDirectory {
				tc.want.CertDirValue = certDir
			} else {
				tc.want.CertDirValue = "apiserver.local.config/certificates"
				tc.want.CertFile = certDir + "/tls.crt"
				tc.want.KeyFile = certDir + "/tls.key"
			}

			// Remove the CertDirectory boolean from comparison
			ignoreCertDirectoryBool := cmpopts.IgnoreFields(wantVisibilityServerOptions{}, "CertDirectory")

			if diff := cmp.Diff(tc.want, got, ignoreCertDirectoryBool); diff != "" {
				t.Errorf("Unexpected options (-want,+got):\n%s", diff)
			}
		})
	}
}

type wantVisibilityServerOptions struct {
	BindAddress   string
	BindPort      int
	CertDirectory bool
	CertDirValue  string
	CertFile      string
	KeyFile       string
}

func collectVisibilityServerOptions(o *genericoptions.RecommendedOptions) wantVisibilityServerOptions {
	return wantVisibilityServerOptions{
		BindAddress:  o.SecureServing.BindAddress.String(),
		BindPort:     o.SecureServing.BindPort,
		CertDirValue: o.SecureServing.ServerCert.CertDirectory,
		CertFile:     o.SecureServing.ServerCert.CertKey.CertFile,
		KeyFile:      o.SecureServing.ServerCert.CertKey.KeyFile,
	}
}

func setVisibilityServerTestGlobals(t *testing.T) {
	t.Helper()

	oldCertDir := certDir
	certDir = t.TempDir()
	t.Cleanup(func() {
		certDir = oldCertDir
	})

	kubeConfigPath := t.TempDir() + "/kubeconfig"
	if err := os.WriteFile(kubeConfigPath, []byte("dummy kubeconfig"), 0600); err != nil {
		t.Fatalf("Writing kubeconfig: %v", err)
	}

	kubeConfigFlag := flag.Lookup("kubeconfig")
	if kubeConfigFlag == nil {
		flag.String("kubeconfig", kubeConfigPath, "")
		return
	}

	oldKubeConfigPath := kubeConfigFlag.Value.String()
	if err := kubeConfigFlag.Value.Set(kubeConfigPath); err != nil {
		t.Fatalf("Setting kubeconfig flag: %v", err)
	}
	t.Cleanup(func() {
		if err := kubeConfigFlag.Value.Set(oldKubeConfigPath); err != nil {
			t.Fatalf("Restoring kubeconfig flag: %v", err)
		}
	})
}

/*
Copyright 2021 The Kubernetes Authors.

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

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApply(t *testing.T) {
	// temp dir
	tmpDir, err := os.MkdirTemp("", "temp")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	portOverWriteConfig := filepath.Join(tmpDir, "port-overwrite.yaml")
	if err := os.WriteFile(portOverWriteConfig, []byte(`
apiVersion: config.kueue.x-k8s.io/v1alpha1
kind: Configuration
leaderElection:
  leaderElect: true
webhook:
  port: 9444
`), os.FileMode(0600)); err != nil {
		t.Fatal(err)
	}

	certOverWriteConfig := filepath.Join(tmpDir, "cert-overwrite.yaml")
	if err := os.WriteFile(certOverWriteConfig, []byte(`
apiVersion: config.kueue.x-k8s.io/v1alpha1
kind: Configuration
leaderElection:
  leaderElect: true
enableInternalCertManagement: false
`), os.FileMode(0600)); err != nil {
		t.Fatal(err)
	}

	testcases := []struct {
		name                             string
		configFile                       string
		wantPort                         int
		wantEnableInternalCertManagement bool
	}{
		{
			name:                             "default config",
			configFile:                       "",
			wantPort:                         9443,
			wantEnableInternalCertManagement: true,
		},
		{
			name:                             "port overwrite config",
			configFile:                       portOverWriteConfig,
			wantPort:                         9444,
			wantEnableInternalCertManagement: true,
		},
		{
			name:                             "cert overwrite config",
			configFile:                       certOverWriteConfig,
			wantPort:                         9443,
			wantEnableInternalCertManagement: false,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			options, config := apply(tc.configFile)

			if options.Port != tc.wantPort {
				t.Fatalf("got unexpected port, want: %v, got: %v", tc.wantPort, options.Port)
			}

			if *config.EnableInternalCertManagement != tc.wantEnableInternalCertManagement {
				t.Fatalf("got unexpected enableInternalCertManagement, want: %v, got: %v", tc.wantEnableInternalCertManagement, *config.EnableInternalCertManagement)
			}
		})
	}
}

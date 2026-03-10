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

package kubeconfig

import (
	"flag"
	"os"
	"testing"
)

func TestResolvePath(t *testing.T) {
	tests := []struct {
		name           string
		envKubeconfig  string
		flagKubeconfig string
		wantPath       string
	}{
		{
			name:     "none set",
			wantPath: "",
		},
		{
			name:          "env set",
			envKubeconfig: "/path/from/env",
			wantPath:      "/path/from/env",
		},
		{
			name:           "flag set",
			flagKubeconfig: "/path/from/flag",
			wantPath:       "/path/from/flag",
		},
		{
			name:           "both set, flag wins",
			envKubeconfig:  "/path/from/env",
			flagKubeconfig: "/path/from/flag",
			wantPath:       "/path/from/flag",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Save and restore
			oldEnv := os.Getenv("KUBECONFIG")
			defer os.Setenv("KUBECONFIG", oldEnv)

			// Handle flag
			f := flag.Lookup("kubeconfig")
			var oldFlagValue string
			if f != nil {
				oldFlagValue = f.Value.String()
				defer f.Value.Set(oldFlagValue)
			} else {
				// Register if not present, though in kueue it should be there via controller-runtime
				flag.String("kubeconfig", "", "Paths to a kubeconfig. Only required if out-of-cluster.")
				f = flag.Lookup("kubeconfig")
			}

			// Set values for test
			os.Setenv("KUBECONFIG", tc.envKubeconfig)
			if f != nil {
				f.Value.Set(tc.flagKubeconfig)
			}

			gotPath := resolvePath()
			if gotPath != tc.wantPath {
				t.Errorf("resolvePath() = %q, want %q", gotPath, tc.wantPath)
			}
		})
	}
}

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

package app

import (
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"testing"

	"k8s.io/component-base/featuregate"

	"sigs.k8s.io/kueue/pkg/features"
)

func TestRun(t *testing.T) {
	validKubeconfig := `apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://127.0.0.1:6443
  name: test
contexts:
- context:
    cluster: test
    user: test
  name: test
current-context: test
users:
- name: test
  user:
    token: test-token
`

	tests := map[string]struct {
		configYAML   string
		missingCfg   bool
		kubeconfig   string
		extraArgs    []string
		featureGates map[featuregate.Feature]bool
		wantTargetIs error
	}{
		"invalid flag": {
			extraArgs: []string{"--unknown-flag"},
		},
		"help flag": {
			extraArgs:    []string{"--help"},
			wantTargetIs: flag.ErrHelp,
		},
		"missing config file": {
			missingCfg:   true,
			wantTargetIs: os.ErrNotExist,
		},
		"invalid config YAML": {
			configYAML: "not: [valid: yaml",
		},
		"conflicting feature gates": {
			configYAML: `apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
featureGates:
  TopologyAwareScheduling: true
`,
			extraArgs: []string{"--feature-gates=TopologyAwareScheduling=false"},
		},
		"invalid configuration": {
			configYAML: `apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
waitForPodsReady:
  enable: true
  timeout: -1s
`,
		},
		"TLSOptions enabled with invalid TLS options": {
			featureGates: map[featuregate.Feature]bool{
				features.TLSOptions: true,
			},
			configYAML: `apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
tls:
  minVersion: InvalidVersion
`,
		},
		"CustomMetricLabels enabled with custom labels": {
			featureGates: map[featuregate.Feature]bool{
				features.CustomMetricLabels: true,
			},
			configYAML: `apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
internalCertManagement:
  enable: true
metrics:
  customLabels:
  - name: team
    sourceLabelKey: team
`,
		},
		"CustomMetricLabels disabled with custom labels": {
			featureGates: map[featuregate.Feature]bool{
				features.CustomMetricLabels: false,
			},
			configYAML: `apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
internalCertManagement:
  enable: true
metrics:
  customLabels:
  - name: team
    sourceLabelKey: team
`,
		},
		"missing metrics certificates": {
			configYAML: `apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
internalCertManagement:
  enable: false
`,
			wantTargetIs: os.ErrNotExist,
		},
		"missing kubeconfig": {
			configYAML: `apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
internalCertManagement:
  enable: true
`,
		},
		"kubeconfig flag accepted": {
			extraArgs: []string{"--kubeconfig", filepath.Join(t.TempDir(), "nonexistent-kubeconfig")},
			configYAML: `apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
internalCertManagement:
  enable: true
`,
			wantTargetIs: os.ErrNotExist,
		},
		"rate limiter disabled via negative QPS": {
			kubeconfig: validKubeconfig,
			configYAML: `apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
internalCertManagement:
  enable: false
clientConnection:
  qps: -1
  burst: 30
`,
			wantTargetIs: os.ErrNotExist,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("KUBERNETES_SERVICE_HOST", "")
			t.Setenv("KUBERNETES_SERVICE_PORT", "")
			if tc.kubeconfig != "" {
				kcPath := filepath.Join(t.TempDir(), "kubeconfig")
				if err := os.WriteFile(kcPath, []byte(tc.kubeconfig), 0o600); err != nil {
					t.Fatalf("failed to write temp kubeconfig: %v", err)
				}
				t.Setenv("KUBECONFIG", kcPath)
			} else {
				t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "nonexistent-kubeconfig"))
			}

			for fg, enabled := range tc.featureGates {
				features.SetFeatureGateDuringTest(t, fg, enabled)
			}

			var args []string
			if tc.missingCfg {
				args = append(args, "--config", filepath.Join(t.TempDir(), "missing-config.yaml"))
			} else if tc.configYAML != "" {
				cfgPath := filepath.Join(t.TempDir(), "config.yaml")
				if err := os.WriteFile(cfgPath, []byte(tc.configYAML), 0o600); err != nil {
					t.Fatalf("failed to write temp config: %v", err)
				}
				args = append(args, "--config", cfgPath)
			}
			args = append(args, tc.extraArgs...)

			err := runWithOutput(t.Context(), args, io.Discard)
			if err == nil {
				t.Fatal("expected Run to return an error, got nil")
			}
			if tc.wantTargetIs != nil && !errors.Is(err, tc.wantTargetIs) {
				t.Errorf("expected error matching %v, got: %v", tc.wantTargetIs, err)
			}
		})
	}
}

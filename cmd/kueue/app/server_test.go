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
	"crypto/tls"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"k8s.io/component-base/featuregate"
	ctrl "sigs.k8s.io/controller-runtime"
	clientconfig "sigs.k8s.io/controller-runtime/pkg/client/config"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
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
		wantTargetIs []error
		wantPathErr  bool
	}{
		"invalid flag": {
			extraArgs:    []string{"--unknown-flag"},
			wantTargetIs: []error{errFlagParse},
		},
		"help flag": {
			extraArgs:    []string{"--help"},
			wantTargetIs: []error{errFlagParse, flag.ErrHelp},
		},
		"missing config file": {
			missingCfg:   true,
			wantTargetIs: []error{errConfigLoad, os.ErrNotExist},
		},
		"invalid config YAML": {
			configYAML:   "not: [valid: yaml",
			wantTargetIs: []error{errConfigLoad},
		},
		"conflicting feature gates": {
			configYAML: `apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
featureGates:
  TopologyAwareScheduling: true
`,
			extraArgs:    []string{"--feature-gates=TopologyAwareScheduling=false"},
			wantTargetIs: []error{errFeatureGates},
		},
		"invalid configuration": {
			configYAML: `apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
waitForPodsReady:
  timeout: -1s
`,
			wantTargetIs: []error{errConfigValidate},
		},
		"invalid TLS options rejected by validation": {
			featureGates: map[featuregate.Feature]bool{
				features.TLSOptions: true,
			},
			configYAML: `apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
tls:
  minVersion: InvalidVersion
`,
			wantTargetIs: []error{errConfigValidate},
		},
		"missing metrics certificates": {
			configYAML: `apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
internalCertManagement:
  enable: false
`,
			wantTargetIs: []error{errMetricsCerts, os.ErrNotExist},
			wantPathErr:  true,
		},
		"missing kubeconfig": {
			configYAML: `apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
internalCertManagement:
  enable: true
`,
			wantTargetIs: []error{errKubeConfig},
		},
		"kubeconfig flag accepted": {
			extraArgs: []string{"--kubeconfig", filepath.Join(t.TempDir(), "nonexistent-kubeconfig")},
			configYAML: `apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
internalCertManagement:
  enable: true
`,
			wantTargetIs: []error{errKubeConfig, os.ErrNotExist},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("KUBERNETES_SERVICE_HOST", "")
			t.Setenv("KUBERNETES_SERVICE_PORT", "")
			t.Cleanup(func() {
				clientconfig.RegisterFlags(flag.NewFlagSet("reset", flag.ContinueOnError))
			})
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

			certDir := t.TempDir()
			err := runWithOptions(t.Context(), args, io.Discard, certDir)
			if err == nil {
				t.Fatal("expected runWithOptions to return an error, got nil")
			}
			for _, target := range tc.wantTargetIs {
				if !errors.Is(err, target) {
					t.Errorf("expected error matching %v, got: %v", target, err)
				}
			}
			if tc.wantPathErr {
				var pathErr *os.PathError
				if !errors.As(err, &pathErr) {
					t.Fatalf("expected *os.PathError, got: %T (%v)", err, err)
				}
				if diff := cmp.Diff(filepath.Join(certDir, "tls.crt"), pathErr.Path); diff != "" {
					t.Errorf("unexpected missing cert path (-want/+got):\n%s", diff)
				}
			}
		})
	}

	t.Run("metrics and TLS configuration", func(t *testing.T) {
		cfgTests := map[string]struct {
			featureGates     map[featuregate.Feature]bool
			tlsMinVersion    string
			customLabels     []configapi.ControllerMetricsCustomLabel
			wantMinVersion   uint16
			wantCustomLabels bool
		}{
			"TLSOptions applies valid non-default minVersion": {
				featureGates: map[featuregate.Feature]bool{
					features.TLSOptions: true,
				},
				tlsMinVersion:  "VersionTLS13",
				wantMinVersion: tls.VersionTLS13,
			},
			"CustomMetricLabels enabled installs custom labels": {
				featureGates: map[featuregate.Feature]bool{
					features.CustomMetricLabels: true,
				},
				customLabels: []configapi.ControllerMetricsCustomLabel{
					{Name: "team", SourceLabelKey: "team"},
				},
				wantCustomLabels: true,
			},
			"CustomMetricLabels disabled ignores custom labels": {
				featureGates: map[featuregate.Feature]bool{
					features.CustomMetricLabels: false,
				},
				customLabels: []configapi.ControllerMetricsCustomLabel{
					{Name: "team", SourceLabelKey: "team"},
				},
				wantCustomLabels: false,
			},
		}

		for name, tc := range cfgTests {
			t.Run(name, func(t *testing.T) {
				for fg, enabled := range tc.featureGates {
					features.SetFeatureGateDuringTest(t, fg, enabled)
				}

				options := ctrl.Options{}
				var cfg configapi.Configuration
				scheme.Default(&cfg)
				cfg.InternalCertManagement = &configapi.InternalCertManagement{Enable: new(true)}
				if tc.tlsMinVersion != "" {
					cfg.TLS = &configapi.TLSOptions{MinVersion: tc.tlsMinVersion}
				}
				cfg.Metrics.CustomLabels = tc.customLabels

				parsedTLS, _, _, customLabels, err := configureMetricsAndWebhooks(&options, &cfg, t.TempDir())
				if err != nil {
					t.Fatalf("configureMetricsAndWebhooks() unexpected error: %v", err)
				}
				if tc.wantMinVersion != 0 {
					if diff := cmp.Diff(tc.wantMinVersion, parsedTLS.MinVersion); diff != "" {
						t.Errorf("unexpected TLS MinVersion (-want/+got):\n%s", diff)
					}
				}
				if (customLabels != nil) != tc.wantCustomLabels {
					t.Errorf("customLabels != nil is %v, want %v", customLabels != nil, tc.wantCustomLabels)
				}
			})
		}
	})

	t.Run("kubeconfig rate limiter", func(t *testing.T) {
		type limiterResult struct {
			HasLimiter bool
			QPS        float32
		}

		kcTests := map[string]struct {
			qps  float32
			want limiterResult
		}{
			"positive QPS enables rate limiter": {
				qps:  20,
				want: limiterResult{HasLimiter: true, QPS: 20},
			},
			"negative QPS disables rate limiter": {
				qps:  -1,
				want: limiterResult{HasLimiter: false},
			},
		}

		for name, tc := range kcTests {
			t.Run(name, func(t *testing.T) {
				kcPath := filepath.Join(t.TempDir(), "kubeconfig")
				if err := os.WriteFile(kcPath, []byte(validKubeconfig), 0o600); err != nil {
					t.Fatalf("failed to write temp kubeconfig: %v", err)
				}
				t.Setenv("KUBECONFIG", kcPath)

				cfg := &configapi.Configuration{
					ClientConnection: &configapi.ClientConnection{
						QPS:   new(tc.qps),
						Burst: new(int32(30)),
					},
				}
				kubeConfig, err := buildKubeConfig(cfg)
				if err != nil {
					t.Fatalf("buildKubeConfig() unexpected error: %v", err)
				}
				var got limiterResult
				if kubeConfig.RateLimiter != nil {
					got = limiterResult{HasLimiter: true, QPS: kubeConfig.RateLimiter.QPS()}
				}
				if diff := cmp.Diff(tc.want, got); diff != "" {
					t.Errorf("unexpected rate limiter configuration (-want/+got):\n%s", diff)
				}
			})
		}
	})
}

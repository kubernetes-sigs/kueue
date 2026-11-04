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

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-logr/logr/funcr"
	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
)

func TestMetricsServerOptions(t *testing.T) {
	cases := map[string]struct {
		address     string
		gate        bool
		wantFilter  bool
		wantWarning bool
	}{
		"default delegated":             {address: ":8443", wantFilter: true},
		"loopback without gate":         {address: "127.0.0.1:8443", wantFilter: true},
		"IPv4 opt out":                  {address: "127.0.0.1:8443", gate: true, wantWarning: true},
		"IPv6 opt out":                  {address: "[::1]:8443", gate: true, wantWarning: true},
		"mapped opt out":                {address: "[::ffff:127.0.0.1]:8443", gate: true, wantWarning: true},
		"disabled metrics without gate": {address: "0", wantFilter: true},
		"disabled metrics with gate":    {address: "0", gate: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.DisableMetricsAccessControl, tc.gate)
			var logs []string
			log := funcr.New(func(prefix, args string) { logs = append(logs, prefix+args) }, funcr.Options{})
			options := MetricsServerOptions(log, configapi.ControllerMetrics{BindAddress: tc.address})
			if diff := cmp.Diff(tc.address, options.BindAddress); diff != "" {
				t.Errorf("unexpected address (-want,+got): %s", diff)
			}
			if !options.SecureServing {
				t.Error("metrics must use HTTPS")
			}
			if gotFilter := options.FilterProvider != nil; gotFilter != tc.wantFilter {
				t.Errorf("filter present = %v, want %v", gotFilter, tc.wantFilter)
			}
			if tc.wantWarning {
				if len(logs) != 1 {
					t.Fatalf("expected one startup warning, got %v", logs)
				}
				for _, text := range []string{"WARNING", "authentication and authorization are disabled", "tenant information", "network namespace", tc.address} {
					if !strings.Contains(logs[0], text) {
						t.Errorf("warning %q missing %q", logs[0], text)
					}
				}
			} else if len(logs) != 0 {
				t.Errorf("unexpected warning: %v", logs)
			}
		})
	}
}

func TestLoadMetricsConfiguration(t *testing.T) {
	cases := map[string]struct {
		configYAML  string
		wantAddress string
		wantErrors  field.ErrorList
	}{
		"default authenticated listener": {wantAddress: configapi.DefaultMetricsBindAddress},
		"opt out with loopback": {
			configYAML:  "featureGates: {DisableMetricsAccessControl: true}\nmetrics: {bindAddress: '127.0.0.1:8443'}\n",
			wantAddress: "127.0.0.1:8443",
		},
		"opt out with metrics disabled": {
			configYAML:  "featureGates: {DisableMetricsAccessControl: true}\nmetrics: {bindAddress: '0'}\n",
			wantAddress: "0",
		},
		"opt out with omitted bind address": {
			configYAML:  "featureGates: {DisableMetricsAccessControl: true}\n",
			wantAddress: configapi.DefaultMetricsBindAddress,
			wantErrors: field.ErrorList{
				field.Invalid(
					field.NewPath("metrics", "bindAddress"),
					configapi.DefaultMetricsBindAddress,
					"DisableMetricsAccessControl requires a literal loopback IP and numeric port in the range 1–65535 (for example 127.0.0.1:8443 or [::1]:8443); disable the feature gate for other addresses",
				),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.DisableMetricsAccessControl, false)
			scheme := runtime.NewScheme()
			if err := configapi.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "config.yaml")
			content := "apiVersion: config.kueue.x-k8s.io/v1beta2\nkind: Configuration\n" + tc.configYAML
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			_, cfg, err := Load(scheme, path)
			if err != nil {
				t.Fatal(err)
			}
			if errs := LoadAndValidateFeatureGates("", cfg.FeatureGates); len(errs) != 0 {
				t.Fatalf("unexpected feature gate errors: %v", errs)
			}
			if diff := cmp.Diff(tc.wantAddress, cfg.Metrics.BindAddress); diff != "" {
				t.Errorf("unexpected bind address (-want,+got): %s", diff)
			}
			if diff := cmp.Diff(tc.wantErrors, Validate(&cfg, scheme)); diff != "" {
				t.Errorf("unexpected validation errors (-want,+got): %s", diff)
			}
		})
	}
}

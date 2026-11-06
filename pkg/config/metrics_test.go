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
	"sigs.k8s.io/kueue/pkg/controller/jobs"
	"sigs.k8s.io/kueue/pkg/features"
)

func TestMetricsServerOptions(t *testing.T) {
	cases := map[string]struct {
		mode        *configapi.MetricsAccessControlMode
		address     string
		gate        bool
		wantFilter  bool
		wantWarning bool
		wantError   string
	}{
		"default delegated":               {address: ":8443", wantFilter: true},
		"explicit delegated without gate": {mode: new(configapi.MetricsAccessControlModeDelegated), address: ":8443", wantError: "metrics.accessControlMode"},
		"opt out without gate":            {mode: new(configapi.MetricsAccessControlModeNone), address: "127.0.0.1:8443", wantError: "metrics.accessControlMode"},
		"delegated with gate":             {mode: new(configapi.MetricsAccessControlModeDelegated), address: ":8443", gate: true, wantFilter: true},
		"IPv4 opt out":                    {mode: new(configapi.MetricsAccessControlModeNone), address: "127.0.0.1:8443", gate: true, wantWarning: true},
		"IPv6 opt out":                    {mode: new(configapi.MetricsAccessControlModeNone), address: "[::1]:8443", gate: true, wantWarning: true},
		"mapped opt out":                  {mode: new(configapi.MetricsAccessControlModeNone), address: "[::ffff:127.0.0.1]:8443", gate: true, wantWarning: true},
		"disabled default without gate":   {address: "0", wantFilter: true},
		"disabled delegated":              {gate: true, mode: new(configapi.MetricsAccessControlModeDelegated), address: "0", wantFilter: true},
		"disabled opt out without gate":   {mode: new(configapi.MetricsAccessControlModeNone), address: "0", wantError: "metrics.accessControlMode"},
		"disabled opt out with gate":      {mode: new(configapi.MetricsAccessControlModeNone), address: "0", gate: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.MetricsAccessControl, tc.gate)
			var logs []string
			log := funcr.New(func(prefix, args string) { logs = append(logs, prefix+args) }, funcr.Options{})
			cfg := &configapi.Configuration{Metrics: configapi.ControllerMetrics{AccessControlMode: tc.mode, BindAddress: tc.address}}
			configapi.SetDefaults_Configuration(cfg)
			options, clientCAWatcher, err := MetricsServerOptions(log, cfg.Metrics)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("expected construction error for %s, got %v", tc.wantError, err)
				}
				if clientCAWatcher != nil || len(logs) != 0 {
					t.Error("invalid configuration started a watcher or emitted an opt-out warning")
				}
				return
			}
			if err != nil {
				t.Fatalf("construct metrics options: %v", err)
			}
			if clientCAWatcher != nil {
				t.Error("existing modes must not start a client-CA watcher")
			}
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
		metricsYAML  string
		wantMode     *configapi.MetricsAccessControlMode
		wantCA       *string
		gateDisabled bool
		wantErrors   field.ErrorList
	}{
		"omitted mode without gate": {metricsYAML: "{}", gateDisabled: true},
		"explicit mode without gate": {
			metricsYAML: "{accessControlMode: Delegated}", gateDisabled: true,
			wantMode:   new(configapi.MetricsAccessControlModeDelegated),
			wantErrors: field.ErrorList{field.Forbidden(field.NewPath("metrics", "accessControlMode"), "requires the MetricsAccessControl feature gate")},
		},
		"omitted mode":                {metricsYAML: "{}", wantMode: new(configapi.MetricsAccessControlModeDelegated)},
		"null mode":                   {metricsYAML: "{accessControlMode: null}", wantMode: new(configapi.MetricsAccessControlModeDelegated)},
		"null CA preserves delegated": {metricsYAML: "{clientCAFile: null}", wantMode: new(configapi.MetricsAccessControlModeDelegated)},
		"certificate mode": {
			metricsYAML: "{accessControlMode: ClientCertificateOrBearer, clientCAFile: /public/client-ca.crt}",
			wantMode:    new(configapi.MetricsAccessControlModeClientCertificateOrBearer), wantCA: new("/public/client-ca.crt"),
		},
		"certificate mode without gate": {
			metricsYAML: "{accessControlMode: ClientCertificateOrBearer, clientCAFile: /public/client-ca.crt}", gateDisabled: true,
			wantMode: new(configapi.MetricsAccessControlModeClientCertificateOrBearer), wantCA: new("/public/client-ca.crt"),
			wantErrors: field.ErrorList{
				field.Forbidden(field.NewPath("metrics", "accessControlMode"), "requires the MetricsAccessControl feature gate"),
				field.Forbidden(field.NewPath("metrics", "clientCAFile"), "requires the MetricsAccessControl feature gate"),
			},
		},
		"null CA in certificate mode": {
			metricsYAML: "{accessControlMode: ClientCertificateOrBearer, clientCAFile: null}",
			wantMode:    new(configapi.MetricsAccessControlModeClientCertificateOrBearer),
			wantErrors:  field.ErrorList{field.Required(field.NewPath("metrics", "clientCAFile"), "ClientCertificateOrBearer requires a public client-CA bundle")},
		},
		"client CA with omitted mode": {
			metricsYAML: "{clientCAFile: /public/client-ca.crt}", wantMode: new(configapi.MetricsAccessControlModeDelegated), wantCA: new("/public/client-ca.crt"),
			wantErrors: field.ErrorList{field.Forbidden(field.NewPath("metrics", "clientCAFile"), "only supported in ClientCertificateOrBearer mode")},
		},
		"client CA without mode or gate": {
			metricsYAML: "{clientCAFile: /public/client-ca.crt}", gateDisabled: true, wantCA: new("/public/client-ca.crt"),
			wantErrors: field.ErrorList{field.Forbidden(field.NewPath("metrics", "clientCAFile"), "requires the MetricsAccessControl feature gate")},
		},
		"explicit delegated": {metricsYAML: "{accessControlMode: Delegated}", wantMode: new(configapi.MetricsAccessControlModeDelegated)},
		"empty mode": {
			metricsYAML: `{accessControlMode: ""}`, wantMode: new(configapi.MetricsAccessControlMode("")),
			wantErrors: field.ErrorList{field.NotSupported(field.NewPath("metrics", "accessControlMode"), configapi.MetricsAccessControlMode(""), []configapi.MetricsAccessControlMode{
				configapi.MetricsAccessControlModeDelegated, configapi.MetricsAccessControlModeNone, configapi.MetricsAccessControlModeClientCertificateOrBearer,
			})},
		},
		"opt out with omitted address": {
			metricsYAML: "{accessControlMode: None}", wantMode: new(configapi.MetricsAccessControlModeNone),
			wantErrors: field.ErrorList{field.Invalid(field.NewPath("metrics", "bindAddress"), configapi.DefaultMetricsBindAddress,
				"accessControlMode None requires a literal loopback IP and numeric port in the range 1–65535 (for example 127.0.0.1:8443 or [::1]:8443); use Delegated for other addresses")},
		},
		"opt out with empty address": {
			metricsYAML: `{accessControlMode: None, bindAddress: ""}`, wantMode: new(configapi.MetricsAccessControlModeNone),
			wantErrors: field.ErrorList{field.Invalid(field.NewPath("metrics", "bindAddress"), configapi.DefaultMetricsBindAddress,
				"accessControlMode None requires a literal loopback IP and numeric port in the range 1–65535 (for example 127.0.0.1:8443 or [::1]:8443); use Delegated for other addresses")},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.MetricsAccessControl, false)
			scheme := runtime.NewScheme()
			if err := configapi.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "config.yaml")
			content := "apiVersion: config.kueue.x-k8s.io/v1beta2\nkind: Configuration\nmetrics: " + tc.metricsYAML + "\n"
			if !tc.gateDisabled {
				content += "featureGates:\n  MetricsAccessControl: true\n"
			}
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
			scheme.Default(&cfg)
			if diff := cmp.Diff(tc.wantMode, cfg.Metrics.AccessControlMode); diff != "" {
				t.Errorf("unexpected mode (-want,+got): %s", diff)
			}
			if diff := cmp.Diff(tc.wantCA, cfg.Metrics.ClientCAFile); diff != "" {
				t.Errorf("unexpected client CA path (-want,+got): %s", diff)
			}
			if diff := cmp.Diff(configapi.DefaultMetricsBindAddress, cfg.Metrics.BindAddress); diff != "" {
				t.Errorf("unexpected default bind address (-want,+got): %s", diff)
			}
			if diff := cmp.Diff(tc.wantErrors, Validate(&cfg, scheme, jobs.NewIntegrationManager())); diff != "" {
				t.Errorf("unexpected validation errors (-want,+got): %s", diff)
			}
		})
	}
}

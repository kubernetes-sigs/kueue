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
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/util/validation/field"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
)

func TestValidateMetricsClientCertificate(t *testing.T) {
	cases := map[string]struct {
		mode    configapi.MetricsAccessControlMode
		caFile  *string
		address string
		gate    bool
		want    field.ErrorList
	}{
		"remote certificate mode": {
			mode: configapi.MetricsAccessControlModeClientCertificateOrBearer, caFile: new("/public/client-ca.crt"), address: ":8443", gate: true,
		},
		"disabled certificate listener performs no file I/O": {
			mode: configapi.MetricsAccessControlModeClientCertificateOrBearer, caFile: new("/missing/client-ca.crt"), address: "0", gate: true,
		},
		"certificate mode without gate": {
			mode:    configapi.MetricsAccessControlModeClientCertificateOrBearer,
			caFile:  new("/public/client-ca.crt"),
			address: ":8443",
			want: field.ErrorList{
				field.Forbidden(field.NewPath("metrics", "accessControlMode"), "requires the MetricsAccessControl feature gate"),
				field.Forbidden(field.NewPath("metrics", "clientCAFile"), "requires the MetricsAccessControl feature gate"),
			},
		},
		"gate rollback rejects retained certificate mode even with metrics disabled": {
			mode:    configapi.MetricsAccessControlModeClientCertificateOrBearer,
			caFile:  new("/public/client-ca.crt"),
			address: "0",
			want: field.ErrorList{
				field.Forbidden(field.NewPath("metrics", "accessControlMode"), "requires the MetricsAccessControl feature gate"),
				field.Forbidden(field.NewPath("metrics", "clientCAFile"), "requires the MetricsAccessControl feature gate"),
			},
		},
		"missing client CA": {
			mode: configapi.MetricsAccessControlModeClientCertificateOrBearer, address: ":8443", gate: true,
			want: field.ErrorList{field.Required(field.NewPath("metrics", "clientCAFile"), "ClientCertificateOrBearer requires a public client-CA bundle")},
		},
		"empty client CA path": {
			mode: configapi.MetricsAccessControlModeClientCertificateOrBearer, caFile: new(""), address: ":8443", gate: true,
			want: field.ErrorList{field.Invalid(field.NewPath("metrics", "clientCAFile"), "", "must be a nonempty absolute path without surrounding whitespace")},
		},
		"whitespace client CA path": {
			mode: configapi.MetricsAccessControlModeClientCertificateOrBearer, caFile: new(" /public/client-ca.crt "), address: ":8443", gate: true,
			want: field.ErrorList{field.Invalid(field.NewPath("metrics", "clientCAFile"), " /public/client-ca.crt ", "must be a nonempty absolute path without surrounding whitespace")},
		},
		"relative client CA path": {
			mode: configapi.MetricsAccessControlModeClientCertificateOrBearer, caFile: new("client-ca.crt"), address: ":8443", gate: true,
			want: field.ErrorList{field.Invalid(field.NewPath("metrics", "clientCAFile"), "client-ca.crt", "must be a nonempty absolute path without surrounding whitespace")},
		},
		"client CA cannot silently alter delegated mode": {
			mode: configapi.MetricsAccessControlModeDelegated, caFile: new("/public/client-ca.crt"), address: ":8443", gate: true,
			want: field.ErrorList{field.Forbidden(field.NewPath("metrics", "clientCAFile"), "only supported in ClientCertificateOrBearer mode")},
		},
		"client CA cannot be combined with anonymous mode": {
			mode: configapi.MetricsAccessControlModeNone, caFile: new("/public/client-ca.crt"), address: "127.0.0.1:8443", gate: true,
			want: field.ErrorList{field.Forbidden(field.NewPath("metrics", "clientCAFile"), "only supported in ClientCertificateOrBearer mode")},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.MetricsAccessControl, tc.gate)
			got := validateMetrics(configapi.ControllerMetrics{AccessControlMode: new(tc.mode), ClientCAFile: tc.caFile, BindAddress: tc.address})
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("unexpected validation (-want,+got): %s", diff)
			}
		})
	}
}

func TestMetricsServerOptionsRejectInvalidConfiguration(t *testing.T) {
	cases := map[string]struct {
		metrics   configapi.ControllerMetrics
		wantField string
	}{
		"gate off cannot start certificate mode": {
			metrics: configapi.ControllerMetrics{
				AccessControlMode: new(configapi.MetricsAccessControlModeClientCertificateOrBearer),
				ClientCAFile:      new("/public/client-ca.crt"),
				BindAddress:       ":8443",
			},
			wantField: "metrics.accessControlMode",
		},
		"gate off cannot start unauthenticated listener": {
			metrics:   configapi.ControllerMetrics{AccessControlMode: new(configapi.MetricsAccessControlModeNone), BindAddress: "127.0.0.1:8443"},
			wantField: "metrics.accessControlMode",
		},
		"default mode cannot silently ignore a client CA": {
			metrics:   configapi.ControllerMetrics{ClientCAFile: new("/public/client-ca.crt"), BindAddress: ":8443"},
			wantField: "metrics.clientCAFile",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.MetricsAccessControl, false)
			_, watcher, err := MetricsServerOptions(logr.Discard(), tc.metrics)
			if err == nil || !strings.Contains(err.Error(), tc.wantField) {
				t.Fatalf("constructor did not enforce %s: %v", tc.wantField, err)
			}
			if watcher != nil {
				t.Error("invalid configuration started a client-CA watcher")
			}
		})
	}
}

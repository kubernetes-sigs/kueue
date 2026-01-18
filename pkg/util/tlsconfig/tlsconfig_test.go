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

package tlsconfig

import (
	"crypto/tls"
	"testing"

	"github.com/google/go-cmp/cmp"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
)

func TestBuildTLSOptions(t *testing.T) {
	tests := []struct {
		name             string
		cfg              *config.TLSOptions
		featureGateValue *bool
		expectedMinVer   uint16
		expectedCipher   []uint16
		expectNil        bool
		wantErr          bool
	}{
		{
			name:           "nil config",
			cfg:            nil,
			expectedMinVer: 0,
			expectedCipher: nil,
			expectNil:      true,
			wantErr:        false,
		},
		{
			name:           "empty config",
			cfg:            &config.TLSOptions{},
			expectedMinVer: tls.VersionTLS12,
			expectedCipher: nil,
			wantErr:        false,
		},
		{
			name: "TLS 1.2",
			cfg: &config.TLSOptions{
				TLSMinVersion: "VersionTLS12",
			},
			expectedMinVer: tls.VersionTLS12,
			expectedCipher: nil,
			wantErr:        false,
		},
		{
			name: "TLS 1.3",
			cfg: &config.TLSOptions{
				TLSMinVersion: "VersionTLS13",
			},
			expectedMinVer: tls.VersionTLS13,
			expectedCipher: nil,
			wantErr:        false,
		},
		{
			name: "with cipher suites",
			cfg: &config.TLSOptions{
				TLSMinVersion: "VersionTLS12",
				TLSCipherSuites: []string{
					"TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256",
					"TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384",
				},
			},
			expectedMinVer: tls.VersionTLS12,
			expectedCipher: []uint16{
				tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
				tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			},
			wantErr: false,
		},
		{
			name: "full config",
			cfg: &config.TLSOptions{
				TLSMinVersion: "VersionTLS13",
				TLSCipherSuites: []string{
					"TLS_AES_128_GCM_SHA256",
					"TLS_AES_256_GCM_SHA384",
				},
			},
			expectedMinVer: tls.VersionTLS13,
			expectedCipher: []uint16{
				tls.TLS_AES_128_GCM_SHA256,
				tls.TLS_AES_256_GCM_SHA384,
			},
			wantErr: false,
		},
		{
			name:             "feature gate disabled with nil config",
			cfg:              nil,
			featureGateValue: ptrBool(false),
			expectNil:        true,
			wantErr:          false,
		},
		{
			name: "feature gate disabled with valid config",
			cfg: &config.TLSOptions{
				TLSMinVersion: "VersionTLS13",
				TLSCipherSuites: []string{
					"TLS_AES_128_GCM_SHA256",
				},
			},
			featureGateValue: ptrBool(false),
			expectNil:        true,
			wantErr:          false,
		},
		{
			name:             "feature gate enabled with nil config",
			cfg:              nil,
			featureGateValue: ptrBool(true),
			expectNil:        true,
			wantErr:          false,
		},
		{
			name:             "feature gate enabled with empty config",
			cfg:              &config.TLSOptions{},
			featureGateValue: ptrBool(true),
			expectedMinVer:   tls.VersionTLS12,
			expectedCipher:   nil,
			wantErr:          false,
		},
		{
			name: "feature gate enabled with TLS 1.3",
			cfg: &config.TLSOptions{
				TLSMinVersion: "VersionTLS13",
			},
			featureGateValue: ptrBool(true),
			expectedMinVer:   tls.VersionTLS13,
			expectedCipher:   nil,
			wantErr:          false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set the feature gate if specified
			if tt.featureGateValue != nil {
				features.SetFeatureGateDuringTest(t, features.TLSOptions, *tt.featureGateValue)
			}

			tlsOpts, err := BuildTLSOptions(tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("BuildTLSOptions() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.expectNil {
				if tlsOpts != nil {
					t.Errorf("BuildTLSOptions() returned non-nil, want nil")
				}
				return
			}

			// Apply the options to a test TLS config
			testConfig := &tls.Config{}
			for _, opt := range tlsOpts {
				opt(testConfig)
			}

			if testConfig.MinVersion != tt.expectedMinVer {
				t.Errorf("MinVersion = %v, want %v", testConfig.MinVersion, tt.expectedMinVer)
			}

			if !cmp.Equal(testConfig.CipherSuites, tt.expectedCipher) {
				t.Errorf("CipherSuites diff: %s", cmp.Diff(tt.expectedCipher, testConfig.CipherSuites))
			}
		})
	}
}

func ptrBool(b bool) *bool {
	return &b
}

func TestConvertCipherSuites(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		expected []uint16
		wantErr  bool
	}{
		{
			name:     "empty input",
			input:    []string{},
			expected: nil,
			wantErr:  false,
		},
		{
			name:     "nil input",
			input:    nil,
			expected: nil,
			wantErr:  false,
		},
		{
			name: "valid cipher suites",
			input: []string{
				"TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256",
				"TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384",
			},
			expected: []uint16{
				tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
				tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			},
			wantErr: false,
		},
		{
			name: "invalid cipher suite",
			input: []string{
				"INVALID_CIPHER_SUITE",
			},
			expected: nil,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ConvertCipherSuites(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ConvertCipherSuites() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !cmp.Equal(got, tt.expected) {
				t.Errorf("ConvertCipherSuites() diff: %s", cmp.Diff(tt.expected, got))
			}
		})
	}
}

func TestConvertTLSMinVersion(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected uint16
		wantErr  bool
	}{
		{
			name:     "TLS 1.2",
			input:    "VersionTLS12",
			expected: tls.VersionTLS12,
			wantErr:  false,
		},
		{
			name:     "TLS 1.3",
			input:    "VersionTLS13",
			expected: tls.VersionTLS13,
			wantErr:  false,
		},
		{
			name:     "empty version defaults to TLS 1.2",
			input:    "",
			expected: tls.VersionTLS12,
			wantErr:  false,
		},
		{
			name:     "invalid version returns error and defaults to TLS 1.2",
			input:    "InvalidVersion",
			expected: tls.VersionTLS12,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ConvertTLSMinVersion(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ConvertTLSMinVersion() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.expected {
				t.Errorf("ConvertTLSMinVersion() = %v, want %v", got, tt.expected)
			}
		})
	}
}

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
)

func TestBuildTLSOptions(t *testing.T) {
	tests := []struct {
		name           string
		cfg            *config.TLSOptions
		expectedMinVer uint16
		expectedCipher []uint16
		wantErr        bool
	}{
		{
			name:           "nil config",
			cfg:            nil,
			expectedMinVer: 0,
			expectedCipher: nil,
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
				MinTLSVersion: config.TLSVersion12,
			},
			expectedMinVer: tls.VersionTLS12,
			expectedCipher: nil,
			wantErr:        false,
		},
		{
			name: "TLS 1.3",
			cfg: &config.TLSOptions{
				MinTLSVersion: config.TLSVersion13,
			},
			expectedMinVer: tls.VersionTLS13,
			expectedCipher: nil,
			wantErr:        false,
		},
		{
			name: "with cipher suites",
			cfg: &config.TLSOptions{
				MinTLSVersion: config.TLSVersion12,
				CipherSuites: []config.CipherSuite{
					config.CipherSuiteTLSECDHERSAWithAES128GCMSHA256,
					config.CipherSuiteTLSECDHERSAWithAES256GCMSHA384,
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
				MinTLSVersion: config.TLSVersion13,
				CipherSuites: []config.CipherSuite{
					config.CipherSuiteTLSAES128GCMSHA256,
					config.CipherSuiteTLSAES256GCMSHA384,
				},
			},
			expectedMinVer: tls.VersionTLS13,
			expectedCipher: []uint16{
				tls.TLS_AES_128_GCM_SHA256,
				tls.TLS_AES_256_GCM_SHA384,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tlsOpts, err := BuildTLSOptions(tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("BuildTLSOptions() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.cfg == nil {
				if tlsOpts != nil {
					t.Errorf("BuildTLSOptions() returned non-nil for nil config")
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

func TestConvertCipherSuites(t *testing.T) {
	tests := []struct {
		name     string
		input    []config.CipherSuite
		expected []uint16
		wantErr  bool
	}{
		{
			name:     "empty input",
			input:    []config.CipherSuite{},
			expected: nil,
			wantErr:  false,
		},
		{
			name: "valid cipher suites",
			input: []config.CipherSuite{
				config.CipherSuiteTLSECDHERSAWithAES128GCMSHA256,
				config.CipherSuiteTLSECDHEECDSAWithAES256GCMSHA384,
			},
			expected: []uint16{
				tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
				tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ConvertCipherSuites(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("convertCipherSuites() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !cmp.Equal(got, tt.expected) {
				t.Errorf("convertCipherSuites() diff: %s", cmp.Diff(tt.expected, got))
			}
		})
	}
}

func TestConvertTLSMinVersion(t *testing.T) {
	tests := []struct {
		name     string
		input    config.TLSVersion
		expected uint16
	}{
		{
			name:     "TLS 1.2",
			input:    config.TLSVersion12,
			expected: tls.VersionTLS12,
		},
		{
			name:     "TLS 1.3",
			input:    config.TLSVersion13,
			expected: tls.VersionTLS13,
		},
		{
			name:     "empty version defaults to TLS 1.2",
			input:    "",
			expected: tls.VersionTLS12,
		},
		{
			name:     "invalid version defaults to TLS 1.2",
			input:    "TLS1.1",
			expected: tls.VersionTLS12,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ConvertTLSMinVersion(tt.input)
			if got != tt.expected {
				t.Errorf("ConvertTLSMinVersion() = %v, want %v", got, tt.expected)
			}
		})
	}
}

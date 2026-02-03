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
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
)

// compareErrors compares errors by their string representation
func compareErrors(want, got error) bool {
	if want == nil && got == nil {
		return true
	}
	if want == nil || got == nil {
		return false
	}
	// Compare error strings, handling wrapped errors
	return strings.Contains(got.Error(), want.Error())
}

var (
	errInvalidMinVersionTLS10or11 = errors.New("invalid minVersion. Please use VersionTLS12 or VersionTLS13")
	errInvalidCipherSuite         = errors.New("cipher suite")        // Partial match for cipher suite errors
	errInvalidVersion             = errors.New("unknown tls version") // From cliflag.TLSVersion for invalid versions
)

func TestParseTLSOptions(t *testing.T) {
	tests := []struct {
		name       string
		cfg        *config.TLSOptions
		expectNil  bool
		wantErr    error
		validateFn func(*testing.T, *TLS)
	}{
		{
			name:      "nil config",
			cfg:       nil,
			expectNil: true,
		},
		{
			name: "empty config",
			cfg:  &config.TLSOptions{},
			validateFn: func(t *testing.T, tlsOpts *TLS) {
				if tlsOpts.MinVersion != tls.VersionTLS12 {
					t.Errorf("MinVersion = %v, want %v", tlsOpts.MinVersion, tls.VersionTLS12)
				}
				if tlsOpts.CipherSuites != nil {
					t.Errorf("CipherSuites = %v, want nil", tlsOpts.CipherSuites)
				}
			},
		},
		{
			name: "no tls min version but ciphersconfig",
			cfg: &config.TLSOptions{
				CipherSuites: []string{
					"TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256",
					"TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384",
				},
			},
			validateFn: func(t *testing.T, tlsOpts *TLS) {
				if tlsOpts.MinVersion != tls.VersionTLS12 {
					t.Errorf("MinVersion = %v, want %v", tlsOpts.MinVersion, tls.VersionTLS12)
				}
				expectedCiphers := []uint16{
					tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
					tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
				}
				if !cmp.Equal(tlsOpts.CipherSuites, expectedCiphers) {
					t.Errorf("CipherSuites diff: %s", cmp.Diff(expectedCiphers, tlsOpts.CipherSuites))
				}
			},
		},
		{
			name: "TLS 1.2",
			cfg: &config.TLSOptions{
				MinVersion: "VersionTLS12",
			},
			validateFn: func(t *testing.T, tlsOpts *TLS) {
				if tlsOpts.MinVersion != tls.VersionTLS12 {
					t.Errorf("MinVersion = %v, want %v", tlsOpts.MinVersion, tls.VersionTLS12)
				}
				if tlsOpts.CipherSuites != nil {
					t.Errorf("CipherSuites = %v, want nil", tlsOpts.CipherSuites)
				}
			},
		},
		{
			name: "TLS 1.3",
			cfg: &config.TLSOptions{
				MinVersion: "VersionTLS13",
			},
			validateFn: func(t *testing.T, tlsOpts *TLS) {
				if tlsOpts.MinVersion != tls.VersionTLS13 {
					t.Errorf("MinVersion = %v, want %v", tlsOpts.MinVersion, tls.VersionTLS13)
				}
				if tlsOpts.CipherSuites != nil {
					t.Errorf("CipherSuites = %v, want nil", tlsOpts.CipherSuites)
				}
			},
		},
		{
			name: "with cipher suites",
			cfg: &config.TLSOptions{
				MinVersion: "VersionTLS12",
				CipherSuites: []string{
					"TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256",
					"TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384",
				},
			},
			validateFn: func(t *testing.T, tlsOpts *TLS) {
				if tlsOpts.MinVersion != tls.VersionTLS12 {
					t.Errorf("MinVersion = %v, want %v", tlsOpts.MinVersion, tls.VersionTLS12)
				}
				expectedCiphers := []uint16{
					tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
					tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
				}
				if !cmp.Equal(tlsOpts.CipherSuites, expectedCiphers) {
					t.Errorf("CipherSuites diff: %s", cmp.Diff(expectedCiphers, tlsOpts.CipherSuites))
				}
			},
		},
		{
			name: "tls 1.3 with cipher suites",
			cfg: &config.TLSOptions{
				MinVersion: "VersionTLS13",
				CipherSuites: []string{
					"TLS_AES_128_GCM_SHA256",
					"TLS_AES_256_GCM_SHA384",
				},
			},
			validateFn: func(t *testing.T, tlsOpts *TLS) {
				if tlsOpts.MinVersion != tls.VersionTLS13 {
					t.Errorf("MinVersion = %v, want %v", tlsOpts.MinVersion, tls.VersionTLS13)
				}
				expectedCiphers := []uint16{
					tls.TLS_AES_128_GCM_SHA256,
					tls.TLS_AES_256_GCM_SHA384,
				}
				if !cmp.Equal(tlsOpts.CipherSuites, expectedCiphers) {
					t.Errorf("CipherSuites diff: %s", cmp.Diff(expectedCiphers, tlsOpts.CipherSuites))
				}
			},
		},
		{
			name: "tls 1.0 with cipher suites",
			cfg: &config.TLSOptions{
				MinVersion: "VersionTLS10",
				CipherSuites: []string{
					"TLS_AES_128_GCM_SHA256",
					"TLS_AES_256_GCM_SHA384",
				},
			},
			wantErr: errInvalidMinVersionTLS10or11,
		},
		{
			name: "tls 1.2 with invalid cipher suites",
			cfg: &config.TLSOptions{
				MinVersion: "VersionTLS12",
				CipherSuites: []string{
					"DUMMY",
				},
			},
			wantErr: errInvalidCipherSuite,
		},
		{
			name: "tls 1.0 with invalid cipher suites",
			cfg: &config.TLSOptions{
				MinVersion: "VersionTLS10",
				CipherSuites: []string{
					"DUMMY",
				},
			},
			wantErr: errInvalidCipherSuite, // Both TLS version and cipher suite are invalid, but cipher suite error is returned last
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tlsOpts, err := ParseTLSOptions(tt.cfg)
			if !compareErrors(tt.wantErr, err) {
				t.Errorf("ParseTLSOptions() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if err != nil {
				return
			}

			if tt.expectNil {
				if tlsOpts != nil {
					t.Errorf("ParseTLSOptions() returned non-nil, want nil")
				}
				return
			}

			if tt.validateFn != nil {
				tt.validateFn(t, tlsOpts)
			}
		})
	}
}

func TestConvertCipherSuites(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		expected []uint16
		wantErr  error
	}{
		{
			name:     "empty input",
			input:    []string{},
			expected: nil,
		},
		{
			name:     "nil input",
			input:    nil,
			expected: nil,
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
		},
		{
			name: "invalid cipher suite",
			input: []string{
				"INVALID_CIPHER_SUITE",
			},
			wantErr: errInvalidCipherSuite,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := convertCipherSuites(tt.input)
			if !compareErrors(tt.wantErr, err) {
				t.Errorf("convertCipherSuites() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if err != nil {
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
		input    string
		expected uint16
		wantErr  error
	}{
		{
			name:     "TLS 1.2",
			input:    "VersionTLS12",
			expected: tls.VersionTLS12,
		},
		{
			name:     "TLS 1.3",
			input:    "VersionTLS13",
			expected: tls.VersionTLS13,
		},
		{
			name:     "empty version defaults to TLS 1.2",
			input:    "",
			expected: tls.VersionTLS12,
		},
		{
			name:     "invalid version returns error",
			input:    "InvalidVersion",
			expected: 0,
			wantErr:  errInvalidVersion,
		},
		{
			name:     "tls version 1.1 returns error",
			input:    "VersionTLS11",
			expected: 0,
			wantErr:  errInvalidMinVersionTLS10or11,
		},
		{
			name:     "tls version 1.0 returns error",
			input:    "VersionTLS10",
			expected: 0,
			wantErr:  errInvalidMinVersionTLS10or11,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := convertTLSMinVersion(tt.input)
			if !compareErrors(tt.wantErr, err) {
				t.Errorf("convertTLSMinVersion() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if err != nil {
				return
			}

			if got != tt.expected {
				t.Errorf("convertTLSMinVersion() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestBuildTLSOptions(t *testing.T) {
	tests := []struct {
		name           string
		tlsOptions     *TLS
		wantNil        bool
		wantMinVersion uint16
		wantCiphers    []uint16
	}{
		{
			name:       "nil TLS options",
			tlsOptions: nil,
			wantNil:    true,
		},
		{
			name: "TLS 1.2 with no cipher suites",
			tlsOptions: &TLS{
				MinVersion:   tls.VersionTLS12,
				CipherSuites: nil,
			},
			wantMinVersion: tls.VersionTLS12,
			wantCiphers:    nil,
		},
		{
			name: "TLS 1.3 with no cipher suites",
			tlsOptions: &TLS{
				MinVersion:   tls.VersionTLS13,
				CipherSuites: nil,
			},
			wantMinVersion: tls.VersionTLS13,
			wantCiphers:    nil,
		},
		{
			name: "TLS 1.2 with cipher suites",
			tlsOptions: &TLS{
				MinVersion: tls.VersionTLS12,
				CipherSuites: []uint16{
					tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
					tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
				},
			},
			wantMinVersion: tls.VersionTLS12,
			wantCiphers: []uint16{
				tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
				tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			},
		},
		{
			name: "TLS 1.3 with cipher suites",
			tlsOptions: &TLS{
				MinVersion: tls.VersionTLS13,
				CipherSuites: []uint16{
					tls.TLS_AES_128_GCM_SHA256,
					tls.TLS_AES_256_GCM_SHA384,
				},
			},
			wantMinVersion: tls.VersionTLS13,
			wantCiphers: []uint16{
				tls.TLS_AES_128_GCM_SHA256,
				tls.TLS_AES_256_GCM_SHA384,
			},
		},
		{
			name: "empty cipher suites slice",
			tlsOptions: &TLS{
				MinVersion:   tls.VersionTLS12,
				CipherSuites: []uint16{},
			},
			wantMinVersion: tls.VersionTLS12,
			wantCiphers:    []uint16{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildTLSOptions(tt.tlsOptions)

			if tt.wantNil {
				if got != nil {
					t.Errorf("BuildTLSOptions() = %v, want nil", got)
				}
				return
			}

			if got == nil {
				t.Error("BuildTLSOptions() = nil, want non-nil")
				return
			}

			if len(got) != 1 {
				t.Errorf("BuildTLSOptions() returned %d functions, want 1", len(got))
				return
			}

			// Apply the TLS option function to a config and verify it
			cfg := &tls.Config{}
			got[0](cfg)

			if cfg.MinVersion != tt.wantMinVersion {
				t.Errorf("MinVersion = %v, want %v", cfg.MinVersion, tt.wantMinVersion)
			}

			if !cmp.Equal(cfg.CipherSuites, tt.wantCiphers) {
				t.Errorf("CipherSuites diff: %s", cmp.Diff(tt.wantCiphers, cfg.CipherSuites))
			}
		})
	}
}

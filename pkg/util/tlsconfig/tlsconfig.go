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
	"fmt"

	cliflag "k8s.io/component-base/cli/flag"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
)

const TLS12 = tls.VersionTLS12

// BuildTLSOptions converts TLSOptions from the configuration to controller-runtime TLSOpts
// If the TLSOptions feature gate is disabled, it returns nil.
func BuildTLSOptions(cfg *config.TLSOptions) ([]func(*tls.Config), error) {
	// If feature gate is disabled, return nil to use golang defaults
	if !features.Enabled(features.TLSOptions) {
		return nil, nil
	}

	if cfg == nil {
		return nil, nil
	}

	var tlsOpts []func(*tls.Config)

	tlsOpts = append(tlsOpts, func(c *tls.Config) {
		// Set minimum TLS version
		if cfg.TLSMinVersion != "" {
			version, err := ConvertTLSMinVersion(cfg.TLSMinVersion)
			if err == nil {
				c.MinVersion = version
			} else {
				// Default to TLS 1.2 on error
				c.MinVersion = TLS12
			}
		} else {
			// Default to TLS 1.2
			c.MinVersion = TLS12
		}

		// Set cipher suites
		if len(cfg.TLSCipherSuites) > 0 {
			cipherSuites, err := ConvertCipherSuites(cfg.TLSCipherSuites)
			if err == nil && len(cipherSuites) > 0 {
				c.CipherSuites = cipherSuites
			}
		}
	})

	return tlsOpts, nil
}

// ConvertTLSMinVersion converts a TLS version string to the corresponding uint16 constant
// using k8s.io/component-base/cli/flag for validation
func ConvertTLSMinVersion(tlsMinVersion string) (uint16, error) {
	if tlsMinVersion == "" {
		return TLS12, nil
	}
	version, err := cliflag.TLSVersion(tlsMinVersion)
	if err != nil {
		return TLS12, fmt.Errorf("invalid TLS version: %w", err)
	}
	return version, nil
}

// ConvertCipherSuites converts cipher suite names to their crypto/tls constants
// using k8s.io/component-base/cli/flag for validation
func ConvertCipherSuites(cipherSuites []string) ([]uint16, error) {
	if len(cipherSuites) == 0 {
		return nil, nil
	}
	suites, err := cliflag.TLSCipherSuites(cipherSuites)
	if err != nil {
		return nil, fmt.Errorf("invalid cipher suites: %w", err)
	}
	return suites, nil
}

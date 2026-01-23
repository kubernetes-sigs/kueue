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
	"fmt"

	cliflag "k8s.io/component-base/cli/flag"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
)

const TLS12 = tls.VersionTLS12

type TLS struct {
	MinVersion   uint16
	CipherSuites []uint16
}

// ParseTLSOptions
func ParseTLSOptions(cfg *config.TLSOptions) (*TLS, error) {
	ret := &TLS{}
	var errRet error
	if cfg == nil {
		return nil, nil
	}
	version, err := convertTLSMinVersion(cfg.MinVersion)
	if err != nil {
		errRet = errors.Join(err)
	}
	ret.MinVersion = version

	// Set cipher suites
	if len(cfg.CipherSuites) > 0 {
		cipherSuites, err := convertCipherSuites(cfg.CipherSuites)
		if err != nil {
			errRet = errors.Join(err)
		}
		if err == nil && len(cipherSuites) > 0 {
			ret.CipherSuites = cipherSuites
		}
	}
	return ret, errRet
}

// BuildTLSOptions converts TLSOptions from the configuration to controller-runtime TLSOpts
// If the TLSOptions feature gate is disabled, it returns nil.
func BuildTLSOptions(tlsOptions *TLS) []func(*tls.Config) {
	if tlsOptions == nil {
		return nil
	}

	var tlsOpts []func(*tls.Config)

	tlsOpts = append(tlsOpts, func(c *tls.Config) {
		c.MinVersion = tlsOptions.MinVersion
		c.CipherSuites = tlsOptions.CipherSuites
	})

	return tlsOpts
}

// convertTLSMinVersion converts a TLS version string to the corresponding uint16 constant
// using k8s.io/component-base/cli/flag for validation
func convertTLSMinVersion(tlsMinVersion string) (uint16, error) {
	if tlsMinVersion == "" {
		return TLS12, nil
	}
	if tlsMinVersion == "VersionTLS11" || tlsMinVersion == "VersionTLS10" {
		return 0, errors.New("invalid minVersion. Please use VersionTLS12 or VersionTLS13")
	}
	version, err := cliflag.TLSVersion(tlsMinVersion)
	if err != nil {
		return 0, fmt.Errorf("invalid minVersion: %w. Please use VersionTLS12 or VersionTLS13", err)
	}
	return version, nil
}

// convertCipherSuites converts cipher suite names to their crypto/tls constants
// using k8s.io/component-base/cli/flag for validation
func convertCipherSuites(cipherSuites []string) ([]uint16, error) {
	if len(cipherSuites) == 0 {
		return nil, nil
	}
	suites, err := cliflag.TLSCipherSuites(cipherSuites)
	if err != nil {
		return nil, fmt.Errorf("invalid cipher suites: %w. Please use the secure cipher names: %v", err, cliflag.PreferredTLSCipherNames())
	}
	return suites, nil
}

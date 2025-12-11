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

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
)

var (
	// tlsVersionMap maps TLS versions to crypto/tls constants
	tlsVersionMap = map[config.TLSVersion]uint16{
		config.TLSVersion12: tls.VersionTLS12,
		config.TLSVersion13: tls.VersionTLS13,
	}

	// cipherSuiteMap maps cipher suite names to their crypto/tls constants
	cipherSuiteMap = map[config.CipherSuite]uint16{
		// TLS 1.2 cipher suites
		config.CipherSuiteTLSECDHEECDSAWithAES128CBCSHA:           tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA,
		config.CipherSuiteTLSECDHEECDSAWithAES256CBCSHA:           tls.TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA,
		config.CipherSuiteTLSECDHERSAWithAES128CBCSHA:             tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
		config.CipherSuiteTLSECDHERSAWithAES256CBCSHA:             tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
		config.CipherSuiteTLSECDHEECDSAWithAES128GCMSHA256:        tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
		config.CipherSuiteTLSECDHEECDSAWithAES256GCMSHA384:        tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
		config.CipherSuiteTLSECDHERSAWithAES128GCMSHA256:          tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
		config.CipherSuiteTLSECDHERSAWithAES256GCMSHA384:          tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
		config.CipherSuiteTLSECDHERSAWithCHACHA20POLY1305SHA256:   tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
		config.CipherSuiteTLSECDHEECDSAWithCHACHA20POLY1305SHA256: tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
		// TLS 1.3 cipher suites
		config.CipherSuiteTLSAES128GCMSHA256:        tls.TLS_AES_128_GCM_SHA256,
		config.CipherSuiteTLSAES256GCMSHA384:        tls.TLS_AES_256_GCM_SHA384,
		config.CipherSuiteTLSCHACHA20POLY1305SHA256: tls.TLS_CHACHA20_POLY1305_SHA256,
	}
)

// BuildTLSOptions converts TLSOptions from the configuration to controller-runtime TLSOpts
func BuildTLSOptions(cfg *config.TLSOptions) ([]func(*tls.Config), error) {
	if cfg == nil {
		return nil, nil
	}

	var tlsOpts []func(*tls.Config)

	tlsOpts = append(tlsOpts, func(c *tls.Config) {
		// Set minimum TLS version
		if cfg.MinTLSVersion != "" {
			version := ConvertTLSMinVersion(cfg.MinTLSVersion)
			c.MinVersion = version
		} else {
			// Default to TLS 1.2
			c.MinVersion = tls.VersionTLS12
		}

		// Set cipher suites
		if len(cfg.CipherSuites) > 0 {
			cipherSuites, err := ConvertCipherSuites(cfg.CipherSuites)
			if err == nil && len(cipherSuites) > 0 {
				c.CipherSuites = cipherSuites
			}
		}
	})

	return tlsOpts, nil
}

func ConvertTLSMinVersion(tlsMinVersions config.TLSVersion) uint16 {
	version, ok := tlsVersionMap[tlsMinVersions]
	if !ok {
		// Default to TLS 1.2 if invalid version is provided
		version = tls.VersionTLS12
	}
	return version
}

// ConvertCipherSuites converts cipher suite names to their crypto/tls constants
func ConvertCipherSuites(cipherSuites []config.CipherSuite) ([]uint16, error) {
	var suites []uint16
	var invalidSuites []config.CipherSuite

	for _, cipherSuite := range cipherSuites {
		suite, ok := cipherSuiteMap[cipherSuite]
		if !ok {
			invalidSuites = append(invalidSuites, cipherSuite)
			continue
		}
		suites = append(suites, suite)
	}

	if len(invalidSuites) > 0 {
		return suites, fmt.Errorf("invalid cipher suites: %v", invalidSuites)
	}

	return suites, nil
}

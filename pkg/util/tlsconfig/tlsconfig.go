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

var (
	// ErrInvalidMinVersion is returned when the TLS minimum version is invalid.
	ErrInvalidMinVersion = errors.New("invalid minVersion")
	// ErrInvalidCipherSuites is returned when cipher suites are invalid.
	ErrInvalidCipherSuites = errors.New("invalid cipher suites")
	// ErrInvalidCurvePreferences is returned when curve preferences are invalid.
	ErrInvalidCurvePreferences = errors.New("invalid curve preferences")
)

type TLS struct {
	MinVersion       uint16
	CipherSuites     []uint16
	CurvePreferences []tls.CurveID
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
		errRet = err
	}
	ret.MinVersion = version

	ret.CipherSuites, errRet = parseAndCollect(errRet, cfg.CipherSuites, convertCipherSuites)
	ret.CurvePreferences, errRet = parseAndCollect(errRet, cfg.CurvePreferences, convertCurvePreferences)
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
		c.CurvePreferences = tlsOptions.CurvePreferences
	})

	return tlsOpts
}

// parseAndCollect converts input using the provided convert function and accumulates errors.
func parseAndCollect[I any, O any](errRet error, input []I, convert func([]I) ([]O, error)) ([]O, error) {
	if len(input) == 0 {
		return nil, errRet
	}
	result, err := convert(input)
	if err != nil {
		return nil, errors.Join(errRet, err)
	}
	return result, errRet
}

// convertTLSMinVersion converts a TLS version string to the corresponding uint16 constant
// using k8s.io/component-base/cli/flag for validation
func convertTLSMinVersion(tlsMinVersion string) (uint16, error) {
	if tlsMinVersion == "" {
		return TLS12, nil
	}
	if tlsMinVersion == "VersionTLS11" || tlsMinVersion == "VersionTLS10" {
		return 0, fmt.Errorf("%w. Please use VersionTLS12 or VersionTLS13", ErrInvalidMinVersion)
	}
	version, err := cliflag.TLSVersion(tlsMinVersion)
	if err != nil {
		return 0, fmt.Errorf("%w: %v. Please use VersionTLS12 or VersionTLS13", ErrInvalidMinVersion, err)
	}
	return version, nil
}

// convertCipherSuites converts cipher suite names to their crypto/tls constants
// using k8s.io/component-base/cli/flag for validation
func convertCipherSuites(cipherSuites []string) ([]uint16, error) {
	suites, err := cliflag.TLSCipherSuites(cipherSuites)
	if err != nil {
		return nil, fmt.Errorf("%w: %v. Please use the secure cipher names: %v", ErrInvalidCipherSuites, err, cliflag.PreferredTLSCipherNames())
	}
	return suites, nil
}

func convertCurvePreferences(curveIDs []int32) ([]tls.CurveID, error) {
	curves, err := cliflag.TLSCurvePreferences(curveIDs)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidCurvePreferences, err)
	}
	return curves, nil
}

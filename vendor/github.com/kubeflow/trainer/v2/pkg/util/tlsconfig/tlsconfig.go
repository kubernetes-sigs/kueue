/*
Copyright The Kubeflow Authors.

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
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation/field"
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/klog/v2"

	configapi "github.com/kubeflow/trainer/v2/pkg/apis/config/v1alpha1"
)

// Apply configures c from the TLS options in the Configuration API. It is the
// single entry point for translating configapi.TLSOptions into a *tls.Config,
// so the metrics, webhook, and status servers all resolve TLS identically.
//
// A nil opts is valid and yields the secure defaults.
func Apply(c *tls.Config, opts *configapi.TLSOptions) {
	if opts != nil && len(opts.NextProtos) > 0 {
		c.NextProtos = opts.NextProtos
	} else {
		// HTTP/2 is disabled by default to mitigate the Rapid Reset CVEs
		// (CVE-2023-44487 and CVE-2023-39325). For more information see:
		// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
		// - https://github.com/advisories/GHSA-4374-p667-p6c8
		c.NextProtos = []string{"http/1.1"}
	}

	if opts == nil {
		return
	}

	if opts.MinVersion != "" {
		if v := parseTLSVersion(opts.MinVersion); v != 0 {
			c.MinVersion = v
		}
	}
	if len(opts.CipherSuites) > 0 {
		if ids := parseCipherSuiteIDs(opts.CipherSuites); len(ids) > 0 {
			c.CipherSuites = ids
		}
	}
	if len(opts.CurvePreferences) > 0 {
		if curves, err := parseCurveIDs(opts.CurvePreferences); err != nil {
			klog.Warningf("Invalid TLS curve preferences %v, using Go defaults: %v", opts.CurvePreferences, err)
		} else {
			c.CurvePreferences = curves
		}
	}
}

// parseTLSVersion converts a TLS version string to its uint16 constant.
// It logs a warning and returns 0 if the version is not recognized, leaving
// the Go default in place. Validate rejects unrecognized versions at
// configuration load time.
func parseTLSVersion(version string) uint16 {
	if v, ok := supportedTLSVersions[version]; ok {
		return v
	}
	klog.Warningf("Unrecognized TLS version %q, using Go default", version)
	return 0
}

// supportedTLSVersions maps the supported TLS version strings to their
// crypto/tls constants.
var supportedTLSVersions = map[string]uint16{
	configapi.TLSVersion10: tls.VersionTLS10,
	configapi.TLSVersion11: tls.VersionTLS11,
	configapi.TLSVersion12: tls.VersionTLS12,
	configapi.TLSVersion13: tls.VersionTLS13,
}

// supportedTLSVersionNames returns the recognized TLS version strings,
// sorted, for inclusion in validation errors.
func supportedTLSVersionNames() []string {
	names := make([]string, 0, len(supportedTLSVersions))
	for name := range supportedTLSVersions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// parseCipherSuiteIDs converts cipher suite name strings to their uint16 IDs.
// Unrecognized names and RC4 suites are ignored with a warning. Other insecure
// suites are accepted, since operators may still need them for legacy clients,
// but warn loudly. Recognition is enforced earlier, at configuration load time, by Validate.
func parseCipherSuiteIDs(names []string) []uint16 {
	ids := make([]uint16, 0, len(names))
	for _, name := range names {
		if isRC4Suite(name) {
			klog.Warningf("Cipher suite %q is prohibited by RFC 7465, ignoring", name)
			continue
		}
		if id, recognized, insecure := lookupCipherSuiteID(name); recognized {
			if insecure {
				klog.Warningf("Cipher suite %q is insecure and should not be used in production", name)
			}
			ids = append(ids, id)
		} else {
			klog.Warningf("Unrecognized cipher suite %q, ignoring", name)
		}
	}
	return ids
}

// lookupCipherSuiteID resolves a Go crypto/tls cipher suite name to its ID.
// recognized reports whether the name is known to this build; insecure
// reports whether the suite is on Go's insecure list.
func lookupCipherSuiteID(name string) (id uint16, recognized, insecure bool) {
	for _, cs := range tls.CipherSuites() {
		if cs.Name == name {
			return cs.ID, true, false
		}
	}
	for _, cs := range tls.InsecureCipherSuites() {
		if cs.Name == name {
			return cs.ID, true, true
		}
	}
	return 0, false, false
}

// isRC4Suite reports whether the name is one of Go's RC4 cipher suites.
// RC4 is negotiable by this build but categorically broken: RFC 7465
// prohibits it, so it is rejected outright rather than accepted with the
// legacy-suite warning.
func isRC4Suite(name string) bool {
	switch name {
	case "TLS_RSA_WITH_RC4_128_SHA", "TLS_ECDHE_RSA_WITH_RC4_128_SHA", "TLS_ECDHE_ECDSA_WITH_RC4_128_SHA":
		return true
	}
	return false
}

// parseCurveIDs converts IANA TLS Supported Group IDs to their tls.CurveID
// values and rejects IDs not supported by the current Go version.
func parseCurveIDs(ids []int32) ([]tls.CurveID, error) {
	return cliflag.TLSCurvePreferences(ids)
}

// Validate checks that the TLS option values are recognized by this build.
// It runs during configuration loading so that unrecognized values fail
// controller startup: the TLS option closures that call Apply cannot return
// errors, so recognition cannot be enforced at apply time.
func Validate(opts *configapi.TLSOptions, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	if opts == nil {
		return allErrs
	}

	if opts.MinVersion != "" {
		if _, ok := supportedTLSVersions[opts.MinVersion]; !ok {
			allErrs = append(allErrs, field.Invalid(fldPath.Child("minVersion"), opts.MinVersion,
				fmt.Sprintf("must be one of: %s", strings.Join(supportedTLSVersionNames(), ", "))))
		}
	}

	for i, name := range opts.CipherSuites {
		if isRC4Suite(name) {
			allErrs = append(allErrs, field.Invalid(fldPath.Child("cipherSuites").Index(i), name,
				"RC4 cipher suites are prohibited by RFC 7465"))
			continue
		}
		if _, recognized, _ := lookupCipherSuiteID(name); !recognized {
			allErrs = append(allErrs, field.Invalid(fldPath.Child("cipherSuites").Index(i), name,
				"must be a Go crypto/tls cipher suite name (see https://pkg.go.dev/crypto/tls#CipherSuites)"))
		}
	}

	if _, err := parseCurveIDs(opts.CurvePreferences); err != nil {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("curvePreferences"), opts.CurvePreferences, err.Error()))
	}

	return allErrs
}

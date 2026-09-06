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
}

// parseTLSVersion converts a TLS version string to its uint16 constant.
// It logs a warning and returns 0 if the version is not recognized, which
// leaves the Go default in place rather than failing the server startup.
func parseTLSVersion(version string) uint16 {
	switch version {
	case configapi.TLSVersion10:
		return tls.VersionTLS10
	case configapi.TLSVersion11:
		return tls.VersionTLS11
	case configapi.TLSVersion12:
		return tls.VersionTLS12
	case configapi.TLSVersion13:
		return tls.VersionTLS13
	default:
		klog.Warningf("Unrecognized TLS version %q, using Go default", version)
		return 0
	}
}

// parseCipherSuiteIDs converts cipher suite name strings to their uint16 IDs.
// Unrecognized names are ignored with a warning. Insecure suites are accepted,
// since operators may still need them for legacy clients, but warn loudly.
func parseCipherSuiteIDs(names []string) []uint16 {
	secure := make(map[string]uint16, len(tls.CipherSuites()))
	for _, cs := range tls.CipherSuites() {
		secure[cs.Name] = cs.ID
	}
	insecure := make(map[string]uint16, len(tls.InsecureCipherSuites()))
	for _, cs := range tls.InsecureCipherSuites() {
		insecure[cs.Name] = cs.ID
	}

	ids := make([]uint16, 0, len(names))
	for _, name := range names {
		switch id, ok := secure[name]; {
		case ok:
			ids = append(ids, id)
		default:
			if id, ok := insecure[name]; ok {
				klog.Warningf("Cipher suite %q is insecure and should not be used in production", name)
				ids = append(ids, id)
			} else {
				klog.Warningf("Unrecognized cipher suite %q, ignoring", name)
			}
		}
	}
	return ids
}

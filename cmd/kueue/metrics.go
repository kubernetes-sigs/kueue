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

package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"net/netip"
	"path/filepath"

	"sigs.k8s.io/controller-runtime/pkg/certwatcher"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

type metricsOptions struct {
	authentication bool
}

func (o *metricsOptions) bindFlags(fs *flag.FlagSet) {
	fs.BoolVar(&o.authentication, "metrics-authentication", true,
		"Authenticate and authorize metrics requests using the Kubernetes API. "+
			"Disabling requires metrics.bindAddress to be an explicit loopback IP and port; HTTPS is always enabled.")
}

func (o *metricsOptions) serverOptions(bindAddress string) (metricsserver.Options, error) {
	options := metricsserver.Options{
		BindAddress:   bindAddress,
		SecureServing: true,
	}
	if o.authentication {
		options.FilterProvider = filters.WithAuthenticationAndAuthorization
	} else if bindAddress != "0" {
		// Validate before controller-runtime can default an empty address to a wildcard.
		address, err := netip.ParseAddrPort(bindAddress)
		if err != nil {
			return metricsserver.Options{}, fmt.Errorf(
				"--metrics-authentication=false requires metrics.bindAddress to be an explicit loopback IP and port (for example 127.0.0.1:8443 or [::1]:8443), got %q: %w",
				bindAddress, err,
			)
		}
		if !address.Addr().IsLoopback() {
			return metricsserver.Options{}, fmt.Errorf(
				"--metrics-authentication=false requires a loopback metrics.bindAddress (for example 127.0.0.1:8443 or [::1]:8443), got %q; use --metrics-authentication=true for other addresses",
				bindAddress,
			)
		}
	}
	return options, nil
}

func setupMetricsCertWatcher(options *metricsserver.Options, certPath string) (*certwatcher.CertWatcher, error) {
	watcher, err := certwatcher.New(filepath.Join(certPath, "tls.crt"), filepath.Join(certPath, "tls.key"))
	if err != nil {
		return nil, err
	}
	options.TLSOpts = append(options.TLSOpts, func(config *tls.Config) {
		config.GetCertificate = watcher.GetCertificate
	})
	return watcher, nil
}

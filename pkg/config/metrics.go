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

package config

import (
	"github.com/go-logr/logr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
)

// MetricsServerOptions constructs HTTPS metrics options after configuration validation.
func MetricsServerOptions(log logr.Logger, metrics configapi.ControllerMetrics) metricsserver.Options {
	options := metricsserver.Options{
		BindAddress:    metrics.BindAddress,
		SecureServing:  true,
		FilterProvider: filters.WithAuthenticationAndAuthorization,
	}
	if ptr.Deref(metrics.AccessControlMode, configapi.MetricsAccessControlModeDelegated) == configapi.MetricsAccessControlModeNone {
		options.FilterProvider = nil
		if metrics.BindAddress != "0" {
			log.Info(
				"WARNING: Metrics authentication and authorization are disabled; metrics may expose tenant information to other processes sharing the network namespace",
				"bindAddress",
				metrics.BindAddress,
			)
		}
	}
	return options
}

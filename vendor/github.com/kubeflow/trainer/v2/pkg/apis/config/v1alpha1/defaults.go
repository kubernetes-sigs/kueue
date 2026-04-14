/*
Copyright 2025 The Kubeflow Authors.

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

package v1alpha1

import (
	"k8s.io/utils/ptr"
)

// SetDefaults_Configuration sets default values for Configuration.
func SetDefaults_Configuration(cfg *Configuration) {
	if cfg.Webhook.Port == nil {
		cfg.Webhook.Port = ptr.To(int32(9443))
	}
	if cfg.Metrics.BindAddress == "" {
		cfg.Metrics.BindAddress = ":8443"
	}
	if cfg.Metrics.SecureServing == nil {
		cfg.Metrics.SecureServing = ptr.To(true)
	}
	if cfg.Health.HealthProbeBindAddress == "" {
		cfg.Health.HealthProbeBindAddress = ":8081"
	}
	if cfg.Health.ReadinessEndpointName == "" {
		cfg.Health.ReadinessEndpointName = "readyz"
	}
	if cfg.Health.LivenessEndpointName == "" {
		cfg.Health.LivenessEndpointName = "healthz"
	}
	if cfg.CertManagement == nil {
		cfg.CertManagement = &CertManagement{}
	}
	if cfg.CertManagement.Enable == nil {
		cfg.CertManagement.Enable = ptr.To(true)
	}
	if cfg.CertManagement.WebhookServiceName == "" {
		cfg.CertManagement.WebhookServiceName = "kubeflow-trainer-controller-manager"
	}
	if cfg.CertManagement.WebhookSecretName == "" {
		cfg.CertManagement.WebhookSecretName = "kubeflow-trainer-webhook-cert"
	}
	if cfg.ClientConnection == nil {
		cfg.ClientConnection = &ClientConnection{}
	}
	if cfg.ClientConnection.QPS == nil {
		cfg.ClientConnection.QPS = ptr.To[float32](50)
	}
	if cfg.ClientConnection.Burst == nil {
		cfg.ClientConnection.Burst = ptr.To[int32](100)
	}
	if cfg.StatusServer == nil {
		cfg.StatusServer = &StatusServer{}
	}
	if cfg.StatusServer.Port == nil {
		cfg.StatusServer.Port = ptr.To[int32](10443)
	}
	if cfg.StatusServer.QPS == nil {
		cfg.StatusServer.QPS = ptr.To[float32](5)
	}
	if cfg.StatusServer.Burst == nil {
		cfg.StatusServer.Burst = ptr.To[int32](10)
	}
}

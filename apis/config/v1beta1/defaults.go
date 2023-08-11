/*
Copyright 2022 The Kubernetes Authors.

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

package v1beta1

import (
	"os"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/pkg/controller/jobs/job"
)

const (
	DefaultNamespace                      = "kueue-system"
	DefaultWebhookServiceName             = "kueue-webhook-service"
	DefaultWebhookSecretName              = "kueue-webhook-server-cert"
	DefaultWebhookPort            int     = 9443
	DefaultHealthProbeBindAddress         = ":8081"
	DefaultMetricsBindAddress             = ":8080"
	DefaultLeaderElectionID               = "c1f6bfd2.kueue.x-k8s.io"
	DefaultClientConnectionQPS    float32 = 20.0
	DefaultClientConnectionBurst  int32   = 30
	defaultPodsReadyTimeout               = 5 * time.Minute
)

func addDefaultingFuncs(scheme *runtime.Scheme) error {
	scheme.AddTypeDefaultingFunc(&Configuration{}, func(obj interface{}) {
		SetDefaults_Configuration(obj.(*Configuration))
	})
	return nil
}

func getOperatorNamespace() string {
	if data, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
		if ns := strings.TrimSpace(string(data)); len(ns) > 0 {
			return ns
		}
	}
	return DefaultNamespace
}

// SetDefaults_Configuration sets default values for ComponentConfig.
func SetDefaults_Configuration(cfg *Configuration) {
	if cfg.Namespace == nil {
		cfg.Namespace = ptr.To(getOperatorNamespace())
	}
	if cfg.Webhook.Port == nil {
		cfg.Webhook.Port = ptr.To(DefaultWebhookPort)
	}
	if len(cfg.Metrics.BindAddress) == 0 {
		cfg.Metrics.BindAddress = DefaultMetricsBindAddress
	}
	if len(cfg.Health.HealthProbeBindAddress) == 0 {
		cfg.Health.HealthProbeBindAddress = DefaultHealthProbeBindAddress
	}
	if cfg.LeaderElection != nil && cfg.LeaderElection.LeaderElect != nil &&
		*cfg.LeaderElection.LeaderElect && len(cfg.LeaderElection.ResourceName) == 0 {
		cfg.LeaderElection.ResourceName = DefaultLeaderElectionID
	}
	if cfg.InternalCertManagement == nil {
		cfg.InternalCertManagement = &InternalCertManagement{}
	}
	if cfg.InternalCertManagement.Enable == nil {
		cfg.InternalCertManagement.Enable = ptr.To(true)
	}
	if *cfg.InternalCertManagement.Enable {
		if cfg.InternalCertManagement.WebhookServiceName == nil {
			cfg.InternalCertManagement.WebhookServiceName = ptr.To(DefaultWebhookServiceName)
		}
		if cfg.InternalCertManagement.WebhookSecretName == nil {
			cfg.InternalCertManagement.WebhookSecretName = ptr.To(DefaultWebhookSecretName)
		}
	}
	if cfg.ClientConnection == nil {
		cfg.ClientConnection = &ClientConnection{}
	}
	if cfg.ClientConnection.QPS == nil {
		cfg.ClientConnection.QPS = ptr.To(DefaultClientConnectionQPS)
	}
	if cfg.ClientConnection.Burst == nil {
		cfg.ClientConnection.Burst = ptr.To(DefaultClientConnectionBurst)
	}
	if cfg.WaitForPodsReady != nil {
		if cfg.WaitForPodsReady.Timeout == nil {
			cfg.WaitForPodsReady.Timeout = &metav1.Duration{Duration: defaultPodsReadyTimeout}
		}
		if cfg.WaitForPodsReady.BlockAdmission == nil {
			defaultBlockAdmission := true
			if !cfg.WaitForPodsReady.Enable {
				defaultBlockAdmission = false
			}
			cfg.WaitForPodsReady.BlockAdmission = &defaultBlockAdmission
		}
	}
	if cfg.Integrations == nil {
		cfg.Integrations = &Integrations{}
	}
	if cfg.Integrations.Frameworks == nil {
		cfg.Integrations.Frameworks = []string{job.FrameworkName}
	}
}

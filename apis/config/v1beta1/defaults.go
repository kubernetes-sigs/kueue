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
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	configv1alpha1 "k8s.io/component-base/config/v1alpha1"
	"k8s.io/utils/ptr"
)

const (
	DefaultNamespace                                    = "kueue-system"
	DefaultWebhookServiceName                           = "kueue-webhook-service"
	DefaultWebhookSecretName                            = "kueue-webhook-server-cert"
	DefaultWebhookPort                                  = 9443
	DefaultHealthProbeBindAddress                       = ":8081"
	DefaultMetricsBindAddress                           = ":8080"
	DefaultLeaderElectionID                             = "c1f6bfd2.kueue.x-k8s.io"
	DefaultLeaderElectionLeaseDuration                  = 15 * time.Second
	DefaultLeaderElectionRenewDeadline                  = 10 * time.Second
	DefaultLeaderElectionRetryPeriod                    = 2 * time.Second
	DefaultClientConnectionQPS                  float32 = 20.0
	DefaultClientConnectionBurst                int32   = 30
	defaultPodsReadyTimeout                             = 5 * time.Minute
	DefaultQueueVisibilityUpdateIntervalSeconds int32   = 5
	DefaultClusterQueuesMaxCount                int32   = 10
	defaultJobFrameworkName                             = "batch/job"
	DefaultMultiKueueGCInterval                         = time.Minute
	DefaultMultiKueueOrigin                             = "multikueue"
	DefaultMultiKueueWorkerLostTimeout                  = 15 * time.Minute
	DefaultRequeuingBackoffBaseSeconds                  = 60
	DefaultRequeuingBackoffMaxSeconds                   = 3600
)

func getOperatorNamespace() string {
	if data, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
		if ns := strings.TrimSpace(string(data)); len(ns) > 0 {
			return ns
		}
	}
	return DefaultNamespace
}

// SetDefaults_Configuration sets default values for ComponentConfig.
//
//nolint:revive // format required by generated code for defaulting
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

	if cfg.LeaderElection == nil {
		cfg.LeaderElection = &configv1alpha1.LeaderElectionConfiguration{}
	}
	if len(cfg.LeaderElection.ResourceName) == 0 {
		cfg.LeaderElection.ResourceName = DefaultLeaderElectionID
	}
	if len(cfg.LeaderElection.ResourceLock) == 0 {
		// Default to Lease as component-base still defaults to endpoint resources
		// until core components migrate to using Leases. See k/k #80289 for more details.
		cfg.LeaderElection.ResourceLock = resourcelock.LeasesResourceLock
	}
	// Use the default LeaderElectionConfiguration options
	configv1alpha1.RecommendedDefaultLeaderElectionConfiguration(cfg.LeaderElection)

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
		if cfg.WaitForPodsReady.RequeuingStrategy == nil {
			cfg.WaitForPodsReady.RequeuingStrategy = &RequeuingStrategy{}
		}
		if cfg.WaitForPodsReady.RequeuingStrategy.Timestamp == nil {
			cfg.WaitForPodsReady.RequeuingStrategy.Timestamp = ptr.To(EvictionTimestamp)
		}
		if cfg.WaitForPodsReady.RequeuingStrategy.BackoffBaseSeconds == nil {
			cfg.WaitForPodsReady.RequeuingStrategy.BackoffBaseSeconds = ptr.To[int32](DefaultRequeuingBackoffBaseSeconds)
		}
		if cfg.WaitForPodsReady.RequeuingStrategy.BackoffMaxSeconds == nil {
			cfg.WaitForPodsReady.RequeuingStrategy.BackoffMaxSeconds = ptr.To[int32](DefaultRequeuingBackoffMaxSeconds)
		}
	}
	if cfg.Integrations == nil {
		cfg.Integrations = &Integrations{}
	}
	if cfg.Integrations.Frameworks == nil {
		cfg.Integrations.Frameworks = []string{defaultJobFrameworkName}
	}
	if cfg.QueueVisibility == nil {
		cfg.QueueVisibility = &QueueVisibility{}
	}
	if cfg.QueueVisibility.UpdateIntervalSeconds == 0 {
		cfg.QueueVisibility.UpdateIntervalSeconds = DefaultQueueVisibilityUpdateIntervalSeconds
	}
	if cfg.QueueVisibility.ClusterQueues == nil {
		cfg.QueueVisibility.ClusterQueues = &ClusterQueueVisibility{
			MaxCount: DefaultClusterQueuesMaxCount,
		}
	}

	if cfg.Integrations.PodOptions == nil {
		cfg.Integrations.PodOptions = &PodIntegrationOptions{}
	}

	if cfg.Integrations.PodOptions.NamespaceSelector == nil {
		matchExpressionsValues := []string{"kube-system", *cfg.Namespace}

		cfg.Integrations.PodOptions.NamespaceSelector = &metav1.LabelSelector{
			MatchExpressions: []metav1.LabelSelectorRequirement{
				{
					Key:      "kubernetes.io/metadata.name",
					Operator: metav1.LabelSelectorOpNotIn,
					Values:   matchExpressionsValues,
				},
			},
		}
	}

	if cfg.Integrations.PodOptions.PodSelector == nil {
		cfg.Integrations.PodOptions.PodSelector = &metav1.LabelSelector{}
	}

	if cfg.MultiKueue == nil {
		cfg.MultiKueue = &MultiKueue{}
	}
	if cfg.MultiKueue.GCInterval == nil {
		cfg.MultiKueue.GCInterval = &metav1.Duration{Duration: DefaultMultiKueueGCInterval}
	}
	if ptr.Deref(cfg.MultiKueue.Origin, "") == "" {
		cfg.MultiKueue.Origin = ptr.To(DefaultMultiKueueOrigin)
	}
	if cfg.MultiKueue.WorkerLostTimeout == nil {
		cfg.MultiKueue.WorkerLostTimeout = &metav1.Duration{Duration: DefaultMultiKueueWorkerLostTimeout}
	}
	if fs := cfg.FairSharing; fs != nil && fs.Enable && len(fs.PreemptionStrategies) == 0 {
		fs.PreemptionStrategies = []PreemptionStrategy{LessThanOrEqualToFinalShare, LessThanInitialShare}
	}
	if cfg.ObjectRetentionPolicies == nil {
		cfg.ObjectRetentionPolicies = &ObjectRetentionPolicies{}
	}
}

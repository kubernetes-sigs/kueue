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

package v1beta1

import (
	"cmp"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
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
	DefaultWebhookCertDir                               = "/tmp/k8s-webhook-server/serving-certs"
	DefaultHealthProbeBindAddress                       = ":8081"
	DefaultMetricsBindAddress                           = ":8443"
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
	DefaultResourceTransformationStrategy               = Retain
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
	cfg.Namespace = cmp.Or(cfg.Namespace, ptr.To(getOperatorNamespace()))
	cfg.Webhook.Port = cmp.Or(cfg.Webhook.Port, ptr.To(DefaultWebhookPort))
	cfg.Webhook.CertDir = cmp.Or(cfg.Webhook.CertDir, DefaultWebhookCertDir)
	cfg.Metrics.BindAddress = cmp.Or(cfg.Metrics.BindAddress, DefaultMetricsBindAddress)
	cfg.Health.HealthProbeBindAddress = cmp.Or(cfg.Health.HealthProbeBindAddress, DefaultHealthProbeBindAddress)
	cfg.LeaderElection = cmp.Or(cfg.LeaderElection, &configv1alpha1.LeaderElectionConfiguration{})
	cfg.LeaderElection.ResourceName = cmp.Or(cfg.LeaderElection.ResourceName, DefaultLeaderElectionID)

	// Default to Lease as component-base still defaults to endpoint resources
	// until core components migrate to using Leases. See k/k #80289 for more details.
	cfg.LeaderElection.ResourceLock = cmp.Or(cfg.LeaderElection.ResourceLock, resourcelock.LeasesResourceLock)

	// Use the default LeaderElectionConfiguration options
	configv1alpha1.RecommendedDefaultLeaderElectionConfiguration(cfg.LeaderElection)

	cfg.InternalCertManagement = cmp.Or(cfg.InternalCertManagement, &InternalCertManagement{})
	cfg.InternalCertManagement.Enable = cmp.Or(cfg.InternalCertManagement.Enable, ptr.To(true))
	if *cfg.InternalCertManagement.Enable {
		cfg.InternalCertManagement.WebhookServiceName = cmp.Or(cfg.InternalCertManagement.WebhookServiceName, ptr.To(DefaultWebhookServiceName))
		cfg.InternalCertManagement.WebhookSecretName = cmp.Or(cfg.InternalCertManagement.WebhookSecretName, ptr.To(DefaultWebhookSecretName))
	}

	cfg.ClientConnection = cmp.Or(cfg.ClientConnection, &ClientConnection{})
	cfg.ClientConnection.QPS = cmp.Or(cfg.ClientConnection.QPS, ptr.To(DefaultClientConnectionQPS))
	cfg.ClientConnection.Burst = cmp.Or(cfg.ClientConnection.Burst, ptr.To(DefaultClientConnectionBurst))

	cfg.WaitForPodsReady = cmp.Or(cfg.WaitForPodsReady, &WaitForPodsReady{Enable: false})
	if cfg.WaitForPodsReady.Enable {
		cfg.WaitForPodsReady.Timeout = cmp.Or(cfg.WaitForPodsReady.Timeout, &metav1.Duration{Duration: defaultPodsReadyTimeout})
		cfg.WaitForPodsReady.BlockAdmission = cmp.Or(cfg.WaitForPodsReady.BlockAdmission, &cfg.WaitForPodsReady.Enable)
		cfg.WaitForPodsReady.RequeuingStrategy = cmp.Or(cfg.WaitForPodsReady.RequeuingStrategy, &RequeuingStrategy{})
		cfg.WaitForPodsReady.RequeuingStrategy.Timestamp = cmp.Or(cfg.WaitForPodsReady.RequeuingStrategy.Timestamp, ptr.To(EvictionTimestamp))
		cfg.WaitForPodsReady.RequeuingStrategy.BackoffBaseSeconds = cmp.Or(cfg.WaitForPodsReady.RequeuingStrategy.BackoffBaseSeconds, ptr.To[int32](DefaultRequeuingBackoffBaseSeconds))
		cfg.WaitForPodsReady.RequeuingStrategy.BackoffMaxSeconds = cmp.Or(cfg.WaitForPodsReady.RequeuingStrategy.BackoffMaxSeconds, ptr.To[int32](DefaultRequeuingBackoffMaxSeconds))
	}

	cfg.Integrations = cmp.Or(cfg.Integrations, &Integrations{})
	if len(cfg.Integrations.Frameworks) == 0 {
		cfg.Integrations.Frameworks = []string{defaultJobFrameworkName}
	}

	cfg.QueueVisibility = cmp.Or(cfg.QueueVisibility, &QueueVisibility{})
	cfg.QueueVisibility.UpdateIntervalSeconds = cmp.Or(cfg.QueueVisibility.UpdateIntervalSeconds, DefaultQueueVisibilityUpdateIntervalSeconds)
	cfg.QueueVisibility.ClusterQueues = cmp.Or(cfg.QueueVisibility.ClusterQueues, &ClusterQueueVisibility{
		MaxCount: DefaultClusterQueuesMaxCount,
	})

	cfg.ManagedJobsNamespaceSelector = cmp.Or(cfg.ManagedJobsNamespaceSelector, &metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{
			{
				Key:      corev1.LabelMetadataName,
				Operator: metav1.LabelSelectorOpNotIn,
				Values:   []string{"kube-system", *cfg.Namespace},
			},
		},
	})

	cfg.MultiKueue = cmp.Or(cfg.MultiKueue, &MultiKueue{})
	cfg.MultiKueue.GCInterval = cmp.Or(cfg.MultiKueue.GCInterval, &metav1.Duration{Duration: DefaultMultiKueueGCInterval})
	cfg.MultiKueue.Origin = ptr.To(cmp.Or(ptr.Deref(cfg.MultiKueue.Origin, ""), DefaultMultiKueueOrigin))
	cfg.MultiKueue.WorkerLostTimeout = cmp.Or(cfg.MultiKueue.WorkerLostTimeout, &metav1.Duration{Duration: DefaultMultiKueueWorkerLostTimeout})
	cfg.MultiKueue.DispatcherName = cmp.Or(cfg.MultiKueue.DispatcherName, ptr.To(MultiKueueDispatcherModeAllAtOnce))

	if fs := cfg.FairSharing; fs != nil && fs.Enable && len(fs.PreemptionStrategies) == 0 {
		fs.PreemptionStrategies = []PreemptionStrategy{LessThanOrEqualToFinalShare, LessThanInitialShare}
	}
	if afs := cfg.AdmissionFairSharing; afs != nil {
		afs.UsageSamplingInterval.Duration = cmp.Or(afs.UsageSamplingInterval.Duration, 5*time.Minute)
	}

	if cfg.Resources != nil {
		for idx := range cfg.Resources.Transformations {
			cfg.Resources.Transformations[idx].Strategy = ptr.To(cmp.Or(ptr.Deref(cfg.Resources.Transformations[idx].Strategy, ""), DefaultResourceTransformationStrategy))
		}
	}
}

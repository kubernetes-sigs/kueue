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
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	componentconfigv1alpha1 "k8s.io/component-base/config/v1alpha1"
	"k8s.io/utils/ptr"
)

const (
	overwriteNamespace              = "kueue-tenant-a"
	overwriteWebhookPort            = 9444
	overwriteMetricBindAddress      = ":38081"
	overwriteHealthProbeBindAddress = ":38080"
	overwriteLeaderElectionID       = "foo.kueue.x-k8s.io"
)

func TestSetDefaults_Configuration(t *testing.T) {
	defaultCtrlManagerConfigurationSpec := ControllerManager{
		LeaderElection: &componentconfigv1alpha1.LeaderElectionConfiguration{
			LeaderElect:   ptr.To(true),
			LeaseDuration: metav1.Duration{Duration: DefaultLeaderElectionLeaseDuration},
			RenewDeadline: metav1.Duration{Duration: DefaultLeaderElectionRenewDeadline},
			RetryPeriod:   metav1.Duration{Duration: DefaultLeaderElectionRetryPeriod},
			ResourceLock:  "leases",
			ResourceName:  "c1f6bfd2.kueue.x-k8s.io",
		},
		Webhook: ControllerWebhook{
			Port: ptr.To(DefaultWebhookPort),
		},
		Metrics: ControllerMetrics{
			BindAddress: DefaultMetricsBindAddress,
		},
		Health: ControllerHealth{
			HealthProbeBindAddress: DefaultHealthProbeBindAddress,
		},
	}
	defaultClientConnection := &ClientConnection{
		QPS:   ptr.To(DefaultClientConnectionQPS),
		Burst: ptr.To(DefaultClientConnectionBurst),
	}
	defaultIntegrations := &Integrations{
		Frameworks: []string{defaultJobFrameworkName},
		PodOptions: &PodIntegrationOptions{
			NamespaceSelector: &metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{
						Key:      "kubernetes.io/metadata.name",
						Operator: metav1.LabelSelectorOpNotIn,
						Values:   []string{"kube-system", "kueue-system"},
					},
				},
			},
			PodSelector: &metav1.LabelSelector{},
		},
	}
	defaultQueueVisibility := &QueueVisibility{
		UpdateIntervalSeconds: DefaultQueueVisibilityUpdateIntervalSeconds,
		ClusterQueues: &ClusterQueueVisibility{
			MaxCount: 10,
		},
	}

	overwriteNamespaceIntegrations := &Integrations{
		Frameworks: []string{defaultJobFrameworkName},
		PodOptions: &PodIntegrationOptions{
			NamespaceSelector: &metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{
						Key:      "kubernetes.io/metadata.name",
						Operator: metav1.LabelSelectorOpNotIn,
						Values:   []string{"kube-system", overwriteNamespace},
					},
				},
			},
			PodSelector: &metav1.LabelSelector{},
		},
	}

	defaultMultiKueue := &MultiKueue{
		GCInterval:        &metav1.Duration{Duration: DefaultMultiKueueGCInterval},
		Origin:            ptr.To(DefaultMultiKueueOrigin),
		WorkerLostTimeout: &metav1.Duration{Duration: DefaultMultiKueueWorkerLostTimeout},
	}

	defaultObjectRetentionPolicies := &ObjectRetentionPolicies{
		FinishedWorkloadRetention: nil,
	}

	podsReadyTimeoutTimeout := metav1.Duration{Duration: defaultPodsReadyTimeout}
	podsReadyTimeoutOverwrite := metav1.Duration{Duration: time.Minute}

	testCases := map[string]struct {
		original *Configuration
		want     *Configuration
	}{
		"defaulting namespace": {
			original: &Configuration{
				InternalCertManagement: &InternalCertManagement{
					Enable: ptr.To(false),
				},
			},
			want: &Configuration{
				Namespace:         ptr.To(DefaultNamespace),
				ControllerManager: defaultCtrlManagerConfigurationSpec,
				InternalCertManagement: &InternalCertManagement{
					Enable: ptr.To(false),
				},
				ClientConnection:        defaultClientConnection,
				Integrations:            defaultIntegrations,
				QueueVisibility:         defaultQueueVisibility,
				MultiKueue:              defaultMultiKueue,
				ObjectRetentionPolicies: defaultObjectRetentionPolicies,
			},
		},
		"defaulting ControllerManager": {
			original: &Configuration{
				ControllerManager: ControllerManager{
					LeaderElection: &componentconfigv1alpha1.LeaderElectionConfiguration{
						LeaderElect: ptr.To(true),
					},
				},
				InternalCertManagement: &InternalCertManagement{
					Enable: ptr.To(false),
				},
			},
			want: &Configuration{
				Namespace: ptr.To(DefaultNamespace),
				ControllerManager: ControllerManager{
					Webhook: ControllerWebhook{
						Port: ptr.To(DefaultWebhookPort),
					},
					Metrics: ControllerMetrics{
						BindAddress: DefaultMetricsBindAddress,
					},
					Health: ControllerHealth{
						HealthProbeBindAddress: DefaultHealthProbeBindAddress,
					},
					LeaderElection: &componentconfigv1alpha1.LeaderElectionConfiguration{
						LeaderElect:   ptr.To(true),
						LeaseDuration: metav1.Duration{Duration: DefaultLeaderElectionLeaseDuration},
						RenewDeadline: metav1.Duration{Duration: DefaultLeaderElectionRenewDeadline},
						RetryPeriod:   metav1.Duration{Duration: DefaultLeaderElectionRetryPeriod},
						ResourceLock:  "leases",
						ResourceName:  DefaultLeaderElectionID,
					},
				},
				InternalCertManagement: &InternalCertManagement{
					Enable: ptr.To(false),
				},
				ClientConnection:        defaultClientConnection,
				Integrations:            defaultIntegrations,
				QueueVisibility:         defaultQueueVisibility,
				MultiKueue:              defaultMultiKueue,
				ObjectRetentionPolicies: defaultObjectRetentionPolicies,
			},
		},
		"should not default ControllerManager": {
			original: &Configuration{
				ControllerManager: ControllerManager{
					Webhook: ControllerWebhook{
						Port: ptr.To(overwriteWebhookPort),
					},
					Metrics: ControllerMetrics{
						BindAddress: overwriteMetricBindAddress,
					},
					Health: ControllerHealth{
						HealthProbeBindAddress: overwriteHealthProbeBindAddress,
					},
					LeaderElection: &componentconfigv1alpha1.LeaderElectionConfiguration{
						LeaderElect:   ptr.To(true),
						LeaseDuration: metav1.Duration{Duration: DefaultLeaderElectionLeaseDuration},
						RenewDeadline: metav1.Duration{Duration: DefaultLeaderElectionRenewDeadline},
						RetryPeriod:   metav1.Duration{Duration: DefaultLeaderElectionRetryPeriod},
						ResourceLock:  "leases",
						ResourceName:  overwriteLeaderElectionID,
					},
				},
				InternalCertManagement: &InternalCertManagement{
					Enable: ptr.To(false),
				},
				Integrations:    defaultIntegrations,
				QueueVisibility: defaultQueueVisibility,
			},
			want: &Configuration{
				Namespace: ptr.To(DefaultNamespace),
				ControllerManager: ControllerManager{
					Webhook: ControllerWebhook{
						Port: ptr.To(overwriteWebhookPort),
					},
					Metrics: ControllerMetrics{
						BindAddress: overwriteMetricBindAddress,
					},
					Health: ControllerHealth{
						HealthProbeBindAddress: overwriteHealthProbeBindAddress,
					},
					LeaderElection: &componentconfigv1alpha1.LeaderElectionConfiguration{
						LeaderElect:   ptr.To(true),
						LeaseDuration: metav1.Duration{Duration: DefaultLeaderElectionLeaseDuration},
						RenewDeadline: metav1.Duration{Duration: DefaultLeaderElectionRenewDeadline},
						RetryPeriod:   metav1.Duration{Duration: DefaultLeaderElectionRetryPeriod},
						ResourceLock:  "leases",
						ResourceName:  overwriteLeaderElectionID,
					},
				},
				InternalCertManagement: &InternalCertManagement{
					Enable: ptr.To(false),
				},
				ClientConnection:        defaultClientConnection,
				Integrations:            defaultIntegrations,
				QueueVisibility:         defaultQueueVisibility,
				MultiKueue:              defaultMultiKueue,
				ObjectRetentionPolicies: defaultObjectRetentionPolicies,
			},
		},
		"should not set LeaderElectionID": {
			original: &Configuration{
				ControllerManager: ControllerManager{
					LeaderElection: &componentconfigv1alpha1.LeaderElectionConfiguration{
						LeaderElect: ptr.To(false),
					},
				},
				InternalCertManagement: &InternalCertManagement{
					Enable: ptr.To(false),
				},
			},
			want: &Configuration{
				Namespace: ptr.To(DefaultNamespace),
				ControllerManager: ControllerManager{
					Webhook: ControllerWebhook{
						Port: ptr.To(DefaultWebhookPort),
					},
					Metrics: ControllerMetrics{
						BindAddress: DefaultMetricsBindAddress,
					},
					Health: ControllerHealth{
						HealthProbeBindAddress: DefaultHealthProbeBindAddress,
					},
					LeaderElection: &componentconfigv1alpha1.LeaderElectionConfiguration{
						LeaderElect:   ptr.To(false),
						LeaseDuration: metav1.Duration{Duration: DefaultLeaderElectionLeaseDuration},
						RenewDeadline: metav1.Duration{Duration: DefaultLeaderElectionRenewDeadline},
						RetryPeriod:   metav1.Duration{Duration: DefaultLeaderElectionRetryPeriod},
						ResourceLock:  "leases",
						ResourceName:  "c1f6bfd2.kueue.x-k8s.io",
					},
				},
				InternalCertManagement: &InternalCertManagement{
					Enable: ptr.To(false),
				},
				ClientConnection:        defaultClientConnection,
				Integrations:            defaultIntegrations,
				QueueVisibility:         defaultQueueVisibility,
				MultiKueue:              defaultMultiKueue,
				ObjectRetentionPolicies: defaultObjectRetentionPolicies,
			},
		},
		"defaulting InternalCertManagement": {
			original: &Configuration{
				Namespace: ptr.To(overwriteNamespace),
			},
			want: &Configuration{
				Namespace:         ptr.To(overwriteNamespace),
				ControllerManager: defaultCtrlManagerConfigurationSpec,
				InternalCertManagement: &InternalCertManagement{
					Enable:             ptr.To(true),
					WebhookServiceName: ptr.To(DefaultWebhookServiceName),
					WebhookSecretName:  ptr.To(DefaultWebhookSecretName),
				},
				ClientConnection:        defaultClientConnection,
				Integrations:            overwriteNamespaceIntegrations,
				QueueVisibility:         defaultQueueVisibility,
				MultiKueue:              defaultMultiKueue,
				ObjectRetentionPolicies: defaultObjectRetentionPolicies,
			},
		},
		"should not default InternalCertManagement": {
			original: &Configuration{
				Namespace: ptr.To(overwriteNamespace),
				InternalCertManagement: &InternalCertManagement{
					Enable: ptr.To(false),
				},
			},
			want: &Configuration{
				Namespace:         ptr.To(overwriteNamespace),
				ControllerManager: defaultCtrlManagerConfigurationSpec,
				InternalCertManagement: &InternalCertManagement{
					Enable: ptr.To(false),
				},
				ClientConnection:        defaultClientConnection,
				Integrations:            overwriteNamespaceIntegrations,
				QueueVisibility:         defaultQueueVisibility,
				MultiKueue:              defaultMultiKueue,
				ObjectRetentionPolicies: defaultObjectRetentionPolicies,
			},
		},
		"should not default values in custom ClientConnection": {
			original: &Configuration{
				Namespace: ptr.To(overwriteNamespace),
				InternalCertManagement: &InternalCertManagement{
					Enable: ptr.To(false),
				},
				ClientConnection: &ClientConnection{
					QPS:   ptr.To[float32](123.0),
					Burst: ptr.To[int32](456),
				},
			},
			want: &Configuration{
				Namespace:         ptr.To(overwriteNamespace),
				ControllerManager: defaultCtrlManagerConfigurationSpec,
				InternalCertManagement: &InternalCertManagement{
					Enable: ptr.To(false),
				},
				ClientConnection: &ClientConnection{
					QPS:   ptr.To[float32](123.0),
					Burst: ptr.To[int32](456),
				},
				Integrations:            overwriteNamespaceIntegrations,
				QueueVisibility:         defaultQueueVisibility,
				MultiKueue:              defaultMultiKueue,
				ObjectRetentionPolicies: defaultObjectRetentionPolicies,
			},
		},
		"should default empty custom ClientConnection": {
			original: &Configuration{
				Namespace: ptr.To(overwriteNamespace),
				InternalCertManagement: &InternalCertManagement{
					Enable: ptr.To(false),
				},
				ClientConnection: &ClientConnection{},
			},
			want: &Configuration{
				Namespace:         ptr.To(overwriteNamespace),
				ControllerManager: defaultCtrlManagerConfigurationSpec,
				InternalCertManagement: &InternalCertManagement{
					Enable: ptr.To(false),
				},
				ClientConnection:        defaultClientConnection,
				Integrations:            overwriteNamespaceIntegrations,
				QueueVisibility:         defaultQueueVisibility,
				MultiKueue:              defaultMultiKueue,
				ObjectRetentionPolicies: defaultObjectRetentionPolicies,
			},
		},
		"defaulting waitForPodsReady values": {
			original: &Configuration{
				WaitForPodsReady: &WaitForPodsReady{
					Enable: true,
				},
				InternalCertManagement: &InternalCertManagement{
					Enable: ptr.To(false),
				},
			},
			want: &Configuration{
				WaitForPodsReady: &WaitForPodsReady{
					Enable:         true,
					BlockAdmission: ptr.To(true),
					Timeout:        &podsReadyTimeoutTimeout,
					RequeuingStrategy: &RequeuingStrategy{
						Timestamp:          ptr.To(EvictionTimestamp),
						BackoffBaseSeconds: ptr.To[int32](DefaultRequeuingBackoffBaseSeconds),
						BackoffMaxSeconds:  ptr.To[int32](DefaultRequeuingBackoffMaxSeconds),
					},
				},
				Namespace:         ptr.To(DefaultNamespace),
				ControllerManager: defaultCtrlManagerConfigurationSpec,
				InternalCertManagement: &InternalCertManagement{
					Enable: ptr.To(false),
				},
				ClientConnection:        defaultClientConnection,
				Integrations:            defaultIntegrations,
				QueueVisibility:         defaultQueueVisibility,
				MultiKueue:              defaultMultiKueue,
				ObjectRetentionPolicies: defaultObjectRetentionPolicies,
			},
		},
		"set waitForPodsReady.blockAdmission to false when enable is false": {
			original: &Configuration{
				WaitForPodsReady: &WaitForPodsReady{
					Enable: false,
				},
				InternalCertManagement: &InternalCertManagement{
					Enable: ptr.To(false),
				},
			},
			want: &Configuration{
				WaitForPodsReady: &WaitForPodsReady{
					Enable:         false,
					BlockAdmission: ptr.To(false),
					Timeout:        &podsReadyTimeoutTimeout,
					RequeuingStrategy: &RequeuingStrategy{
						Timestamp:          ptr.To(EvictionTimestamp),
						BackoffBaseSeconds: ptr.To[int32](DefaultRequeuingBackoffBaseSeconds),
						BackoffMaxSeconds:  ptr.To[int32](DefaultRequeuingBackoffMaxSeconds),
					},
				},
				Namespace:         ptr.To(DefaultNamespace),
				ControllerManager: defaultCtrlManagerConfigurationSpec,
				InternalCertManagement: &InternalCertManagement{
					Enable: ptr.To(false),
				},
				ClientConnection:        defaultClientConnection,
				Integrations:            defaultIntegrations,
				QueueVisibility:         defaultQueueVisibility,
				MultiKueue:              defaultMultiKueue,
				ObjectRetentionPolicies: defaultObjectRetentionPolicies,
			},
		},
		"respecting provided waitForPodsReady values": {
			original: &Configuration{
				WaitForPodsReady: &WaitForPodsReady{
					Enable:  true,
					Timeout: &podsReadyTimeoutOverwrite,
					RequeuingStrategy: &RequeuingStrategy{
						Timestamp:          ptr.To(CreationTimestamp),
						BackoffBaseSeconds: ptr.To[int32](63),
						BackoffMaxSeconds:  ptr.To[int32](1800),
					},
				},
				InternalCertManagement: &InternalCertManagement{
					Enable: ptr.To(false),
				},
			},
			want: &Configuration{
				WaitForPodsReady: &WaitForPodsReady{
					Enable:         true,
					BlockAdmission: ptr.To(true),
					Timeout:        &podsReadyTimeoutOverwrite,
					RequeuingStrategy: &RequeuingStrategy{
						Timestamp:          ptr.To(CreationTimestamp),
						BackoffBaseSeconds: ptr.To[int32](63),
						BackoffMaxSeconds:  ptr.To[int32](1800),
					},
				},
				Namespace:         ptr.To(DefaultNamespace),
				ControllerManager: defaultCtrlManagerConfigurationSpec,
				InternalCertManagement: &InternalCertManagement{
					Enable: ptr.To(false),
				},
				ClientConnection:        defaultClientConnection,
				Integrations:            defaultIntegrations,
				QueueVisibility:         defaultQueueVisibility,
				MultiKueue:              defaultMultiKueue,
				ObjectRetentionPolicies: defaultObjectRetentionPolicies,
			},
		},
		"integrations": {
			original: &Configuration{
				InternalCertManagement: &InternalCertManagement{
					Enable: ptr.To(false),
				},
				Integrations: &Integrations{
					Frameworks: []string{"a", "b"},
				},
			},
			want: &Configuration{
				Namespace:         ptr.To(DefaultNamespace),
				ControllerManager: defaultCtrlManagerConfigurationSpec,
				InternalCertManagement: &InternalCertManagement{
					Enable: ptr.To(false),
				},
				ClientConnection: defaultClientConnection,
				Integrations: &Integrations{
					Frameworks: []string{"a", "b"},
					PodOptions: defaultIntegrations.PodOptions,
				},
				QueueVisibility:         defaultQueueVisibility,
				MultiKueue:              defaultMultiKueue,
				ObjectRetentionPolicies: defaultObjectRetentionPolicies,
			},
		},
		"queue visibility": {
			original: &Configuration{
				InternalCertManagement: &InternalCertManagement{
					Enable: ptr.To(false),
				},
				QueueVisibility: &QueueVisibility{
					UpdateIntervalSeconds: 10,
					ClusterQueues: &ClusterQueueVisibility{
						MaxCount: 0,
					},
				},
			},
			want: &Configuration{
				Namespace:         ptr.To(DefaultNamespace),
				ControllerManager: defaultCtrlManagerConfigurationSpec,
				InternalCertManagement: &InternalCertManagement{
					Enable: ptr.To(false),
				},
				ClientConnection: defaultClientConnection,
				Integrations:     defaultIntegrations,
				QueueVisibility: &QueueVisibility{
					UpdateIntervalSeconds: 10,
					ClusterQueues: &ClusterQueueVisibility{
						MaxCount: 0,
					},
				},
				MultiKueue:              defaultMultiKueue,
				ObjectRetentionPolicies: defaultObjectRetentionPolicies,
			},
		},
		"multiKueue": {
			original: &Configuration{
				InternalCertManagement: &InternalCertManagement{
					Enable: ptr.To(false),
				},
				MultiKueue: &MultiKueue{
					GCInterval:        &metav1.Duration{Duration: time.Second},
					Origin:            ptr.To("multikueue-manager1"),
					WorkerLostTimeout: &metav1.Duration{Duration: time.Minute},
				},
			},
			want: &Configuration{
				Namespace:         ptr.To(DefaultNamespace),
				ControllerManager: defaultCtrlManagerConfigurationSpec,
				InternalCertManagement: &InternalCertManagement{
					Enable: ptr.To(false),
				},
				ClientConnection: defaultClientConnection,
				Integrations:     defaultIntegrations,
				QueueVisibility:  defaultQueueVisibility,
				MultiKueue: &MultiKueue{
					GCInterval:        &metav1.Duration{Duration: time.Second},
					Origin:            ptr.To("multikueue-manager1"),
					WorkerLostTimeout: &metav1.Duration{Duration: time.Minute},
				},
				ObjectRetentionPolicies: defaultObjectRetentionPolicies,
			},
		},
		"multiKueue GCInterval 0": {
			original: &Configuration{
				InternalCertManagement: &InternalCertManagement{
					Enable: ptr.To(false),
				},
				MultiKueue: &MultiKueue{
					GCInterval: &metav1.Duration{},
					Origin:     ptr.To("multikueue-manager1"),
				},
			},
			want: &Configuration{
				Namespace:         ptr.To(DefaultNamespace),
				ControllerManager: defaultCtrlManagerConfigurationSpec,
				InternalCertManagement: &InternalCertManagement{
					Enable: ptr.To(false),
				},
				ClientConnection: defaultClientConnection,
				Integrations:     defaultIntegrations,
				QueueVisibility:  defaultQueueVisibility,
				MultiKueue: &MultiKueue{
					GCInterval:        &metav1.Duration{},
					Origin:            ptr.To("multikueue-manager1"),
					WorkerLostTimeout: &metav1.Duration{Duration: 15 * time.Minute},
				},
				ObjectRetentionPolicies: defaultObjectRetentionPolicies,
			},
		},
		"add default fair sharing configuration when enabled": {
			original: &Configuration{
				InternalCertManagement: &InternalCertManagement{
					Enable: ptr.To(false),
				},
				FairSharing: &FairSharing{
					Enable: true,
				},
			},
			want: &Configuration{
				Namespace:         ptr.To(DefaultNamespace),
				ControllerManager: defaultCtrlManagerConfigurationSpec,
				InternalCertManagement: &InternalCertManagement{
					Enable: ptr.To(false),
				},
				ClientConnection: defaultClientConnection,
				Integrations:     defaultIntegrations,
				QueueVisibility:  defaultQueueVisibility,
				MultiKueue:       defaultMultiKueue,
				FairSharing: &FairSharing{
					Enable:               true,
					PreemptionStrategies: []PreemptionStrategy{LessThanOrEqualToFinalShare, LessThanInitialShare},
				},
				ObjectRetentionPolicies: defaultObjectRetentionPolicies,
			},
		},
		"set object retention policy for finished workloads": {
			original: &Configuration{
				InternalCertManagement: &InternalCertManagement{
					Enable: ptr.To(false),
				},
				ObjectRetentionPolicies: &ObjectRetentionPolicies{
					FinishedWorkloadRetention: &metav1.Duration{Duration: 30 * time.Minute},
				},
			},
			want: &Configuration{
				Namespace:         ptr.To(DefaultNamespace),
				ControllerManager: defaultCtrlManagerConfigurationSpec,
				InternalCertManagement: &InternalCertManagement{
					Enable: ptr.To(false),
				},
				ClientConnection: defaultClientConnection,
				Integrations:     defaultIntegrations,
				QueueVisibility:  defaultQueueVisibility,
				MultiKueue:       defaultMultiKueue,
				ObjectRetentionPolicies: &ObjectRetentionPolicies{
					FinishedWorkloadRetention: &metav1.Duration{Duration: 30 * time.Minute},
				},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			SetDefaults_Configuration(tc.original)
			if diff := cmp.Diff(tc.want, tc.original); diff != "" {
				t.Errorf("unexpected error (-want,+got):\n%s", diff)
			}
		})
	}
}

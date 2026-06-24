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
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	componentconfigv1alpha1 "k8s.io/component-base/config/v1alpha1"
	"k8s.io/utils/ptr"
)

const (
	overwriteNamespace              = "kueue-tenant-a"
	overwriteWebhookPort            = 9444
	overwriteWebhookCertDir         = "/tmp/test"
	overwriteMetricBindAddress      = ":38081"
	overwriteHealthProbeBindAddress = ":38080"
	overwriteLeaderElectionID       = "foo.kueue.x-k8s.io"
)

func TestSetDefaults_Configuration(t *testing.T) {
	defaultCtrlManagerConfigurationSpec := ControllerManager{
		LeaderElection: &componentconfigv1alpha1.LeaderElectionConfiguration{
			LeaderElect:   new(true),
			LeaseDuration: metav1.Duration{Duration: DefaultLeaderElectionLeaseDuration},
			RenewDeadline: metav1.Duration{Duration: DefaultLeaderElectionRenewDeadline},
			RetryPeriod:   metav1.Duration{Duration: DefaultLeaderElectionRetryPeriod},
			ResourceLock:  "leases",
			ResourceName:  "c1f6bfd2.kueue.x-k8s.io",
		},
		Webhook: ControllerWebhook{
			Port:    new(DefaultWebhookPort),
			CertDir: DefaultWebhookCertDir,
		},
		Metrics: ControllerMetrics{
			BindAddress: DefaultMetricsBindAddress,
			LocalQueueMetrics: &LocalQueueMetrics{
				Enable: true,
			},
		},
		Health: ControllerHealth{
			HealthProbeBindAddress: DefaultHealthProbeBindAddress,
		},
	}
	defaultClientConnection := &ClientConnection{
		QPS:   new(DefaultClientConnectionQPS),
		Burst: new(DefaultClientConnectionBurst),
	}
	defaultIntegrations := &Integrations{
		Frameworks: []string{defaultJobFrameworkName},
	}
	defaultManagedJobsNamespaceSelector := &metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{
			{
				Key:      corev1.LabelMetadataName,
				Operator: metav1.LabelSelectorOpNotIn,
				Values:   []string{"kube-system", "kueue-system"},
			},
		},
	}

	overwriteNamespaceIntegrations := &Integrations{
		Frameworks: []string{defaultJobFrameworkName},
	}

	overwriteNamespaceSelector := &metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{
			{
				Key:      corev1.LabelMetadataName,
				Operator: metav1.LabelSelectorOpNotIn,
				Values:   []string{"kube-system", overwriteNamespace},
			},
		},
	}

	defaultMultiKueue := &MultiKueue{
		GCInterval:        &metav1.Duration{Duration: DefaultMultiKueueGCInterval},
		Origin:            new(DefaultMultiKueueOrigin),
		WorkerLostTimeout: &metav1.Duration{Duration: DefaultMultiKueueWorkerLostTimeout},
		DispatcherName:    new(MultiKueueDispatcherModeAllAtOnce),
	}

	podsReadyTimeout := metav1.Duration{Duration: defaultPodsReadyTimeout}
	podsReadyTimeoutOverwrite := metav1.Duration{Duration: time.Minute}

	testCases := map[string]struct {
		original *Configuration
		want     *Configuration
	}{
		"defaulting namespace": {
			original: &Configuration{
				InternalCertManagement: &InternalCertManagement{
					Enable: new(false),
				},
			},
			want: &Configuration{
				Namespace:         new(DefaultNamespace),
				ControllerManager: defaultCtrlManagerConfigurationSpec,
				InternalCertManagement: &InternalCertManagement{
					Enable: new(false),
				},
				ClientConnection:             defaultClientConnection,
				Integrations:                 defaultIntegrations,
				MultiKueue:                   defaultMultiKueue,
				ManagedJobsNamespaceSelector: defaultManagedJobsNamespaceSelector,
				WaitForPodsReady:             &WaitForPodsReady{},
			},
		},
		"defaulting ControllerManager": {
			original: &Configuration{
				ControllerManager: ControllerManager{
					LeaderElection: &componentconfigv1alpha1.LeaderElectionConfiguration{
						LeaderElect: new(true),
					},
				},
				InternalCertManagement: &InternalCertManagement{
					Enable: new(false),
				},
			},
			want: &Configuration{
				Namespace: new(DefaultNamespace),
				ControllerManager: ControllerManager{
					Webhook: ControllerWebhook{
						Port:    new(DefaultWebhookPort),
						CertDir: DefaultWebhookCertDir,
					},
					Metrics: ControllerMetrics{
						BindAddress: DefaultMetricsBindAddress,
						LocalQueueMetrics: &LocalQueueMetrics{
							Enable: true,
						},
					},
					Health: ControllerHealth{
						HealthProbeBindAddress: DefaultHealthProbeBindAddress,
					},
					LeaderElection: &componentconfigv1alpha1.LeaderElectionConfiguration{
						LeaderElect:   new(true),
						LeaseDuration: metav1.Duration{Duration: DefaultLeaderElectionLeaseDuration},
						RenewDeadline: metav1.Duration{Duration: DefaultLeaderElectionRenewDeadline},
						RetryPeriod:   metav1.Duration{Duration: DefaultLeaderElectionRetryPeriod},
						ResourceLock:  "leases",
						ResourceName:  DefaultLeaderElectionID,
					},
				},
				InternalCertManagement: &InternalCertManagement{
					Enable: new(false),
				},
				ClientConnection:             defaultClientConnection,
				Integrations:                 defaultIntegrations,
				MultiKueue:                   defaultMultiKueue,
				ManagedJobsNamespaceSelector: defaultManagedJobsNamespaceSelector,
				WaitForPodsReady:             &WaitForPodsReady{},
			},
		},
		"should not default ControllerManager": {
			original: &Configuration{
				ControllerManager: ControllerManager{
					Webhook: ControllerWebhook{
						Port:    new(overwriteWebhookPort),
						CertDir: overwriteWebhookCertDir,
					},
					Metrics: ControllerMetrics{
						BindAddress: overwriteMetricBindAddress,
						LocalQueueMetrics: &LocalQueueMetrics{
							Enable: true,
						},
					},
					Health: ControllerHealth{
						HealthProbeBindAddress: overwriteHealthProbeBindAddress,
					},
					LeaderElection: &componentconfigv1alpha1.LeaderElectionConfiguration{
						LeaderElect:   new(true),
						LeaseDuration: metav1.Duration{Duration: DefaultLeaderElectionLeaseDuration},
						RenewDeadline: metav1.Duration{Duration: DefaultLeaderElectionRenewDeadline},
						RetryPeriod:   metav1.Duration{Duration: DefaultLeaderElectionRetryPeriod},
						ResourceLock:  "leases",
						ResourceName:  overwriteLeaderElectionID,
					},
				},
				InternalCertManagement: &InternalCertManagement{
					Enable: new(false),
				},
				Integrations: defaultIntegrations,
			},
			want: &Configuration{
				Namespace: new(DefaultNamespace),
				ControllerManager: ControllerManager{
					Webhook: ControllerWebhook{
						Port:    new(overwriteWebhookPort),
						CertDir: overwriteWebhookCertDir,
					},
					Metrics: ControllerMetrics{
						BindAddress: overwriteMetricBindAddress,
						LocalQueueMetrics: &LocalQueueMetrics{
							Enable: true,
						},
					},
					Health: ControllerHealth{
						HealthProbeBindAddress: overwriteHealthProbeBindAddress,
					},
					LeaderElection: &componentconfigv1alpha1.LeaderElectionConfiguration{
						LeaderElect:   new(true),
						LeaseDuration: metav1.Duration{Duration: DefaultLeaderElectionLeaseDuration},
						RenewDeadline: metav1.Duration{Duration: DefaultLeaderElectionRenewDeadline},
						RetryPeriod:   metav1.Duration{Duration: DefaultLeaderElectionRetryPeriod},
						ResourceLock:  "leases",
						ResourceName:  overwriteLeaderElectionID,
					},
				},
				InternalCertManagement: &InternalCertManagement{
					Enable: new(false),
				},
				ClientConnection:             defaultClientConnection,
				Integrations:                 defaultIntegrations,
				MultiKueue:                   defaultMultiKueue,
				ManagedJobsNamespaceSelector: defaultManagedJobsNamespaceSelector,
				WaitForPodsReady:             &WaitForPodsReady{},
			},
		},
		"should not set LeaderElectionID": {
			original: &Configuration{
				ControllerManager: ControllerManager{
					LeaderElection: &componentconfigv1alpha1.LeaderElectionConfiguration{
						LeaderElect: new(false),
					},
				},
				InternalCertManagement: &InternalCertManagement{
					Enable: new(false),
				},
			},
			want: &Configuration{
				Namespace: new(DefaultNamespace),
				ControllerManager: ControllerManager{
					Webhook: ControllerWebhook{
						Port:    new(DefaultWebhookPort),
						CertDir: DefaultWebhookCertDir,
					},
					Metrics: ControllerMetrics{
						BindAddress: DefaultMetricsBindAddress,
						LocalQueueMetrics: &LocalQueueMetrics{
							Enable: true,
						},
					},
					Health: ControllerHealth{
						HealthProbeBindAddress: DefaultHealthProbeBindAddress,
					},
					LeaderElection: &componentconfigv1alpha1.LeaderElectionConfiguration{
						LeaderElect:   new(false),
						LeaseDuration: metav1.Duration{Duration: DefaultLeaderElectionLeaseDuration},
						RenewDeadline: metav1.Duration{Duration: DefaultLeaderElectionRenewDeadline},
						RetryPeriod:   metav1.Duration{Duration: DefaultLeaderElectionRetryPeriod},
						ResourceLock:  "leases",
						ResourceName:  "c1f6bfd2.kueue.x-k8s.io",
					},
				},
				InternalCertManagement: &InternalCertManagement{
					Enable: new(false),
				},
				ClientConnection:             defaultClientConnection,
				Integrations:                 defaultIntegrations,
				MultiKueue:                   defaultMultiKueue,
				ManagedJobsNamespaceSelector: defaultManagedJobsNamespaceSelector,
				WaitForPodsReady:             &WaitForPodsReady{},
			},
		},
		"defaulting InternalCertManagement": {
			original: &Configuration{
				Namespace: new(overwriteNamespace),
			},
			want: &Configuration{
				Namespace:         new(overwriteNamespace),
				ControllerManager: defaultCtrlManagerConfigurationSpec,
				InternalCertManagement: &InternalCertManagement{
					Enable:             new(true),
					WebhookServiceName: new(DefaultWebhookServiceName),
					WebhookSecretName:  new(DefaultWebhookSecretName),
				},
				ClientConnection:             defaultClientConnection,
				Integrations:                 overwriteNamespaceIntegrations,
				MultiKueue:                   defaultMultiKueue,
				ManagedJobsNamespaceSelector: overwriteNamespaceSelector,
				WaitForPodsReady:             &WaitForPodsReady{},
			},
		},
		"should not default InternalCertManagement": {
			original: &Configuration{
				Namespace: new(overwriteNamespace),
				InternalCertManagement: &InternalCertManagement{
					Enable: new(false),
				},
			},
			want: &Configuration{
				Namespace:         new(overwriteNamespace),
				ControllerManager: defaultCtrlManagerConfigurationSpec,
				InternalCertManagement: &InternalCertManagement{
					Enable: new(false),
				},
				ClientConnection:             defaultClientConnection,
				Integrations:                 overwriteNamespaceIntegrations,
				MultiKueue:                   defaultMultiKueue,
				ManagedJobsNamespaceSelector: overwriteNamespaceSelector,
				WaitForPodsReady:             &WaitForPodsReady{},
			},
		},
		"should not default values in custom ClientConnection": {
			original: &Configuration{
				Namespace: new(overwriteNamespace),
				InternalCertManagement: &InternalCertManagement{
					Enable: new(false),
				},
				ClientConnection: &ClientConnection{
					QPS:   ptr.To[float32](123.0),
					Burst: ptr.To[int32](456),
				},
			},
			want: &Configuration{
				Namespace:         new(overwriteNamespace),
				ControllerManager: defaultCtrlManagerConfigurationSpec,
				InternalCertManagement: &InternalCertManagement{
					Enable: new(false),
				},
				ClientConnection: &ClientConnection{
					QPS:   ptr.To[float32](123.0),
					Burst: ptr.To[int32](456),
				},
				Integrations:                 overwriteNamespaceIntegrations,
				MultiKueue:                   defaultMultiKueue,
				ManagedJobsNamespaceSelector: overwriteNamespaceSelector,
				WaitForPodsReady:             &WaitForPodsReady{},
			},
		},
		"should default empty custom ClientConnection": {
			original: &Configuration{
				Namespace: new(overwriteNamespace),
				InternalCertManagement: &InternalCertManagement{
					Enable: new(false),
				},
				ClientConnection: &ClientConnection{},
			},
			want: &Configuration{
				Namespace:         new(overwriteNamespace),
				ControllerManager: defaultCtrlManagerConfigurationSpec,
				InternalCertManagement: &InternalCertManagement{
					Enable: new(false),
				},
				ClientConnection:             defaultClientConnection,
				Integrations:                 overwriteNamespaceIntegrations,
				MultiKueue:                   defaultMultiKueue,
				ManagedJobsNamespaceSelector: overwriteNamespaceSelector,
				WaitForPodsReady:             &WaitForPodsReady{},
			},
		},
		"defaulting waitForPodsReady values": {
			original: &Configuration{
				WaitForPodsReady: &WaitForPodsReady{
					Enable: true,
				},
				InternalCertManagement: &InternalCertManagement{
					Enable: new(false),
				},
			},
			want: &Configuration{
				WaitForPodsReady: &WaitForPodsReady{
					Enable:          true,
					BlockAdmission:  new(true),
					Timeout:         &podsReadyTimeout,
					RecoveryTimeout: &podsReadyTimeout,
					RequeuingStrategy: &RequeuingStrategy{
						Timestamp:          new(EvictionTimestamp),
						BackoffBaseSeconds: ptr.To[int32](DefaultRequeuingBackoffBaseSeconds),
						BackoffMaxSeconds:  ptr.To[int32](DefaultRequeuingBackoffMaxSeconds),
					},
				},
				Namespace:         new(DefaultNamespace),
				ControllerManager: defaultCtrlManagerConfigurationSpec,
				InternalCertManagement: &InternalCertManagement{
					Enable: new(false),
				},
				ClientConnection:             defaultClientConnection,
				Integrations:                 defaultIntegrations,
				MultiKueue:                   defaultMultiKueue,
				ManagedJobsNamespaceSelector: defaultManagedJobsNamespaceSelector,
			},
		},
		"set waitForPodsReady.blockAdmission to false, and waitForPodsReady.recoveryTimeout to nil when enable is false": {
			original: &Configuration{
				WaitForPodsReady: &WaitForPodsReady{
					Enable: false,
				},
				InternalCertManagement: &InternalCertManagement{
					Enable: new(false),
				},
			},
			want: &Configuration{
				WaitForPodsReady: &WaitForPodsReady{
					Enable: false,
				},
				Namespace:         new(DefaultNamespace),
				ControllerManager: defaultCtrlManagerConfigurationSpec,
				InternalCertManagement: &InternalCertManagement{
					Enable: new(false),
				},
				ClientConnection:             defaultClientConnection,
				Integrations:                 defaultIntegrations,
				MultiKueue:                   defaultMultiKueue,
				ManagedJobsNamespaceSelector: defaultManagedJobsNamespaceSelector,
			},
		},
		"respecting provided waitForPodsReady values": {
			original: &Configuration{
				WaitForPodsReady: &WaitForPodsReady{
					Enable:  true,
					Timeout: &podsReadyTimeoutOverwrite,
					RequeuingStrategy: &RequeuingStrategy{
						Timestamp:          new(CreationTimestamp),
						BackoffBaseSeconds: ptr.To[int32](63),
						BackoffMaxSeconds:  ptr.To[int32](1800),
					},
					RecoveryTimeout: &metav1.Duration{Duration: time.Minute},
				},
				InternalCertManagement: &InternalCertManagement{
					Enable: new(false),
				},
			},
			want: &Configuration{
				WaitForPodsReady: &WaitForPodsReady{
					Enable:          true,
					BlockAdmission:  new(true),
					Timeout:         &podsReadyTimeoutOverwrite,
					RecoveryTimeout: &metav1.Duration{Duration: time.Minute},
					RequeuingStrategy: &RequeuingStrategy{
						Timestamp:          new(CreationTimestamp),
						BackoffBaseSeconds: ptr.To[int32](63),
						BackoffMaxSeconds:  ptr.To[int32](1800),
					},
				},
				Namespace:         new(DefaultNamespace),
				ControllerManager: defaultCtrlManagerConfigurationSpec,
				InternalCertManagement: &InternalCertManagement{
					Enable: new(false),
				},
				ClientConnection:             defaultClientConnection,
				Integrations:                 defaultIntegrations,
				MultiKueue:                   defaultMultiKueue,
				ManagedJobsNamespaceSelector: defaultManagedJobsNamespaceSelector,
			},
		},
		"integrations": {
			original: &Configuration{
				InternalCertManagement: &InternalCertManagement{
					Enable: new(false),
				},
				Integrations: &Integrations{
					Frameworks: []string{"a", "b"},
				},
			},
			want: &Configuration{
				Namespace:         new(DefaultNamespace),
				ControllerManager: defaultCtrlManagerConfigurationSpec,
				InternalCertManagement: &InternalCertManagement{
					Enable: new(false),
				},
				ClientConnection: defaultClientConnection,
				Integrations: &Integrations{
					Frameworks: []string{"a", "b"},
				},
				MultiKueue:                   defaultMultiKueue,
				ManagedJobsNamespaceSelector: defaultManagedJobsNamespaceSelector,
				WaitForPodsReady:             &WaitForPodsReady{},
			},
		},
		"multiKueue": {
			original: &Configuration{
				InternalCertManagement: &InternalCertManagement{
					Enable: new(false),
				},
				MultiKueue: &MultiKueue{
					GCInterval:        &metav1.Duration{Duration: time.Second},
					Origin:            new("multikueue-manager1"),
					WorkerLostTimeout: &metav1.Duration{Duration: time.Minute},
					DispatcherName:    new(MultiKueueDispatcherModeIncremental),
				},
			},
			want: &Configuration{
				Namespace:         new(DefaultNamespace),
				ControllerManager: defaultCtrlManagerConfigurationSpec,
				InternalCertManagement: &InternalCertManagement{
					Enable: new(false),
				},
				ClientConnection: defaultClientConnection,
				Integrations:     defaultIntegrations,
				MultiKueue: &MultiKueue{
					GCInterval:        &metav1.Duration{Duration: time.Second},
					Origin:            new("multikueue-manager1"),
					WorkerLostTimeout: &metav1.Duration{Duration: time.Minute},
					DispatcherName:    new(MultiKueueDispatcherModeIncremental),
				},
				ManagedJobsNamespaceSelector: defaultManagedJobsNamespaceSelector,
				WaitForPodsReady:             &WaitForPodsReady{},
			},
		},
		"multiKueue origin is an empty value": {
			original: &Configuration{
				InternalCertManagement: &InternalCertManagement{
					Enable: new(false),
				},
				MultiKueue: &MultiKueue{
					GCInterval:        &metav1.Duration{Duration: time.Second},
					Origin:            new(""),
					WorkerLostTimeout: &metav1.Duration{Duration: time.Minute},
					DispatcherName:    defaultMultiKueue.DispatcherName,
				},
			},
			want: &Configuration{
				Namespace:         new(DefaultNamespace),
				ControllerManager: defaultCtrlManagerConfigurationSpec,
				InternalCertManagement: &InternalCertManagement{
					Enable: new(false),
				},
				ClientConnection: defaultClientConnection,
				Integrations:     defaultIntegrations,
				MultiKueue: &MultiKueue{
					GCInterval:        &metav1.Duration{Duration: time.Second},
					Origin:            new(DefaultMultiKueueOrigin),
					WorkerLostTimeout: &metav1.Duration{Duration: time.Minute},
					DispatcherName:    defaultMultiKueue.DispatcherName,
				},
				ManagedJobsNamespaceSelector: defaultManagedJobsNamespaceSelector,
				WaitForPodsReady:             &WaitForPodsReady{},
			},
		},
		"multiKueue GCInterval 0": {
			original: &Configuration{
				InternalCertManagement: &InternalCertManagement{
					Enable: new(false),
				},
				MultiKueue: &MultiKueue{
					GCInterval: &metav1.Duration{},
					Origin:     new("multikueue-manager1"),
				},
			},
			want: &Configuration{
				Namespace:         new(DefaultNamespace),
				ControllerManager: defaultCtrlManagerConfigurationSpec,
				InternalCertManagement: &InternalCertManagement{
					Enable: new(false),
				},
				ClientConnection: defaultClientConnection,
				Integrations:     defaultIntegrations,
				MultiKueue: &MultiKueue{
					GCInterval:        &metav1.Duration{},
					Origin:            new("multikueue-manager1"),
					WorkerLostTimeout: &metav1.Duration{Duration: 15 * time.Minute},
					DispatcherName:    defaultMultiKueue.DispatcherName,
				},
				ManagedJobsNamespaceSelector: defaultManagedJobsNamespaceSelector,
				WaitForPodsReady:             &WaitForPodsReady{},
			},
		},
		"add default fair sharing configuration when enabled": {
			original: &Configuration{
				InternalCertManagement: &InternalCertManagement{
					Enable: new(false),
				},
				FairSharing: &FairSharing{
					Enable: true,
				},
			},
			want: &Configuration{
				Namespace:         new(DefaultNamespace),
				ControllerManager: defaultCtrlManagerConfigurationSpec,
				InternalCertManagement: &InternalCertManagement{
					Enable: new(false),
				},
				ClientConnection:             defaultClientConnection,
				Integrations:                 defaultIntegrations,
				MultiKueue:                   defaultMultiKueue,
				ManagedJobsNamespaceSelector: defaultManagedJobsNamespaceSelector,
				FairSharing: &FairSharing{
					Enable:               true,
					PreemptionStrategies: []PreemptionStrategy{LessThanOrEqualToFinalShare, LessThanInitialShare},
				},
				WaitForPodsReady: &WaitForPodsReady{},
			},
		},
		"set object retention policy for workloads": {
			original: &Configuration{
				InternalCertManagement: &InternalCertManagement{
					Enable: new(false),
				},
				ObjectRetentionPolicies: &ObjectRetentionPolicies{
					Workloads: &WorkloadRetentionPolicy{
						AfterFinished:           &metav1.Duration{Duration: 30 * time.Minute},
						AfterDeactivatedByKueue: &metav1.Duration{Duration: 30 * time.Minute},
					},
				},
			},
			want: &Configuration{
				Namespace:         new(DefaultNamespace),
				ControllerManager: defaultCtrlManagerConfigurationSpec,
				InternalCertManagement: &InternalCertManagement{
					Enable: new(false),
				},
				ClientConnection:             defaultClientConnection,
				Integrations:                 defaultIntegrations,
				MultiKueue:                   defaultMultiKueue,
				ManagedJobsNamespaceSelector: defaultManagedJobsNamespaceSelector,
				ObjectRetentionPolicies: &ObjectRetentionPolicies{
					Workloads: &WorkloadRetentionPolicy{
						AfterFinished:           &metav1.Duration{Duration: 30 * time.Minute},
						AfterDeactivatedByKueue: &metav1.Duration{Duration: 30 * time.Minute},
					},
				},
				WaitForPodsReady: &WaitForPodsReady{},
			},
		},
		"resources.transformations strategy": {
			original: &Configuration{
				InternalCertManagement: &InternalCertManagement{
					Enable: new(false),
				},
				Resources: &Resources{
					Transformations: []ResourceTransformation{
						{Input: corev1.ResourceCPU},
						{Input: corev1.ResourceMemory, Strategy: new(Replace)},
						{Input: corev1.ResourceEphemeralStorage, Strategy: ptr.To[ResourceTransformationStrategy]("")},
					},
				},
			},
			want: &Configuration{
				Namespace:         new(DefaultNamespace),
				ControllerManager: defaultCtrlManagerConfigurationSpec,
				InternalCertManagement: &InternalCertManagement{
					Enable: new(false),
				},
				ClientConnection:             defaultClientConnection,
				Integrations:                 defaultIntegrations,
				MultiKueue:                   defaultMultiKueue,
				ManagedJobsNamespaceSelector: defaultManagedJobsNamespaceSelector,
				Resources: &Resources{
					Transformations: []ResourceTransformation{
						{Input: corev1.ResourceCPU, Strategy: new(DefaultResourceTransformationStrategy)},
						{Input: corev1.ResourceMemory, Strategy: new(Replace)},
						{Input: corev1.ResourceEphemeralStorage, Strategy: new(DefaultResourceTransformationStrategy)},
					},
				},
				WaitForPodsReady: &WaitForPodsReady{},
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

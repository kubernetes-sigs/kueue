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

// Use _test package to avoid import cycles while still asserting that
// constants match expected values from other packages (i.e. job.FrameworkName).
package v1beta1_test

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	componentconfigv1alpha1 "k8s.io/component-base/config/v1alpha1"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/apis/config/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobs/job"
)

const (
	overwriteNamespace              = "kueue-tenant-a"
	overwriteWebhookPort            = 9444
	overwriteMetricBindAddress      = ":38081"
	overwriteHealthProbeBindAddress = ":38080"
	overwriteLeaderElectionID       = "foo.kueue.x-k8s.io"
)

func TestSetDefaults_Configuration(t *testing.T) {
	defaultCtrlManagerConfigurationSpec := v1beta1.ControllerManager{
		Webhook: v1beta1.ControllerWebhook{
			Port: ptr.To(v1beta1.DefaultWebhookPort),
		},
		Metrics: v1beta1.ControllerMetrics{
			BindAddress: v1beta1.DefaultMetricsBindAddress,
		},
		Health: v1beta1.ControllerHealth{
			HealthProbeBindAddress: v1beta1.DefaultHealthProbeBindAddress,
		},
	}
	defaultClientConnection := &v1beta1.ClientConnection{
		QPS:   ptr.To(v1beta1.DefaultClientConnectionQPS),
		Burst: ptr.To(v1beta1.DefaultClientConnectionBurst),
	}
	defaultIntegrations := &v1beta1.Integrations{
		Frameworks: []string{job.FrameworkName},
		PodOptions: &v1beta1.PodIntegrationOptions{
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
	defaultQueueVisibility := &v1beta1.QueueVisibility{
		UpdateIntervalSeconds: v1beta1.DefaultQueueVisibilityUpdateIntervalSeconds,
		ClusterQueues: &v1beta1.ClusterQueueVisibility{
			MaxCount: 10,
		},
	}

	overwriteNamespaceIntegrations := &v1beta1.Integrations{
		Frameworks: []string{job.FrameworkName},
		PodOptions: &v1beta1.PodIntegrationOptions{
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

	podsReadyTimeoutTimeout := metav1.Duration{Duration: v1beta1.DefaultPodsReadyTimeout}
	podsReadyTimeoutOverwrite := metav1.Duration{Duration: time.Minute}

	testCases := map[string]struct {
		original *v1beta1.Configuration
		want     *v1beta1.Configuration
	}{
		"defaulting namespace": {
			original: &v1beta1.Configuration{
				InternalCertManagement: &v1beta1.InternalCertManagement{
					Enable: ptr.To(false),
				},
			},
			want: &v1beta1.Configuration{
				Namespace:         ptr.To(v1beta1.DefaultNamespace),
				ControllerManager: defaultCtrlManagerConfigurationSpec,
				InternalCertManagement: &v1beta1.InternalCertManagement{
					Enable: ptr.To(false),
				},
				ClientConnection: defaultClientConnection,
				Integrations:     defaultIntegrations,
				QueueVisibility:  defaultQueueVisibility,
			},
		},
		"defaulting ControllerManager": {
			original: &v1beta1.Configuration{
				ControllerManager: v1beta1.ControllerManager{
					LeaderElection: &componentconfigv1alpha1.LeaderElectionConfiguration{
						LeaderElect: ptr.To(true),
					},
				},
				InternalCertManagement: &v1beta1.InternalCertManagement{
					Enable: ptr.To(false),
				},
			},
			want: &v1beta1.Configuration{
				Namespace: ptr.To(v1beta1.DefaultNamespace),
				ControllerManager: v1beta1.ControllerManager{
					Webhook: v1beta1.ControllerWebhook{
						Port: ptr.To(v1beta1.DefaultWebhookPort),
					},
					Metrics: v1beta1.ControllerMetrics{
						BindAddress: v1beta1.DefaultMetricsBindAddress,
					},
					Health: v1beta1.ControllerHealth{
						HealthProbeBindAddress: v1beta1.DefaultHealthProbeBindAddress,
					},
					LeaderElection: &componentconfigv1alpha1.LeaderElectionConfiguration{
						LeaderElect:  ptr.To(true),
						ResourceName: v1beta1.DefaultLeaderElectionID,
					},
				},
				InternalCertManagement: &v1beta1.InternalCertManagement{
					Enable: ptr.To(false),
				},
				ClientConnection: defaultClientConnection,
				Integrations:     defaultIntegrations,
				QueueVisibility:  defaultQueueVisibility,
			},
		},
		"should not default ControllerManager": {
			original: &v1beta1.Configuration{
				ControllerManager: v1beta1.ControllerManager{
					Webhook: v1beta1.ControllerWebhook{
						Port: ptr.To(overwriteWebhookPort),
					},
					Metrics: v1beta1.ControllerMetrics{
						BindAddress: overwriteMetricBindAddress,
					},
					Health: v1beta1.ControllerHealth{
						HealthProbeBindAddress: overwriteHealthProbeBindAddress,
					},
					LeaderElection: &componentconfigv1alpha1.LeaderElectionConfiguration{
						LeaderElect:  ptr.To(true),
						ResourceName: overwriteLeaderElectionID,
					},
				},
				InternalCertManagement: &v1beta1.InternalCertManagement{
					Enable: ptr.To(false),
				},
				Integrations:    defaultIntegrations,
				QueueVisibility: defaultQueueVisibility,
			},
			want: &v1beta1.Configuration{
				Namespace: ptr.To(v1beta1.DefaultNamespace),
				ControllerManager: v1beta1.ControllerManager{
					Webhook: v1beta1.ControllerWebhook{
						Port: ptr.To(overwriteWebhookPort),
					},
					Metrics: v1beta1.ControllerMetrics{
						BindAddress: overwriteMetricBindAddress,
					},
					Health: v1beta1.ControllerHealth{
						HealthProbeBindAddress: overwriteHealthProbeBindAddress,
					},
					LeaderElection: &componentconfigv1alpha1.LeaderElectionConfiguration{
						LeaderElect:  ptr.To(true),
						ResourceName: overwriteLeaderElectionID,
					},
				},
				InternalCertManagement: &v1beta1.InternalCertManagement{
					Enable: ptr.To(false),
				},
				ClientConnection: defaultClientConnection,
				Integrations:     defaultIntegrations,
				QueueVisibility:  defaultQueueVisibility,
			},
		},
		"should not set LeaderElectionID": {
			original: &v1beta1.Configuration{
				ControllerManager: v1beta1.ControllerManager{
					LeaderElection: &componentconfigv1alpha1.LeaderElectionConfiguration{
						LeaderElect: ptr.To(false),
					},
				},
				InternalCertManagement: &v1beta1.InternalCertManagement{
					Enable: ptr.To(false),
				},
			},
			want: &v1beta1.Configuration{
				Namespace: ptr.To(v1beta1.DefaultNamespace),
				ControllerManager: v1beta1.ControllerManager{
					Webhook: v1beta1.ControllerWebhook{
						Port: ptr.To(v1beta1.DefaultWebhookPort),
					},
					Metrics: v1beta1.ControllerMetrics{
						BindAddress: v1beta1.DefaultMetricsBindAddress,
					},
					Health: v1beta1.ControllerHealth{
						HealthProbeBindAddress: v1beta1.DefaultHealthProbeBindAddress,
					},
					LeaderElection: &componentconfigv1alpha1.LeaderElectionConfiguration{
						LeaderElect: ptr.To(false),
					},
				},
				InternalCertManagement: &v1beta1.InternalCertManagement{
					Enable: ptr.To(false),
				},
				ClientConnection: defaultClientConnection,
				Integrations:     defaultIntegrations,
				QueueVisibility:  defaultQueueVisibility,
			},
		},
		"defaulting InternalCertManagement": {
			original: &v1beta1.Configuration{
				Namespace: ptr.To(overwriteNamespace),
			},
			want: &v1beta1.Configuration{
				Namespace:         ptr.To(overwriteNamespace),
				ControllerManager: defaultCtrlManagerConfigurationSpec,
				InternalCertManagement: &v1beta1.InternalCertManagement{
					Enable:             ptr.To(true),
					WebhookServiceName: ptr.To(v1beta1.DefaultWebhookServiceName),
					WebhookSecretName:  ptr.To(v1beta1.DefaultWebhookSecretName),
				},
				ClientConnection: defaultClientConnection,
				Integrations:     overwriteNamespaceIntegrations,
				QueueVisibility:  defaultQueueVisibility,
			},
		},
		"should not default InternalCertManagement": {
			original: &v1beta1.Configuration{
				Namespace: ptr.To(overwriteNamespace),
				InternalCertManagement: &v1beta1.InternalCertManagement{
					Enable: ptr.To(false),
				},
			},
			want: &v1beta1.Configuration{
				Namespace:         ptr.To(overwriteNamespace),
				ControllerManager: defaultCtrlManagerConfigurationSpec,
				InternalCertManagement: &v1beta1.InternalCertManagement{
					Enable: ptr.To(false),
				},
				ClientConnection: defaultClientConnection,
				Integrations:     overwriteNamespaceIntegrations,
				QueueVisibility:  defaultQueueVisibility,
			},
		},
		"should not default values in custom ClientConnection": {
			original: &v1beta1.Configuration{
				Namespace: ptr.To(overwriteNamespace),
				InternalCertManagement: &v1beta1.InternalCertManagement{
					Enable: ptr.To(false),
				},
				ClientConnection: &v1beta1.ClientConnection{
					QPS:   ptr.To[float32](123.0),
					Burst: ptr.To[int32](456),
				},
			},
			want: &v1beta1.Configuration{
				Namespace:         ptr.To(overwriteNamespace),
				ControllerManager: defaultCtrlManagerConfigurationSpec,
				InternalCertManagement: &v1beta1.InternalCertManagement{
					Enable: ptr.To(false),
				},
				ClientConnection: &v1beta1.ClientConnection{
					QPS:   ptr.To[float32](123.0),
					Burst: ptr.To[int32](456),
				},
				Integrations:    overwriteNamespaceIntegrations,
				QueueVisibility: defaultQueueVisibility,
			},
		},
		"should default empty custom ClientConnection": {
			original: &v1beta1.Configuration{
				Namespace: ptr.To(overwriteNamespace),
				InternalCertManagement: &v1beta1.InternalCertManagement{
					Enable: ptr.To(false),
				},
				ClientConnection: &v1beta1.ClientConnection{},
			},
			want: &v1beta1.Configuration{
				Namespace:         ptr.To(overwriteNamespace),
				ControllerManager: defaultCtrlManagerConfigurationSpec,
				InternalCertManagement: &v1beta1.InternalCertManagement{
					Enable: ptr.To(false),
				},
				ClientConnection: defaultClientConnection,
				Integrations:     overwriteNamespaceIntegrations,
				QueueVisibility:  defaultQueueVisibility,
			},
		},
		"defaulting waitForPodsReady.timeout": {
			original: &v1beta1.Configuration{
				WaitForPodsReady: &v1beta1.WaitForPodsReady{
					Enable: true,
				},
				InternalCertManagement: &v1beta1.InternalCertManagement{
					Enable: ptr.To(false),
				},
			},
			want: &v1beta1.Configuration{
				WaitForPodsReady: &v1beta1.WaitForPodsReady{
					Enable:             true,
					BlockAdmission:     ptr.To(true),
					Timeout:            &podsReadyTimeoutTimeout,
					RequeuingTimestamp: v1beta1.Eviction,
				},
				Namespace:         ptr.To(v1beta1.DefaultNamespace),
				ControllerManager: defaultCtrlManagerConfigurationSpec,
				InternalCertManagement: &v1beta1.InternalCertManagement{
					Enable: ptr.To(false),
				},
				ClientConnection: defaultClientConnection,
				Integrations:     defaultIntegrations,
				QueueVisibility:  defaultQueueVisibility,
			},
		},
		"set waitForPodsReady.blockAdmission to false when enable is false": {
			original: &v1beta1.Configuration{
				WaitForPodsReady: &v1beta1.WaitForPodsReady{
					Enable: false,
				},
				InternalCertManagement: &v1beta1.InternalCertManagement{
					Enable: ptr.To(false),
				},
			},
			want: &v1beta1.Configuration{
				WaitForPodsReady: &v1beta1.WaitForPodsReady{
					Enable:             false,
					BlockAdmission:     ptr.To(false),
					Timeout:            &podsReadyTimeoutTimeout,
					RequeuingTimestamp: v1beta1.Eviction,
				},
				Namespace:         ptr.To(v1beta1.DefaultNamespace),
				ControllerManager: defaultCtrlManagerConfigurationSpec,
				InternalCertManagement: &v1beta1.InternalCertManagement{
					Enable: ptr.To(false),
				},
				ClientConnection: defaultClientConnection,
				Integrations:     defaultIntegrations,
				QueueVisibility:  defaultQueueVisibility,
			},
		},
		"respecting provided waitForPodsReady values": {
			original: &v1beta1.Configuration{
				WaitForPodsReady: &v1beta1.WaitForPodsReady{
					Enable:             true,
					Timeout:            &podsReadyTimeoutOverwrite,
					RequeuingTimestamp: v1beta1.Creation,
				},
				InternalCertManagement: &v1beta1.InternalCertManagement{
					Enable: ptr.To(false),
				},
			},
			want: &v1beta1.Configuration{
				WaitForPodsReady: &v1beta1.WaitForPodsReady{
					Enable:             true,
					BlockAdmission:     ptr.To(true),
					Timeout:            &podsReadyTimeoutOverwrite,
					RequeuingTimestamp: v1beta1.Creation,
				},
				Namespace:         ptr.To(v1beta1.DefaultNamespace),
				ControllerManager: defaultCtrlManagerConfigurationSpec,
				InternalCertManagement: &v1beta1.InternalCertManagement{
					Enable: ptr.To(false),
				},
				ClientConnection: defaultClientConnection,
				Integrations:     defaultIntegrations,
				QueueVisibility:  defaultQueueVisibility,
			},
		},
		"integrations": {
			original: &v1beta1.Configuration{
				InternalCertManagement: &v1beta1.InternalCertManagement{
					Enable: ptr.To(false),
				},
				Integrations: &v1beta1.Integrations{
					Frameworks: []string{"a", "b"},
				},
			},
			want: &v1beta1.Configuration{
				Namespace:         ptr.To(v1beta1.DefaultNamespace),
				ControllerManager: defaultCtrlManagerConfigurationSpec,
				InternalCertManagement: &v1beta1.InternalCertManagement{
					Enable: ptr.To(false),
				},
				ClientConnection: defaultClientConnection,
				Integrations: &v1beta1.Integrations{
					Frameworks: []string{"a", "b"},
					PodOptions: defaultIntegrations.PodOptions,
				},
				QueueVisibility: defaultQueueVisibility,
			},
		},
		"queue visibility": {
			original: &v1beta1.Configuration{
				InternalCertManagement: &v1beta1.InternalCertManagement{
					Enable: ptr.To(false),
				},
				QueueVisibility: &v1beta1.QueueVisibility{
					UpdateIntervalSeconds: 10,
					ClusterQueues: &v1beta1.ClusterQueueVisibility{
						MaxCount: 0,
					},
				},
			},
			want: &v1beta1.Configuration{
				Namespace:         ptr.To(v1beta1.DefaultNamespace),
				ControllerManager: defaultCtrlManagerConfigurationSpec,
				InternalCertManagement: &v1beta1.InternalCertManagement{
					Enable: ptr.To(false),
				},
				ClientConnection: defaultClientConnection,
				Integrations:     defaultIntegrations,
				QueueVisibility: &v1beta1.QueueVisibility{
					UpdateIntervalSeconds: 10,
					ClusterQueues: &v1beta1.ClusterQueueVisibility{
						MaxCount: 0,
					},
				},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			v1beta1.SetDefaults_Configuration(tc.original)
			if diff := cmp.Diff(tc.want, tc.original); diff != "" {
				t.Errorf("unexpected error (-want,+got):\n%s", diff)
			}
		})
	}
}

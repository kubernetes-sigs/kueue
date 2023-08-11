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
	defaultCtrlManagerConfigurationSpec := ControllerManager{
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
		Frameworks: []string{job.FrameworkName},
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
				ClientConnection: defaultClientConnection,
				Integrations:     defaultIntegrations,
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
						LeaderElect:  ptr.To(true),
						ResourceName: DefaultLeaderElectionID,
					},
				},
				InternalCertManagement: &InternalCertManagement{
					Enable: ptr.To(false),
				},
				ClientConnection: defaultClientConnection,
				Integrations:     defaultIntegrations,
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
						LeaderElect:  ptr.To(true),
						ResourceName: overwriteLeaderElectionID,
					},
				},
				InternalCertManagement: &InternalCertManagement{
					Enable: ptr.To(false),
				},
				Integrations: defaultIntegrations,
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
						LeaderElect:  ptr.To(true),
						ResourceName: overwriteLeaderElectionID,
					},
				},
				InternalCertManagement: &InternalCertManagement{
					Enable: ptr.To(false),
				},
				ClientConnection: defaultClientConnection,
				Integrations:     defaultIntegrations,
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
						LeaderElect: ptr.To(false),
					},
				},
				InternalCertManagement: &InternalCertManagement{
					Enable: ptr.To(false),
				},
				ClientConnection: defaultClientConnection,
				Integrations:     defaultIntegrations,
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
				ClientConnection: defaultClientConnection,
				Integrations:     defaultIntegrations,
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
				ClientConnection: defaultClientConnection,
				Integrations:     defaultIntegrations,
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
				Integrations: defaultIntegrations,
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
				ClientConnection: defaultClientConnection,
				Integrations:     defaultIntegrations,
			},
		},
		"defaulting waitForPodsReady.timeout": {
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
				},
				Namespace:         ptr.To(DefaultNamespace),
				ControllerManager: defaultCtrlManagerConfigurationSpec,
				InternalCertManagement: &InternalCertManagement{
					Enable: ptr.To(false),
				},
				ClientConnection: defaultClientConnection,
				Integrations:     defaultIntegrations,
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
				},
				Namespace:         ptr.To(DefaultNamespace),
				ControllerManager: defaultCtrlManagerConfigurationSpec,
				InternalCertManagement: &InternalCertManagement{
					Enable: ptr.To(false),
				},
				ClientConnection: defaultClientConnection,
				Integrations:     defaultIntegrations,
			},
		},
		"respecting provided waitForPodsReady.timeout": {
			original: &Configuration{
				WaitForPodsReady: &WaitForPodsReady{
					Enable:  true,
					Timeout: &podsReadyTimeoutOverwrite,
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
				},
				Namespace:         ptr.To(DefaultNamespace),
				ControllerManager: defaultCtrlManagerConfigurationSpec,
				InternalCertManagement: &InternalCertManagement{
					Enable: ptr.To(false),
				},
				ClientConnection: defaultClientConnection,
				Integrations:     defaultIntegrations,
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

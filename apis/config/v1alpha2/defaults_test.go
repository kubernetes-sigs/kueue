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

package v1alpha2

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	componentconfigv1alpha1 "k8s.io/component-base/config/v1alpha1"
	"k8s.io/utils/pointer"
	ctrlconfigv1alpha1 "sigs.k8s.io/controller-runtime/pkg/config/v1alpha1"
)

const (
	overwriteNamespace              = "kueue-tenant-a"
	overwriteWebhookPort            = 9444
	overwriteMetricBindAddress      = ":38081"
	overwriteHealthProbeBindAddress = ":38080"
	overwriteLeaderElectionID       = "foo.kueue.x-k8s.io"
)

func TestSetDefaults_Configuration(t *testing.T) {
	defaultCtrlManagerConfigurationSpec := ctrlconfigv1alpha1.ControllerManagerConfigurationSpec{
		Webhook: ctrlconfigv1alpha1.ControllerWebhook{
			Port: pointer.Int(DefaultWebhookPort),
		},
		Metrics: ctrlconfigv1alpha1.ControllerMetrics{
			BindAddress: DefaultMetricsBindAddress,
		},
		Health: ctrlconfigv1alpha1.ControllerHealth{
			HealthProbeBindAddress: DefaultHealthProbeBindAddress,
		},
	}

	testCases := map[string]struct {
		original *Configuration
		want     *Configuration
	}{
		"defaulting namespace": {
			original: &Configuration{
				InternalCertManagement: &InternalCertManagement{
					Enable: pointer.Bool(false),
				},
			},
			want: &Configuration{
				Namespace:                          pointer.String(DefaultNamespace),
				ControllerManagerConfigurationSpec: defaultCtrlManagerConfigurationSpec,
				InternalCertManagement: &InternalCertManagement{
					Enable: pointer.Bool(false),
				},
			},
		},
		"defaulting ControllerManagerConfigurationSpec": {
			original: &Configuration{
				ControllerManagerConfigurationSpec: ctrlconfigv1alpha1.ControllerManagerConfigurationSpec{
					LeaderElection: &componentconfigv1alpha1.LeaderElectionConfiguration{
						LeaderElect: pointer.Bool(true),
					},
				},
				InternalCertManagement: &InternalCertManagement{
					Enable: pointer.Bool(false),
				},
			},
			want: &Configuration{
				Namespace: pointer.String(DefaultNamespace),
				ControllerManagerConfigurationSpec: ctrlconfigv1alpha1.ControllerManagerConfigurationSpec{
					Webhook: ctrlconfigv1alpha1.ControllerWebhook{
						Port: pointer.Int(DefaultWebhookPort),
					},
					Metrics: ctrlconfigv1alpha1.ControllerMetrics{
						BindAddress: DefaultMetricsBindAddress,
					},
					Health: ctrlconfigv1alpha1.ControllerHealth{
						HealthProbeBindAddress: DefaultHealthProbeBindAddress,
					},
					LeaderElection: &componentconfigv1alpha1.LeaderElectionConfiguration{
						LeaderElect:  pointer.Bool(true),
						ResourceName: DefaultLeaderElectionID,
					},
				},
				InternalCertManagement: &InternalCertManagement{
					Enable: pointer.Bool(false),
				},
			},
		},
		"should not default ControllerManagerConfigurationSpec": {
			original: &Configuration{
				ControllerManagerConfigurationSpec: ctrlconfigv1alpha1.ControllerManagerConfigurationSpec{
					Webhook: ctrlconfigv1alpha1.ControllerWebhook{
						Port: pointer.Int(overwriteWebhookPort),
					},
					Metrics: ctrlconfigv1alpha1.ControllerMetrics{
						BindAddress: overwriteMetricBindAddress,
					},
					Health: ctrlconfigv1alpha1.ControllerHealth{
						HealthProbeBindAddress: overwriteHealthProbeBindAddress,
					},
					LeaderElection: &componentconfigv1alpha1.LeaderElectionConfiguration{
						LeaderElect:  pointer.Bool(true),
						ResourceName: overwriteLeaderElectionID,
					},
				},
				InternalCertManagement: &InternalCertManagement{
					Enable: pointer.Bool(false),
				},
			},
			want: &Configuration{
				Namespace: pointer.String(DefaultNamespace),
				ControllerManagerConfigurationSpec: ctrlconfigv1alpha1.ControllerManagerConfigurationSpec{
					Webhook: ctrlconfigv1alpha1.ControllerWebhook{
						Port: pointer.Int(overwriteWebhookPort),
					},
					Metrics: ctrlconfigv1alpha1.ControllerMetrics{
						BindAddress: overwriteMetricBindAddress,
					},
					Health: ctrlconfigv1alpha1.ControllerHealth{
						HealthProbeBindAddress: overwriteHealthProbeBindAddress,
					},
					LeaderElection: &componentconfigv1alpha1.LeaderElectionConfiguration{
						LeaderElect:  pointer.Bool(true),
						ResourceName: overwriteLeaderElectionID,
					},
				},
				InternalCertManagement: &InternalCertManagement{
					Enable: pointer.Bool(false),
				},
			},
		},
		"should not set LeaderElectionID": {
			original: &Configuration{
				ControllerManagerConfigurationSpec: ctrlconfigv1alpha1.ControllerManagerConfigurationSpec{
					LeaderElection: &componentconfigv1alpha1.LeaderElectionConfiguration{
						LeaderElect: pointer.Bool(false),
					},
				},
				InternalCertManagement: &InternalCertManagement{
					Enable: pointer.Bool(false),
				},
			},
			want: &Configuration{
				Namespace: pointer.String(DefaultNamespace),
				ControllerManagerConfigurationSpec: ctrlconfigv1alpha1.ControllerManagerConfigurationSpec{
					Webhook: ctrlconfigv1alpha1.ControllerWebhook{
						Port: pointer.Int(DefaultWebhookPort),
					},
					Metrics: ctrlconfigv1alpha1.ControllerMetrics{
						BindAddress: DefaultMetricsBindAddress,
					},
					Health: ctrlconfigv1alpha1.ControllerHealth{
						HealthProbeBindAddress: DefaultHealthProbeBindAddress,
					},
					LeaderElection: &componentconfigv1alpha1.LeaderElectionConfiguration{
						LeaderElect: pointer.Bool(false),
					},
				},
				InternalCertManagement: &InternalCertManagement{
					Enable: pointer.Bool(false),
				},
			},
		},
		"defaulting InternalCertManagement": {
			original: &Configuration{
				Namespace: pointer.String(overwriteNamespace),
			},
			want: &Configuration{
				Namespace:                          pointer.String(overwriteNamespace),
				ControllerManagerConfigurationSpec: defaultCtrlManagerConfigurationSpec,
				InternalCertManagement: &InternalCertManagement{
					Enable:             pointer.Bool(true),
					WebhookServiceName: pointer.String(DefaultWebhookServiceName),
					WebhookSecretName:  pointer.String(DefaultWebhookSecretName),
				},
			},
		},
		"should not default InternalCertManagement": {
			original: &Configuration{
				Namespace: pointer.String(overwriteNamespace),
				InternalCertManagement: &InternalCertManagement{
					Enable: pointer.Bool(false),
				},
			},
			want: &Configuration{
				Namespace:                          pointer.String(overwriteNamespace),
				ControllerManagerConfigurationSpec: defaultCtrlManagerConfigurationSpec,
				InternalCertManagement: &InternalCertManagement{
					Enable: pointer.Bool(false),
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

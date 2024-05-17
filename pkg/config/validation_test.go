/*
Copyright 2023 The Kubernetes Authors.

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
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
)

func TestValidate(t *testing.T) {
	testScheme := runtime.NewScheme()
	if err := configapi.AddToScheme(testScheme); err != nil {
		t.Fatal(err)
	}
	if err := clientgoscheme.AddToScheme(testScheme); err != nil {
		t.Fatal(err)
	}

	defaultQueueVisibility := &configapi.QueueVisibility{
		UpdateIntervalSeconds: configapi.DefaultQueueVisibilityUpdateIntervalSeconds,
		ClusterQueues: &configapi.ClusterQueueVisibility{
			MaxCount: configapi.DefaultClusterQueuesMaxCount,
		},
	}
	defaultPodIntegrationOptions := &configapi.PodIntegrationOptions{
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
	}
	defaultIntegrations := &configapi.Integrations{
		Frameworks: []string{"batch/job"},
		PodOptions: defaultPodIntegrationOptions,
	}

	testCases := map[string]struct {
		cfg     *configapi.Configuration
		wantErr field.ErrorList
	}{
		"empty": {
			cfg: &configapi.Configuration{},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeRequired,
					Field: "integrations",
				},
			},
		},
		"invalid queue visibility UpdateIntervalSeconds": {
			cfg: &configapi.Configuration{
				QueueVisibility: &configapi.QueueVisibility{
					UpdateIntervalSeconds: 0,
				},
				Integrations: defaultIntegrations,
			},
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("queueVisibility").Child("updateIntervalSeconds"), 0, fmt.Sprintf("greater than or equal to %d", queueVisibilityClusterQueuesUpdateIntervalSeconds)),
			},
		},
		"invalid queue visibility cluster queue max count": {
			cfg: &configapi.Configuration{
				QueueVisibility: &configapi.QueueVisibility{
					ClusterQueues: &configapi.ClusterQueueVisibility{
						MaxCount: 4001,
					},
					UpdateIntervalSeconds: 1,
				},
				Integrations: defaultIntegrations,
			},
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("queueVisibility").Child("clusterQueues").Child("maxCount"), 4001, fmt.Sprintf("must be less than %d", queueVisibilityClusterQueuesMaxValue)),
			},
		},
		"empty integrations.frameworks": {
			cfg: &configapi.Configuration{
				Integrations: &configapi.Integrations{},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeRequired,
					Field: "integrations.frameworks",
				},
			},
		},
		"unregistered integrations.frameworks": {
			cfg: &configapi.Configuration{
				Integrations: &configapi.Integrations{
					Frameworks: []string{"unregistered/jobframework"},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeNotSupported,
					Field: "integrations.frameworks[0]",
				},
			},
		},
		"duplicate integrations.frameworks": {
			cfg: &configapi.Configuration{
				Integrations: &configapi.Integrations{
					Frameworks: []string{
						"batch/job",
						"batch/job",
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeDuplicate,
					Field: "integrations.frameworks[1]",
				},
			},
		},
		"duplicate frameworks between integrations.frameworks and integrations.externalFrameworks": {
			cfg: &configapi.Configuration{
				Integrations: &configapi.Integrations{
					Frameworks:         []string{"batch/job"},
					ExternalFrameworks: []string{"Job.v1.batch"},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeDuplicate,
					Field: "integrations.externalFrameworks[0]",
				},
			},
		},
		"invalid format integrations.externalFrameworks": {
			cfg: &configapi.Configuration{
				Integrations: &configapi.Integrations{
					Frameworks:         []string{"batch/job"},
					ExternalFrameworks: []string{"invalid"},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "integrations.externalFrameworks[0]",
				},
			},
		},
		"duplicate integrations.externalFrameworks": {
			cfg: &configapi.Configuration{
				Integrations: &configapi.Integrations{
					Frameworks: []string{"batch/job"},
					ExternalFrameworks: []string{
						"Foo.v1.example.com",
						"Foo.v1.example.com",
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeDuplicate,
					Field: "integrations.externalFrameworks[1]",
				},
			},
		},
		"nil PodIntegrationOptions": {
			cfg: &configapi.Configuration{
				QueueVisibility: defaultQueueVisibility,
				Integrations: &configapi.Integrations{
					Frameworks: []string{"pod"},
					PodOptions: nil,
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeRequired,
					Field: "integrations.podOptions",
				},
			},
		},
		"nil PodIntegrationOptions.NamespaceSelector": {
			cfg: &configapi.Configuration{
				QueueVisibility: defaultQueueVisibility,
				Integrations: &configapi.Integrations{
					Frameworks: []string{"pod"},
					PodOptions: &configapi.PodIntegrationOptions{
						NamespaceSelector: nil,
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeRequired,
					Field: "integrations.podOptions.namespaceSelector",
				},
			},
		},
		"emptyLabelSelector": {
			cfg: &configapi.Configuration{
				Namespace:       ptr.To("kueue-system"),
				QueueVisibility: defaultQueueVisibility,
				Integrations: &configapi.Integrations{
					Frameworks: []string{"pod"},
					PodOptions: &configapi.PodIntegrationOptions{
						NamespaceSelector: &metav1.LabelSelector{},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "integrations.podOptions.namespaceSelector",
				},
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "integrations.podOptions.namespaceSelector",
				},
			},
		},
		"prohibited namespace in MatchLabels": {
			cfg: &configapi.Configuration{
				QueueVisibility: defaultQueueVisibility,
				Integrations: &configapi.Integrations{
					Frameworks: []string{"pod"},
					PodOptions: &configapi.PodIntegrationOptions{
						NamespaceSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"kubernetes.io/metadata.name": "kube-system",
							},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "integrations.podOptions.namespaceSelector",
				},
			},
		},
		"prohibited namespace in MatchExpressions with operator In": {
			cfg: &configapi.Configuration{
				QueueVisibility: defaultQueueVisibility,
				Integrations: &configapi.Integrations{
					Frameworks: []string{"pod"},
					PodOptions: &configapi.PodIntegrationOptions{
						NamespaceSelector: &metav1.LabelSelector{
							MatchExpressions: []metav1.LabelSelectorRequirement{
								{
									Key:      "kubernetes.io/metadata.name",
									Operator: metav1.LabelSelectorOpIn,
									Values:   []string{"kube-system"},
								},
							},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "integrations.podOptions.namespaceSelector",
				},
			},
		},
		"prohibited namespace in MatchExpressions with operator NotIn": {
			cfg: &configapi.Configuration{
				QueueVisibility: defaultQueueVisibility,
				Integrations: &configapi.Integrations{
					Frameworks: []string{"pod"},
					PodOptions: &configapi.PodIntegrationOptions{
						NamespaceSelector: &metav1.LabelSelector{
							MatchExpressions: []metav1.LabelSelectorRequirement{
								{
									Key:      "kubernetes.io/metadata.name",
									Operator: metav1.LabelSelectorOpNotIn,
									Values:   []string{"kube-system", "kueue-system"},
								},
							},
						},
					},
				},
			},
			wantErr: nil,
		},
		"no supported waitForPodsReady.requeuingStrategy.timestamp": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				WaitForPodsReady: &configapi.WaitForPodsReady{
					Enable: true,
					RequeuingStrategy: &configapi.RequeuingStrategy{
						Timestamp: ptr.To[configapi.RequeuingTimestamp]("NoSupported"),
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeNotSupported,
					Field: "waitForPodsReady.requeuingStrategy.timestamp",
				},
			},
		},
		"negative waitForPodsReady.timeout": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				WaitForPodsReady: &configapi.WaitForPodsReady{
					Enable: true,
					Timeout: &metav1.Duration{
						Duration: -1,
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "waitForPodsReady.timeout",
				},
			},
		},
		"valid waitForPodsReady": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				WaitForPodsReady: &configapi.WaitForPodsReady{
					Enable: true,
					Timeout: &metav1.Duration{
						Duration: 50,
					},
					BlockAdmission: ptr.To(false),
					RequeuingStrategy: &configapi.RequeuingStrategy{
						Timestamp:          ptr.To(configapi.CreationTimestamp),
						BackoffLimitCount:  ptr.To[int32](10),
						BackoffBaseSeconds: ptr.To[int32](30),
					},
				},
			},
		},
		"negative waitForPodsReady.requeuingStrategy.backoffLimitCount": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				WaitForPodsReady: &configapi.WaitForPodsReady{
					Enable: true,
					RequeuingStrategy: &configapi.RequeuingStrategy{
						BackoffLimitCount: ptr.To[int32](-1),
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "waitForPodsReady.requeuingStrategy.backoffLimitCount",
				},
			},
		},
		"negative waitForPodsReady.requeuingStrategy.backoffBaseSeconds": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				WaitForPodsReady: &configapi.WaitForPodsReady{
					Enable: true,
					RequeuingStrategy: &configapi.RequeuingStrategy{
						BackoffBaseSeconds: ptr.To[int32](-1),
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "waitForPodsReady.requeuingStrategy.backoffBaseSeconds",
				},
			},
		},
		"negative multiKueue.gcInterval": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				MultiKueue: &configapi.MultiKueue{
					GCInterval: &metav1.Duration{
						Duration: -time.Second,
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "multiKueue.gcInterval",
				},
			},
		},
		"negative multiKueue.workerLostTimeout": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				MultiKueue: &configapi.MultiKueue{
					WorkerLostTimeout: &metav1.Duration{
						Duration: -time.Second,
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "multiKueue.workerLostTimeout",
				},
			},
		},
		"invalid .multiKueue.origin label value": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				MultiKueue: &configapi.MultiKueue{
					Origin: ptr.To("=]"),
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "multiKueue.origin",
				},
			},
		},
		"valid .multiKueue configuration": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				MultiKueue: &configapi.MultiKueue{
					GCInterval: &metav1.Duration{
						Duration: time.Second,
					},
					Origin: ptr.To("valid"),
					WorkerLostTimeout: &metav1.Duration{
						Duration: 2 * time.Second,
					},
				},
			},
		},
		"unsupported preemption strategy": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				FairSharing: &configapi.FairSharing{
					Enable:               true,
					PreemptionStrategies: []configapi.PreemptionStrategy{configapi.LessThanOrEqualToFinalShare, "UNKNOWN", configapi.LessThanInitialShare, configapi.LessThanOrEqualToFinalShare},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeNotSupported,
					Field: "fairSharing.preemptionStrategies",
				},
			},
		},
		"valid preemption strategy": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				FairSharing: &configapi.FairSharing{
					Enable:               true,
					PreemptionStrategies: []configapi.PreemptionStrategy{configapi.LessThanOrEqualToFinalShare, configapi.LessThanInitialShare},
				},
			},
		},
		"invalid .internalCertManagement.webhookSecretName": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				InternalCertManagement: &configapi.InternalCertManagement{
					Enable:            ptr.To(true),
					WebhookSecretName: ptr.To(":)"),
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "internalCertManagement.webhookSecretName",
				},
			},
		},
		"invalid .internalCertManagement.webhookServiceName": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				InternalCertManagement: &configapi.InternalCertManagement{
					Enable:             ptr.To(true),
					WebhookServiceName: ptr.To("0-invalid"),
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "internalCertManagement.webhookServiceName",
				},
			},
		},
		"disabled .internalCertManagement with invalid .internalCertManagement.webhookServiceName": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				InternalCertManagement: &configapi.InternalCertManagement{
					Enable:             ptr.To(false),
					WebhookServiceName: ptr.To("0-invalid"),
				},
			},
		},
		"valid .internalCertManagement": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				InternalCertManagement: &configapi.InternalCertManagement{
					Enable:             ptr.To(true),
					WebhookServiceName: ptr.To("webhook-svc"),
					WebhookSecretName:  ptr.To("webhook-sec"),
				},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			if diff := cmp.Diff(tc.wantErr, validate(tc.cfg, testScheme), cmpopts.IgnoreFields(field.Error{}, "BadValue", "Detail")); diff != "" {
				t.Errorf("Unexpected returned error (-want,+got):\n%s", diff)
			}
		})
	}
}

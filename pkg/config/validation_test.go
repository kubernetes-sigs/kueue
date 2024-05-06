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

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
)

func TestValidate(t *testing.T) {
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
		"supported waitForPodsReady.requeuingStrategy.timestamp": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				WaitForPodsReady: &configapi.WaitForPodsReady{
					Enable: true,
					RequeuingStrategy: &configapi.RequeuingStrategy{
						Timestamp: ptr.To(configapi.CreationTimestamp),
					},
				},
			},
			wantErr: nil,
		},
		"non-negative waitForPodsReady.requeuingStrategy.backoffLimitCount": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				WaitForPodsReady: &configapi.WaitForPodsReady{
					Enable: true,
					RequeuingStrategy: &configapi.RequeuingStrategy{
						BackoffLimitCount: ptr.To[int32](10),
					},
				},
			},
			wantErr: nil,
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
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			if diff := cmp.Diff(tc.wantErr, validate(tc.cfg), cmpopts.IgnoreFields(field.Error{}, "BadValue", "Detail")); diff != "" {
				t.Errorf("Unexpected returned error (-want,+got):\n%s", diff)
			}
		})
	}
}

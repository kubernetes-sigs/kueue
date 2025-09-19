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

package config

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
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
	systemNamespacesSelector := &metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{
			{
				Key:      corev1.LabelMetadataName,
				Operator: metav1.LabelSelectorOpNotIn,
				Values:   []string{"kube-system", "kueue-system"},
			},
		},
	}
	defaultPodIntegrationOptions := &configapi.PodIntegrationOptions{
		NamespaceSelector: systemNamespacesSelector,
		PodSelector:       &metav1.LabelSelector{},
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
		"invalid queue visibility cluster queue max count due to exceeding maximal value": {
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
		"negative queue visibility cluster queue max cont": {
			cfg: &configapi.Configuration{
				QueueVisibility: &configapi.QueueVisibility{
					ClusterQueues: &configapi.ClusterQueueVisibility{
						MaxCount: -1,
					},
					UpdateIntervalSeconds: 1,
				},
				Integrations: defaultIntegrations,
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "queueVisibility.clusterQueues.maxCount",
				},
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
		"nil PodIntegrationOptions and nil managedJobsNamespaceSelector": {
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
					Field: "managedJobsNamespaceSelector",
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
		"valid managedJobsNamespaceSelector ": {
			cfg: &configapi.Configuration{
				QueueVisibility:              defaultQueueVisibility,
				ManagedJobsNamespaceSelector: systemNamespacesSelector,
				Integrations:                 defaultIntegrations,
			},
			wantErr: nil,
		},

		"prohibited namespace in MatchLabels": {
			cfg: &configapi.Configuration{
				QueueVisibility: defaultQueueVisibility,
				Integrations: &configapi.Integrations{
					Frameworks: []string{"pod"},
					PodOptions: &configapi.PodIntegrationOptions{
						NamespaceSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								corev1.LabelMetadataName: "kube-system",
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
		"prohibited namespace in MatchLabels managedJobsNamespaceSelector": {
			cfg: &configapi.Configuration{
				QueueVisibility: defaultQueueVisibility,
				ManagedJobsNamespaceSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						corev1.LabelMetadataName: "kube-system",
					},
				},
				Integrations: defaultIntegrations,
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "managedJobsNamespaceSelector",
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
									Key:      corev1.LabelMetadataName,
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
		"prohibited namespace in MatchExpressions with operator In managedJobsNamespaceSelector": {
			cfg: &configapi.Configuration{
				QueueVisibility: defaultQueueVisibility,
				ManagedJobsNamespaceSelector: &metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{
							Key:      corev1.LabelMetadataName,
							Operator: metav1.LabelSelectorOpIn,
							Values:   []string{"kube-system"},
						},
					},
				},
				Integrations: defaultIntegrations,
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "managedJobsNamespaceSelector",
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
									Key:      corev1.LabelMetadataName,
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
		"prohibited namespace in MatchExpressions with operator NotIn managedJobsNamespaceSelector": {
			cfg: &configapi.Configuration{
				QueueVisibility: defaultQueueVisibility,
				ManagedJobsNamespaceSelector: &metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{
							Key:      corev1.LabelMetadataName,
							Operator: metav1.LabelSelectorOpNotIn,
							Values:   []string{"kube-system", "kueue-system"},
						},
					},
				},
				Integrations: defaultIntegrations,
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
		"negative waitForPodsReady.recoveryTimeout": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				WaitForPodsReady: &configapi.WaitForPodsReady{
					Enable: true,
					RecoveryTimeout: &metav1.Duration{
						Duration: -1,
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "waitForPodsReady.recoveryTimeout",
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
					RecoveryTimeout: &metav1.Duration{
						Duration: 3,
					},
					BlockAdmission: ptr.To(false),
					RequeuingStrategy: &configapi.RequeuingStrategy{
						Timestamp:          ptr.To(configapi.CreationTimestamp),
						BackoffLimitCount:  ptr.To[int32](10),
						BackoffBaseSeconds: ptr.To[int32](30),
						BackoffMaxSeconds:  ptr.To[int32](1800),
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
		"negative waitForPodsReady.requeuingStrategy.backoffMaxSeconds": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				WaitForPodsReady: &configapi.WaitForPodsReady{
					Enable: true,
					RequeuingStrategy: &configapi.RequeuingStrategy{
						BackoffMaxSeconds: ptr.To[int32](-1),
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "waitForPodsReady.requeuingStrategy.backoffMaxSeconds",
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
		"valid admissionFairSharing configuration": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				AdmissionFairSharing: &configapi.AdmissionFairSharing{
					UsageHalfLifeTime:     metav1.Duration{Duration: time.Second},
					UsageSamplingInterval: metav1.Duration{Duration: time.Second},
					ResourceWeights: map[corev1.ResourceName]float64{
						corev1.ResourceCPU:    0.5,
						corev1.ResourceMemory: 0.5,
					},
				},
			},
		},
		"invalid admissionFairSharing.usageHalfLifeTime configuration": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				AdmissionFairSharing: &configapi.AdmissionFairSharing{
					UsageHalfLifeTime:     metav1.Duration{Duration: -time.Second},
					UsageSamplingInterval: metav1.Duration{Duration: time.Second},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "admissionFairSharing.usageHalfLifeTime",
				},
			},
		},
		"invalid admissionFairSharing.usageSamplingInterval configuration": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				AdmissionFairSharing: &configapi.AdmissionFairSharing{
					UsageSamplingInterval: metav1.Duration{Duration: -time.Second},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "admissionFairSharing.usageSamplingInterval",
				},
			},
		},
		"invalid admissionFairSharing.resourceWeights configuration": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				AdmissionFairSharing: &configapi.AdmissionFairSharing{
					UsageSamplingInterval: metav1.Duration{Duration: time.Second},
					ResourceWeights: map[corev1.ResourceName]float64{
						corev1.ResourceCPU: -0.5,
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "admissionFairSharing.resourceWeights[cpu]",
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
		"invalid .resources.transformations.strategy": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				Resources: &configapi.Resources{
					Transformations: []configapi.ResourceTransformation{
						{
							Input:    corev1.ResourceCPU,
							Strategy: ptr.To(configapi.ResourceTransformationStrategy("invalid")),
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeNotSupported,
					Field: "resources.transformations[0].strategy",
				},
			},
		},

		"invalid .resources.transformations.inputs": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				Resources: &configapi.Resources{
					Transformations: []configapi.ResourceTransformation{
						{
							Input:    corev1.ResourceCPU,
							Strategy: ptr.To(configapi.Retain),
						},
						{
							Input:    corev1.ResourceMemory,
							Strategy: ptr.To(configapi.Retain),
						},
						{
							Input:    corev1.ResourceCPU,
							Strategy: ptr.To(configapi.Retain),
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeDuplicate,
					Field: "resources.transformations[2].input",
				},
			},
		},

		"valid .resources.transformations": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				Resources: &configapi.Resources{
					Transformations: []configapi.ResourceTransformation{
						{
							Input:    corev1.ResourceCPU,
							Strategy: ptr.To(configapi.Retain),
						},
						{
							Input:    corev1.ResourceMemory,
							Strategy: ptr.To(configapi.Replace),
						},
					},
				},
			},
		},
		"negative afterFinished in .objectRetentionPolicies.workloads": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				ObjectRetentionPolicies: &configapi.ObjectRetentionPolicies{
					Workloads: &configapi.WorkloadRetentionPolicy{
						AfterFinished: ptr.To(metav1.Duration{Duration: -1}),
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "objectRetentionPolicies.workloads.afterFinished",
				},
			},
		},
		"zero afterFinished in .objectRetentionPolicies.workloads": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				ObjectRetentionPolicies: &configapi.ObjectRetentionPolicies{
					Workloads: &configapi.WorkloadRetentionPolicy{
						AfterFinished: ptr.To(metav1.Duration{Duration: 0}),
					},
				},
			},
		},
		"positive afterFinished in .objectRetentionPolicies.workloads": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				ObjectRetentionPolicies: &configapi.ObjectRetentionPolicies{
					Workloads: &configapi.WorkloadRetentionPolicy{
						AfterFinished: ptr.To(metav1.Duration{Duration: 1}),
					},
				},
			},
		},
		"negative afterDeactivatedByKueue in .objectRetentionPolicies.workloads": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				ObjectRetentionPolicies: &configapi.ObjectRetentionPolicies{
					Workloads: &configapi.WorkloadRetentionPolicy{
						AfterDeactivatedByKueue: ptr.To(metav1.Duration{Duration: -1}),
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "objectRetentionPolicies.workloads.afterDeactivatedByKueue",
				},
			},
		},
		"zero afterDeactivatedByKueue in .objectRetentionPolicies.workloads": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				ObjectRetentionPolicies: &configapi.ObjectRetentionPolicies{
					Workloads: &configapi.WorkloadRetentionPolicy{
						AfterDeactivatedByKueue: ptr.To(metav1.Duration{Duration: 0}),
					},
				},
			},
		},
		"positive afterDeactivatedByKueue in .objectRetentionPolicies.workloads": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				ObjectRetentionPolicies: &configapi.ObjectRetentionPolicies{
					Workloads: &configapi.WorkloadRetentionPolicy{
						AfterDeactivatedByKueue: ptr.To(metav1.Duration{Duration: 1}),
					},
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

func TestValidateFeatureGates(t *testing.T) {
	cases := map[string]struct {
		featureGatesCLI string
		featureGateMap  map[string]bool
		errorStr        string
	}{
		"no feature gates is null": {
			featureGatesCLI: "",
			featureGateMap:  nil,
			errorStr:        "",
		},
		"feature gate cli": {
			featureGatesCLI: "test:true",
			featureGateMap:  nil,
			errorStr:        "",
		},
		"cannot specify both feature gates": {
			featureGatesCLI: "test:true",
			featureGateMap:  map[string]bool{"test": true},

			errorStr: "feature gates for CLI and configuration cannot both specified",
		},
		"cannot specify more than one TAS profile using GateMap": {
			featureGateMap: map[string]bool{"TASProfileMixed": true,
				"TASProfileLeastFreeCapacity": true},
			errorStr: "Cannot use more than one TAS profiles",
		},
		"cannot specify more than one TAS profile using GatesCLI": {
			featureGatesCLI: "TASProfileLeastFreeCapacity:true,TASProfileMixed:true",
			errorStr:        "Cannot use more than one TAS profiles",
		},
		"cannot set TAS profile with TAS disabled": {
			featureGateMap: map[string]bool{"TASProfileLeastFreeCapacity": true},
			errorStr:       "Cannot use a TAS profile with TAS disabled",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := ValidateFeatureGates(tc.featureGatesCLI, tc.featureGateMap)
			if got != nil && tc.errorStr != got.Error() {
				t.Errorf("Unexpected result from ValidateFeatureGates\nwant:\n%v\ngot:%v\n", tc.errorStr, got.Error())
			}
		})
	}
}

func TestValidateDeviceClassMappings(t *testing.T) {
	testCases := map[string]struct {
		cfg     *configapi.Configuration
		wantErr field.ErrorList
	}{
		"nil resources": {
			cfg: &configapi.Configuration{},
		},
		"nil DRA config": {
			cfg: &configapi.Configuration{
				Resources: &configapi.Resources{},
			},
		},
		"empty DRA resources": {
			cfg: &configapi.Configuration{
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{},
				},
			},
		},
		"valid single resource": {
			cfg: &configapi.Configuration{
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name:             "whole-foo",
							DeviceClassNames: []corev1.ResourceName{"foo.com/device"},
						},
					},
				},
			},
		},
		"valid multiple resources": {
			cfg: &configapi.Configuration{
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name:             "whole-foo",
							DeviceClassNames: []corev1.ResourceName{"foo.com/device"},
						},
						{
							Name:             "shared-bar",
							DeviceClassNames: []corev1.ResourceName{"bar.com/device-1g", "bar.com/device-2g"},
						},
					},
				},
			},
		},
		"valid resource with multiple device classes": {
			cfg: &configapi.Configuration{
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name:             "mixed-devices",
							DeviceClassNames: []corev1.ResourceName{"foo.com/gpu", "bar.com/accelerator", "baz.org/compute"},
						},
					},
				},
			},
		},
		"invalid resource name": {
			cfg: &configapi.Configuration{
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name:             "@invalid-name",
							DeviceClassNames: []corev1.ResourceName{"foo.com/device"},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "resources.deviceClassMappings[0].name",
				},
			},
		},
		"duplicate resource names": {
			cfg: &configapi.Configuration{
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name:             "duplicate-name",
							DeviceClassNames: []corev1.ResourceName{"foo.com/device"},
						},
						{
							Name:             "duplicate-name",
							DeviceClassNames: []corev1.ResourceName{"bar.com/device"},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeDuplicate,
					Field: "resources.deviceClassMappings[1].name",
				},
			},
		},
		"empty device class names": {
			cfg: &configapi.Configuration{
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name:             "empty-devices",
							DeviceClassNames: []corev1.ResourceName{},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeRequired,
					Field: "resources.deviceClassMappings[0].deviceClassNames",
				},
			},
		},
		"invalid device class name": {
			cfg: &configapi.Configuration{
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name:             "valid-name",
							DeviceClassNames: []corev1.ResourceName{"valid.com/device", "@invalid-device"},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "resources.deviceClassMappings[0].deviceClassNames[1]",
				},
			},
		},
		"device class conflict": {
			cfg: &configapi.Configuration{
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name:             "first-resource",
							DeviceClassNames: []corev1.ResourceName{"foo.com/device"},
						},
						{
							Name:             "second-resource",
							DeviceClassNames: []corev1.ResourceName{"foo.com/device"},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "resources.deviceClassMappings[1].deviceClassNames[0]",
				},
			},
		},
		"multiple validation errors": {
			cfg: &configapi.Configuration{
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name:             "@invalid",
							DeviceClassNames: []corev1.ResourceName{},
						},
						{
							Name:             "valid-name",
							DeviceClassNames: []corev1.ResourceName{"@invalid-device", "valid.com/device"},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "resources.deviceClassMappings[0].name",
				},
				&field.Error{
					Type:  field.ErrorTypeRequired,
					Field: "resources.deviceClassMappings[0].deviceClassNames",
				},
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "resources.deviceClassMappings[1].deviceClassNames[0]",
				},
			},
		},
		"duplicate device class names within same mapping": {
			cfg: &configapi.Configuration{
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name:             "duplicate-devices",
							DeviceClassNames: []corev1.ResourceName{"foo.com/device", "bar.com/device", "foo.com/device"},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeDuplicate,
					Field: "resources.deviceClassMappings[0].deviceClassNames[2]",
				},
			},
		},
		"empty string resource name": {
			cfg: &configapi.Configuration{
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name:             "",
							DeviceClassNames: []corev1.ResourceName{"foo.com/device"},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "resources.deviceClassMappings[0].name",
				},
			},
		},
		"whitespace in resource name": {
			cfg: &configapi.Configuration{
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name:             "foo bar",
							DeviceClassNames: []corev1.ResourceName{"foo.com/device"},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "resources.deviceClassMappings[0].name",
				},
			},
		},
		"empty string device class name": {
			cfg: &configapi.Configuration{
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name:             "valid-name",
							DeviceClassNames: []corev1.ResourceName{""},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "resources.deviceClassMappings[0].deviceClassNames[0]",
				},
			},
		},
		"whitespace in device class name": {
			cfg: &configapi.Configuration{
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name:             "valid-name",
							DeviceClassNames: []corev1.ResourceName{"foo   bar"},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "resources.deviceClassMappings[0].deviceClassNames[0]",
				},
			},
		},
		"max length resource name": {
			cfg: &configapi.Configuration{
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name:             corev1.ResourceName(fmt.Sprintf("%s/%s", strings.Repeat("a", 240), strings.Repeat("b", 12))), // 253 chars total
							DeviceClassNames: []corev1.ResourceName{"foo.com/device"},
						},
					},
				},
			},
		},
		"over max length resource name": {
			cfg: &configapi.Configuration{
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name:             corev1.ResourceName(fmt.Sprintf("%s/%s", strings.Repeat("a", 240), strings.Repeat("b", 14))), // 255 chars total
							DeviceClassNames: []corev1.ResourceName{"foo.com/device"},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "resources.deviceClassMappings[0].name",
				},
			},
		},
		"max length device class name": {
			cfg: &configapi.Configuration{
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name:             "valid-name",
							DeviceClassNames: []corev1.ResourceName{corev1.ResourceName(fmt.Sprintf("%s/%s", strings.Repeat("x", 240), strings.Repeat("y", 12)))}, // 253 chars total
						},
					},
				},
			},
		},
		"over max length device class name": {
			cfg: &configapi.Configuration{
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name:             "valid-name",
							DeviceClassNames: []corev1.ResourceName{corev1.ResourceName(fmt.Sprintf("%s/%s", strings.Repeat("x", 240), strings.Repeat("y", 14)))}, // 255 chars total
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "resources.deviceClassMappings[0].deviceClassNames[0]",
				},
			},
		},
		"nil device class mappings": {
			cfg: &configapi.Configuration{
				Resources: &configapi.Resources{
					DeviceClassMappings: nil,
				},
			},
		},
		"too many device class mappings": {
			cfg: &configapi.Configuration{
				Resources: &configapi.Resources{
					DeviceClassMappings: func() []configapi.DeviceClassMapping {
						mappings := make([]configapi.DeviceClassMapping, 17) // Exceed limit of 16
						for i := range 17 {
							mappings[i] = configapi.DeviceClassMapping{
								Name:             corev1.ResourceName(fmt.Sprintf("resource-%d", i)),
								DeviceClassNames: []corev1.ResourceName{corev1.ResourceName(fmt.Sprintf("device-%d.example.com", i))},
							}
						}
						return mappings
					}(),
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeTooMany,
					Field: "resources.deviceClassMappings",
				},
			},
		},
		"too many device class names per mapping": {
			cfg: &configapi.Configuration{
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name: "many-devices",
							DeviceClassNames: func() []corev1.ResourceName {
								deviceClasses := make([]corev1.ResourceName, 9) // Exceed limit of 8
								for i := range 9 {
									deviceClasses[i] = corev1.ResourceName(fmt.Sprintf("device-%d.example.com", i))
								}
								return deviceClasses
							}(),
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeTooMany,
					Field: "resources.deviceClassMappings[0].deviceClassNames",
				},
			},
		},
		"exactly max device class mappings": {
			cfg: &configapi.Configuration{
				Resources: &configapi.Resources{
					DeviceClassMappings: func() []configapi.DeviceClassMapping {
						mappings := make([]configapi.DeviceClassMapping, 16) // Exactly at limit
						for i := range 16 {
							mappings[i] = configapi.DeviceClassMapping{
								Name:             corev1.ResourceName(fmt.Sprintf("resource-%d", i)),
								DeviceClassNames: []corev1.ResourceName{corev1.ResourceName(fmt.Sprintf("device-%d.example.com", i))},
							}
						}
						return mappings
					}(),
				},
			},
		},
		"exactly max device class names per mapping": {
			cfg: &configapi.Configuration{
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name: "max-devices",
							DeviceClassNames: func() []corev1.ResourceName {
								deviceClasses := make([]corev1.ResourceName, 8) // Exactly at limit
								for i := range 8 {
									deviceClasses[i] = corev1.ResourceName(fmt.Sprintf("device-%d.example.com", i))
								}
								return deviceClasses
							}(),
						},
					},
				},
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			got := validateDeviceClassMappings(tc.cfg)
			if diff := cmp.Diff(tc.wantErr, got, cmpopts.IgnoreFields(field.Error{}, "BadValue", "Detail")); diff != "" {
				t.Errorf("validateDeviceClassMappings() returned unexpected error (-want,+got):\n%s", diff)
			}
		})
	}
}

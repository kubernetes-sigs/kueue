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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/component-base/featuregate"
	"k8s.io/utils/ptr"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
)

func TestValidate(t *testing.T) {
	testScheme := runtime.NewScheme()
	if err := configapi.AddToScheme(testScheme); err != nil {
		t.Fatal(err)
	}
	if err := clientgoscheme.AddToScheme(testScheme); err != nil {
		t.Fatal(err)
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
	defaultIntegrations := &configapi.Integrations{
		Frameworks: []string{"batch/job"},
	}

	testCases := map[string]struct {
		cfg          *configapi.Configuration
		featureGates map[featuregate.Feature]bool
		wantErr      field.ErrorList
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
		"nil managedJobsNamespaceSelector with pod framework": {
			cfg: &configapi.Configuration{
				Integrations: &configapi.Integrations{
					Frameworks: []string{"pod"},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeRequired,
					Field: "managedJobsNamespaceSelector",
				},
			},
		},

		"valid managedJobsNamespaceSelector ": {
			cfg: &configapi.Configuration{
				ManagedJobsNamespaceSelector: systemNamespacesSelector,
				Integrations:                 defaultIntegrations,
			},
			wantErr: nil,
		},

		"prohibited namespace in MatchLabels managedJobsNamespaceSelector": {
			cfg: &configapi.Configuration{
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

		"prohibited namespace in MatchExpressions with operator In managedJobsNamespaceSelector": {
			cfg: &configapi.Configuration{
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

		"prohibited namespace in MatchExpressions with operator NotIn managedJobsNamespaceSelector": {
			cfg: &configapi.Configuration{
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
					Timeout: metav1.Duration{Duration: 5 * time.Minute},
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
		"invalid an empty waitForPodsReady.timeout": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				WaitForPodsReady: &configapi.WaitForPodsReady{
					Timeout: metav1.Duration{Duration: 0},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeRequired,
					Field: "waitForPodsReady.timeout",
				},
			},
		},
		"negative waitForPodsReady.timeout": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				WaitForPodsReady: &configapi.WaitForPodsReady{
					Timeout: metav1.Duration{
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
					Timeout: metav1.Duration{Duration: 5 * time.Minute},
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
					Timeout: metav1.Duration{
						Duration: 50,
					},
					RecoveryTimeout: &metav1.Duration{
						Duration: 3,
					},
					BlockAdmission: new(false),
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
					Timeout: metav1.Duration{Duration: 5 * time.Minute},
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
					Timeout: metav1.Duration{Duration: 5 * time.Minute},
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
					Timeout: metav1.Duration{Duration: 5 * time.Minute},
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
					Origin: new("=]"),
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
					Origin: new("valid"),
					WorkerLostTimeout: &metav1.Duration{
						Duration: 2 * time.Second,
					},
				},
			},
		},
		"valid multiKueue.incrementalDispatcherConfig with incremental dispatcher": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				MultiKueue: &configapi.MultiKueue{
					DispatcherName: ptr.To(configapi.MultiKueueDispatcherModeIncremental),
					IncrementalDispatcherConfig: &configapi.IncrementalDispatcherConfig{
						StepSize: new(int32(2)),
					},
				},
			},
		},
		"multiKueue.incrementalDispatcherConfig without incremental dispatcher": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				MultiKueue: &configapi.MultiKueue{
					DispatcherName: ptr.To(configapi.MultiKueueDispatcherModeAllAtOnce),
					IncrementalDispatcherConfig: &configapi.IncrementalDispatcherConfig{
						StepSize: new(int32(2)),
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "multiKueue.incrementalDispatcherConfig",
				},
			},
		},
		"multiKueue.incrementalDispatcherConfig.stepSize below minimum": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				MultiKueue: &configapi.MultiKueue{
					DispatcherName: ptr.To(configapi.MultiKueueDispatcherModeIncremental),
					IncrementalDispatcherConfig: &configapi.IncrementalDispatcherConfig{
						StepSize: new(int32(0)),
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "multiKueue.incrementalDispatcherConfig.stepSize",
				},
			},
		},
		"empty multiKueue.clusterProfile.accessProviders.name": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				MultiKueue: &configapi.MultiKueue{
					ClusterProfile: &configapi.ClusterProfile{
						AccessProviders: []configapi.ClusterProfileAccessProvider{
							{
								Name: "",
								ExecConfig: clientcmdapi.ExecConfig{
									Command:         "test-command",
									APIVersion:      "client.authentication.k8s.io/v1",
									InteractiveMode: clientcmdapi.NeverExecInteractiveMode,
								},
							},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeRequired,
					Field: "multiKueue.clusterProfile.accessProviders.name",
				},
			},
		},
		"empty multiKueue.clusterProfile.accessProviders.execConfig.command": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				MultiKueue: &configapi.MultiKueue{
					ClusterProfile: &configapi.ClusterProfile{
						AccessProviders: []configapi.ClusterProfileAccessProvider{
							{
								Name: "test-provider",
								ExecConfig: clientcmdapi.ExecConfig{
									Command:         "",
									APIVersion:      "client.authentication.k8s.io/v1",
									InteractiveMode: clientcmdapi.NeverExecInteractiveMode,
								},
							},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeRequired,
					Field: "multiKueue.clusterProfile.accessProviders.execConfig.command",
				},
			},
		},
		"empty multiKueue.clusterProfile.accessProviders.execConfig.apiVersion": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				MultiKueue: &configapi.MultiKueue{
					ClusterProfile: &configapi.ClusterProfile{
						AccessProviders: []configapi.ClusterProfileAccessProvider{
							{
								Name: "test-provider",
								ExecConfig: clientcmdapi.ExecConfig{
									Command:         "test-command",
									APIVersion:      "",
									InteractiveMode: clientcmdapi.NeverExecInteractiveMode,
								},
							},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeRequired,
					Field: "multiKueue.clusterProfile.accessProviders.execConfig.apiVersion",
				},
			},
		},
		"empty multiKueue.clusterProfile.accessProviders.execConfig.env.name": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				MultiKueue: &configapi.MultiKueue{
					ClusterProfile: &configapi.ClusterProfile{
						AccessProviders: []configapi.ClusterProfileAccessProvider{
							{
								Name: "test-provider",
								ExecConfig: clientcmdapi.ExecConfig{
									Command:         "test-command",
									APIVersion:      "client.authentication.k8s.io/v1",
									InteractiveMode: clientcmdapi.NeverExecInteractiveMode,
									Env: []clientcmdapi.ExecEnvVar{
										{Name: "", Value: "test-value"},
									},
								},
							},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeRequired,
					Field: "multiKueue.clusterProfile.accessProviders.execConfig.env.name",
				},
			},
		},
		"empty multiKueue.clusterProfile.accessProviders.execConfig.interactiveMode": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				MultiKueue: &configapi.MultiKueue{
					ClusterProfile: &configapi.ClusterProfile{
						AccessProviders: []configapi.ClusterProfileAccessProvider{
							{
								Name: "test-provider",
								ExecConfig: clientcmdapi.ExecConfig{
									Command:         "test-command",
									APIVersion:      "client.authentication.k8s.io/v1",
									InteractiveMode: "",
								},
							},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeRequired,
					Field: "multiKueue.clusterProfile.accessProviders.execConfig.interactiveMode",
				},
			},
		},
		"invalid multiKueue.clusterProfile.accessProviders.execConfig.interactiveMode": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				MultiKueue: &configapi.MultiKueue{
					ClusterProfile: &configapi.ClusterProfile{
						AccessProviders: []configapi.ClusterProfileAccessProvider{
							{
								Name: "test-provider",
								ExecConfig: clientcmdapi.ExecConfig{
									Command:         "test-command",
									APIVersion:      "client.authentication.k8s.io/v1",
									InteractiveMode: "Invalid",
								},
							},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeNotSupported,
					Field: "multiKueue.clusterProfile.accessProviders.execConfig.interactiveMode",
				},
			},
		},
		"valid multiKueue.clusterProfile configuration": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				MultiKueue: &configapi.MultiKueue{
					ClusterProfile: &configapi.ClusterProfile{
						AccessProviders: []configapi.ClusterProfileAccessProvider{
							{
								Name: "test-provider",
								ExecConfig: clientcmdapi.ExecConfig{
									Command:         "test-command",
									APIVersion:      "client.authentication.k8s.io/v1",
									InteractiveMode: clientcmdapi.NeverExecInteractiveMode,
									Env: []clientcmdapi.ExecEnvVar{
										{Name: "TEST_VAR", Value: "test-value"},
									},
								},
							},
						},
					},
				},
			},
		},
		"valid multiKueue.clusterProfile credentialsProviders configuration": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				MultiKueue: &configapi.MultiKueue{
					ClusterProfile: &configapi.ClusterProfile{
						CredentialsProviders: []configapi.ClusterProfileCredentialsProvider{
							{
								Name: "legacy-provider",
								ExecConfig: clientcmdapi.ExecConfig{
									Command:         "test-command",
									APIVersion:      "client.authentication.k8s.io/v1",
									InteractiveMode: clientcmdapi.NeverExecInteractiveMode,
									Env: []clientcmdapi.ExecEnvVar{
										{Name: "TEST_VAR", Value: "test-value"},
									},
								},
							},
						},
					},
				},
			},
		},
		"multiKueue.clusterProfile accessProviders and credentialsProviders are mutually exclusive": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				MultiKueue: &configapi.MultiKueue{
					ClusterProfile: &configapi.ClusterProfile{
						AccessProviders: []configapi.ClusterProfileAccessProvider{
							{
								Name: "test-provider",
								ExecConfig: clientcmdapi.ExecConfig{
									Command:         "test-command",
									APIVersion:      "client.authentication.k8s.io/v1",
									InteractiveMode: clientcmdapi.NeverExecInteractiveMode,
								},
							},
						},
						CredentialsProviders: []configapi.ClusterProfileCredentialsProvider{
							{
								Name: "test-provider",
								ExecConfig: clientcmdapi.ExecConfig{
									Command:         "deprecated-command",
									APIVersion:      "client.authentication.k8s.io/v1",
									InteractiveMode: clientcmdapi.NeverExecInteractiveMode,
								},
							},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeForbidden,
					Field: "multiKueue.clusterProfile.credentialsProviders",
				},
			},
		},
		"invalid multiKueue.clusterProfile.credentialsProviders configuration": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				MultiKueue: &configapi.MultiKueue{
					ClusterProfile: &configapi.ClusterProfile{
						CredentialsProviders: []configapi.ClusterProfileCredentialsProvider{
							{
								Name: "legacy-provider",
								ExecConfig: clientcmdapi.ExecConfig{
									Command:         "",
									APIVersion:      "client.authentication.k8s.io/v1",
									InteractiveMode: clientcmdapi.NeverExecInteractiveMode,
								},
							},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeRequired,
					Field: "multiKueue.clusterProfile.credentialsProviders.execConfig.command",
				},
			},
		},
		"invalid an empty preemption strategy": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				FairSharing:  &configapi.FairSharing{},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeRequired,
					Field: "fairSharing.preemptionStrategies",
				},
			},
		},
		"unsupported preemption strategy": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				FairSharing: &configapi.FairSharing{
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
					Enable:            new(true),
					WebhookSecretName: new(":)"),
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
					Enable:             new(true),
					WebhookServiceName: new("0-invalid"),
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
					Enable:             new(false),
					WebhookServiceName: new("0-invalid"),
				},
			},
		},
		"valid .internalCertManagement": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				InternalCertManagement: &configapi.InternalCertManagement{
					Enable:             new(true),
					WebhookServiceName: new("webhook-svc"),
					WebhookSecretName:  new("webhook-sec"),
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
						AfterFinished: new(metav1.Duration{Duration: -1}),
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
						AfterFinished: new(metav1.Duration{Duration: 0}),
					},
				},
			},
		},
		"positive afterFinished in .objectRetentionPolicies.workloads": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				ObjectRetentionPolicies: &configapi.ObjectRetentionPolicies{
					Workloads: &configapi.WorkloadRetentionPolicy{
						AfterFinished: new(metav1.Duration{Duration: 1}),
					},
				},
			},
		},
		"negative afterDeactivatedByKueue in .objectRetentionPolicies.workloads": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				ObjectRetentionPolicies: &configapi.ObjectRetentionPolicies{
					Workloads: &configapi.WorkloadRetentionPolicy{
						AfterDeactivatedByKueue: new(metav1.Duration{Duration: -1}),
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
						AfterDeactivatedByKueue: new(metav1.Duration{Duration: 0}),
					},
				},
			},
		},
		"positive afterDeactivatedByKueue in .objectRetentionPolicies.workloads": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				ObjectRetentionPolicies: &configapi.ObjectRetentionPolicies{
					Workloads: &configapi.WorkloadRetentionPolicy{
						AfterDeactivatedByKueue: new(metav1.Duration{Duration: 1}),
					},
				},
			},
		},
		"valid TLS with TLS 1.2 and cipher suites": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				ControllerManager: configapi.ControllerManager{
					TLS: &configapi.TLSOptions{
						MinVersion: "VersionTLS12",
						CipherSuites: []string{
							"TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256",
						},
					},
				},
			},
		},
		"valid TLS with TLS 1.3 and no cipher suites": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				ControllerManager: configapi.ControllerManager{
					TLS: &configapi.TLSOptions{
						MinVersion: "VersionTLS13",
					},
				},
			},
		},
		"invalid TLS with TLS 1.3 and cipher suites": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				ControllerManager: configapi.ControllerManager{
					TLS: &configapi.TLSOptions{
						MinVersion: "VersionTLS13",
						CipherSuites: []string{
							"TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256",
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "tls.cipherSuites",
				},
			},
		},
		"invalid TLS and valid cipher suites": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				ControllerManager: configapi.ControllerManager{
					TLS: &configapi.TLSOptions{
						MinVersion: "DUMMY",
						CipherSuites: []string{
							"TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256",
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "tls",
				},
			},
		},
		"invalid TLS and invalid cipher suites": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				ControllerManager: configapi.ControllerManager{
					TLS: &configapi.TLSOptions{
						MinVersion: "DUMMY",
						CipherSuites: []string{
							"DUMMY",
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "tls",
				},
			},
		},
		"valid TLS with curve preferences": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				ControllerManager: configapi.ControllerManager{
					TLS: &configapi.TLSOptions{
						MinVersion:       "VersionTLS12",
						CurvePreferences: []int32{23, 29}, // P256, X25519
					},
				},
			},
		},
		"invalid TLS with invalid curve preferences": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				ControllerManager: configapi.ControllerManager{
					TLS: &configapi.TLSOptions{
						MinVersion:       "VersionTLS12",
						CurvePreferences: []int32{0},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "tls",
				},
			},
		},
		"invalid .visibilityServer.bindAddress": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				VisibilityServer: &configapi.VisibilityServerConfiguration{
					BindAddress: new("invalid"),
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "visibilityServer.bindAddress",
				},
			},
		},
		"valid .visibilityServer.bindAddress": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				VisibilityServer: &configapi.VisibilityServerConfiguration{
					BindAddress: new("127.0.0.1"),
				},
			},
		},
		"invalid .visibilityServer.bindPort": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				VisibilityServer: &configapi.VisibilityServerConfiguration{
					BindPort: ptr.To[int32](0),
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "visibilityServer.bindPort",
				},
			},
		},
		"valid .visibilityServer.bindPort": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				VisibilityServer: &configapi.VisibilityServerConfiguration{
					BindPort: ptr.To[int32](8080),
				},
			},
		},
		"quotaCheckStrategy with value ignoreUndeclared not allowed with excludeResourcePrefixes": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				Resources: &configapi.Resources{
					QuotaCheckStrategy:      ptr.To(configapi.QuotaCheckIgnoreUndeclared),
					ExcludeResourcePrefixes: []string{"foo.com/device"},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:   field.ErrorTypeInvalid,
					Field:  "resources.quotaCheckStrategy",
					Detail: "excludeResourcePrefixes is not allowed when quotaCheckStrategy is IgnoreUndeclared",
				},
			},
		},
		"quotaCheckStrategy with value ignoreundeclared allowed without excludeResourcePrefixes": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				Resources: &configapi.Resources{
					QuotaCheckStrategy: ptr.To(configapi.QuotaCheckIgnoreUndeclared),
				},
			},
		},
		"quotaCheckStrategy with value blockundeclared allowed with excludeResourcePrefixes": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				Resources: &configapi.Resources{
					QuotaCheckStrategy:      ptr.To(configapi.QuotaCheckBlockUndeclared),
					ExcludeResourcePrefixes: []string{"foo.com/device"},
				},
			},
		},
		"quotaCheckStrategy with unsupported value": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				Resources: &configapi.Resources{
					QuotaCheckStrategy: ptr.To(configapi.QuotaCheckStrategy("test")),
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeNotSupported,
					Field: "resources.quotaCheckStrategy",
				},
			},
		},
		"quotaCheckStrategy validation skipped when feature gate disabled": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				Resources: &configapi.Resources{
					QuotaCheckStrategy: ptr.To(configapi.QuotaCheckStrategy("test")),
				},
			},
			featureGates: map[featuregate.Feature]bool{
				features.QuotaCheckStrategy: false,
			},
		},
		"valid counter source on deviceClassMapping": {
			featureGates: map[featuregate.Feature]bool{features.KueueDRAIntegrationPartitionableDevices: true},
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name:             "gpu.memory",
							DeviceClassNames: []corev1.ResourceName{"mig.nvidia.com"},
							Sources: []configapi.DeviceClassSourceConfig{
								{Counter: &configapi.DeviceClassCounterSource{
									Name:   "memory",
									Driver: "gpu.nvidia.com",
									DeviceSelector: resourcev1.DeviceSelector{
										CEL: &resourcev1.CELDeviceSelector{Expression: "device.driver == 'gpu.nvidia.com'"},
									},
								}},
							},
						},
					},
				},
			},
		},
		"multi-counter: same DeviceClass with different counter names allowed": {
			featureGates: map[featuregate.Feature]bool{features.KueueDRAIntegrationPartitionableDevices: true},
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name:             "gpu.memory",
							DeviceClassNames: []corev1.ResourceName{"mig.nvidia.com"},
							Sources: []configapi.DeviceClassSourceConfig{
								{Counter: &configapi.DeviceClassCounterSource{
									Name:   "memory",
									Driver: "gpu.nvidia.com",
									DeviceSelector: resourcev1.DeviceSelector{
										CEL: &resourcev1.CELDeviceSelector{Expression: "device.driver == 'gpu.nvidia.com'"},
									},
								}},
							},
						},
						{
							Name:             "gpu.compute",
							DeviceClassNames: []corev1.ResourceName{"mig.nvidia.com"},
							Sources: []configapi.DeviceClassSourceConfig{
								{Counter: &configapi.DeviceClassCounterSource{
									Name:   "multiprocessors",
									Driver: "gpu.nvidia.com",
									DeviceSelector: resourcev1.DeviceSelector{
										CEL: &resourcev1.CELDeviceSelector{Expression: "device.driver == 'gpu.nvidia.com'"},
									},
								}},
							},
						},
					},
				},
			},
		},
		"multi-counter: same DeviceClass with same counter name rejected": {
			featureGates: map[featuregate.Feature]bool{features.KueueDRAIntegrationPartitionableDevices: true},
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name:             "gpu.memory",
							DeviceClassNames: []corev1.ResourceName{"mig.nvidia.com"},
							Sources: []configapi.DeviceClassSourceConfig{
								{Counter: &configapi.DeviceClassCounterSource{
									Name:   "memory",
									Driver: "gpu.nvidia.com",
									DeviceSelector: resourcev1.DeviceSelector{
										CEL: &resourcev1.CELDeviceSelector{Expression: "device.driver == 'gpu.nvidia.com'"},
									},
								}},
							},
						},
						{
							Name:             "gpu.memory2",
							DeviceClassNames: []corev1.ResourceName{"mig.nvidia.com"},
							Sources: []configapi.DeviceClassSourceConfig{
								{Counter: &configapi.DeviceClassCounterSource{
									Name:   "memory",
									Driver: "gpu.nvidia.com",
									DeviceSelector: resourcev1.DeviceSelector{
										CEL: &resourcev1.CELDeviceSelector{Expression: "device.driver == 'gpu.nvidia.com'"},
									},
								}},
							},
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
		"multi-counter: whole-device and counter for same DeviceClass rejected": {
			featureGates: map[featuregate.Feature]bool{features.KueueDRAIntegrationPartitionableDevices: true},
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name:             "gpu",
							DeviceClassNames: []corev1.ResourceName{"gpu.nvidia.com"},
						},
						{
							Name:             "gpu.memory",
							DeviceClassNames: []corev1.ResourceName{"gpu.nvidia.com"},
							Sources: []configapi.DeviceClassSourceConfig{
								{Counter: &configapi.DeviceClassCounterSource{
									Name:   "memory",
									Driver: "gpu.nvidia.com",
									DeviceSelector: resourcev1.DeviceSelector{
										CEL: &resourcev1.CELDeviceSelector{Expression: "device.driver == 'gpu.nvidia.com'"},
									},
								}},
							},
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
		"multi-counter: counter then whole-device for same DeviceClass rejected": {
			featureGates: map[featuregate.Feature]bool{features.KueueDRAIntegrationPartitionableDevices: true},
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name:             "gpu.memory",
							DeviceClassNames: []corev1.ResourceName{"gpu.nvidia.com"},
							Sources: []configapi.DeviceClassSourceConfig{
								{Counter: &configapi.DeviceClassCounterSource{
									Name:   "memory",
									Driver: "gpu.nvidia.com",
									DeviceSelector: resourcev1.DeviceSelector{
										CEL: &resourcev1.CELDeviceSelector{Expression: "device.driver == 'gpu.nvidia.com'"},
									},
								}},
							},
						},
						{
							Name:             "gpu",
							DeviceClassNames: []corev1.ResourceName{"gpu.nvidia.com"},
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
		"sources configured but KueueDRAIntegrationPartitionableDevices disabled": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name:             "gpu.memory",
							DeviceClassNames: []corev1.ResourceName{"mig.nvidia.com"},
							Sources: []configapi.DeviceClassSourceConfig{
								{Counter: &configapi.DeviceClassCounterSource{
									Name:   "memory",
									Driver: "gpu.nvidia.com",
									DeviceSelector: resourcev1.DeviceSelector{
										CEL: &resourcev1.CELDeviceSelector{Expression: "device.driver == 'gpu.nvidia.com'"},
									},
								}},
							},
						},
					},
				},
			},
			featureGates: map[featuregate.Feature]bool{features.KueueDRAIntegrationPartitionableDevices: false},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "resources.deviceClassMappings[0].sources[0].counter",
				},
			},
		},
		"sources: missing driver": {
			featureGates: map[featuregate.Feature]bool{features.KueueDRAIntegrationPartitionableDevices: true},
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name:             "gpu.memory",
							DeviceClassNames: []corev1.ResourceName{"mig.nvidia.com"},
							Sources: []configapi.DeviceClassSourceConfig{
								{Counter: &configapi.DeviceClassCounterSource{
									Name:   "memory",
									Driver: "",
									DeviceSelector: resourcev1.DeviceSelector{
										CEL: &resourcev1.CELDeviceSelector{Expression: "device.driver == 'gpu.nvidia.com'"},
									},
								}},
							},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeRequired,
					Field: "resources.deviceClassMappings[0].sources[0].counter.driver",
				},
			},
		},
		"sources: missing name": {
			featureGates: map[featuregate.Feature]bool{features.KueueDRAIntegrationPartitionableDevices: true},
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name:             "gpu.memory",
							DeviceClassNames: []corev1.ResourceName{"mig.nvidia.com"},
							Sources: []configapi.DeviceClassSourceConfig{
								{Counter: &configapi.DeviceClassCounterSource{
									Name:   "",
									Driver: "gpu.nvidia.com",
									DeviceSelector: resourcev1.DeviceSelector{
										CEL: &resourcev1.CELDeviceSelector{Expression: "device.driver == 'gpu.nvidia.com'"},
									},
								}},
							},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeRequired,
					Field: "resources.deviceClassMappings[0].sources[0].counter.name",
				},
			},
		},
		"sources: invalid counter name format": {
			featureGates: map[featuregate.Feature]bool{features.KueueDRAIntegrationPartitionableDevices: true},
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name:             "gpu.memory",
							DeviceClassNames: []corev1.ResourceName{"mig.nvidia.com"},
							Sources: []configapi.DeviceClassSourceConfig{
								{Counter: &configapi.DeviceClassCounterSource{
									Name:   "INVALID_NAME",
									Driver: "gpu.nvidia.com",
									DeviceSelector: resourcev1.DeviceSelector{
										CEL: &resourcev1.CELDeviceSelector{Expression: "device.driver == 'gpu.nvidia.com'"},
									},
								}},
							},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "resources.deviceClassMappings[0].sources[0].counter.name",
				},
			},
		},
		"sources: invalid driver name format": {
			featureGates: map[featuregate.Feature]bool{features.KueueDRAIntegrationPartitionableDevices: true},
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name:             "gpu.memory",
							DeviceClassNames: []corev1.ResourceName{"mig.nvidia.com"},
							Sources: []configapi.DeviceClassSourceConfig{
								{Counter: &configapi.DeviceClassCounterSource{
									Name:   "memory",
									Driver: "NOT_VALID!!",
									DeviceSelector: resourcev1.DeviceSelector{
										CEL: &resourcev1.CELDeviceSelector{Expression: "device.driver == 'gpu.nvidia.com'"},
									},
								}},
							},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "resources.deviceClassMappings[0].sources[0].counter.driver",
				},
			},
		},
		"sources: driver name exceeds max length": {
			featureGates: map[featuregate.Feature]bool{features.KueueDRAIntegrationPartitionableDevices: true},
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name:             "gpu.memory",
							DeviceClassNames: []corev1.ResourceName{"mig.nvidia.com"},
							Sources: []configapi.DeviceClassSourceConfig{
								{Counter: &configapi.DeviceClassCounterSource{
									Name:   "memory",
									Driver: "gpu-accelerator.nvidia-corporation.datacenter.example.com.internal",
									DeviceSelector: resourcev1.DeviceSelector{
										CEL: &resourcev1.CELDeviceSelector{Expression: "device.driver == 'gpu.nvidia.com'"},
									},
								}},
							},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "resources.deviceClassMappings[0].sources[0].counter.driver",
				},
			},
		},
		"sources: missing deviceSelector": {
			featureGates: map[featuregate.Feature]bool{features.KueueDRAIntegrationPartitionableDevices: true},
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name:             "gpu.memory",
							DeviceClassNames: []corev1.ResourceName{"mig.nvidia.com"},
							Sources: []configapi.DeviceClassSourceConfig{
								{Counter: &configapi.DeviceClassCounterSource{
									Name:   "memory",
									Driver: "gpu.nvidia.com",
								}},
							},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeRequired,
					Field: "resources.deviceClassMappings[0].sources[0].counter.deviceSelector.cel.expression",
				},
			},
		},
		"sources: multiple counter entries valid": {
			featureGates: map[featuregate.Feature]bool{features.KueueDRAIntegrationPartitionableDevices: true},
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name:             "gpu.memory",
							DeviceClassNames: []corev1.ResourceName{"mig.nvidia.com"},
							Sources: []configapi.DeviceClassSourceConfig{
								{Counter: &configapi.DeviceClassCounterSource{
									Name:   "memory",
									Driver: "gpu.nvidia.com",
									DeviceSelector: resourcev1.DeviceSelector{
										CEL: &resourcev1.CELDeviceSelector{Expression: "device.driver == 'gpu.nvidia.com'"},
									},
								}},
								{Counter: &configapi.DeviceClassCounterSource{
									Name:   "multiprocessors",
									Driver: "gpu.nvidia.com",
									DeviceSelector: resourcev1.DeviceSelector{
										CEL: &resourcev1.CELDeviceSelector{Expression: "device.driver == 'gpu.nvidia.com'"},
									},
								}},
							},
						},
					},
				},
			},
		},
		"valid capacity source": {
			featureGates: map[featuregate.Feature]bool{features.KueueDRAIntegrationConsumableCapacity: true},
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name:             "gpu.memory",
							DeviceClassNames: []corev1.ResourceName{"vgpu.example.com"},
							Sources: []configapi.DeviceClassSourceConfig{
								{Capacity: &configapi.DeviceClassCapacitySource{
									Name:   "memory",
									Driver: "gpu.example.com",
									DeviceSelector: resourcev1.DeviceSelector{
										CEL: &resourcev1.CELDeviceSelector{Expression: "device.driver == 'gpu.example.com'"},
									},
								}},
							},
						},
					},
				},
			},
		},
		"valid capacity source with qualified name": {
			featureGates: map[featuregate.Feature]bool{features.KueueDRAIntegrationConsumableCapacity: true},
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name:             "gpu.memory",
							DeviceClassNames: []corev1.ResourceName{"vgpu.example.com"},
							Sources: []configapi.DeviceClassSourceConfig{
								{Capacity: &configapi.DeviceClassCapacitySource{
									Name:   "gpu.example.com/memory",
									Driver: "gpu.example.com",
									DeviceSelector: resourcev1.DeviceSelector{
										CEL: &resourcev1.CELDeviceSelector{Expression: "device.driver == 'gpu.example.com'"},
									},
								}},
							},
						},
					},
				},
			},
		},
		"capacity source with CC gate disabled": {
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name:             "gpu.memory",
							DeviceClassNames: []corev1.ResourceName{"vgpu.example.com"},
							Sources: []configapi.DeviceClassSourceConfig{
								{Capacity: &configapi.DeviceClassCapacitySource{
									Name:   "memory",
									Driver: "gpu.example.com",
									DeviceSelector: resourcev1.DeviceSelector{
										CEL: &resourcev1.CELDeviceSelector{Expression: "device.driver == 'gpu.example.com'"},
									},
								}},
							},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "resources.deviceClassMappings[0].sources[0].capacity",
				},
			},
		},
		"multiple capacity sources (multi-dimension)": {
			featureGates: map[featuregate.Feature]bool{features.KueueDRAIntegrationConsumableCapacity: true},
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name:             "gpu.memory",
							DeviceClassNames: []corev1.ResourceName{"vgpu.example.com"},
							Sources: []configapi.DeviceClassSourceConfig{
								{Capacity: &configapi.DeviceClassCapacitySource{
									Name:   "memory",
									Driver: "gpu.example.com",
									DeviceSelector: resourcev1.DeviceSelector{
										CEL: &resourcev1.CELDeviceSelector{Expression: "device.driver == 'gpu.example.com'"},
									},
								}},
								{Capacity: &configapi.DeviceClassCapacitySource{
									Name:   "cores",
									Driver: "gpu.example.com",
									DeviceSelector: resourcev1.DeviceSelector{
										CEL: &resourcev1.CELDeviceSelector{Expression: "device.driver == 'gpu.example.com'"},
									},
								}},
							},
						},
					},
				},
			},
		},
		"counter and capacity mixing rejected": {
			featureGates: map[featuregate.Feature]bool{
				features.KueueDRAIntegrationPartitionableDevices: true,
				features.KueueDRAIntegrationConsumableCapacity:   true,
			},
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name:             "gpu.memory",
							DeviceClassNames: []corev1.ResourceName{"mig.nvidia.com"},
							Sources: []configapi.DeviceClassSourceConfig{
								{Counter: &configapi.DeviceClassCounterSource{
									Name:   "memory",
									Driver: "gpu.nvidia.com",
									DeviceSelector: resourcev1.DeviceSelector{
										CEL: &resourcev1.CELDeviceSelector{Expression: "device.driver == 'gpu.nvidia.com'"},
									},
								}},
								{Capacity: &configapi.DeviceClassCapacitySource{
									Name:   "cores",
									Driver: "gpu.nvidia.com",
									DeviceSelector: resourcev1.DeviceSelector{
										CEL: &resourcev1.CELDeviceSelector{Expression: "device.driver == 'gpu.nvidia.com'"},
									},
								}},
							},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "resources.deviceClassMappings[0].sources",
				},
			},
		},
		"capacity source with mixed-case driver rejected": {
			featureGates: map[featuregate.Feature]bool{features.KueueDRAIntegrationConsumableCapacity: true},
			cfg: &configapi.Configuration{
				Integrations: defaultIntegrations,
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name:             "gpu.memory",
							DeviceClassNames: []corev1.ResourceName{"vgpu.example.com"},
							Sources: []configapi.DeviceClassSourceConfig{
								{Capacity: &configapi.DeviceClassCapacitySource{
									Name:   "memory",
									Driver: "Gpu.Example.Com",
									DeviceSelector: resourcev1.DeviceSelector{
										CEL: &resourcev1.CELDeviceSelector{Expression: "device.driver == 'gpu.example.com'"},
									},
								}},
							},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "resources.deviceClassMappings[0].sources[0].capacity.driver",
				},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			if diff := cmp.Diff(tc.wantErr, Validate(tc.cfg, testScheme), cmpopts.IgnoreFields(field.Error{}, "BadValue", "Detail")); diff != "" {
				t.Errorf("Unexpected returned error (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestLoadAndValidateFeatureGates(t *testing.T) {
	cases := map[string]struct {
		featureGatesCLI string
		featureGateMap  map[string]bool
		gatesToRestore  map[featuregate.Feature]bool
		wantErr         field.ErrorList
	}{
		"no feature gates is null": {
			featureGatesCLI: "",
		},
		"feature gate cli": {
			featureGatesCLI: string(
				features.KueueDRAIntegration,
			) + "=false," + string(
				features.KueueDRAIntegrationExtendedResource,
			) + "=false," + string(
				features.KueueDRAIntegrationPartitionableDevices,
			) + "=false",
			gatesToRestore: map[featuregate.Feature]bool{
				features.KueueDRAIntegration:                     false,
				features.KueueDRAIntegrationExtendedResource:     false,
				features.KueueDRAIntegrationPartitionableDevices: false,
			},
		},
		"cannot specify both feature gates": {
			featureGatesCLI: string(features.KueueDRAIntegration) + "=false",
			featureGateMap: map[string]bool{
				string(features.KueueDRAIntegration):                     false,
				string(features.KueueDRAIntegrationExtendedResource):     false,
				string(features.KueueDRAIntegrationPartitionableDevices): false,
			},
			gatesToRestore: map[featuregate.Feature]bool{
				features.KueueDRAIntegration:                     false,
				features.KueueDRAIntegrationExtendedResource:     false,
				features.KueueDRAIntegrationPartitionableDevices: false,
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:   field.ErrorTypeInvalid,
					Field:  "featureGates",
					Detail: "feature gates for CLI and configuration cannot both specified",
				},
			},
		},
		"cannot set TAS profile with TAS disabled": {
			featureGateMap: map[string]bool{
				string(features.TASProfileMixed):                  true,
				string(features.TopologyAwareScheduling):          false,
				string(features.TASHandleOverlappingFlavors):      false,
				string(features.TASFailedNodeReplacement):         false,
				string(features.TASFailedNodeReplacementFailFast): false,
				string(features.TASReplaceNodeOnPodTermination):   false,
				string(features.TASReplaceNodeOnNodeTaints):       false,
				string(features.TASMultiLayerTopology):            false,
			},
			gatesToRestore: map[featuregate.Feature]bool{
				features.TASProfileMixed:                  false,
				features.TopologyAwareScheduling:          true,
				features.TASFailedNodeReplacement:         true,
				features.TASFailedNodeReplacementFailFast: true,
				features.TASReplaceNodeOnPodTermination:   true,
				features.TASReplaceNodeOnNodeTaints:       true,
				features.TASMultiLayerTopology:            true,
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:   field.ErrorTypeInvalid,
					Field:  "featureGates",
					Detail: "cannot use a TAS profile with TAS disabled",
				},
			},
		},
		"TASReplaceNodeDueToNotReadyOverFixedTime requires TASFailedNodeReplacement": {
			featureGateMap: map[string]bool{
				string(features.TASReplaceNodeDueToNotReadyOverFixedTime): true,
				string(features.TopologyAwareScheduling):                  true,
				string(features.TASFailedNodeReplacement):                 false,
				string(features.TASFailedNodeReplacementFailFast):         false,
				string(features.TASReplaceNodeOnPodTermination):           false,
				string(features.TASProfileMixed):                          false,
			},
			gatesToRestore: map[featuregate.Feature]bool{
				features.TASReplaceNodeDueToNotReadyOverFixedTime: false,
				features.TopologyAwareScheduling:                  true,
				features.TASFailedNodeReplacement:                 true,
				features.TASFailedNodeReplacementFailFast:         true,
				features.TASReplaceNodeOnPodTermination:           true,
				features.TASProfileMixed:                          false,
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:   field.ErrorTypeInvalid,
					Field:  "featureGates",
					Detail: "TASReplaceNodeDueToNotReadyOverFixedTime requires TASFailedNodeReplacement to be enabled",
				},
			},
		},
		"ElasticJobsViaWorkloadSlicesWithTAS requires ElasticJobsViaWorkloadSlices": {
			featureGateMap: map[string]bool{
				string(features.ElasticJobsViaWorkloadSlicesWithTAS): true,
				string(features.TopologyAwareScheduling):             true,
				string(features.ElasticJobsViaWorkloadSlices):        false,
				string(features.TASProfileMixed):                     false,
			},
			gatesToRestore: map[featuregate.Feature]bool{
				features.ElasticJobsViaWorkloadSlicesWithTAS: false,
				features.TopologyAwareScheduling:             false,
				features.ElasticJobsViaWorkloadSlices:        true,
				features.TASProfileMixed:                     true,
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:   field.ErrorTypeInvalid,
					Field:  "featureGates",
					Detail: "ElasticJobsViaWorkloadSlicesWithTAS requires ElasticJobsViaWorkloadSlices to be enabled",
				},
			},
		},
		"ElasticJobsViaWorkloadSlicesWithTAS requires TopologyAwareScheduling": {
			featureGateMap: map[string]bool{
				string(features.ElasticJobsViaWorkloadSlicesWithTAS): true,
				string(features.ElasticJobsViaWorkloadSlices):        true,
				string(features.TopologyAwareScheduling):             false,
				string(features.TASHandleOverlappingFlavors):         false,
				string(features.TASProfileMixed):                     false,
				string(features.TASFailedNodeReplacement):            false,
				string(features.TASFailedNodeReplacementFailFast):    false,
				string(features.TASReplaceNodeOnPodTermination):      false,
				string(features.TASReplaceNodeOnNodeTaints):          false,
				string(features.TASMultiLayerTopology):               false,
			},
			gatesToRestore: map[featuregate.Feature]bool{
				features.ElasticJobsViaWorkloadSlicesWithTAS: false,
				features.ElasticJobsViaWorkloadSlices:        false,
				features.TopologyAwareScheduling:             true,
				features.TASProfileMixed:                     true,
				features.TASFailedNodeReplacement:            true,
				features.TASFailedNodeReplacementFailFast:    true,
				features.TASReplaceNodeOnPodTermination:      true,
				features.TASReplaceNodeOnNodeTaints:          true,
				features.TASMultiLayerTopology:               true,
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:   field.ErrorTypeInvalid,
					Field:  "featureGates",
					Detail: "ElasticJobsViaWorkloadSlicesWithTAS requires TopologyAwareScheduling to be enabled",
				},
			},
		},
		"ElasticJobsViaWorkloadSlicesWithTAS valid when all dependencies enabled": {
			featureGateMap: map[string]bool{
				string(features.ElasticJobsViaWorkloadSlicesWithTAS): true,
				string(features.ElasticJobsViaWorkloadSlices):        true,
				string(features.TopologyAwareScheduling):             true,
				string(features.TASProfileMixed):                     false,
			},
			gatesToRestore: map[featuregate.Feature]bool{
				features.ElasticJobsViaWorkloadSlicesWithTAS: false,
				features.ElasticJobsViaWorkloadSlices:        false,
				features.TopologyAwareScheduling:             false,
				features.TASProfileMixed:                     true,
			},
		},
		"multiple FG validation errors at once": {
			featureGateMap: map[string]bool{
				string(features.TASProfileMixed):                     true,
				string(features.TopologyAwareScheduling):             false,
				string(features.TASHandleOverlappingFlavors):         true,
				string(features.ElasticJobsViaWorkloadSlicesWithTAS): true,
				string(features.ElasticJobsViaWorkloadSlices):        false,
				string(features.TASFailedNodeReplacement):            false,
				string(features.TASFailedNodeReplacementFailFast):    false,
				string(features.TASReplaceNodeOnPodTermination):      false,
				string(features.TASReplaceNodeOnNodeTaints):          false,
				string(features.TASMultiLayerTopology):               false,
			},
			gatesToRestore: map[featuregate.Feature]bool{
				features.TASProfileMixed:                     false,
				features.TopologyAwareScheduling:             true,
				features.TASHandleOverlappingFlavors:         true,
				features.ElasticJobsViaWorkloadSlicesWithTAS: false,
				features.ElasticJobsViaWorkloadSlices:        true,
				features.TASFailedNodeReplacement:            true,
				features.TASFailedNodeReplacementFailFast:    true,
				features.TASReplaceNodeOnPodTermination:      true,
				features.TASReplaceNodeOnNodeTaints:          true,
				features.TASMultiLayerTopology:               true,
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:   field.ErrorTypeInvalid,
					Field:  "featureGates",
					Detail: "cannot use a TAS profile with TAS disabled",
				},
				&field.Error{
					Type:   field.ErrorTypeInvalid,
					Field:  "featureGates",
					Detail: "TASHandleOverlappingFlavors requires TopologyAwareScheduling to be enabled",
				},
				&field.Error{
					Type:   field.ErrorTypeInvalid,
					Field:  "featureGates",
					Detail: "ElasticJobsViaWorkloadSlicesWithTAS requires ElasticJobsViaWorkloadSlices to be enabled",
				},
				&field.Error{
					Type:   field.ErrorTypeInvalid,
					Field:  "featureGates",
					Detail: "ElasticJobsViaWorkloadSlicesWithTAS requires TopologyAwareScheduling to be enabled",
				},
			},
		},
		"KueueDRAIntegrationExtendedResource requires KueueDRAIntegration": {
			featureGateMap: map[string]bool{
				string(features.KueueDRAIntegrationExtendedResource):     true,
				string(features.KueueDRAIntegration):                     false,
				string(features.KueueDRAIntegrationPartitionableDevices): false,
			},
			gatesToRestore: map[featuregate.Feature]bool{
				features.KueueDRAIntegrationExtendedResource:     false,
				features.KueueDRAIntegration:                     true,
				features.KueueDRAIntegrationPartitionableDevices: true,
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:   field.ErrorTypeInvalid,
					Field:  "featureGates",
					Detail: "KueueDRAIntegrationExtendedResource requires KueueDRAIntegration to be enabled",
				},
			},
		},
		"KueueDRAIntegrationPartitionableDevices requires KueueDRAIntegration": {
			featureGateMap: map[string]bool{
				string(features.KueueDRAIntegrationPartitionableDevices): true,
				string(features.KueueDRAIntegration):                     false,
				string(features.KueueDRAIntegrationExtendedResource):     false,
			},
			gatesToRestore: map[featuregate.Feature]bool{
				features.KueueDRAIntegrationPartitionableDevices: false,
				features.KueueDRAIntegration:                     true,
				features.KueueDRAIntegrationExtendedResource:     true,
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:   field.ErrorTypeInvalid,
					Field:  "featureGates",
					Detail: "KueueDRAIntegrationPartitionableDevices requires KueueDRAIntegration to be enabled",
				},
			},
		},
		"KueueDRAIntegrationConsumableCapacity requires KueueDRAIntegration": {
			featureGateMap: map[string]bool{
				string(features.KueueDRAIntegrationConsumableCapacity):   true,
				string(features.KueueDRAIntegration):                     false,
				string(features.KueueDRAIntegrationExtendedResource):     false,
				string(features.KueueDRAIntegrationPartitionableDevices): false,
			},
			gatesToRestore: map[featuregate.Feature]bool{
				features.KueueDRAIntegrationConsumableCapacity:   false,
				features.KueueDRAIntegration:                     true,
				features.KueueDRAIntegrationExtendedResource:     true,
				features.KueueDRAIntegrationPartitionableDevices: true,
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:   field.ErrorTypeInvalid,
					Field:  "featureGates",
					Detail: "KueueDRAIntegrationConsumableCapacity requires KueueDRAIntegration to be enabled",
				},
			},
		},
		"UnadmittedWorkloadsExplicitStatus requires UnadmittedWorkloadsObservability": {
			featureGateMap: map[string]bool{
				string(features.UnadmittedWorkloadsExplicitStatus): true,
				string(features.UnadmittedWorkloadsObservability):  false,
			},
			gatesToRestore: map[featuregate.Feature]bool{
				features.UnadmittedWorkloadsExplicitStatus: false,
				features.UnadmittedWorkloadsObservability:  true,
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:   field.ErrorTypeInvalid,
					Field:  "featureGates",
					Detail: "UnadmittedWorkloadsExplicitStatus requires UnadmittedWorkloadsObservability to be enabled",
				},
			},
		},
		"TASHandleOverlappingFlavors requires TopologyAwareScheduling": {
			featureGateMap: map[string]bool{
				string(features.TopologyAwareScheduling):          false,
				string(features.TASProfileMixed):                  false,
				string(features.TASHandleOverlappingFlavors):      true,
				string(features.TASFailedNodeReplacement):         false,
				string(features.TASFailedNodeReplacementFailFast): false,
				string(features.TASReplaceNodeOnPodTermination):   false,
				string(features.TASReplaceNodeOnNodeTaints):       false,
				string(features.TASMultiLayerTopology):            false,
			},
			gatesToRestore: map[featuregate.Feature]bool{
				features.TASHandleOverlappingFlavors:      true,
				features.TopologyAwareScheduling:          true,
				features.TASProfileMixed:                  true,
				features.TASFailedNodeReplacement:         true,
				features.TASFailedNodeReplacementFailFast: true,
				features.TASReplaceNodeOnPodTermination:   true,
				features.TASReplaceNodeOnNodeTaints:       true,
				features.TASMultiLayerTopology:            true,
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:   field.ErrorTypeInvalid,
					Field:  "featureGates",
					Detail: "TASHandleOverlappingFlavors requires TopologyAwareScheduling to be enabled",
				},
			},
		},
		"TASHandleOverlappingFlavors valid when all dependencies enabled": {
			featureGateMap: map[string]bool{
				string(features.TopologyAwareScheduling):     true,
				string(features.TASHandleOverlappingFlavors): true,
			},
			gatesToRestore: map[featuregate.Feature]bool{
				features.TASHandleOverlappingFlavors: true,
				features.TopologyAwareScheduling:     true,
				features.TASProfileMixed:             true,
			},
		},
		"TASFailedNodeReplacement requires TopologyAwareScheduling": {
			featureGateMap: map[string]bool{
				string(features.TopologyAwareScheduling):          false,
				string(features.TASProfileMixed):                  false,
				string(features.TASHandleOverlappingFlavors):      false,
				string(features.TASFailedNodeReplacement):         true,
				string(features.TASFailedNodeReplacementFailFast): false,
				string(features.TASReplaceNodeOnPodTermination):   false,
				string(features.TASReplaceNodeOnNodeTaints):       false,
				string(features.TASMultiLayerTopology):            false,
			},
			gatesToRestore: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling:          true,
				features.TASProfileMixed:                  true,
				features.TASHandleOverlappingFlavors:      true,
				features.TASFailedNodeReplacement:         true,
				features.TASFailedNodeReplacementFailFast: true,
				features.TASReplaceNodeOnPodTermination:   true,
				features.TASReplaceNodeOnNodeTaints:       true,
				features.TASMultiLayerTopology:            true,
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:   field.ErrorTypeInvalid,
					Field:  "featureGates",
					Detail: "TASFailedNodeReplacement requires TopologyAwareScheduling to be enabled",
				},
			},
		},
		"TASBalancedPlacement requires TopologyAwareScheduling": {
			featureGateMap: map[string]bool{
				string(features.TopologyAwareScheduling):          false,
				string(features.TASProfileMixed):                  false,
				string(features.TASHandleOverlappingFlavors):      false,
				string(features.TASFailedNodeReplacement):         false,
				string(features.TASFailedNodeReplacementFailFast): false,
				string(features.TASReplaceNodeOnPodTermination):   false,
				string(features.TASReplaceNodeOnNodeTaints):       false,
				string(features.TASBalancedPlacement):             true,
				string(features.TASMultiLayerTopology):            false,
			},
			gatesToRestore: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling:          true,
				features.TASProfileMixed:                  true,
				features.TASHandleOverlappingFlavors:      true,
				features.TASFailedNodeReplacement:         true,
				features.TASFailedNodeReplacementFailFast: true,
				features.TASReplaceNodeOnPodTermination:   true,
				features.TASReplaceNodeOnNodeTaints:       true,
				features.TASBalancedPlacement:             false,
				features.TASMultiLayerTopology:            true,
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:   field.ErrorTypeInvalid,
					Field:  "featureGates",
					Detail: "TASBalancedPlacement requires TopologyAwareScheduling to be enabled",
				},
			},
		},
		"TASReplaceNodeOnNodeTaints requires TopologyAwareScheduling": {
			featureGateMap: map[string]bool{
				string(features.TopologyAwareScheduling):          false,
				string(features.TASProfileMixed):                  false,
				string(features.TASHandleOverlappingFlavors):      false,
				string(features.TASFailedNodeReplacement):         false,
				string(features.TASFailedNodeReplacementFailFast): false,
				string(features.TASReplaceNodeOnPodTermination):   false,
				string(features.TASReplaceNodeOnNodeTaints):       true,
				string(features.TASMultiLayerTopology):            false,
			},
			gatesToRestore: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling:          true,
				features.TASProfileMixed:                  true,
				features.TASHandleOverlappingFlavors:      true,
				features.TASFailedNodeReplacement:         true,
				features.TASFailedNodeReplacementFailFast: true,
				features.TASReplaceNodeOnPodTermination:   true,
				features.TASReplaceNodeOnNodeTaints:       true,
				features.TASMultiLayerTopology:            true,
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:   field.ErrorTypeInvalid,
					Field:  "featureGates",
					Detail: "TASReplaceNodeOnNodeTaints requires TopologyAwareScheduling to be enabled",
				},
			},
		},
		"TASMultiLayerTopology requires TopologyAwareScheduling": {
			featureGateMap: map[string]bool{
				string(features.TopologyAwareScheduling):          false,
				string(features.TASProfileMixed):                  false,
				string(features.TASHandleOverlappingFlavors):      false,
				string(features.TASFailedNodeReplacement):         false,
				string(features.TASFailedNodeReplacementFailFast): false,
				string(features.TASReplaceNodeOnPodTermination):   false,
				string(features.TASReplaceNodeOnNodeTaints):       false,
				string(features.TASMultiLayerTopology):            true,
			},
			gatesToRestore: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling:          true,
				features.TASProfileMixed:                  true,
				features.TASHandleOverlappingFlavors:      true,
				features.TASFailedNodeReplacement:         true,
				features.TASFailedNodeReplacementFailFast: true,
				features.TASReplaceNodeOnPodTermination:   true,
				features.TASReplaceNodeOnNodeTaints:       true,
				features.TASMultiLayerTopology:            false,
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:   field.ErrorTypeInvalid,
					Field:  "featureGates",
					Detail: "TASMultiLayerTopology requires TopologyAwareScheduling to be enabled",
				},
			},
		},
		"TASRespectNodeAffinityPreferred requires TopologyAwareScheduling": {
			featureGateMap: map[string]bool{
				string(features.TopologyAwareScheduling):          false,
				string(features.TASProfileMixed):                  false,
				string(features.TASHandleOverlappingFlavors):      false,
				string(features.TASFailedNodeReplacement):         false,
				string(features.TASFailedNodeReplacementFailFast): false,
				string(features.TASReplaceNodeOnPodTermination):   false,
				string(features.TASReplaceNodeOnNodeTaints):       false,
				string(features.TASRespectNodeAffinityPreferred):  true,
				string(features.TASMultiLayerTopology):            false,
			},
			gatesToRestore: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling:          true,
				features.TASProfileMixed:                  true,
				features.TASHandleOverlappingFlavors:      true,
				features.TASFailedNodeReplacement:         true,
				features.TASFailedNodeReplacementFailFast: true,
				features.TASReplaceNodeOnPodTermination:   true,
				features.TASReplaceNodeOnNodeTaints:       true,
				features.TASRespectNodeAffinityPreferred:  false,
				features.TASMultiLayerTopology:            true,
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:   field.ErrorTypeInvalid,
					Field:  "featureGates",
					Detail: "TASRespectNodeAffinityPreferred requires TopologyAwareScheduling to be enabled",
				},
			},
		},
		"TASFailedNodeReplacementFailFast requires TASFailedNodeReplacement": {
			featureGateMap: map[string]bool{
				string(features.TopologyAwareScheduling):          true,
				string(features.TASFailedNodeReplacement):         false,
				string(features.TASFailedNodeReplacementFailFast): true,
				string(features.TASReplaceNodeOnPodTermination):   false,
				string(features.TASMultiLayerTopology):            false,
			},
			gatesToRestore: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling:          true,
				features.TASFailedNodeReplacement:         true,
				features.TASFailedNodeReplacementFailFast: true,
				features.TASReplaceNodeOnPodTermination:   true,
				features.TASMultiLayerTopology:            true,
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:   field.ErrorTypeInvalid,
					Field:  "featureGates",
					Detail: "TASFailedNodeReplacementFailFast requires TASFailedNodeReplacement to be enabled",
				},
			},
		},
		"TASReplaceNodeOnPodTermination requires TASFailedNodeReplacement": {
			featureGateMap: map[string]bool{
				string(features.TopologyAwareScheduling):          true,
				string(features.TASFailedNodeReplacement):         false,
				string(features.TASFailedNodeReplacementFailFast): false,
				string(features.TASReplaceNodeOnPodTermination):   true,
			},
			gatesToRestore: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling:          true,
				features.TASFailedNodeReplacement:         true,
				features.TASFailedNodeReplacementFailFast: true,
				features.TASReplaceNodeOnPodTermination:   true,
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:   field.ErrorTypeInvalid,
					Field:  "featureGates",
					Detail: "TASReplaceNodeOnPodTermination requires TASFailedNodeReplacement to be enabled",
				},
			},
		},
		"TASFailedNodeReplacementFailFast requires both TopologyAwareScheduling and TASFailedNodeReplacement": {
			featureGateMap: map[string]bool{
				string(features.TopologyAwareScheduling):          false,
				string(features.TASProfileMixed):                  false,
				string(features.TASHandleOverlappingFlavors):      false,
				string(features.TASFailedNodeReplacement):         false,
				string(features.TASFailedNodeReplacementFailFast): true,
				string(features.TASReplaceNodeOnPodTermination):   false,
				string(features.TASReplaceNodeOnNodeTaints):       false,
				string(features.TASMultiLayerTopology):            false,
			},
			gatesToRestore: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling:          true,
				features.TASProfileMixed:                  true,
				features.TASHandleOverlappingFlavors:      true,
				features.TASFailedNodeReplacement:         true,
				features.TASFailedNodeReplacementFailFast: true,
				features.TASReplaceNodeOnPodTermination:   true,
				features.TASReplaceNodeOnNodeTaints:       true,
				features.TASMultiLayerTopology:            true,
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:   field.ErrorTypeInvalid,
					Field:  "featureGates",
					Detail: "TASFailedNodeReplacementFailFast requires TopologyAwareScheduling to be enabled",
				},
				&field.Error{
					Type:   field.ErrorTypeInvalid,
					Field:  "featureGates",
					Detail: "TASFailedNodeReplacementFailFast requires TASFailedNodeReplacement to be enabled",
				},
			},
		},
		"TASReplaceNodeOnPodTermination requires both TopologyAwareScheduling and TASFailedNodeReplacement": {
			featureGateMap: map[string]bool{
				string(features.TopologyAwareScheduling):          false,
				string(features.TASProfileMixed):                  false,
				string(features.TASHandleOverlappingFlavors):      false,
				string(features.TASFailedNodeReplacement):         false,
				string(features.TASFailedNodeReplacementFailFast): false,
				string(features.TASReplaceNodeOnPodTermination):   true,
				string(features.TASReplaceNodeOnNodeTaints):       false,
				string(features.TASMultiLayerTopology):            false,
			},
			gatesToRestore: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling:          true,
				features.TASProfileMixed:                  true,
				features.TASHandleOverlappingFlavors:      true,
				features.TASFailedNodeReplacement:         true,
				features.TASFailedNodeReplacementFailFast: true,
				features.TASReplaceNodeOnPodTermination:   true,
				features.TASReplaceNodeOnNodeTaints:       true,
				features.TASMultiLayerTopology:            true,
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:   field.ErrorTypeInvalid,
					Field:  "featureGates",
					Detail: "TASReplaceNodeOnPodTermination requires TopologyAwareScheduling to be enabled",
				},
				&field.Error{
					Type:   field.ErrorTypeInvalid,
					Field:  "featureGates",
					Detail: "TASReplaceNodeOnPodTermination requires TASFailedNodeReplacement to be enabled",
				},
			},
		},
		"all TAS sub-features valid when TopologyAwareScheduling and TASFailedNodeReplacement enabled": {
			featureGateMap: map[string]bool{
				string(features.TopologyAwareScheduling):          true,
				string(features.TASFailedNodeReplacement):         true,
				string(features.TASFailedNodeReplacementFailFast): true,
				string(features.TASReplaceNodeOnPodTermination):   true,
				string(features.TASBalancedPlacement):             true,
				string(features.TASReplaceNodeOnNodeTaints):       true,
				string(features.TASMultiLayerTopology):            true,
				string(features.TASRespectNodeAffinityPreferred):  true,
				string(features.TASHandleOverlappingFlavors):      true,
			},
			gatesToRestore: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling:          true,
				features.TASFailedNodeReplacement:         true,
				features.TASFailedNodeReplacementFailFast: true,
				features.TASReplaceNodeOnPodTermination:   true,
				features.TASBalancedPlacement:             false,
				features.TASReplaceNodeOnNodeTaints:       true,
				features.TASMultiLayerTopology:            false,
				features.TASRespectNodeAffinityPreferred:  false,
				features.TASHandleOverlappingFlavors:      true,
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// Ensure clean up is registered for the feature gates to their default values
			features.SetFeatureGatesDuringTest(t, tc.gatesToRestore)
			got := LoadAndValidateFeatureGates(tc.featureGatesCLI, tc.featureGateMap)
			if diff := cmp.Diff(tc.wantErr, got, cmpopts.IgnoreFields(field.Error{}, "BadValue")); diff != "" {
				t.Errorf("Unexpected result from LoadAndValidateFeatureGates (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestValidateDeviceClassMappings(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.KueueDRAIntegrationConsumableCapacity, true)

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
		"valid capacity unqualified name": {
			cfg: &configapi.Configuration{
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name:             "gpu.memory",
							DeviceClassNames: []corev1.ResourceName{"vgpu.example.com"},
							Sources: []configapi.DeviceClassSourceConfig{
								{Capacity: &configapi.DeviceClassCapacitySource{
									Name:   "memory",
									Driver: "gpu.example.com",
									DeviceSelector: resourcev1.DeviceSelector{
										CEL: &resourcev1.CELDeviceSelector{Expression: "device.driver == 'gpu.example.com'"},
									},
								}},
							},
						},
					},
				},
			},
		},
		"valid capacity qualified name": {
			cfg: &configapi.Configuration{
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name:             "gpu.memory",
							DeviceClassNames: []corev1.ResourceName{"vgpu.example.com"},
							Sources: []configapi.DeviceClassSourceConfig{
								{Capacity: &configapi.DeviceClassCapacitySource{
									Name:   "gpu.example.com/memory",
									Driver: "gpu.example.com",
									DeviceSelector: resourcev1.DeviceSelector{
										CEL: &resourcev1.CELDeviceSelector{Expression: "device.driver == 'gpu.example.com'"},
									},
								}},
							},
						},
					},
				},
			},
		},
		"valid capacity C identifier": {
			cfg: &configapi.Configuration{
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name:             "gpu.memory",
							DeviceClassNames: []corev1.ResourceName{"vgpu.example.com"},
							Sources: []configapi.DeviceClassSourceConfig{
								{Capacity: &configapi.DeviceClassCapacitySource{
									Name:   "gpu.example.com/Memory_Bytes",
									Driver: "gpu.example.com",
									DeviceSelector: resourcev1.DeviceSelector{
										CEL: &resourcev1.CELDeviceSelector{Expression: "device.driver == 'gpu.example.com'"},
									},
								}},
							},
						},
					},
				},
			},
		},
		"valid capacity name with uppercase domain": {
			cfg: &configapi.Configuration{
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name:             "gpu.memory",
							DeviceClassNames: []corev1.ResourceName{"vgpu.example.com"},
							Sources: []configapi.DeviceClassSourceConfig{
								{Capacity: &configapi.DeviceClassCapacitySource{
									Name:   "GPU.example.com/memory",
									Driver: "gpu.example.com",
									DeviceSelector: resourcev1.DeviceSelector{
										CEL: &resourcev1.CELDeviceSelector{Expression: "device.driver == 'gpu.example.com'"},
									},
								}},
							},
						},
					},
				},
			},
		},
		"capacity name with empty domain": {
			cfg: &configapi.Configuration{
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name:             "gpu.memory",
							DeviceClassNames: []corev1.ResourceName{"vgpu.example.com"},
							Sources: []configapi.DeviceClassSourceConfig{
								{Capacity: &configapi.DeviceClassCapacitySource{
									Name:   "/memory",
									Driver: "gpu.example.com",
									DeviceSelector: resourcev1.DeviceSelector{
										CEL: &resourcev1.CELDeviceSelector{Expression: "device.driver == 'gpu.example.com'"},
									},
								}},
							},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "resources.deviceClassMappings[0].sources[0].capacity.name",
				},
			},
		},
		"capacity name with invalid C identifier": {
			cfg: &configapi.Configuration{
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name:             "gpu.memory",
							DeviceClassNames: []corev1.ResourceName{"vgpu.example.com"},
							Sources: []configapi.DeviceClassSourceConfig{
								{Capacity: &configapi.DeviceClassCapacitySource{
									Name:   "gpu.example.com/memory-bytes",
									Driver: "gpu.example.com",
									DeviceSelector: resourcev1.DeviceSelector{
										CEL: &resourcev1.CELDeviceSelector{Expression: "device.driver == 'gpu.example.com'"},
									},
								}},
							},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "resources.deviceClassMappings[0].sources[0].capacity.name",
				},
			},
		},
		"capacity name domain exceeds max length": {
			cfg: &configapi.Configuration{
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name:             "gpu.memory",
							DeviceClassNames: []corev1.ResourceName{"vgpu.example.com"},
							Sources: []configapi.DeviceClassSourceConfig{
								{Capacity: &configapi.DeviceClassCapacitySource{
									Name:   resourcev1.QualifiedName(strings.Repeat("a", resourcev1.DeviceMaxDomainLength+1) + "/memory"),
									Driver: "gpu.example.com",
									DeviceSelector: resourcev1.DeviceSelector{
										CEL: &resourcev1.CELDeviceSelector{Expression: "device.driver == 'gpu.example.com'"},
									},
								}},
							},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeTooLong,
					Field: "resources.deviceClassMappings[0].sources[0].capacity.name",
				},
			},
		},
		"capacity name identifier exceeds max length": {
			cfg: &configapi.Configuration{
				Resources: &configapi.Resources{
					DeviceClassMappings: []configapi.DeviceClassMapping{
						{
							Name:             "gpu.memory",
							DeviceClassNames: []corev1.ResourceName{"vgpu.example.com"},
							Sources: []configapi.DeviceClassSourceConfig{
								{Capacity: &configapi.DeviceClassCapacitySource{
									Name:   resourcev1.QualifiedName("gpu.example.com/" + strings.Repeat("a", resourcev1.DeviceMaxIDLength+1)),
									Driver: "gpu.example.com",
									DeviceSelector: resourcev1.DeviceSelector{
										CEL: &resourcev1.CELDeviceSelector{Expression: "device.driver == 'gpu.example.com'"},
									},
								}},
							},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeTooLong,
					Field: "resources.deviceClassMappings[0].sources[0].capacity.name",
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

func TestValidateCustomLabels(t *testing.T) {
	testCases := map[string]struct {
		cfg     *configapi.Configuration
		wantErr field.ErrorList
	}{
		"empty custom labels": {
			cfg: &configapi.Configuration{},
		},
		"valid name only": {
			cfg: &configapi.Configuration{
				ControllerManager: configapi.ControllerManager{
					Metrics: configapi.ControllerMetrics{
						CustomLabels: []configapi.ControllerMetricsCustomLabel{
							{Name: "team"},
						},
					},
				},
			},
		},
		"name with underscore valid as k8s label key": {
			cfg: &configapi.Configuration{
				ControllerManager: configapi.ControllerManager{
					Metrics: configapi.ControllerMetrics{
						CustomLabels: []configapi.ControllerMetricsCustomLabel{
							{Name: "has_underscore"},
						},
					},
				},
			},
		},
		"valid multiple entries": {
			cfg: &configapi.Configuration{
				ControllerManager: configapi.ControllerManager{
					Metrics: configapi.ControllerMetrics{
						CustomLabels: []configapi.ControllerMetricsCustomLabel{
							{Name: "team"},
							{Name: "env", SourceLabelKey: "environment"},
							{Name: "cost", SourceAnnotationKey: "billing/cost"},
						},
					},
				},
			},
		},
		"valid with sourceLabelKey": {
			cfg: &configapi.Configuration{
				ControllerManager: configapi.ControllerManager{
					Metrics: configapi.ControllerMetrics{
						CustomLabels: []configapi.ControllerMetricsCustomLabel{
							{Name: "team", SourceLabelKey: "org.example.com/team"},
						},
					},
				},
			},
		},
		"valid with sourceAnnotationKey": {
			cfg: &configapi.Configuration{
				ControllerManager: configapi.ControllerManager{
					Metrics: configapi.ControllerMetrics{
						CustomLabels: []configapi.ControllerMetricsCustomLabel{
							{Name: "cost_center", SourceAnnotationKey: "billing.example.com/cost-center"},
						},
					},
				},
			},
		},
		"valid workload with tracked values": {
			cfg: &configapi.Configuration{
				ControllerManager: configapi.ControllerManager{
					Metrics: configapi.ControllerMetrics{
						CustomLabels: []configapi.ControllerMetricsCustomLabel{
							{
								Name:          "team",
								SourceKind:    ptr.To(configapi.SourceKindWorkload),
								TrackedValues: []string{"a"},
							},
						},
					},
				},
			},
		},
		"valid cohort with tracked values": {
			cfg: &configapi.Configuration{
				ControllerManager: configapi.ControllerManager{
					Metrics: configapi.ControllerMetrics{
						CustomLabels: []configapi.ControllerMetricsCustomLabel{
							{
								Name:          "team",
								SourceKind:    ptr.To(configapi.SourceKindCohort),
								TrackedValues: []string{"a"},
							},
						},
					},
				},
			},
		},
		"invalid name - special chars": {
			cfg: &configapi.Configuration{
				ControllerManager: configapi.ControllerManager{
					Metrics: configapi.ControllerMetrics{
						CustomLabels: []configapi.ControllerMetricsCustomLabel{
							{Name: "team-name"},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:   field.ErrorTypeInvalid,
					Field:  "metrics.customLabels[0].name",
					Detail: "must match ^[a-zA-Z][a-zA-Z0-9_]*$",
				},
			},
		},
		"invalid name - leading digit": {
			cfg: &configapi.Configuration{
				ControllerManager: configapi.ControllerManager{
					Metrics: configapi.ControllerMetrics{
						CustomLabels: []configapi.ControllerMetricsCustomLabel{
							{Name: "1team"},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:   field.ErrorTypeInvalid,
					Field:  "metrics.customLabels[0].name",
					Detail: "must match ^[a-zA-Z][a-zA-Z0-9_]*$",
				},
			},
		},
		"invalid name - empty": {
			cfg: &configapi.Configuration{
				ControllerManager: configapi.ControllerManager{
					Metrics: configapi.ControllerMetrics{
						CustomLabels: []configapi.ControllerMetricsCustomLabel{
							{Name: ""},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:   field.ErrorTypeInvalid,
					Field:  "metrics.customLabels[0].name",
					Detail: "must match ^[a-zA-Z][a-zA-Z0-9_]*$",
				},
				&field.Error{
					Type:   field.ErrorTypeInvalid,
					Field:  "metrics.customLabels[0].name",
					Detail: "name part must be non-empty; name part must consist of alphanumeric characters, '-', '_' or '.', and must start and end with an alphanumeric character (e.g. 'MyName',  or 'my.name',  or '123-abc', regex used for validation is '([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9]')",
				},
			},
		},
		"duplicate names": {
			cfg: &configapi.Configuration{
				ControllerManager: configapi.ControllerManager{
					Metrics: configapi.ControllerMetrics{
						CustomLabels: []configapi.ControllerMetricsCustomLabel{
							{Name: "team"},
							{Name: "team"},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeDuplicate,
					Field: "metrics.customLabels[1].name",
				},
			},
		},
		"mutually exclusive sources": {
			cfg: &configapi.Configuration{
				ControllerManager: configapi.ControllerManager{
					Metrics: configapi.ControllerMetrics{
						CustomLabels: []configapi.ControllerMetricsCustomLabel{
							{Name: "team", SourceLabelKey: "team-label", SourceAnnotationKey: "team-annotation"},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:   field.ErrorTypeInvalid,
					Field:  "metrics.customLabels[0]",
					Detail: "sourceLabelKey and sourceAnnotationKey are mutually exclusive",
				},
			},
		},
		"invalid sourceLabelKey": {
			cfg: &configapi.Configuration{
				ControllerManager: configapi.ControllerManager{
					Metrics: configapi.ControllerMetrics{
						CustomLabels: []configapi.ControllerMetricsCustomLabel{
							{Name: "team", SourceLabelKey: "invalid key with spaces"},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:   field.ErrorTypeInvalid,
					Field:  "metrics.customLabels[0].sourceLabelKey",
					Detail: "name part must consist of alphanumeric characters, '-', '_' or '.', and must start and end with an alphanumeric character (e.g. 'MyName',  or 'my.name',  or '123-abc', regex used for validation is '([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9]')",
				},
			},
		},
		"invalid sourceAnnotationKey": {
			cfg: &configapi.Configuration{
				ControllerManager: configapi.ControllerManager{
					Metrics: configapi.ControllerMetrics{
						CustomLabels: []configapi.ControllerMetricsCustomLabel{
							{Name: "team", SourceAnnotationKey: "invalid key with spaces"},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:   field.ErrorTypeInvalid,
					Field:  "metrics.customLabels[0].sourceAnnotationKey",
					Detail: "name part must consist of alphanumeric characters, '-', '_' or '.', and must start and end with an alphanumeric character (e.g. 'MyName',  or 'my.name',  or '123-abc', regex used for validation is '([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9]')",
				},
			},
		},
		"unknown source kind": {
			cfg: &configapi.Configuration{
				ControllerManager: configapi.ControllerManager{
					Metrics: configapi.ControllerMetrics{
						CustomLabels: []configapi.ControllerMetricsCustomLabel{
							{
								Name:       "team",
								SourceKind: ptr.To(configapi.SourceKind("Unknown")),
							},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:   field.ErrorTypeInvalid,
					Field:  "metrics.customLabels",
					Detail: "unknown source kind: Unknown",
				},
			},
		},
		"too many custom labels in total": {
			cfg: &configapi.Configuration{
				ControllerManager: configapi.ControllerManager{
					Metrics: configapi.ControllerMetrics{
						CustomLabels: []configapi.ControllerMetricsCustomLabel{
							{Name: "c1"}, {Name: "c2"}, {Name: "c3"}, {Name: "c4"}, {Name: "c5"},
							{Name: "c6"}, {Name: "c7"}, {Name: "c8"}, {Name: "c9"}, {Name: "c10"},
							{Name: "c11"}, {Name: "c12"}, {Name: "c13"}, {Name: "c14"}, {Name: "c15"},
							{Name: "c16"}, {Name: "c17"}, {Name: "c18"}, {Name: "c19"}, {Name: "c20"},
							{Name: "c21"},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:   field.ErrorTypeTooMany,
					Field:  "metrics.customLabels",
					Detail: "must have at most 20 items",
				},
				&field.Error{
					Type:   field.ErrorTypeInvalid,
					Field:  "metrics.customLabels",
					Detail: "too many custom labels for source kind ClusterQueue: found 21, expected <= 6",
				},
			},
		},
		"too many custom labels": {
			cfg: &configapi.Configuration{
				ControllerManager: configapi.ControllerManager{
					Metrics: configapi.ControllerMetrics{
						CustomLabels: []configapi.ControllerMetricsCustomLabel{
							{Name: "c1", SourceKind: ptr.To(configapi.SourceKindCohort)},
							{Name: "c2", SourceKind: ptr.To(configapi.SourceKindCohort)},
							{Name: "c3", SourceKind: ptr.To(configapi.SourceKindCohort)},
							{Name: "c4", SourceKind: ptr.To(configapi.SourceKindCohort)},
							{Name: "c5", SourceKind: ptr.To(configapi.SourceKindCohort)},
							{Name: "c6", SourceKind: ptr.To(configapi.SourceKindCohort)},
							{Name: "c7", SourceKind: ptr.To(configapi.SourceKindCohort)},
							{Name: "c8", SourceKind: ptr.To(configapi.SourceKindCohort)},
							{Name: "c9", SourceKind: ptr.To(configapi.SourceKindCohort)},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:   field.ErrorTypeInvalid,
					Field:  "metrics.customLabels",
					Detail: "too many custom labels for source kind Cohort: found 9, expected <= 6",
				},
			},
		},
		"workload without tracked values": {
			cfg: &configapi.Configuration{
				ControllerManager: configapi.ControllerManager{
					Metrics: configapi.ControllerMetrics{
						CustomLabels: []configapi.ControllerMetricsCustomLabel{
							{
								Name:       "team",
								SourceKind: ptr.To(configapi.SourceKindWorkload),
							},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:   field.ErrorTypeInvalid,
					Field:  "metrics.customLabels[0].trackedValues",
					Detail: "must not be empty when sourceKind is 'Workload'",
				},
			},
		},
		"too many tracked values": {
			cfg: &configapi.Configuration{
				ControllerManager: configapi.ControllerManager{
					Metrics: configapi.ControllerMetrics{
						CustomLabels: []configapi.ControllerMetricsCustomLabel{
							{
								Name:          "team",
								SourceKind:    ptr.To(configapi.SourceKindCohort),
								TrackedValues: []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12", "13", "14", "15", "16", "17"},
							},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:   field.ErrorTypeTooMany,
					Field:  "metrics.customLabels[0].trackedValues",
					Detail: "must have at most 16 items",
				},
			},
		},
		"too many tracked values for workload": {
			cfg: &configapi.Configuration{
				ControllerManager: configapi.ControllerManager{
					Metrics: configapi.ControllerMetrics{
						CustomLabels: []configapi.ControllerMetricsCustomLabel{
							{
								Name:          "team",
								SourceKind:    ptr.To(configapi.SourceKindWorkload),
								TrackedValues: []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12", "13"},
							},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:   field.ErrorTypeInvalid,
					Field:  "metrics.customLabels[0].trackedValues",
					Detail: "must not be greater than 12 when sourceKind is 'Workload'",
				},
			},
		},
		"duplicate tracked values": {
			cfg: &configapi.Configuration{
				ControllerManager: configapi.ControllerManager{
					Metrics: configapi.ControllerMetrics{
						CustomLabels: []configapi.ControllerMetricsCustomLabel{
							{
								Name:          "team",
								SourceKind:    ptr.To(configapi.SourceKindCohort),
								TrackedValues: []string{"a", "b", "a"},
							},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:   field.ErrorTypeInvalid,
					Field:  "metrics.customLabels[0].trackedValues",
					Detail: "must not contain duplicates",
				},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			got := validateCustomLabels(tc.cfg)
			if diff := cmp.Diff(tc.wantErr, got, cmpopts.IgnoreFields(field.Error{}, "BadValue")); diff != "" {
				t.Errorf("validateCustomLabels() returned unexpected error (-want,+got):\n%s", diff)
			}
		})
	}

	t.Run("too many custom labels message detail", func(t *testing.T) {
		cfg := &configapi.Configuration{
			ControllerManager: configapi.ControllerManager{
				Metrics: configapi.ControllerMetrics{
					CustomLabels: []configapi.ControllerMetricsCustomLabel{
						{Name: "c1", SourceKind: ptr.To(configapi.SourceKindCohort)},
						{Name: "c2", SourceKind: ptr.To(configapi.SourceKindCohort)},
						{Name: "c3", SourceKind: ptr.To(configapi.SourceKindCohort)},
						{Name: "c4", SourceKind: ptr.To(configapi.SourceKindCohort)},
						{Name: "c5", SourceKind: ptr.To(configapi.SourceKindCohort)},
						{Name: "c6", SourceKind: ptr.To(configapi.SourceKindCohort)},
						{Name: "c7", SourceKind: ptr.To(configapi.SourceKindCohort)},
						{Name: "c8", SourceKind: ptr.To(configapi.SourceKindCohort)},
						{Name: "c9", SourceKind: ptr.To(configapi.SourceKindCohort)},
						{Name: "l1", SourceKind: ptr.To(configapi.SourceKindLocalQueue)},
						{Name: "l2", SourceKind: ptr.To(configapi.SourceKindLocalQueue)},
						{Name: "l3", SourceKind: ptr.To(configapi.SourceKindLocalQueue)},
						{Name: "l4", SourceKind: ptr.To(configapi.SourceKindLocalQueue)},
						{Name: "l5", SourceKind: ptr.To(configapi.SourceKindLocalQueue)},
						{Name: "l6", SourceKind: ptr.To(configapi.SourceKindLocalQueue)},
						{Name: "l7", SourceKind: ptr.To(configapi.SourceKindLocalQueue)},
						{Name: "l8", SourceKind: ptr.To(configapi.SourceKindLocalQueue)},
						{Name: "l9", SourceKind: ptr.To(configapi.SourceKindLocalQueue)},
						{Name: "l10", SourceKind: ptr.To(configapi.SourceKindLocalQueue)},
						{Name: "w1", SourceKind: ptr.To(configapi.SourceKindWorkload), TrackedValues: []string{"a"}},
						{Name: "w2", SourceKind: ptr.To(configapi.SourceKindWorkload), TrackedValues: []string{"a"}},
						{Name: "w3", SourceKind: ptr.To(configapi.SourceKindWorkload), TrackedValues: []string{"a"}},
						{Name: "w4", SourceKind: ptr.To(configapi.SourceKindWorkload), TrackedValues: []string{"a"}},
						{Name: "w5", SourceKind: ptr.To(configapi.SourceKindWorkload), TrackedValues: []string{"a"}},
					},
				},
			},
		}
		wantDetails := []string{
			"too many custom labels for source kind Cohort: found 9, expected <= 6",
			"too many custom labels for source kind LocalQueue: found 10, expected <= 6",
			"too many custom labels for source kind Workload: found 5, expected <= 2",
		}

		got := validateCustomLabels(cfg)

		var gotDetails []string
		for _, err := range got {
			if err.Type != field.ErrorTypeTooMany {
				gotDetails = append(gotDetails, err.Detail)
			}
		}

		if len(gotDetails) != 3 {
			t.Fatalf("expected 3 errors, got %d", len(gotDetails))
		}

		slices.Sort(gotDetails)
		if diff := cmp.Diff(wantDetails, gotDetails); diff != "" {
			t.Errorf("unexpected error details (-want,+got):\n%s", diff)
		}
	})
}

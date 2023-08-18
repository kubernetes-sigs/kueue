package config

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
)

func TestValidateQueueVisibility(t *testing.T) {
	testcases := []struct {
		name    string
		cfg     configapi.Configuration
		wantErr field.ErrorList
	}{

		{
			name:    "empty",
			cfg:     configapi.Configuration{},
			wantErr: nil,
		},
		{
			name: "invalid queue visibility UpdateIntervalSeconds",
			cfg: configapi.Configuration{
				QueueVisibility: &configapi.QueueVisibility{
					UpdateIntervalSeconds: 0,
				},
			},
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("queueVisibility").Child("updateIntervalSeconds"), 0, fmt.Sprintf("greater than or equal to %d", queueVisibilityClusterQueuesUpdateIntervalSeconds)),
			},
		},
		{
			name: "invalid queue visibility cluster queue max count",
			cfg: configapi.Configuration{
				QueueVisibility: &configapi.QueueVisibility{
					ClusterQueues: &configapi.ClusterQueueVisibility{
						MaxCount: 4001,
					},
					UpdateIntervalSeconds: 1,
				},
			},
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("queueVisibility").Child("clusterQueues").Child("maxCount"), 4001, fmt.Sprintf("must be less than %d", queueVisibilityClusterQueuesMaxValue)),
			},
		},
	}
	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.wantErr, validateQueueVisibility(tc.cfg), cmpopts.IgnoreFields(field.Error{}, "BadValue")); diff != "" {
				t.Errorf("Unexpected returned error (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestValidateNamespaceSelector(t *testing.T) {
	testCases := map[string]struct {
		podIntegrationOptions *configapi.PodIntegrationOptions
		wantError             field.ErrorList
	}{
		"nil PodIntegrationOptions": {
			podIntegrationOptions: nil,
			wantError: field.ErrorList{
				field.Required(podOptionsPath, errMsgPodOptionsIsNil),
			},
		},

		"nil PodIntegrationOptions.PodNamespaceSelector": {
			podIntegrationOptions: &configapi.PodIntegrationOptions{
				NamespaceSelector: nil,
			},
			wantError: field.ErrorList{
				field.Required(namespaceSelectorPath, errMsgNamespaceSelectorIsNil),
			},
		},
		"emptyLabelSelector": {
			podIntegrationOptions: &configapi.PodIntegrationOptions{
				NamespaceSelector: &metav1.LabelSelector{},
			},
			wantError: field.ErrorList{
				field.Invalid(namespaceSelectorPath, labels.Set{corev1.LabelMetadataName: "kube-system"}, errMsgProhibitedNamespace),
				field.Invalid(namespaceSelectorPath, labels.Set{corev1.LabelMetadataName: "kueue-system"}, errMsgProhibitedNamespace),
			},
		},
		"prohibited namespace in MatchLabels": {
			podIntegrationOptions: &configapi.PodIntegrationOptions{
				NamespaceSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"kubernetes.io/metadata.name": "kube-system",
					},
				},
			},
			wantError: field.ErrorList{
				field.Invalid(namespaceSelectorPath, labels.Set{corev1.LabelMetadataName: "kube-system"}, errMsgProhibitedNamespace),
			},
		},
		"prohibited namespace in MatchExpressions with operator In": {
			podIntegrationOptions: &configapi.PodIntegrationOptions{
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
			wantError: field.ErrorList{
				field.Invalid(namespaceSelectorPath, labels.Set{corev1.LabelMetadataName: "kube-system"}, errMsgProhibitedNamespace),
			},
		},
		"prohibited namespace in MatchExpressions with operator NotIn": {
			podIntegrationOptions: &configapi.PodIntegrationOptions{
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
			wantError: nil,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			cfg := configapi.Configuration{
				Namespace: ptr.To("kueue-system"),
				Integrations: &configapi.Integrations{
					PodOptions: tc.podIntegrationOptions,
				},
			}
			gotError := validateNamespaceSelector(cfg)
			if diff := cmp.Diff(tc.wantError, gotError); diff != "" {
				t.Errorf("unexpected non-nil error (-want,+got):\n%s", diff)
			}
		})
	}
}

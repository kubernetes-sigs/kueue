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

package webhooks

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"

	kueuev1alpha1 "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/features"
)

func makeConfig(resourceName, dcName corev1.ResourceName) *kueuev1alpha1.DynamicResourceAllocationConfig {
	return &kueuev1alpha1.DynamicResourceAllocationConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "default",
			Namespace: "kueue-system",
		},
		Spec: kueuev1alpha1.DynamicResourceAllocationConfigSpec{
			Resources: []kueuev1alpha1.DynamicResource{
				{
					Name:             resourceName,
					DeviceClassNames: []corev1.ResourceName{dcName},
				},
			},
		},
	}
}

func TestValidateDynamicResourceAllocationConfig(t *testing.T) {
	testCases := []struct {
		name     string
		cfg      *kueuev1alpha1.DynamicResourceAllocationConfig
		wantErrs field.ErrorList
	}{
		{
			name: "valid config",
			cfg:  makeConfig(corev1.ResourceName("example.com/gpu"), corev1.ResourceName("example.com/device-class")),
		},
		{
			name: "invalid resource name",
			cfg:  makeConfig(corev1.ResourceName("@invalid"), corev1.ResourceName("example.com/device-class")),
			wantErrs: field.ErrorList{
				field.Invalid(field.NewPath("spec", "resources").Index(0).Child("name"), corev1.ResourceName("@invalid"), "a lowercase RFC 1123 subdomain must consist of lower case alphanumeric characters, '-' or '.', and must start and end with an alphanumeric character (e.g. 'example.com', regex used for validation is '[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*')"),
			},
		},
		{
			name: "invalid device class name",
			cfg:  makeConfig(corev1.ResourceName("example.com/gpu"), corev1.ResourceName("@invalid")),
			wantErrs: field.ErrorList{
				field.Invalid(field.NewPath("spec", "resources").Index(0).Child("deviceClassNames").Index(0), corev1.ResourceName("@invalid"), "a lowercase RFC 1123 subdomain must consist of lower case alphanumeric characters, '-' or '.', and must start and end with an alphanumeric character (e.g. 'example.com', regex used for validation is '[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*')"),
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			gotErrs := validateDynamicResourceAllocationConfig(tc.cfg)
			if diff := cmp.Diff(tc.wantErrs, gotErrs, cmpopts.IgnoreFields(field.Error{}, "Detail", "BadValue")); diff != "" {
				t.Errorf("validateDynamicResourceAllocationConfig() error mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestDynamicResourceAllocationConfigFeatureGate(t *testing.T) {
	testCases := []struct {
		name           string
		featureEnabled bool
		wantErr        bool // true when creation should be rejected
	}{
		{name: "gate disabled", featureEnabled: false, wantErr: true},
		{name: "gate enabled", featureEnabled: true, wantErr: false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.DynamicResourceAllocation, tc.featureEnabled)

			cfg := makeConfig(corev1.ResourceName("example.com/gpu"), corev1.ResourceName("example.com/device-class")).DeepCopy()
			wh := &DynamicResourceAllocationConfigWebhook{namespace: "kueue-system"}
			_, err := wh.ValidateCreate(context.Background(), cfg)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateCreate() error expectation mismatch: wanted error=%v got err=%v", tc.wantErr, err)
			}
		})
	}
}

func TestDynamicResourceAllocationConfigNamespace(t *testing.T) {
	testCases := []struct {
		name              string
		configNamespace   string
		webhookNamespace  string
		wantErr           bool
		expectedErrDetail string
	}{
		{
			name:             "valid namespace - default",
			configNamespace:  "kueue-system",
			webhookNamespace: "kueue-system",
			wantErr:          false,
		},
		{
			name:             "valid namespace - custom",
			configNamespace:  "custom-namespace",
			webhookNamespace: "custom-namespace",
			wantErr:          false,
		},
		{
			name:              "invalid namespace",
			configNamespace:   "wrong-namespace",
			webhookNamespace:  "kueue-system",
			wantErr:           true,
			expectedErrDetail: "metadata.namespace: Invalid value: \"wrong-namespace\": must be 'kueue-system'",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.DynamicResourceAllocation, true)

			cfg := makeConfig(corev1.ResourceName("example.com/gpu"), corev1.ResourceName("example.com/device-class")).DeepCopy()
			cfg.Namespace = tc.configNamespace
			wh := &DynamicResourceAllocationConfigWebhook{namespace: tc.webhookNamespace}
			_, err := wh.ValidateCreate(context.Background(), cfg)

			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateCreate() error expectation mismatch: wanted error=%v got err=%v", tc.wantErr, err)
			}

			if tc.wantErr && err != nil {
				errStr := err.Error()
				if errStr != tc.expectedErrDetail {
					t.Errorf("Expected error detail %q, got %q", tc.expectedErrDetail, errStr)
				}
			}
		})
	}
}

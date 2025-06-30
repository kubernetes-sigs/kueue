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
	specPath := field.NewPath("spec")
	resourcesPath := specPath.Child("resources")

	testCases := map[string]struct {
		cfg     *kueuev1alpha1.DynamicResourceAllocationConfig
		wantErr field.ErrorList
	}{
		"valid": {
			cfg:     makeConfig(corev1.ResourceName("example.com/gpu"), corev1.ResourceName("example.com/device-class")),
			wantErr: nil,
		},
		"invalid resource name": {
			cfg: func() *kueuev1alpha1.DynamicResourceAllocationConfig {
				c := makeConfig(corev1.ResourceName("@invalid"), corev1.ResourceName("example.com/device-class"))
				return c
			}(),
			wantErr: field.ErrorList{
				field.Invalid(resourcesPath.Index(0).Child("name"), "@invalid", ""),
			},
		},
		"invalid device class name": {
			cfg: func() *kueuev1alpha1.DynamicResourceAllocationConfig {
				c := makeConfig(corev1.ResourceName("example.com/gpu"), corev1.ResourceName("@invalid"))
				return c
			}(),
			wantErr: field.ErrorList{
				field.Invalid(resourcesPath.Index(0).Child("deviceClassNames").Index(0), "@invalid", ""),
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			gotErr := validateDynamicResourceAllocationConfig(tc.cfg)
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.IgnoreFields(field.Error{}, "BadValue", "Detail")); diff != "" {
				t.Errorf("validateDynamicResourceAllocationConfig() mismatch (-want +got):\n%s", diff)
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
			wh := &DynamicResourceAllocationConfigWebhook{}
			_, err := wh.ValidateCreate(context.Background(), cfg)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateCreate() error expectation mismatch: wanted error=%v got err=%v", tc.wantErr, err)
			}
		})
	}
}

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

package v1beta1

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

func TestMultiKueueClusterConvertTo(t *testing.T) {
	defaultObjectMeta := metav1.ObjectMeta{
		Name: "test-multikueuecluster",
	}

	testCases := map[string]struct {
		input    *MultiKueueCluster
		expected *v1beta2.MultiKueueCluster
	}{
		"KubeConfig conversion": {
			input: &MultiKueueCluster{
				ObjectMeta: defaultObjectMeta,
				Spec: MultiKueueClusterSpec{
					KubeConfig: KubeConfig{
						Location:     "test-location",
						LocationType: SecretLocationType,
					},
				},
			},
			expected: &v1beta2.MultiKueueCluster{
				ObjectMeta: defaultObjectMeta,
				Spec: v1beta2.MultiKueueClusterSpec{
					ClusterSource: v1beta2.ClusterSource{
						KubeConfig: &v1beta2.KubeConfig{
							Location:     "test-location",
							LocationType: v1beta2.LocationType(SecretLocationType),
						},
					},
				},
			},
		},
		"empty KubeConfig": {
			input: &MultiKueueCluster{
				ObjectMeta: defaultObjectMeta,
				Spec:       MultiKueueClusterSpec{},
			},
			expected: &v1beta2.MultiKueueCluster{
				ObjectMeta: defaultObjectMeta,
				Spec:       v1beta2.MultiKueueClusterSpec{},
			},
		},
		"complete MultiKueueCluster with status": {
			input: &MultiKueueCluster{
				ObjectMeta: defaultObjectMeta,
				Spec: MultiKueueClusterSpec{
					KubeConfig: KubeConfig{
						Location:     "test-location",
						LocationType: PathLocationType,
					},
				},
				Status: MultiKueueClusterStatus{
					Conditions: []metav1.Condition{
						{
							Type:   "Active",
							Status: metav1.ConditionTrue,
							Reason: "Ready",
						},
					},
				},
			},
			expected: &v1beta2.MultiKueueCluster{
				ObjectMeta: defaultObjectMeta,
				Spec: v1beta2.MultiKueueClusterSpec{
					ClusterSource: v1beta2.ClusterSource{
						KubeConfig: &v1beta2.KubeConfig{
							Location:     "test-location",
							LocationType: v1beta2.LocationType(PathLocationType),
						},
					},
				},
				Status: v1beta2.MultiKueueClusterStatus{
					Conditions: []metav1.Condition{
						{
							Type:   "Active",
							Status: metav1.ConditionTrue,
							Reason: "Ready",
						},
					},
				},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			result := &v1beta2.MultiKueueCluster{}
			if err := tc.input.ConvertTo(result); err != nil {
				t.Fatalf("ConvertTo failed: %v", err)
			}
			if diff := cmp.Diff(tc.expected, result); diff != "" {
				t.Errorf("unexpected conversion result (-want +got):\n%s", diff)
			}
		})
	}
}

func TestMultiKueueClusterConvertFrom(t *testing.T) {
	defaultObjectMeta := metav1.ObjectMeta{
		Name: "test-multikueuecluster",
	}

	testCases := map[string]struct {
		input    *v1beta2.MultiKueueCluster
		expected *MultiKueueCluster
	}{
		"KubeConfig conversion": {
			input: &v1beta2.MultiKueueCluster{
				ObjectMeta: defaultObjectMeta,
				Spec: v1beta2.MultiKueueClusterSpec{
					ClusterSource: v1beta2.ClusterSource{
						KubeConfig: &v1beta2.KubeConfig{
							Location:     "test-location",
							LocationType: v1beta2.LocationType(SecretLocationType),
						},
					},
				},
			},
			expected: &MultiKueueCluster{
				ObjectMeta: defaultObjectMeta,
				Spec: MultiKueueClusterSpec{
					KubeConfig: KubeConfig{
						Location:     "test-location",
						LocationType: SecretLocationType,
					},
				},
			},
		},
		"nil KubeConfig": {
			input: &v1beta2.MultiKueueCluster{
				ObjectMeta: defaultObjectMeta,
				Spec: v1beta2.MultiKueueClusterSpec{
					ClusterSource: v1beta2.ClusterSource{
						KubeConfig: nil,
					},
				},
			},
			expected: &MultiKueueCluster{
				ObjectMeta: defaultObjectMeta,
				Spec:       MultiKueueClusterSpec{},
			},
		},
		"ClusterProfileRef conversion": {
			input: &v1beta2.MultiKueueCluster{
				ObjectMeta: defaultObjectMeta,
				Spec: v1beta2.MultiKueueClusterSpec{
					ClusterSource: v1beta2.ClusterSource{
						ClusterProfileRef: &v1beta2.ClusterProfileReference{
							Name: "foobar",
						},
					},
				},
			},
			expected: &MultiKueueCluster{
				ObjectMeta: defaultObjectMeta,
				Spec: MultiKueueClusterSpec{
					ClusterProfileRef: &ClusterProfileReference{
						Name: "foobar",
					},
				},
			},
		},
		"complete MultiKueueCluster with status": {
			input: &v1beta2.MultiKueueCluster{
				ObjectMeta: defaultObjectMeta,
				Spec: v1beta2.MultiKueueClusterSpec{
					ClusterSource: v1beta2.ClusterSource{
						KubeConfig: &v1beta2.KubeConfig{
							Location:     "test-location",
							LocationType: v1beta2.LocationType(PathLocationType),
						},
					},
				},
				Status: v1beta2.MultiKueueClusterStatus{
					Conditions: []metav1.Condition{
						{
							Type:   "Active",
							Status: metav1.ConditionTrue,
							Reason: "Ready",
						},
					},
				},
			},
			expected: &MultiKueueCluster{
				ObjectMeta: defaultObjectMeta,
				Spec: MultiKueueClusterSpec{
					KubeConfig: KubeConfig{
						Location:     "test-location",
						LocationType: PathLocationType,
					},
				},
				Status: MultiKueueClusterStatus{
					Conditions: []metav1.Condition{
						{
							Type:   "Active",
							Status: metav1.ConditionTrue,
							Reason: "Ready",
						},
					},
				},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			result := &MultiKueueCluster{}
			if err := result.ConvertFrom(tc.input); err != nil {
				t.Fatalf("ConvertFrom failed: %v", err)
			}
			if diff := cmp.Diff(tc.expected, result); diff != "" {
				t.Errorf("unexpected conversion result (-want +got):\n%s", diff)
			}
		})
	}
}

func TestMultiKueueClusterConversion_RoundTrip(t *testing.T) {
	testCases := map[string]struct {
		v1beta1Obj *MultiKueueCluster
	}{
		"complete MultiKueueCluster with KubeConfig and status": {
			v1beta1Obj: &MultiKueueCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-multikueuecluster",
				},
				Spec: MultiKueueClusterSpec{
					KubeConfig: KubeConfig{
						Location:     "test-location",
						LocationType: SecretLocationType,
					},
				},
				Status: MultiKueueClusterStatus{
					Conditions: []metav1.Condition{
						{
							Type:   "Active",
							Status: metav1.ConditionTrue,
							Reason: "Ready",
						},
					},
				},
			},
		},
		"minimal MultiKueueCluster": {
			v1beta1Obj: &MultiKueueCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: "minimal-mkc",
				},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			// Convert v1beta1 -> v1beta2
			v1beta2Obj := &v1beta2.MultiKueueCluster{}
			if err := tc.v1beta1Obj.ConvertTo(v1beta2Obj); err != nil {
				t.Fatalf("ConvertTo failed: %v", err)
			}

			// Convert v1beta2 -> v1beta1 (round-trip)
			roundTripped := &MultiKueueCluster{}
			if err := roundTripped.ConvertFrom(v1beta2Obj); err != nil {
				t.Fatalf("ConvertFrom failed: %v", err)
			}

			// Verify round-trip
			if diff := cmp.Diff(tc.v1beta1Obj, roundTripped); diff != "" {
				t.Errorf("round-trip conversion produced diff (-original +roundtripped):\n%s", diff)
			}
		})
	}
}

func TestMultiKueueClusterConversion_RoundTrip_Reverse(t *testing.T) {
	testCases := map[string]struct {
		v1beta2Obj *v1beta2.MultiKueueCluster
	}{
		"complete MultiKueueCluster with ClusterProfile and status": {
			v1beta2Obj: &v1beta2.MultiKueueCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-multikueuecluster",
				},
				Spec: v1beta2.MultiKueueClusterSpec{
					ClusterSource: v1beta2.ClusterSource{
						ClusterProfileRef: &v1beta2.ClusterProfileReference{
							Name: "foobar",
						},
					},
				},
				Status: v1beta2.MultiKueueClusterStatus{
					Conditions: []metav1.Condition{
						{
							Type:   "Active",
							Status: metav1.ConditionTrue,
							Reason: "Ready",
						},
					},
				},
			},
		},
		"complete MultiKueueCluster with KubeConfig and status": {
			v1beta2Obj: &v1beta2.MultiKueueCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-multikueuecluster",
				},
				Spec: v1beta2.MultiKueueClusterSpec{
					ClusterSource: v1beta2.ClusterSource{
						KubeConfig: &v1beta2.KubeConfig{
							Location:     "test-location",
							LocationType: v1beta2.LocationType(SecretLocationType),
						},
					},
				},
				Status: v1beta2.MultiKueueClusterStatus{
					Conditions: []metav1.Condition{
						{
							Type:   "Active",
							Status: metav1.ConditionTrue,
							Reason: "Ready",
						},
					},
				},
			},
		},
		"minimal MultiKueueCluster": {
			v1beta2Obj: &v1beta2.MultiKueueCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: "minimal-mkc",
				},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			// Convert v1beta2 -> v1beta1
			v1beta1Obj := &MultiKueueCluster{}
			if err := v1beta1Obj.ConvertFrom(tc.v1beta2Obj); err != nil {
				t.Fatalf("ConvertFrom failed: %v", err)
			}

			// Convert v1beta1 -> v1beta2 (round-trip)
			roundTripped := &v1beta2.MultiKueueCluster{}
			if err := v1beta1Obj.ConvertTo(roundTripped); err != nil {
				t.Fatalf("ConvertTo failed: %v", err)
			}

			// Verify round-trip
			if diff := cmp.Diff(tc.v1beta2Obj, roundTripped); diff != "" {
				t.Errorf("round-trip conversion produced diff (-original +roundtripped):\n%s", diff)
			}
		})
	}
}

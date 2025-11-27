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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

func TestLocalQueueConvertTo(t *testing.T) {
	defaultObjectMeta := metav1.ObjectMeta{
		Name:      "test-localqueue",
		Namespace: "default",
	}
	now := metav1.Now()

	testCases := map[string]struct {
		input    *LocalQueue
		expected *v1beta2.LocalQueue
	}{
		"nil FlavorUsage": {
			input: &LocalQueue{
				ObjectMeta: defaultObjectMeta,
				Status: LocalQueueStatus{
					FlavorUsage: nil,
				},
			},
			expected: &v1beta2.LocalQueue{
				ObjectMeta: defaultObjectMeta,
				Status: v1beta2.LocalQueueStatus{
					FlavorsUsage: nil,
				},
			},
		},
		"empty FlavorUsage": {
			input: &LocalQueue{
				ObjectMeta: defaultObjectMeta,
				Status: LocalQueueStatus{
					FlavorUsage: []LocalQueueFlavorUsage{},
				},
			},
			expected: &v1beta2.LocalQueue{
				ObjectMeta: defaultObjectMeta,
				Status: v1beta2.LocalQueueStatus{
					FlavorsUsage: []v1beta2.LocalQueueFlavorUsage{},
				},
			},
		},
		"single FlavorUsage": {
			input: &LocalQueue{
				ObjectMeta: defaultObjectMeta,
				Status: LocalQueueStatus{
					FlavorUsage: []LocalQueueFlavorUsage{
						{
							Name: "default-flavor",
							Resources: []LocalQueueResourceUsage{
								{
									Name:  corev1.ResourceCPU,
									Total: resource.MustParse("10"),
								},
							},
						},
					},
				},
			},
			expected: &v1beta2.LocalQueue{
				ObjectMeta: defaultObjectMeta,
				Status: v1beta2.LocalQueueStatus{
					FlavorsUsage: []v1beta2.LocalQueueFlavorUsage{
						{
							Name: "default-flavor",
							Resources: []v1beta2.LocalQueueResourceUsage{
								{
									Name:  corev1.ResourceCPU,
									Total: resource.MustParse("10"),
								},
							},
						},
					},
				},
			},
		},
		"multiple FlavorUsage with various resources": {
			input: &LocalQueue{
				ObjectMeta: defaultObjectMeta,
				Status: LocalQueueStatus{
					FlavorUsage: []LocalQueueFlavorUsage{
						{
							Name: "flavor-1",
							Resources: []LocalQueueResourceUsage{
								{
									Name:  corev1.ResourceCPU,
									Total: resource.MustParse("10"),
								},
								{
									Name:  corev1.ResourceMemory,
									Total: resource.MustParse("20Gi"),
								},
							},
						},
						{
							Name: "flavor-2",
							Resources: []LocalQueueResourceUsage{
								{
									Name:  corev1.ResourceCPU,
									Total: resource.MustParse("5"),
								},
							},
						},
					},
					PendingWorkloads:   3,
					ReservingWorkloads: 2,
					AdmittedWorkloads:  1,
				},
			},
			expected: &v1beta2.LocalQueue{
				ObjectMeta: defaultObjectMeta,
				Status: v1beta2.LocalQueueStatus{
					FlavorsUsage: []v1beta2.LocalQueueFlavorUsage{
						{
							Name: "flavor-1",
							Resources: []v1beta2.LocalQueueResourceUsage{
								{
									Name:  corev1.ResourceCPU,
									Total: resource.MustParse("10"),
								},
								{
									Name:  corev1.ResourceMemory,
									Total: resource.MustParse("20Gi"),
								},
							},
						},
						{
							Name: "flavor-2",
							Resources: []v1beta2.LocalQueueResourceUsage{
								{
									Name:  corev1.ResourceCPU,
									Total: resource.MustParse("5"),
								},
							},
						},
					},
					PendingWorkloads:   3,
					ReservingWorkloads: 2,
					AdmittedWorkloads:  1,
				},
			},
		},
		"with conditions and status fields": {
			input: &LocalQueue{
				ObjectMeta: defaultObjectMeta,
				Spec: LocalQueueSpec{
					ClusterQueue: "cluster-queue-1",
				},
				Status: LocalQueueStatus{
					FairSharing: &FairSharingStatus{
						WeightedShare: 100,
						AdmissionFairSharingStatus: &AdmissionFairSharingStatus{
							ConsumedResources: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("10"),
							},
							LastUpdate: now,
						},
					},
					Conditions: []metav1.Condition{
						{
							Type:   "Active",
							Status: metav1.ConditionTrue,
							Reason: "Ready",
						},
					},
					FlavorUsage: []LocalQueueFlavorUsage{
						{
							Name: "default-flavor",
							Resources: []LocalQueueResourceUsage{
								{
									Name:  corev1.ResourceCPU,
									Total: resource.MustParse("10"),
								},
							},
						},
					},
					PendingWorkloads: 5,
				},
			},
			expected: &v1beta2.LocalQueue{
				ObjectMeta: defaultObjectMeta,
				Spec: v1beta2.LocalQueueSpec{
					ClusterQueue: "cluster-queue-1",
				},
				Status: v1beta2.LocalQueueStatus{
					FairSharing: &v1beta2.LocalQueueFairSharingStatus{
						WeightedShare: 100,
						AdmissionFairSharingStatus: &v1beta2.LocalQueueAdmissionFairSharingStatus{
							ConsumedResources: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("10"),
							},
							LastUpdate: now,
						},
					},
					Conditions: []metav1.Condition{
						{
							Type:   "Active",
							Status: metav1.ConditionTrue,
							Reason: "Ready",
						},
					},
					FlavorsUsage: []v1beta2.LocalQueueFlavorUsage{
						{
							Name: "default-flavor",
							Resources: []v1beta2.LocalQueueResourceUsage{
								{
									Name:  corev1.ResourceCPU,
									Total: resource.MustParse("10"),
								},
							},
						},
					},
					PendingWorkloads: 5,
				},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			result := &v1beta2.LocalQueue{}
			if err := tc.input.ConvertTo(result); err != nil {
				t.Fatalf("ConvertTo failed: %v", err)
			}
			if diff := cmp.Diff(tc.expected, result); diff != "" {
				t.Errorf("unexpected conversion result (-want +got):\n%s", diff)
			}
		})
	}
}

func TestLocalQueueConvertFrom(t *testing.T) {
	defaultObjectMeta := metav1.ObjectMeta{
		Name:      "test-localqueue",
		Namespace: "default",
	}

	testCases := map[string]struct {
		input    *v1beta2.LocalQueue
		expected *LocalQueue
	}{
		"nil FlavorsUsage": {
			input: &v1beta2.LocalQueue{
				ObjectMeta: defaultObjectMeta,
				Status: v1beta2.LocalQueueStatus{
					FlavorsUsage: nil,
				},
			},
			expected: &LocalQueue{
				ObjectMeta: defaultObjectMeta,
				Status: LocalQueueStatus{
					FlavorUsage: nil,
				},
			},
		},
		"empty FlavorsUsage": {
			input: &v1beta2.LocalQueue{
				ObjectMeta: defaultObjectMeta,
				Status: v1beta2.LocalQueueStatus{
					FlavorsUsage: []v1beta2.LocalQueueFlavorUsage{},
				},
			},
			expected: &LocalQueue{
				ObjectMeta: defaultObjectMeta,
				Status: LocalQueueStatus{
					FlavorUsage: []LocalQueueFlavorUsage{},
				},
			},
		},
		"single FlavorsUsage": {
			input: &v1beta2.LocalQueue{
				ObjectMeta: defaultObjectMeta,
				Status: v1beta2.LocalQueueStatus{
					FlavorsUsage: []v1beta2.LocalQueueFlavorUsage{
						{
							Name: "default-flavor",
							Resources: []v1beta2.LocalQueueResourceUsage{
								{
									Name:  corev1.ResourceCPU,
									Total: resource.MustParse("10"),
								},
							},
						},
					},
				},
			},
			expected: &LocalQueue{
				ObjectMeta: defaultObjectMeta,
				Status: LocalQueueStatus{
					FlavorUsage: []LocalQueueFlavorUsage{
						{
							Name: "default-flavor",
							Resources: []LocalQueueResourceUsage{
								{
									Name:  corev1.ResourceCPU,
									Total: resource.MustParse("10"),
								},
							},
						},
					},
				},
			},
		},
		"multiple FlavorsUsage with various resources": {
			input: &v1beta2.LocalQueue{
				ObjectMeta: defaultObjectMeta,
				Status: v1beta2.LocalQueueStatus{
					FlavorsUsage: []v1beta2.LocalQueueFlavorUsage{
						{
							Name: "flavor-1",
							Resources: []v1beta2.LocalQueueResourceUsage{
								{
									Name:  corev1.ResourceCPU,
									Total: resource.MustParse("10"),
								},
								{
									Name:  corev1.ResourceMemory,
									Total: resource.MustParse("20Gi"),
								},
							},
						},
						{
							Name: "flavor-2",
							Resources: []v1beta2.LocalQueueResourceUsage{
								{
									Name:  corev1.ResourceCPU,
									Total: resource.MustParse("5"),
								},
							},
						},
					},
					PendingWorkloads:   3,
					ReservingWorkloads: 2,
					AdmittedWorkloads:  1,
				},
			},
			expected: &LocalQueue{
				ObjectMeta: defaultObjectMeta,
				Status: LocalQueueStatus{
					FlavorUsage: []LocalQueueFlavorUsage{
						{
							Name: "flavor-1",
							Resources: []LocalQueueResourceUsage{
								{
									Name:  corev1.ResourceCPU,
									Total: resource.MustParse("10"),
								},
								{
									Name:  corev1.ResourceMemory,
									Total: resource.MustParse("20Gi"),
								},
							},
						},
						{
							Name: "flavor-2",
							Resources: []LocalQueueResourceUsage{
								{
									Name:  corev1.ResourceCPU,
									Total: resource.MustParse("5"),
								},
							},
						},
					},
					PendingWorkloads:   3,
					ReservingWorkloads: 2,
					AdmittedWorkloads:  1,
				},
			},
		},
		"with conditions and status fields": {
			input: &v1beta2.LocalQueue{
				ObjectMeta: defaultObjectMeta,
				Spec: v1beta2.LocalQueueSpec{
					ClusterQueue: "cluster-queue-1",
				},
				Status: v1beta2.LocalQueueStatus{
					Conditions: []metav1.Condition{
						{
							Type:   "Active",
							Status: metav1.ConditionTrue,
							Reason: "Ready",
						},
					},
					FlavorsUsage: []v1beta2.LocalQueueFlavorUsage{
						{
							Name: "default-flavor",
							Resources: []v1beta2.LocalQueueResourceUsage{
								{
									Name:  corev1.ResourceCPU,
									Total: resource.MustParse("10"),
								},
							},
						},
					},
					PendingWorkloads: 5,
				},
			},
			expected: &LocalQueue{
				ObjectMeta: defaultObjectMeta,
				Spec: LocalQueueSpec{
					ClusterQueue: "cluster-queue-1",
				},
				Status: LocalQueueStatus{
					Conditions: []metav1.Condition{
						{
							Type:   "Active",
							Status: metav1.ConditionTrue,
							Reason: "Ready",
						},
					},
					FlavorUsage: []LocalQueueFlavorUsage{
						{
							Name: "default-flavor",
							Resources: []LocalQueueResourceUsage{
								{
									Name:  corev1.ResourceCPU,
									Total: resource.MustParse("10"),
								},
							},
						},
					},
					PendingWorkloads: 5,
				},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			result := &LocalQueue{}
			if err := result.ConvertFrom(tc.input); err != nil {
				t.Fatalf("ConvertFrom failed: %v", err)
			}
			if diff := cmp.Diff(tc.expected, result); diff != "" {
				t.Errorf("unexpected conversion result (-want +got):\n%s", diff)
			}
		})
	}
}

func TestLocalQueueConversion_RoundTrip(t *testing.T) {
	testCases := map[string]struct {
		v1beta1Obj *LocalQueue
	}{
		"complete LocalQueue with multiple flavors": {
			v1beta1Obj: &LocalQueue{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-localqueue",
					Namespace: "default",
				},
				Spec: LocalQueueSpec{
					ClusterQueue: "main-cluster-queue",
				},
				Status: LocalQueueStatus{
					Conditions: []metav1.Condition{
						{
							Type:   "Active",
							Status: metav1.ConditionTrue,
							Reason: "Ready",
						},
					},
					FlavorUsage: []LocalQueueFlavorUsage{
						{
							Name: "flavor-1",
							Resources: []LocalQueueResourceUsage{
								{
									Name:  corev1.ResourceCPU,
									Total: resource.MustParse("10"),
								},
							},
						},
					},
					PendingWorkloads:   5,
					ReservingWorkloads: 3,
					AdmittedWorkloads:  2,
				},
			},
		},
		"LocalQueue with nil FlavorUsage": {
			v1beta1Obj: &LocalQueue{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "simple-localqueue",
					Namespace: "test-ns",
				},
				Status: LocalQueueStatus{
					FlavorUsage: nil,
				},
			},
		},
		"LocalQueue with empty FlavorUsage": {
			v1beta1Obj: &LocalQueue{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "empty-flavor-localqueue",
					Namespace: "test-ns",
				},
				Status: LocalQueueStatus{
					FlavorUsage: []LocalQueueFlavorUsage{},
				},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			// Convert v1beta1 -> v1beta2
			v1beta2Obj := &v1beta2.LocalQueue{}
			if err := tc.v1beta1Obj.ConvertTo(v1beta2Obj); err != nil {
				t.Fatalf("ConvertTo failed: %v", err)
			}

			// Convert v1beta2 -> v1beta1 (round-trip)
			roundTripped := &LocalQueue{}
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

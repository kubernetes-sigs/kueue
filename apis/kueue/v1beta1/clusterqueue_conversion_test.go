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

func TestClusterQueueConvertTo(t *testing.T) {
	defaultObjectMeta := metav1.ObjectMeta{
		Name: "test-clusterqueue",
	}

	testCases := map[string]struct {
		input    *ClusterQueue
		expected *v1beta2.ClusterQueue
	}{
		"nil cohort": {
			input: &ClusterQueue{
				ObjectMeta: defaultObjectMeta,
				Spec: ClusterQueueSpec{
					Cohort: "",
				},
			},
			expected: &v1beta2.ClusterQueue{
				ObjectMeta: defaultObjectMeta,
				Spec: v1beta2.ClusterQueueSpec{
					CohortName: "",
				},
			},
		},
		"cohort to cohortName conversion": {
			input: &ClusterQueue{
				ObjectMeta: defaultObjectMeta,
				Spec: ClusterQueueSpec{
					Cohort: "test-cohort",
				},
			},
			expected: &v1beta2.ClusterQueue{
				ObjectMeta: defaultObjectMeta,
				Spec: v1beta2.ClusterQueueSpec{
					CohortName: "test-cohort",
				},
			},
		},
		"AdmissionChecks to AdmissionChecksStrategy conversion": {
			input: &ClusterQueue{
				ObjectMeta: defaultObjectMeta,
				Spec: ClusterQueueSpec{
					AdmissionChecks: []AdmissionCheckReference{"check1", "check2"},
				},
			},
			expected: &v1beta2.ClusterQueue{
				ObjectMeta: defaultObjectMeta,
				Spec: v1beta2.ClusterQueueSpec{
					AdmissionChecksStrategy: &v1beta2.AdmissionChecksStrategy{
						AdmissionChecks: []v1beta2.AdmissionCheckStrategyRule{
							{Name: "check1"},
							{Name: "check2"},
						},
					},
				},
			},
		},
		"AdmissionChecksStrategy preserved": {
			input: &ClusterQueue{
				ObjectMeta: defaultObjectMeta,
				Spec: ClusterQueueSpec{
					AdmissionChecksStrategy: &AdmissionChecksStrategy{
						AdmissionChecks: []AdmissionCheckStrategyRule{
							{Name: "strategy-check1"},
							{Name: "strategy-check2"},
						},
					},
				},
			},
			expected: &v1beta2.ClusterQueue{
				ObjectMeta: defaultObjectMeta,
				Spec: v1beta2.ClusterQueueSpec{
					AdmissionChecksStrategy: &v1beta2.AdmissionChecksStrategy{
						AdmissionChecks: []v1beta2.AdmissionCheckStrategyRule{
							{Name: "strategy-check1"},
							{Name: "strategy-check2"},
						},
					},
				},
			},
		},
		"empty AdmissionChecks": {
			input: &ClusterQueue{
				ObjectMeta: defaultObjectMeta,
				Spec: ClusterQueueSpec{
					AdmissionChecks: []AdmissionCheckReference{},
				},
			},
			expected: &v1beta2.ClusterQueue{
				ObjectMeta: defaultObjectMeta,
				Spec:       v1beta2.ClusterQueueSpec{},
			},
		},
		"FlavorFungibility Preempt to MayStopSearch": {
			input: &ClusterQueue{
				ObjectMeta: defaultObjectMeta,
				Spec: ClusterQueueSpec{
					FlavorFungibility: &FlavorFungibility{
						WhenCanPreempt: Preempt,
					},
				},
			},
			expected: &v1beta2.ClusterQueue{
				ObjectMeta: defaultObjectMeta,
				Spec: v1beta2.ClusterQueueSpec{
					FlavorFungibility: &v1beta2.FlavorFungibility{
						WhenCanPreempt: v1beta2.MayStopSearch,
					},
				},
			},
		},
		"FlavorFungibility Borrow to MayStopSearch": {
			input: &ClusterQueue{
				ObjectMeta: defaultObjectMeta,
				Spec: ClusterQueueSpec{
					FlavorFungibility: &FlavorFungibility{
						WhenCanBorrow: Borrow,
					},
				},
			},
			expected: &v1beta2.ClusterQueue{
				ObjectMeta: defaultObjectMeta,
				Spec: v1beta2.ClusterQueueSpec{
					FlavorFungibility: &v1beta2.FlavorFungibility{
						WhenCanBorrow: v1beta2.MayStopSearch,
					},
				},
			},
		},
		"FlavorFungibility MayStopSearch preserved": {
			input: &ClusterQueue{
				ObjectMeta: defaultObjectMeta,
				Spec: ClusterQueueSpec{
					FlavorFungibility: &FlavorFungibility{
						WhenCanPreempt: MayStopSearch,
						WhenCanBorrow:  MayStopSearch,
					},
				},
			},
			expected: &v1beta2.ClusterQueue{
				ObjectMeta: defaultObjectMeta,
				Spec: v1beta2.ClusterQueueSpec{
					FlavorFungibility: &v1beta2.FlavorFungibility{
						WhenCanPreempt: v1beta2.MayStopSearch,
						WhenCanBorrow:  v1beta2.MayStopSearch,
					},
				},
			},
		},
		"FlavorFungibility TryNextFlavor preserved": {
			input: &ClusterQueue{
				ObjectMeta: defaultObjectMeta,
				Spec: ClusterQueueSpec{
					FlavorFungibility: &FlavorFungibility{
						WhenCanPreempt: TryNextFlavor,
						WhenCanBorrow:  TryNextFlavor,
					},
				},
			},
			expected: &v1beta2.ClusterQueue{
				ObjectMeta: defaultObjectMeta,
				Spec: v1beta2.ClusterQueueSpec{
					FlavorFungibility: &v1beta2.FlavorFungibility{
						WhenCanPreempt: v1beta2.TryNextFlavor,
						WhenCanBorrow:  v1beta2.TryNextFlavor,
					},
				},
			},
		},
		"complete ClusterQueue with all fields": {
			input: &ClusterQueue{
				ObjectMeta: defaultObjectMeta,
				Spec: ClusterQueueSpec{
					Cohort:          "prod-cohort",
					AdmissionChecks: []AdmissionCheckReference{"multikueue-check"},
					FlavorFungibility: &FlavorFungibility{
						WhenCanPreempt: TryNextFlavor,
						WhenCanBorrow:  MayStopSearch,
					},
					QueueingStrategy: StrictFIFO,
				},
				Status: ClusterQueueStatus{
					FairSharing: &FairSharingStatus{
						WeightedShare: 100,
						AdmissionFairSharingStatus: &AdmissionFairSharingStatus{
							ConsumedResources: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("10"),
							},
							LastUpdate: metav1.Now(),
						},
					},
					Conditions: []metav1.Condition{
						{
							Type:   "Active",
							Status: metav1.ConditionTrue,
							Reason: "Ready",
						},
					},
					PendingWorkloads: 5,
				},
			},
			expected: &v1beta2.ClusterQueue{
				ObjectMeta: defaultObjectMeta,
				Spec: v1beta2.ClusterQueueSpec{
					CohortName: "prod-cohort",
					AdmissionChecksStrategy: &v1beta2.AdmissionChecksStrategy{
						AdmissionChecks: []v1beta2.AdmissionCheckStrategyRule{
							{Name: "multikueue-check"},
						},
					},
					FlavorFungibility: &v1beta2.FlavorFungibility{
						WhenCanPreempt: v1beta2.TryNextFlavor,
						WhenCanBorrow:  v1beta2.MayStopSearch,
					},
					QueueingStrategy: v1beta2.StrictFIFO,
				},
				Status: v1beta2.ClusterQueueStatus{
					FairSharing: &v1beta2.FairSharingStatus{
						WeightedShare: 100,
					},
					Conditions: []metav1.Condition{
						{
							Type:   "Active",
							Status: metav1.ConditionTrue,
							Reason: "Ready",
						},
					},
					PendingWorkloads: 5,
				},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			result := &v1beta2.ClusterQueue{}
			if err := tc.input.ConvertTo(result); err != nil {
				t.Fatalf("ConvertTo failed: %v", err)
			}
			if diff := cmp.Diff(tc.expected, result); diff != "" {
				t.Errorf("unexpected conversion result (-want +got):\n%s", diff)
			}
		})
	}
}

func TestClusterQueueConvertFrom(t *testing.T) {
	defaultObjectMeta := metav1.ObjectMeta{
		Name: "test-clusterqueue",
	}

	testCases := map[string]struct {
		input    *v1beta2.ClusterQueue
		expected *ClusterQueue
	}{
		"nil cohortName": {
			input: &v1beta2.ClusterQueue{
				ObjectMeta: defaultObjectMeta,
				Spec: v1beta2.ClusterQueueSpec{
					CohortName: "",
				},
			},
			expected: &ClusterQueue{
				ObjectMeta: defaultObjectMeta,
				Spec: ClusterQueueSpec{
					Cohort: "",
				},
			},
		},
		"cohortName to cohort conversion": {
			input: &v1beta2.ClusterQueue{
				ObjectMeta: defaultObjectMeta,
				Spec: v1beta2.ClusterQueueSpec{
					CohortName: "test-cohort",
				},
			},
			expected: &ClusterQueue{
				ObjectMeta: defaultObjectMeta,
				Spec: ClusterQueueSpec{
					Cohort: "test-cohort",
				},
			},
		},
		"FlavorFungibility MayStopSearch preserved": {
			input: &v1beta2.ClusterQueue{
				ObjectMeta: defaultObjectMeta,
				Spec: v1beta2.ClusterQueueSpec{
					FlavorFungibility: &v1beta2.FlavorFungibility{
						WhenCanPreempt: v1beta2.MayStopSearch,
						WhenCanBorrow:  v1beta2.MayStopSearch,
					},
				},
			},
			expected: &ClusterQueue{
				ObjectMeta: defaultObjectMeta,
				Spec: ClusterQueueSpec{
					FlavorFungibility: &FlavorFungibility{
						WhenCanPreempt: MayStopSearch,
						WhenCanBorrow:  MayStopSearch,
					},
				},
			},
		},
		"FlavorFungibility TryNextFlavor preserved": {
			input: &v1beta2.ClusterQueue{
				ObjectMeta: defaultObjectMeta,
				Spec: v1beta2.ClusterQueueSpec{
					FlavorFungibility: &v1beta2.FlavorFungibility{
						WhenCanPreempt: v1beta2.TryNextFlavor,
						WhenCanBorrow:  v1beta2.TryNextFlavor,
					},
				},
			},
			expected: &ClusterQueue{
				ObjectMeta: defaultObjectMeta,
				Spec: ClusterQueueSpec{
					FlavorFungibility: &FlavorFungibility{
						WhenCanPreempt: TryNextFlavor,
						WhenCanBorrow:  TryNextFlavor,
					},
				},
			},
		},
		"complete ClusterQueue with all fields": {
			input: &v1beta2.ClusterQueue{
				ObjectMeta: defaultObjectMeta,
				Spec: v1beta2.ClusterQueueSpec{
					CohortName: "prod-cohort",
					FlavorFungibility: &v1beta2.FlavorFungibility{
						WhenCanPreempt: v1beta2.MayStopSearch,
						WhenCanBorrow:  v1beta2.TryNextFlavor,
					},
					QueueingStrategy: v1beta2.StrictFIFO,
				},
				Status: v1beta2.ClusterQueueStatus{
					FairSharing: &v1beta2.FairSharingStatus{
						WeightedShare: 100,
					},
					Conditions: []metav1.Condition{
						{
							Type:   "Active",
							Status: metav1.ConditionTrue,
							Reason: "Ready",
						},
					},
					PendingWorkloads: 5,
				},
			},
			expected: &ClusterQueue{
				ObjectMeta: defaultObjectMeta,
				Spec: ClusterQueueSpec{
					Cohort: "prod-cohort",
					FlavorFungibility: &FlavorFungibility{
						WhenCanPreempt: MayStopSearch,
						WhenCanBorrow:  TryNextFlavor,
					},
					QueueingStrategy: StrictFIFO,
				},
				Status: ClusterQueueStatus{
					FairSharing: &FairSharingStatus{
						WeightedShare: 100,
					},
					Conditions: []metav1.Condition{
						{
							Type:   "Active",
							Status: metav1.ConditionTrue,
							Reason: "Ready",
						},
					},
					PendingWorkloads: 5,
				},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			result := &ClusterQueue{}
			if err := result.ConvertFrom(tc.input); err != nil {
				t.Fatalf("ConvertFrom failed: %v", err)
			}
			if diff := cmp.Diff(tc.expected, result); diff != "" {
				t.Errorf("unexpected conversion result (-want +got):\n%s", diff)
			}
		})
	}
}

func TestClusterQueueConversion_RoundTrip(t *testing.T) {
	testCases := map[string]struct {
		v1beta1Obj *ClusterQueue
	}{
		"complete ClusterQueue with cohort and FlavorFungibility": {
			v1beta1Obj: &ClusterQueue{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-clusterqueue",
				},
				Spec: ClusterQueueSpec{
					Cohort: "prod-cohort",
					FlavorFungibility: &FlavorFungibility{
						WhenCanPreempt: TryNextFlavor,
						WhenCanBorrow:  MayStopSearch,
					},
					QueueingStrategy: StrictFIFO,
				},
				Status: ClusterQueueStatus{
					Conditions: []metav1.Condition{
						{
							Type:   "Active",
							Status: metav1.ConditionTrue,
							Reason: "Ready",
						},
					},
					PendingWorkloads: 10,
				},
			},
		},
		"minimal ClusterQueue": {
			v1beta1Obj: &ClusterQueue{
				ObjectMeta: metav1.ObjectMeta{
					Name: "minimal-cq",
				},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			// Convert v1beta1 -> v1beta2
			v1beta2Obj := &v1beta2.ClusterQueue{}
			if err := tc.v1beta1Obj.ConvertTo(v1beta2Obj); err != nil {
				t.Fatalf("ConvertTo failed: %v", err)
			}

			// Convert v1beta2 -> v1beta1 (round-trip)
			roundTripped := &ClusterQueue{}
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

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
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

func TestWorkloadConvertTo(t *testing.T) {
	defaultObjectMeta := metav1.ObjectMeta{
		Name:      "test-workload",
		Namespace: "default",
	}

	testCases := map[string]struct {
		input    *Workload
		expected *v1beta2.Workload
	}{
		"nil AccumulatedPastExexcutionTimeSeconds": {
			input: &Workload{
				ObjectMeta: defaultObjectMeta,
				Status: WorkloadStatus{
					AccumulatedPastExexcutionTimeSeconds: nil,
				},
			},
			expected: &v1beta2.Workload{
				ObjectMeta: defaultObjectMeta,
				Status: v1beta2.WorkloadStatus{
					AccumulatedPastExecutionTimeSeconds: nil,
				},
			},
		},
		"zero AccumulatedPastExexcutionTimeSeconds": {
			input: &Workload{
				ObjectMeta: defaultObjectMeta,
				Status: WorkloadStatus{
					AccumulatedPastExexcutionTimeSeconds: ptr.To[int32](0),
				},
			},
			expected: &v1beta2.Workload{
				ObjectMeta: defaultObjectMeta,
				Status: v1beta2.WorkloadStatus{
					AccumulatedPastExecutionTimeSeconds: ptr.To[int32](0),
				},
			},
		},
		"non-zero AccumulatedPastExexcutionTimeSeconds": {
			input: &Workload{
				ObjectMeta: defaultObjectMeta,
				Status: WorkloadStatus{
					AccumulatedPastExexcutionTimeSeconds: ptr.To[int32](3600),
				},
			},
			expected: &v1beta2.Workload{
				ObjectMeta: defaultObjectMeta,
				Status: v1beta2.WorkloadStatus{
					AccumulatedPastExecutionTimeSeconds: ptr.To[int32](3600),
				},
			},
		},
		"with conditions and other fields": {
			input: &Workload{
				ObjectMeta: defaultObjectMeta,
				Status: WorkloadStatus{
					Conditions: []metav1.Condition{
						{
							Type:   "Admitted",
							Status: metav1.ConditionTrue,
							Reason: "AdmittedByClusterQueue",
						},
					},
					AccumulatedPastExexcutionTimeSeconds: ptr.To[int32](7200),
				},
			},
			expected: &v1beta2.Workload{
				ObjectMeta: defaultObjectMeta,
				Status: v1beta2.WorkloadStatus{
					Conditions: []metav1.Condition{
						{
							Type:   "Admitted",
							Status: metav1.ConditionTrue,
							Reason: "AdmittedByClusterQueue",
						},
					},
					AccumulatedPastExecutionTimeSeconds: ptr.To[int32](7200),
				},
			},
		},
		"with PodPriorityClassSource": {
			input: &Workload{
				ObjectMeta: defaultObjectMeta,
				Spec: WorkloadSpec{
					Priority:            ptr.To[int32](100),
					PriorityClassSource: PodPriorityClassSource,
					PriorityClassName:   "low",
				},
			},
			expected: &v1beta2.Workload{
				ObjectMeta: defaultObjectMeta,
				Spec: v1beta2.WorkloadSpec{
					Priority: ptr.To[int32](100),
					PriorityClassRef: &v1beta2.PriorityClassRef{
						Group: v1beta2.PodPriorityClassGroup,
						Kind:  v1beta2.PodPriorityClassKind,
						Name:  "low",
					},
				},
			},
		},
		"with WorkloadPriorityClassSource": {
			input: &Workload{
				ObjectMeta: defaultObjectMeta,
				Spec: WorkloadSpec{
					Priority:            ptr.To[int32](100),
					PriorityClassSource: WorkloadPriorityClassSource,
					PriorityClassName:   "low",
				},
			},
			expected: &v1beta2.Workload{
				ObjectMeta: defaultObjectMeta,
				Spec: v1beta2.WorkloadSpec{
					Priority: ptr.To[int32](100),
					PriorityClassRef: &v1beta2.PriorityClassRef{
						Group: v1beta2.WorkloadPriorityClassGroup,
						Kind:  v1beta2.WorkloadPriorityClassKind,
						Name:  "low",
					},
				},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			result := &v1beta2.Workload{}
			if err := tc.input.ConvertTo(result); err != nil {
				t.Fatalf("ConvertTo failed: %v", err)
			}
			if diff := cmp.Diff(tc.expected, result); diff != "" {
				t.Errorf("unexpected conversion result (-want +got):\n%s", diff)
			}
		})
	}
}

func TestWorkloadConvertFrom(t *testing.T) {
	defaultObjectMeta := metav1.ObjectMeta{
		Name:      "test-workload",
		Namespace: "default",
	}

	testCases := map[string]struct {
		input    *v1beta2.Workload
		expected *Workload
	}{
		"nil AccumulatedPastExecutionTimeSeconds": {
			input: &v1beta2.Workload{
				ObjectMeta: defaultObjectMeta,
				Status: v1beta2.WorkloadStatus{
					AccumulatedPastExecutionTimeSeconds: nil,
				},
			},
			expected: &Workload{
				ObjectMeta: defaultObjectMeta,
				Status: WorkloadStatus{
					AccumulatedPastExexcutionTimeSeconds: nil,
				},
			},
		},
		"zero AccumulatedPastExecutionTimeSeconds": {
			input: &v1beta2.Workload{
				ObjectMeta: defaultObjectMeta,
				Status: v1beta2.WorkloadStatus{
					AccumulatedPastExecutionTimeSeconds: ptr.To[int32](0),
				},
			},
			expected: &Workload{
				ObjectMeta: defaultObjectMeta,
				Status: WorkloadStatus{
					AccumulatedPastExexcutionTimeSeconds: ptr.To[int32](0),
				},
			},
		},
		"non-zero AccumulatedPastExecutionTimeSeconds": {
			input: &v1beta2.Workload{
				ObjectMeta: defaultObjectMeta,
				Status: v1beta2.WorkloadStatus{
					AccumulatedPastExecutionTimeSeconds: ptr.To[int32](3600),
				},
			},
			expected: &Workload{
				ObjectMeta: defaultObjectMeta,
				Status: WorkloadStatus{
					AccumulatedPastExexcutionTimeSeconds: ptr.To[int32](3600),
				},
			},
		},
		"with conditions and other fields": {
			input: &v1beta2.Workload{
				ObjectMeta: defaultObjectMeta,
				Status: v1beta2.WorkloadStatus{
					Conditions: []metav1.Condition{
						{
							Type:   "Finished",
							Status: metav1.ConditionTrue,
							Reason: "JobFinished",
						},
					},
					AccumulatedPastExecutionTimeSeconds: ptr.To[int32](1800),
				},
			},
			expected: &Workload{
				ObjectMeta: defaultObjectMeta,
				Status: WorkloadStatus{
					Conditions: []metav1.Condition{
						{
							Type:   "Finished",
							Status: metav1.ConditionTrue,
							Reason: "JobFinished",
						},
					},
					AccumulatedPastExexcutionTimeSeconds: ptr.To[int32](1800),
				},
			},
		},
		"with PodPriorityClassRef": {
			input: &v1beta2.Workload{
				ObjectMeta: defaultObjectMeta,
				Spec: v1beta2.WorkloadSpec{
					Priority: ptr.To[int32](100),
					PriorityClassRef: &v1beta2.PriorityClassRef{
						Group: v1beta2.PodPriorityClassGroup,
						Kind:  v1beta2.PodPriorityClassKind,
						Name:  "low",
					},
				},
			},
			expected: &Workload{
				ObjectMeta: defaultObjectMeta,
				Spec: WorkloadSpec{
					Priority:            ptr.To[int32](100),
					PriorityClassSource: PodPriorityClassSource,
					PriorityClassName:   "low",
				},
			},
		},
		"with WorkloadPriorityClassRef": {
			input: &v1beta2.Workload{
				ObjectMeta: defaultObjectMeta,
				Spec: v1beta2.WorkloadSpec{
					Priority: ptr.To[int32](100),
					PriorityClassRef: &v1beta2.PriorityClassRef{
						Group: v1beta2.WorkloadPriorityClassGroup,
						Kind:  v1beta2.WorkloadPriorityClassKind,
						Name:  "low",
					},
				},
			},
			expected: &Workload{
				ObjectMeta: defaultObjectMeta,
				Spec: WorkloadSpec{
					Priority:            ptr.To[int32](100),
					PriorityClassSource: WorkloadPriorityClassSource,
					PriorityClassName:   "low",
				},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			result := &Workload{}
			if err := result.ConvertFrom(tc.input); err != nil {
				t.Fatalf("ConvertFrom failed: %v", err)
			}
			if diff := cmp.Diff(tc.expected, result); diff != "" {
				t.Errorf("unexpected conversion result (-want +got):\n%s", diff)
			}
		})
	}
}

func TestWorkloadConversion_RoundTrip(t *testing.T) {
	defaultObjectMeta := metav1.ObjectMeta{
		Name:      "test-workload",
		Namespace: "default",
	}

	testCases := map[string]struct {
		v1beta1Obj *Workload
	}{
		"complete Workload with AccumulatedPastExexcutionTimeSeconds": {
			v1beta1Obj: &Workload{
				ObjectMeta: defaultObjectMeta,
				Status: WorkloadStatus{
					Conditions: []metav1.Condition{
						{
							Type:   "Admitted",
							Status: metav1.ConditionTrue,
							Reason: "AdmittedByClusterQueue",
						},
					},
					AccumulatedPastExexcutionTimeSeconds: ptr.To[int32](5400),
				},
			},
		},
		"Workload with nil AccumulatedPastExexcutionTimeSeconds": {
			v1beta1Obj: &Workload{
				ObjectMeta: defaultObjectMeta,
				Status: WorkloadStatus{
					AccumulatedPastExexcutionTimeSeconds: nil,
				},
			},
		},
		"Workload with zero AccumulatedPastExexcutionTimeSeconds": {
			v1beta1Obj: &Workload{
				ObjectMeta: defaultObjectMeta,
				Status: WorkloadStatus{
					AccumulatedPastExexcutionTimeSeconds: ptr.To[int32](0),
				},
			},
		},
		"Workload with PodPriorityClassSource": {
			v1beta1Obj: &Workload{
				ObjectMeta: defaultObjectMeta,
				Spec: WorkloadSpec{
					Priority:            ptr.To[int32](100),
					PriorityClassSource: PodPriorityClassSource,
					PriorityClassName:   "low",
				},
			},
		},
		"Workload with WorkloadPriorityClassSource": {
			v1beta1Obj: &Workload{
				ObjectMeta: defaultObjectMeta,
				Spec: WorkloadSpec{
					Priority:            ptr.To[int32](100),
					PriorityClassSource: WorkloadPriorityClassSource,
					PriorityClassName:   "low",
				},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			// Convert v1beta1 -> v1beta2
			v1beta2Obj := &v1beta2.Workload{}
			if err := tc.v1beta1Obj.ConvertTo(v1beta2Obj); err != nil {
				t.Fatalf("ConvertTo failed: %v", err)
			}

			// Convert v1beta2 -> v1beta1 (round-trip)
			roundTripped := &Workload{}
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

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

package testing

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestHaveConditionStatusAndReason(t *testing.T) {
	testCases := map[string]struct {
		conditionType         string
		status                metav1.ConditionStatus
		reason                string
		conditions            any
		want                  bool
		wantErr               string
		wantFailureMsg        string
		wantNegatedFailureMsg string
	}{
		"invalid conditions": {
			conditionType: "TestCondition",
			conditions:    "invalid type",
			want:          false,
			wantErr:       "Condition matcher expects a []metav1.Condition. Got:\n    <string>: invalid type",
		},
		"success": {
			conditionType: "TestCondition",
			conditions:    []metav1.Condition{{Type: "TestCondition"}},
			want:          true,
		},
		"success with status": {
			conditionType: "TestCondition",
			conditions:    []metav1.Condition{{Type: "TestCondition", Status: metav1.ConditionTrue}},
			status:        metav1.ConditionTrue,
			want:          true,
		},
		"success with status and reason": {
			conditionType: "TestCondition",
			conditions:    []metav1.Condition{{Type: "TestCondition", Status: metav1.ConditionTrue, Reason: "TestReason"}},
			status:        metav1.ConditionTrue,
			reason:        "TestReason",
			want:          true,
		},
		"failure": {
			conditionType: "InvalidTestCondition",
			conditions:    []metav1.Condition{},
			want:          false,
			wantFailureMsg: `Expected
    <[]v1.Condition | len:0, cap:0>: []
to have condition type InvalidTestCondition`,
			wantNegatedFailureMsg: `Expected
    <[]v1.Condition | len:0, cap:0>: []
not to have condition type InvalidTestCondition`,
		},
		"failure with status": {
			conditionType: "InvalidTestCondition",
			status:        metav1.ConditionTrue,
			conditions:    []metav1.Condition{},
			want:          false,
			wantFailureMsg: `Expected
    <[]v1.Condition | len:0, cap:0>: []
to have condition type InvalidTestCondition and status True`,
			wantNegatedFailureMsg: `Expected
    <[]v1.Condition | len:0, cap:0>: []
not to have condition type InvalidTestCondition and status True`,
		},
		"failure with reason": {
			conditionType: "InvalidTestCondition",
			reason:        "TestReason",
			conditions:    []metav1.Condition{},
			want:          false,
			wantFailureMsg: `Expected
    <[]v1.Condition | len:0, cap:0>: []
to have condition type InvalidTestCondition and reason TestReason`,
			wantNegatedFailureMsg: `Expected
    <[]v1.Condition | len:0, cap:0>: []
not to have condition type InvalidTestCondition and reason TestReason`,
		},
		"failure with status and reason": {
			conditionType: "InvalidTestCondition",
			status:        metav1.ConditionTrue,
			reason:        "TestReason",
			conditions:    []metav1.Condition{},
			want:          false,
			wantFailureMsg: `Expected
    <[]v1.Condition | len:0, cap:0>: []
to have condition type InvalidTestCondition, status True and reason TestReason`,
			wantNegatedFailureMsg: `Expected
    <[]v1.Condition | len:0, cap:0>: []
not to have condition type InvalidTestCondition, status True and reason TestReason`,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			matcher := HaveConditionStatusAndReason(tc.conditionType, tc.status, tc.reason)
			got, gotErr := matcher.Match(tc.conditions)

			var gotErrStr string
			if gotErr != nil {
				gotErrStr = gotErr.Error()
			}

			if diff := cmp.Diff(tc.wantErr, gotErrStr); diff != "" {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}

			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Unexpected result (-want,+got):\n%s", diff)
			}

			if !got && gotErr == nil {
				gotFailureMsg := matcher.FailureMessage(tc.conditions)
				if diff := cmp.Diff(tc.wantFailureMsg, gotFailureMsg); diff != "" {
					t.Errorf("Unexpected failure message (-want,+got):\n%s", diff)
				}

				gotNegatedFailureMsg := matcher.NegatedFailureMessage(tc.conditions)
				if diff := cmp.Diff(tc.wantNegatedFailureMsg, gotNegatedFailureMsg); diff != "" {
					t.Errorf("Unexpected negated failure messge (-want,+got):\n%s", diff)
				}
			}
		})
	}
}

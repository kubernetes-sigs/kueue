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

package util

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
)

func TestHaveEvent(t *testing.T) {
	observedEvent := eventsv1.Event{
		Regarding: corev1.ObjectReference{Namespace: "default", Name: "observed-workload"},
		Reason:    "TestReason",
		Type:      "Normal",
		Note:      "observed note",
	}
	testCases := map[string]struct {
		expectedEvent         eventsv1.Event
		actual                any
		want                  bool
		wantErr               string
		wantFailureSubstrings []string
	}{
		"invalid event list": {
			actual:  "invalid type",
			wantErr: "event matcher expects a []eventsv1.Event. Got:\n    <string>: invalid type",
		},
		"matching fields ignore Regarding": {
			expectedEvent: eventsv1.Event{
				Regarding: corev1.ObjectReference{Namespace: "other", Name: "other-workload"},
				Reason:    observedEvent.Reason,
				Type:      observedEvent.Type,
				Note:      observedEvent.Note,
			},
			actual: []eventsv1.Event{observedEvent},
			want:   true,
		},
		"different reason": {
			expectedEvent: eventsv1.Event{Reason: "DifferentReason", Type: observedEvent.Type, Note: observedEvent.Note},
			actual:        []eventsv1.Event{observedEvent},
			want:          false,
			wantFailureSubstrings: []string{
				observedEvent.Reason,
				`Reason "DifferentReason"`,
			},
		},
		"different type": {
			expectedEvent: eventsv1.Event{Reason: observedEvent.Reason, Type: "Warning", Note: observedEvent.Note},
			actual:        []eventsv1.Event{observedEvent},
			want:          false,
			wantFailureSubstrings: []string{
				observedEvent.Type,
				`Type "Warning"`,
			},
		},
		"different note includes observed events in failure": {
			expectedEvent: eventsv1.Event{Reason: observedEvent.Reason, Type: observedEvent.Type, Note: "expected note"},
			actual:        []eventsv1.Event{observedEvent},
			wantFailureSubstrings: []string{
				observedEvent.Note,
				`Reason "TestReason"`,
				`Type "Normal"`,
				`Note "expected note"`,
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			matcher := haveEvent(tc.expectedEvent)
			got, gotErr := matcher.Match(tc.actual)

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
				failureMessage := matcher.FailureMessage(tc.actual)
				for _, want := range tc.wantFailureSubstrings {
					if !strings.Contains(failureMessage, want) {
						t.Errorf("FailureMessage() does not contain %q:\n%s", want, failureMessage)
					}
				}
			}
		})
	}
}

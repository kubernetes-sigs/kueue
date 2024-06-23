/*
Copyright 2024 The Kubernetes Authors.

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

package list

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestPodPrint(t *testing.T) {
	testStartTime := time.Now()

	testCases := map[string]struct {
		options *PodOptions
		in      *corev1.PodList
		out     []metav1.TableRow
	}{
		"should print local queue list": {
			options: &PodOptions{},
			in: &corev1.PodList{
				Items: []corev1.Pod{
					{
						TypeMeta: metav1.TypeMeta{},
						ObjectMeta: metav1.ObjectMeta{
							Name:              "test-pod",
							CreationTimestamp: metav1.NewTime(testStartTime.Add(-time.Hour).Truncate(time.Second)),
						},
						Status: corev1.PodStatus{
							Phase: "RUNNING",
						},
					},
				},
			},
			out: []metav1.TableRow{
				{
					Cells: []any{"test-pod", corev1.PodPhase("RUNNING"), "60m"},
					Object: runtime.RawExtension{
						Object: &corev1.Pod{
							TypeMeta: metav1.TypeMeta{},
							ObjectMeta: metav1.ObjectMeta{
								Name:              "test-pod",
								CreationTimestamp: metav1.NewTime(testStartTime.Add(-time.Hour).Truncate(time.Second)),
							},
							Status: corev1.PodStatus{
								Phase: "RUNNING",
							},
						},
					},
				},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			out := printPodList(tc.in)
			if diff := cmp.Diff(tc.out, out); diff != "" {
				t.Errorf("Unexpected result (-want,+got):\n%s", diff)
			}
		})
	}
}
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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	testingclock "k8s.io/utils/clock/testing"

	"sigs.k8s.io/kueue/apis/kueue/v1beta1"
)

func TestLocalQueuePrint(t *testing.T) {
	testStartTime := time.Now()

	testCases := map[string]struct {
		options *LocalQueueOptions
		in      *v1beta1.LocalQueueList
		out     []metav1.TableRow
	}{
		"should print local queue list": {
			options: &LocalQueueOptions{},
			in: &v1beta1.LocalQueueList{
				Items: []v1beta1.LocalQueue{
					{
						TypeMeta: metav1.TypeMeta{},
						ObjectMeta: metav1.ObjectMeta{
							Name:              "lq",
							CreationTimestamp: metav1.NewTime(testStartTime.Add(-time.Hour).Truncate(time.Second)),
						},
						Spec: v1beta1.LocalQueueSpec{ClusterQueue: "cq1"},
						Status: v1beta1.LocalQueueStatus{
							PendingWorkloads:  1,
							AdmittedWorkloads: 2,
						},
					},
				},
			},
			out: []metav1.TableRow{
				{
					Cells: []any{"lq", v1beta1.ClusterQueueReference("cq1"), int32(1), int32(2), "60m"},
					Object: runtime.RawExtension{
						Object: &v1beta1.LocalQueue{
							TypeMeta: metav1.TypeMeta{},
							ObjectMeta: metav1.ObjectMeta{
								Name:              "lq",
								CreationTimestamp: metav1.NewTime(testStartTime.Add(-time.Hour).Truncate(time.Second)),
							},
							Spec: v1beta1.LocalQueueSpec{ClusterQueue: "cq1"},
							Status: v1beta1.LocalQueueStatus{
								PendingWorkloads:  1,
								AdmittedWorkloads: 2,
							},
						},
					},
				},
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			p := newLocalQueueTablePrinter().WithClock(testingclock.NewFakeClock(testStartTime))
			out := p.printLocalQueueList(tc.in)
			if diff := cmp.Diff(tc.out, out); diff != "" {
				t.Errorf("Unexpected result (-want,+got):\n%s", diff)
			}
		})
	}
}

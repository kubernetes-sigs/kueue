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

package list

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	testingclock "k8s.io/utils/clock/testing"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestLocalQueuePrint(t *testing.T) {
	testStartTime := time.Now()
	creationTime := testStartTime.Add(-time.Hour).Truncate(time.Second)

	testCases := map[string]struct {
		in  *kueue.LocalQueueList
		out []metav1.TableRow
	}{
		"should print active local queue": {
			in: &kueue.LocalQueueList{
				Items: []kueue.LocalQueue{
					*utiltestingapi.MakeLocalQueue("lq", "").
						ClusterQueue("cq1").
						Creation(creationTime).
						PendingWorkloads(1).
						AdmittedWorkloads(2).
						Active(metav1.ConditionTrue).
						Obj(),
				},
			},
			out: []metav1.TableRow{
				{
					Cells: []any{"lq", kueue.ClusterQueueReference("cq1"), int32(1), int32(2), true, "60m"},
					Object: runtime.RawExtension{
						Object: utiltestingapi.MakeLocalQueue("lq", "").
							ClusterQueue("cq1").
							Creation(creationTime).
							PendingWorkloads(1).
							AdmittedWorkloads(2).
							Active(metav1.ConditionTrue).
							Obj(),
					},
				},
			},
		},
		"should print inactive local queue": {
			in: &kueue.LocalQueueList{
				Items: []kueue.LocalQueue{
					*utiltestingapi.MakeLocalQueue("lq", "").
						ClusterQueue("cq1").
						Creation(creationTime).
						Active(metav1.ConditionFalse).
						Obj(),
				},
			},
			out: []metav1.TableRow{
				{
					Cells: []any{"lq", kueue.ClusterQueueReference("cq1"), int32(0), int32(0), false, "60m"},
					Object: runtime.RawExtension{
						Object: utiltestingapi.MakeLocalQueue("lq", "").
							ClusterQueue("cq1").
							Creation(creationTime).
							Active(metav1.ConditionFalse).
							Obj(),
					},
				},
			},
		},
		"should print local queue without active condition as inactive": {
			in: &kueue.LocalQueueList{
				Items: []kueue.LocalQueue{
					*utiltestingapi.MakeLocalQueue("lq", "").
						ClusterQueue("cq1").
						Creation(creationTime).
						Obj(),
				},
			},
			out: []metav1.TableRow{
				{
					Cells: []any{"lq", kueue.ClusterQueueReference("cq1"), int32(0), int32(0), false, "60m"},
					Object: runtime.RawExtension{
						Object: utiltestingapi.MakeLocalQueue("lq", "").
							ClusterQueue("cq1").
							Creation(creationTime).
							Obj(),
					},
				},
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			p := newLocalQueueTablePrinter().WithClock(testingclock.NewFakeClock(testStartTime))
			out := p.printLocalQueueList(tc.in)
			if diff := cmp.Diff(tc.out, out, cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime")); diff != "" {
				t.Errorf("Unexpected result (-want,+got):\n%s", diff)
			}
		})
	}
}

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

package queue

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
)

func TestRunDurationFIFOAndPriority(t *testing.T) {
	now := time.Unix(1000, 0)
	cases := map[string]struct {
		priority int32
		reason   string
		waitAt   time.Time
		want     string
	}{
		"waiting same priority gets a turn": {priority: 10, reason: kueue.WorkloadEvictedByRunDuration, waitAt: now.Add(-time.Minute), want: "waiting"},
		"same second is not ordered by UID": {priority: 10, reason: kueue.WorkloadEvictedByRunDuration, waitAt: now, want: "waiting"},
		"higher priority remains first":     {priority: 20, reason: kueue.WorkloadEvictedByRunDuration, waitAt: now.Add(-time.Minute), want: "expired"},
		"other eviction keeps its ordering": {priority: 10, reason: kueue.WorkloadEvictedByPreemption, waitAt: now.Add(-time.Minute), want: "expired"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			q := newClusterQueueImpl(ctx, nil, workload.Ordering{}, realClock)
			expired := utiltestingapi.MakeWorkload("expired", "ns").Creation(now.Add(-time.Hour)).Priority(tc.priority).Obj()
			expired.UID = "a"
			waiting := utiltestingapi.MakeWorkload("waiting", "ns").Creation(tc.waitAt).Priority(10).Obj()
			waiting.UID = "z"
			workloadevict.SetEvictedCondition(expired, now, tc.reason, "expired")
			workload.CompleteRunDurationRequeue(expired, now)
			expired.Status.Conditions = append(expired.Status.Conditions, metav1.Condition{Type: kueue.WorkloadRequeued, Status: metav1.ConditionTrue})
			q.PushOrUpdate(workload.NewInfo(log, expired))
			q.PushOrUpdate(workload.NewInfo(log, waiting))
			if got := q.Pop(); got == nil || got.Obj.Name != tc.want {
				t.Fatalf("queue head = %v, want %s", got, tc.want)
			}
		})
	}
}

func TestRunDurationUnsupportedDoesNotBlockUntimedWork(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	q := newClusterQueueImpl(ctx, nil, workload.Ordering{}, realClock)
	q.queueingStrategy = kueue.StrictFIFO
	timed := utiltestingapi.MakeWorkload("unsupported", "ns").Creation(time.Unix(1000, 0)).
		MaximumRunDurationSeconds(60).Obj()
	untimed := utiltestingapi.MakeWorkload("untimed", "ns").Creation(time.Unix(1001, 0)).Obj()
	q.PushOrUpdate(workload.NewInfo(log, timed))
	q.PushOrUpdate(workload.NewInfo(log, untimed))
	head := q.Pop()
	if head == nil || head.Obj.Name != timed.Name {
		t.Fatal("expected older timed workload first")
	}
	q.RequeueIfNotPresent(ctx, head, RequeueReasonRunDurationUnsupported, QuotaReservedReason(kueue.WorkloadQuotaReservedReasonRunDurationUnsupported))
	if next := q.Pop(); next == nil || next.Obj.Name != untimed.Name {
		t.Fatalf("unsupported duration blocked eligible untimed workload: %v", next)
	}
	updated := timed.DeepCopy()
	updated.Spec.MaximumRunDurationSeconds = nil
	q.PushOrUpdate(workload.NewInfo(log, updated))
	if next := q.Pop(); next == nil || next.Obj.Name != timed.Name {
		t.Fatalf("removing duration did not restore eligibility: %v", next)
	}
}

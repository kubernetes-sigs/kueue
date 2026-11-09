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
	"context"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	testingclock "k8s.io/utils/clock/testing"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestStrictFIFONoFitRetainsHeadWithoutBusyRetry(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	now := time.Now()
	cl := testingclock.NewFakeClock(now)
	cq := newClusterQueueImpl(ctx, nil, defaultOrdering, cl)
	cq.queueingStrategy = kueue.StrictFIFO
	head := workload.NewInfo(log, utiltestingapi.MakeWorkload("head", "ns").UID("head-uid").Creation(now).Obj())
	tail := workload.NewInfo(log, utiltestingapi.MakeWorkload("tail", "ns").UID("tail-uid").Creation(now.Add(time.Second)).Obj())
	cq.PushOrUpdate(head)
	cq.PushOrUpdate(tail)
	if got := cq.Pop(); got != head {
		t.Fatalf("first Pop() = %v, want head", got)
	}
	cq.RequeueIfNotPresent(ctx, head, RequeueReasonNoFit, "")
	for range 20 {
		cl.Step(10 * time.Millisecond)
		if got := cq.Pop(); got != nil {
			t.Fatalf("unchanged NoFit was retried or bypassed: got %s", got.Obj.Name)
		}
		if got := cq.PopMidCycle(); got != nil {
			t.Fatalf("mid-cycle refill bypassed retry control: %s", got.Obj.Name)
		}
	}
	if cq.PendingTotal() != 2 {
		t.Fatalf("pending = %d, want both head and tail retained", cq.PendingTotal())
	}
	cl.Step(time.Second)
	if got := cq.Pop(); got != head {
		t.Fatalf("fallback Pop() = %v, want original head", got)
	}
}

func TestStrictFIFONoFitWakeups(t *testing.T) {
	for _, change := range []string{"quota notification", "notification while inflight", "higher priority", "status only", "spec", "recreated UID", "preemption retry", "best effort", "pending flavors", "strategy", "requeued", "evicted", "effective requests", "deleted head"} {
		t.Run(change, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			now := time.Now()
			cl := testingclock.NewFakeClock(now)
			cq := newClusterQueueImpl(ctx, nil, defaultOrdering, cl)
			cq.queueingStrategy = kueue.StrictFIFO
			if change == "best effort" {
				cq.queueingStrategy = kueue.BestEffortFIFO
			}
			head := utiltestingapi.MakeWorkload("head", "ns").UID("head-uid").Creation(now).Request(corev1.ResourceCPU, "2").Obj()
			tail := utiltestingapi.MakeWorkload("tail", "ns").UID("tail-uid").Creation(now.Add(time.Second)).Request(corev1.ResourceCPU, "1").Obj()
			cq.PushOrUpdate(workload.NewInfo(log, head))
			cq.PushOrUpdate(workload.NewInfo(log, tail))
			info := cq.Pop()
			if change == "pending flavors" {
				info.FlavorScanState = &workload.FlavorScanState{
					TriedFlavors: []map[corev1.ResourceName]sets.Set[kueue.ResourceFlavorReference]{
						{corev1.ResourceCPU: sets.New[kueue.ResourceFlavorReference]("next")},
					},
				}
			}
			if change == "notification while inflight" {
				queueInadmissibleWorkloads(ctx, cq, nil)
			}
			reason := RequeueReasonNoFit
			if change == "preemption retry" {
				reason = RequeueReasonPendingPreemption
			}
			cq.RequeueIfNotPresent(ctx, info, reason, "")
			wantName := "head"
			switch change {
			case "quota notification":
				queueInadmissibleWorkloads(ctx, cq, nil)
			case "higher priority":
				cq.PushOrUpdate(workload.NewInfo(log, utiltestingapi.MakeWorkload("urgent", "ns").UID("urgent-uid").Priority(100).Obj()))
				wantName = "urgent"
			case "status only":
				updated := head.DeepCopy()
				updated.ResourceVersion = "new-rv"
				cq.PushOrUpdate(workload.NewInfo(log, updated))
				wantName = ""
			case "spec":
				updated := head.DeepCopy()
				updated.Spec.Priority = new(int32(1))
				cq.PushOrUpdate(workload.NewInfo(log, updated))
			case "strategy":
				if err := cq.Update(utiltestingapi.MakeClusterQueue("cq").QueueingStrategy(kueue.BestEffortFIFO).Obj()); err != nil {
					t.Fatal(err)
				}
			case "deleted head":
				cq.Delete(log, workload.Key(head))
				wantName = "tail"
			case "requeued", "evicted":
				updated := head.DeepCopy()
				conditionType := kueue.WorkloadRequeued
				if change == "evicted" {
					conditionType = kueue.WorkloadEvicted
				}
				updated.Status.Conditions = []metav1.Condition{{Type: conditionType, Status: metav1.ConditionTrue}}
				cq.PushOrUpdate(workload.NewInfo(log, updated))
			case "effective requests":
				updated := utiltestingapi.MakeWorkload("template", "ns").Request(corev1.ResourceCPU, "1").Obj().Spec.PodSets[0].Template.Spec
				cq.PushOrUpdate(workload.NewInfo(log, head.DeepCopy(), workload.WithEffectivePodSpecs([]corev1.PodSpec{updated})))
			case "recreated UID":
				updated := head.DeepCopy()
				updated.UID = "different-uid"
				cq.PushOrUpdate(workload.NewInfo(log, updated))
			case "best effort":
				wantName = "tail"
			}
			got := cq.Pop()
			if wantName == "" {
				if got != nil {
					t.Fatalf("status-only update retried %s", got.Obj.Name)
				}
			} else if got == nil || got.Obj.Name != wantName {
				t.Fatalf("Pop() = %v, want %s", got, wantName)
			}
		})
	}
}

// Advance between Pop's eligibility check and Heads' timer calculation.
type deadlineCrossingClock struct {
	*testingclock.FakeClock
	reads     atomic.Int32
	advanceAt atomic.Int32
}

func (c *deadlineCrossingClock) Now() time.Time {
	if c.reads.Add(1) == c.advanceAt.Load() {
		c.Step(time.Second)
	}
	return c.FakeClock.Now()
}

func TestStrictFIFONoFitManagerWakeup(t *testing.T) {
	for _, wakeup := range []string{"fallback", "resource notification", "cancel", "deadline crossing"} {
		t.Run(wakeup, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			cl := &deadlineCrossingClock{FakeClock: testingclock.NewFakeClock(time.Now())}
			cq := utiltestingapi.MakeClusterQueue("cq").QueueingStrategy(kueue.StrictFIFO).Obj()
			lq := utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue(cq.Name).Obj()
			wl := utiltestingapi.MakeWorkload("head", "ns").Queue("lq").UID("head-uid").Obj()
			client := utiltesting.NewClientBuilder().WithObjects(wl, lq).
				WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType).Build()
			m, requeuer := NewManagerForUnitTestsWithRequeuer(client, nil, WithClock(cl), WithPreemptionExpectations(preemptexpectations.New()))
			if err := m.AddLocalQueue(ctx, lq); err != nil {
				t.Fatal(err)
			}
			if err := m.AddClusterQueue(ctx, cq); err != nil {
				t.Fatal(err)
			}
			if err := m.AddOrUpdateWorkload(ctx, log, wl); err != nil {
				t.Fatal(err)
			}
			requeuer.ProcessRequeues(ctx)
			go m.CleanUpOnContext(ctx)
			first := m.Heads(ctx)
			if len(first) != 1 {
				t.Fatalf("first Heads() = %v", first)
			}
			m.RequeueWorkload(ctx, &first[0].Info, RequeueReasonNoFit, "")
			if wakeup == "deadline crossing" {
				cl.advanceAt.Store(cl.reads.Load() + 2)
			}
			done := make(chan []Head, 1)
			go func() { done <- m.Heads(ctx) }()
			if err := wait.PollUntilContextTimeout(ctx, time.Millisecond, time.Second, true, func(context.Context) (bool, error) { return cl.HasWaiters(), nil }); err != nil {
				t.Fatal("Heads did not install its fallback timer: ", err)
			}
			switch wakeup {
			case "deadline crossing":
				cl.Step(time.Nanosecond)
			case "fallback":
				cl.Step(time.Second)
			case "resource notification":
				m.QueueAssociatedInadmissibleWorkloadsAfter(ctx, workload.Key(wl), nil)
				requeuer.ProcessRequeues(ctx)
			case "cancel":
				cancel()
			}
			select {
			case got := <-done:
				if wakeup == "cancel" {
					if len(got) != 0 {
						t.Errorf("canceled Heads returned %v", got)
					}
				} else if len(got) != 1 || got[0].Obj.Name != "head" {
					t.Errorf("woken Heads() = %v, want head", got)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("Heads was not woken")
			}
		})
	}
}

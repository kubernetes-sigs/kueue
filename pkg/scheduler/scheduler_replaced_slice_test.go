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

package scheduler

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	corev1 "k8s.io/api/core/v1"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

const replacedSliceDeactivatedMsg = "The workload slice that this workload replaces is deactivated"

// TestNominateReplacedSliceDeactivated covers the deactivated-predecessor check at nomination.
// The predecessor is deactivated but not finished in every case.
// A replacement without a quota reservation must be held back.
// A replacement that already holds a reservation, for example one taking a second pass, must not be.
func TestNominateReplacedSliceDeactivated(t *testing.T) {
	cases := map[string]struct {
		reserved bool
		wantMsg  bool
	}{
		"replacement without a quota reservation is held back": {
			wantMsg: true,
		},
		"replacement that holds a quota reservation is not held back": {
			reserved: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlices, true)
			ctx, log := utiltesting.ContextWithLog(t)
			now := time.Now().Truncate(time.Second)

			admission := utiltestingapi.MakeAdmission("cq").PodSets(
				utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(corev1.ResourceCPU, "default", "1").Count(1).Obj(),
			).Obj()
			predecessor := utiltestingapi.MakeWorkload("old", "default").
				Queue("lq").
				Request(corev1.ResourceCPU, "1").
				ReserveQuotaAt(admission, now).
				Active(false).
				Obj()
			probeWrapper := utiltestingapi.MakeWorkload("new", "default").
				Queue("lq").
				Request(corev1.ResourceCPU, "1").
				Annotation(workloadslicing.WorkloadSliceReplacementFor, "default/old")
			if tc.reserved {
				probeWrapper = probeWrapper.ReserveQuotaAt(admission, now)
			}
			probe := probeWrapper.Obj()

			cl := utiltesting.NewClientBuilder().
				WithObjects(utiltesting.MakeNamespaceWrapper("default").Obj(), predecessor, probe).
				WithStatusSubresource(&kueue.Workload{}).
				Build()
			cqCache, _, scheduler := newSliceTestScheduler(ctx, t, cl, sliceTestClusterQueue().Obj(), nil, func(c *schdcache.Cache) *qcache.Manager {
				return qcache.NewManagerForUnitTests(cl, c, qcache.WithPreemptionExpectations(preemptexpectations.New()))
			}, WithClock(t, testingclock.NewFakeClock(now)))
			snapshot, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("Taking the snapshot: %v", err)
			}

			head := qcache.Head{Info: *workload.NewInfo(log, probe)}
			head.ClusterQueue = "cq"
			e, _ := scheduler.nominateWorkload(ctx, log, head, snapshot)

			if gotMsg := e.inadmissibleMsg == replacedSliceDeactivatedMsg; gotMsg != tc.wantMsg {
				t.Errorf("Unexpected deactivated-predecessor rejection: want %v, got %v (message %q)", tc.wantMsg, gotMsg, e.inadmissibleMsg)
			}
		})
	}
}

// TestNominateRetriesAfterReplacedSliceLookupError covers a failed read of the workload slice that a replacement replaces.
// The workload keeps its status, and is retried after replacedSliceLookupRetryDelay without any other event.
func TestNominateRetriesAfterReplacedSliceLookupError(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlices, true)
	synctest.Test(t, func(t *testing.T) {
		ctx, log := utiltesting.ContextWithLog(t)
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		cq := sliceTestClusterQueue().QueueingStrategy(kueue.BestEffortFIFO).Obj()
		lq := utiltestingapi.MakeLocalQueue("lq", "default").ClusterQueue("cq").Obj()
		probe := utiltestingapi.MakeWorkload("new", "default").
			Queue("lq").
			Request(corev1.ResourceCPU, "1").
			Annotation(workloadslicing.WorkloadSliceReplacementFor, "default/old").
			Obj()

		lookupErr := errors.New("injected read failure")
		cl := utiltesting.NewClientBuilder().
			WithLists(&kueue.WorkloadList{Items: []kueue.Workload{*probe}}, &kueue.LocalQueueList{Items: []kueue.LocalQueue{*lq}}).
			WithObjects(utiltesting.MakeNamespace("default"), utiltestingapi.MakeResourceFlavor("default").Obj(), cq).
			WithStatusSubresource(&kueue.Workload{}).
			WithInterceptorFuncs(interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if _, ok := obj.(*kueue.Workload); ok && key.Name == "old" {
						return lookupErr
					}
					return c.Get(ctx, key, obj, opts...)
				},
			}).
			Build()

		requeuer := qcache.NewRequeuer()
		cqCache, qManager, scheduler := newSliceTestScheduler(ctx, t, cl, cq, lq, func(c *schdcache.Cache) *qcache.Manager {
			return qcache.NewManager(cl, c, requeuer, qcache.WithPreemptionExpectations(preemptexpectations.New()))
		})
		done := make(chan error, 1)
		go func() { done <- requeuer.Start(ctx) }()
		defer func() {
			cancel()
			if err := <-done; err != nil {
				t.Errorf("Requeuer shutdown: %v", err)
			}
		}()
		// Flush the initial AddClusterQueue requeue notification before the cycle.
		synctest.Sleep(time.Second)

		heads := qManager.Heads(ctx)
		if len(heads) != 1 {
			t.Fatalf("Heads() returned %d workloads, want 1", len(heads))
		}
		snapshot, err := cqCache.Snapshot(ctx)
		if err != nil {
			t.Fatalf("Taking the snapshot: %v", err)
		}
		wantRetryAt := time.Now().Add(replacedSliceLookupRetryDelay)
		e, nominated := scheduler.nominateWorkload(ctx, log, heads[0], snapshot)
		if nominated {
			t.Fatal("The workload was nominated although its predecessor could not be read")
		}
		if e.inadmissibleMsg != lookupErr.Error() || !e.skipStatusUpdate {
			t.Errorf("Unexpected entry: message %q, skipStatusUpdate %v", e.inadmissibleMsg, e.skipStatusUpdate)
		}
		if e.retryAt == nil || !e.retryAt.Equal(wantRetryAt) {
			t.Fatalf("Unexpected retryAt: want %v, got %v", wantRetryAt, e.retryAt)
		}

		scheduler.requeueAndUpdate(ctx, e)
		if got := qManager.DumpInadmissible()["cq"]; len(got) != 1 {
			t.Fatalf("Expected the workload parked as inadmissible, got %v", got)
		}
		synctest.Sleep(replacedSliceLookupRetryDelay - time.Nanosecond)
		if got := qManager.Dump(); len(got) != 0 {
			t.Fatalf("The workload was retried before the delay elapsed: %v", got)
		}
		synctest.Sleep(time.Nanosecond)
		synctest.Wait()
		if got := qManager.Dump()["cq"]; len(got) != 1 {
			t.Errorf("The workload was not retried after the delay: active %v, inadmissible %v", qManager.Dump(), qManager.DumpInadmissible())
		}
	})
}

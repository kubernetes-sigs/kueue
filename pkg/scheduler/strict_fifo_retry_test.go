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
	"fmt"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	testingclock "k8s.io/utils/clock/testing"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	utilwait "sigs.k8s.io/kueue/pkg/util/wait"
	"sigs.k8s.io/kueue/pkg/workload"
)

// Heads logs an empty result while holding its condition lock. Taking that lock
// after this signal synchronizes with Heads entering its wait, for either strategy.
type emptyHeadsLogSink struct {
	logr.LogSink
	waiting chan struct{}
}

func (s *emptyHeadsLogSink) Enabled(level int) bool {
	return level == 3 || s.LogSink.Enabled(level)
}

func (s *emptyHeadsLogSink) Info(level int, message string, keysAndValues ...any) {
	if message == "Obtained ClusterQueue heads" {
		for i := 0; i+1 < len(keysAndValues); i += 2 {
			if keysAndValues[i] == "count" && keysAndValues[i+1] == 0 {
				select {
				case s.waiting <- struct{}{}:
				default:
				}
			}
		}
	}
	if s.LogSink.Enabled(level) {
		s.LogSink.Info(level, message, keysAndValues...)
	}
}

func (s *emptyHeadsLogSink) WithValues(keysAndValues ...any) logr.LogSink {
	return &emptyHeadsLogSink{LogSink: s.LogSink.WithValues(keysAndValues...), waiting: s.waiting}
}

func (s *emptyHeadsLogSink) WithName(name string) logr.LogSink {
	return &emptyHeadsLogSink{LogSink: s.LogSink.WithName(name), waiting: s.waiting}
}

// Exercise the scheduler's queue and backoff loop without external retry events.
func TestStrictFIFOStableNoFitSchedulingLoop(t *testing.T) {
	for _, strategy := range []kueue.QueueingStrategy{kueue.StrictFIFO, kueue.BestEffortFIFO} {
		t.Run(string(strategy), func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			waiting := make(chan struct{}, 1)
			log = log.WithSink(&emptyHeadsLogSink{LogSink: log.GetSink(), waiting: waiting})
			ctx = ctrl.LoggerInto(ctx, log)
			fakeClock := testingclock.NewFakeClock(time.Now())
			cq := utiltestingapi.MakeClusterQueue("cq").QueueingStrategy(strategy).ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "1").Obj()).Obj()
			lq := utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue(cq.Name).Obj()
			wl := utiltestingapi.MakeWorkload("too-large", "ns").Queue(kueue.LocalQueueName(lq.Name)).UID("workload-uid").Request(corev1.ResourceCPU, "2").Obj()
			cl := utiltesting.NewClientBuilder().WithObjects(cq, lq, wl,
				utiltesting.MakeNamespaceWrapper("ns").Obj()).WithStatusSubresource(&kueue.Workload{}).
				WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType).Build()
			cache := schdcache.New(cl)
			cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())
			if err := cache.AddClusterQueue(ctx, cq); err != nil {
				t.Fatal(err)
			}
			qm := qcache.NewManagerForUnitTests(cl, cache, qcache.WithClock(fakeClock), qcache.WithPreemptionExpectations(preemptexpectations.New()))
			if err := qm.AddLocalQueue(ctx, lq); err != nil {
				t.Fatal(err)
			}
			if err := qm.AddClusterQueue(ctx, cq); err != nil {
				t.Fatal(err)
			}
			if err := qm.AddOrUpdateWorkload(ctx, log, wl); err != nil {
				t.Fatal(err)
			}
			recorder := &utiltesting.EventRecorder{}
			s := New(qm, cache, cl, recorder, WithClock(t, fakeClock))
			// The real deadline only guards against a hung loop; queue retry time
			// remains frozen until the first scheduling attempts have completed.
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			go qm.CleanUpOnContext(ctx)
			attempts := 0
			firstAttempt := make(chan struct{})
			done := make(chan struct{})
			go func() {
				defer close(done)
				utilwait.UntilWithBackoff(ctx, func(ctx context.Context) utilwait.SpeedSignal {
					signal := s.schedule(ctx)
					if ctx.Err() == nil {
						attempts++
						if attempts == 1 {
							close(firstAttempt)
						}
						if attempts > 2 {
							cancel()
						}
					}
					return signal
				})
			}()
			defer func() {
				cancel()
				<-done
			}()
			for _, phase := range []struct {
				name    string
				reached <-chan struct{}
			}{
				{name: "first completed attempt", reached: firstAttempt},
				{name: "empty Heads wait", reached: waiting},
			} {
				select {
				case <-phase.reached:
				case <-done:
					t.Fatalf("scheduling loop stopped before %s after %d attempts", phase.name, attempts)
				case <-ctx.Done():
					t.Fatalf("waiting for %s: %v", phase.name, ctx.Err())
				}
			}
			qm.Lock()
			if strategy == kueue.StrictFIFO && !fakeClock.HasWaiters() {
				t.Error("Heads did not install its NoFit fallback timer")
			}
			// Reaching the wait while retry time is frozen proves that unchanged
			// NoFit is no longer cycling. Queue tests cover deadline expiry.
			qm.Unlock()
			cancel()
			<-done
			if ctx.Err() != context.Canceled {
				t.Fatalf("scheduling loop exceeded its watchdog deadline: %v", ctx.Err())
			}
			if attempts == 0 || attempts > 2 {
				t.Errorf("unchanged NoFit attempted %d times, want 1 or at most 2 flavor passes", attempts)
			}
			if len(recorder.RecordedEvents) != 1 {
				t.Errorf("Pending events = %d, want 1", len(recorder.RecordedEvents))
			}
		})
	}
}

// A timer retry must still run fit checks, without publishing an identical
// Pending event. A new diagnosis must remain visible with either patch mode.
func TestNoFitPendingEventsFollowConditionChanges(t *testing.T) {
	for _, mergePatch := range []bool{false, true} {
		t.Run(fmt.Sprintf("mergePatch=%t", mergePatch), func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.WorkloadRequestUseMergePatch, mergePatch)
			features.SetFeatureGateDuringTest(t, features.UnadmittedWorkloadsObservability, true)
			ctx, log := utiltesting.ContextWithLog(t)
			wl := utiltestingapi.MakeWorkload("blocked", "ns").UID("blocked-uid").Obj()
			cl := utiltesting.NewClientBuilder().WithObjects(wl).WithStatusSubresource(wl).Build()
			cache := schdcache.New(cl)
			qm := qcache.NewManagerForUnitTests(cl, cache)
			recorder := &utiltesting.EventRecorder{}
			s := New(qm, cache, cl, recorder)
			for _, step := range []struct {
				message string
				reason  string
				count   int
			}{
				{message: "insufficient cpu", reason: kueue.WorkloadQuotaReservedReasonWaitingForQuota, count: 1},
				{message: "insufficient cpu", reason: kueue.WorkloadQuotaReservedReasonWaitingForQuota, count: 1},
				{message: "insufficient memory", reason: kueue.WorkloadQuotaReservedReasonWaitingForQuota, count: 2},
				{message: "insufficient memory", reason: kueue.WorkloadQuotaReservedReasonWaitingForQuota, count: 2},
				{message: "insufficient memory", reason: kueue.PreemptionGated, count: 3},
				{message: "insufficient memory", reason: kueue.PreemptionGated, count: 3},
			} {
				if err := cl.Get(ctx, client.ObjectKeyFromObject(wl), wl); err != nil {
					t.Fatal(err)
				}
				e := entry{
					Info:                *workload.NewInfo(log, wl),
					status:              notNominated,
					requeueReason:       qcache.RequeueReasonNoFit,
					quotaReservedReason: step.reason,
					inadmissibleMsg:     step.message,
				}
				s.requeueAndUpdate(ctx, e)
				if err := cl.Get(ctx, client.ObjectKeyFromObject(wl), wl); err != nil {
					t.Fatal(err)
				}
				condition := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
				if condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != step.reason || condition.Message != step.message {
					t.Fatalf("after %q / %q: QuotaReserved = %#v, want False with matching reason and message", step.reason, step.message, condition)
				}
				if got := len(recorder.RecordedEvents); got != step.count {
					t.Fatalf("after %q / %q: events = %d, want %d", step.reason, step.message, got, step.count)
				}
			}
		})
	}
}

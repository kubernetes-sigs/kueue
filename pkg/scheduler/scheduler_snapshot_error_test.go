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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	"sigs.k8s.io/kueue/pkg/util/routine"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

// snapshotErrorFixture is a scheduler whose snapshot can be made to fail on
// demand, through the LocalQueue lookup that AdmissionFairSharing performs.
type snapshotErrorFixture struct {
	ctx               context.Context
	cl                client.Client
	qManager          *qcache.Manager
	scheduler         *Scheduler
	fakeClock         *testingclock.FakeClock
	failLocalQueueGet *atomic.Bool
	wg                *sync.WaitGroup
	// pending is the regular head sitting in the ClusterQueue.
	pending kueue.Workload
}

func newSnapshotErrorFixture(t *testing.T, now time.Time, extraWorkloads ...kueue.Workload) *snapshotErrorFixture {
	t.Helper()
	features.SetFeatureGateDuringTest(t, features.AdmissionFairSharing, true)
	afsConfig := &config.AdmissionFairSharing{
		UsageHalfLifeTime:     metav1.Duration{Duration: 10 * time.Second},
		UsageSamplingInterval: metav1.Duration{Duration: time.Second},
	}
	fakeClock := testingclock.NewFakeClock(now)

	flavor := *utiltestingapi.MakeResourceFlavor("default").Obj()
	cq := *utiltestingapi.MakeClusterQueue("cq").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
			Resource(corev1.ResourceCPU, "8").Obj()).
		AdmissionMode(kueue.UsageBasedAdmissionFairSharing).
		Obj()
	lq := *utiltestingapi.MakeLocalQueue("lq", "default").ClusterQueue("cq").Obj()

	// The admitted workload is what makes the snapshot read the LocalQueue.
	admitted := *utiltestingapi.MakeWorkload("admitted", "default").
		Queue("lq").
		PodSets(*utiltestingapi.MakePodSet("one", 1).
			Request(corev1.ResourceCPU, "1").Obj()).
		ReserveQuotaAt(
			utiltestingapi.MakeAdmission("cq").
				PodSets(utiltestingapi.MakePodSetAssignment("one").
					Assignment(corev1.ResourceCPU, "default", "1").
					Obj()).
				Obj(), now).
		Obj()
	pending := *utiltestingapi.MakeWorkload("pending", "default").
		Queue("lq").
		PodSets(*utiltestingapi.MakePodSet("one", 1).
			Request(corev1.ResourceCPU, "1").Obj()).
		Creation(now).
		Obj()

	ctx, log := utiltesting.ContextWithLog(t)

	// The snapshot resolves the LocalQueue weight through the client, so failing
	// that Get fails the snapshot.
	var failLocalQueueGet atomic.Bool
	failLocalQueueGet.Store(true)
	cl := utiltesting.NewClientBuilder().
		WithLists(
			&kueue.WorkloadList{Items: append([]kueue.Workload{admitted, pending}, extraWorkloads...)},
			&kueue.ClusterQueueList{Items: []kueue.ClusterQueue{cq}},
			&kueue.LocalQueueList{Items: []kueue.LocalQueue{lq}}).
		WithObjects(utiltesting.MakeNamespace("default")).
		WithStatusSubresource(&kueue.Workload{}).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge,
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, isLocalQueue := obj.(*kueue.LocalQueue); isLocalQueue && failLocalQueueGet.Load() {
					return errors.New("injected LocalQueue get failure")
				}
				return cl.Get(ctx, key, obj, opts...)
			},
		}).
		Build()

	recorder := &utiltesting.EventRecorder{}
	cqCache := schdcache.New(cl, schdcache.WithFairSharing(true), schdcache.WithAdmissionFairSharing(afsConfig))
	qManager := qcache.NewManagerForUnitTests(cl, cqCache, qcache.WithClock(fakeClock), qcache.WithAdmissionFairSharing(afsConfig))

	cqCache.AddOrUpdateResourceFlavor(log, &flavor)
	if err := cqCache.AddClusterQueue(ctx, &cq); err != nil {
		t.Fatalf("Inserting clusterQueue in cache: %v", err)
	}
	if err := qManager.AddClusterQueue(ctx, &cq); err != nil {
		t.Fatalf("Inserting clusterQueue in manager: %v", err)
	}
	if err := qManager.AddLocalQueue(ctx, &lq); err != nil {
		t.Fatalf("Inserting localQueue in manager: %v", err)
	}
	cqCache.AddOrUpdateWorkload(log, &admitted)

	scheduler := New(qManager, cqCache, cl, recorder,
		WithFairSharing(&config.FairSharing{}),
		WithAdmissionFairSharing(afsConfig),
		WithClock(t, fakeClock),
		WithPreemptionExpectations(preemptexpectations.New()))
	wg := &sync.WaitGroup{}
	scheduler.setAdmissionRoutineWrapper(routine.NewWrapper(
		func() { wg.Add(1) },
		func() { wg.Done() },
	))

	schedCtx, cancel := context.WithTimeout(ctx, queueingTimeout)
	t.Cleanup(cancel)
	go qManager.CleanUpOnContext(schedCtx)

	return &snapshotErrorFixture{
		ctx:               schedCtx,
		cl:                cl,
		qManager:          qManager,
		scheduler:         scheduler,
		fakeClock:         fakeClock,
		failLocalQueueGet: &failLocalQueueGet,
		wg:                wg,
		pending:           pending,
	}
}

func (f *snapshotErrorFixture) runCycle() {
	f.scheduler.schedule(f.ctx)
	f.wg.Wait()
}

// TestScheduleSnapshotErrorRequeuesHeads covers a cycle whose snapshot fails:
// without the requeue, the popped heads never return to the ClusterQueue.
func TestScheduleSnapshotErrorRequeuesHeads(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	f := newSnapshotErrorFixture(t, now)

	f.runCycle()

	wantLeft := map[kueue.ClusterQueueReference][]workload.Reference{
		"cq": {workload.Key(&f.pending)},
	}
	if diff := cmp.Diff(wantLeft, f.qManager.Dump()); diff != "" {
		t.Errorf("Unexpected workloads left in the ClusterQueue after a failed snapshot (-want,+got):\n%s", diff)
	}

	// The next cycle, once the snapshot succeeds again, must admit the head.
	f.failLocalQueueGet.Store(false)
	f.runCycle()

	var gotPending kueue.Workload
	if err := f.cl.Get(f.ctx, client.ObjectKeyFromObject(&f.pending), &gotPending); err != nil {
		t.Fatalf("Failed obtaining the workload: %v", err)
	}
	if !workload.HasQuotaReservation(&gotPending) {
		t.Errorf("Expected the workload to get quota reserved in the cycle following the snapshot failure")
	}
}

// TestScheduleSnapshotErrorRequeuesSecondPassHeads covers the second-pass
// population of the same error path, which returns after a backoff step rather
// than immediately.
func TestScheduleSnapshotErrorRequeuesSecondPassHeads(t *testing.T) {
	now := time.Now().Truncate(time.Second)

	secondPass := *utiltestingapi.MakeWorkload("second-pass", "default").
		Queue("lq").
		PodSets(*utiltestingapi.MakePodSet("one", 1).
			RequiredTopologyRequest(corev1.LabelHostname).
			Request(corev1.ResourceCPU, "1").Obj()).
		ReserveQuotaAt(
			utiltestingapi.MakeAdmission("cq").
				PodSets(utiltestingapi.MakePodSetAssignment("one").
					Assignment(corev1.ResourceCPU, "default", "1").
					DelayedTopologyRequest(kueue.DelayedTopologyRequestStatePending).
					Obj()).
				Obj(), now).
		AdmissionCheck(kueue.AdmissionCheckState{Name: "check", State: kueue.CheckStateReady}).
		Obj()

	f := newSnapshotErrorFixture(t, now, secondPass)

	// Let the initial backoff elapse, so the failing cycle drains it.
	if !f.qManager.QueueSecondPassIfNeeded(f.ctx, &secondPass, 0) {
		t.Fatalf("Failed queueing the workload for the second pass")
	}
	f.fakeClock.Step(time.Second)

	f.runCycle()

	// Without advancing the clock only the regular head is back.
	gotHeads := headNames(f.qManager.Heads(f.ctx))
	if diff := cmp.Diff(sets.New(workload.Key(&f.pending)), gotHeads); diff != "" {
		t.Errorf("Unexpected heads right after the failed snapshot (-want,+got):\n%s", diff)
	}

	// After the backoff step it returns, having consumed one iteration.
	f.fakeClock.Step(2 * time.Second)
	heads := f.qManager.Heads(f.ctx)
	if diff := cmp.Diff(sets.New(workload.Key(&secondPass)), headNames(heads)); diff != "" {
		t.Fatalf("Unexpected heads after the second pass backoff (-want,+got):\n%s", diff)
	}
	if got := heads[0].SecondPassIteration; got != 2 {
		t.Errorf("Unexpected second pass iteration: want 2, got %d", got)
	}
}

func headNames(heads []qcache.Head) sets.Set[workload.Reference] {
	names := sets.New[workload.Reference]()
	for _, h := range heads {
		names.Insert(workload.Key(h.Obj))
	}
	return names
}

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

// TestScheduleSnapshotErrorRequeuesHeads covers a cycle whose snapshot fails:
// without the requeue, the popped heads never return to the ClusterQueue.
func TestScheduleSnapshotErrorRequeuesHeads(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.AdmissionFairSharing, true)
	afsConfig := &config.AdmissionFairSharing{
		UsageHalfLifeTime:     metav1.Duration{Duration: 10 * time.Second},
		UsageSamplingInterval: metav1.Duration{Duration: time.Second},
	}
	now := time.Now().Truncate(time.Second)
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
			&kueue.WorkloadList{Items: []kueue.Workload{admitted, pending}},
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
	wg := sync.WaitGroup{}
	scheduler.setAdmissionRoutineWrapper(routine.NewWrapper(
		func() { wg.Add(1) },
		func() { wg.Done() },
	))

	schedCtx, cancel := context.WithTimeout(ctx, queueingTimeout)
	defer cancel()
	go qManager.CleanUpOnContext(schedCtx)

	scheduler.schedule(schedCtx)
	wg.Wait()

	wantLeft := map[kueue.ClusterQueueReference][]workload.Reference{
		"cq": {workload.Key(&pending)},
	}
	if diff := cmp.Diff(wantLeft, qManager.Dump()); diff != "" {
		t.Errorf("Unexpected workloads left in the ClusterQueue after a failed snapshot (-want,+got):\n%s", diff)
	}

	// The next cycle, once the snapshot succeeds again, must admit the head.
	failLocalQueueGet.Store(false)
	scheduler.schedule(schedCtx)
	wg.Wait()

	var gotPending kueue.Workload
	if err := cl.Get(ctx, client.ObjectKeyFromObject(&pending), &gotPending); err != nil {
		t.Fatalf("Failed obtaining the workload: %v", err)
	}
	if !workload.HasQuotaReservation(&gotPending) {
		t.Errorf("Expected the workload to get quota reserved in the cycle following the snapshot failure")
	}
}

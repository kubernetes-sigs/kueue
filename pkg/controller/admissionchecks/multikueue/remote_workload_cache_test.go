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

package multikueue

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobs"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
)

func TestRemoteWorkloadCacheGet(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	key := types.NamespacedName{Name: "wl1", Namespace: TestNamespace}
	cachedWl := utiltestingapi.MakeWorkload("wl1", TestNamespace).ResourceVersion("10").Obj()

	cases := map[string]struct {
		cached     []client.Object
		notSynced  bool
		readErr    error
		pending    *pendingRemoteWrite
		elapsed    time.Duration
		wantServed bool
		wantFound  bool
		// wantPending is whether the pending write is still recorded after the read.
		wantPending bool
	}{
		"not served before the informer is synced": {
			cached:    []client.Object{cachedWl},
			notSynced: true,
		},
		"served from the cache": {
			cached:     []client.Object{cachedWl},
			wantServed: true,
			wantFound:  true,
		},
		"a missing workload is served as not found": {
			wantServed: true,
		},
		"not served when the cache read fails": {
			cached:  []client.Object{cachedWl},
			readErr: errFake,
		},
		"not served before the cache observed a write": {
			cached:      []client.Object{cachedWl},
			pending:     &pendingRemoteWrite{resourceVersion: "11"},
			wantPending: true,
		},
		"served once the cache observed a write": {
			cached:     []client.Object{cachedWl},
			pending:    &pendingRemoteWrite{resourceVersion: "10"},
			wantServed: true,
			wantFound:  true,
		},
		"not served when the cache still has a deleted workload": {
			cached:      []client.Object{cachedWl},
			pending:     &pendingRemoteWrite{deleted: true},
			wantPending: true,
		},
		"served once the cache observed a delete": {
			pending:    &pendingRemoteWrite{deleted: true},
			wantServed: true,
		},
		"a workload with a deletion timestamp is an observed delete": {
			cached: []client.Object{utiltestingapi.MakeWorkload("wl1", TestNamespace).ResourceVersion("10").
				Finalizers(kueue.ResourceInUseFinalizerName).DeletionTimestamp(now).Obj()},
			pending:    &pendingRemoteWrite{deleted: true},
			wantServed: true,
			wantFound:  true,
		},
		"not served after a write of unknown outcome": {
			cached:      []client.Object{cachedWl},
			pending:     &pendingRemoteWrite{},
			wantPending: true,
		},
		"served once a pending write expires": {
			cached:     []client.Object{cachedWl},
			pending:    &pendingRemoteWrite{},
			elapsed:    remoteWorkloadWriteTTL,
			wantServed: true,
			wantFound:  true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			fakeClock := testingclock.NewFakeClock(now)
			reader := getClientBuilder(ctx).WithObjects(tc.cached...).WithInterceptorFuncs(interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if tc.readErr != nil {
						return tc.readErr
					}
					return c.Get(ctx, key, obj, opts...)
				},
			}).Build()
			c := newRemoteWorkloadCache(reader, func() bool { return !tc.notSynced }, fakeClock)
			if tc.pending != nil {
				c.recordWrite(key, *tc.pending)
			}
			fakeClock.Step(tc.elapsed)

			wl := &kueue.Workload{}
			served, err := c.get(ctx, key, wl)
			if served != tc.wantServed {
				t.Errorf("served = %t, want %t", served, tc.wantServed)
			}
			if served {
				if gotFound := err == nil; gotFound != tc.wantFound {
					t.Errorf("found = %t (err %v), want %t", gotFound, err, tc.wantFound)
				}
				if err != nil && !apierrors.IsNotFound(err) {
					t.Errorf("unexpected error: %v", err)
				}
			} else if err != nil {
				t.Errorf("unexpected error when not served: %v", err)
			}
			if _, gotPending := c.pending[key]; gotPending != tc.wantPending {
				t.Errorf("pending = %t, want %t", gotPending, tc.wantPending)
			}
		})
	}
}

func TestWorkloadWriteRecordingClient(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	key := types.NamespacedName{Name: "wl1", Namespace: TestNamespace}
	fakeClock := testingclock.NewFakeClock(time.Now())
	worker := NewNeverCachingClient(getClientBuilder(ctx).WithStatusSubresource(&kueue.Workload{}).Build())
	wlCache := newRemoteWorkloadCache(worker, func() bool { return true }, fakeClock)
	c := &workloadWriteRecordingClient{SelectivelyCachingClient: worker, cache: wlCache}

	wl := utiltestingapi.MakeWorkload("wl1", TestNamespace).Obj()
	if err := c.Create(ctx, wl); err != nil {
		t.Fatalf("creating workload: %v", err)
	}
	if diff := cmp.Diff(wl.ResourceVersion, wlCache.pending[key].resourceVersion); diff != "" {
		t.Errorf("unexpected pending resource version after create (-want/+got):\n%s", diff)
	}

	wl.Status.Conditions = []metav1.Condition{{Type: kueue.WorkloadQuotaReserved, Status: metav1.ConditionTrue, Reason: "ByTest", LastTransitionTime: metav1.Now()}}
	if err := c.Status().Update(ctx, wl); err != nil {
		t.Fatalf("updating workload status: %v", err)
	}
	if diff := cmp.Diff(wl.ResourceVersion, wlCache.pending[key].resourceVersion); diff != "" {
		t.Errorf("unexpected pending resource version after status update (-want/+got):\n%s", diff)
	}

	stale := wl.DeepCopy()
	stale.ResourceVersion = "1"
	if err := c.Update(ctx, stale); !apierrors.IsConflict(err) {
		t.Fatalf("expected a conflict, got %v", err)
	}
	if diff := cmp.Diff(pendingRemoteWrite{}, wlCache.pending[key], cmp.AllowUnexported(pendingRemoteWrite{}), cmp.FilterPath(func(p cmp.Path) bool {
		return p.Last().String() == ".expires"
	}, cmp.Ignore())); diff != "" {
		t.Errorf("a failed write must be recorded with an unknown outcome (-want/+got):\n%s", diff)
	}

	if err := c.Delete(ctx, wl); err != nil {
		t.Fatalf("deleting workload: %v", err)
	}
	if !wlCache.pending[key].deleted {
		t.Errorf("expected a pending delete, got %+v", wlCache.pending[key])
	}

	job := testingjob.MakeJob("job1", TestNamespace).Obj()
	if err := c.Create(ctx, job); err != nil {
		t.Fatalf("creating job: %v", err)
	}
	if _, found := wlCache.pending[types.NamespacedName{Name: "job1", Namespace: TestNamespace}]; found {
		t.Error("a job write must not be recorded")
	}
}

// TestWlReconcileConfirmsStaleCacheLive checks that a remote workload cache lagging behind the
// worker cannot cause a decision that changes the local admission state.
func TestWlReconcileConfirmsStaleCacheLive(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	fakeClock := testingclock.NewFakeClock(now)
	features.SetFeatureGateDuringTest(t, features.MultiKueueRemoteWorkloadCache, true)
	features.SetFeatureGateDuringTest(t, features.WorkloadIdentifierAnnotations, false)

	baseWorkloadBuilder := utiltestingapi.MakeWorkload("wl1", TestNamespace)
	localWl := baseWorkloadBuilder.Clone().
		AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStateReady, Message: `The workload was admitted on "worker1"`}).
		ClusterName("worker1").
		ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
		ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
		Obj()
	admittedRemote := baseWorkloadBuilder.Clone().
		Label(kueue.MultiKueueOriginLabel, defaultOrigin).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
		AdmittedAt(true, now).
		Obj()
	remoteJob := testingjob.MakeJob("job1", TestNamespace).Suspend(false).
		Label(kueue.MultiKueueOriginLabel, defaultOrigin).
		PrebuiltWorkloadLabel("wl1").
		Obj()

	cases := map[string]struct {
		cachedWorkloads []kueue.Workload
	}{
		"the cache does not have the admitted remote yet": {},
		"the cache has a finished copy of the remote": {
			cachedWorkloads: []kueue.Workload{*baseWorkloadBuilder.Clone().
				Label(kueue.MultiKueueOriginLabel, defaultOrigin).
				Condition(metav1.Condition{Type: kueue.WorkloadFinished, Status: metav1.ConditionTrue, Reason: "ByTest", Message: "by test"}).
				Obj()},
		},
		"the cache has an evicted copy of the remote": {
			cachedWorkloads: []kueue.Workload{*baseWorkloadBuilder.Clone().
				Label(kueue.MultiKueueOriginLabel, defaultOrigin).
				Condition(metav1.Condition{Type: kueue.WorkloadEvicted, Status: metav1.ConditionTrue, Reason: "ByTest", Message: "by test"}).
				Obj()},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			managerClient := getClientBuilder(ctx).
				WithInterceptorFuncs(interceptor.Funcs{SubResourceApply: utiltesting.TreatSSAAsStrategicMergeForApplyConfiguration}).
				WithLists(&kueue.WorkloadList{Items: []kueue.Workload{*localWl.DeepCopy()}},
					&batchv1.JobList{Items: []batchv1.Job{*testingjob.MakeJob("job1", TestNamespace).Suspend(false).ManagedBy(kueue.MultiKueueControllerName).Obj()}}).
				WithStatusSubresource(&kueue.Workload{}, &batchv1.Job{}).
				WithObjects(
					utiltestingapi.MakeMultiKueueConfig("config1").Clusters("worker1").Obj(),
					utiltestingapi.MakeAdmissionCheck("ac1").ControllerName(kueue.MultiKueueControllerName).
						Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "config1").Obj(),
				).
				Build()
			adapters, _ := jobs.NewIntegrationManager().GetMultiKueueAdapters(sets.New("batch/job"))
			cRec := newClustersReconciler(managerClient, TestNamespace, withAdapters(adapters))

			worker := NewNeverCachingClient(getClientBuilder(ctx).
				WithLists(&kueue.WorkloadList{Items: []kueue.Workload{*admittedRemote.DeepCopy()}}, &batchv1.JobList{Items: []batchv1.Job{*remoteJob.DeepCopy()}}).
				WithStatusSubresource(&kueue.Workload{}, &batchv1.Job{}).
				WithInterceptorFuncs(interceptor.Funcs{SubResourceApply: utiltesting.TreatSSAAsStrategicMergeForApplyConfiguration}).
				Build())
			staleReader := getClientBuilder(ctx).WithLists(&kueue.WorkloadList{Items: tc.cachedWorkloads}).Build()
			rc := newRemoteClient(managerClient, nil, nil, nil, defaultOrigin, "worker1", adapters)
			rc.wlCache = newRemoteWorkloadCache(staleReader, func() bool { return true }, fakeClock)
			rc.client = &workloadWriteRecordingClient{SelectivelyCachingClient: worker, cache: rc.wlCache}
			rc.connState.markConnected()
			cRec.remoteClients["worker1"] = rc

			helper, _ := admissioncheck.NewMultiKueueStoreHelper(managerClient)
			reconciler := newWlReconciler(managerClient, helper, cRec, defaultOrigin, &utiltesting.EventRecorder{}, defaultWorkerLostTimeout, time.Second,
				adapters, config.MultiKueueDispatcherModeAllAtOnce, nil, WithClock(t, fakeClock))

			if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(localWl)}); err != nil {
				t.Fatalf("unexpected reconcile error: %v", err)
			}

			got := &kueue.Workload{}
			if err := managerClient.Get(ctx, client.ObjectKeyFromObject(localWl), got); err != nil {
				t.Fatalf("reading the local workload: %v", err)
			}
			if acs := admissioncheck.FindAdmissionCheck(got.Status.AdmissionChecks, "ac1"); acs == nil || acs.State != kueue.CheckStateReady {
				t.Errorf("expected the admission check to stay Ready, got %+v", acs)
			}
			if apimeta.IsStatusConditionTrue(got.Status.Conditions, kueue.WorkloadFinished) {
				t.Error("the local workload must not be finished from a stale cache")
			}
			remote := &kueue.Workload{}
			if err := worker.Get(ctx, client.ObjectKeyFromObject(localWl), remote); err != nil {
				t.Errorf("the admitted remote workload must be kept: %v", err)
			}
		})
	}
}

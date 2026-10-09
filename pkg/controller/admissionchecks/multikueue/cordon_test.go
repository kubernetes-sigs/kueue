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
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
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

func TestCordonDispatch(t *testing.T) {
	cases := map[string]struct {
		mode       string
		gate       bool
		existing   bool
		wantRemote bool
	}{
		"all at once skips cordoned cluster":        {mode: config.MultiKueueDispatcherModeAllAtOnce, gate: true},
		"incremental skips stale nomination":        {mode: config.MultiKueueDispatcherModeIncremental, gate: true},
		"external skips cordoned nomination":        {mode: "example.com/dispatcher", gate: true},
		"all at once preserves dispatched workload": {mode: config.MultiKueueDispatcherModeAllAtOnce, gate: true, existing: true, wantRemote: true},
		"incremental preserves dispatched workload": {mode: config.MultiKueueDispatcherModeIncremental, gate: true, existing: true, wantRemote: true},
		"external preserves dispatched workload":    {mode: "example.com/dispatcher", gate: true, existing: true, wantRemote: true},
		"disabled feature gate preserves dispatch":  {mode: config.MultiKueueDispatcherModeAllAtOnce, wantRemote: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.MultiKueueClusterCordon, tc.gate)
			features.SetFeatureGateDuringTest(t, features.WorkloadIdentifierAnnotations, false)
			ctx, _ := utiltesting.ContextWithLog(t)
			now := time.Now().Truncate(time.Second)
			wl := utiltestingapi.MakeWorkload("wl", TestNamespace).
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job", "uid").
				AdmissionCheck(kueue.AdmissionCheckState{Name: "ac", State: kueue.CheckStatePending}).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).
				NominatedClusterNames("worker1", "worker2").Obj()
			job := testingjob.MakeJob("job", TestNamespace).ManagedBy(kueue.MultiKueueControllerName).Obj()
			cluster := utiltestingapi.MakeMultiKueueCluster("worker1").Unschedulable(true).Obj()
			manager := getClientBuilder(ctx).WithObjects(wl, job, cluster,
				utiltestingapi.MakeMultiKueueCluster("worker2").Obj(),
				utiltestingapi.MakeMultiKueueConfig("config").Clusters("worker1", "worker2").Obj(),
				utiltestingapi.MakeAdmissionCheck("ac").ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "config").Obj()).
				WithStatusSubresource(&kueue.Workload{}).
				WithInterceptorFuncs(interceptor.Funcs{SubResourceApply: utiltesting.TreatSSAAsStrategicMergeForApplyConfiguration}).Build()
			worker1Builder := getClientBuilder(ctx)
			if tc.existing {
				worker1Builder = worker1Builder.WithObjects(cloneForCreate(wl, defaultOrigin, true))
			}
			worker1 := NewNeverCachingClient(worker1Builder.Build())
			worker2 := NewNeverCachingClient(getClientBuilder(ctx).Build())
			adapters, err := jobs.NewIntegrationManager().GetMultiKueueAdapters(sets.New("batch/job"))
			if err != nil {
				t.Fatal(err)
			}
			clusters := newClustersReconciler(manager, TestNamespace, withAdapters(adapters))
			for name, worker := range map[string]SelectivelyCachingClient{"worker1": worker1, "worker2": worker2} {
				remote := newRemoteClient(manager, nil, nil, nil, defaultOrigin, "", adapters)
				remote.client = worker
				remote.connState.markConnected()
				clusters.remoteClients[name] = remote
			}
			helper, err := admissioncheck.NewMultiKueueStoreHelper(manager)
			if err != nil {
				t.Fatal(err)
			}
			reconciler := newWlReconciler(manager, helper, clusters, defaultOrigin, &utiltesting.EventRecorder{}, defaultWorkerLostTimeout, 0, adapters, tc.mode, nil)
			req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(wl)}
			if _, err := reconciler.Reconcile(ctx, req); err != nil {
				t.Fatal(err)
			}
			got := &kueue.Workload{}
			err = worker1.Get(ctx, req.NamespacedName, got)
			if tc.wantRemote && err != nil {
				t.Fatalf("existing or permitted workload missing: %v", err)
			}
			if !tc.wantRemote && !apierrors.IsNotFound(err) {
				t.Fatal("new workload dispatched to cordoned worker")
			}
			if err := worker2.Get(ctx, req.NamespacedName, &kueue.Workload{}); err != nil {
				t.Fatalf("schedulable worker did not receive workload: %v", err)
			}

			// Uncordoning permits the same waiting workload to be dispatched.
			before := cluster.DeepCopy()
			cluster.Spec.Unschedulable = new(false)
			if err := manager.Patch(ctx, cluster, client.MergeFrom(before)); err != nil {
				t.Fatal(err)
			}
			if _, err := reconciler.Reconcile(ctx, req); err != nil {
				t.Fatal(err)
			}
			if err := worker1.Get(ctx, req.NamespacedName, &kueue.Workload{}); err != nil {
				t.Fatalf("workload not dispatched after uncordon: %v", err)
			}
		})
	}
}

func TestCordonPreservesAdmissionAndCleanup(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.MultiKueueClusterCordon, true)
	features.SetFeatureGateDuringTest(t, features.WorkloadIdentifierAnnotations, false)
	ctx, _ := utiltesting.ContextWithLog(t)
	r := setupAdmittedMetricTest(ctx, t, kueue.CheckStatePending)
	if err := r.client.Create(ctx, utiltestingapi.MakeMultiKueueCluster("worker1").Unschedulable(true).Obj()); err != nil {
		t.Fatal(err)
	}
	req := reconcile.Request{Name: "wl1", Namespace: TestNamespace}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	wl := &kueue.Workload{}
	if err := r.client.Get(ctx, req.NamespacedName, wl); err != nil {
		t.Fatal(err)
	}
	if got := wl.Status.AdmissionChecks[0].State; got != kueue.CheckStateReady {
		t.Fatalf("admission stopped after cordon: %s", got)
	}
	remote := r.clusters.remoteClients["worker1"].getClient()
	if err := remote.Get(ctx, req.NamespacedName, &kueue.Workload{}); err != nil {
		t.Fatal(err)
	}
	wl.Status.Conditions = append(wl.Status.Conditions, metav1.Condition{
		Type:               kueue.WorkloadFinished,
		Status:             metav1.ConditionTrue,
		Reason:             "Succeeded",
		Message:            "Job finished",
		LastTransitionTime: metav1.Now(),
	})
	if err := r.client.Status().Update(ctx, wl); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := remote.Get(ctx, req.NamespacedName, &kueue.Workload{}); client.IgnoreNotFound(err) != nil || err == nil {
		t.Fatalf("cordoned remote workload was not cleaned up: %v", err)
	}
}

func TestCordonSingleClusterDispatch(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	worker := NewNeverCachingClient(getClientBuilder(ctx).Build())
	group := &wlGroup{
		local:                 utiltestingapi.MakeWorkload("wl", TestNamespace).Obj(),
		remotes:               map[string]*kueue.Workload{"worker1": nil},
		remoteClients:         map[string]*remoteClient{"worker1": {client: worker, origin: defaultOrigin}},
		unschedulableClusters: sets.New("worker1"),
	}
	r := &wlReconciler{}
	if _, err := r.syncToSingleCluster(ctx, log, group, "worker1"); err != nil {
		t.Fatal(err)
	}
	got := &kueue.WorkloadList{}
	if err := worker.List(ctx, got); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(0, len(got.Items)); diff != "" {
		t.Fatalf("dispatched to cordoned pinned worker: %s", diff)
	}
}

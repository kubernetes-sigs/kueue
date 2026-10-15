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

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingray "sigs.k8s.io/kueue/pkg/util/testingjobs/raycluster"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

// TestEvictedSliceCleanup covers replacement finishing before eviction cleanup.
func TestEvictedSliceCleanup(t *testing.T) {
	cases := map[string]struct {
		replacedBeforeCleanup bool
		holdDeletion          bool
		wantRemoteExists      bool
	}{
		"cleanup observes quota release before replacement": {},
		"terminating remote blocks cleanup":                 {replacedBeforeCleanup: true, holdDeletion: true, wantRemoteExists: true},
		"replacement finishes evicted slice before cleanup": {
			replacedBeforeCleanup: true,
			wantRemoteExists:      false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlices, true)
			ctx, _ := utiltesting.ContextWithLog(t)
			key := types.NamespacedName{Name: "ray", Namespace: TestNamespace}
			gvk := rayv1.SchemeGroupVersion.WithKind("RayCluster")
			local := utiltestingapi.MakeWorkload("old-slice", TestNamespace).
				Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				ControllerReference(gvk, key.Name, "manager-uid").
				PodSets(kueue.PodSet{Name: "head", Count: 1}, kueue.PodSet{Name: "gpu-group", Count: 2}).
				AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStateReady}).
				Condition(metav1.Condition{Type: kueue.WorkloadEvicted, Status: metav1.ConditionTrue,
					Reason: kueue.WorkloadEvictedByPreemption, Message: "reclamation within the cohort"}).
				Obj()
			remoteWl := local.DeepCopy()
			remoteWl.Status.Conditions = []metav1.Condition{{Type: kueue.WorkloadAdmitted,
				Status: metav1.ConditionTrue, Reason: kueue.WorkloadAdmitted}}
			remoteWl.Labels = map[string]string{kueue.MultiKueueOriginLabel: defaultOrigin}
			remoteRay := testingray.MakeCluster(key.Name, key.Namespace).
				Suspend(true).Label(kueue.MultiKueueOriginLabel, defaultOrigin).Obj()
			remoteRay.UID = "original-worker-uid"
			if tc.holdDeletion {
				remoteRay.Finalizers = []string{"test.example/hold"}
			}
			jobframework.SetPrebuiltWorkloadName(remoteRay, local.Name)
			localClient := utiltesting.NewClientBuilder(rayv1.AddToScheme).WithObjects(local).
				WithStatusSubresource(local).Build()
			workerClient := utiltesting.NewClientBuilder(rayv1.AddToScheme).WithObjects(remoteWl, remoteRay).
				WithStatusSubresource(remoteWl).Build()
			adapters, err := jobs.NewIntegrationManager().GetMultiKueueAdapters(sets.New("ray.io/raycluster"))
			if err != nil {
				t.Fatal(err)
			}
			adapter := adapters[gvk.String()]
			if adapter == nil {
				t.Fatal("RayCluster adapter missing")
			}
			group := &wlGroup{
				local: local, localClient: localClient, acName: "ac1", jobAdapter: adapter,
				controllerKey: key,
				remotes:       map[string]*kueue.Workload{"worker1": remoteWl},
				remoteClients: map[string]*remoteClient{
					"worker1": {client: NewNeverCachingClient(workerClient), origin: defaultOrigin},
				},
			}
			reconciler := &wlReconciler{client: localClient}
			if tc.replacedBeforeCleanup {
				meta.SetStatusCondition(&local.Status.Conditions, metav1.Condition{
					Type: kueue.WorkloadFinished, Status: metav1.ConditionTrue,
					Reason: kueue.WorkloadSliceReplaced, Message: "Replaced to accommodate a new workload slice",
				})
			}
			for range 3 {
				if _, err := reconciler.reconcileGroup(ctx, group); err != nil {
					t.Fatal(err)
				}
			}
			for _, obj := range []client.Object{&rayv1.RayCluster{}, &kueue.Workload{}} {
				objectKey := key
				if _, ok := obj.(*kueue.Workload); ok {
					objectKey.Name = remoteWl.Name
				}
				err := workerClient.Get(ctx, objectKey, obj)
				if err != nil && !apierrors.IsNotFound(err) {
					t.Fatal(err)
				}
				exists := err == nil
				if exists != tc.wantRemoteExists {
					t.Fatalf("%T exists=%v, want %v", obj, exists, tc.wantRemoteExists)
				}
			}
		})
	}
}

func TestRaySliceCleanupWaitsAndFencesReplacement(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	gvk := rayv1.SchemeGroupVersion.WithKind("RayCluster")
	adapters, err := jobs.NewIntegrationManager().GetMultiKueueAdapters(sets.New("ray.io/raycluster"))
	if err != nil {
		t.Fatal(err)
	}
	adapter := adapters[gvk.String()].(sliceCleanupAdapter)
	ray := testingray.MakeCluster("ray", TestNamespace).Label(kueue.MultiKueueOriginLabel, defaultOrigin).Obj()
	ray.UID = "old-uid"
	ray.Finalizers = []string{"test.example/hold"}
	jobframework.SetPrebuiltWorkloadName(ray, "root")
	worker := utiltesting.NewClientBuilder(rayv1.AddToScheme).WithObjects(ray).Build()
	key := client.ObjectKeyFromObject(ray)
	if _, err := adapter.CleanupRemoteSlice(ctx, worker, key, "root", ""); err != jobframework.ErrMultiKueueOriginEmpty {
		t.Fatalf("empty origin: got %v, want %v", err, jobframework.ErrMultiKueueOriginEmpty)
	}
	done, err := adapter.CleanupRemoteSlice(ctx, worker, key, "root", defaultOrigin)
	if err != nil || done {
		t.Fatalf("cleanup done=%v err=%v; expected to wait for finalizer", done, err)
	}
	if err := worker.Get(ctx, key, ray); err != nil {
		t.Fatal(err)
	}
	if ray.DeletionTimestamp.IsZero() {
		t.Fatal("deletion not requested")
	}
	ray.Finalizers = nil
	if err := worker.Update(ctx, ray); err != nil {
		t.Fatal(err)
	}
	successor := testingray.MakeCluster(key.Name, key.Namespace).Label(kueue.MultiKueueOriginLabel, defaultOrigin).Obj()
	successor.UID = "new-uid"
	jobframework.SetPrebuiltWorkloadName(successor, "successor")
	if err := worker.Create(ctx, successor); err != nil {
		t.Fatal(err)
	}
	done, err = adapter.CleanupRemoteSlice(ctx, worker, key, "root", defaultOrigin)
	if err != nil || !done {
		t.Fatalf("stale cleanup done=%v err=%v", done, err)
	}
	if err := worker.Get(ctx, key, successor); err != nil {
		t.Fatalf("stale cleanup deleted successor: %v", err)
	}
	if successor.UID != "new-uid" {
		t.Fatal("successor UID changed")
	}
}

func TestRemoteEvictedSliceBlocksHandover(t *testing.T) {
	for name, tc := range map[string]struct {
		evicted, finished, remoteMissing, alreadyReplaced bool
		wantWaiting                                       bool
	}{
		"normal resize reuses remote":     {},
		"evicted predecessor":             {evicted: true, wantWaiting: true},
		"finished evicted predecessor":    {evicted: true, finished: true, wantWaiting: true},
		"cleanup completed":               {evicted: true, remoteMissing: true},
		"new remote survives old cleanup": {evicted: true, alreadyReplaced: true},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			gvk := rayv1.SchemeGroupVersion.WithKind("RayCluster")
			adapters, err := jobs.NewIntegrationManager().GetMultiKueueAdapters(sets.New("ray.io/raycluster"))
			if err != nil {
				t.Fatal(err)
			}
			cached := utiltestingapi.MakeWorkload("root", TestNamespace).Obj()
			current := cached.DeepCopy()
			if tc.evicted {
				current.Status.Conditions = []metav1.Condition{{Type: kueue.WorkloadEvicted, Status: metav1.ConditionTrue}}
			}
			if tc.finished {
				meta.SetStatusCondition(&current.Status.Conditions, metav1.Condition{Type: kueue.WorkloadFinished, Status: metav1.ConditionTrue, Reason: kueue.WorkloadSliceReplaced})
			}
			successor := utiltestingapi.MakeWorkload("successor", TestNamespace).Obj()
			ray := testingray.MakeCluster("ray", TestNamespace).Obj()
			jobframework.SetPrebuiltWorkloadName(ray, cached.Name)
			if tc.alreadyReplaced {
				jobframework.SetPrebuiltWorkloadName(ray, successor.Name)
			}
			workerBuilder := utiltesting.NewClientBuilder(rayv1.AddToScheme)
			if !tc.remoteMissing {
				workerBuilder.WithObjects(ray)
			}
			group := &wlGroup{
				local:         successor,
				controllerKey: client.ObjectKeyFromObject(ray),
				jobAdapter:    adapters[gvk.String()],
				remoteClients: map[string]*remoteClient{"worker1": {client: NewNeverCachingClient(workerBuilder.Build())}},
			}
			reconciler := &wlReconciler{client: utiltesting.NewClientBuilder().WithObjects(cached).Build(), apiReader: utiltesting.NewClientBuilder().WithObjects(current).Build()}
			waiting, err := reconciler.waitForRemoteEvictedSlice(ctx, group)
			if err != nil {
				t.Fatal(err)
			}
			if waiting != tc.wantWaiting {
				t.Fatalf("waiting=%v, want %v", waiting, tc.wantWaiting)
			}
		})
	}
}

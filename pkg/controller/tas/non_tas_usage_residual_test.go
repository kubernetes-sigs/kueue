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

package tas

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

func TestReconcileReleasesResidualTASPodUsage(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, true)
	features.SetFeatureGateDuringTest(t, features.TASResidualPodAccounting, true)
	features.SetFeatureGateDuringTest(t, features.SchedulerLibraryIntegration, false)
	podKey := types.NamespacedName{Namespace: "ns", Name: "tas-pod"}

	runningTASPod := testingpod.MakePod("tas-pod", "ns").
		NodeName("node-a").
		Annotation(kueue.WorkloadAnnotation, "wl1").
		Annotation(kueue.PodSetRequiredTopologyAnnotation, "rack").
		StatusPhase(corev1.PodRunning).
		Request(corev1.ResourceCPU, "1").
		Obj()

	terminatedTASPod := runningTASPod.DeepCopy()
	terminatedTASPod.Status.Phase = corev1.PodSucceeded

	tests := map[string]struct {
		objects         []client.Object
		seedTASUsage    bool
		wantPendingKeys sets.Set[string]
	}{
		"terminated TAS pod clears simulator and physical usage": {
			objects:         []client.Object{terminatedTASPod},
			seedTASUsage:    true,
			wantPendingKeys: sets.New[string]("node-a"),
		},
		"deleted TAS pod clears simulator and physical usage": {
			seedTASUsage:    true,
			wantPendingKeys: sets.New[string]("node-a"),
		},
		"deleted TAS pod with only physical usage notifies freed node": {
			seedTASUsage:    true,
			wantPendingKeys: sets.New[string]("node-a"),
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			cl := utiltesting.NewClientBuilder().WithObjects(tc.objects...).Build()
			cache := schdcache.New(cl)
			r := newNonTasUsageReconciler(cl, nil, cache, nil)

			cache.TASCache().SyncNode(testingnode.MakeNode("node-a").
				Label(corev1.LabelHostname, "node-a").
				StatusAllocatable(corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourcePods: resource.MustParse("10"),
				}).Ready().Obj())
			cache.AddOrUpdateTopology(log, utiltestingapi.MakeTopology("default").Levels(corev1.LabelHostname).Obj())
			cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("tas-flavor").TopologyName("default").Obj())
			if err := cache.AddClusterQueue(ctx, utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-flavor").Resource(corev1.ResourceCPU, "2").Obj()).Obj()); err != nil {
				t.Fatal(err)
			}
			freeCapacity := func() string {
				t.Helper()
				snapshot, err := cache.Snapshot(ctx)
				if err != nil {
					t.Fatal(err)
				}
				capacity, err := snapshot.ClusterQueue("cq").TASFlavors["tas-flavor"].SerializeFreeCapacityPerDomain()
				if err != nil {
					t.Fatal(err)
				}
				return capacity
			}
			wantCapacity := freeCapacity()
			if tc.seedTASUsage {
				cache.TASCache().UpdateTASPodUsage(runningTASPod, log)
				if got := freeCapacity(); got == wantCapacity {
					t.Fatalf("seeding physical usage did not reduce free capacity: %s", got)
				}
			}

			if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: podKey}); err != nil {
				t.Fatalf("Reconcile failed: %v", err)
			}
			if !r.pending.nodes.Equal(tc.wantPendingKeys) {
				t.Errorf("pendingRequeueNodes = %v, want %v", r.pending.nodes, tc.wantPendingKeys)
			}
			if got := freeCapacity(); got != wantCapacity {
				t.Errorf("physical capacity = %s, want %s", got, wantCapacity)
			}
			r.pending.drain()
			if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: podKey}); err != nil {
				t.Fatalf("repeated Reconcile failed: %v", err)
			}
			if r.pending.nodes.Len() != 0 {
				t.Errorf("repeated Reconcile notified freed nodes: %v", r.pending.nodes)
			}
		})
	}
}

func TestResidualTASPodPredicates(t *testing.T) {
	pod := testingpod.MakePod("pod", "ns").UID("old").NodeName("node").
		Annotation(kueue.WorkloadAnnotation, "wl").
		Annotation(kueue.PodSetRequiredTopologyAnnotation, "rack").
		Request(corev1.ResourceCPU, "1").Obj()
	mutate := func(fn func(*corev1.Pod)) *corev1.Pod {
		p := pod.DeepCopy()
		fn(p)
		return p
	}
	cases := map[string]struct {
		pod        *corev1.Pod
		wantChange bool
	}{
		"unchanged":             {pod: pod},
		"workload attribution":  {pod: mutate(func(p *corev1.Pod) { p.Annotations[kueue.WorkloadAnnotation] = "other" }), wantChange: true},
		"slice attribution":     {pod: mutate(func(p *corev1.Pod) { p.Annotations[kueue.WorkloadSliceNameAnnotation] = "slice" }), wantChange: true},
		"same-name replacement": {pod: mutate(func(p *corev1.Pod) { p.UID = "new" }), wantChange: true},
		"owner changed":         {pod: mutate(func(p *corev1.Pod) { p.OwnerReferences = []metav1.OwnerReference{{UID: "owner"}} }), wantChange: true},
		"deleting":              {pod: mutate(func(p *corev1.Pod) { p.DeletionTimestamp = new(metav1.NewTime(time.Now())) }), wantChange: true},
		"terminated":            {pod: mutate(func(p *corev1.Pod) { p.Status.Phase = corev1.PodSucceeded }), wantChange: true},
		"resources resized": {pod: mutate(func(p *corev1.Pod) {
			p.Generation++
			p.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("2")
		}), wantChange: true},
	}
	for _, enabled := range []bool{false, true} {
		name := "disabled"
		if enabled {
			name = "enabled"
		}
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TASResidualPodAccounting, enabled)
			r := &NonTasUsageReconciler{}
			if got := r.Create(event.TypedCreateEvent[*corev1.Pod]{Object: pod}); got != enabled {
				t.Errorf("TAS Create=%t, want %t", got, enabled)
			}
			if got := r.Delete(event.TypedDeleteEvent[*corev1.Pod]{Object: cases["terminated"].pod}); got != enabled {
				t.Errorf("terminal TAS Delete=%t, want %t", got, enabled)
			}
			for name, tc := range cases {
				t.Run(name, func(t *testing.T) {
					if got, want := r.Update(event.TypedUpdateEvent[*corev1.Pod]{ObjectOld: pod, ObjectNew: tc.pod}), enabled && tc.wantChange; got != want {
						t.Errorf("TAS Update=%t, want %t", got, want)
					}
				})
			}
		})
	}
}

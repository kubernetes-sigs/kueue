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
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

func TestBelongsToNonTASCache(t *testing.T) {
	tests := []struct {
		name string
		pod  *corev1.Pod
		want bool
	}{
		{
			name: "nil pod",
			pod:  nil,
			want: false,
		},
		{
			name: "scheduled non-TAS pod",
			pod:  testingpod.MakePod("pod", "ns").NodeName("node-a").Obj(),
			want: true,
		},
		{
			name: "unscheduled pod",
			pod:  testingpod.MakePod("pod", "ns").Obj(),
			want: false,
		},
		{
			name: "scheduled TAS pod",
			pod: testingpod.MakePod("pod", "ns").
				NodeName("node-a").
				Annotation(kueue.PodSetRequiredTopologyAnnotation, "rack").
				Obj(),
			want: false,
		},
		{
			name: "scheduled succeeded non-TAS pod",
			pod:  testingpod.MakePod("pod", "ns").NodeName("node-a").StatusPhase(corev1.PodSucceeded).Obj(),
			want: false,
		},
		{
			name: "scheduled failed non-TAS pod",
			pod:  testingpod.MakePod("pod", "ns").NodeName("node-a").StatusPhase(corev1.PodFailed).Obj(),
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := belongsToNonTASCache(tc.pod); got != tc.want {
				t.Errorf("belongsToNonTASCache() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPodUsageReconcilerCreate(t *testing.T) {
	reconciler := &PodUsageReconciler{}

	tests := []struct {
		name string
		pod  *corev1.Pod
		want bool
	}{
		{
			name: "scheduled non-TAS pod",
			pod:  testingpod.MakePod("pod", "ns").NodeName("node-a").Obj(),
			want: true,
		},
		{
			name: "unscheduled pod",
			pod:  testingpod.MakePod("pod", "ns").Obj(),
			want: false,
		},
		{
			name: "scheduled TAS pod",
			pod: testingpod.MakePod("pod", "ns").
				NodeName("node-a").
				Annotation(kueue.PodSetRequiredTopologyAnnotation, "rack").
				Obj(),
			want: true,
		},
		{
			name: "terminated scheduled non-TAS pod",
			pod:  testingpod.MakePod("pod", "ns").NodeName("node-a").StatusPhase(corev1.PodSucceeded).Obj(),
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := reconciler.Create(event.TypedCreateEvent[*corev1.Pod]{Object: tc.pod}); got != tc.want {
				t.Errorf("Create() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPodUsageReconcilerDelete(t *testing.T) {
	reconciler := &PodUsageReconciler{}

	tests := []struct {
		name string
		pod  *corev1.Pod
		want bool
	}{
		{
			name: "scheduled non-TAS pod",
			pod:  testingpod.MakePod("pod", "ns").NodeName("node-a").Obj(),
			want: true,
		},
		{
			name: "unscheduled pod",
			pod:  testingpod.MakePod("pod", "ns").Obj(),
			want: false,
		},
		{
			name: "scheduled TAS pod",
			pod: testingpod.MakePod("pod", "ns").
				NodeName("node-a").
				Annotation(kueue.PodSetRequiredTopologyAnnotation, "rack").
				Obj(),
			want: true,
		},
		{
			name: "terminated scheduled non-TAS pod",
			pod:  testingpod.MakePod("pod", "ns").NodeName("node-a").StatusPhase(corev1.PodSucceeded).Obj(),
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := reconciler.Delete(event.TypedDeleteEvent[*corev1.Pod]{Object: tc.pod}); got != tc.want {
				t.Errorf("Delete() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPodUsageReconcilerUpdatePredicate(t *testing.T) {
	tests := []struct {
		name   string
		oldPod *corev1.Pod
		newPod *corev1.Pod
		want   bool
	}{
		{
			name:   "ignores status-only churn for scheduled non-TAS pod",
			oldPod: testingpod.MakePod("pod", "ns").NodeName("node-a").StatusPhase(corev1.PodPending).Obj(),
			newPod: testingpod.MakePod("pod", "ns").NodeName("node-a").StatusPhase(corev1.PodRunning).Obj(),
			want:   false,
		},
		{
			name:   "reconciles when non-TAS pod gets scheduled",
			oldPod: testingpod.MakePod("pod", "ns").StatusPhase(corev1.PodPending).Obj(),
			newPod: testingpod.MakePod("pod", "ns").NodeName("node-a").StatusPhase(corev1.PodPending).Obj(),
			want:   true,
		},
		{
			name:   "reconciles when scheduled non-TAS pod terminates",
			oldPod: testingpod.MakePod("pod", "ns").NodeName("node-a").StatusPhase(corev1.PodRunning).Obj(),
			newPod: testingpod.MakePod("pod", "ns").NodeName("node-a").StatusPhase(corev1.PodSucceeded).Obj(),
			want:   true,
		},
		{
			name: "reconciles when scheduled non-TAS pod becomes TAS",
			oldPod: testingpod.MakePod("pod", "ns").
				NodeName("node-a").
				StatusPhase(corev1.PodRunning).
				Obj(),
			newPod: testingpod.MakePod("pod", "ns").
				NodeName("node-a").
				Annotation(kueue.PodSetRequiredTopologyAnnotation, "rack").
				StatusPhase(corev1.PodRunning).
				Obj(),
			want: true,
		},
		{
			name:   "reconciles when scheduled non-TAS pod becomes unscheduled",
			oldPod: testingpod.MakePod("pod", "ns").NodeName("node-a").StatusPhase(corev1.PodRunning).Obj(),
			newPod: testingpod.MakePod("pod", "ns").StatusPhase(corev1.PodRunning).Obj(),
			want:   true,
		},
		{
			name: "reconciles when TAS pod becomes scheduled non-TAS",
			oldPod: testingpod.MakePod("pod", "ns").
				NodeName("node-a").
				Annotation(kueue.PodSetRequiredTopologyAnnotation, "rack").
				StatusPhase(corev1.PodRunning).
				Obj(),
			newPod: testingpod.MakePod("pod", "ns").
				NodeName("node-a").
				StatusPhase(corev1.PodRunning).
				Obj(),
			want: true,
		},
		{
			name: "ignores TAS pod status-only churn",
			oldPod: testingpod.MakePod("pod", "ns").
				NodeName("node-a").
				Annotation(kueue.PodSetRequiredTopologyAnnotation, "rack").
				StatusPhase(corev1.PodPending).
				Obj(),
			newPod: testingpod.MakePod("pod", "ns").
				NodeName("node-a").
				Annotation(kueue.PodSetRequiredTopologyAnnotation, "rack").
				StatusPhase(corev1.PodRunning).
				Obj(),
			want: false,
		},
		{
			name:   "ignores unscheduled non-TAS update",
			oldPod: testingpod.MakePod("pod", "ns").StatusPhase(corev1.PodPending).Obj(),
			newPod: testingpod.MakePod("pod", "ns").StatusPhase(corev1.PodPending).Obj(),
			want:   false,
		},
		{
			name: "reconciles when non-TAS pod resources change (resize)",
			oldPod: func() *corev1.Pod {
				p := testingpod.MakePod("pod", "ns").NodeName("node-a").Request(corev1.ResourceCPU, "2").Obj()
				p.Generation = 1
				return p
			}(),
			newPod: func() *corev1.Pod {
				p := testingpod.MakePod("pod", "ns").NodeName("node-a").Request(corev1.ResourceCPU, "4").Obj()
				p.Generation = 2
				return p
			}(),
			want: true,
		},
		{
			name:   "reconciles when non-TAS pod moves between nodes",
			oldPod: testingpod.MakePod("pod", "ns").NodeName("node-a").Request(corev1.ResourceCPU, "2").Obj(),
			newPod: testingpod.MakePod("pod", "ns").NodeName("node-b").Request(corev1.ResourceCPU, "2").Obj(),
			want:   true,
		},
		{
			name:   "ignores non-TAS pod update with same resources",
			oldPod: testingpod.MakePod("pod", "ns").NodeName("node-a").Request(corev1.ResourceCPU, "2").Obj(),
			newPod: testingpod.MakePod("pod", "ns").NodeName("node-a").Request(corev1.ResourceCPU, "2").Obj(),
			want:   false,
		},
		{
			name:   "ignores unscheduled non-TAS pod with changed resources",
			oldPod: testingpod.MakePod("pod", "ns").Request(corev1.ResourceCPU, "2").Obj(),
			newPod: testingpod.MakePod("pod", "ns").Request(corev1.ResourceCPU, "4").Obj(),
			want:   false,
		},
	}

	reconciler := &PodUsageReconciler{}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := reconciler.Update(event.TypedUpdateEvent[*corev1.Pod]{
				ObjectOld: tc.oldPod,
				ObjectNew: tc.newPod,
			})
			if got != tc.want {
				t.Errorf("Update() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPodUsageReconcilerUpdate(t *testing.T) {
	reconciler := &PodUsageReconciler{}
	oldPod := testingpod.MakePod("pod", "ns").NodeName("node-a").StatusPhase(corev1.PodRunning).Obj()
	newPod := testingpod.MakePod("pod", "ns").NodeName("node-a").StatusPhase(corev1.PodSucceeded).Obj()

	got := reconciler.Update(event.TypedUpdateEvent[*corev1.Pod]{
		ObjectOld: oldPod,
		ObjectNew: newPod,
	})
	if !got {
		t.Errorf("Update() = %v, want %v", got, true)
	}
}

func TestDrainPendingNodes(t *testing.T) {
	tests := map[string]struct {
		initialNodes    []string
		objects         []corev1.Node
		interceptGet    bool
		wantPendingLen  int
		wantPendingKeys sets.Set[string]
	}{
		"empty set is a no-op": {
			wantPendingLen: 0,
		},
		"NotFound node is skipped without re-insert": {
			initialNodes:   []string{"deleted-node"},
			wantPendingLen: 0,
		},
		"existing node is drained": {
			initialNodes:   []string{"node-a"},
			objects:        []corev1.Node{{Name: "node-a"}},
			wantPendingLen: 0,
		},
		"duplicate nodes are deduplicated": {
			initialNodes: []string{"node-a", "node-a", "node-b"},
			objects: []corev1.Node{
				{Name: "node-a"},
				{Name: "node-b"},
			},
			wantPendingLen: 0,
		},
		"transient error re-inserts node for next cycle": {
			initialNodes:    []string{"flaky-node"},
			interceptGet:    true,
			wantPendingLen:  1,
			wantPendingKeys: sets.New[string]("flaky-node"),
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			builder := fake.NewClientBuilder()
			for i := range tc.objects {
				builder = builder.WithObjects(&tc.objects[i])
			}
			if tc.interceptGet {
				builder = builder.WithInterceptorFuncs(interceptor.Funcs{
					Get: func(_ context.Context, _ client.WithWatch, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
						return errors.New("transient error")
					},
				})
			}
			cl := builder.Build()
			cache := schdcache.New(cl)
			r := newPodUsageReconciler(cl, nil, cache, nil)

			for _, n := range tc.initialNodes {
				r.notifyFreedNode(n)
			}
			r.drainPendingNodes(t.Context())

			if r.pending.nodes.Len() != tc.wantPendingLen {
				t.Errorf("pendingRequeueNodes has %d items, want %d", r.pending.nodes.Len(), tc.wantPendingLen)
			}
			if tc.wantPendingKeys != nil && !r.pending.nodes.Equal(tc.wantPendingKeys) {
				t.Errorf("pendingRequeueNodes = %v, want %v", r.pending.nodes, tc.wantPendingKeys)
			}
		})
	}
}

type fakeSimulatorFactory struct {
	simulator.Factory
	pods map[client.ObjectKey]string
}

func (f *fakeSimulatorFactory) TrackPod(_ context.Context, pod *corev1.Pod) {
	f.pods[client.ObjectKeyFromObject(pod)] = pod.Spec.NodeName
}

func (f *fakeSimulatorFactory) UntrackPod(_ context.Context, key client.ObjectKey) string {
	node := f.pods[key]
	delete(f.pods, key)
	return node
}

func TestReconcileNotifiesFreedNodeOnUntrackPod(t *testing.T) {
	ctx := t.Context()
	podKey := types.NamespacedName{Namespace: "ns", Name: "tas-pod"}

	runningTASPod := testingpod.MakePod("tas-pod", "ns").
		NodeName("node-a").
		Annotation(kueue.WorkloadAnnotation, "wl1").
		Annotation(kueue.PodSetRequiredTopologyAnnotation, "rack").
		StatusPhase(corev1.PodRunning).
		Obj()

	terminatedTASPod := runningTASPod.DeepCopy()
	terminatedTASPod.Status.Phase = corev1.PodSucceeded

	tests := map[string]struct {
		objects         []client.Object
		wantPendingKeys sets.Set[string]
	}{
		"terminated TAS pod notifies freed node": {
			objects:         []client.Object{terminatedTASPod},
			wantPendingKeys: sets.New[string]("node-a"),
		},
		"deleted TAS pod notifies freed node": {
			wantPendingKeys: sets.New[string]("node-a"),
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cl := fake.NewClientBuilder().WithObjects(tc.objects...).Build()
			simulatorFactory := &fakeSimulatorFactory{pods: make(map[client.ObjectKey]string)}
			cache := schdcache.New(cl, schdcache.WithSimulatorFactory(simulatorFactory))
			r := newPodUsageReconciler(cl, nil, cache, nil)

			// Seed the simulator with the running TAS pod first.
			cache.TASCache().TrackPod(ctx, runningTASPod)

			if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: podKey}); err != nil {
				t.Fatalf("Reconcile failed: %v", err)
			}
			if !r.pending.nodes.Equal(tc.wantPendingKeys) {
				t.Errorf("pendingRequeueNodes = %v, want %v", r.pending.nodes, tc.wantPendingKeys)
			}
		})
	}
}

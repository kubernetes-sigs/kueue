//go:build !exclude_scheduler_library

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

package was

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

func TestWorkloadMapping(t *testing.T) {
	basicPod := testingpod.MakePod("pod", "ns").Annotation(kueue.WorkloadAnnotation, "wl").Obj()

	testCases := map[string]struct {
		operation func(context.Context, *wasSimulator)
		want      podsByWorkload
	}{
		"add pod with workload annotation": {
			operation: func(ctx context.Context, sim *wasSimulator) {
				sim.TrackPod(ctx, basicPod)
			},
			want: podsByWorkload{
				types.NamespacedName{Namespace: "ns", Name: "wl"}: podsByKey{
					types.NamespacedName{Namespace: "ns", Name: "pod"}: basicPod,
				},
			},
		},
		"remove pod": {
			operation: func(ctx context.Context, sim *wasSimulator) {
				sim.TrackPod(ctx, testingpod.MakePod("pod1", "ns").Annotation(kueue.WorkloadAnnotation, "wl1").Obj())
				sim.TrackPod(ctx, testingpod.MakePod("pod2", "ns").Annotation(kueue.WorkloadAnnotation, "wl1").Obj())
				sim.TrackPod(ctx, testingpod.MakePod("pod3", "ns").Annotation(kueue.WorkloadAnnotation, "wl2").Obj())
				sim.TrackPod(ctx, testingpod.MakePod("pod4", "ns").Annotation(kueue.WorkloadAnnotation, "wl2").Obj())
				sim.UntrackPod(ctx, types.NamespacedName{Namespace: "ns", Name: "pod1"})
			},
			want: podsByWorkload{
				types.NamespacedName{Namespace: "ns", Name: "wl1"}: podsByKey{
					types.NamespacedName{Namespace: "ns", Name: "pod2"}: testingpod.MakePod("pod2", "ns").Annotation(kueue.WorkloadAnnotation, "wl1").Obj(),
				},
				types.NamespacedName{Namespace: "ns", Name: "wl2"}: podsByKey{
					types.NamespacedName{Namespace: "ns", Name: "pod3"}: testingpod.MakePod("pod3", "ns").Annotation(kueue.WorkloadAnnotation, "wl2").Obj(),
					types.NamespacedName{Namespace: "ns", Name: "pod4"}: testingpod.MakePod("pod4", "ns").Annotation(kueue.WorkloadAnnotation, "wl2").Obj(),
				},
			},
		},
		"remove all pods": {
			operation: func(ctx context.Context, sim *wasSimulator) {
				sim.TrackPod(ctx, testingpod.MakePod("pod1", "ns").Annotation(kueue.WorkloadAnnotation, "wl1").Obj())
				sim.TrackPod(ctx, testingpod.MakePod("pod2", "ns").Annotation(kueue.WorkloadAnnotation, "wl1").Obj())
				sim.TrackPod(ctx, testingpod.MakePod("pod3", "ns").Annotation(kueue.WorkloadAnnotation, "wl2").Obj())
				sim.UntrackPod(ctx, types.NamespacedName{Namespace: "ns", Name: "pod1"})
				sim.UntrackPod(ctx, types.NamespacedName{Namespace: "ns", Name: "pod2"})
				sim.UntrackPod(ctx, types.NamespacedName{Namespace: "ns", Name: "pod3"})
			},
			want: podsByWorkload{},
		},
		"update pod workload annotation": {
			operation: func(ctx context.Context, sim *wasSimulator) {
				sim.TrackPod(ctx, testingpod.MakePod("pod1", "ns").Annotation(kueue.WorkloadAnnotation, "wl1").Obj())
				sim.TrackPod(ctx, testingpod.MakePod("pod1", "ns").Annotation(kueue.WorkloadAnnotation, "wl2").Obj())
			},
			want: podsByWorkload{
				types.NamespacedName{Namespace: "ns", Name: "wl2"}: podsByKey{
					types.NamespacedName{Namespace: "ns", Name: "pod1"}: testingpod.MakePod("pod1", "ns").Annotation(kueue.WorkloadAnnotation, "wl2").Obj(),
				},
			},
		},
		"update unassigned pod to have workload annotation": {
			operation: func(ctx context.Context, sim *wasSimulator) {
				sim.TrackPod(ctx, testingpod.MakePod("pod1", "ns").Annotation("", "").Obj())
				sim.TrackPod(ctx, testingpod.MakePod("pod1", "ns").Annotation(kueue.WorkloadAnnotation, "wl1").Obj())
			},
			want: podsByWorkload{
				types.NamespacedName{Namespace: "ns", Name: "wl1"}: podsByKey{
					types.NamespacedName{Namespace: "ns", Name: "pod1"}: testingpod.MakePod("pod1", "ns").Annotation(kueue.WorkloadAnnotation, "wl1").Obj(),
				},
			},
		},
		"update pod from workload annotation to unassigned": {
			operation: func(ctx context.Context, sim *wasSimulator) {
				sim.TrackPod(ctx, testingpod.MakePod("pod1", "ns").Annotation(kueue.WorkloadAnnotation, "wl1").Obj())
				sim.TrackPod(ctx, testingpod.MakePod("pod1", "ns").Annotation("", "").Obj())
			},
			want: podsByWorkload{},
		},
		"add pod with empty workload annotation": {
			operation: func(ctx context.Context, sim *wasSimulator) {
				sim.TrackPod(ctx, testingpod.MakePod("pod1", "ns").Annotation(kueue.WorkloadAnnotation, "").Obj())
			},
			want: podsByWorkload{},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			sim, err := NewWASSimulator(klog.NewContext(ctx, logr.Discard()), nil)
			if err != nil {
				t.Fatalf("NewWASSimulator failed: %v", err)
			}

			tc.operation(ctx, sim)

			snapshotRaw, err := sim.Snapshot(ctx, []*corev1.Node{})
			if err != nil {
				t.Fatalf("Snapshot failed: %v", err)
			}
			snapshot, ok := snapshotRaw.(*wasSimulatorSnapshot)
			if !ok {
				t.Fatalf("Snapshot is not a wasSimulatorSnapshot: %T", snapshotRaw)
			}

			if diff := cmp.Diff(tc.want, snapshot.podsByWorkload); diff != "" {
				t.Errorf("Unexpected pod assignments (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestTrackPodDeepCopy(t *testing.T) {
	ctx := t.Context()
	sim, err := NewWASSimulator(klog.NewContext(ctx, logr.Discard()), nil)
	if err != nil {
		t.Fatalf("NewWASSimulator failed: %v", err)
	}

	pod := testingpod.MakePod("pod1", "ns").Annotation(kueue.WorkloadAnnotation, "wl1").Obj()
	sim.TrackPod(ctx, pod)

	// Mutate the pod object that was passed into TrackPod
	pod.Annotations[kueue.WorkloadAnnotation] = "mutated-wl"

	snapshotRaw, err := sim.Snapshot(ctx, nil)
	if err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}
	snapshot := snapshotRaw.(*wasSimulatorSnapshot)

	want := podsByWorkload{
		types.NamespacedName{Namespace: "ns", Name: "wl1"}: podsByKey{
			types.NamespacedName{Namespace: "ns", Name: "pod1"}: testingpod.MakePod("pod1", "ns").Annotation(kueue.WorkloadAnnotation, "wl1").Obj(),
		},
	}

	if diff := cmp.Diff(want, snapshot.podsByWorkload); diff != "" {
		t.Errorf("TrackPod did not deep copy pod (-want,+got):\n%s", diff)
	}
}

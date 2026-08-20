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
	"testing"

	"github.com/google/go-cmp/cmp"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	"k8s.io/apimachinery/pkg/types"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

const quotaExceededGate = controllerconstants.RayClusterQuotaExceededScaleGate

func rayClusterWithGroups(groups ...string) *rayv1.RayCluster {
	rc := &rayv1.RayCluster{}
	for _, g := range groups {
		rc.Spec.WorkerGroupSpecs = append(rc.Spec.WorkerGroupSpecs, rayv1.WorkerGroupSpec{GroupName: g})
	}
	return rc
}

func TestNotFitWorkerGroups(t *testing.T) {
	noFit := flavorassigner.PodSetAssignment{
		Name:   kueue.NewPodSetReference("gpu-group-preferred"),
		Status: *flavorassigner.NewStatus("insufficient quota"),
	}
	fit := flavorassigner.PodSetAssignment{
		Name: kueue.NewPodSetReference("gpu-group-fallback"),
	}
	head := flavorassigner.PodSetAssignment{
		Name:   kueue.NewPodSetReference(headGroupPodSetName),
		Status: *flavorassigner.NewStatus("insufficient quota"),
	}

	assignment := flavorassigner.Assignment{
		PodSets: []flavorassigner.PodSetAssignment{head, noFit, fit},
	}

	got := notFitWorkerGroups(&assignment)
	want := map[string]bool{"gpu-group-preferred": true}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("notFitWorkerGroups() mismatch (-want +got):\n%s", diff)
	}
}

func TestApplyScaleGate(t *testing.T) {
	cases := map[string]struct {
		rc          *rayv1.RayCluster
		gatedGroups map[string]bool
		gated       bool
		wantChanged bool
		wantGates   map[string][]string
	}{
		"add gate to the not-fit group only": {
			rc:          rayClusterWithGroups("gpu-group-preferred", "gpu-group-fallback"),
			gatedGroups: map[string]bool{"gpu-group-preferred": true},
			gated:       true,
			wantChanged: true,
			wantGates: map[string][]string{
				"gpu-group-preferred": {quotaExceededGate},
				"gpu-group-fallback":  nil,
			},
		},
		"idempotent when gate already present": {
			rc: func() *rayv1.RayCluster {
				rc := rayClusterWithGroups("gpu-group-preferred")
				rc.Spec.WorkerGroupSpecs[0].ScaleStrategy.ScaleGate = []string{quotaExceededGate}
				return rc
			}(),
			gatedGroups: map[string]bool{"gpu-group-preferred": true},
			gated:       true,
			wantChanged: false,
			wantGates: map[string][]string{
				"gpu-group-preferred": {quotaExceededGate},
			},
		},
		"clear gate from all groups": {
			rc: func() *rayv1.RayCluster {
				rc := rayClusterWithGroups("gpu-group-preferred", "gpu-group-fallback")
				rc.Spec.WorkerGroupSpecs[0].ScaleStrategy.ScaleGate = []string{quotaExceededGate}
				return rc
			}(),
			gated:       false,
			wantChanged: true,
			wantGates: map[string][]string{
				"gpu-group-preferred": {},
				"gpu-group-fallback":  nil,
			},
		},
		"clear is a no-op when no gate present": {
			rc:          rayClusterWithGroups("gpu-group-preferred"),
			gated:       false,
			wantChanged: false,
			wantGates: map[string][]string{
				"gpu-group-preferred": nil,
			},
		},
		"preserves unrelated gates": {
			rc: func() *rayv1.RayCluster {
				rc := rayClusterWithGroups("gpu-group-preferred")
				rc.Spec.WorkerGroupSpecs[0].ScaleStrategy.ScaleGate = []string{"other.io/gate", quotaExceededGate}
				return rc
			}(),
			gated:       false,
			wantChanged: true,
			wantGates: map[string][]string{
				"gpu-group-preferred": {"other.io/gate"},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			changed := applyScaleGate(tc.rc, tc.gatedGroups, tc.gated)
			if changed != tc.wantChanged {
				t.Errorf("applyScaleGate() changed = %v, want %v", changed, tc.wantChanged)
			}
			for i := range tc.rc.Spec.WorkerGroupSpecs {
				wgs := &tc.rc.Spec.WorkerGroupSpecs[i]
				want := tc.wantGates[wgs.GroupName]
				if diff := cmp.Diff(want, wgs.ScaleStrategy.ScaleGate); diff != "" {
					t.Errorf("group %q scaleGate mismatch (-want +got):\n%s", wgs.GroupName, diff)
				}
			}
		})
	}
}

func TestReconcileRayClusterScaleGate(t *testing.T) {
	const ns = "default"
	rc := rayClusterWithGroups("gpu-group-preferred", "gpu-group-fallback")
	rc.Name = "my-raycluster"
	rc.Namespace = ns

	wl := utiltestingapi.MakeWorkload("wl", ns).
		ControllerReference(rayClusterGVK, rc.Name, "uid-1").
		Obj()

	cl := utiltesting.NewClientBuilder(rayv1.AddToScheme).WithObjects(rc, wl).Build()
	s := &Scheduler{client: cl}

	e := &entry{
		Info: *workload.NewInfo(wl),
		assignment: flavorassigner.Assignment{
			PodSets: []flavorassigner.PodSetAssignment{
				{Name: kueue.NewPodSetReference(headGroupPodSetName)},
				{
					Name:   kueue.NewPodSetReference("gpu-group-preferred"),
					Status: *flavorassigner.NewStatus("insufficient quota"),
				},
				{Name: kueue.NewPodSetReference("gpu-group-fallback")},
			},
		},
	}

	ctx := context.Background()

	// Gate: only the not-fit preferred group gets the gate.
	s.reconcileRayClusterScaleGate(ctx, e, true)
	got := &rayv1.RayCluster{}
	if err := cl.Get(ctx, types.NamespacedName{Namespace: ns, Name: rc.Name}, got); err != nil {
		t.Fatalf("get raycluster: %v", err)
	}
	if diff := cmp.Diff([]string{quotaExceededGate}, got.Spec.WorkerGroupSpecs[0].ScaleStrategy.ScaleGate); diff != "" {
		t.Errorf("preferred group scaleGate mismatch (-want +got):\n%s", diff)
	}
	if got.Spec.WorkerGroupSpecs[1].ScaleStrategy.ScaleGate != nil {
		t.Errorf("fallback group should not be gated, got %v", got.Spec.WorkerGroupSpecs[1].ScaleStrategy.ScaleGate)
	}

	// Clear: gate removed on admission.
	s.reconcileRayClusterScaleGate(ctx, e, false)
	if err := cl.Get(ctx, types.NamespacedName{Namespace: ns, Name: rc.Name}, got); err != nil {
		t.Fatalf("get raycluster: %v", err)
	}
	if len(got.Spec.WorkerGroupSpecs[0].ScaleStrategy.ScaleGate) != 0 {
		t.Errorf("preferred group scaleGate should be cleared, got %v", got.Spec.WorkerGroupSpecs[0].ScaleStrategy.ScaleGate)
	}
}

func TestReconcileRayClusterScaleGate_NonRayClusterOwnerIgnored(t *testing.T) {
	wl := utiltestingapi.MakeWorkload("wl", "default").Obj()
	cl := utiltesting.NewClientBuilder(rayv1.AddToScheme).WithObjects(wl).Build()
	s := &Scheduler{client: cl}
	e := &entry{Info: *workload.NewInfo(wl)}

	// No owner reference: must be a no-op and not panic.
	s.reconcileRayClusterScaleGate(context.Background(), e, true)
}

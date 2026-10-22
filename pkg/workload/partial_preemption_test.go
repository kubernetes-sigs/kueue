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

package workload

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestIsPartialPreemptionJob(t *testing.T) {
	cases := map[string]struct {
		gateEnabled bool
		annotated   bool
		want        bool
	}{
		"gate off":               {gateEnabled: false, annotated: true, want: false},
		"gate on, not annotated": {gateEnabled: true, annotated: false, want: false},
		"gate on, annotated":     {gateEnabled: true, annotated: true, want: true},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.PartialPreemption, tc.gateEnabled)
			wl := utiltesting.MakeWorkload("wl", "ns").Obj()
			if tc.annotated {
				wl.Annotations = map[string]string{constants.PartialPreemptionAnnotation: "true"}
			}
			if got := IsPartialPreemptionJob(wl); got != tc.want {
				t.Errorf("IsPartialPreemptionJob() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestPodSetSpecCount(t *testing.T) {
	wl := utiltesting.MakeWorkload("wl", "ns").
		PodSets(
			*utiltesting.MakePodSet("worker", 5).Request(corev1.ResourceCPU, "1").Obj(),
			*utiltesting.MakePodSet("driver", 1).Request(corev1.ResourceCPU, "2").Obj(),
		).
		Obj()

	cases := map[string]struct {
		name      kueue.PodSetReference
		wantCount int32
		wantOk    bool
	}{
		"existing worker": {name: "worker", wantCount: 5, wantOk: true},
		"existing driver": {name: "driver", wantCount: 1, wantOk: true},
		"missing":         {name: "missing", wantCount: 0, wantOk: false},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gotCount, gotOk := PodSetSpecCount(wl, tc.name)
			if gotCount != tc.wantCount || gotOk != tc.wantOk {
				t.Errorf("PodSetSpecCount(%q) = (%d, %t), want (%d, %t)", tc.name, gotCount, gotOk, tc.wantCount, tc.wantOk)
			}
		})
	}
}

func TestReclaimTargetCount(t *testing.T) {
	wl := utiltesting.MakeWorkload("wl", "ns").
		PodSets(*utiltesting.MakePodSet("main", 10).Request(corev1.ResourceCPU, "1").Obj()).
		Obj()
	wl.Status.Admission = utiltesting.MakeAdmission("cq").
		PodSets(
			utiltesting.MakePodSetAssignment("main").
				Assignment(corev1.ResourceCPU, "default", "1").
				Count(10).
				Obj(),
		).
		Obj()
	wl.Status.Admission.PodSetAssignments[0].ReclaimTargetCount = ptr.To[int32](7)

	cases := map[string]struct {
		name      kueue.PodSetReference
		wantCount int32
		wantOk    bool
	}{
		"main with target": {name: "main", wantCount: 7, wantOk: true},
		"missing podset":   {name: "missing", wantCount: 0, wantOk: false},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gotCount, gotOk := ReclaimTargetCount(wl, tc.name)
			if gotCount != tc.wantCount || gotOk != tc.wantOk {
				t.Errorf("ReclaimTargetCount(%q) = (%d, %t), want (%d, %t)", tc.name, gotCount, gotOk, tc.wantCount, tc.wantOk)
			}
		})
	}
}

func TestHasPendingReclaimTargetCount(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.PartialPreemption, true)

	// The active release path lowers spec.Count (synced from job parallelism) and/or reports
	// reclaimable pods; the effective count is spec.Count minus reclaimable pods.
	makeWorkload := func(admitted bool, specCount, reclaimable int32, target *int32) *kueue.Workload {
		wl := utiltesting.MakeWorkload("wl", "ns").
			Annotation(constants.PartialPreemptionAnnotation, "true").
			PodSets(*utiltesting.MakePodSet("executor", int(specCount)).
				Request(corev1.ResourceCPU, "1").
				SetMinimumCount(1).
				Obj()).
			Obj()
		if admitted {
			wl.Status.Admission = utiltesting.MakeAdmission("cq").
				PodSets(utiltesting.MakePodSetAssignment("executor").
					Assignment(corev1.ResourceCPU, "default", "1").
					Count(specCount).
					Obj()).
				Obj()
		}
		if target != nil {
			wl.Status.Admission.PodSetAssignments[0].ReclaimTargetCount = target
		}
		if reclaimable > 0 {
			wl.Status.ReclaimablePods = []kueue.ReclaimablePod{{Name: "executor", Count: reclaimable}}
		}
		return wl
	}

	cases := map[string]struct {
		reclaimablePodsGate bool
		admitted            bool
		specCount           int32
		reclaimable         int32
		target              *int32
		want                bool
	}{
		"no admission":               {specCount: 5, want: false},
		"admission without target":   {admitted: true, specCount: 5, want: false},
		"target above spec":          {admitted: true, specCount: 5, target: ptr.To[int32](2), want: true},
		"spec lowered to target":     {admitted: true, specCount: 2, target: ptr.To[int32](2), want: false},
		"reclaimable reaches target": {reclaimablePodsGate: true, admitted: true, specCount: 5, reclaimable: 3, target: ptr.To[int32](2), want: false},
		"reclaimable above target":   {reclaimablePodsGate: true, admitted: true, specCount: 5, reclaimable: 1, target: ptr.To[int32](2), want: true},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.ReclaimablePods, tc.reclaimablePodsGate)
			if got := HasPendingReclaimTargetCount(makeWorkload(tc.admitted, tc.specCount, tc.reclaimable, tc.target)); got != tc.want {
				t.Fatalf("HasPendingReclaimTargetCount() = %t, want %t", got, tc.want)
			}
		})
	}
}

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

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/util/limitrange"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

// TestEffectivePodSpecs verifies resource defaulting and RuntimeClass overhead
// against explicit expected PodSpecs, while preserving the original Workload.
func TestEffectivePodSpecs(t *testing.T) {
	runtimeClass := utiltesting.MakeRuntimeClass("kata", "handler").
		PodOverhead(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}).
		Obj()
	limitRange := utiltesting.MakeLimitRange("limits", "ns").
		WithValue("Default", corev1.ResourceCPU, "4").
		WithValue("DefaultRequest", corev1.ResourceCPU, "2").
		Obj()

	cases := map[string]struct {
		wl           *kueue.Workload
		wantPodSpecs []corev1.PodSpec
	}{
		"nothing set": {
			wl: utiltestingapi.MakeWorkload("wl", "ns").
				PodSets(*utiltestingapi.MakePodSet("main", 1).Obj()).
				Obj(),
			wantPodSpecs: []corev1.PodSpec{
				utiltestingapi.MakePodSet("main", 1).
					Limit(corev1.ResourceCPU, "4").Request(corev1.ResourceCPU, "2").Template.Spec,
			},
		},
		"limits only": {
			wl: utiltestingapi.MakeWorkload("wl", "ns").
				PodSets(*utiltestingapi.MakePodSet("main", 1).
					Limit(corev1.ResourceCPU, "3").Obj()).
				Obj(),
			// An explicit limit supplies the missing request before LimitRange defaults.
			wantPodSpecs: []corev1.PodSpec{
				utiltestingapi.MakePodSet("main", 1).
					Limit(corev1.ResourceCPU, "3").Request(corev1.ResourceCPU, "3").Template.Spec,
			},
		},
		"requests set": {
			wl: utiltestingapi.MakeWorkload("wl", "ns").
				PodSets(*utiltestingapi.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "1").Obj()).
				Obj(),
			wantPodSpecs: []corev1.PodSpec{
				utiltestingapi.MakePodSet("main", 1).
					Limit(corev1.ResourceCPU, "4").Request(corev1.ResourceCPU, "1").Template.Spec,
			},
		},
		"overhead via runtime class": {
			wl: utiltestingapi.MakeWorkload("wl", "ns").
				PodSets(*utiltestingapi.MakePodSet("main", 1).
					RuntimeClass("kata").
					Limit(corev1.ResourceCPU, "3").Obj()).
				Obj(),
			wantPodSpecs: []corev1.PodSpec{
				utiltestingapi.MakePodSet("main", 1).
					RuntimeClass("kata").
					PodOverHead(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}).
					Limit(corev1.ResourceCPU, "3").Request(corev1.ResourceCPU, "3").Template.Spec,
			},
		},
		"missing runtime class": {
			wl: utiltestingapi.MakeWorkload("wl", "ns").
				PodSets(*utiltestingapi.MakePodSet("main", 1).
					RuntimeClass("missing").
					Limit(corev1.ResourceCPU, "3").Obj()).
				Obj(),
			wantPodSpecs: []corev1.PodSpec{
				utiltestingapi.MakePodSet("main", 1).
					RuntimeClass("missing").
					Limit(corev1.ResourceCPU, "3").Request(corev1.ResourceCPU, "3").Template.Spec,
			},
		},
		"pod-level limits": {
			wl: utiltestingapi.MakeWorkload("wl", "ns").
				PodSets(*utiltestingapi.MakePodSet("main", 1).
					PodLevelLimit(corev1.ResourceMemory, "2Gi").Obj()).
				Obj(),
			wantPodSpecs: []corev1.PodSpec{
				utiltestingapi.MakePodSet("main", 1).
					PodLevelLimit(corev1.ResourceMemory, "2Gi").PodLevelRequest(corev1.ResourceMemory, "2Gi").
					Limit(corev1.ResourceCPU, "4").Request(corev1.ResourceCPU, "2").Template.Spec,
			},
		},
		"two podsets mixed": {
			wl: utiltestingapi.MakeWorkload("wl", "ns").
				PodSets(
					*utiltestingapi.MakePodSet("a", 1).Limit(corev1.ResourceCPU, "3").Obj(),
					*utiltestingapi.MakePodSet("b", 1).RuntimeClass("kata").Obj(),
				).
				Obj(),
			wantPodSpecs: []corev1.PodSpec{
				utiltestingapi.MakePodSet("a", 1).
					Limit(corev1.ResourceCPU, "3").Request(corev1.ResourceCPU, "3").Template.Spec,
				utiltestingapi.MakePodSet("b", 1).
					RuntimeClass("kata").
					PodOverHead(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}).
					Limit(corev1.ResourceCPU, "4").Request(corev1.ResourceCPU, "2").Template.Spec,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cl := utiltesting.NewClientBuilder().
				WithObjects(runtimeClass, limitRange).
				WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType).
				Build()
			ctx, _ := utiltesting.ContextWithLog(t)
			original := tc.wl.DeepCopy()

			in, _ := ResolveAdjustmentInputs(ctx, cl, tc.wl)
			effective := EffectivePodSpecs(tc.wl, in)

			if diff := cmp.Diff(original, tc.wl); diff != "" {
				t.Errorf("EffectivePodSpecs mutated the workload (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantPodSpecs, effective); diff != "" {
				t.Errorf("Unexpected effective PodSpecs (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestEffectivePodSpecsOwnOverhead(t *testing.T) {
	overhead := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}
	inputs := AdjustmentInputs{PodOverheads: map[string]corev1.ResourceList{"runtime": overhead}}
	wl := utiltestingapi.MakeWorkload("wl", "ns").PodSets(
		*utiltestingapi.MakePodSet("a", 1).RuntimeClass("runtime").Obj(),
		*utiltestingapi.MakePodSet("b", 1).RuntimeClass("runtime").Obj(),
	).Obj()
	effective := EffectivePodSpecs(wl, inputs)
	effective[0].Overhead[corev1.ResourceCPU] = resource.MustParse("9")
	if got := overhead[corev1.ResourceCPU]; got.Cmp(resource.MustParse("1")) != 0 {
		t.Errorf("editing effective view changed resolved inputs: got CPU %s, want 1", got.String())
	}
	if got := effective[1].Overhead[corev1.ResourceCPU]; got.Cmp(resource.MustParse("1")) != 0 {
		t.Errorf("editing one PodSet changed another: got CPU %s, want 1", got.String())
	}
	if len(wl.Spec.PodSets[0].Template.Spec.Overhead) != 0 {
		t.Fatal("raw Workload changed")
	}
}

// TestEffectivePodSpecsKeepsOnlyAdjustedSpecs checks that an Info keeps the
// effective PodSpecs exactly when an adjustment changes the PodSet, and that
// comparing only the adjusted fields agrees with comparing whole PodSpecs.
func TestEffectivePodSpecsKeepsOnlyAdjustedSpecs(t *testing.T) {
	cases := map[string]struct {
		podSet     *kueue.PodSet
		inputs     AdjustmentInputs
		wantChange bool
	}{
		"nothing to adjust": {
			podSet: utiltestingapi.MakePodSet("main", 1).
				RuntimeClass("runc").
				NodeSelector(map[string]string{"pool": "a"}).
				Toleration(corev1.Toleration{Key: "gpu", Operator: corev1.TolerationOpExists}).
				Request(corev1.ResourceCPU, "1").Limit(corev1.ResourceCPU, "2").
				Obj(),
		},
		"defaults already set": {
			podSet: utiltestingapi.MakePodSet("main", 1).
				Request(corev1.ResourceCPU, "1").Limit(corev1.ResourceCPU, "2").
				Obj(),
			inputs: AdjustmentInputs{LimitRangeSummary: limitrange.Summarize(*utiltesting.MakeLimitRange("lr", "ns").
				WithValue("Default", corev1.ResourceCPU, "4").
				WithValue("DefaultRequest", corev1.ResourceCPU, "2").
				Obj())},
		},
		"own overhead": {
			podSet: utiltestingapi.MakePodSet("main", 1).
				RuntimeClass("kata").
				PodOverHead(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}).
				Request(corev1.ResourceCPU, "1").
				Obj(),
			inputs: AdjustmentInputs{PodOverheads: map[string]corev1.ResourceList{"kata": {corev1.ResourceCPU: resource.MustParse("2")}}},
		},
		"runtime class overhead": {
			podSet: utiltestingapi.MakePodSet("main", 1).
				RuntimeClass("kata").
				Request(corev1.ResourceCPU, "1").
				Obj(),
			inputs:     AdjustmentInputs{PodOverheads: map[string]corev1.ResourceList{"kata": {corev1.ResourceCPU: resource.MustParse("2")}}},
			wantChange: true,
		},
		"container limits as requests": {
			podSet: utiltestingapi.MakePodSet("main", 1).
				Limit(corev1.ResourceCPU, "2").
				Obj(),
			wantChange: true,
		},
		"init container limits as requests": {
			podSet: utiltestingapi.MakePodSet("main", 1).
				InitContainers(corev1.Container{Name: "init", Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
				}}).
				Request(corev1.ResourceCPU, "1").
				Obj(),
			wantChange: true,
		},
		"pod-level limits as requests": {
			podSet: utiltestingapi.MakePodSet("main", 1).
				Request(corev1.ResourceCPU, "1").
				PodLevelLimit(corev1.ResourceMemory, "1Gi").
				Obj(),
			wantChange: true,
		},
		"container LimitRange default": {
			podSet: utiltestingapi.MakePodSet("main", 1).
				Request(corev1.ResourceCPU, "1").
				Obj(),
			inputs: AdjustmentInputs{LimitRangeSummary: limitrange.Summarize(*utiltesting.MakeLimitRange("lr", "ns").
				WithValue("Default", corev1.ResourceCPU, "4").
				Obj())},
			wantChange: true,
		},
		"pod LimitRange default": {
			podSet: utiltestingapi.MakePodSet("main", 1).
				Request(corev1.ResourceCPU, "1").Limit(corev1.ResourceCPU, "1").
				PodLevelRequest(corev1.ResourceMemory, "1Gi").
				Obj(),
			inputs: AdjustmentInputs{LimitRangeSummary: limitrange.Summarize(*utiltesting.MakeLimitRange("lr", "ns").
				WithType(corev1.LimitTypePod).
				WithValue("Default", corev1.ResourceMemory, "2Gi").
				Obj())},
			wantChange: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			wl := utiltestingapi.MakeWorkload("wl", "ns").PodSets(*tc.podSet).Obj()
			if got := effectivePodSpecs(wl, tc.inputs) != nil; got != tc.wantChange {
				t.Errorf("effectivePodSpecs kept the effective PodSpecs: %v, want %v", got, tc.wantChange)
			}
			raw := []corev1.PodSpec{wl.Spec.PodSets[0].Template.Spec}
			if got := !equality.Semantic.DeepEqual(EffectivePodSpecs(wl, tc.inputs), raw); got != tc.wantChange {
				t.Errorf("whole PodSpec compare reports a change: %v, want %v", got, tc.wantChange)
			}
		})
	}
}

func TestInfoEffectiveResources(t *testing.T) {
	for name, admitted := range map[string]bool{"pending": false, "reserved": true} {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			rc := utiltesting.MakeRuntimeClass("runtime", "handler").PodOverhead(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}).Obj()
			cl := utiltesting.NewClientBuilder().WithObjects(rc).
				WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType).Build()
			wl := utiltestingapi.MakeWorkload("wl", "ns").PodSets(*utiltestingapi.MakePodSet("main", 2).Limit(corev1.ResourceCPU, "3").RuntimeClass("runtime").Obj()).Obj()
			wantCPU := int64(8000)
			if admitted {
				wl.Status.Admission = &kueue.Admission{
					ClusterQueue:      "cq",
					PodSetAssignments: []kueue.PodSetAssignment{{Name: "main", ResourceUsage: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("6")}}},
				}
				wantCPU = 6000
			}
			original := wl.DeepCopy()
			info := NewInfoFromClient(ctx, cl, wl)
			if info.Obj != wl {
				t.Fatal("Info did not retain the original object")
			}
			if diff := cmp.Diff(original, wl); diff != "" {
				t.Fatalf("raw workload changed: %s", diff)
			}
			if got := info.TotalRequests[0].Requests.ResourceValue(corev1.ResourceCPU); got.CmpInt64(wantCPU) != 0 {
				t.Errorf("quota CPU = %s, want %d", got, wantCPU)
			}
			if got := resources.NewRequestsFromPodSpec(info.PodSpec(0)).ResourceValue(corev1.ResourceCPU); got.CmpInt64(4000) != 0 {
				t.Errorf("single pod CPU = %s, want 4000", got)
			}
		})
	}
}

func TestInfoDefaultRefreshWithUnchangedTotalRequests(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.SchedulingEquivalenceHashing, true)
	ctx, _ := utiltesting.ContextWithLog(t)
	lr := utiltesting.MakeLimitRange("defaults", "ns").WithValue("DefaultRequest", corev1.ResourceCPU, "1").Obj()
	cl := utiltesting.NewClientBuilder().WithObjects(lr).
		WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType).Build()
	wl := utiltestingapi.MakeWorkload("wl", "ns").Request(corev1.ResourceCPU, "4").Obj()
	wl.ResourceVersion = "7"
	wl.UID = "stable"
	wl.Spec.PodSets[0].Template.Spec.InitContainers = []corev1.Container{{Name: "init"}}
	original := wl.DeepCopy()
	info := NewInfoFromClient(ctx, cl, wl)
	snapshot := *info
	oldHash := info.SchedulingHash
	lr.Spec.Limits[0].DefaultRequest[corev1.ResourceCPU] = resource.MustParse("2")
	if err := cl.Update(ctx, lr); err != nil {
		t.Fatal(err)
	}
	info.UpdateFromClient(ctx, cl, wl)
	if got := info.TotalRequests[0].Requests.ResourceValue(corev1.ResourceCPU); got.CmpInt64(4000) != 0 {
		t.Fatalf("CPU total changed: %s", got)
	}
	if info.SchedulingHash == oldHash {
		t.Fatal("effective init requests changed but scheduling hash was reused")
	}
	if got := info.PodSpec(0).InitContainers[0].Resources.Requests[corev1.ResourceCPU]; got.Cmp(resource.MustParse("2")) != 0 {
		t.Errorf("init CPU = %s, want 2", got.String())
	}
	if got := snapshot.PodSpec(0).InitContainers[0].Resources.Requests[corev1.ResourceCPU]; got.Cmp(resource.MustParse("1")) != 0 {
		t.Errorf("updating Info mutated an existing snapshot: %s", got.String())
	}
	if diff := cmp.Diff(original, wl); diff != "" {
		t.Fatalf("raw workload changed: %s", diff)
	}
	if err := cl.Delete(ctx, lr); err != nil {
		t.Fatal(err)
	}
	info.UpdateFromClient(ctx, cl, wl)
	if len(info.PodSpec(0).InitContainers[0].Resources.Requests) != 0 {
		t.Error("deleted LimitRange defaults survived refresh")
	}
}

func TestInfoValidationUsesEffectiveResources(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	lr := utiltesting.MakeLimitRange("defaults", "ns").WithValue("Default", corev1.ResourceCPU, "1").Obj()
	cl := utiltesting.NewClientBuilder().WithObjects(lr, &corev1.Namespace{Name: "ns"}).
		WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType).Build()
	wl := utiltestingapi.MakeWorkload("wl", "ns").Request(corev1.ResourceCPU, "2").Obj()
	info := NewInfoFromClient(ctx, cl, wl)
	if errs := ValidateResources(info); len(errs) != 1 {
		t.Errorf("expected effective request 2 > default limit 1 rejection, got %v", errs)
	}
	if len(wl.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Limits) != 0 {
		t.Fatal("validation view leaked into raw workload")
	}
}

func TestInfoCarriesEffectiveSnapshotAcrossDefaultChanges(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	lr := utiltesting.MakeLimitRange("defaults", "ns").WithValue("DefaultRequest", corev1.ResourceCPU, "1").Obj()
	cl := utiltesting.NewClientBuilder().WithObjects(lr).
		WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType).Build()
	wl := utiltestingapi.MakeWorkload("wl", "ns").Obj()
	initial := NewInfoFromClient(ctx, cl, wl)
	lr.Spec.Limits[0].DefaultRequest[corev1.ResourceCPU] = resource.MustParse("2")
	if err := cl.Update(ctx, lr); err != nil {
		t.Fatal(err)
	}
	carried := NewInfoFromClient(ctx, cl, wl, WithEffectivePodSpecs(initial.EffectivePodSpecs))
	refreshed := NewInfoFromClient(ctx, cl, wl)
	if got := carried.TotalRequests[0].Requests.ResourceValue(corev1.ResourceCPU); got.CmpInt64(1000) != 0 {
		t.Errorf("carried CPU = %s, want 1000", got)
	}
	if got := refreshed.TotalRequests[0].Requests.ResourceValue(corev1.ResourceCPU); got.CmpInt64(2000) != 0 {
		t.Errorf("fresh CPU = %s, want 2000", got)
	}
}

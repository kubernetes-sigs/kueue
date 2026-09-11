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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

// EffectivePodSpecs must (1) leave the workload untouched and (2) produce
// exactly what AdjustResources writes into a mutated copy, for every input
// combination.
func TestEffectivePodSpecsMatchAdjustResources(t *testing.T) {
	runtimeClass := utiltesting.MakeRuntimeClass("kata", "handler").
		PodOverhead(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}).
		Obj()
	limitRange := utiltesting.MakeLimitRange("limits", "ns").
		WithValue("Default", corev1.ResourceCPU, "4").
		WithValue("DefaultRequest", corev1.ResourceCPU, "2").
		Obj()

	cases := map[string]*kueue.Workload{
		"nothing set": utiltestingapi.MakeWorkload("wl", "ns").
			PodSets(*utiltestingapi.MakePodSet("main", 1).Obj()).
			Obj(),
		"limits only": utiltestingapi.MakeWorkload("wl", "ns").
			PodSets(*utiltestingapi.MakePodSet("main", 1).
				Limit(corev1.ResourceCPU, "3").Obj()).
			Obj(),
		"requests set": utiltestingapi.MakeWorkload("wl", "ns").
			PodSets(*utiltestingapi.MakePodSet("main", 1).
				Request(corev1.ResourceCPU, "1").Obj()).
			Obj(),
		"overhead via runtime class": utiltestingapi.MakeWorkload("wl", "ns").
			PodSets(*utiltestingapi.MakePodSet("main", 1).
				RuntimeClass("kata").
				Limit(corev1.ResourceCPU, "3").Obj()).
			Obj(),
		"missing runtime class": utiltestingapi.MakeWorkload("wl", "ns").
			PodSets(*utiltestingapi.MakePodSet("main", 1).
				RuntimeClass("missing").
				Limit(corev1.ResourceCPU, "3").Obj()).
			Obj(),
		"pod-level limits": utiltestingapi.MakeWorkload("wl", "ns").
			PodSets(*utiltestingapi.MakePodSet("main", 1).
				PodLevelLimit(corev1.ResourceMemory, "2Gi").Obj()).
			Obj(),
		"two podsets mixed": utiltestingapi.MakeWorkload("wl", "ns").
			PodSets(
				*utiltestingapi.MakePodSet("a", 1).Limit(corev1.ResourceCPU, "3").Obj(),
				*utiltestingapi.MakePodSet("b", 1).RuntimeClass("kata").Obj(),
			).
			Obj(),
	}

	for name, wl := range cases {
		t.Run(name, func(t *testing.T) {
			cl := utiltesting.NewClientBuilder().
				WithObjects(runtimeClass, limitRange).
				WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType).
				Build()
			ctx, _ := utiltesting.ContextWithLog(t)

			original := wl.DeepCopy()

			adjusted := wl.DeepCopy()
			AdjustResources(ctx, cl, adjusted)

			in, _ := ResolveAdjustmentInputs(ctx, cl, wl)
			effective := EffectivePodSpecs(wl, in)

			if diff := cmp.Diff(original, wl); diff != "" {
				t.Errorf("EffectivePodSpecs mutated the workload (-want,+got):\n%s", diff)
			}
			for i := range effective {
				if diff := cmp.Diff(adjusted.Spec.PodSets[i].Template.Spec, effective[i]); diff != "" {
					t.Errorf("podSet %d effective spec differs from AdjustResources (-adjusted,+effective):\n%s", i, diff)
				}
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
			if got := info.TotalRequests[0].Requests.ResourceValue(corev1.ResourceCPU); got != wantCPU {
				t.Errorf("quota CPU = %d, want %d", got, wantCPU)
			}
			if got := resources.NewRequestsFromPodSpec(info.PodSpec(0)).ResourceValue(corev1.ResourceCPU); got != 4000 {
				t.Errorf("single pod CPU = %d, want 4000", got)
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
	if got := info.TotalRequests[0].Requests.ResourceValue(corev1.ResourceCPU); got != 4000 {
		t.Fatalf("CPU total changed: %d", got)
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
	cl := utiltesting.NewClientBuilder().WithObjects(lr, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns"}}).
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
	if got := carried.TotalRequests[0].Requests.ResourceValue(corev1.ResourceCPU); got != 1000 {
		t.Errorf("carried CPU = %d, want 1000", got)
	}
	if got := refreshed.TotalRequests[0].Requests.ResourceValue(corev1.ResourceCPU); got != 2000 {
		t.Errorf("fresh CPU = %d, want 2000", got)
	}
}

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
	versionutil "k8s.io/apimachinery/pkg/util/version"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
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
		"pod-level request aggregates the container requests": {
			wl: utiltestingapi.MakeWorkload("wl", "ns").
				PodSets(*utiltestingapi.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "1").
					PodLevelLimit(corev1.ResourceCPU, "4").Obj()).
				Obj(),
			wantPodSpecs: []corev1.PodSpec{
				utiltestingapi.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "1").
					Limit(corev1.ResourceCPU, "4").
					PodLevelLimit(corev1.ResourceCPU, "4").
					PodLevelRequest(corev1.ResourceCPU, "1").Template.Spec,
			},
		},
		"pod-level request aggregates the container limits": {
			wl: utiltestingapi.MakeWorkload("wl", "ns").
				PodSets(*utiltestingapi.MakePodSet("main", 1).
					Limit(corev1.ResourceCPU, "1").
					PodLevelLimit(corev1.ResourceCPU, "4").Obj()).
				Obj(),
			wantPodSpecs: []corev1.PodSpec{
				utiltestingapi.MakePodSet("main", 1).
					Limit(corev1.ResourceCPU, "1").
					Request(corev1.ResourceCPU, "1").
					PodLevelLimit(corev1.ResourceCPU, "4").
					PodLevelRequest(corev1.ResourceCPU, "1").Template.Spec,
			},
		},
		"pod-level limits": {
			wl: utiltestingapi.MakeWorkload("wl", "ns").
				PodSets(*utiltestingapi.MakePodSet("main", 1).
					PodLevelLimit(corev1.ResourceMemory, "2Gi").Obj()).
				Obj(),
			wantPodSpecs: []corev1.PodSpec{
				utiltestingapi.MakePodSet("main", 1).
					PodLevelLimit(corev1.ResourceMemory, "2Gi").
					PodLevelRequest(corev1.ResourceMemory, "2Gi").PodLevelRequest(corev1.ResourceCPU, "2").
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
	if got := carried.TotalRequests[0].Requests.ResourceValue(corev1.ResourceCPU); got != 1000 {
		t.Errorf("carried CPU = %d, want 1000", got)
	}
	if got := refreshed.TotalRequests[0].Requests.ResourceValue(corev1.ResourceCPU); got != 2000 {
		t.Errorf("fresh CPU = %d, want 2000", got)
	}
}

type fakeServerVersionFetcher struct {
	version versionutil.Version
}

func (f fakeServerVersionFetcher) GetServerVersion() versionutil.Version {
	return f.version
}

func TestUsesLegacyPodLevelDefaulting(t *testing.T) {
	cases := map[string]struct {
		version string
		want    bool
	}{
		"1.34":             {version: "1.34.11", want: true},
		"1.35":             {version: "1.35.8", want: true},
		"1.36":             {version: "1.36.4", want: true},
		"1.37":             {version: "1.37.0", want: false},
		"1.38":             {version: "1.38.1", want: false},
		"1.36 pre-release": {version: "1.36.0-rc.1", want: true},
		"1.37 pre-release": {version: "1.37.0-rc.0", want: false},
		"not fetched yet":  {want: false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			version := versionutil.Version{}
			if tc.version != "" {
				version = *versionutil.MustParseSemantic(tc.version)
			}
			if got := UsesLegacyPodLevelDefaulting(version); got != tc.want {
				t.Errorf("UsesLegacyPodLevelDefaulting(%q) = %t, want %t", tc.version, got, tc.want)
			}
		})
	}
}

// TestEffectivePodSpecsPodLevelDefaultingModes verifies the effective pod spec
// follows the pod-level defaulting of the API server version: 1.37 and newer
// default the pod-level resources from the container aggregates after the
// LimitRanger container defaults; older servers default them before the
// container defaults, and the requests only when the pod has pod-level limits
// (the hugepage limits defaulted from the containers count as such).
func TestEffectivePodSpecsPodLevelDefaultingModes(t *testing.T) {
	partiallySpecifiedContainers := func(secondRequest string) []corev1.Container {
		second := corev1.Container{Name: "second"}
		if secondRequest != "" {
			second.Resources.Requests = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(secondRequest)}
		}
		return []corev1.Container{
			{
				Name:      "first",
				Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}},
			},
			second,
		}
	}

	hugePages2Mi := corev1.ResourceName(corev1.ResourceHugePagesPrefix + "2Mi")
	hugePageContainers := func() []corev1.Container {
		return []corev1.Container{{
			Name: "first",
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
				Limits:   corev1.ResourceList{hugePages2Mi: resource.MustParse("2Mi")},
			},
		}}
	}
	hugePageContainersEffective := func() []corev1.Container {
		return []corev1.Container{{
			Name: "first",
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("1"),
					hugePages2Mi:       resource.MustParse("2Mi"),
				},
				Limits: corev1.ResourceList{hugePages2Mi: resource.MustParse("2Mi")},
			},
		}}
	}

	cases := map[string]struct {
		legacy            bool
		limitRangeDefault string
		wl                *kueue.Workload
		wantPodSpecs      []corev1.PodSpec
	}{
		"no container requests, 1.37+ defaults from the LimitRange default": {
			limitRangeDefault: "3",
			wl: utiltestingapi.MakeWorkload("wl", "ns").
				PodSets(*utiltestingapi.MakePodSet("main", 1).
					PodLevelLimit(corev1.ResourceCPU, "4").Obj()).
				Obj(),
			wantPodSpecs: []corev1.PodSpec{
				utiltestingapi.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "3").
					PodLevelLimit(corev1.ResourceCPU, "4").
					PodLevelRequest(corev1.ResourceCPU, "3").Template.Spec,
			},
		},
		"no container requests, legacy server keeps the pod-level limit as request": {
			legacy:            true,
			limitRangeDefault: "3",
			wl: utiltestingapi.MakeWorkload("wl", "ns").
				PodSets(*utiltestingapi.MakePodSet("main", 1).
					PodLevelLimit(corev1.ResourceCPU, "4").Obj()).
				Obj(),
			wantPodSpecs: []corev1.PodSpec{
				utiltestingapi.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "3").
					PodLevelLimit(corev1.ResourceCPU, "4").
					PodLevelRequest(corev1.ResourceCPU, "4").Template.Spec,
			},
		},
		"partially specified containers, 1.37+ aggregates the container defaults": {
			limitRangeDefault: "1",
			wl: utiltestingapi.MakeWorkload("wl", "ns").
				PodSets(*utiltestingapi.MakePodSet("main", 1).
					Containers(partiallySpecifiedContainers("")...).
					PodLevelLimit(corev1.ResourceCPU, "4").Obj()).
				Obj(),
			wantPodSpecs: []corev1.PodSpec{
				utiltestingapi.MakePodSet("main", 1).
					Containers(partiallySpecifiedContainers("1")...).
					PodLevelLimit(corev1.ResourceCPU, "4").
					PodLevelRequest(corev1.ResourceCPU, "2").Template.Spec,
			},
		},
		"partially specified containers, legacy server aggregates before the defaults": {
			legacy:            true,
			limitRangeDefault: "1",
			wl: utiltestingapi.MakeWorkload("wl", "ns").
				PodSets(*utiltestingapi.MakePodSet("main", 1).
					Containers(partiallySpecifiedContainers("")...).
					PodLevelLimit(corev1.ResourceCPU, "4").Obj()).
				Obj(),
			wantPodSpecs: []corev1.PodSpec{
				utiltestingapi.MakePodSet("main", 1).
					Containers(partiallySpecifiedContainers("1")...).
					PodLevelLimit(corev1.ResourceCPU, "4").
					PodLevelRequest(corev1.ResourceCPU, "1").Template.Spec,
			},
		},
		"pod-level requests without limits, 1.37+ fills missing requests": {
			limitRangeDefault: "1",
			wl: utiltestingapi.MakeWorkload("wl", "ns").
				PodSets(*utiltestingapi.MakePodSet("main", 1).
					PodLevelRequest(corev1.ResourceMemory, "1Gi").Obj()).
				Obj(),
			wantPodSpecs: []corev1.PodSpec{
				utiltestingapi.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "1").
					PodLevelRequest(corev1.ResourceMemory, "1Gi").
					PodLevelRequest(corev1.ResourceCPU, "1").Template.Spec,
			},
		},
		"pod-level requests without limits, legacy server leaves them alone": {
			legacy:            true,
			limitRangeDefault: "1",
			wl: utiltestingapi.MakeWorkload("wl", "ns").
				PodSets(*utiltestingapi.MakePodSet("main", 1).
					PodLevelRequest(corev1.ResourceMemory, "1Gi").Obj()).
				Obj(),
			wantPodSpecs: []corev1.PodSpec{
				utiltestingapi.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "1").
					PodLevelRequest(corev1.ResourceMemory, "1Gi").Template.Spec,
			},
		},
		"container hugepage limits, 1.37+ defaults the pod-level hugepage limit and request": {
			limitRangeDefault: "1",
			wl: utiltestingapi.MakeWorkload("wl", "ns").
				PodSets(*utiltestingapi.MakePodSet("main", 1).
					Containers(hugePageContainers()...).
					PodLevelLimit(corev1.ResourceCPU, "4").Obj()).
				Obj(),
			wantPodSpecs: []corev1.PodSpec{
				utiltestingapi.MakePodSet("main", 1).
					Containers(hugePageContainersEffective()...).
					PodLevelLimit(corev1.ResourceCPU, "4").
					PodLevelLimit(hugePages2Mi, "2Mi").
					PodLevelRequest(corev1.ResourceCPU, "1").
					PodLevelRequest(hugePages2Mi, "2Mi").Template.Spec,
			},
		},
		"container hugepage limits, legacy server also defaults the pod-level hugepage limit and request": {
			legacy:            true,
			limitRangeDefault: "1",
			wl: utiltestingapi.MakeWorkload("wl", "ns").
				PodSets(*utiltestingapi.MakePodSet("main", 1).
					Containers(hugePageContainers()...).
					PodLevelLimit(corev1.ResourceCPU, "4").Obj()).
				Obj(),
			wantPodSpecs: []corev1.PodSpec{
				utiltestingapi.MakePodSet("main", 1).
					Containers(hugePageContainersEffective()...).
					PodLevelLimit(corev1.ResourceCPU, "4").
					PodLevelLimit(hugePages2Mi, "2Mi").
					PodLevelRequest(corev1.ResourceCPU, "1").
					PodLevelRequest(hugePages2Mi, "2Mi").Template.Spec,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			limitRange := utiltesting.MakeLimitRange("limits", "ns").
				WithValue("DefaultRequest", corev1.ResourceCPU, tc.limitRangeDefault).
				Obj()
			cl := utiltesting.NewClientBuilder().
				WithObjects(limitRange).
				WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType).
				Build()
			ctx, _ := utiltesting.ContextWithLog(t)

			in, errs := ResolveAdjustmentInputs(ctx, cl, tc.wl)
			if len(errs) > 0 {
				t.Fatalf("ResolveAdjustmentInputs returned errors: %v", errs)
			}
			in.LegacyPodLevelDefaulting = tc.legacy

			if diff := cmp.Diff(tc.wantPodSpecs, EffectivePodSpecs(tc.wl, in)); diff != "" {
				t.Errorf("Unexpected effective PodSpecs (-want,+got):\n%s", diff)
			}
		})
	}
}

// TestUpdateFromClientServerVersionDefaulting verifies the Info resolves the
// pod-level defaulting mode from the provided server version fetcher.
func TestUpdateFromClientServerVersionDefaulting(t *testing.T) {
	limitRange := utiltesting.MakeLimitRange("limits", "ns").
		WithValue("DefaultRequest", corev1.ResourceCPU, "1").
		Obj()
	cl := utiltesting.NewClientBuilder().
		WithObjects(limitRange).
		WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType).
		Build()
	wl := utiltestingapi.MakeWorkload("wl", "ns").
		PodSets(*utiltestingapi.MakePodSet("main", 1).
			PodLevelLimit(corev1.ResourceCPU, "4").Obj()).
		Obj()

	cases := map[string]struct {
		version versionutil.Version
		wantCPU string
	}{
		"1.36 server":         {version: *versionutil.MustParseSemantic("1.36.4"), wantCPU: "4"},
		"1.37 server":         {version: *versionutil.MustParseSemantic("1.37.0"), wantCPU: "1"},
		"version not fetched": {wantCPU: "1"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			info := NewInfoFromClient(ctx, cl, wl, WithServerVersionFetcher(fakeServerVersionFetcher{version: tc.version}))
			if len(info.EffectivePodSpecs) != 1 || info.EffectivePodSpecs[0].Resources == nil {
				t.Fatal("expected the effective pod spec to carry pod-level resources")
			}
			got := info.EffectivePodSpecs[0].Resources.Requests[corev1.ResourceCPU]
			if got.Cmp(resource.MustParse(tc.wantCPU)) != 0 {
				t.Errorf("pod-level CPU request = %s, want %s", got.String(), tc.wantCPU)
			}
		})
	}
}

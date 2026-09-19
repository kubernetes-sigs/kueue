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
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
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
		wantErr      bool
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
			wantErr: true,
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
				Build()
			ctx, _ := utiltesting.ContextWithLog(t)
			original := tc.wl.DeepCopy()

			in, err := ResolveAdjustmentInputs(ctx, cl, tc.wl)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ResolveAdjustmentInputs() error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr && !apierrors.IsNotFound(err) {
				t.Errorf("expected apierrors.IsNotFound, got: %v", err)
			}
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
			cl := utiltesting.NewClientBuilder().WithObjects(rc).Build()
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
			info, err := NewInfoFromClient(ctx, cl, wl)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
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
	cl := utiltesting.NewClientBuilder().WithObjects(lr).Build()
	wl := utiltestingapi.MakeWorkload("wl", "ns").Request(corev1.ResourceCPU, "4").Obj()
	wl.ResourceVersion = "7"
	wl.UID = "stable"
	wl.Spec.PodSets[0].Template.Spec.InitContainers = []corev1.Container{{Name: "init"}}
	original := wl.DeepCopy()
	info, err := NewInfoFromClient(ctx, cl, wl)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := *info
	oldHash := info.SchedulingHash
	lr.Spec.Limits[0].DefaultRequest[corev1.ResourceCPU] = resource.MustParse("2")
	if err := cl.Update(ctx, lr); err != nil {
		t.Fatal(err)
	}
	if err := info.UpdateFromClient(ctx, cl, wl); err != nil {
		t.Fatal(err)
	}
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
	if err := info.UpdateFromClient(ctx, cl, wl); err != nil {
		t.Fatal(err)
	}
	if len(info.PodSpec(0).InitContainers[0].Resources.Requests) != 0 {
		t.Error("deleted LimitRange defaults survived refresh")
	}
}

func TestInfoValidationUsesEffectiveResources(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	lr := utiltesting.MakeLimitRange("defaults", "ns").WithValue("Default", corev1.ResourceCPU, "1").Obj()
	cl := utiltesting.NewClientBuilder().WithObjects(lr, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns"}}).Build()
	wl := utiltestingapi.MakeWorkload("wl", "ns").Request(corev1.ResourceCPU, "2").Obj()
	info, err := NewInfoFromClient(ctx, cl, wl)
	if err != nil {
		t.Fatal(err)
	}
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
	cl := utiltesting.NewClientBuilder().WithObjects(lr).Build()
	wl := utiltestingapi.MakeWorkload("wl", "ns").Obj()
	initial, err := NewInfoFromClient(ctx, cl, wl)
	if err != nil {
		t.Fatal(err)
	}
	lr.Spec.Limits[0].DefaultRequest[corev1.ResourceCPU] = resource.MustParse("2")
	if err := cl.Update(ctx, lr); err != nil {
		t.Fatal(err)
	}
	carried, err := NewInfoFromClient(ctx, cl, wl, WithEffectivePodSpecs(initial.EffectivePodSpecs))
	if err != nil {
		t.Fatal(err)
	}
	refreshed, err := NewInfoFromClient(ctx, cl, wl)
	if err != nil {
		t.Fatal(err)
	}
	if got := carried.TotalRequests[0].Requests.ResourceValue(corev1.ResourceCPU); got != 1000 {
		t.Errorf("carried CPU = %d, want 1000", got)
	}
	if got := refreshed.TotalRequests[0].Requests.ResourceValue(corev1.ResourceCPU); got != 2000 {
		t.Errorf("fresh CPU = %d, want 2000", got)
	}
}

func TestResolveAdjustmentInputsErrors(t *testing.T) {
	testErr := errors.New("simulated network failure")

	cases := map[string]struct {
		clientBuilder func() client.Client
		wl            *kueue.Workload
		wantNotFound  bool
		wantInternal  bool
	}{
		"missing RuntimeClass returns NotFound error and not ErrInternal": {
			clientBuilder: func() client.Client {
				return utiltesting.NewClientBuilder().Build()
			},
			wl: utiltestingapi.MakeWorkload("wl", "ns").PodSets(
				*utiltestingapi.MakePodSet("main", 1).RuntimeClass("non-existent").Obj(),
			).Obj(),
			wantNotFound: true,
			wantInternal: false,
		},
		"client error fetching RuntimeClass returns ErrInternal": {
			clientBuilder: func() client.Client {
				return utiltesting.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
					Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if _, ok := obj.(*nodev1.RuntimeClass); ok {
							return testErr
						}
						return cl.Get(ctx, key, obj, opts...)
					},
				}).Build()
			},
			wl: utiltestingapi.MakeWorkload("wl", "ns").PodSets(
				*utiltestingapi.MakePodSet("main", 1).RuntimeClass("runtime").Obj(),
			).Obj(),
			wantNotFound: false,
			wantInternal: true,
		},
		"client error listing LimitRanges returns ErrInternal": {
			clientBuilder: func() client.Client {
				return utiltesting.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
					List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
						if _, ok := list.(*corev1.LimitRangeList); ok {
							return testErr
						}
						return cl.List(ctx, list, opts...)
					},
				}).Build()
			},
			wl:           utiltestingapi.MakeWorkload("wl", "ns").Obj(),
			wantNotFound: false,
			wantInternal: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			cl := tc.clientBuilder()

			_, err := ResolveAdjustmentInputs(ctx, cl, tc.wl)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if tc.wantNotFound && !apierrors.IsNotFound(err) {
				t.Errorf("expected apierrors.IsNotFound, got: %v", err)
			}
			if gotInternal := errors.Is(err, ErrInternal); gotInternal != tc.wantInternal {
				t.Errorf("errors.Is(err, ErrInternal) = %v, want %v", gotInternal, tc.wantInternal)
			}

			// Also verify NewInfoFromClient propagates the same error and records AdjustmentErr.
			info, infoErr := NewInfoFromClient(ctx, cl, tc.wl)
			if infoErr == nil {
				t.Fatal("NewInfoFromClient expected error, got nil")
			}
			if info.AdjustmentErr != infoErr {
				t.Errorf("info.AdjustmentErr (%v) != infoErr (%v)", info.AdjustmentErr, infoErr)
			}
		})
	}
}

func TestValidateAdmissibilityAdjustmentError(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	cl := utiltesting.NewClientBuilder().WithObjects(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns"}},
	).Build()

	wl := utiltestingapi.MakeWorkload("wl", "ns").PodSets(
		*utiltestingapi.MakePodSet("main", 1).RuntimeClass("missing-rc").Obj(),
	).Obj()

	info, err := NewInfoFromClient(ctx, cl, wl)
	if err == nil {
		t.Fatal("expected NewInfoFromClient to return error for missing RuntimeClass")
	}

	admErr := ValidateAdmissibility(ctx, cl, info, nil)
	if admErr == nil {
		t.Fatal("expected ValidateAdmissibility to fail with AdjustmentErr, got nil")
	}
	if !apierrors.IsNotFound(admErr) {
		t.Errorf("expected ValidateAdmissibility error to preserve NotFound, got: %v", admErr)
	}
}

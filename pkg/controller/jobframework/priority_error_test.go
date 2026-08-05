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

package jobframework

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
)

// TestExtractPriorityClassifiesMissingWorkloadPriorityClass pins which lookup
// failure is a missing WorkloadPriorityClass, and that saying so does not cost
// the original error its identity: wrapping it in a sentinel would make
// apierrors.IsNotFound false for every caller above.
func TestExtractPriorityClassifiesMissingWorkloadPriorityClass(t *testing.T) {
	podSets := []kueue.PodSet{*utiltestingapi.MakePodSet("main", 1).Obj()}

	cases := map[string]struct {
		job             *batchv1.Job
		podSets         []kueue.PodSet
		objects         []client.Object
		wantMissingWPC  bool
		wantMissingName string
		wantNotFound    bool
	}{
		"an explicitly referenced class that does not exist": {
			job:             testingjob.MakeJob("job", "ns").WorkloadPriorityClass("missing").Obj(),
			wantMissingWPC:  true,
			wantMissingName: "missing",
			wantNotFound:    true,
		},
		"an explicitly referenced class that exists": {
			job: testingjob.MakeJob("job", "ns").WorkloadPriorityClass("present").Obj(),
			objects: []client.Object{
				utiltestingapi.MakeWorkloadPriorityClass("present").PriorityValue(10).Obj(),
			},
		},
		"no class referenced at all": {
			job: testingjob.MakeJob("job", "ns").Obj(),
		},
		// The boundary this change draws: scheduling.k8s.io is a different
		// resource and stays with its existing handling.
		"a pod template naming a PriorityClass that does not exist": {
			job:          testingjob.MakeJob("job", "ns").Obj(),
			podSets:      []kueue.PodSet{*utiltestingapi.MakePodSet("main", 1).PriorityClass("missing-pc").Obj()},
			wantNotFound: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cl := utiltesting.NewClientBuilder().WithObjects(tc.objects...).Build()
			if tc.podSets == nil {
				tc.podSets = podSets
			}

			_, _, err := ExtractPriority(t.Context(), cl, tc.job, tc.podSets, nil)

			// wantNotFound doubles as whether the row expects an error at all.
			if !tc.wantNotFound && err != nil {
				t.Fatalf("ExtractPriority() = %v, want no error", err)
			}

			var missing *workloadPriorityClassNotFoundError
			if got := errors.As(err, &missing); got != tc.wantMissingWPC {
				t.Fatalf("recognised as a missing WorkloadPriorityClass = %v, want %v (err: %v)",
					got, tc.wantMissingWPC, err)
			}
			if tc.wantMissingWPC && missing.name != tc.wantMissingName {
				t.Errorf("missing class name = %q, want %q", missing.name, tc.wantMissingName)
			}
			if got := apierrors.IsNotFound(err); got != tc.wantNotFound {
				t.Errorf("apierrors.IsNotFound = %v, want %v; the API error has to survive wrapping",
					got, tc.wantNotFound)
			}
			if tc.wantMissingWPC {
				want := apierrors.NewNotFound(
					kueue.SchemeGroupVersion.WithResource("workloadpriorityclasses").GroupResource(),
					tc.wantMissingName)
				var status apierrors.APIStatus
				if !errors.As(err, &status) {
					t.Fatalf("the wrapped error no longer carries an API status: %v", err)
				}
				if diff := cmp.Diff(want.ErrStatus.Details, status.Status().Details); diff != "" {
					t.Errorf("the GroupResource did not survive wrapping (-want +got):\n%s", diff)
				}
			}
		})
	}
}

// TestExtractPriorityLeavesUnrelatedLookupFailuresAlone is the other half. A
// lookup that failed for another reason leaves the answer unknown, so telling a
// user their class does not exist would be a guess.
func TestExtractPriorityLeavesUnrelatedLookupFailuresAlone(t *testing.T) {
	forbidden := apierrors.NewForbidden(
		kueue.SchemeGroupVersion.WithResource("workloadpriorityclasses").GroupResource(),
		"denied", errors.New("not allowed"))
	cl := utiltesting.NewClientBuilder().
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*kueue.WorkloadPriorityClass); ok {
					return forbidden
				}
				return c.Get(ctx, key, obj, opts...)
			},
		}).Build()

	_, _, err := ExtractPriority(t.Context(), cl,
		testingjob.MakeJob("job", "ns").WorkloadPriorityClass("denied").Obj(),
		[]kueue.PodSet{*utiltestingapi.MakePodSet("main", 1).Obj()}, nil)

	var missing *workloadPriorityClassNotFoundError
	if errors.As(err, &missing) {
		t.Errorf("a Forbidden was reported as a missing WorkloadPriorityClass: %v", err)
	}
	if !apierrors.IsForbidden(err) {
		t.Errorf("apierrors.IsForbidden = false, want true; got %v", err)
	}
}

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

package classical

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

const (
	preemptorCQName = "preemptor-cq"
	otherCQName     = "other-cq"
	testFlavor      = "default"
)

func candidateInfo(t *testing.T, name, clusterQueue string, priority int32) *workload.Info {
	t.Helper()
	_, log := utiltesting.ContextWithLog(t)
	wl := utiltestingapi.MakeWorkload(name, "ns").
		Priority(priority).
		Request(corev1.ResourceCPU, "1").
		Obj()
	info := workload.NewInfo(log, wl)
	info.ClusterQueue = kueue.ClusterQueueReference(clusterQueue)
	// NewInfo derives TotalRequests from the pod sets but without an admission it
	// has no flavor assigned. Stamp the flavor so WorkloadUsesResources matches.
	for i := range info.TotalRequests {
		info.TotalRequests[i].Flavors = map[corev1.ResourceName]kueue.ResourceFlavorReference{
			corev1.ResourceCPU: testFlavor,
		}
	}
	return info
}

func preemptorCtx(t *testing.T, priority int32, within, reclaim kueue.PreemptionPolicy, borrow *kueue.BorrowWithinCohort) *HierarchicalPreemptionCtx {
	t.Helper()
	_, log := utiltesting.ContextWithLog(t)
	preemptorWl := utiltestingapi.MakeWorkload("preemptor", "ns").Priority(priority).Obj()
	cq := &schdcache.ClusterQueueSnapshot{
		Name: preemptorCQName,
		Preemption: kueue.ClusterQueuePreemption{
			WithinClusterQueue:  within,
			ReclaimWithinCohort: reclaim,
			BorrowWithinCohort:  borrow,
		},
	}
	return &HierarchicalPreemptionCtx{
		Log:               log,
		Wl:                preemptorWl,
		Cq:                cq,
		FrsNeedPreemption: sets.New(resources.FlavorResource{Flavor: testFlavor, Resource: corev1.ResourceCPU}),
		WorkloadOrdering:  workload.Ordering{},
	}
}

func TestClassifyPreemptionVariant(t *testing.T) {
	anyPolicy := kueue.PreemptionPolicyAny
	lowerPriority := kueue.PreemptionPolicyLowerPriority
	never := kueue.PreemptionPolicyNever
	borrowLowerPriority := &kueue.BorrowWithinCohort{Policy: kueue.BorrowWithinCohortPolicyLowerPriority}

	cases := map[string]struct {
		ctx       *HierarchicalPreemptionCtx
		candidate *workload.Info
		advantage bool
		want      preemptionVariant
	}{
		"does not use contested resources": {
			ctx: preemptorCtx(t, 10, anyPolicy, anyPolicy, nil),
			candidate: func() *workload.Info {
				c := candidateInfo(t, "c", otherCQName, 5)
				// no flavor -> WorkloadUsesResources false
				for i := range c.TotalRequests {
					c.TotalRequests[i].Flavors = nil
				}
				return c
			}(),
			want: Never,
		},
		"same queue satisfies policy": {
			ctx:       preemptorCtx(t, 10, anyPolicy, never, nil),
			candidate: candidateInfo(t, "c", preemptorCQName, 5),
			want:      WithinCQ,
		},
		"same queue lower-priority policy but candidate higher priority": {
			ctx:       preemptorCtx(t, 5, lowerPriority, never, nil),
			candidate: candidateInfo(t, "c", preemptorCQName, 10),
			want:      Never,
		},
		"hierarchical advantage wins over priority": {
			ctx:       preemptorCtx(t, 1, never, anyPolicy, nil),
			candidate: candidateInfo(t, "c", otherCQName, 100),
			advantage: true,
			want:      HiearchicalReclaim,
		},
		"no advantage, borrow forbidden -> without borrowing": {
			ctx:       preemptorCtx(t, 10, never, anyPolicy, nil),
			candidate: candidateInfo(t, "c", otherCQName, 5),
			advantage: false,
			want:      ReclaimWithoutBorrowing,
		},
		"no advantage, borrow allowed, candidate lower priority -> while borrowing": {
			ctx:       preemptorCtx(t, 10, never, anyPolicy, borrowLowerPriority),
			candidate: candidateInfo(t, "c", otherCQName, 5),
			advantage: false,
			want:      ReclaimWhileBorrowing,
		},
		"no advantage, borrow allowed, candidate not below preemptor -> without borrowing": {
			ctx:       preemptorCtx(t, 10, never, anyPolicy, borrowLowerPriority),
			candidate: candidateInfo(t, "c", otherCQName, 20),
			advantage: false,
			want:      ReclaimWithoutBorrowing,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := classifyPreemptionVariant(tc.ctx, tc.candidate, tc.advantage); got != tc.want {
				t.Errorf("classifyPreemptionVariant() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestNoRealCandidate covers the deduped helper feeding both
// NoCandidateFromOtherQueues and NoCandidateForHierarchicalReclaim: it reports
// true only when every workload of every class classifies as Never.
func TestNoRealCandidate(t *testing.T) {
	ctx := preemptorCtx(t, 10, kueue.PreemptionPolicyAny, kueue.PreemptionPolicyAny, nil)

	real1 := candidateInfo(t, "real1", otherCQName, 5)
	real2 := candidateInfo(t, "real2", otherCQName, 6)
	fake := candidateInfo(t, "fake", otherCQName, 5)
	for i := range fake.TotalRequests {
		fake.TotalRequests[i].Flavors = nil
	}

	cases := map[string]struct {
		classes []classifiedClusterQueue
		want    bool
	}{
		"no classes": {classes: nil, want: true},
		"only never candidates": {
			classes: []classifiedClusterQueue{
				{cq: &schdcache.ClusterQueueSnapshot{Name: otherCQName}, sorted: []*workload.Info{fake}},
			},
			want: true,
		},
		"one real candidate": {
			classes: []classifiedClusterQueue{
				{cq: &schdcache.ClusterQueueSnapshot{Name: otherCQName}, sorted: []*workload.Info{fake, real1}},
			},
			want: false,
		},
		"real candidate short-circuits across classes": {
			classes: []classifiedClusterQueue{
				{cq: &schdcache.ClusterQueueSnapshot{Name: otherCQName}, sorted: []*workload.Info{real2}},
				{cq: &schdcache.ClusterQueueSnapshot{Name: otherCQName}, sorted: []*workload.Info{fake}},
			},
			want: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := noRealCandidate(ctx, tc.classes); got != tc.want {
				t.Errorf("noRealCandidate() = %v, want %v", got, tc.want)
			}
		})
	}
}

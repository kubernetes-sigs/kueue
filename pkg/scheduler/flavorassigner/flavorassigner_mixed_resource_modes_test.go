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

package flavorassigner

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

// Quota, borrowing, and preemption are evaluated per resource, but the flavor
// summary must retain both requirements before deciding whether to look for
// victims or try another flavor.
func TestAssignFlavorsMixedBorrowingAndPreemption(t *testing.T) {
	tests := []struct {
		name           string
		preference     kueue.FlavorFungibilityPreference
		borrowCPU      bool
		withFallback   bool
		wantMode       FlavorAssignmentMode
		wantFlavor     kueue.ResourceFlavorReference
		wantBorrowing  int
	}{
		{
			name:          "preemption over borrowing retains memory preemption",
			preference:    kueue.PreemptionOverBorrowing,
			borrowCPU:     true,
			wantMode:      Preempt,
			wantFlavor:    "mixed",
			wantBorrowing: 1,
		},
		{
			name:          "borrowing over preemption retains memory preemption",
			preference:    kueue.BorrowingOverPreemption,
			borrowCPU:     true,
			wantMode:      Preempt,
			wantFlavor:    "mixed",
			wantBorrowing: 1,
		},
		{
			name:          "borrowing over preemption continues past mixed mode to a fit flavor",
			preference:    kueue.BorrowingOverPreemption,
			withFallback:  true,
			wantMode:      Fit,
			wantFlavor:    "fallback",
			wantBorrowing: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.FlavorFungibility, true)
			ctx, log := utiltesting.ContextWithLog(t)

			flavors := map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{
				"mixed": utiltestingapi.MakeResourceFlavor("mixed").Obj(),
			}
			primaryQuotas := []kueue.FlavorQuotas{
				*utiltestingapi.MakeFlavorQuotas("mixed").
					Resource(corev1.ResourceCPU, "4").
					Resource(corev1.ResourceMemory, "4Gi").
					Obj(),
			}
			lenderQuotas := []kueue.FlavorQuotas{
				*utiltestingapi.MakeFlavorQuotas("mixed").
					Resource(corev1.ResourceCPU, "4").
					Resource(corev1.ResourceMemory, "4Gi").
					Obj(),
			}

			used := resources.FlavorResourceQuantities{}
			podSet := utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1)
			if tc.borrowCPU {
				// CPU 1+6 exceeds cq-a's nominal 4, but fits with cq-b's 4.
				// Memory 4Gi+2Gi requires preempting the current workload.
				used[resources.FlavorResource{Flavor: "mixed", Resource: corev1.ResourceCPU}] = resources.NewAmount(1_000)
				used[resources.FlavorResource{Flavor: "mixed", Resource: corev1.ResourceMemory}] = resources.NewAmount(4 << 30)
				lenderQuotas = []kueue.FlavorQuotas{
					*utiltestingapi.MakeFlavorQuotas("mixed").
						Resource(corev1.ResourceCPU, "4").
						Resource(corev1.ResourceMemory, "0").
						Obj(),
				}
				podSet.Request(corev1.ResourceCPU, "6").Request(corev1.ResourceMemory, "2Gi")
			} else {
				// CPU needs a victim, while memory must borrow. Without keeping
				// the borrowing level, MayStopSearch prevents trying fallback.
				used[resources.FlavorResource{Flavor: "mixed", Resource: corev1.ResourceCPU}] = resources.NewAmount(4_000)
				podSet.Request(corev1.ResourceCPU, "2").Request(corev1.ResourceMemory, "6Gi")
			}

			if tc.withFallback {
				flavors["fallback"] = utiltestingapi.MakeResourceFlavor("fallback").Obj()
				primaryQuotas = append(primaryQuotas, *utiltestingapi.MakeFlavorQuotas("fallback").
					Resource(corev1.ResourceCPU, "4").
					Resource(corev1.ResourceMemory, "8Gi").
					Obj())
			}

			whenCanPreempt := kueue.TryNextFlavor
			if tc.withFallback {
				whenCanPreempt = kueue.MayStopSearch
			}
			cqA := utiltestingapi.MakeClusterQueue("cq-a").
				Cohort("cohort").
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
				}).
				FlavorFungibility(kueue.FlavorFungibility{
					WhenCanBorrow:  kueue.TryNextFlavor,
					WhenCanPreempt: whenCanPreempt,
					Preference:     &tc.preference,
				}).
				ResourceGroup(primaryQuotas...).Obj()
			cqB := utiltestingapi.MakeClusterQueue("cq-b").
				Cohort("cohort").
				ResourceGroup(lenderQuotas...).Obj()

			cache := schdcache.New(utiltesting.NewFakeClient())
			if err := cache.AddClusterQueue(ctx, cqA); err != nil {
				t.Fatalf("add cq-a: %v", err)
			}
			if err := cache.AddClusterQueue(ctx, cqB); err != nil {
				t.Fatalf("add cq-b: %v", err)
			}
			for _, flavor := range flavors {
				cache.AddOrUpdateResourceFlavor(log, flavor)
			}
			if err := cache.AddOrUpdateCohort(utiltestingapi.MakeCohort("cohort").Obj()); err != nil {
				t.Fatalf("add cohort: %v", err)
			}
			snapshot, err := cache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("snapshot: %v", err)
			}
			cqSnapshot := snapshot.ClusterQueue("cq-a")
			if cqSnapshot == nil {
				t.Fatal("cq-a missing from snapshot")
			}
			cqSnapshot.AddUsage(workload.Usage{
				Quota: workload.ResourceUsage{Assigned: used},
			})

			wl := workload.NewInfo(log, &kueue.Workload{
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{*podSet.Obj()},
				},
			})
			assigner := New(wl, cqSnapshot, flavors, false, &testOracle{}, nil,
				configapi.QuotaCheckBlockUndeclared, resources.NewResourceFormatter(), 0)
			assignment := assigner.AssignFlavors(ctx, log, nil)

			if got := assignment.RepresentativeMode(); got != tc.wantMode {
				t.Errorf("RepresentativeMode() = %s, want %s", got, tc.wantMode)
			}
			if got := assignment.Borrowing; got != tc.wantBorrowing {
				t.Errorf("Borrowing = %d, want %d", got, tc.wantBorrowing)
			}
			if len(assignment.PodSets) != 1 {
				t.Fatalf("got %d PodSets, want 1", len(assignment.PodSets))
			}
			ps := assignment.PodSets[0]
			for _, r := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
				flavor, ok := ps.Flavors[r]
				if !ok {
					t.Errorf("resource %s has no flavor assignment", r)
					continue
				}
				if flavor.Name != tc.wantFlavor {
					t.Errorf("resource %s assigned %s, want %s", r, flavor.Name, tc.wantFlavor)
				}
			}
			if got, want := ps.Status.IsFit(), tc.wantMode == Fit; got != want {
				t.Errorf("PodSet Status.IsFit() = %t, want %t", got, want)
			}
			if tc.borrowCPU {
				if got := ps.Flavors[corev1.ResourceCPU].Mode; got != Fit {
					t.Errorf("borrowed CPU mode = %s, want Fit", got)
				}
				if got := ps.Flavors[corev1.ResourceMemory].Mode; got != Preempt {
					t.Errorf("memory mode = %s, want Preempt", got)
				}
			}
		})
	}
}

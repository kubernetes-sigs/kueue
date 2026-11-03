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
	"k8s.io/apimachinery/pkg/api/resource"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestAssignFlavorsRecordsOnlyTotalsAQuantityCanStore(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.UnadmittedWorkloadsObservability, true)

	const gpu corev1.ResourceName = "example.com/gpu"
	// Two ClusterQueues of 5E, with borrowing, can admit 10E. 8E still fits in
	// a Quantity. 9.8E and 10E do not, and must not be written back as MaxInt64.
	nominal := "5000000000000000000"

	cases := map[string]struct {
		count      int
		perPod     string
		wantMode   FlavorAssignmentMode
		wantUsage  string
		wantReason string
		wantTotal  string
	}{
		"a borrowed total that fits in a Quantity is kept": {
			count:     2,
			perPod:    "4000000000000000000",
			wantMode:  Fit,
			wantUsage: "8000000000000000000",
		},
		"seven pods at 1.4E are not admitted": {
			count:      7,
			perPod:     "1400000000000000000",
			wantMode:   NoFit,
			wantReason: kueue.WorkloadQuotaReservedReasonUnrecordableResourceUsage,
			wantTotal:  "9800000000000000000",
		},
		"two pods at 5E are not admitted": {
			count:      2,
			perPod:     "5000000000000000000",
			wantMode:   NoFit,
			wantReason: kueue.WorkloadQuotaReservedReasonUnrecordableResourceUsage,
			wantTotal:  "10000000000000000000",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			flavor := utiltestingapi.MakeResourceFlavor("default").Obj()
			cqA := utiltestingapi.MakeClusterQueue("cq-a").
				Cohort("gpu-cohort").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).Resource(gpu, nominal).Obj()).
				Obj()
			cqB := utiltestingapi.MakeClusterQueue("cq-b").
				Cohort("gpu-cohort").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).Resource(gpu, nominal).Obj()).
				Obj()

			cache := schdcache.New(utiltesting.NewFakeClient())
			if err := cache.AddClusterQueue(ctx, cqA); err != nil {
				t.Fatalf("adding cq-a: %v", err)
			}
			if err := cache.AddClusterQueue(ctx, cqB); err != nil {
				t.Fatalf("adding cq-b: %v", err)
			}
			cache.AddOrUpdateResourceFlavor(log, flavor)
			snapshot, err := cache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("snapshot: %v", err)
			}
			cq := snapshot.ClusterQueue("cq-a")
			if cq == nil {
				t.Fatal("cq-a missing from snapshot")
			}

			wl := utiltestingapi.MakeWorkload("wl", "ns").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, tc.count).Request(gpu, tc.perPod).Obj()).
				Obj()
			assigner := New(
				workload.NewInfo(log, wl),
				cq,
				map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{kueue.ResourceFlavorReference(flavor.Name): flavor},
				false,
				&testOracle{},
				nil,
				configapi.QuotaCheckBlockUndeclared,
				resources.NewResourceFormatter(),
				0,
			)
			got := assigner.AssignFlavors(ctx, log, nil)
			if got.RepresentativeMode() != tc.wantMode {
				t.Fatalf("RepresentativeMode() = %s, want %s; message %q", got.RepresentativeMode(), tc.wantMode, got.Message())
			}
			if tc.wantMode == Fit {
				if len(got.PodSets) != 1 {
					t.Fatalf("PodSets = %d, want 1", len(got.PodSets))
				}
				ps := got.PodSets[0]
				usage, ok := ps.Requests[gpu]
				if !ok {
					t.Fatalf("Requests missing %s", gpu)
				}
				if !usage.Equal(resource.MustParse(tc.wantUsage)) {
					t.Errorf("resourceUsage candidate %s, want %s", usage.String(), tc.wantUsage)
				}
				assigned := got.Usage.Quota.Assigned[resources.FlavorResource{Flavor: "default", Resource: gpu}]
				if !assigned.Equal(resources.AmountFromQuantity(gpu, resource.MustParse(tc.wantUsage))) {
					t.Errorf("assigned usage %s, want %s", assigned, tc.wantUsage)
				}
				return
			}
			if got.NoFitReason != tc.wantReason {
				t.Errorf("NoFitReason = %q, want %q", got.NoFitReason, tc.wantReason)
			}
			if len(got.Usage.Quota.Assigned) != 0 {
				t.Errorf("assigned usage = %v, want none", got.Usage.Quota.Assigned)
			}
			if len(got.PodSets) != 1 || len(got.PodSets[0].Flavors) != 0 {
				t.Errorf("PodSet flavors = %#v, want none", got.PodSets)
			}
			wantMessage := "couldn't assign flavors to pod set main: resourceUsage cannot store the admitted total for example.com/gpu (" + tc.wantTotal + ")"
			if got.Message() != wantMessage {
				t.Errorf("Message() = %q, want %q", got.Message(), wantMessage)
			}
			assertUnrecordableFlavorAttempt(t, got.PodSets[0].FlavorAssignmentAttempts, "default")
		})
	}
}

func TestUnrecordableUsageOutranksAWorseFlavorAttempt(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.UnadmittedWorkloadsObservability, true)

	const gpu corev1.ResourceName = "example.com/gpu"
	nominal := "5000000000000000000"

	ctx, log := utiltesting.ContextWithLog(t)
	// The tainted flavor is listed first, so its NoFit attempt is recorded before
	// the flavor that fits. ResolveNoFitReason would otherwise keep that worse
	// reason and skip the flavor that fit.
	tainted := utiltestingapi.MakeResourceFlavor("tainted").
		Taint(corev1.Taint{Key: "spot", Value: "true", Effect: corev1.TaintEffectNoSchedule}).
		Obj()
	fitting := utiltestingapi.MakeResourceFlavor("default").Obj()
	cqA := utiltestingapi.MakeClusterQueue("cq-a").
		Cohort("gpu-cohort").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas(tainted.Name).Resource(gpu, nominal).Obj(),
			*utiltestingapi.MakeFlavorQuotas(fitting.Name).Resource(gpu, nominal).Obj(),
		).
		Obj()
	cqB := utiltestingapi.MakeClusterQueue("cq-b").
		Cohort("gpu-cohort").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas(fitting.Name).Resource(gpu, nominal).Obj()).
		Obj()

	cache := schdcache.New(utiltesting.NewFakeClient())
	for _, cq := range []*kueue.ClusterQueue{cqA, cqB} {
		if err := cache.AddClusterQueue(ctx, cq); err != nil {
			t.Fatalf("adding %s: %v", cq.Name, err)
		}
	}
	cache.AddOrUpdateResourceFlavor(log, tainted)
	cache.AddOrUpdateResourceFlavor(log, fitting)
	snapshot, err := cache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	cq := snapshot.ClusterQueue("cq-a")
	if cq == nil {
		t.Fatal("cq-a missing from snapshot")
	}

	wl := utiltestingapi.MakeWorkload("wl", "ns").
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).Request(gpu, nominal).Obj()).
		Obj()
	assigner := New(
		workload.NewInfo(log, wl),
		cq,
		map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{
			kueue.ResourceFlavorReference(tainted.Name): tainted,
			kueue.ResourceFlavorReference(fitting.Name): fitting,
		},
		false,
		&testOracle{},
		nil,
		configapi.QuotaCheckBlockUndeclared,
		resources.NewResourceFormatter(),
		0,
	)
	got := assigner.AssignFlavors(ctx, log, nil)
	if got.NoFitReason != kueue.WorkloadQuotaReservedReasonUnrecordableResourceUsage {
		t.Fatalf("NoFitReason = %q, want %q; message %q", got.NoFitReason, kueue.WorkloadQuotaReservedReasonUnrecordableResourceUsage, got.Message())
	}
	if len(got.PodSets) != 1 {
		t.Fatalf("PodSets = %d, want 1", len(got.PodSets))
	}
	attempts := got.PodSets[0].FlavorAssignmentAttempts
	assertUnrecordableFlavorAttempt(t, attempts, kueue.ResourceFlavorReference(fitting.Name))
	var taintedAttempt *FlavorAssignmentAttempt
	for i := range attempts {
		if attempts[i].Flavor == kueue.ResourceFlavorReference(tainted.Name) {
			taintedAttempt = &attempts[i]
		}
	}
	if taintedAttempt == nil {
		t.Fatalf("missing attempt for flavor %s: %#v", tainted.Name, attempts)
	}
	if taintedAttempt.Mode != NoFit || taintedAttempt.NoFitReason != kueue.WorkloadQuotaReservedReasonNoMatchingFlavor {
		t.Errorf("tainted attempt = mode %s reason %q, want NoFit %s", taintedAttempt.Mode, taintedAttempt.NoFitReason, kueue.WorkloadQuotaReservedReasonNoMatchingFlavor)
	}
}

func assertUnrecordableFlavorAttempt(t *testing.T, attempts []FlavorAssignmentAttempt, flavor kueue.ResourceFlavorReference) {
	t.Helper()
	for _, attempt := range attempts {
		if attempt.Flavor != flavor {
			continue
		}
		if attempt.Mode != NoFit || attempt.NoFitReason != kueue.WorkloadQuotaReservedReasonUnrecordableResourceUsage {
			t.Errorf("flavor %s attempt = mode %s reason %q, want NoFit %s", flavor, attempt.Mode, attempt.NoFitReason, kueue.WorkloadQuotaReservedReasonUnrecordableResourceUsage)
		}
		return
	}
	t.Errorf("missing attempt for flavor %s: %#v", flavor, attempts)
}

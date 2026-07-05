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

package preemption

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/component-base/featuregate"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	preemptioncommon "sigs.k8s.io/kueue/pkg/scheduler/preemption/common"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	utilslices "sigs.k8s.io/kueue/pkg/util/slices"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

// TestPreemptionProtectionClassical verifies that classical (non fair
// sharing) preemption skips cross-ClusterQueue reclaim candidates that are
// within their preemption-protection window, while leaving
// within-ClusterQueue candidates unaffected.
func TestPreemptionProtectionClassical(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	protectionDuration := 10 * time.Minute
	reclaimProtection := &config.PreemptionProtection{
		ReclaimWithinCohort: &config.PreemptionProtectionPolicy{
			MinAdmitDuration: &metav1.Duration{Duration: protectionDuration},
		},
	}
	fsOnlyProtection := &config.PreemptionProtection{
		FairSharing: &config.PreemptionProtectionPolicy{
			MinAdmitDuration: &metav1.Duration{Duration: protectionDuration},
		},
	}
	bothProtections := &config.PreemptionProtection{
		FairSharing: &config.PreemptionProtectionPolicy{
			MinAdmitDuration: &metav1.Duration{Duration: protectionDuration},
		},
		ReclaimWithinCohort: &config.PreemptionProtectionPolicy{
			MinAdmitDuration: &metav1.Duration{Duration: protectionDuration},
		},
	}
	flavors := []*kueue.ResourceFlavor{
		utiltestingapi.MakeResourceFlavor("default").Obj(),
	}
	clusterQueues := []*kueue.ClusterQueue{
		utiltestingapi.MakeClusterQueue("owner").
			Cohort("protection-cohort").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "6").Obj()).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
			}).
			Obj(),
		utiltestingapi.MakeClusterQueue("borrower").
			Cohort("protection-cohort").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "0").Obj()).
			Obj(),
	}
	borrowerWl := func(name string, admittedAt time.Time) kueue.Workload {
		return *utiltestingapi.MakeWorkload(name, "").
			Request(corev1.ResourceCPU, "3").
			SimpleReserveQuota("borrower", "default", now).
			AdmittedAt(true, admittedAt).
			Obj()
	}
	ownerLowWl := func(name string, admittedAt time.Time) kueue.Workload {
		return *utiltestingapi.MakeWorkload(name, "").
			Priority(-1).
			Request(corev1.ResourceCPU, "3").
			SimpleReserveQuota("owner", "default", now).
			AdmittedAt(true, admittedAt).
			Obj()
	}
	cases := map[string]struct {
		featureGates         map[featuregate.Feature]bool
		preemptionProtection *config.PreemptionProtection
		// clockAdvance shifts the preemptor's fake clock forward relative
		// to the workloads' admission times.
		clockAdvance time.Duration
		admitted     []kueue.Workload
		incoming     *kueue.Workload
		targetCQ     kueue.ClusterQueueReference
		wantTargets  sets.Set[string]
	}{
		"reclaim candidates within protection window are not preempted": {
			featureGates:         map[featuregate.Feature]bool{features.PreemptionProtection: true},
			preemptionProtection: reclaimProtection,
			admitted: []kueue.Workload{
				borrowerWl("b1", now),
				borrowerWl("b2", now),
			},
			incoming: utiltestingapi.MakeWorkload("in", "").Request(corev1.ResourceCPU, "6").Obj(),
			targetCQ: "owner",
		},
		"reclaim proceeds when the feature gate is disabled": {
			featureGates:         map[featuregate.Feature]bool{features.PreemptionProtection: false},
			preemptionProtection: reclaimProtection,
			admitted: []kueue.Workload{
				borrowerWl("b1", now),
				borrowerWl("b2", now),
			},
			incoming: utiltestingapi.MakeWorkload("in", "").Request(corev1.ResourceCPU, "6").Obj(),
			targetCQ: "owner",
			wantTargets: sets.New(
				targetKeyReason("/b1", kueue.InCohortReclamationReason),
				targetKeyReason("/b2", kueue.InCohortReclamationReason),
			),
		},
		"reclaim proceeds after the protection window elapses": {
			featureGates:         map[featuregate.Feature]bool{features.PreemptionProtection: true},
			preemptionProtection: reclaimProtection,
			clockAdvance:         protectionDuration + 5*time.Minute,
			admitted: []kueue.Workload{
				borrowerWl("b1", now),
				borrowerWl("b2", now),
			},
			incoming: utiltestingapi.MakeWorkload("in", "").Request(corev1.ResourceCPU, "6").Obj(),
			targetCQ: "owner",
			wantTargets: sets.New(
				targetKeyReason("/b1", kueue.InCohortReclamationReason),
				targetKeyReason("/b2", kueue.InCohortReclamationReason),
			),
		},
		"reclaim proceeds when runtime equals the protection duration exactly": {
			featureGates:         map[featuregate.Feature]bool{features.PreemptionProtection: true},
			preemptionProtection: reclaimProtection,
			clockAdvance:         protectionDuration,
			admitted: []kueue.Workload{
				borrowerWl("b1", now),
				borrowerWl("b2", now),
			},
			incoming: utiltestingapi.MakeWorkload("in", "").Request(corev1.ResourceCPU, "6").Obj(),
			targetCQ: "owner",
			wantTargets: sets.New(
				targetKeyReason("/b1", kueue.InCohortReclamationReason),
				targetKeyReason("/b2", kueue.InCohortReclamationReason),
			),
		},
		"reclaim is not affected by the fairSharing protection rule": {
			featureGates:         map[featuregate.Feature]bool{features.PreemptionProtection: true},
			preemptionProtection: fsOnlyProtection,
			admitted: []kueue.Workload{
				borrowerWl("b1", now),
				borrowerWl("b2", now),
			},
			incoming: utiltestingapi.MakeWorkload("in", "").Request(corev1.ResourceCPU, "6").Obj(),
			targetCQ: "owner",
			wantTargets: sets.New(
				targetKeyReason("/b1", kueue.InCohortReclamationReason),
				targetKeyReason("/b2", kueue.InCohortReclamationReason),
			),
		},
		"only the unprotected reclaim candidate is preempted": {
			featureGates:         map[featuregate.Feature]bool{features.PreemptionProtection: true},
			preemptionProtection: reclaimProtection,
			admitted: []kueue.Workload{
				borrowerWl("b1", now),
				borrowerWl("b2", now.Add(-time.Hour)),
			},
			incoming: utiltestingapi.MakeWorkload("in", "").Request(corev1.ResourceCPU, "3").Obj(),
			targetCQ: "owner",
			wantTargets: sets.New(
				targetKeyReason("/b2", kueue.InCohortReclamationReason),
			),
		},
		"already-evicted candidate is preempted even while recently admitted": {
			featureGates:         map[featuregate.Feature]bool{features.PreemptionProtection: true},
			preemptionProtection: reclaimProtection,
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("b1", "").
					Request(corev1.ResourceCPU, "3").
					SimpleReserveQuota("borrower", "default", now).
					AdmittedAt(true, now).
					EvictedAt(now).
					Obj(),
				borrowerWl("b2", now),
			},
			incoming: utiltestingapi.MakeWorkload("in", "").Request(corev1.ResourceCPU, "3").Obj(),
			targetCQ: "owner",
			wantTargets: sets.New(
				targetKeyReason("/b1", kueue.InCohortReclamationReason),
			),
		},
		"within-ClusterQueue candidates are unaffected by preemption protection": {
			featureGates:         map[featuregate.Feature]bool{features.PreemptionProtection: true},
			preemptionProtection: bothProtections,
			admitted: []kueue.Workload{
				ownerLowWl("low1", now),
				ownerLowWl("low2", now),
			},
			incoming: utiltestingapi.MakeWorkload("in", "").Request(corev1.ResourceCPU, "6").Obj(),
			targetCQ: "owner",
			wantTargets: sets.New(
				targetKeyReason("/low1", kueue.InClusterQueueReason),
				targetKeyReason("/low2", kueue.InClusterQueueReason),
			),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			ctx, log := utiltesting.ContextWithLog(t)
			cl := utiltesting.NewClientBuilder().
				WithLists(&kueue.WorkloadList{Items: tc.admitted}).
				Build()
			cqCache := schdcache.New(cl)
			for _, flv := range flavors {
				cqCache.AddOrUpdateResourceFlavor(log, flv)
			}
			for _, cq := range clusterQueues {
				if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
					t.Fatalf("Couldn't add ClusterQueue to cache: %v", err)
				}
			}

			recorder := &utiltesting.EventRecorder{}
			preemptor := New(cl, workload.Ordering{}, recorder, nil, tc.preemptionProtection, false,
				clocktesting.NewFakeClock(now.Add(tc.clockAdvance)), nil, preemptexpectations.New(), nil)

			beforeSnapshot, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}
			snapshotWorkingCopy, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}
			wlInfo := workload.NewInfo(tc.incoming)
			wlInfo.ClusterQueue = tc.targetCQ
			targets := preemptor.GetTargets(log, *wlInfo, singlePodSetAssignment(
				flavorassigner.ResourceAssignment{
					corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
						Name: "default", Mode: flavorassigner.Preempt,
					},
				},
			), snapshotWorkingCopy)
			gotTargets := sets.New(utilslices.Map(targets, func(t **Target) string {
				return targetKeyReason(workload.Key((*t).WorkloadInfo.Obj), (*t).Reason)
			})...)
			if diff := cmp.Diff(tc.wantTargets, gotTargets, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unexpected preemption targets (-want,+got):\n%s", diff)
			}

			if diff := cmp.Diff(beforeSnapshot, snapshotWorkingCopy, snapCmpOpts); diff != "" {
				t.Errorf("Snapshot was modified (-initial,+end):\n%s", diff)
			}
		})
	}
}

// TestGetTargetsSchedulesRetryAtProtectionExpiry verifies that GetTargets
// invokes the retryAfter callback with the earliest protection expiry among
// skipped candidates when target selection produced no targets, and does not
// invoke it when targets were found.
func TestGetTargetsSchedulesRetryAtProtectionExpiry(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	protectionDuration := 10 * time.Minute
	reclaimProtection := &config.PreemptionProtection{
		ReclaimWithinCohort: &config.PreemptionProtectionPolicy{
			MinAdmitDuration: &metav1.Duration{Duration: protectionDuration},
		},
	}
	flavors := []*kueue.ResourceFlavor{
		utiltestingapi.MakeResourceFlavor("default").Obj(),
	}
	clusterQueues := []*kueue.ClusterQueue{
		utiltestingapi.MakeClusterQueue("owner").
			Cohort("protection-cohort").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "6").Obj()).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
			}).
			Obj(),
		utiltestingapi.MakeClusterQueue("borrower").
			Cohort("protection-cohort").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "0").Obj()).
			Obj(),
	}
	cases := map[string]struct {
		gateEnabled   bool
		wantRetryTime *time.Time
	}{
		"retry scheduled at the earliest protection expiry": {
			gateEnabled: true,
			// The earliest expiry belongs to the candidate admitted first.
			wantRetryTime: ptr.To(now.Add(-2 * time.Minute).Add(protectionDuration)),
		},
		"no retry when targets are found": {
			gateEnabled: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.PreemptionProtection, tc.gateEnabled)
			ctx, log := utiltesting.ContextWithLog(t)
			admitted := []kueue.Workload{
				*utiltestingapi.MakeWorkload("b1", "").
					Request(corev1.ResourceCPU, "3").
					SimpleReserveQuota("borrower", "default", now).
					AdmittedAt(true, now.Add(-2*time.Minute)).
					Obj(),
				*utiltestingapi.MakeWorkload("b2", "").
					Request(corev1.ResourceCPU, "3").
					SimpleReserveQuota("borrower", "default", now).
					AdmittedAt(true, now.Add(-time.Minute)).
					Obj(),
			}
			cl := utiltesting.NewClientBuilder().
				WithLists(&kueue.WorkloadList{Items: admitted}).
				Build()
			cqCache := schdcache.New(cl)
			for _, flv := range flavors {
				cqCache.AddOrUpdateResourceFlavor(log, flv)
			}
			for _, cq := range clusterQueues {
				if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
					t.Fatalf("Couldn't add ClusterQueue to cache: %v", err)
				}
			}

			recorder := &utiltesting.EventRecorder{}
			preemptor := New(cl, workload.Ordering{}, recorder, nil, reclaimProtection, false,
				clocktesting.NewFakeClock(now), nil, preemptexpectations.New(), nil)
			var gotRetryTimes []time.Time
			var gotRetryCQs []kueue.ClusterQueueReference
			preemptor.SetRetryAfter(func(t time.Time, cq kueue.ClusterQueueReference) {
				gotRetryTimes = append(gotRetryTimes, t)
				gotRetryCQs = append(gotRetryCQs, cq)
			})

			snapshot, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}
			wlInfo := workload.NewInfo(utiltestingapi.MakeWorkload("in", "").Request(corev1.ResourceCPU, "6").Obj())
			wlInfo.ClusterQueue = "owner"
			targets := preemptor.GetTargets(log, *wlInfo, singlePodSetAssignment(
				flavorassigner.ResourceAssignment{
					corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
						Name: "default", Mode: flavorassigner.Preempt,
					},
				},
			), snapshot)

			if tc.wantRetryTime == nil {
				if len(targets) == 0 {
					t.Errorf("Expected preemption targets, got none")
				}
				if len(gotRetryTimes) != 0 {
					t.Errorf("Expected no retry to be scheduled, got %v", gotRetryTimes)
				}
				return
			}
			if len(targets) != 0 {
				t.Errorf("Expected no preemption targets, got %d", len(targets))
			}
			if len(gotRetryTimes) != 1 {
				t.Fatalf("Expected exactly one retry to be scheduled, got %d", len(gotRetryTimes))
			}
			if !gotRetryTimes[0].Equal(*tc.wantRetryTime) {
				t.Errorf("Retry scheduled at %v, want %v", gotRetryTimes[0], *tc.wantRetryTime)
			}
			if gotRetryCQs[0] != "owner" {
				t.Errorf("Retry scheduled for ClusterQueue %q, want %q", gotRetryCQs[0], "owner")
			}
		})
	}
}

// TestSimulatePreemptionWithProtectedCandidates verifies the oracle
// consistency mandated by the KEP: SimulatePreemption reports NoCandidates
// when every candidate for the flavor is protected, and the oracle path
// never arms the protection-expiry retry callback.
func TestSimulatePreemptionWithProtectedCandidates(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	protectionDuration := 10 * time.Minute
	reclaimProtection := &config.PreemptionProtection{
		ReclaimWithinCohort: &config.PreemptionProtectionPolicy{
			MinAdmitDuration: &metav1.Duration{Duration: protectionDuration},
		},
	}
	flavors := []*kueue.ResourceFlavor{
		utiltestingapi.MakeResourceFlavor("default").Obj(),
	}
	clusterQueues := []*kueue.ClusterQueue{
		utiltestingapi.MakeClusterQueue("owner").
			Cohort("protection-cohort").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "6").Obj()).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
			}).
			Obj(),
		utiltestingapi.MakeClusterQueue("borrower").
			Cohort("protection-cohort").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "0").Obj()).
			Obj(),
	}
	cases := map[string]struct {
		gateEnabled     bool
		wantPossibility preemptioncommon.PreemptionPossibility
	}{
		"all candidates protected reports NoCandidates": {
			gateEnabled:     true,
			wantPossibility: preemptioncommon.NoCandidates,
		},
		"gate disabled reports Reclaim": {
			gateEnabled:     false,
			wantPossibility: preemptioncommon.Reclaim,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.PreemptionProtection, tc.gateEnabled)
			ctx, log := utiltesting.ContextWithLog(t)
			admitted := []kueue.Workload{
				*utiltestingapi.MakeWorkload("b1", "").
					Request(corev1.ResourceCPU, "3").
					SimpleReserveQuota("borrower", "default", now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("b2", "").
					Request(corev1.ResourceCPU, "3").
					SimpleReserveQuota("borrower", "default", now).
					AdmittedAt(true, now).
					Obj(),
			}
			cl := utiltesting.NewClientBuilder().
				WithLists(&kueue.WorkloadList{Items: admitted}).
				Build()
			cqCache := schdcache.New(cl)
			for _, flv := range flavors {
				cqCache.AddOrUpdateResourceFlavor(log, flv)
			}
			for _, cq := range clusterQueues {
				if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
					t.Fatalf("Couldn't add ClusterQueue to cache: %v", err)
				}
			}

			recorder := &utiltesting.EventRecorder{}
			preemptor := New(cl, workload.Ordering{}, recorder, nil, reclaimProtection, false,
				clocktesting.NewFakeClock(now), nil, preemptexpectations.New(), nil)
			preemptor.SetRetryAfter(func(retryTime time.Time, cq kueue.ClusterQueueReference) {
				t.Errorf("retryAfter must not be invoked from the oracle path (got retryTime=%v, cq=%q)", retryTime, cq)
			})

			snapshot, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}
			oracle := NewOracle(preemptor, snapshot)
			wlInfo := workload.NewInfo(utiltestingapi.MakeWorkload("in", "").Request(corev1.ResourceCPU, "6").Obj())
			wlInfo.ClusterQueue = "owner"
			fr := resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceCPU}
			possibility, _ := oracle.SimulatePreemption(log, snapshot.ClusterQueue("owner"), *wlInfo, fr, resources.NewAmount(6000))
			if possibility != tc.wantPossibility {
				t.Errorf("SimulatePreemption() = %v, want %v", possibility, tc.wantPossibility)
			}
		})
	}
}

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
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-base/featuregate"
	clocktesting "k8s.io/utils/clock/testing"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	queueafs "sigs.k8s.io/kueue/pkg/cache/queue/afs"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	afs "sigs.k8s.io/kueue/pkg/util/admissionfairsharing"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
	utilslices "sigs.k8s.io/kueue/pkg/util/slices"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestAdmissionFairSharingClassicalPreemptionVictimStability(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	const runs = 500

	cases := map[string]struct {
		// preemptorQuota is the nominal quota of the preemptor ClusterQueue.
		// An empty donorQuota leaves the cohort without a donor.
		preemptorQuota   string
		donorQuota       string
		preemption       kueue.ClusterQueuePreemption
		incomingPriority int32
		wantVictim       string
	}{
		"reclaim without borrowing": {
			preemptorQuota: "3",
			preemption: kueue.ClusterQueuePreemption{
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
			},
			wantVictim: "default/c",
		},
		// The borrowing pass skips a (priority 5 >= incoming priority 4) as
		// ReclaimWithoutBorrowing. The per-ClusterQueue merge must not let that
		// skipped candidate hide b, which is the victim of the cached path.
		"borrowing pass with a higher-priority candidate": {
			preemptorQuota: "0",
			donorQuota:     "3",
			preemption: kueue.ClusterQueuePreemption{
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				BorrowWithinCohort: &kueue.BorrowWithinCohort{
					Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
				},
			},
			incomingPriority: 4,
			wantVictim:       "default/b",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			clusterQueues := []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("p").
					Cohort("all").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, tc.preemptorQuota).Obj()).
					Preemption(tc.preemption).
					Obj(),
			}
			if tc.donorQuota != "" {
				clusterQueues = append(clusterQueues, utiltestingapi.MakeClusterQueue("donor").
					Cohort("all").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, tc.donorQuota).Obj()).
					Obj())
			}
			clusterQueues = append(clusterQueues,
				utiltestingapi.MakeClusterQueue("q1").
					Cohort("all").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "0").Obj()).
					AdmissionMode(kueue.UsageBasedAdmissionFairSharing).
					Obj(),
				utiltestingapi.MakeClusterQueue("q2").
					Cohort("all").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "0").Obj()).
					Obj(),
			)

			localQueues := []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("lq1", "default").ClusterQueue("q1").Obj(),
				*utiltestingapi.MakeLocalQueue("lq2", "default").ClusterQueue("q1").Obj(),
			}
			admitted := []kueue.Workload{
				*utiltestingapi.MakeWorkload("a", "default").Queue("lq1").Priority(5).
					Request(corev1.ResourceCPU, "1").SimpleReserveQuota("q1", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("b", "default").Queue("lq2").Priority(0).
					Request(corev1.ResourceCPU, "1").SimpleReserveQuota("q1", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("c", "default").Queue("default").Priority(3).
					Request(corev1.ResourceCPU, "1").SimpleReserveQuota("q2", "default", now).Obj(),
			}
			for i := range admitted {
				admitted[i].UID = types.UID(admitted[i].Name)
			}

			for _, cacheEnabled := range []bool{true, false} {
				t.Run(fmt.Sprintf("ClassicalPreemptionCandidateOrderCache=%t", cacheEnabled), func(t *testing.T) {
					features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
						features.AdmissionFairSharing:                   true,
						features.ClassicalPreemptionCandidateOrderCache: cacheEnabled,
					})

					ctx, log := utiltesting.ContextWithLog(t)
					cl := utiltesting.NewClientBuilder().
						WithLists(&kueue.WorkloadList{Items: admitted}, &kueue.LocalQueueList{Items: localQueues}).
						Build()

					afsConfig := &config.AdmissionFairSharing{}
					cqCache := schdcache.New(cl, schdcache.WithAdmissionFairSharing(afsConfig))
					cqCache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())
					for _, cq := range clusterQueues {
						if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
							t.Fatalf("Couldn't add ClusterQueue to cache: %v", err)
						}
					}

					ledger := queueafs.NewAfsUsageLedger()
					ledger.SetForTest(utilqueue.LocalQueueReference("default/lq1"),
						corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10")}, now)
					ledger.SetForTest(utilqueue.LocalQueueReference("default/lq2"),
						corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}, now)

					snapshot, err := cqCache.Snapshot(ctx, schdcache.WithAfsUsageLedger(ledger))
					if err != nil {
						t.Fatalf("unexpected error while building snapshot: %v", err)
					}

					preemptor := New(cl, workload.Ordering{}, &utiltesting.EventRecorder{}, nil,
						afs.Enabled(afsConfig), clocktesting.NewFakeClock(now), nil,
						preemptexpectations.New(), nil)

					incoming := utiltestingapi.MakeWorkload("p_incoming", "default").
						Priority(tc.incomingPriority).Request(corev1.ResourceCPU, "1").Obj()
					wlInfo := workload.NewInfo(log, incoming)
					wlInfo.ClusterQueue = "p"
					assignment := singlePodSetAssignment(flavorassigner.ResourceAssignment{
						corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
							Name: "default", Mode: flavorassigner.Preempt,
						},
					})

					distribution := make(map[string]int)
					for range runs {
						strategies := preemptor.GetPreemptionStrategyIterator(ctx, *wlInfo, snapshot, assignment)
						targets := preemptor.GetTargetsWithStrategy(ctx, strategies)
						got := utilslices.Map(targets, func(t **Target) string {
							return string(workload.Key((*t).WorkloadInfo.Obj))
						})
						slices.Sort(got)
						distribution[strings.Join(got, ",")]++
					}
					t.Logf("victim distribution over %d runs: %v", runs, distribution)

					if len(distribution) != 1 {
						t.Fatalf("the preemption victim depends on candidate iteration order: got %d different outcomes %v", len(distribution), distribution)
					}
					var victim string
					for v := range distribution {
						victim = v
					}
					// Both cache modes must pick the same, specifically expected
					// victim; asserting only parity would also pass if both paths
					// returned no victim or the same wrong victim.
					if victim != tc.wantVictim {
						t.Errorf("the classical preemption victim = %q, want %q; the candidate order cache must not change the victim", victim, tc.wantVictim)
					}
				})
			}
		})
	}
}

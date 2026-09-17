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

package scheduler

import (
	"math"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

const ledgerResource = corev1.ResourceName("example.com/gpu")

var ledgerFR = resources.FlavorResource{Flavor: "default", Resource: ledgerResource}

// A workload at the old ceiling read as the unlimited sentinel, so a second
// workload's units were subtracted on removal but never added.
func TestLedgerRecoversAcrossTheCache(t *testing.T) {
	for name, vectorized := range map[string]bool{
		"map requests":    false,
		"vector requests": true,
	} {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.VectorizedResourceRequests, vectorized)

			ctx, log := utiltesting.ContextWithLog(t)
			cache := New(utiltesting.NewFakeClient())
			cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())

			if err := cache.AddOrUpdateCohort(utiltestingapi.MakeCohort("cohort").Obj()); err != nil {
				t.Fatalf("AddOrUpdateCohort() = %v", err)
			}
			cq := utiltestingapi.MakeClusterQueue("cq").
				Cohort("cohort").
				NamespaceSelector(nil).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
					Resource(ledgerResource, "1").Obj()).
				Obj()
			if err := cache.AddClusterQueue(ctx, cq); err != nil {
				t.Fatalf("AddClusterQueue() = %v", err)
			}
			if err := cache.AddLocalQueue(utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj()); err != nil {
				t.Fatalf("AddLocalQueue() = %v", err)
			}

			// Both hold quota already, so the cache charges them without the scheduler.
			saturating := utiltestingapi.MakeWorkload("saturating", "ns").
				Queue("lq").
				Request(ledgerResource, "9223372036854775807").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(ledgerResource, "default", "9223372036854775807").
						Obj()).Obj(), time.Now()).
				Condition(metav1.Condition{Type: kueue.WorkloadAdmitted, Status: metav1.ConditionTrue}).
				Obj()
			seven := utiltestingapi.MakeWorkload("seven", "ns").
				Queue("lq").
				Request(ledgerResource, "7").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(ledgerResource, "default", "7").
						Obj()).Obj(), time.Now()).
				Condition(metav1.Condition{Type: kueue.WorkloadAdmitted, Status: metav1.ConditionTrue}).
				Obj()

			cache.AddOrUpdateWorkload(ctx, log, saturating)
			assertLedgers(t, cache, "after the saturating workload joined", resources.NewAmount(math.MaxInt64))

			cache.AddOrUpdateWorkload(ctx, log, seven)
			// Built by arithmetic: no single Quantity carries more than MaxInt64.
			assertLedgers(t, cache, "after the 7-unit workload joined", resources.NewAmount(math.MaxInt64).AddInt64(7))

			// Compared as strings: a write through a shared big.Int would move the snapshot too.
			snap, err := cache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("Snapshot() = %v", err)
			}
			const wantSnapshot = "9223372036854775814"
			if got := snap.ClusterQueue("cq").ResourceNode.Usage[ledgerFR].String(); got != wantSnapshot {
				t.Fatalf("the snapshot did not take the total: %s, want %s", got, wantSnapshot)
			}

			if err := cache.DeleteWorkload(log, workload.Key(saturating)); err != nil {
				t.Fatalf("DeleteWorkload() = %v", err)
			}
			assertLedgers(t, cache, "after the saturating workload left", resources.NewAmount(7))

			if err := cache.DeleteWorkload(log, workload.Key(seven)); err != nil {
				t.Fatalf("DeleteWorkload() = %v", err)
			}
			assertLedgers(t, cache, "after both left", resources.NewAmount(0))

			if got := snap.ClusterQueue("cq").ResourceNode.Usage[ledgerFR].String(); got != wantSnapshot {
				t.Errorf("the snapshot followed the cache: %s, want %s", got, wantSnapshot)
			}
		})
	}
}

// assertLedgers checks every ledger the charge passes through.
func assertLedgers(t *testing.T, cache *Cache, when string, want resources.Amount) {
	t.Helper()
	cache.Lock()
	defer cache.Unlock()

	cq := cache.hm.ClusterQueue("cq")
	check := func(what string, got resources.Amount) {
		t.Helper()
		if !got.Equal(want) {
			t.Errorf("%s: %s = %s, want %s", when, what, got, want)
		}
	}
	check("ClusterQueue usage", cq.resourceNode.Usage[ledgerFR])
	check("ClusterQueue admitted usage", cq.AdmittedUsage[ledgerFR])

	lq, found := cq.localQueues["ns/lq"]
	if !found {
		t.Fatalf("%s: the LocalQueue is not in the cache", when)
	}
	check("LocalQueue reserved", lq.totalReserved[ledgerFR])
	check("LocalQueue admitted usage", lq.admittedUsage[ledgerFR])

	if cq.HasParent() {
		check("Cohort usage", cq.Parent().resourceNode.Usage[ledgerFR])
	}
}

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
	"strconv"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
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

// admittedWorkload builds a Workload already holding quota for count units, so
// the cache takes it into the ledgers without running the scheduler.
func admittedWorkload(name string, count int64) *kueue.Workload {
	units := strconv.FormatInt(count, 10)
	return utiltestingapi.MakeWorkload(name, "ns").
		Queue("lq").
		Request(ledgerResource, units).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
			PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
				Assignment(ledgerResource, "default", units).
				Obj()).Obj(), time.Now()).
		Condition(metav1.Condition{Type: kueue.WorkloadAdmitted, Status: metav1.ConditionTrue}).
		Obj()
}

// The sequence from #14105, driven through the cache rather than through
// Amount alone. A workload whose usage reaches the old ceiling used to become
// indistinguishable from the unlimited sentinel, so the second workload's units
// were never added while its removal was still subtracted, and the ledgers came
// back below where they started.
//
// Run against both Requests backends, since the charge reaches the cache
// through whichever one is compiled in.
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

			saturating := admittedWorkload("saturating", math.MaxInt64)
			seven := admittedWorkload("seven", 7)

			cache.AddOrUpdateWorkload(log, saturating)
			assertLedgers(t, cache, "after the saturating workload joined", mustAmount(t, "9223372036854775807"))

			cache.AddOrUpdateWorkload(log, seven)
			// Built by arithmetic rather than parsed: a single Quantity does not
			// carry more than MaxInt64, which is the point of the two workloads.
			// The ledger reaches the total; no one request could have asked for it.
			assertLedgers(t, cache, "after the 7-unit workload joined",
				mustAmount(t, "9223372036854775807").AddInt64(7))

			// A snapshot taken now must not follow the cache afterwards. The
			// observation is a string: a mutation written through the shared
			// big.Int would move a captured Amount with the snapshot, and it
			// would still compare equal to itself.
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

// assertLedgers checks every ledger the charge passes through, since one of
// them holding the right number says nothing about the others.
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

// mustAmount reads one request the way the cache does, so what a test asks for
// is bounded the same way a real request is.
func mustAmount(t *testing.T, s string) resources.Amount {
	t.Helper()
	return resources.AmountFromQuantity(ledgerResource, resource.MustParse(s))
}

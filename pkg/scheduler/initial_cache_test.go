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
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestInitialCacheReady(t *testing.T) {
	full := initialObjectSet{
		clusterQueues:     []kueue.ClusterQueueReference{"cq"},
		cohorts:           []kueue.CohortReference{"team"},
		flavors:           []kueue.ResourceFlavorReference{"f1"},
		topologies:        []kueue.TopologyReference{"topo"},
		reservedWorkloads: []workload.Reference{"ns/reserved"},
	}
	missingQueue := full
	missingQueue.clusterQueues = []kueue.ClusterQueueReference{"cq", "other"}

	cases := map[string]struct {
		previous *initialObjectSet
		current  initialObjectSet
		holds    bool
		want     bool
	}{
		"first list is not enough": {
			current: full,
			holds:   true,
			want:    false,
		},
		"matching lists with the cache caught up": {
			previous: &full,
			current:  full,
			holds:    true,
			want:     true,
		},
		"matching lists while the cache is missing an object": {
			previous: &full,
			current:  full,
			holds:    false,
			want:     false,
		},
		"second list gained an object": {
			previous: &full,
			current:  missingQueue,
			holds:    true,
			want:     false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := initialCacheReady(tc.previous, tc.current, tc.holds)
			if got != tc.want {
				t.Fatalf("initialCacheReady() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNewInitialObjectSetIgnoresOrderAndInactiveWorkloads(t *testing.T) {
	now := time.Now()
	reserved := *utiltestingapi.MakeWorkload("reserved", "ns").ReserveQuotaAt(
		utiltestingapi.MakeAdmission("cq").Obj(), now,
	).Obj()
	pending := *utiltestingapi.MakeWorkload("pending", "ns").Obj()
	finished := *utiltestingapi.MakeWorkload("finished", "ns").ReserveQuotaAt(
		utiltestingapi.MakeAdmission("cq").Obj(), now,
	).FinishedAt(now).Obj()

	first := newInitialObjectSet(
		[]kueue.ClusterQueue{{ObjectMeta: metav1.ObjectMeta{Name: "b"}}, {ObjectMeta: metav1.ObjectMeta{Name: "a"}}},
		[]kueue.Cohort{{ObjectMeta: metav1.ObjectMeta{Name: "team"}}},
		[]kueue.ResourceFlavor{{ObjectMeta: metav1.ObjectMeta{Name: "f1"}}},
		[]kueue.Topology{{ObjectMeta: metav1.ObjectMeta{Name: "topo"}}},
		[]kueue.Workload{pending, finished, reserved},
	)
	second := newInitialObjectSet(
		[]kueue.ClusterQueue{{ObjectMeta: metav1.ObjectMeta{Name: "a"}}, {ObjectMeta: metav1.ObjectMeta{Name: "b"}}},
		[]kueue.Cohort{{ObjectMeta: metav1.ObjectMeta{Name: "team"}}},
		[]kueue.ResourceFlavor{{ObjectMeta: metav1.ObjectMeta{Name: "f1"}}},
		[]kueue.Topology{{ObjectMeta: metav1.ObjectMeta{Name: "topo"}}},
		[]kueue.Workload{reserved},
	)
	if diff := cmp.Diff(first, second, cmp.AllowUnexported(initialObjectSet{})); diff != "" {
		t.Fatalf("unexpected set mismatch (-first +second):\n%s", diff)
	}
	if got, want := first.reservedWorkloads, []workload.Reference{workload.Key(&reserved)}; !cmp.Equal(got, want) {
		t.Fatalf("reserved workloads = %v, want %v", got, want)
	}
}

func TestHoldsInitialObjects(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	log := ctrl.LoggerFrom(ctx)
	now := time.Now()

	flavor := utiltestingapi.MakeResourceFlavor("f1").Obj()
	topology := utiltestingapi.MakeTopology("topo").Levels(corev1.LabelHostname).Obj()
	cq := utiltestingapi.MakeClusterQueue("cq").
		Cohort("team").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("f1").Resource(corev1.ResourceCPU, "1").Obj()).
		Obj()
	cohort := utiltestingapi.MakeCohort("team").Obj()
	reserved := utiltestingapi.MakeWorkload("reserved", "ns").ReserveQuotaAt(
		utiltestingapi.MakeAdmission("cq").
			PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
				Assignment(corev1.ResourceCPU, "f1", "1").
				Obj()).
			Obj(),
		now,
	).Obj()

	cache := schdcache.New(utiltesting.NewFakeClient())
	wantQueues := []kueue.ClusterQueueReference{"cq"}
	wantCohorts := []kueue.CohortReference{"team"}
	wantFlavors := []kueue.ResourceFlavorReference{"f1"}
	wantTopologies := []kueue.TopologyReference{"topo"}
	wantWorkloads := []workload.Reference{workload.Key(reserved)}

	holds := func() bool {
		return cache.HoldsInitialObjects(wantQueues, wantCohorts, wantFlavors, wantTopologies, wantWorkloads)
	}
	if holds() {
		t.Fatal("empty cache reported ready")
	}

	cache.AddOrUpdateResourceFlavor(log, flavor)
	cache.AddOrUpdateTopology(log, topology)
	if err := cache.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("add cluster queue: %v", err)
	}
	if holds() {
		t.Fatal("implicit cohort counted as the Cohort object, or the reserved workload was not required")
	}

	if err := cache.AddOrUpdateCohort(cohort); err != nil {
		t.Fatalf("add cohort: %v", err)
	}
	if holds() {
		t.Fatal("cache reported ready while the reserved workload was absent")
	}

	if !cache.AddOrUpdateWorkload(ctx, log, reserved) {
		t.Fatal("reserved workload was not added")
	}
	if !holds() {
		t.Fatal("cache did not report ready after the listed objects were added")
	}

	if cache.HoldsInitialObjects(append(wantQueues, "missing"), wantCohorts, wantFlavors, wantTopologies, wantWorkloads) {
		t.Fatal("missing cluster queue was ignored")
	}
	if cache.HoldsInitialObjects(wantQueues, append(wantCohorts, "missing"), wantFlavors, wantTopologies, wantWorkloads) {
		t.Fatal("missing cohort was ignored")
	}
	if cache.HoldsInitialObjects(wantQueues, wantCohorts, append(wantFlavors, "missing"), wantTopologies, wantWorkloads) {
		t.Fatal("missing flavor was ignored")
	}
	if cache.HoldsInitialObjects(wantQueues, wantCohorts, wantFlavors, append(wantTopologies, "missing"), wantWorkloads) {
		t.Fatal("missing topology was ignored")
	}
	if cache.HoldsInitialObjects(wantQueues, wantCohorts, wantFlavors, wantTopologies, append(wantWorkloads, "ns/missing")) {
		t.Fatal("missing reserved workload was ignored")
	}
}

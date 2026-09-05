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

package simulation

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/kueue/pkg/cache/scheduler"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"

	"sigs.k8s.io/controller-runtime/pkg/client"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/hierarchy"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

var simCmpOpts = []cmp.Option{
	cmpopts.IgnoreFields(preemption{}, "revert"),
	cmpopts.IgnoreTypes(&workload.Info{}),
}

var snapshotCmpOpts = cmp.Options{
	cmpopts.EquateEmpty(),
	cmpopts.IgnoreUnexported(schdcache.ClusterQueueSnapshot{}),
	cmpopts.IgnoreUnexported(schdcache.CohortSnapshot{}),
	cmpopts.IgnoreUnexported(hierarchy.Cohort[*schdcache.ClusterQueueSnapshot, *schdcache.CohortSnapshot]{}),
	cmpopts.IgnoreUnexported(hierarchy.ClusterQueue[*schdcache.CohortSnapshot]{}),
	cmpopts.IgnoreUnexported(hierarchy.Manager[*schdcache.ClusterQueueSnapshot, *schdcache.CohortSnapshot]{}),
	cmpopts.IgnoreUnexported(resources.Amount{}),
	cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
	cmpopts.IgnoreFields(schdcache.Snapshot{}, "SimulatorSnapshot"),
	cmpopts.IgnoreFields(schdcache.ClusterQueueSnapshot{},
		"NamespaceSelector",
		"Preemption",
		"Status",
		"AllocatableResourceGeneration",
		"Workloads",
		"ResourceGroups",
		"FlavorFungibility",
		"FairWeight",
	),
	cmpopts.IgnoreFields(schdcache.Snapshot{}, "ResourceFlavors", "SimulatorSnapshot"),
	cmpopts.IgnoreTypes(&workload.Info{}),
}

func setupSimulationTest(t *testing.T, flavors []*kueue.ResourceFlavor, clusterQueues []*kueue.ClusterQueue, workloads []kueue.Workload) (context.Context, *schdcache.Cache, map[string]*workload.Info) {
	t.Helper()

	ctx, log := utiltesting.ContextWithLog(t)
	cl := utiltesting.NewClientBuilder().WithLists(&kueue.WorkloadList{Items: workloads}).Build()

	cqCache := schdcache.New(cl)
	for _, flv := range flavors {
		cqCache.AddOrUpdateResourceFlavor(log, flv)
	}
	for _, cq := range clusterQueues {
		if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
			t.Fatalf("Couldn't add ClusterQueue to cache: %v", err)
		}
	}
	snapshot, err := cqCache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("unexpected error while building snapshot: %v", err)
	}
	wlInfos := make(map[string]*workload.Info, 2*len(workloads))
	for _, cq := range snapshot.ClusterQueues() {
		for _, wl := range cq.Workloads {
			wlInfos[wl.Obj.Name] = wl
			wlInfos[string(workload.Key(wl.Obj))] = wl
		}
	}
	return ctx, cqCache, wlInfos
}

func defaultSetup(t *testing.T) (context.Context, *schdcache.Cache, map[string]*workload.Info) {
	t.Helper()
	now := time.Now().Truncate(time.Second)
	flavors := []*kueue.ResourceFlavor{
		utiltestingapi.MakeResourceFlavor("default").Obj(),
	}
	clusterQueues := []*kueue.ClusterQueue{
		utiltestingapi.MakeClusterQueue("c1").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "10").Obj(),
			).
			Obj(),
	}
	workloads := []kueue.Workload{
		*utiltestingapi.MakeWorkload("wl1", "").
			Request(corev1.ResourceCPU, "2").
			ReserveQuotaAt(utiltestingapi.MakeAdmission("c1").
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(corev1.ResourceCPU, "default", "2000m").
					Obj()).
				Obj(), now).
			Obj(),
		*utiltestingapi.MakeWorkload("wl2", "").
			Request(corev1.ResourceCPU, "3").
			ReserveQuotaAt(utiltestingapi.MakeAdmission("c1").
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(corev1.ResourceCPU, "default", "3000m").
					Obj()).
				Obj(), now).
			Obj(),
	}
	return setupSimulationTest(t, flavors, clusterQueues, workloads)
}

func TestAddRemoveWorkloadWithLendingLimit(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	flavors := []*kueue.ResourceFlavor{
		utiltestingapi.MakeResourceFlavor("default").Obj(),
	}
	clusterQueues := []*kueue.ClusterQueue{
		utiltestingapi.MakeClusterQueue("lend-a").
			Cohort("lend").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "10", "", "4").Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj(),
		utiltestingapi.MakeClusterQueue("lend-b").
			Cohort("lend").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "10", "", "6").Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyNever,
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
			}).
			Obj(),
	}
	workloads := []kueue.Workload{
		*utiltestingapi.MakeWorkload("lend-a-1", "").
			Request(corev1.ResourceCPU, "1").
			ReserveQuotaAt(utiltestingapi.MakeAdmission("lend-a").
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(corev1.ResourceCPU, "default", "1").
					Obj()).
				Obj(), now).
			Obj(),
		*utiltestingapi.MakeWorkload("lend-a-2", "").
			Request(corev1.ResourceCPU, "9").
			ReserveQuotaAt(utiltestingapi.MakeAdmission("lend-a").
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(corev1.ResourceCPU, "default", "9").
					Obj()).
				Obj(), now).
			Obj(),
		*utiltestingapi.MakeWorkload("lend-a-3", "").
			Request(corev1.ResourceCPU, "6").
			ReserveQuotaAt(utiltestingapi.MakeAdmission("lend-a").
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(corev1.ResourceCPU, "default", "6").
					Obj()).
				Obj(), now).
			Obj(),
		*utiltestingapi.MakeWorkload("lend-b-1", "").
			Request(corev1.ResourceCPU, "4").
			ReserveQuotaAt(utiltestingapi.MakeAdmission("lend-b").
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(corev1.ResourceCPU, "default", "4").
					Obj()).
				Obj(), now).
			Obj(),
	}

	ctx, cqCache, wlInfos := setupSimulationTest(t, flavors, clusterQueues, workloads)
	initialSnapshot, err := cqCache.Snapshot(ctx)

	if err != nil {
		t.Fatalf("unexpected error while building snapshot: %v", err)
	}

	initialCohortResources := initialSnapshot.ClusterQueue("lend-a").Parent().ResourceNode.SubtreeQuota
	cases := map[string]struct {
		remove []workload.Reference
		add    []workload.Reference
		want   schdcache.Snapshot
	}{
		"remove all then add all": {
			remove: []workload.Reference{"/lend-a-1", "/lend-a-2", "/lend-a-3", "/lend-b-1"},
			add:    []workload.Reference{"/lend-a-1", "/lend-a-2", "/lend-a-3", "/lend-b-1"},
			want:   *initialSnapshot,
		},
		"remove all": {
			remove: []workload.Reference{"/lend-a-1", "/lend-a-2", "/lend-a-3", "/lend-b-1"},
			want: schdcache.Snapshot{
				Manager: hierarchy.NewManagerForTest(
					map[kueue.CohortReference]*schdcache.CohortSnapshot{
						"lend": makeCohortSnapshot(
							"lend",
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(0),
							},
							initialCohortResources,
						),
					},
					map[kueue.ClusterQueueReference]*schdcache.ClusterQueueSnapshot{
						"lend-a": makeCQSnapshot("lend-a",
							0,
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(0),
							},
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(10_000),
							},
						),
						"lend-b": makeCQSnapshot("lend-b",
							0,
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(0),
							},
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(10_000),
							},
						),
					},
				),
			},
		},
		"remove workload, but still using quota over GuaranteedQuota": {
			remove: []workload.Reference{"/lend-a-2"},
			want: schdcache.Snapshot{
				Manager: hierarchy.NewManagerForTest(
					map[kueue.CohortReference]*schdcache.CohortSnapshot{
						"lend": makeCohortSnapshot(
							"lend",
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(1_000),
							},
							initialCohortResources,
						),
					},
					map[kueue.ClusterQueueReference]*schdcache.ClusterQueueSnapshot{
						"lend-a": makeCQSnapshot("lend-a",
							0,
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(7_000),
							},
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(10_000),
							},
						),
						"lend-b": makeCQSnapshot("lend-b",
							1,
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(4_000),
							},
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(10_000),
							},
						),
					},
				),
			},
		},
		"remove wokload, using same quota as GuaranteedQuota": {
			remove: []workload.Reference{"/lend-a-1", "/lend-a-2"},
			want: schdcache.Snapshot{
				Manager: hierarchy.NewManagerForTest(
					map[kueue.CohortReference]*schdcache.CohortSnapshot{
						"lend": makeCohortSnapshot(
							"lend",
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(0),
							},
							initialCohortResources,
						),
					},
					map[kueue.ClusterQueueReference]*schdcache.ClusterQueueSnapshot{
						"lend-a": makeCQSnapshot("lend-a",
							0,
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(6_000),
							},
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(10_000),
							},
						),
						"lend-b": makeCQSnapshot("lend-b",
							1,
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(4_000),
							},
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(10_000),
							},
						),
					},
				),
			},
		},
		"remove workload, using less quota than GuaranteedQuota": {
			remove: []workload.Reference{"/lend-a-2", "/lend-a-3"},
			want: schdcache.Snapshot{
				Manager: hierarchy.NewManagerForTest(
					map[kueue.CohortReference]*schdcache.CohortSnapshot{
						"lend": makeCohortSnapshot(
							"lend",
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(0),
							},
							initialCohortResources,
						),
					},
					map[kueue.ClusterQueueReference]*schdcache.ClusterQueueSnapshot{
						"lend-a": makeCQSnapshot("lend-a",
							0,
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(1_000),
							},
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(10_000),
							},
						),
						"lend-b": makeCQSnapshot("lend-b",
							1,
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(4_000),
							},
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(10_000),
							},
						),
					},
				),
			},
		},
		"remove all then add workload, using less quota than GuaranteedQuota": {
			remove: []workload.Reference{"/lend-a-1", "/lend-a-2", "/lend-a-3", "/lend-b-1"},
			add:    []workload.Reference{"/lend-a-1"},
			want: schdcache.Snapshot{
				Manager: hierarchy.NewManagerForTest(
					map[kueue.CohortReference]*schdcache.CohortSnapshot{
						"lend": makeCohortSnapshot(
							"lend",
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(0),
							},
							initialCohortResources,
						),
					},
					map[kueue.ClusterQueueReference]*schdcache.ClusterQueueSnapshot{
						"lend-a": makeCQSnapshot("lend-a",
							0,
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(1_000),
							},
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(10_000),
							},
						),
						"lend-b": makeCQSnapshot("lend-b",
							1,
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(0),
							},
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(10_000),
							},
						),
					},
				),
			},
		},
		"remove all then add workload, using same quota as GuaranteedQuota": {
			remove: []workload.Reference{"/lend-a-1", "/lend-a-2", "/lend-a-3", "/lend-b-1"},
			add:    []workload.Reference{"/lend-a-3"},
			want: schdcache.Snapshot{
				Manager: hierarchy.NewManagerForTest(
					map[kueue.CohortReference]*schdcache.CohortSnapshot{
						"lend": makeCohortSnapshot(
							"lend",
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(0),
							},
							initialCohortResources,
						),
					},
					map[kueue.ClusterQueueReference]*schdcache.ClusterQueueSnapshot{
						"lend-a": makeCQSnapshot("lend-a",
							0,
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(6_000),
							},
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(10_000),
							},
						),
						"lend-b": makeCQSnapshot("lend-b",
							0,
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(0),
							},
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(10_000),
							},
						),
					},
				),
			},
		},
		"remove all then add workload, using quota over GuaranteedQuota": {
			remove: []workload.Reference{"/lend-a-1", "/lend-a-2", "/lend-a-3", "/lend-b-1"},
			add:    []workload.Reference{"/lend-a-2"},
			want: schdcache.Snapshot{
				Manager: hierarchy.NewManagerForTest(
					map[kueue.CohortReference]*schdcache.CohortSnapshot{
						"lend": makeCohortSnapshot(
							"lend",
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(3_000),
							},
							initialCohortResources,
						),
					},
					map[kueue.ClusterQueueReference]*schdcache.ClusterQueueSnapshot{
						"lend-a": makeCQSnapshot("lend-a",
							0,
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(9_000),
							},
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(10_000),
							},
						),
						"lend-b": makeCQSnapshot("lend-b",
							1,
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(0),
							},
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(10_000),
							},
						),
					},
				),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			snap, err := cqCache.Snapshot(ctx)
			sim := newSimulationContext(snap)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}
			for _, name := range tc.remove {
				sim.removeWorkload(wlInfos[string(name)])
			}
			for _, name := range tc.add {
				sim.addWorkload(wlInfos[string(name)])
			}
			if diff := cmp.Diff(tc.want, *snap, snapshotCmpOpts...); diff != "" {
				t.Errorf("Unexpected snapshot state after operations (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestPreemptWorkload(t *testing.T) {
	ctx, cqCache, wlInfos := defaultSetup(t)
	errSimulatorFailed := errors.New("simulator preempt error")

	cases := map[string]struct {
		preempt             []string
		injectSimErr        error
		wantErr             bool
		wantSnapshotState   schdcache.Snapshot
		wantSimulationState map[workloadKey]preemption
	}{
		"preempt single workload": {
			preempt: []string{"wl1"},
			wantSnapshotState: schdcache.Snapshot{
				Manager: hierarchy.NewManagerForTest(
					nil,
					map[kueue.ClusterQueueReference]*schdcache.ClusterQueueSnapshot{
						"c1": makeCQSnapshot("c1",
							0,
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(3_000),
							},
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(10_000),
							},
						),
					},
				),
			},
			wantSimulationState: map[workloadKey]preemption{
				client.ObjectKeyFromObject(wlInfos["wl1"].Obj): {target: wlInfos["wl1"]},
			},
		},
		"preempt multiple workloads": {
			preempt: []string{"wl1", "wl2"},
			wantSnapshotState: schdcache.Snapshot{
				Manager: hierarchy.NewManagerForTest(
					nil,
					map[kueue.ClusterQueueReference]*schdcache.ClusterQueueSnapshot{
						"c1": makeCQSnapshot("c1",
							0,
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(0),
							},
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(10_000),
							},
						),
					},
				),
			},
			wantSimulationState: map[workloadKey]preemption{
				client.ObjectKeyFromObject(wlInfos["wl1"].Obj): {target: wlInfos["wl1"]},
				client.ObjectKeyFromObject(wlInfos["wl2"].Obj): {target: wlInfos["wl2"]},
			},
		},
		"preempt workload when simulator fails returns error": {
			preempt:      []string{"wl1"},
			injectSimErr: errSimulatorFailed,
			wantErr:      true,
			wantSnapshotState: schdcache.Snapshot{
				Manager: hierarchy.NewManagerForTest(
					nil,
					map[kueue.ClusterQueueReference]*schdcache.ClusterQueueSnapshot{
						"c1": makeCQSnapshot("c1",
							0,
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(5_000),
							},
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(10_000),
							},
						),
					},
				),
			},
			wantSimulationState: make(map[workloadKey]preemption),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			snap, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error building snapshot: %v", err)
			}
			if tc.injectSimErr != nil {
				snap.SimulatorSnapshot = &errSimulatorSnapshot{err: tc.injectSimErr}
			}
			sim := newSimulationContext(snap)
			var preemptErr error
			for _, wlName := range tc.preempt {
				if err := sim.PreemptWorkload(ctx, wlInfos[wlName]); err != nil {
					preemptErr = err
				}
			}
			if (preemptErr != nil) != tc.wantErr {
				t.Errorf("PreemptWorkload() error = %v, wantErr %v", preemptErr, tc.wantErr)
			}
			if diff := cmp.Diff(tc.wantSnapshotState, *snap, snapshotCmpOpts...); diff != "" {
				t.Errorf("Unexpected snapshot state after preemptions (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantSimulationState, sim.simulatedPreemptions, simCmpOpts...); diff != "" {
				t.Errorf("Unexpected simulator state after preemptions (-want,+got):\n%s", diff)
			}
		})
	}
}

type errSimulatorSnapshot struct {
	simulator.SimulatorSnapshot
	err error
}

func (s *errSimulatorSnapshot) PreemptWorkload(_ context.Context, _ types.NamespacedName) (func() error, error) {
	return nil, s.err
}

func TestRestoreWorkload(t *testing.T) {
	ctx, cqCache, wlInfos := defaultSetup(t)
	errRevertFailed := errors.New("revert error")

	cases := map[string]struct {
		preempt      []string
		injectError  map[string]error
		restore      []string
		wantErr      bool
		want         schdcache.Snapshot
		wantSimState map[workloadKey]preemption
	}{
		"restore single preempted workload": {
			preempt: []string{"wl1", "wl2"},
			restore: []string{"wl1"},
			want: schdcache.Snapshot{
				Manager: hierarchy.NewManagerForTest(
					nil,
					map[kueue.ClusterQueueReference]*schdcache.ClusterQueueSnapshot{
						"c1": makeCQSnapshot("c1",
							0,
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(2_000),
							},
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(10_000),
							},
						),
					},
				),
			},
			wantSimState: map[workloadKey]preemption{
				client.ObjectKeyFromObject(wlInfos["wl2"].Obj): {target: wlInfos["wl2"]},
			},
		},
		"restore all preempted workloads": {
			preempt: []string{"wl1", "wl2"},
			restore: []string{"wl1", "wl2"},
			want: schdcache.Snapshot{
				Manager: hierarchy.NewManagerForTest(
					nil,
					map[kueue.ClusterQueueReference]*schdcache.ClusterQueueSnapshot{
						"c1": makeCQSnapshot("c1",
							0,
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(5_000),
							},
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(10_000),
							},
						),
					},
				),
			},
			wantSimState: make(map[workloadKey]preemption),
		},
		"restore non-preempted workload (no-op)": {
			preempt: []string{"wl1"},
			restore: []string{"wl2"},
			want: schdcache.Snapshot{
				Manager: hierarchy.NewManagerForTest(
					nil,
					map[kueue.ClusterQueueReference]*schdcache.ClusterQueueSnapshot{
						"c1": makeCQSnapshot("c1",
							0,
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(3_000),
							},
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(10_000),
							},
						),
					},
				),
			},
			wantSimState: map[workloadKey]preemption{
				client.ObjectKeyFromObject(wlInfos["wl1"].Obj): {target: wlInfos["wl1"]},
			},
		},
		"restore workload with revert error": {
			preempt: []string{"wl1"},
			injectError: map[string]error{
				"wl1": errRevertFailed,
			},
			restore: []string{"wl1"},
			wantErr: true,
			want: schdcache.Snapshot{
				Manager: hierarchy.NewManagerForTest(
					nil,
					map[kueue.ClusterQueueReference]*schdcache.ClusterQueueSnapshot{
						"c1": makeCQSnapshot("c1",
							0,
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(3_000),
							},
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(10_000),
							},
						),
					},
				),
			},
			wantSimState: map[workloadKey]preemption{
				client.ObjectKeyFromObject(wlInfos["wl1"].Obj): {target: wlInfos["wl1"]},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			snap, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error building snapshot: %v", err)
			}
			sim := newSimulationContext(snap)
			for _, wlName := range tc.preempt {
				if err := sim.PreemptWorkload(ctx, wlInfos[wlName]); err != nil {
					t.Fatalf("unexpected error preempting %s: %v", wlName, err)
				}
			}
			for wlName, injErr := range tc.injectError {
				key := client.ObjectKeyFromObject(wlInfos[wlName].Obj)
				if p, ok := sim.simulatedPreemptions[key]; ok {
					p.revert = func() error { return injErr }
					sim.simulatedPreemptions[key] = p
				}
			}
			var restoreErr error
			for _, wlName := range tc.restore {
				wlKey := client.ObjectKeyFromObject(wlInfos[wlName].Obj)
				if err := sim.RestoreWorkload(wlKey); err != nil {
					restoreErr = err
				}
			}
			if (restoreErr != nil) != tc.wantErr {
				t.Errorf("RestoreWorkloads() error = %v, wantErr %v", restoreErr, tc.wantErr)
			}
			if diff := cmp.Diff(tc.want, *snap, snapshotCmpOpts...); diff != "" {
				t.Errorf("Unexpected snapshot state after restores (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantSimState, sim.simulatedPreemptions, simCmpOpts...); diff != "" {
				t.Errorf("Unexpected simulator state after restores (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestRestoreSnapshot(t *testing.T) {
	ctx, cqCache, wlInfos := defaultSetup(t)
	errRevertFailed := errors.New("revert error")

	cases := map[string]struct {
		preempt        []string
		injectError    map[string]error
		restoreTargets []string
		wantErr        bool
		want           schdcache.Snapshot
		wantSimState   map[workloadKey]preemption
	}{
		"restore subset of targets": {
			preempt:        []string{"wl1", "wl2"},
			restoreTargets: []string{"wl1"},
			want: schdcache.Snapshot{
				Manager: hierarchy.NewManagerForTest(
					nil,
					map[kueue.ClusterQueueReference]*schdcache.ClusterQueueSnapshot{
						"c1": makeCQSnapshot("c1",
							0,
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(2_000),
							},
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(10_000),
							},
						),
					},
				),
			},
			wantSimState: map[workloadKey]preemption{
				client.ObjectKeyFromObject(wlInfos["wl2"].Obj): {target: wlInfos["wl2"]},
			},
		},
		"restore all targets": {
			preempt:        []string{"wl1", "wl2"},
			restoreTargets: []string{"wl1", "wl2"},
			want: schdcache.Snapshot{
				Manager: hierarchy.NewManagerForTest(
					nil,
					map[kueue.ClusterQueueReference]*schdcache.ClusterQueueSnapshot{
						"c1": makeCQSnapshot("c1",
							0,
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(5_000),
							},
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(10_000),
							},
						),
					},
				),
			},
			wantSimState: make(map[workloadKey]preemption),
		},
		"restore empty targets set": {
			preempt:        []string{"wl1", "wl2"},
			restoreTargets: []string{},
			want: schdcache.Snapshot{
				Manager: hierarchy.NewManagerForTest(
					nil,
					map[kueue.ClusterQueueReference]*schdcache.ClusterQueueSnapshot{
						"c1": makeCQSnapshot("c1",
							0,
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(5_000),
							},
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(10_000),
							},
						),
					},
				),
			},
			wantSimState: make(map[workloadKey]preemption),
		},
		"restore with revert error returns error": {
			preempt: []string{"wl1"},
			injectError: map[string]error{
				"wl1": errRevertFailed,
			},
			restoreTargets: []string{"wl1"},
			wantErr:        true,
			want: schdcache.Snapshot{
				Manager: hierarchy.NewManagerForTest(
					nil,
					map[kueue.ClusterQueueReference]*schdcache.ClusterQueueSnapshot{
						"c1": makeCQSnapshot("c1",
							0,
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(3_000),
							},
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(10_000),
							},
						),
					},
				),
			},
			wantSimState: map[workloadKey]preemption{
				client.ObjectKeyFromObject(wlInfos["wl1"].Obj): {target: wlInfos["wl1"]},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			snap, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error building snapshot: %v", err)
			}
			sim := newSimulationContext(snap)
			for _, wlName := range tc.preempt {
				if err := sim.PreemptWorkload(ctx, wlInfos[wlName]); err != nil {
					t.Fatalf("unexpected error preempting %s: %v", wlName, err)
				}
			}
			for wlName, injErr := range tc.injectError {
				key := client.ObjectKeyFromObject(wlInfos[wlName].Obj)
				if p, ok := sim.simulatedPreemptions[key]; ok {
					p.revert = func() error { return injErr }
					sim.simulatedPreemptions[key] = p
				}
			}
			targets := make([]types.NamespacedName, 0, len(tc.restoreTargets))
			for _, wlName := range tc.restoreTargets {
				targets = append(targets, client.ObjectKeyFromObject(wlInfos[wlName].Obj))
			}
			err = sim.restoreWorkloads(targets...)
			if (err != nil) != tc.wantErr {
				t.Errorf("RestoreSnapshot() error = %v, wantErr %v", err, tc.wantErr)
			}
			if diff := cmp.Diff(tc.want, *snap, snapshotCmpOpts...); diff != "" {
				t.Errorf("Unexpected snapshot state after RestoreSnapshot (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantSimState, sim.simulatedPreemptions, simCmpOpts...); diff != "" {
				t.Errorf("Unexpected simulator state after RestoreSnapshot (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestSimulation(t *testing.T) {
	ctx, cqCache, wlInfos := defaultSetup(t)

	initialSnap, err := cqCache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("unexpected error building initial snapshot: %v", err)
	}

	cases := map[string]struct {
		simFunc func(sim *SimulationContext) error
	}{
		"preempt single workload inside simulation": {
			simFunc: func(sim *SimulationContext) error {
				if err := sim.PreemptWorkload(ctx, wlInfos["wl1"]); err != nil {
					return fmt.Errorf("unexpected error preempting wl1: %v", err)
				}
				return nil
			},
		},
		"preempt multiple workloads inside simulation": {
			simFunc: func(sim *SimulationContext) error {
				if err := sim.PreemptWorkload(ctx, wlInfos["wl1"]); err != nil {
					return fmt.Errorf("unexpected error preempting wl1: %v", err)
				}
				if err := sim.PreemptWorkload(ctx, wlInfos["wl2"]); err != nil {
					return fmt.Errorf("unexpected error preempting wl2: %v", err)
				}
				return nil
			},
		},
		"preempt and partially restore workload inside simulation": {
			simFunc: func(sim *SimulationContext) error {
				if err := sim.PreemptWorkload(ctx, wlInfos["wl1"]); err != nil {
					return fmt.Errorf("unexpected error preempting wl1: %v", err)
				}
				if err := sim.PreemptWorkload(ctx, wlInfos["wl2"]); err != nil {
					return fmt.Errorf("unexpected error preempting wl2: %v", err)
				}
				wlKey := client.ObjectKeyFromObject(wlInfos["wl1"].Obj)
				if err := sim.RestoreWorkload(wlKey); err != nil {
					return fmt.Errorf("unexpected error restoring wl1: %v", err)
				}
				return nil
			},
		},
		"preempt and restore snapshot subset inside simulation": {
			simFunc: func(sim *SimulationContext) error {
				if err := sim.PreemptWorkload(ctx, wlInfos["wl1"]); err != nil {
					return fmt.Errorf("unexpected error preempting wl1: %v", err)
				}
				if err := sim.PreemptWorkload(ctx, wlInfos["wl2"]); err != nil {
					return fmt.Errorf("unexpected error preempting wl2: %v", err)
				}
				if err := sim.RestoreWorkload(client.ObjectKeyFromObject(wlInfos["wl1"].Obj)); err != nil {
					return fmt.Errorf("unexpected error restoring snapshot: %v", err)
				}
				return nil
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			snap, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error building snapshot: %v", err)
			}
			err = Simulate(ctx, snap, tc.simFunc)
			if err != nil {
				t.Errorf("Unexpected error during simulation: %v", err)
			}
			if diff := cmp.Diff(*initialSnap, *snap, snapshotCmpOpts...); diff != "" {
				t.Errorf("schdcache.Snapshot state was not restored after simulation (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestAddRemoveWorkload(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	flavors := []*kueue.ResourceFlavor{
		utiltestingapi.MakeResourceFlavor("default").Obj(),
		utiltestingapi.MakeResourceFlavor("alpha").Obj(),
		utiltestingapi.MakeResourceFlavor("beta").Obj(),
	}
	clusterQueues := []*kueue.ClusterQueue{
		utiltestingapi.MakeClusterQueue("c1").
			Cohort("cohort").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "6").Obj(),
			).
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("alpha").Resource(corev1.ResourceMemory, "6Gi").Obj(),
				*utiltestingapi.MakeFlavorQuotas("beta").Resource(corev1.ResourceMemory, "6Gi").Obj(),
			).
			Obj(),
		utiltestingapi.MakeClusterQueue("c2").
			Cohort("cohort").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "6").Obj(),
			).
			Obj(),
	}
	workloads := []kueue.Workload{
		*utiltestingapi.MakeWorkload("c1-cpu", "").
			Request(corev1.ResourceCPU, "1").
			ReserveQuotaAt(utiltestingapi.MakeAdmission("c1").
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(corev1.ResourceCPU, "default", "1000m").
					Obj()).
				Obj(), now).
			Obj(),
		*utiltestingapi.MakeWorkload("c1-memory-alpha", "").
			Request(corev1.ResourceMemory, "1Gi").
			ReserveQuotaAt(utiltestingapi.MakeAdmission("c1").
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(corev1.ResourceMemory, "alpha", "1Gi").
					Obj()).
				Obj(), now).
			Obj(),
		*utiltestingapi.MakeWorkload("c1-memory-beta", "").
			Request(corev1.ResourceMemory, "1Gi").
			ReserveQuotaAt(utiltestingapi.MakeAdmission("c1").
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(corev1.ResourceMemory, "beta", "1Gi").
					Obj()).
				Obj(), now).
			Obj(),
		*utiltestingapi.MakeWorkload("c2-cpu-1", "").
			Request(corev1.ResourceCPU, "1").
			ReserveQuotaAt(utiltestingapi.MakeAdmission("c2").
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(corev1.ResourceCPU, "default", "1000m").
					Obj()).
				Obj(), now).
			Obj(),
		*utiltestingapi.MakeWorkload("c2-cpu-2", "").
			Request(corev1.ResourceCPU, "1").
			ReserveQuotaAt(utiltestingapi.MakeAdmission("c2").
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(corev1.ResourceCPU, "default", "1000m").
					Obj()).
				Obj(), now).
			Obj(),
	}

	ctx, cqCache, wlInfos := setupSimulationTest(t, flavors, clusterQueues, workloads)
	initialSnapshot, err := cqCache.Snapshot(ctx)

	if err != nil {
		t.Fatalf("unexpected error while building snapshot: %v", err)
	}
	initialCohortResources := initialSnapshot.ClusterQueue("c1").Parent().ResourceNode.SubtreeQuota
	cases := map[string]struct {
		remove []workload.Reference
		add    []workload.Reference
		want   schdcache.Snapshot
	}{
		"no-op remove add": {
			remove: []workload.Reference{"/c1-cpu", "/c2-cpu-1"},
			add:    []workload.Reference{"/c1-cpu", "/c2-cpu-1"},
			want:   *initialSnapshot,
		},
		"remove all": {
			remove: []workload.Reference{"/c1-cpu", "/c1-memory-alpha", "/c1-memory-beta", "/c2-cpu-1", "/c2-cpu-2"},
			want: schdcache.Snapshot{
				Manager: hierarchy.NewManagerForTest(
					map[kueue.CohortReference]*schdcache.CohortSnapshot{
						"cohort": makeCohortSnapshot(
							"cohort",
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}:  resources.NewAmount(0),
								{Flavor: "alpha", Resource: corev1.ResourceMemory}: resources.NewAmount(0),
								{Flavor: "beta", Resource: corev1.ResourceMemory}:  resources.NewAmount(0),
							},
							initialCohortResources,
						),
					},
					map[kueue.ClusterQueueReference]*schdcache.ClusterQueueSnapshot{
						"c1": makeCQSnapshot("c1",
							0,
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}:  resources.NewAmount(0),
								{Flavor: "alpha", Resource: corev1.ResourceMemory}: resources.NewAmount(0),
								{Flavor: "beta", Resource: corev1.ResourceMemory}:  resources.NewAmount(0),
							},
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}:  resources.NewAmount(6_000),
								{Flavor: "alpha", Resource: corev1.ResourceMemory}: resources.NewAmount(utiltesting.Gi * 6),
								{Flavor: "beta", Resource: corev1.ResourceMemory}:  resources.NewAmount(utiltesting.Gi * 6),
							},
						),
						"c2": makeCQSnapshot("c2",
							1,
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(0),
							},
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(6_000),
							},
						),
					},
				),
			},
		},
		"remove c1-cpu": {
			remove: []workload.Reference{"/c1-cpu"},
			want: schdcache.Snapshot{
				Manager: hierarchy.NewManagerForTest(
					map[kueue.CohortReference]*schdcache.CohortSnapshot{
						"cohort": makeCohortSnapshot(
							"cohort",
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}:  resources.NewAmount(2_000),
								{Flavor: "alpha", Resource: corev1.ResourceMemory}: resources.NewAmount(utiltesting.Gi),
								{Flavor: "beta", Resource: corev1.ResourceMemory}:  resources.NewAmount(utiltesting.Gi),
							},
							initialCohortResources,
						),
					},
					map[kueue.ClusterQueueReference]*schdcache.ClusterQueueSnapshot{
						"c1": makeCQSnapshot("c1",
							0,
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}:  resources.NewAmount(0),
								{Flavor: "alpha", Resource: corev1.ResourceMemory}: resources.NewAmount(utiltesting.Gi),
								{Flavor: "beta", Resource: corev1.ResourceMemory}:  resources.NewAmount(utiltesting.Gi),
							},
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}:  resources.NewAmount(6_000),
								{Flavor: "alpha", Resource: corev1.ResourceMemory}: resources.NewAmount(utiltesting.Gi * 6),
								{Flavor: "beta", Resource: corev1.ResourceMemory}:  resources.NewAmount(utiltesting.Gi * 6),
							},
						),
						"c2": makeCQSnapshot("c2",
							1,
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(2_000),
							},
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(6_000),
							},
						),
					},
				),
			},
		},
		"remove c1-memory-alpha": {
			remove: []workload.Reference{"/c1-memory-alpha"},
			want: schdcache.Snapshot{
				Manager: hierarchy.NewManagerForTest(
					map[kueue.CohortReference]*schdcache.CohortSnapshot{
						"cohort": makeCohortSnapshot(
							"cohort",
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}:  resources.NewAmount(3_000),
								{Flavor: "alpha", Resource: corev1.ResourceMemory}: resources.NewAmount(0),
								{Flavor: "beta", Resource: corev1.ResourceMemory}:  resources.NewAmount(utiltesting.Gi),
							},
							initialCohortResources,
						),
					},
					map[kueue.ClusterQueueReference]*schdcache.ClusterQueueSnapshot{
						"c1": makeCQSnapshot("c1",
							0,
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}:  resources.NewAmount(1_000),
								{Flavor: "alpha", Resource: corev1.ResourceMemory}: resources.NewAmount(0),
								{Flavor: "beta", Resource: corev1.ResourceMemory}:  resources.NewAmount(utiltesting.Gi),
							},
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}:  resources.NewAmount(6_000),
								{Flavor: "alpha", Resource: corev1.ResourceMemory}: resources.NewAmount(utiltesting.Gi * 6),
								{Flavor: "beta", Resource: corev1.ResourceMemory}:  resources.NewAmount(utiltesting.Gi * 6),
							},
						),
						"c2": makeCQSnapshot("c2",
							0,
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(2_000),
							},
							resources.FlavorResourceQuantities{
								{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(6_000),
							},
						),
					},
				),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			snap, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}
			sim := newSimulationContext(snap)
			for _, name := range tc.remove {
				sim.removeWorkload(wlInfos[string(name)])
			}
			for _, name := range tc.add {
				sim.addWorkload(wlInfos[string(name)])
			}
			if diff := cmp.Diff(tc.want, *snap, snapshotCmpOpts...); diff != "" {
				t.Errorf("Unexpected snapshot state after operations (-want,+got):\n%s", diff)
			}
		})
	}
}

func makeCohortSnapshot(name kueue.CohortReference, usage, subtreeQuota resources.FlavorResourceQuantities) *schdcache.CohortSnapshot {
	resourceNode := scheduler.NewResourceNode()
	resourceNode.Usage = usage
	resourceNode.SubtreeQuota = subtreeQuota
	return &schdcache.CohortSnapshot{
		Name:         name,
		ResourceNode: resourceNode,
	}
}

func makeCQSnapshot(name kueue.ClusterQueueReference, allocatableResourceGeneration int64, usage, subtreeQuota resources.FlavorResourceQuantities) *schdcache.ClusterQueueSnapshot {
	resourceNode := scheduler.NewResourceNode()
	resourceNode.Usage = usage
	resourceNode.SubtreeQuota = subtreeQuota
	return &schdcache.ClusterQueueSnapshot{
		Name:                          name,
		AllocatableResourceGeneration: allocatableResourceGeneration,
		ResourceNode:                  resourceNode,
	}
}

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
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-base/featuregate"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/hierarchy"
	schedcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
)

var simCmpOpts = []cmp.Option{
	cmpopts.IgnoreFields(preemption{}, "revert"),
	cmpopts.IgnoreTypes(&workload.Info{}),
}

var snapshotCmpOpts = cmp.Options{
	cmpopts.EquateEmpty(),
	cmpopts.IgnoreUnexported(schedcache.Snapshot{}),
	cmpopts.IgnoreUnexported(schedcache.ClusterQueueSnapshot{}),
	cmpopts.IgnoreUnexported(schedcache.CohortSnapshot{}),
	cmpopts.IgnoreUnexported(hierarchy.Cohort[*schedcache.ClusterQueueSnapshot, *schedcache.CohortSnapshot]{}),
	cmpopts.IgnoreUnexported(hierarchy.ClusterQueue[*schedcache.CohortSnapshot]{}),
	cmpopts.IgnoreUnexported(hierarchy.Manager[*schedcache.ClusterQueueSnapshot, *schedcache.CohortSnapshot]{}),
	cmpopts.IgnoreUnexported(resources.Amount{}),
	cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
	cmpopts.IgnoreFields(schedcache.Snapshot{}, "SimulatorSnapshot"),
	cmpopts.IgnoreFields(schedcache.ClusterQueueSnapshot{},
		"NamespaceSelector",
		"Preemption",
		"Status",
		"AllocatableResourceGeneration",
		"Workloads",
		"ResourceGroups",
		"FlavorFungibility",
		"FairWeight",
	),
	cmpopts.IgnoreFields(schedcache.Snapshot{}, "ResourceFlavors", "SimulatorSnapshot"),
	cmpopts.IgnoreTypes(&workload.Info{}),
}

func setupSimulationTest(
	t *testing.T,
	flavors []*kueue.ResourceFlavor,
	clusterQueues []*kueue.ClusterQueue,
	workloads []kueue.Workload,
) (context.Context, *schedcache.Cache, map[string]*workload.Info) {
	t.Helper()

	ctx, log := utiltesting.ContextWithLog(t)
	cl := utiltesting.NewClientBuilder().WithLists(&kueue.WorkloadList{Items: workloads}).Build()

	cqCache := schedcache.New(cl)
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

func defaultSetup(t *testing.T) (context.Context, *schedcache.Cache, map[string]*workload.Info) {
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

func TestPreemptWorkload(t *testing.T) {
	ctx, cqCache, wlInfos := defaultSetup(t)
	errSimulatorFailed := errors.New("simulator preempt error")

	cases := map[string]struct {
		preempt             []string
		injectSimErr        error
		wantErr             bool
		wantSnapshotState   schedcache.Snapshot
		wantSimulationState map[workloadKey]preemption
	}{
		"preempt single workload": {
			preempt: []string{"wl1"},
			wantSnapshotState: schedcache.Snapshot{
				Manager: hierarchy.NewManagerForTest(
					nil,
					map[kueue.ClusterQueueReference]*schedcache.ClusterQueueSnapshot{
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
			wantSnapshotState: schedcache.Snapshot{
				Manager: hierarchy.NewManagerForTest(
					nil,
					map[kueue.ClusterQueueReference]*schedcache.ClusterQueueSnapshot{
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
			wantSnapshotState: schedcache.Snapshot{
				Manager: hierarchy.NewManagerForTest(
					nil,
					map[kueue.ClusterQueueReference]*schedcache.ClusterQueueSnapshot{
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
			sim := newSimulationContext(ctx, snap)
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
		want         schedcache.Snapshot
		wantSimState map[workloadKey]preemption
	}{
		"restore single preempted workload": {
			preempt: []string{"wl1", "wl2"},
			restore: []string{"wl1"},
			want: schedcache.Snapshot{
				Manager: hierarchy.NewManagerForTest(
					nil,
					map[kueue.ClusterQueueReference]*schedcache.ClusterQueueSnapshot{
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
			want: schedcache.Snapshot{
				Manager: hierarchy.NewManagerForTest(
					nil,
					map[kueue.ClusterQueueReference]*schedcache.ClusterQueueSnapshot{
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
			want: schedcache.Snapshot{
				Manager: hierarchy.NewManagerForTest(
					nil,
					map[kueue.ClusterQueueReference]*schedcache.ClusterQueueSnapshot{
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
			want: schedcache.Snapshot{
				Manager: hierarchy.NewManagerForTest(
					nil,
					map[kueue.ClusterQueueReference]*schedcache.ClusterQueueSnapshot{
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
			sim := newSimulationContext(ctx, snap)
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
		want           schedcache.Snapshot
		wantSimState   map[workloadKey]preemption
	}{
		"restore subset of targets": {
			preempt:        []string{"wl1", "wl2"},
			restoreTargets: []string{"wl1"},
			want: schedcache.Snapshot{
				Manager: hierarchy.NewManagerForTest(
					nil,
					map[kueue.ClusterQueueReference]*schedcache.ClusterQueueSnapshot{
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
			want: schedcache.Snapshot{
				Manager: hierarchy.NewManagerForTest(
					nil,
					map[kueue.ClusterQueueReference]*schedcache.ClusterQueueSnapshot{
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
			want: schedcache.Snapshot{
				Manager: hierarchy.NewManagerForTest(
					nil,
					map[kueue.ClusterQueueReference]*schedcache.ClusterQueueSnapshot{
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
			want: schedcache.Snapshot{
				Manager: hierarchy.NewManagerForTest(
					nil,
					map[kueue.ClusterQueueReference]*schedcache.ClusterQueueSnapshot{
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
			sim := newSimulationContext(ctx, snap)
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

func TestSimulateNested(t *testing.T) {
	ctx, cqCache, wlInfos := defaultSetup(t)

	errSimulation := errors.New("test simulation error")

	cases := map[string]struct {
		setupParent   func(ctx context.Context, parent *SimulationContext)
		nestedSim     func(ctx context.Context, child *SimulationContext) error
		wantErr       error
		wantWorkloads []workload.Reference
		wantUsage     resources.FlavorResourceQuantities
	}{
		"cleans up correctly after nested simulation error": {
			nestedSim: func(ctx context.Context, child *SimulationContext) error {
				if err := child.PreemptWorkload(ctx, wlInfos["wl1"]); err != nil {
					return err
				}
				child.RemoveUsage([]*workload.Info{wlInfos["wl2"]})
				return errSimulation
			},
			wantErr:       errSimulation,
			wantWorkloads: []workload.Reference{"/wl1", "/wl2"},
			wantUsage: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(5_000),
			},
		},
		"cleans up child mutations after error preserving parent mutations": {
			setupParent: func(ctx context.Context, parent *SimulationContext) {
				if err := parent.PreemptWorkload(ctx, wlInfos["wl1"]); err != nil {
					t.Fatalf("unexpected error during parent setup: %v", err)
				}
			},
			nestedSim: func(ctx context.Context, child *SimulationContext) error {
				if err := child.PreemptWorkload(ctx, wlInfos["wl2"]); err != nil {
					return err
				}
				return errSimulation
			},
			wantErr:       errSimulation,
			wantWorkloads: []workload.Reference{"/wl2"},
			wantUsage: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(3_000),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			snap, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error building snapshot: %v", err)
			}
			parentSim := newSimulationContext(ctx, snap)
			if tc.setupParent != nil {
				tc.setupParent(ctx, parentSim)
			}
			err = SimulateNested(parentSim, func(child *SimulationContext) error {
				return tc.nestedSim(ctx, child)
			})
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("SimulateNested() error = %v, want error wrapping %v", err, tc.wantErr)
			}
			cq := snap.ClusterQueue("c1")
			if cq == nil {
				t.Fatalf("ClusterQueue c1 is missing from snapshot")
			}
			if diff := cmp.Diff(tc.wantWorkloads, slices.Sorted(maps.Keys(cq.Workloads))); diff != "" {
				t.Errorf("unexpected Workloads in ClusterQueue (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantUsage, cq.ResourceNode.Usage); diff != "" {
				t.Errorf("unexpected Usage in ClusterQueue (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestSimulatingPreemptionsWithOverlappingTASUsage(t *testing.T) {
	fakeClock := testingclock.NewFakeClock(time.Now().Truncate(time.Second))
	testCases := map[string]struct {
		cqs                            []*kueue.ClusterQueue
		rfs                            []*kueue.ResourceFlavor
		topologies                     []*kueue.Topology
		wls                            []*kueue.Workload
		nodes                          []*corev1.Node
		wantTASUsage                   map[kueue.ResourceFlavorReference]map[utiltas.TopologyDomainID]resources.Requests
		removalSimulationWorkload      workload.Reference
		usageRemovalSimulationWorkload workload.Reference
		wantSimulatedTASUsage          map[kueue.ResourceFlavorReference]map[utiltas.TopologyDomainID]resources.Requests
		wantSimulatedWorkloads         []workload.Reference
		featureGates                   map[featuregate.Feature]bool
	}{
		"overlapping flavors: simulated removal of a Workload frees its node on the sibling flavor": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling:     true,
				features.TASHandleOverlappingFlavors: true,
			},
			topologies: []*kueue.Topology{utiltestingapi.MakeDefaultOneLevelTopology("topology")},
			rfs: []*kueue.ResourceFlavor{
				utiltestingapi.MakeResourceFlavor("tas-victim").
					TopologyName("topology").
					NodeLabel("zone", "a").
					Obj(),
				utiltestingapi.MakeResourceFlavor("tas-sibling").
					TopologyName("topology").
					NodeLabel("zone", "a").
					Obj(),
			},
			cqs: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("tas-victim").
							Resource(corev1.ResourceCPU, "100").
							Obj(),
						*utiltestingapi.MakeFlavorQuotas("tas-sibling").
							Resource(corev1.ResourceCPU, "100").
							Obj(),
					).
					Obj(),
			},
			nodes: []*corev1.Node{
				node.MakeNode("x1").
					Label(corev1.LabelHostname, "x1").
					Label("zone", "a").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			wls: []*kueue.Workload{
				utiltestingapi.MakeWorkload("victim", "").
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment("main").
								Assignment(corev1.ResourceCPU, "tas-victim", "1").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).
										Obj()).
									Obj()).
								Obj()).
							Obj(),
						fakeClock.Now(),
					).
					AdmittedAt(true, fakeClock.Now()).
					Obj(),
			},
			wantTASUsage: map[kueue.ResourceFlavorReference]map[utiltas.TopologyDomainID]resources.Requests{
				"tas-victim":  {"x1": resources.NewRequestsFromMap(map[corev1.ResourceName]int64{corev1.ResourceCPU: 1000, corev1.ResourcePods: 1})},
				"tas-sibling": {"x1": resources.NewRequestsFromMap(map[corev1.ResourceName]int64{corev1.ResourceCPU: 1000, corev1.ResourcePods: 1})},
			},
			removalSimulationWorkload: "/victim",
			wantSimulatedTASUsage: map[kueue.ResourceFlavorReference]map[utiltas.TopologyDomainID]resources.Requests{
				"tas-victim":  {"x1": resources.NewRequestsFromMap(map[corev1.ResourceName]int64{corev1.ResourceCPU: 0, corev1.ResourcePods: 0})},
				"tas-sibling": {"x1": resources.NewRequestsFromMap(map[corev1.ResourceName]int64{corev1.ResourceCPU: 0, corev1.ResourcePods: 0})},
			},
			wantSimulatedWorkloads: nil,
		},
		"overlapping flavors: simulated removal of a Workload's usage keeps it on the ClusterQueue": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling:     true,
				features.TASHandleOverlappingFlavors: true,
			},
			topologies: []*kueue.Topology{utiltestingapi.MakeDefaultOneLevelTopology("topology")},
			rfs: []*kueue.ResourceFlavor{
				utiltestingapi.MakeResourceFlavor("tas-victim").
					TopologyName("topology").
					NodeLabel("zone", "a").
					Obj(),
				utiltestingapi.MakeResourceFlavor("tas-sibling").
					TopologyName("topology").
					NodeLabel("zone", "a").
					Obj(),
			},
			cqs: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("tas-victim").
							Resource(corev1.ResourceCPU, "100").
							Obj(),
						*utiltestingapi.MakeFlavorQuotas("tas-sibling").
							Resource(corev1.ResourceCPU, "100").
							Obj(),
					).
					Obj(),
			},
			nodes: []*corev1.Node{
				node.MakeNode("x1").
					Label(corev1.LabelHostname, "x1").
					Label("zone", "a").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			wls: []*kueue.Workload{
				utiltestingapi.MakeWorkload("victim", "").
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment("main").
								Assignment(corev1.ResourceCPU, "tas-victim", "1").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).
										Obj()).
									Obj()).
								Obj()).
							Obj(),
						fakeClock.Now(),
					).
					AdmittedAt(true, fakeClock.Now()).
					Obj(),
			},
			wantTASUsage: map[kueue.ResourceFlavorReference]map[utiltas.TopologyDomainID]resources.Requests{
				"tas-victim":  {"x1": resources.NewRequestsFromMap(map[corev1.ResourceName]int64{corev1.ResourceCPU: 1000, corev1.ResourcePods: 1})},
				"tas-sibling": {"x1": resources.NewRequestsFromMap(map[corev1.ResourceName]int64{corev1.ResourceCPU: 1000, corev1.ResourcePods: 1})},
			},
			usageRemovalSimulationWorkload: "/victim",
			wantSimulatedTASUsage: map[kueue.ResourceFlavorReference]map[utiltas.TopologyDomainID]resources.Requests{
				"tas-victim":  {"x1": resources.NewRequestsFromMap(map[corev1.ResourceName]int64{corev1.ResourceCPU: 0, corev1.ResourcePods: 0})},
				"tas-sibling": {"x1": resources.NewRequestsFromMap(map[corev1.ResourceName]int64{corev1.ResourceCPU: 0, corev1.ResourcePods: 0})},
			},
			wantSimulatedWorkloads: []workload.Reference{"/victim"},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			ctx, log := utiltesting.ContextWithLog(t)
			cache := schedcache.New(utiltesting.NewFakeClient())
			for _, cq := range tc.cqs {
				if err := cache.AddClusterQueue(ctx, cq); err != nil {
					t.Fatalf("Failed adding ClusterQueue: %v", err)
				}
			}
			for _, rf := range tc.rfs {
				cache.AddOrUpdateResourceFlavor(log, rf)
			}
			for _, topology := range tc.topologies {
				cache.AddOrUpdateTopology(log, topology)
			}
			for _, wl := range tc.wls {
				cache.AddOrUpdateWorkload(log, wl)
			}
			for _, n := range tc.nodes {
				cache.TASCache().SyncNode(n)
			}
			snapshot, err := cache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}
			cqName := kueue.ClusterQueueReference(tc.cqs[0].Name)
			cqSnapshot := snapshot.ClusterQueue(cqName)
			if cqSnapshot == nil {
				t.Fatalf("ClusterQueue %q is missing from the snapshot", cqName)
			}
			workloadsAsBuilt := slices.Sorted(maps.Keys(cqSnapshot.Workloads))

			simErr := Simulate(ctx, snapshot, func(simCtx *SimulationContext) error {
				if tc.removalSimulationWorkload != "" {
					if err := simCtx.PreemptWorkload(ctx, cqSnapshot.Workloads[tc.removalSimulationWorkload]); err != nil {
						return err
					}
				} else {
					simCtx.RemoveUsage([]*workload.Info{cqSnapshot.Workloads[tc.usageRemovalSimulationWorkload]})
				}
				gotSimulatedTASUsage := make(map[kueue.ResourceFlavorReference]map[utiltas.TopologyDomainID]resources.Requests, len(tc.wantSimulatedTASUsage))
				for flavor := range tc.wantSimulatedTASUsage {
					flavorSnapshot := cqSnapshot.TASFlavors[flavor]
					if flavorSnapshot == nil {
						t.Fatalf("flavor %q is missing from the ClusterQueue snapshot", flavor)
					}
					gotSimulatedTASUsage[flavor] = flavorSnapshot.GetDomainUsage()
				}
				if diff := cmp.Diff(tc.wantSimulatedTASUsage, gotSimulatedTASUsage, cmp.Comparer(resources.Equal)); diff != "" {
					t.Errorf("unexpected TAS usage while the simulation is in effect (-want,+got):\n%s", diff)
				}
				if diff := cmp.Diff(tc.wantSimulatedWorkloads, slices.Sorted(maps.Keys(cqSnapshot.Workloads))); diff != "" {
					t.Errorf("unexpected Workloads while the simulation is in effect (-want,+got):\n%s", diff)
				}
				return nil
			})

			if simErr != nil {
				t.Errorf("simulation failed unexpectedly: %v", simErr)
			}

			if diff := cmp.Diff(workloadsAsBuilt, slices.Sorted(maps.Keys(cqSnapshot.Workloads))); diff != "" {
				t.Errorf("unexpected Workloads after the simulation was reverted (-want,+got):\n%s", diff)
			}

			gotTASUsage := make(map[kueue.ResourceFlavorReference]map[utiltas.TopologyDomainID]resources.Requests, len(tc.wantTASUsage))
			for flavor := range tc.wantTASUsage {
				flavorSnapshot := cqSnapshot.TASFlavors[flavor]
				if flavorSnapshot == nil {
					t.Fatalf("flavor %q is missing from the ClusterQueue snapshot", flavor)
				}
				gotTASUsage[flavor] = flavorSnapshot.GetDomainUsage()
			}
			if diff := cmp.Diff(tc.wantTASUsage, gotTASUsage, cmp.Comparer(resources.Equal)); diff != "" {
				t.Errorf("unexpected TAS usage in the flavor snapshots (-want,+got):\n%s", diff)
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
		want   schedcache.Snapshot
	}{
		"no-op remove add": {
			remove: []workload.Reference{"/c1-cpu", "/c2-cpu-1"},
			add:    []workload.Reference{"/c1-cpu", "/c2-cpu-1"},
			want:   *initialSnapshot,
		},
		"remove all": {
			remove: []workload.Reference{"/c1-cpu", "/c1-memory-alpha", "/c1-memory-beta", "/c2-cpu-1", "/c2-cpu-2"},
			want: schedcache.Snapshot{
				Manager: hierarchy.NewManagerForTest(
					map[kueue.CohortReference]*schedcache.CohortSnapshot{
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
					map[kueue.ClusterQueueReference]*schedcache.ClusterQueueSnapshot{
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
			want: schedcache.Snapshot{
				Manager: hierarchy.NewManagerForTest(
					map[kueue.CohortReference]*schedcache.CohortSnapshot{
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
					map[kueue.ClusterQueueReference]*schedcache.ClusterQueueSnapshot{
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
			want: schedcache.Snapshot{
				Manager: hierarchy.NewManagerForTest(
					map[kueue.CohortReference]*schedcache.CohortSnapshot{
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
					map[kueue.ClusterQueueReference]*schedcache.ClusterQueueSnapshot{
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
			sim := newSimulationContext(ctx, snap)
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

func TestContextTerminationOnError(t *testing.T) {
	errSimulator := errors.New("simulator error")
	errRevert := errors.New("revert error")
	errSimulation := errors.New("simulation closure error")

	cases := map[string]struct {
		setupSim func(t *testing.T, ctx context.Context, sim *SimulationContext, wlInfos map[string]*workload.Info)
		run      func(ctx context.Context, sim *SimulationContext, wlInfos map[string]*workload.Info) error
		wantErr  error
	}{
		"PreemptWorkload error terminates context": {
			setupSim: func(_ *testing.T, _ context.Context, sim *SimulationContext, _ map[string]*workload.Info) {
				sim.SimulatorSnapshot = &errSimulatorSnapshot{err: errSimulator}
			},
			run: func(ctx context.Context, sim *SimulationContext, wlInfos map[string]*workload.Info) error {
				return sim.PreemptWorkload(ctx, wlInfos["wl1"])
			},
			wantErr: errSimulator,
		},
		"RestoreWorkload error terminates context": {
			setupSim: func(t *testing.T, ctx context.Context, sim *SimulationContext, wlInfos map[string]*workload.Info) {
				if err := sim.PreemptWorkload(ctx, wlInfos["wl1"]); err != nil {
					t.Fatalf("unexpected error during preemption setup: %v", err)
				}
				key := client.ObjectKeyFromObject(wlInfos["wl1"].Obj)
				p := sim.simulatedPreemptions[key]
				p.revert = func() error { return errRevert }
				sim.simulatedPreemptions[key] = p
			},
			run: func(_ context.Context, sim *SimulationContext, wlInfos map[string]*workload.Info) error {
				return sim.RestoreWorkload(client.ObjectKeyFromObject(wlInfos["wl1"].Obj))
			},
			wantErr: errRevert,
		},
		"SimulateNested closure error terminates parent context": {
			run: func(_ context.Context, sim *SimulationContext, _ map[string]*workload.Info) error {
				return SimulateNested(sim, func(_ *SimulationContext) error {
					return errSimulation
				})
			},
			wantErr: errSimulation,
		},
		"SimulateNested restore error terminates parent context": {
			run: func(ctx context.Context, sim *SimulationContext, wlInfos map[string]*workload.Info) error {
				return SimulateNested(sim, func(child *SimulationContext) error {
					if err := child.PreemptWorkload(ctx, wlInfos["wl1"]); err != nil {
						return err
					}
					key := client.ObjectKeyFromObject(wlInfos["wl1"].Obj)
					p := child.simulatedPreemptions[key]
					p.revert = func() error { return errRevert }
					child.simulatedPreemptions[key] = p
					return nil
				})
			},
			wantErr: errRevert,
		},
		"SimulateNested inner simulation succeeds but child context corrupted terminates parent context": {
			run: func(ctx context.Context, sim *SimulationContext, wlInfos map[string]*workload.Info) error {
				return SimulateNested(sim, func(child *SimulationContext) error {
					child.SimulatorSnapshot = &errSimulatorSnapshot{err: errSimulator}
					_ = child.PreemptWorkload(ctx, wlInfos["wl1"])
					return nil
				})
			},
			wantErr: errSimulator,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, cqCache, wlInfos := defaultSetup(t)
			snap, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error building snapshot: %v", err)
			}
			sim := newSimulationContext(ctx, snap)
			if tc.setupSim != nil {
				tc.setupSim(t, ctx, sim, wlInfos)
			}

			err = tc.run(ctx, sim, wlInfos)
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("got error %v, want error wrapping %v", err, tc.wantErr)
			}
			if !errors.Is(sim.terminalError, tc.wantErr) {
				t.Errorf("sim.terminalError = %v, want error wrapping %v", sim.terminalError, tc.wantErr)
			}
			if !errors.Is(sim.errorTerminated(), tc.wantErr) {
				t.Errorf("sim.errorTerminated() = %v, want error wrapping %v", sim.errorTerminated(), tc.wantErr)
			}
		})
	}
}

func TestTerminatedContextExportedMethods(t *testing.T) {
	ctx, cqCache, wlInfos := defaultSetup(t)
	snap, err := cqCache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("unexpected error building snapshot: %v", err)
	}
	sim := newSimulationContext(ctx, snap)
	initialErr := errors.New("initial termination error")
	sim.terminate(initialErr)

	// 1. PreemptWorkload should fail with terminated error
	if err := sim.PreemptWorkload(ctx, wlInfos["wl1"]); !errors.Is(err, initialErr) {
		t.Errorf("PreemptWorkload() error = %v, want error wrapping %v", err, initialErr)
	}

	// 2. RestoreWorkload should fail with terminated error
	if err := sim.RestoreWorkload(client.ObjectKeyFromObject(wlInfos["wl1"].Obj)); !errors.Is(err, initialErr) {
		t.Errorf("RestoreWorkload() error = %v, want error wrapping %v", err, initialErr)
	}

	// 3. ClusterQueue should return nil
	if cq := sim.ClusterQueue("c1"); cq != nil {
		t.Errorf("ClusterQueue() = %v, want nil", cq)
	}

	// 4. RemoveUsage should be a no-op (no usage subtracted, no restore callback registered)
	snapBefore, err := cqCache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("unexpected error building snapshot: %v", err)
	}
	callbacksBefore := len(sim.restoreUsageCallbacks)
	sim.RemoveUsage([]*workload.Info{wlInfos["wl1"]})
	if diff := cmp.Diff(*snapBefore, *snap, snapshotCmpOpts...); diff != "" {
		t.Errorf("RemoveUsage() modified snapshot unexpectedly (-want,+got):\n%s", diff)
	}
	if len(sim.restoreUsageCallbacks) != callbacksBefore {
		t.Errorf("RemoveUsage() registered callbacks on terminated context, count = %d, want %d", len(sim.restoreUsageCallbacks), callbacksBefore)
	}

	// 5. SimulateNested should fail with terminated error and not run the nested simulation
	nestedRan := false
	if err := SimulateNested(sim, func(_ *SimulationContext) error {
		nestedRan = true
		return nil
	}); !errors.Is(err, initialErr) {
		t.Errorf("SimulateNested() error = %v, want error wrapping %v", err, initialErr)
	}
	if nestedRan {
		t.Errorf("SimulateNested() executed nested simulation on terminated context")
	}
}

func makeCohortSnapshot(name kueue.CohortReference, usage, subtreeQuota resources.FlavorResourceQuantities) *schedcache.CohortSnapshot {
	resourceNode := schedcache.NewResourceNode()
	resourceNode.Usage = usage
	resourceNode.SubtreeQuota = subtreeQuota
	return &schedcache.CohortSnapshot{
		Name:         name,
		ResourceNode: resourceNode,
	}
}

func makeCQSnapshot(name kueue.ClusterQueueReference, allocatableResourceGeneration int64, usage, subtreeQuota resources.FlavorResourceQuantities) *schedcache.ClusterQueueSnapshot {
	resourceNode := schedcache.NewResourceNode()
	resourceNode.Usage = usage
	resourceNode.SubtreeQuota = subtreeQuota
	return &schedcache.ClusterQueueSnapshot{
		Name:                          name,
		AllocatableResourceGeneration: allocatableResourceGeneration,
		ResourceNode:                  resourceNode,
	}
}

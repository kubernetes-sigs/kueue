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

package fit

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/testr"
	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/component-base/featuregate"
	clocktesting "k8s.io/utils/clock/testing"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	"sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
)

const (
	tasTestCQ       = "tas-cq"
	tasTestFlavor   = "tas-flavor"
	tasTestNode     = "node-1"
	tasTestNodeCPUs = "6"

	blockerName = "blocker"
	victimName  = "victim"

	// The incoming workload may preempt workloads with a lower priority only.
	lowPriority      = 0
	incomingPriority = 50
	highPriority     = 100
)

// newTASTestSnapshot builds a snapshot with a single TAS flavor backed by one
// hostname-level node with 6 CPU, and a ClusterQueue with the given CPU quota
// that allows preempting lower-priority workloads within the ClusterQueue.
// Two admitted workloads, "blocker" and "victim", occupy 2 CPU of the node each,
// leaving 2 CPU free. The victim always has a lower priority than the incoming
// workload. The blocker has a higher priority, unless evictableBlocker is set.
func newTASTestSnapshot(ctx context.Context, t *testing.T, log logr.Logger, quota string, evictableBlocker bool) *schdcache.Snapshot {
	t.Helper()

	cache := schdcache.New(utiltesting.NewFakeClient())
	cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor(tasTestFlavor).
		NodeLabel("tas", "true").TopologyName("topology").Obj())
	cache.AddOrUpdateTopology(log, utiltestingapi.MakeTopology("topology").Levels(corev1.LabelHostname).Obj())
	cache.TASCache().SyncNode(testingnode.MakeNode(tasTestNode).
		Label("tas", "true").
		Label(corev1.LabelHostname, tasTestNode).
		StatusAllocatable(corev1.ResourceList{
			corev1.ResourceCPU:  resource.MustParse(tasTestNodeCPUs),
			corev1.ResourcePods: resource.MustParse("32"),
		}).
		Ready().
		Obj())
	if err := cache.AddClusterQueue(ctx, utiltestingapi.MakeClusterQueue(tasTestCQ).
		Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasTestFlavor).Resource(corev1.ResourceCPU, quota).Obj()).
		Obj()); err != nil {
		t.Fatalf("adding ClusterQueue: %v", err)
	}

	blockerPriority := int32(highPriority)
	if evictableBlocker {
		blockerPriority = lowPriority
	}
	for name, priority := range map[string]int32{blockerName: blockerPriority, victimName: lowPriority} {
		wl := utiltestingapi.MakeWorkload(name, "default").
			Priority(priority).
			PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
				RequiredTopologyRequest(corev1.LabelHostname).
				Request(corev1.ResourceCPU, "2").
				Obj()).
			ReserveQuotaAt(utiltestingapi.MakeAdmission(tasTestCQ).
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(corev1.ResourceCPU, tasTestFlavor, "2").
					TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
						Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{tasTestNode}, 1).Obj()).
						Obj()).
					Obj()).
				Obj(), time.Now()).
			Obj()
		if !cache.AddOrUpdateWorkload(ctx, log, wl) {
			t.Fatalf("adding workload %q to the cache", name)
		}
	}

	snapshot, err := cache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("building snapshot: %v", err)
	}
	return snapshot
}

func freeCapacity(t *testing.T, snapshot *schdcache.Snapshot) string {
	t.Helper()
	got, err := snapshot.ClusterQueue(tasTestCQ).TASFlavors[tasTestFlavor].SerializeFreeCapacityPerDomain()
	if err != nil {
		t.Fatalf("serializing TAS free capacity: %v", err)
	}
	return got
}

func targetNames(targets []*preemption.Target) []string {
	var names []string
	for _, target := range targets {
		names = append(names, target.WorkloadInfo.Obj.Name)
	}
	slices.Sort(names)
	return names
}

// TestFindFit runs FindFit with the real flavor assigner and preemptor. The
// initial assignment is computed by the flavor assigner, as in the scheduler,
// so that only the combinations of quota, topology and preemption outcomes that
// can happen in practice are covered.
func TestFindFit(t *testing.T) {
	onNode := &utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
		Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{tasTestNode}, 1).Obj()).
		TopologyAssignment

	cases := map[string]struct {
		// quota is the CPU quota of the ClusterQueue. The admitted workloads use 4 CPU of it.
		quota            string
		evictableBlocker bool
		// cpu is the CPU requested by the single pod of the incoming workload.
		cpu string

		// wantInitialMode documents the quota-based mode computed by the flavor assigner.
		wantInitialMode        flavorassigner.FlavorAssignmentMode
		wantMode               flavorassigner.FlavorAssignmentMode
		wantTargets            []string
		wantFit                bool
		wantNoFitReason        string
		wantTopologyAssignment *tas.TopologyAssignment
	}{
		"quota and topology fit: fits without preemption": {
			quota:                  "20",
			cpu:                    "2",
			wantInitialMode:        flavorassigner.Fit,
			wantMode:               flavorassigner.Fit,
			wantFit:                true,
			wantTopologyAssignment: onNode,
		},
		"quota fits, topology does not: fits by preempting the lower-priority workload": {
			quota:                  "20",
			cpu:                    "3",
			wantInitialMode:        flavorassigner.Fit,
			wantMode:               flavorassigner.Preempt,
			wantTargets:            []string{victimName},
			wantFit:                true,
			wantTopologyAssignment: onNode,
		},
		"quota fits, topology does not and preemption cannot free enough: reserves topology assuming an empty cluster": {
			quota:                  "20",
			cpu:                    "5",
			wantInitialMode:        flavorassigner.Fit,
			wantMode:               flavorassigner.Preempt,
			wantFit:                false,
			wantTopologyAssignment: onNode,
		},
		"quota fits, topology does not fit even in an empty cluster: does not fit": {
			quota:           "20",
			cpu:             "7",
			wantInitialMode: flavorassigner.Fit,
			wantMode:        flavorassigner.NoFit,
			wantFit:         false,
			wantNoFitReason: kueue.WorkloadQuotaReservedReasonTopologyPlacementFailed,
		},
		"quota requires preemption, topology does not fit even in an empty cluster: does not fit": {
			quota:           "8",
			cpu:             "7",
			wantInitialMode: flavorassigner.Preempt,
			wantMode:        flavorassigner.NoFit,
			wantFit:         false,
			wantNoFitReason: kueue.WorkloadQuotaReservedReasonTopologyPlacementFailed,
		},
		"quota requires preemption: fits by preempting the lower-priority workload": {
			quota:                  "4",
			cpu:                    "2",
			wantInitialMode:        flavorassigner.Preempt,
			wantMode:               flavorassigner.Preempt,
			wantTargets:            []string{victimName},
			wantFit:                true,
			wantTopologyAssignment: onNode,
		},
		"quota requires preemption, only a higher-priority workload could free it: reserves topology assuming an empty cluster": {
			quota:                  "4",
			cpu:                    "4",
			wantInitialMode:        flavorassigner.Preempt,
			wantMode:               flavorassigner.Preempt,
			wantFit:                false,
			wantTopologyAssignment: onNode,
		},
		"request exceeds quota: does not fit": {
			quota:           "4",
			cpu:             "5",
			wantInitialMode: flavorassigner.NoFit,
			wantMode:        flavorassigner.NoFit,
			wantFit:         false,
			wantNoFitReason: kueue.WorkloadQuotaReservedReasonExceedsMaxQuota,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// Given
			features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
				features.TopologyAwareScheduling:          true,
				features.UnadmittedWorkloadsObservability: true,
			})

			log := testr.New(t)
			ctx := ctrllog.IntoContext(t.Context(), log)
			snapshot := newTASTestSnapshot(ctx, t, log, tc.quota, tc.evictableBlocker)

			wl := workload.NewInfo(log, utiltestingapi.MakeWorkload("incoming", "default").
				Priority(incomingPriority).
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					RequiredTopologyRequest(corev1.LabelHostname).
					Request(corev1.ResourceCPU, tc.cpu).
					Obj()).
				Obj())
			wl.ClusterQueue = tasTestCQ

			preemptor := preemption.New(
				utiltesting.NewFakeClient(), workload.Ordering{}, &utiltesting.EventRecorder{}, nil, false,
				clocktesting.NewFakeClock(time.Now()), nil, preemptexpectations.New(), nil,
			)
			assigner := flavorassigner.New(
				wl, snapshot.ClusterQueue(tasTestCQ), snapshot.ResourceFlavors, false,
				preemption.NewOracle(preemptor, snapshot), nil,
				configapi.QuotaCheckBlockUndeclared, resources.NewResourceFormatter(), 0,
			)
			initialAssignment := assigner.AssignFlavors(ctx, log, nil)
			if gotMode := initialAssignment.RepresentativeMode(); gotMode != tc.wantInitialMode {
				t.Fatalf("AssignFlavors() mode mismatch: want %v, got %v", tc.wantInitialMode, gotMode)
			}
			freeCapacityBefore := freeCapacity(t, snapshot)

			// When
			finder := NewInternalFitFinder(wl, snapshot, preemptor, assigner)
			gotResult := finder.FindFit(ctx, &initialAssignment)

			// Then
			if gotResult.Assignment != &initialAssignment {
				t.Errorf("FindFit() returned a different assignment object than the initial one")
			}
			if gotMode := gotResult.Assignment.RepresentativeMode(); gotMode != tc.wantMode {
				t.Errorf("FindFit() assignment mode mismatch: want %v, got %v", tc.wantMode, gotMode)
			}
			if diff := cmp.Diff(tc.wantTargets, targetNames(gotResult.PreemptionTargets)); diff != "" {
				t.Errorf("FindFit() preemption targets mismatch (-want +got):\n%s", diff)
			}
			if tc.wantFit != gotResult.CanFit() {
				t.Errorf("FindFit() returned unexpected verdict - want fit: %v, got: %v", tc.wantFit, gotResult.CanFit())
			}
			if gotResult.Assignment.NoFitReason != tc.wantNoFitReason {
				t.Errorf("FindFit() assignment NoFitReason mismatch: want %q, got %q", tc.wantNoFitReason, gotResult.Assignment.NoFitReason)
			}
			if diff := cmp.Diff(tc.wantTopologyAssignment, gotResult.Assignment.PodSets[0].TopologyAssignment); diff != "" {
				t.Errorf("FindFit() topology assignment mismatch (-want +got):\n%s", diff)
			}
			if freeCapacityAfter := freeCapacity(t, snapshot); freeCapacityAfter != freeCapacityBefore {
				t.Errorf("FindFit() did not restore the snapshot TAS free capacity: before %s, after %s", freeCapacityBefore, freeCapacityAfter)
			}
		})
	}
}

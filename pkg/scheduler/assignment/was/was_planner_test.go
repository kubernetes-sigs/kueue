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

package was

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/testr"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/component-base/featuregate"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	wascache "sigs.k8s.io/kueue/pkg/cache/scheduler/was"
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
	testCQ        = "tas-cq"
	testFlavor    = "tas-flavor"
	testNamespace = "default"
	testNode1     = "node-1"
	testNode2     = "node-2"
	unknownNode   = "node-unknown"
	testNodeCPUs  = "8"

	victimName   = "victim"
	incomingName = "incoming"

	// The incoming workload may preempt workloads with a lower priority only.
	lowPriority      = 0
	incomingPriority = 50
)

type testSimulator struct {
	simulator.SchedulerSimulator

	result simulator.SchedulingResult
	calls  int
}

var _ simulator.SchedulerSimulator = (*testSimulator)(nil)

func (f *testSimulator) ScheduleWorkload(_ context.Context, pods []*corev1.Pod, _ ...simulator.ScheduleOption) simulator.SchedulingResult {
	f.calls++
	return f.result
}

// podKey returns the key of the virtual Pod which the planner builds for the
// given replica of the incoming workload's PodSet. It uses CandidateVirtualPodsForPodSet
// to ensure the keys of the predefined PodPlacements match the Pods
// passed to ScheduleWorkload.
func podKey(psName kueue.PodSetReference, replicaIdx int) client.ObjectKey {
	wl := &kueue.Workload{Name: incomingName, Namespace: testNamespace}
	ps := &kueue.PodSet{Name: psName}
	pods, err := wascache.CandidateVirtualPodsForPodSet(wl, ps, int32(replicaIdx+1), wascache.CandidatePodOptions{})
	if err != nil {
		panic(fmt.Sprintf("building virtual Pods for PodSet %q: %v", psName, err))
	}
	return client.ObjectKeyFromObject(pods[replicaIdx])
}

// newTestSnapshot builds a snapshot with a single TAS flavor backed by two
// hostname-level nodes with 8 CPU each, and a ClusterQueue with the given CPU
// quota that allows preempting lower-priority workloads within the ClusterQueue.
// An admitted lower-priority workload, "victim", occupies 2 CPU of node-1.
func newTestSnapshot(ctx context.Context, t *testing.T, log logr.Logger, quota string) *schdcache.Snapshot {
	t.Helper()

	cache := schdcache.New(utiltesting.NewFakeClient())
	cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor(testFlavor).
		NodeLabel("tas", "true").TopologyName("topology").Obj())
	cache.AddOrUpdateTopology(log, utiltestingapi.MakeTopology("topology").Levels(corev1.LabelHostname).Obj())
	for _, node := range []string{testNode1, testNode2} {
		cache.TASCache().SyncNode(testingnode.MakeNode(node).
			Label("tas", "true").
			Label(corev1.LabelHostname, node).
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:  resource.MustParse(testNodeCPUs),
				corev1.ResourcePods: resource.MustParse("32"),
			}).
			Ready().
			Obj())
	}
	if err := cache.AddClusterQueue(ctx, utiltestingapi.MakeClusterQueue(testCQ).
		Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas(testFlavor).Resource(corev1.ResourceCPU, quota).Obj()).
		Obj()); err != nil {
		t.Fatalf("adding ClusterQueue: %v", err)
	}

	victim := utiltestingapi.MakeWorkload(victimName, testNamespace).
		Priority(lowPriority).
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
			RequiredTopologyRequest(corev1.LabelHostname).
			Request(corev1.ResourceCPU, "2").
			Obj()).
		ReserveQuotaAt(utiltestingapi.MakeAdmission(testCQ).
			PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
				Assignment(corev1.ResourceCPU, testFlavor, "2").
				TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
					Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{testNode1}, 1).Obj()).
					Obj()).
				Obj()).
			Obj(), time.Now()).
		Obj()
	if !cache.AddOrUpdateWorkload(ctx, log, victim) {
		t.Fatalf("adding workload %q to the cache", victimName)
	}

	snapshot, err := cache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("building snapshot: %v", err)
	}
	return snapshot
}

func freeCapacity(t *testing.T, snapshot *schdcache.Snapshot) string {
	t.Helper()
	got, err := snapshot.ClusterQueue(testCQ).TASFlavors[testFlavor].SerializeFreeCapacityPerDomain()
	if err != nil {
		t.Fatalf("serializing TAS free capacity: %v", err)
	}
	return got
}

// onNodes builds the expected hostname-level topology assignment.
// Domains must be listed in the topology order (i.e. sorted by node name).
func onNodes(domains ...tas.TopologyDomainAssignment) *tas.TopologyAssignment {
	return &utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
		Domains(domains...).
		TopologyAssignment
}

func domain(node string, count int32) tas.TopologyDomainAssignment {
	return utiltestingapi.MakeTopologyDomainAssignment([]string{node}, count).Obj()
}

type testPodSet struct {
	name  kueue.PodSetReference
	count int
	// cpu is the CPU requested by a single pod of the PodSet.
	cpu string
}

func podFailure(psName kueue.PodSetReference, replicaIdx int, reason string) string {
	return fmt.Sprintf("pod: %s, failure reason: %s", podKey(psName, replicaIdx), reason)
}

var podSetAssignmentCmpOpts = cmp.Options{
	cmpopts.IgnoreFields(
		flavorassigner.PodSetAssignment{},
		"Flavors",
		"Requests",
		"Count",
		"DelayedTopologyRequest",
		"FlavorAssignmentAttempts",
	),
	cmp.AllowUnexported(flavorassigner.Status{}),
	cmpopts.EquateErrors(),
	cmpopts.SortSlices(func(a, b string) bool { return a < b }),
	cmpopts.EquateEmpty(),
}

// TestPlan runs Plan with the real flavor assigner and a test SchedulerLibrary.
// The initial assignment is computed by the flavor assigner, as in the scheduler,
// while each test case defines what ScheduleWorkload returns.
func TestPlan(t *testing.T) {
	errSchedule := errors.New("schedule library failure")
	errPlacement := errors.New("pod placement error")
	errInsufficientCPU := errors.New("Insufficient cpu")
	errNodeAffinity := errors.New("node(s) didn't match Pod's node affinity")

	cases := map[string]struct {
		quota   string
		podSets []testPodSet
		// initialMode documents the quota-based mode computed by the flavor assigner.
		initialMode flavorassigner.FlavorAssignmentMode
		// When set as Nil, ScheduleWorkload is expected not to be called.
		schedulingResult *simulator.SchedulingResult

		wantMode        flavorassigner.FlavorAssignmentMode
		wantFit         bool
		wantErr         string
		wantNoFitReason string
		wantPodSets     []flavorassigner.PodSetAssignment
	}{
		"initial assignment does not fit the quota: returned as is, without scheduling": {
			quota:           "3",
			podSets:         []testPodSet{{name: kueue.DefaultPodSetName, count: 1, cpu: "4"}},
			initialMode:     flavorassigner.NoFit,
			wantMode:        flavorassigner.NoFit,
			wantNoFitReason: kueue.WorkloadQuotaReservedReasonExceedsMaxQuota,
			wantPodSets: []flavorassigner.PodSetAssignment{{
				Name: kueue.DefaultPodSetName,
				Status: *flavorassigner.NewStatus(
					"insufficient quota for cpu in flavor tas-flavor, previously considered podsets requests (0) + current podset request (4) > maximum capacity (3)",
				),
			}},
		},
		"initial assignment requires preemption: not supported": {
			quota:       "3",
			podSets:     []testPodSet{{name: kueue.DefaultPodSetName, count: 1, cpu: "2"}},
			initialMode: flavorassigner.Preempt,
			wantMode:    flavorassigner.Preempt,
			wantErr:     "WAS Planner does not support preemptions",
			wantPodSets: []flavorassigner.PodSetAssignment{{
				Name:   kueue.DefaultPodSetName,
				Status: *flavorassigner.NewStatus("insufficient unused quota for cpu in flavor tas-flavor, 1 more needed"),
			}},
		},
		"ScheduleWorkload fails completely: planning error": {
			quota:            "20",
			podSets:          []testPodSet{{name: kueue.DefaultPodSetName, count: 2, cpu: "1"}},
			initialMode:      flavorassigner.Fit,
			schedulingResult: &simulator.SchedulingResult{Error: errSchedule},
			wantMode:         flavorassigner.Fit,
			wantErr:          "failed to schedule workload: " + errSchedule.Error(),
			wantPodSets: []flavorassigner.PodSetAssignment{
				{Name: kueue.DefaultPodSetName},
			},
		},
		"all pods placed on a single node: fits": {
			quota:       "20",
			podSets:     []testPodSet{{name: kueue.DefaultPodSetName, count: 2, cpu: "1"}},
			initialMode: flavorassigner.Fit,
			schedulingResult: &simulator.SchedulingResult{PodPlacements: simulator.PodPlacements{
				podKey(kueue.DefaultPodSetName, 0): {Node: testNode1},
				podKey(kueue.DefaultPodSetName, 1): {Node: testNode1},
			}},
			wantMode: flavorassigner.Fit,
			wantFit:  true,
			wantPodSets: []flavorassigner.PodSetAssignment{
				{Name: kueue.DefaultPodSetName, TopologyAssignment: onNodes(domain(testNode1, 2))},
			},
		},
		"pods spread across nodes: fits, with a domain per node": {
			quota:       "20",
			podSets:     []testPodSet{{name: kueue.DefaultPodSetName, count: 3, cpu: "1"}},
			initialMode: flavorassigner.Fit,
			schedulingResult: &simulator.SchedulingResult{PodPlacements: simulator.PodPlacements{
				podKey(kueue.DefaultPodSetName, 0): {Node: testNode2},
				podKey(kueue.DefaultPodSetName, 1): {Node: testNode1},
				podKey(kueue.DefaultPodSetName, 2): {Node: testNode2},
			}},
			wantMode: flavorassigner.Fit,
			wantFit:  true,
			wantPodSets: []flavorassigner.PodSetAssignment{{
				Name:               kueue.DefaultPodSetName,
				TopologyAssignment: onNodes(domain(testNode1, 1), domain(testNode2, 2)),
			}},
		},
		"a pod fails to be scheduled: the whole PodSet does not fit": {
			quota:       "20",
			podSets:     []testPodSet{{name: kueue.DefaultPodSetName, count: 2, cpu: "1"}},
			initialMode: flavorassigner.Fit,
			schedulingResult: &simulator.SchedulingResult{PodPlacements: simulator.PodPlacements{
				podKey(kueue.DefaultPodSetName, 0): {Node: testNode1},
				podKey(kueue.DefaultPodSetName, 1): {Error: errInsufficientCPU, Reasons: []string{errInsufficientCPU.Error()}},
			}},
			wantMode:        flavorassigner.NoFit,
			wantNoFitReason: kueue.WorkloadQuotaReservedReasonTopologyPlacementFailed,
			wantPodSets: []flavorassigner.PodSetAssignment{{
				Name: kueue.DefaultPodSetName,
				Status: *flavorassigner.NewErrorStatus(errInsufficientCPU, []string{
					podFailure(kueue.DefaultPodSetName, 1, errInsufficientCPU.Error()),
				}),
			}},
		},
		"a pod placement errors out: the whole PodSet does not fit and reports the error": {
			quota:       "20",
			podSets:     []testPodSet{{name: kueue.DefaultPodSetName, count: 2, cpu: "1"}},
			initialMode: flavorassigner.Fit,
			schedulingResult: &simulator.SchedulingResult{PodPlacements: simulator.PodPlacements{
				podKey(kueue.DefaultPodSetName, 0): {Error: errPlacement, Reasons: []string{"plugin failed"}},
				podKey(kueue.DefaultPodSetName, 1): {Node: testNode1},
			}},
			wantMode:        flavorassigner.NoFit,
			wantNoFitReason: kueue.WorkloadQuotaReservedReasonTopologyPlacementFailed,
			wantPodSets: []flavorassigner.PodSetAssignment{{
				Name: kueue.DefaultPodSetName,
				Status: *flavorassigner.NewErrorStatus(errPlacement, []string{
					podFailure(kueue.DefaultPodSetName, 0, "plugin failed"),
				}),
			}},
		},
		"pod placed on a node unknown to the topology: does not fit": {
			quota:       "20",
			podSets:     []testPodSet{{name: kueue.DefaultPodSetName, count: 1, cpu: "1"}},
			initialMode: flavorassigner.Fit,
			schedulingResult: &simulator.SchedulingResult{PodPlacements: simulator.PodPlacements{
				podKey(kueue.DefaultPodSetName, 0): {Node: unknownNode},
			}},
			wantMode:        flavorassigner.NoFit,
			wantNoFitReason: kueue.WorkloadQuotaReservedReasonTopologyPlacementFailed,
			wantPodSets: []flavorassigner.PodSetAssignment{{
				Name:   kueue.DefaultPodSetName,
				Status: *flavorassigner.NewStatus("node " + unknownNode + " is not defined in the known topology")},
			},
		},
		"multiple PodSets, all pods placed: fits, with a topology assignment per PodSet": {
			quota: "20",
			podSets: []testPodSet{
				{name: "leader", count: 1, cpu: "1"},
				{name: "workers", count: 2, cpu: "1"},
			},
			initialMode: flavorassigner.Fit,
			schedulingResult: &simulator.SchedulingResult{PodPlacements: simulator.PodPlacements{
				podKey("leader", 0):  {Node: testNode2},
				podKey("workers", 0): {Node: testNode1},
				podKey("workers", 1): {Node: testNode1},
			}},
			wantMode: flavorassigner.Fit,
			wantFit:  true,
			wantPodSets: []flavorassigner.PodSetAssignment{
				{Name: "leader", TopologyAssignment: onNodes(domain(testNode2, 1))},
				{Name: "workers", TopologyAssignment: onNodes(domain(testNode1, 2))},
			},
		},
		"multiple PodSets, one fails: the workload does not fit": {
			quota: "20",
			podSets: []testPodSet{
				{name: "leader", count: 1, cpu: "1"},
				{name: "workers", count: 2, cpu: "1"},
			},
			initialMode: flavorassigner.Fit,
			schedulingResult: &simulator.SchedulingResult{PodPlacements: simulator.PodPlacements{
				podKey("leader", 0):  {Node: testNode2},
				podKey("workers", 0): {Node: testNode1},
				podKey("workers", 1): {Error: errNodeAffinity, Reasons: []string{errNodeAffinity.Error()}},
			}},
			wantMode:        flavorassigner.NoFit,
			wantNoFitReason: kueue.WorkloadQuotaReservedReasonTopologyPlacementFailed,
			// Like in the native planner, no PodSet gets a topology assignment,
			// including the ones which got placed.
			wantPodSets: []flavorassigner.PodSetAssignment{
				{Name: "leader"},
				{
					Name: "workers",
					Status: *flavorassigner.NewErrorStatus(errNodeAffinity, []string{
						podFailure("workers", 1, errNodeAffinity.Error()),
					}),
				},
			},
		},
		"all pods fail to be scheduled: does not fit": {
			quota:       "20",
			podSets:     []testPodSet{{name: kueue.DefaultPodSetName, count: 2, cpu: "1"}},
			initialMode: flavorassigner.Fit,
			schedulingResult: &simulator.SchedulingResult{PodPlacements: simulator.PodPlacements{
				podKey(kueue.DefaultPodSetName, 0): {Error: errInsufficientCPU, Reasons: []string{errInsufficientCPU.Error()}},
				podKey(kueue.DefaultPodSetName, 1): {Error: errInsufficientCPU, Reasons: []string{errInsufficientCPU.Error()}},
			}},
			wantMode:        flavorassigner.NoFit,
			wantNoFitReason: kueue.WorkloadQuotaReservedReasonTopologyPlacementFailed,
			wantPodSets: []flavorassigner.PodSetAssignment{{
				Name: kueue.DefaultPodSetName,
				Status: *flavorassigner.NewErrorStatus(errInsufficientCPU, []string{
					podFailure(kueue.DefaultPodSetName, 0, errInsufficientCPU.Error()),
					podFailure(kueue.DefaultPodSetName, 1, errInsufficientCPU.Error()),
				}),
			}},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// Given
			features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
				features.TopologyAwareScheduling:          true,
				features.SchedulerLibraryDeepIntegration:  true,
				features.UnadmittedWorkloadsObservability: true,
			})

			log := testr.New(t)
			ctx := ctrllog.IntoContext(t.Context(), log)
			snapshot := newTestSnapshot(ctx, t, log, tc.quota)
			testSim := &testSimulator{SchedulerSimulator: snapshot.SchedulerSimulator}
			if tc.schedulingResult != nil {
				testSim.result = *tc.schedulingResult
			}
			snapshot.SchedulerSimulator = testSim

			var podSets []kueue.PodSet
			for _, ps := range tc.podSets {
				podSets = append(podSets, *utiltestingapi.MakePodSet(ps.name, ps.count).
					RequiredTopologyRequest(corev1.LabelHostname).
					Request(corev1.ResourceCPU, ps.cpu).
					Obj())
			}
			wl := workload.NewInfo(log, utiltestingapi.MakeWorkload(incomingName, testNamespace).
				Priority(incomingPriority).
				PodSets(podSets...).
				Obj())
			wl.ClusterQueue = testCQ

			preemptor := preemption.New(
				utiltesting.NewFakeClient(), workload.Ordering{}, &utiltesting.EventRecorder{}, nil, false,
				clocktesting.NewFakeClock(time.Now()), nil, preemptexpectations.New(), nil,
			)
			assigner := flavorassigner.New(
				wl, snapshot.ClusterQueue(testCQ), snapshot.ResourceFlavors, false,
				preemption.NewOracle(preemptor, snapshot), nil,
				configapi.QuotaCheckBlockUndeclared, resources.NewResourceFormatter(), 0,
			)
			initialAssignment := assigner.AssignFlavors(ctx, log, nil)
			if initialMode := initialAssignment.RepresentativeMode(); initialMode != tc.initialMode {
				t.Fatalf("AssignFlavors() misconfigured - incorrect initial mode: want %v, got %v", tc.initialMode, initialMode)
			}
			freeCapacityBefore := freeCapacity(t, snapshot)

			// When
			planner := NewPlanner(wl, snapshot)
			gotPlan := planner.Plan(ctx, &initialAssignment)

			// Then
			if gotPlan.Assignment != &initialAssignment {
				t.Errorf("Plan() returned a different assignment object than the initial one")
			}
			wantCalls := 0
			if tc.schedulingResult != nil {
				wantCalls = 1
			}
			if testSim.calls != wantCalls {
				t.Errorf("Plan() called ScheduleWorkload unexpected number of times: want %d, got %d", wantCalls, testSim.calls)
			}
			switch {
			case tc.wantErr == "" && gotPlan.Error != nil:
				t.Errorf("Plan() returned unexpected error: %v", gotPlan.Error)
			case tc.wantErr != "" && gotPlan.Error == nil:
				t.Errorf("Plan() did not return the expected error %q", tc.wantErr)
			case tc.wantErr != "" && !strings.Contains(gotPlan.Error.Error(), tc.wantErr):
				t.Errorf("Plan() error mismatch: want it to contain %q, got %q", tc.wantErr, gotPlan.Error.Error())
			}
			if tc.schedulingResult != nil && tc.schedulingResult.Error != nil && !errors.Is(gotPlan.Error, tc.schedulingResult.Error) {
				t.Errorf("Plan() error does not wrap the ScheduleWorkload error: want %v, got %v", tc.schedulingResult.Error, gotPlan.Error)
			}
			if gotMode := gotPlan.Assignment.RepresentativeMode(); gotMode != tc.wantMode {
				t.Errorf("Plan() assignment mode mismatch: want %v, got %v", tc.wantMode, gotMode)
			}
			if tc.wantFit != gotPlan.CanFit() {
				t.Errorf("Plan() returned unexpected verdict - want fit: %v, got: %v", tc.wantFit, gotPlan.CanFit())
			}
			if len(gotPlan.PreemptionTargets) != 0 {
				t.Errorf("Plan() returned unexpected preemption targets: %v", gotPlan.PreemptionTargets)
			}
			if gotPlan.Assignment.NoFitReason != tc.wantNoFitReason {
				t.Errorf("Plan() assignment NoFitReason mismatch: want %q, got %q", tc.wantNoFitReason, gotPlan.Assignment.NoFitReason)
			}

			if diff := cmp.Diff(tc.wantPodSets, gotPlan.Assignment.PodSets, podSetAssignmentCmpOpts); diff != "" {
				t.Errorf("Plan() PodSet assignments mismatch (-want +got):\n%s", diff)
			}

			if freeCapacityAfter := freeCapacity(t, snapshot); freeCapacityAfter != freeCapacityBefore {
				t.Errorf("Plan() modified the snapshot TAS free capacity: before %s, after %s", freeCapacityBefore, freeCapacityAfter)
			}
		})
	}
}

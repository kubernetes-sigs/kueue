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
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/testr"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

	t      *testing.T
	result simulator.SchedulingResult
	calls  int
}

var _ simulator.SchedulerSimulator = (*testSimulator)(nil)

func (f *testSimulator) ScheduleWorkload(_ context.Context, pods []*corev1.Pod, _ ...simulator.ScheduleOption) simulator.SchedulingResult {
	f.t.Helper()
	f.calls++
	if f.result.PodPlacements != nil {
		// The predefined placements must cover exactly the Pods passed in,
		// as ScheduleWorkload returns a placement for every input Pod.
		gotKeys := make([]client.ObjectKey, 0, len(pods))
		for _, pod := range pods {
			gotKeys = append(gotKeys, client.ObjectKeyFromObject(pod))
		}
		wantKeys := slices.Collect(maps.Keys(f.result.PodPlacements))
		sortKeys := cmpopts.SortSlices(func(a, b client.ObjectKey) bool { return a.String() < b.String() })
		if diff := cmp.Diff(wantKeys, gotKeys, sortKeys); diff != "" {
			f.t.Fatalf("ScheduleWorkload() received Pods not matching the predefined placements (-want +got):\n%s", diff)
		}
	}
	return f.result
}

// podKey returns the key of the virtual Pod which the planner builds for the
// given replica of the incoming workload's PodSet. It relies on the production
// naming, so that the keys of the predefined PodPlacements match the Pods
// passed to ScheduleWorkload.
func podKey(psName kueue.PodSetReference, replicaIdx int) client.ObjectKey {
	wl := &kueue.Workload{ObjectMeta: metav1.ObjectMeta{Name: incomingName, Namespace: testNamespace}}
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

// TestPlan runs Plan with the real flavor assigner and a test ScheduleLibrary.
// The initial assignment is computed by the flavor assigner, as in the scheduler,
// while each test case defines what ScheduleWorkload returns.
func TestPlan(t *testing.T) {
	errSchedule := errors.New("schedule library failure")
	errPlacement := errors.New("pod placement error")

	cases := map[string]struct {
		quota   string
		podSets []testPodSet
		// initialMode documents the quota-based mode computed by the flavor assigner.
		initialMode flavorassigner.FlavorAssignmentMode
		// schedule is the result of ScheduleWorkload.
		// Nil means that ScheduleWorkload is expected not to be called.
		schedule *simulator.SchedulingResult

		wantMode            flavorassigner.FlavorAssignmentMode
		wantFit             bool
		wantErr             string
		wantNoFitReason     string
		wantTopology        map[kueue.PodSetReference]*tas.TopologyAssignment
		wantPodSetMessages  map[kueue.PodSetReference]string
		wantPodSetErrorFlag map[kueue.PodSetReference]bool
	}{
		"initial assignment does not fit the quota: returned as is, without scheduling": {
			quota:           "3",
			podSets:         []testPodSet{{name: kueue.DefaultPodSetName, count: 1, cpu: "4"}},
			initialMode:     flavorassigner.NoFit,
			wantMode:        flavorassigner.NoFit,
			wantNoFitReason: kueue.WorkloadQuotaReservedReasonExceedsMaxQuota,
		},
		"initial assignment requires preemption: not supported": {
			quota:       "3",
			podSets:     []testPodSet{{name: kueue.DefaultPodSetName, count: 1, cpu: "2"}},
			initialMode: flavorassigner.Preempt,
			wantMode:    flavorassigner.Preempt,
			wantErr:     "WAS Planner does not support preemptions",
		},
		"ScheduleWorkload fails completely: planning error": {
			quota:       "20",
			podSets:     []testPodSet{{name: kueue.DefaultPodSetName, count: 2, cpu: "1"}},
			initialMode: flavorassigner.Fit,
			schedule:    &simulator.SchedulingResult{Error: errSchedule},
			wantMode:    flavorassigner.Fit,
			wantErr:     "failed to schedule workload: " + errSchedule.Error(),
		},
		"all pods placed on a single node: fits": {
			quota:       "20",
			podSets:     []testPodSet{{name: kueue.DefaultPodSetName, count: 2, cpu: "1"}},
			initialMode: flavorassigner.Fit,
			schedule: &simulator.SchedulingResult{PodPlacements: simulator.PodPlacements{
				podKey(kueue.DefaultPodSetName, 0): simulator.NewSuccessfulPlacement(testNode1),
				podKey(kueue.DefaultPodSetName, 1): simulator.NewSuccessfulPlacement(testNode1),
			}},
			wantMode: flavorassigner.Fit,
			wantFit:  true,
			wantTopology: map[kueue.PodSetReference]*tas.TopologyAssignment{
				kueue.DefaultPodSetName: onNodes(domain(testNode1, 2)),
			},
		},
		"pods spread across nodes: fits, with a domain per node": {
			quota:       "20",
			podSets:     []testPodSet{{name: kueue.DefaultPodSetName, count: 3, cpu: "1"}},
			initialMode: flavorassigner.Fit,
			schedule: &simulator.SchedulingResult{PodPlacements: simulator.PodPlacements{
				podKey(kueue.DefaultPodSetName, 0): simulator.NewSuccessfulPlacement(testNode2),
				podKey(kueue.DefaultPodSetName, 1): simulator.NewSuccessfulPlacement(testNode1),
				podKey(kueue.DefaultPodSetName, 2): simulator.NewSuccessfulPlacement(testNode2),
			}},
			wantMode: flavorassigner.Fit,
			wantFit:  true,
			wantTopology: map[kueue.PodSetReference]*tas.TopologyAssignment{
				kueue.DefaultPodSetName: onNodes(domain(testNode1, 1), domain(testNode2, 2)),
			},
		},
		"a pod fails to be scheduled: the whole PodSet does not fit": {
			quota:       "20",
			podSets:     []testPodSet{{name: kueue.DefaultPodSetName, count: 2, cpu: "1"}},
			initialMode: flavorassigner.Fit,
			schedule: &simulator.SchedulingResult{PodPlacements: simulator.PodPlacements{
				podKey(kueue.DefaultPodSetName, 0): simulator.NewSuccessfulPlacement(testNode1),
				podKey(kueue.DefaultPodSetName, 1): simulator.NewFailedPlacement("Insufficient cpu"),
			}},
			wantMode:        flavorassigner.NoFit,
			wantNoFitReason: kueue.WorkloadQuotaReservedReasonTopologyPlacementFailed,
			wantPodSetMessages: map[kueue.PodSetReference]string{
				kueue.DefaultPodSetName: "failure reason: Insufficient cpu",
			},
		},
		"a pod placement errors out: the whole PodSet does not fit and reports the error": {
			quota:       "20",
			podSets:     []testPodSet{{name: kueue.DefaultPodSetName, count: 2, cpu: "1"}},
			initialMode: flavorassigner.Fit,
			schedule: &simulator.SchedulingResult{PodPlacements: simulator.PodPlacements{
				podKey(kueue.DefaultPodSetName, 0): simulator.NewPlacementError(errPlacement, "plugin failed"),
				podKey(kueue.DefaultPodSetName, 1): simulator.NewSuccessfulPlacement(testNode1),
			}},
			wantMode:        flavorassigner.NoFit,
			wantNoFitReason: kueue.WorkloadQuotaReservedReasonTopologyPlacementFailed,
			wantPodSetMessages: map[kueue.PodSetReference]string{
				kueue.DefaultPodSetName: "error: " + errPlacement.Error(),
			},
			wantPodSetErrorFlag: map[kueue.PodSetReference]bool{
				kueue.DefaultPodSetName: true,
			},
		},
		"pod placed on a node unknown to the topology: does not fit": {
			quota:       "20",
			podSets:     []testPodSet{{name: kueue.DefaultPodSetName, count: 1, cpu: "1"}},
			initialMode: flavorassigner.Fit,
			schedule: &simulator.SchedulingResult{PodPlacements: simulator.PodPlacements{
				podKey(kueue.DefaultPodSetName, 0): simulator.NewSuccessfulPlacement(unknownNode),
			}},
			wantMode:        flavorassigner.NoFit,
			wantNoFitReason: kueue.WorkloadQuotaReservedReasonTopologyPlacementFailed,
			wantPodSetMessages: map[kueue.PodSetReference]string{
				kueue.DefaultPodSetName: "node " + unknownNode + " is not defined in the known topology",
			},
		},
		"multiple PodSets, all pods placed: fits, with a topology assignment per PodSet": {
			quota: "20",
			podSets: []testPodSet{
				{name: "leader", count: 1, cpu: "1"},
				{name: "workers", count: 2, cpu: "1"},
			},
			initialMode: flavorassigner.Fit,
			schedule: &simulator.SchedulingResult{PodPlacements: simulator.PodPlacements{
				podKey("leader", 0):  simulator.NewSuccessfulPlacement(testNode2),
				podKey("workers", 0): simulator.NewSuccessfulPlacement(testNode1),
				podKey("workers", 1): simulator.NewSuccessfulPlacement(testNode1),
			}},
			wantMode: flavorassigner.Fit,
			wantFit:  true,
			wantTopology: map[kueue.PodSetReference]*tas.TopologyAssignment{
				"leader":  onNodes(domain(testNode2, 1)),
				"workers": onNodes(domain(testNode1, 2)),
			},
		},
		"multiple PodSets, one fails: the workload does not fit": {
			quota: "20",
			podSets: []testPodSet{
				{name: "leader", count: 1, cpu: "1"},
				{name: "workers", count: 2, cpu: "1"},
			},
			initialMode: flavorassigner.Fit,
			schedule: &simulator.SchedulingResult{PodPlacements: simulator.PodPlacements{
				podKey("leader", 0):  simulator.NewSuccessfulPlacement(testNode2),
				podKey("workers", 0): simulator.NewSuccessfulPlacement(testNode1),
				podKey("workers", 1): simulator.NewFailedPlacement("node(s) didn't match Pod's node affinity"),
			}},
			wantMode:        flavorassigner.NoFit,
			wantNoFitReason: kueue.WorkloadQuotaReservedReasonTopologyPlacementFailed,
			// Like in the native planner, no PodSet gets a topology assignment,
			// including the ones which got placed.
			wantPodSetMessages: map[kueue.PodSetReference]string{
				"workers": "failure reason: node(s) didn't match Pod's node affinity",
			},
		},
		"all pods fail to be scheduled: does not fit": {
			quota:       "20",
			podSets:     []testPodSet{{name: kueue.DefaultPodSetName, count: 2, cpu: "1"}},
			initialMode: flavorassigner.Fit,
			schedule: &simulator.SchedulingResult{PodPlacements: simulator.PodPlacements{
				podKey(kueue.DefaultPodSetName, 0): simulator.NewFailedPlacement("Insufficient cpu"),
				podKey(kueue.DefaultPodSetName, 1): simulator.NewFailedPlacement("Insufficient cpu"),
			}},
			wantMode:        flavorassigner.NoFit,
			wantNoFitReason: kueue.WorkloadQuotaReservedReasonTopologyPlacementFailed,
			wantPodSetMessages: map[kueue.PodSetReference]string{
				kueue.DefaultPodSetName: "failure reason: Insufficient cpu",
			},
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
			testSim := &testSimulator{SchedulerSimulator: snapshot.SchedulerSimulator, t: t}
			if tc.schedule != nil {
				testSim.result = *tc.schedule
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
			if tc.schedule != nil {
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
			if tc.schedule != nil && tc.schedule.Error != nil && !errors.Is(gotPlan.Error, tc.schedule.Error) {
				t.Errorf("Plan() error does not wrap the ScheduleWorkload error: want %v, got %v", tc.schedule.Error, gotPlan.Error)
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

			gotTopology := make(map[kueue.PodSetReference]*tas.TopologyAssignment)
			for _, psa := range gotPlan.Assignment.PodSets {
				if psa.TopologyAssignment != nil {
					gotTopology[psa.Name] = psa.TopologyAssignment
				}
			}
			if diff := cmp.Diff(tc.wantTopology, gotTopology, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Plan() topology assignments mismatch (-want +got):\n%s", diff)
			}
			for _, psa := range gotPlan.Assignment.PodSets {
				if want, found := tc.wantPodSetMessages[psa.Name]; found {
					if got := psa.Status.Message(); !strings.Contains(got, want) {
						t.Errorf("Plan() PodSet %q status message mismatch: want it to contain %q, got %q", psa.Name, want, got)
					}
				}
				if want, got := tc.wantPodSetErrorFlag[psa.Name], psa.Status.IsError(); want != got {
					t.Errorf("Plan() PodSet %q status error mismatch: want error: %v, got: %v", psa.Name, want, got)
				}
			}

			if freeCapacityAfter := freeCapacity(t, snapshot); freeCapacityAfter != freeCapacityBefore {
				t.Errorf("Plan() modified the snapshot TAS free capacity: before %s, after %s", freeCapacityBefore, freeCapacityAfter)
			}
		})
	}
}

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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/testr"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	schedulingv1alpha3 "k8s.io/api/scheduling/v1alpha3"
	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/sets"
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
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/policy"
	"sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
)

const (
	testCQ          = "tas-cq"
	testFlavor      = "tas-flavor"
	testNoFitFlavor = "no-fit-flavor"
	testNamespace   = "default"
	testNode1       = "node-1"
	testNode2       = "node-2"
	testNodeCPUs    = "8"

	testBlockLabel = "cloud.com/topology-block"
	testBlock      = "block-1"

	victimName   = "victim"
	incomingName = "incoming"

	// The incoming workload may preempt workloads with a lower priority only.
	lowPriority      = 0
	incomingPriority = 50
)

type testSimulator struct {
	simulator.SchedulerSimulator

	results          []simulator.SchedulingResult
	calls            int
	gotCandidates    [][]client.ObjectKey
	gotSimulateEmpty []bool
	scheduledPods    []*corev1.Pod
	addedPGs         []*schedulingv1beta1.PodGroup
	removedPGs       []*schedulingv1beta1.PodGroup
	addedCPGs        []*schedulingv1alpha3.CompositePodGroup
	removedCPGs      []*schedulingv1alpha3.CompositePodGroup
}

var _ simulator.SchedulerSimulator = (*testSimulator)(nil)

func (f *testSimulator) ScheduleWorkload(_ context.Context, pods []*corev1.Pod, opts ...simulator.ScheduleOption) simulator.SchedulingResult {
	idx := f.calls
	f.calls++
	f.scheduledPods = append(f.scheduledPods, pods...)
	var res simulator.SchedulingResult
	switch {
	case idx < len(f.results):
		res = f.results[idx]
	case len(f.results) > 0:
		res = f.results[len(f.results)-1]
	}
	scheduleOpts := simulator.BuildScheduleOptions(opts...)
	f.gotSimulateEmpty = append(f.gotSimulateEmpty, scheduleOpts.SimulateEmpty)
	candidates := scheduleOpts.PreemptionCandidates
	pf := scheduleOpts.PreemptionFilter
	allPlaced := res.Error == nil && len(res.PodPlacements) > 0
	for _, p := range res.PodPlacements {
		if !p.IsSuccess() {
			allPlaced = false
			break
		}
	}

	var callCandidates []client.ObjectKey
	if candidates != nil {
		nodeVictims := sets.New(res.PreemptedWorkloads...)
		seen := sets.New[client.ObjectKey]()
		for wlKey := range candidates {
			callCandidates = append(callCandidates, wlKey)
			seen.Insert(wlKey)
			if pf != nil {
				pf.PreemptWorkload(wlKey)
			}
			if allPlaced && seen.HasAll(res.PreemptedWorkloads...) && (pf == nil || pf.MayFit()) {
				break
			}
		}
		if allPlaced && pf != nil {
			var finalPreempted []client.ObjectKey
			for _, wlKey := range slices.Backward(callCandidates) {
				if nodeVictims.Has(wlKey) {
					finalPreempted = append(finalPreempted, wlKey)
					continue
				}
				pf.UnpreemptWorkload(wlKey)
				if !pf.MayFit() {
					pf.PreemptWorkload(wlKey)
					finalPreempted = append(finalPreempted, wlKey)
				}
			}
			slices.Reverse(finalPreempted)
			res.PreemptedWorkloads = finalPreempted
		}
	}
	f.gotCandidates = append(f.gotCandidates, callCandidates)
	return res
}

func (f *testSimulator) AddPodGroup(ctx context.Context, pg *schedulingv1beta1.PodGroup) error {
	f.addedPGs = append(f.addedPGs, pg)
	if f.SchedulerSimulator != nil {
		return f.SchedulerSimulator.AddPodGroup(ctx, pg)
	}
	return nil
}

func (f *testSimulator) RemovePodGroup(ctx context.Context, pg *schedulingv1beta1.PodGroup) error {
	f.removedPGs = append(f.removedPGs, pg)
	if f.SchedulerSimulator != nil {
		return f.SchedulerSimulator.RemovePodGroup(ctx, pg)
	}
	return nil
}

func (f *testSimulator) AddCompositePodGroup(ctx context.Context, cpg *schedulingv1alpha3.CompositePodGroup) error {
	f.addedCPGs = append(f.addedCPGs, cpg)
	if f.SchedulerSimulator != nil {
		return f.SchedulerSimulator.AddCompositePodGroup(ctx, cpg)
	}
	return nil
}

func (f *testSimulator) RemoveCompositePodGroup(ctx context.Context, cpg *schedulingv1alpha3.CompositePodGroup) error {
	f.removedCPGs = append(f.removedCPGs, cpg)
	if f.SchedulerSimulator != nil {
		return f.SchedulerSimulator.RemoveCompositePodGroup(ctx, cpg)
	}
	return nil
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

// newTestSnapshot builds a snapshot with:
//   - a TAS flavor per given FlavorQuotas, all sharing the same topology,
//   - a two-level topology: one block with two nodes, 8 CPU each,
//     matching only the testFlavor's node labels,
//   - a ClusterQueue with a single ResourceGroup built from the given
//     FlavorQuotas, in order, that allows preempting lower-priority
//     workloads within the ClusterQueue.
//
// An admitted lower-priority workload, "victim", occupies 2 CPU of node-1
// in testFlavor, so the given FlavorQuotas must include testFlavor.
func newTestSnapshot(ctx context.Context, t *testing.T, log logr.Logger, flavorQuotas ...kueue.FlavorQuotas) *schdcache.Snapshot {
	t.Helper()

	cache := schdcache.New(utiltesting.NewFakeClient())
	for _, fq := range flavorQuotas {
		cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor(string(fq.Name)).
			NodeLabel("tas", string(fq.Name)).TopologyName("topology").Obj())
	}
	cache.AddOrUpdateTopology(log, utiltestingapi.MakeTopology("topology").Levels(testBlockLabel, corev1.LabelHostname).Obj())
	for _, node := range []string{testNode1, testNode2} {
		cache.TASCache().SyncNode(testingnode.MakeNode(node).
			Label("tas", testFlavor).
			Label(testBlockLabel, testBlock).
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
		ResourceGroup(flavorQuotas...).
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

// cpuQuota returns the FlavorQuotas of the given flavor with the given CPU quota.
func cpuQuota(flavor kueue.ResourceFlavorReference, quota string) kueue.FlavorQuotas {
	return *utiltestingapi.MakeFlavorQuotas(string(flavor)).Resource(corev1.ResourceCPU, quota).Obj()
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
	// requiredLevel is the required topology level of the PodSet.
	// Defaults to hostname.
	requiredLevel string
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
	),
	// The per-flavor reasons are verbose quota messages already covered by
	// the flavor assigner tests.
	cmpopts.IgnoreFields(flavorassigner.FlavorAssignmentAttempt{}, "Reasons"),
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
		// flavorQuotas are the ClusterQueue's flavors, in order. See newTestSnapshot.
		flavorQuotas []kueue.FlavorQuotas
		podSets      []testPodSet
		// initialMode documents the quota-based mode computed by the flavor assigner.
		initialMode flavorassigner.FlavorAssignmentMode
		// When set as Nil, ScheduleWorkload is expected not to be called.
		schedulingResult *simulator.SchedulingResult
		// emptyClusterResult, when set, is the result returned by the second ScheduleWorkload call
		// with WithSimulateEmpty(true) after preemption candidate simulation fails.
		emptyClusterResult *simulator.SchedulingResult

		wantMode        flavorassigner.FlavorAssignmentMode
		wantFit         bool
		wantErr         string
		wantNoFitReason string
		// wantCandidates, when set, checks the preemption candidates passed to each ScheduleWorkload call.
		wantCandidates [][]client.ObjectKey
		// wantSimulateEmpty, when set, checks the SimulateEmpty option passed to each ScheduleWorkload call.
		wantSimulateEmpty []bool
		// wantPreemptionTargets checks the workload keys of the PreemptionTargets returned in Plan.
		wantPreemptionTargets []client.ObjectKey
		// wantResolvedNoFitReason configures what should be the NoFitReason
		// the Assignemtn resolves to when the UnadmittedWorkloadsObservability
		// FG ius enabled.
		// When set the UnadmittedWorkloadsObservability feature will be enabled in the test.
		wantResolvedNoFitReason string
		wantPodSets             []flavorassigner.PodSetAssignment
	}{
		"initial assignment does not fit the quota: returned as is, without scheduling": {
			flavorQuotas:            []kueue.FlavorQuotas{cpuQuota(testFlavor, "3")},
			podSets:                 []testPodSet{{name: kueue.DefaultPodSetName, count: 1, cpu: "4"}},
			initialMode:             flavorassigner.NoFit,
			wantMode:                flavorassigner.NoFit,
			wantNoFitReason:         kueue.WorkloadQuotaReservedReasonExceedsMaxQuota,
			wantResolvedNoFitReason: kueue.WorkloadQuotaReservedReasonExceedsMaxQuota,
			wantPodSets: []flavorassigner.PodSetAssignment{{
				Name: kueue.DefaultPodSetName,
				Status: *flavorassigner.NewStatus(
					"insufficient quota for cpu in flavor tas-flavor, previously considered podsets requests (0) + current podset request (4) > maximum capacity (3)",
				),
				FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{
					{Flavor: testFlavor, Mode: flavorassigner.NoFit, NoFitReason: kueue.WorkloadQuotaReservedReasonExceedsMaxQuota},
				},
			}},
		},
		"initial assignment requires preemption and ScheduleWorkload succeeds: fits": {
			flavorQuotas: []kueue.FlavorQuotas{cpuQuota(testFlavor, "3")},
			podSets:      []testPodSet{{name: kueue.DefaultPodSetName, count: 1, cpu: "2"}},
			initialMode:  flavorassigner.Preempt,
			schedulingResult: &simulator.SchedulingResult{
				PodPlacements: simulator.PodPlacements{
					podKey(kueue.DefaultPodSetName, 0): {Node: testNode1},
				},
				PreemptedWorkloads: []client.ObjectKey{{Namespace: testNamespace, Name: victimName}},
			},
			wantMode:              flavorassigner.Preempt,
			wantFit:               true,
			wantCandidates:        [][]client.ObjectKey{{{Namespace: testNamespace, Name: victimName}}},
			wantPreemptionTargets: []client.ObjectKey{{Namespace: testNamespace, Name: victimName}},
			wantPodSets: []flavorassigner.PodSetAssignment{{
				Name:               kueue.DefaultPodSetName,
				Status:             *flavorassigner.NewStatus("insufficient unused quota for cpu in flavor tas-flavor, 1 more needed"),
				TopologyAssignment: onNodes(domain(testNode1, 1)),
				FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{{
					Flavor:                testFlavor,
					Mode:                  flavorassigner.Preempt,
					PreemptionPossibility: new(policy.Preempt),
				}},
			}},
		},
		"initial assignment requires preemption and ScheduleWorkload succeeds on free node without node-level victims: fits": {
			flavorQuotas: []kueue.FlavorQuotas{cpuQuota(testFlavor, "3")},
			podSets:      []testPodSet{{name: kueue.DefaultPodSetName, count: 1, cpu: "2"}},
			initialMode:  flavorassigner.Preempt,
			schedulingResult: &simulator.SchedulingResult{
				PodPlacements: simulator.PodPlacements{
					podKey(kueue.DefaultPodSetName, 0): {Node: testNode2},
				},
			},
			wantMode:              flavorassigner.Preempt,
			wantFit:               true,
			wantCandidates:        [][]client.ObjectKey{{{Namespace: testNamespace, Name: victimName}}},
			wantPreemptionTargets: []client.ObjectKey{{Namespace: testNamespace, Name: victimName}},
			wantPodSets: []flavorassigner.PodSetAssignment{{
				Name:               kueue.DefaultPodSetName,
				Status:             *flavorassigner.NewStatus("insufficient unused quota for cpu in flavor tas-flavor, 1 more needed"),
				TopologyAssignment: onNodes(domain(testNode2, 1)),
				FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{{
					Flavor:                testFlavor,
					Mode:                  flavorassigner.Preempt,
					PreemptionPossibility: new(policy.Preempt),
				}},
			}},
		},
		"initial assignment requires preemption, ScheduleWorkload fails with candidates but succeeds on empty cluster: keeps Preempt mode and reserves topology": {
			flavorQuotas: []kueue.FlavorQuotas{cpuQuota(testFlavor, "3")},
			podSets:      []testPodSet{{name: kueue.DefaultPodSetName, count: 1, cpu: "2"}},
			initialMode:  flavorassigner.Preempt,
			schedulingResult: &simulator.SchedulingResult{PodPlacements: simulator.PodPlacements{
				podKey(kueue.DefaultPodSetName, 0): {Error: errInsufficientCPU, Reasons: []string{errInsufficientCPU.Error()}},
			}},
			emptyClusterResult: &simulator.SchedulingResult{PodPlacements: simulator.PodPlacements{
				podKey(kueue.DefaultPodSetName, 0): {Node: testNode1},
			}},
			wantMode:          flavorassigner.Preempt,
			wantFit:           false,
			wantCandidates:    [][]client.ObjectKey{{{Namespace: testNamespace, Name: victimName}}, nil},
			wantSimulateEmpty: []bool{false, true},
			wantPodSets: []flavorassigner.PodSetAssignment{{
				Name:               kueue.DefaultPodSetName,
				Status:             *flavorassigner.NewStatus("insufficient unused quota for cpu in flavor tas-flavor, 1 more needed"),
				TopologyAssignment: onNodes(domain(testNode1, 1)),
				FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{{
					Flavor:                testFlavor,
					Mode:                  flavorassigner.Preempt,
					PreemptionPossibility: new(policy.Preempt),
				}},
			}},
		},
		"initial assignment requires preemption and ScheduleWorkload fails even on empty cluster: does not fit": {
			flavorQuotas: []kueue.FlavorQuotas{cpuQuota(testFlavor, "3")},
			podSets:      []testPodSet{{name: kueue.DefaultPodSetName, count: 1, cpu: "2"}},
			initialMode:  flavorassigner.Preempt,
			schedulingResult: &simulator.SchedulingResult{PodPlacements: simulator.PodPlacements{
				podKey(kueue.DefaultPodSetName, 0): {Error: errInsufficientCPU, Reasons: []string{errInsufficientCPU.Error()}},
			}},
			emptyClusterResult: &simulator.SchedulingResult{PodPlacements: simulator.PodPlacements{
				podKey(kueue.DefaultPodSetName, 0): {Error: errInsufficientCPU, Reasons: []string{errInsufficientCPU.Error()}},
			}},
			wantMode:                flavorassigner.NoFit,
			wantNoFitReason:         kueue.WorkloadQuotaReservedReasonSimulationFailed,
			wantResolvedNoFitReason: kueue.WorkloadQuotaReservedReasonSimulationFailed,
			wantCandidates:          [][]client.ObjectKey{{{Namespace: testNamespace, Name: victimName}}, nil},
			wantSimulateEmpty:       []bool{false, true},
			wantPodSets: []flavorassigner.PodSetAssignment{{
				Name: kueue.DefaultPodSetName,
				Status: *flavorassigner.NewStatus(
					podFailure(kueue.DefaultPodSetName, 0, errInsufficientCPU.Error()),
				).WithError(errInsufficientCPU).WithNoFitReason(kueue.WorkloadQuotaReservedReasonSimulationFailed),
				FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{{
					Flavor:                testFlavor,
					Mode:                  flavorassigner.NoFit,
					PreemptionPossibility: new(policy.Preempt),
					NoFitReason:           kueue.WorkloadQuotaReservedReasonSimulationFailed,
				}},
			}},
		},
		"ScheduleWorkload fails completely: planning error": {
			flavorQuotas:     []kueue.FlavorQuotas{cpuQuota(testFlavor, "20")},
			podSets:          []testPodSet{{name: kueue.DefaultPodSetName, count: 2, cpu: "1"}},
			initialMode:      flavorassigner.Fit,
			schedulingResult: &simulator.SchedulingResult{Error: errSchedule},
			wantMode:         flavorassigner.Fit,
			wantErr:          "failed to schedule workload: " + errSchedule.Error(),
			wantPodSets: []flavorassigner.PodSetAssignment{{
				Name:                     kueue.DefaultPodSetName,
				FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{{Flavor: testFlavor, Mode: flavorassigner.Fit}},
			}},
		},
		"no virtual pods to schedule: initial assignment returned unchanged, without scheduling": {
			flavorQuotas: []kueue.FlavorQuotas{cpuQuota(testFlavor, "20")},
			podSets:      []testPodSet{{name: kueue.DefaultPodSetName, count: 0, cpu: "1"}},
			initialMode:  flavorassigner.Fit,
			wantMode:     flavorassigner.Fit,
			wantFit:      true,
			wantPodSets: []flavorassigner.PodSetAssignment{{
				Name:                     kueue.DefaultPodSetName,
				FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{{Flavor: testFlavor, Mode: flavorassigner.Fit}},
			}},
		},
		"all pods placed on a single node: fits": {
			flavorQuotas: []kueue.FlavorQuotas{cpuQuota(testFlavor, "20")},
			podSets:      []testPodSet{{name: kueue.DefaultPodSetName, count: 2, cpu: "1"}},
			initialMode:  flavorassigner.Fit,
			schedulingResult: &simulator.SchedulingResult{PodPlacements: simulator.PodPlacements{
				podKey(kueue.DefaultPodSetName, 0): {Node: testNode1},
				podKey(kueue.DefaultPodSetName, 1): {Node: testNode1},
			}},
			wantMode: flavorassigner.Fit,
			wantFit:  true,
			wantPodSets: []flavorassigner.PodSetAssignment{{
				Name:                     kueue.DefaultPodSetName,
				TopologyAssignment:       onNodes(domain(testNode1, 2)),
				FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{{Flavor: testFlavor, Mode: flavorassigner.Fit}},
			}},
		},
		"pods spread across nodes: fits, with a domain per node": {
			flavorQuotas: []kueue.FlavorQuotas{cpuQuota(testFlavor, "20")},
			podSets:      []testPodSet{{name: kueue.DefaultPodSetName, count: 3, cpu: "1", requiredLevel: testBlockLabel}},
			initialMode:  flavorassigner.Fit,
			schedulingResult: &simulator.SchedulingResult{PodPlacements: simulator.PodPlacements{
				podKey(kueue.DefaultPodSetName, 0): {Node: testNode2},
				podKey(kueue.DefaultPodSetName, 1): {Node: testNode1},
				podKey(kueue.DefaultPodSetName, 2): {Node: testNode2},
			}},
			wantMode: flavorassigner.Fit,
			wantFit:  true,
			wantPodSets: []flavorassigner.PodSetAssignment{{
				Name:                     kueue.DefaultPodSetName,
				TopologyAssignment:       onNodes(domain(testNode1, 1), domain(testNode2, 2)),
				FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{{Flavor: testFlavor, Mode: flavorassigner.Fit}},
			}},
		},
		"a pod fails to be scheduled: the whole PodSet does not fit": {
			flavorQuotas: []kueue.FlavorQuotas{cpuQuota(testFlavor, "20")},
			podSets:      []testPodSet{{name: kueue.DefaultPodSetName, count: 2, cpu: "1"}},
			initialMode:  flavorassigner.Fit,
			schedulingResult: &simulator.SchedulingResult{PodPlacements: simulator.PodPlacements{
				podKey(kueue.DefaultPodSetName, 0): {Node: testNode1},
				podKey(kueue.DefaultPodSetName, 1): {Error: errInsufficientCPU, Reasons: []string{errInsufficientCPU.Error()}},
			}},
			wantMode:                flavorassigner.NoFit,
			wantNoFitReason:         kueue.WorkloadQuotaReservedReasonSimulationFailed,
			wantResolvedNoFitReason: kueue.WorkloadQuotaReservedReasonSimulationFailed,
			wantPodSets: []flavorassigner.PodSetAssignment{{
				Name: kueue.DefaultPodSetName,
				Status: *flavorassigner.NewStatus(
					podFailure(kueue.DefaultPodSetName, 1, errInsufficientCPU.Error()),
				).WithError(errInsufficientCPU).WithNoFitReason(kueue.WorkloadQuotaReservedReasonSimulationFailed),
				FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{
					{Flavor: testFlavor, Mode: flavorassigner.NoFit, NoFitReason: kueue.WorkloadQuotaReservedReasonSimulationFailed},
				},
			}},
		},
		"a pod placement errors out: the whole PodSet does not fit and reports the error": {
			flavorQuotas: []kueue.FlavorQuotas{cpuQuota(testFlavor, "20")},
			podSets:      []testPodSet{{name: kueue.DefaultPodSetName, count: 2, cpu: "1"}},
			initialMode:  flavorassigner.Fit,
			schedulingResult: &simulator.SchedulingResult{PodPlacements: simulator.PodPlacements{
				podKey(kueue.DefaultPodSetName, 0): {Error: errPlacement, Reasons: []string{"plugin failed"}},
				podKey(kueue.DefaultPodSetName, 1): {Node: testNode1},
			}},
			wantMode:                flavorassigner.NoFit,
			wantNoFitReason:         kueue.WorkloadQuotaReservedReasonSimulationFailed,
			wantResolvedNoFitReason: kueue.WorkloadQuotaReservedReasonSimulationFailed,
			wantPodSets: []flavorassigner.PodSetAssignment{{
				Name: kueue.DefaultPodSetName,
				Status: *flavorassigner.NewStatus(
					podFailure(kueue.DefaultPodSetName, 0, "plugin failed"),
				).WithError(errPlacement).WithNoFitReason(kueue.WorkloadQuotaReservedReasonSimulationFailed),
				FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{
					{Flavor: testFlavor, Mode: flavorassigner.NoFit, NoFitReason: kueue.WorkloadQuotaReservedReasonSimulationFailed},
				},
			}},
		},
		"multiple PodSets, all pods placed: fits, with a topology assignment per PodSet": {
			flavorQuotas: []kueue.FlavorQuotas{cpuQuota(testFlavor, "20")},
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
				{
					Name:                     "leader",
					TopologyAssignment:       onNodes(domain(testNode2, 1)),
					FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{{Flavor: testFlavor, Mode: flavorassigner.Fit}},
				},
				{
					Name:                     "workers",
					TopologyAssignment:       onNodes(domain(testNode1, 2)),
					FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{{Flavor: testFlavor, Mode: flavorassigner.Fit}},
				},
			},
		},
		"multiple PodSets, one fails: the workload does not fit": {
			flavorQuotas: []kueue.FlavorQuotas{cpuQuota(testFlavor, "20")},
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
			wantMode:                flavorassigner.NoFit,
			wantNoFitReason:         kueue.WorkloadQuotaReservedReasonSimulationFailed,
			wantResolvedNoFitReason: kueue.WorkloadQuotaReservedReasonSimulationFailed,
			// Like in the native planner, no PodSet gets a topology assignment,
			// including the ones which got placed.
			wantPodSets: []flavorassigner.PodSetAssignment{
				{
					Name: "leader",
					// Only the attempts of the failed PodSet are marked.
					FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{{Flavor: testFlavor, Mode: flavorassigner.Fit}},
				},
				{
					Name: "workers",
					Status: *flavorassigner.NewStatus(
						podFailure("workers", 1, errNodeAffinity.Error()),
					).WithError(errNodeAffinity).WithNoFitReason(kueue.WorkloadQuotaReservedReasonSimulationFailed),
					FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{
						{Flavor: testFlavor, Mode: flavorassigner.NoFit, NoFitReason: kueue.WorkloadQuotaReservedReasonSimulationFailed},
					},
				},
			},
		},
		"all pods fail to be scheduled: does not fit": {
			flavorQuotas: []kueue.FlavorQuotas{cpuQuota(testFlavor, "20")},
			podSets:      []testPodSet{{name: kueue.DefaultPodSetName, count: 2, cpu: "1"}},
			initialMode:  flavorassigner.Fit,
			schedulingResult: &simulator.SchedulingResult{PodPlacements: simulator.PodPlacements{
				podKey(kueue.DefaultPodSetName, 0): {Error: errInsufficientCPU, Reasons: []string{errInsufficientCPU.Error()}},
				podKey(kueue.DefaultPodSetName, 1): {Error: errInsufficientCPU, Reasons: []string{errInsufficientCPU.Error()}},
			}},
			wantMode:                flavorassigner.NoFit,
			wantNoFitReason:         kueue.WorkloadQuotaReservedReasonSimulationFailed,
			wantResolvedNoFitReason: kueue.WorkloadQuotaReservedReasonSimulationFailed,
			wantPodSets: []flavorassigner.PodSetAssignment{{
				Name: kueue.DefaultPodSetName,
				Status: *flavorassigner.NewStatus(
					podFailure(kueue.DefaultPodSetName, 0, errInsufficientCPU.Error()),
					podFailure(kueue.DefaultPodSetName, 1, errInsufficientCPU.Error()),
				).WithError(errInsufficientCPU).WithNoFitReason(kueue.WorkloadQuotaReservedReasonSimulationFailed),
				FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{
					{Flavor: testFlavor, Mode: flavorassigner.NoFit, NoFitReason: kueue.WorkloadQuotaReservedReasonSimulationFailed},
				},
			}},
		},
		"multiple flavors, simulation fails on the selected flavor: resolved as SimulationFailed": {
			flavorQuotas: []kueue.FlavorQuotas{cpuQuota(testNoFitFlavor, "1"), cpuQuota(testFlavor, "20")},
			podSets:      []testPodSet{{name: kueue.DefaultPodSetName, count: 2, cpu: "1"}},
			initialMode:  flavorassigner.Fit,
			schedulingResult: &simulator.SchedulingResult{PodPlacements: simulator.PodPlacements{
				podKey(kueue.DefaultPodSetName, 0): {Node: testNode1},
				podKey(kueue.DefaultPodSetName, 1): {Error: errInsufficientCPU, Reasons: []string{errInsufficientCPU.Error()}},
			}},
			wantMode:                flavorassigner.NoFit,
			wantNoFitReason:         kueue.WorkloadQuotaReservedReasonSimulationFailed,
			wantResolvedNoFitReason: kueue.WorkloadQuotaReservedReasonSimulationFailed,
			wantPodSets: []flavorassigner.PodSetAssignment{{
				Name: kueue.DefaultPodSetName,
				Status: *flavorassigner.NewStatus(
					podFailure(kueue.DefaultPodSetName, 1, errInsufficientCPU.Error()),
				).WithError(errInsufficientCPU).WithNoFitReason(kueue.WorkloadQuotaReservedReasonSimulationFailed),
				FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{
					{Flavor: testNoFitFlavor, Mode: flavorassigner.NoFit, NoFitReason: kueue.WorkloadQuotaReservedReasonExceedsMaxQuota},
					{Flavor: testFlavor, Mode: flavorassigner.NoFit, NoFitReason: kueue.WorkloadQuotaReservedReasonSimulationFailed},
				},
			}},
		},
		"a pod fails to be scheduled: flavor attempts are not marked": {
			flavorQuotas: []kueue.FlavorQuotas{cpuQuota(testFlavor, "20")},
			podSets:      []testPodSet{{name: kueue.DefaultPodSetName, count: 2, cpu: "1"}},
			initialMode:  flavorassigner.Fit,
			schedulingResult: &simulator.SchedulingResult{PodPlacements: simulator.PodPlacements{
				podKey(kueue.DefaultPodSetName, 0): {Node: testNode1},
				podKey(kueue.DefaultPodSetName, 1): {Error: errInsufficientCPU, Reasons: []string{errInsufficientCPU.Error()}},
			}},
			wantMode:        flavorassigner.NoFit,
			wantNoFitReason: kueue.WorkloadQuotaReservedReasonSimulationFailed,
			wantPodSets: []flavorassigner.PodSetAssignment{{
				Name: kueue.DefaultPodSetName,
				Status: *flavorassigner.NewStatus(
					podFailure(kueue.DefaultPodSetName, 1, errInsufficientCPU.Error()),
				).WithError(errInsufficientCPU).WithNoFitReason(kueue.WorkloadQuotaReservedReasonSimulationFailed),
				FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{{Flavor: testFlavor, Mode: flavorassigner.Fit}},
			}},
		},
	}

	for name, tc := range cases {
		observability := tc.wantResolvedNoFitReason != ""
		if !observability {
			name = "UnadmittedWorkloadsObservability disabled, " + name
		}
		t.Run(name, func(t *testing.T) {
			// Given
			features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
				features.TopologyAwareScheduling:          true,
				features.SchedulerLibraryDeepIntegration:  true,
				features.UnadmittedWorkloadsObservability: observability,
			})

			log := testr.New(t)
			ctx := ctrllog.IntoContext(t.Context(), log)
			snapshot := newTestSnapshot(ctx, t, log, tc.flavorQuotas...)
			testSim := &testSimulator{SchedulerSimulator: snapshot.SchedulerSimulator}
			if tc.schedulingResult != nil {
				testSim.results = []simulator.SchedulingResult{*tc.schedulingResult}
				if tc.emptyClusterResult != nil {
					testSim.results = append(testSim.results, *tc.emptyClusterResult)
				}
			}
			snapshot.SchedulerSimulator = testSim

			var podSets []kueue.PodSet
			for _, ps := range tc.podSets {
				requiredLevel := ps.requiredLevel
				if requiredLevel == "" {
					requiredLevel = corev1.LabelHostname
				}
				podSets = append(podSets, *utiltestingapi.MakePodSet(ps.name, ps.count).
					RequiredTopologyRequest(requiredLevel).
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
			planner := NewPlanner(wl, snapshot, preemptor)
			gotPlan := planner.Plan(ctx, &initialAssignment)

			// Then
			if gotPlan.Assignment != &initialAssignment {
				t.Errorf("Plan() returned a different assignment object than the initial one")
			}
			wantCalls := len(testSim.results)
			if testSim.calls != wantCalls {
				t.Errorf("Plan() called ScheduleWorkload unexpected number of times: want %d, got %d", wantCalls, testSim.calls)
			}
			if tc.wantCandidates != nil {
				if diff := cmp.Diff(tc.wantCandidates, testSim.gotCandidates); diff != "" {
					t.Errorf("Plan() passed unexpected preemption candidates (-want +got):\n%s", diff)
				}
			}
			if tc.wantSimulateEmpty != nil {
				if diff := cmp.Diff(tc.wantSimulateEmpty, testSim.gotSimulateEmpty); diff != "" {
					t.Errorf("Plan() passed unexpected SimulateEmpty options (-want +got):\n%s", diff)
				}
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
			var gotTargets []client.ObjectKey
			for _, target := range gotPlan.PreemptionTargets {
				gotTargets = append(gotTargets, client.ObjectKeyFromObject(target.WorkloadInfo.Obj))
			}
			if diff := cmp.Diff(tc.wantPreemptionTargets, gotTargets); diff != "" {
				t.Errorf("Plan() preemption targets mismatch (-want +got):\n%s", diff)
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

			if observability {
				gotPlan.Assignment.ResolveNoFitReason(snapshot.ClusterQueue(testCQ))
				if gotPlan.Assignment.NoFitReason != tc.wantResolvedNoFitReason {
					t.Errorf("ResolveNoFitReason() after Plan() NoFitReason mismatch: want %q, got %q", tc.wantResolvedNoFitReason, gotPlan.Assignment.NoFitReason)
				}
			}
		})
	}
}

func TestPlanVirtualPodGroupsHierarchy(t *testing.T) {
	features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
		features.TopologyAwareScheduling:         true,
		features.SchedulerLibraryDeepIntegration: true,
	})

	log := testr.New(t)
	ctx := ctrllog.IntoContext(t.Context(), log)
	snapshot := newTestSnapshot(ctx, t, log, cpuQuota(testFlavor, "20"))
	testSim := &testSimulator{SchedulerSimulator: snapshot.SchedulerSimulator}
	snapshot.SchedulerSimulator = testSim

	podSetGroup := "group1"
	wlObj := utiltestingapi.MakeWorkload(incomingName, testNamespace).
		PodSets(
			*utiltestingapi.MakePodSet("ps1", 2).
				PodSetGroup(podSetGroup).
				RequiredTopologyRequest(corev1.LabelHostname).
				Request(corev1.ResourceCPU, "1").
				Obj(),
			*utiltestingapi.MakePodSet("ps2", 2).
				PodSetGroup(podSetGroup).
				RequiredTopologyRequest(corev1.LabelHostname).
				Request(corev1.ResourceCPU, "1").
				Obj(),
		).
		Obj()
	wl := workload.NewInfo(log, wlObj)
	wl.ClusterQueue = testCQ

	assigner := flavorassigner.New(
		wl, snapshot.ClusterQueue(testCQ), snapshot.ResourceFlavors, false,
		nil, nil,
		configapi.QuotaCheckBlockUndeclared, resources.NewResourceFormatter(), 0,
	)
	assignment := assigner.AssignFlavors(ctx, log, nil)

	planner := NewPlanner(wl, snapshot, nil)
	plan := planner.Plan(ctx, &assignment)
	if plan.Error != nil {
		t.Fatalf("Plan() unexpected error: %v", plan.Error)
	}

	// Verify CompositePodGroup was added and cleaned up
	if len(testSim.addedCPGs) != 1 {
		t.Errorf("expected 1 CompositePodGroup added, got %d", len(testSim.addedCPGs))
	}
	if len(testSim.removedCPGs) != 1 {
		t.Errorf("expected 1 CompositePodGroup removed, got %d", len(testSim.removedCPGs))
	}

	// Verify PodGroups were added and cleaned up
	if len(testSim.addedPGs) != 2 {
		t.Errorf("expected 2 PodGroups added, got %d", len(testSim.addedPGs))
	}
	if len(testSim.removedPGs) != 2 {
		t.Errorf("expected 2 PodGroups removed, got %d", len(testSim.removedPGs))
	}

	// Verify virtual pods were scheduled with leaf PodGroup references
	if len(testSim.scheduledPods) != 4 {
		t.Fatalf("expected 4 scheduled pods, got %d", len(testSim.scheduledPods))
	}
	for _, pod := range testSim.scheduledPods {
		if pod.Spec.SchedulingGroup == nil || pod.Spec.SchedulingGroup.PodGroupName == nil {
			t.Errorf("pod %s has no SchedulingGroup.PodGroupName", pod.Name)
		}
	}
}

func TestPlanPreemptionStrategies(t *testing.T) {
	errInsufficientCPU := errors.New("Insufficient cpu")

	makeAdmittedWorkload := func(name string, cq kueue.ClusterQueueReference, prio int32, cpu string, node string, admitTime time.Time) *kueue.Workload {
		return utiltestingapi.MakeWorkload(name, testNamespace).
			Priority(prio).
			PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
				RequiredTopologyRequest(corev1.LabelHostname).
				Request(corev1.ResourceCPU, cpu).
				Obj()).
			ReserveQuotaAt(utiltestingapi.MakeAdmission(cq).
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(corev1.ResourceCPU, testFlavor, cpu).
					TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
						Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{node}, 1).Obj()).
						Obj()).
					Obj()).
				Obj(), admitTime).
			Obj()
	}

	now := time.Now()
	cases := map[string]struct {
		enableFairSharing     bool
		cqs                   []*kueue.ClusterQueue
		admittedWorkloads     []*kueue.Workload
		incomingCPU           string
		simResults            []simulator.SchedulingResult
		wantCalls             int
		wantCandidates        [][]client.ObjectKey
		wantPreemptionTargets []client.ObjectKey
		wantMode              flavorassigner.FlavorAssignmentMode
	}{
		"classical preemption with BorrowWithinCohort: tries borrowing strategy first, falls back to non-borrowing strategy": {
			enableFairSharing: false,
			cqs: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue(testCQ).
					Cohort("cohort").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
						BorrowWithinCohort: &kueue.BorrowWithinCohort{
							Policy:               kueue.BorrowWithinCohortPolicyLowerPriority,
							MaxPriorityThreshold: new(int32(30)),
						},
					}).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas(testFlavor).
						Resource(corev1.ResourceCPU, "4", "4").
						Obj()).
					Obj(),
				utiltestingapi.MakeClusterQueue("borrower-cq").
					Cohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas(testFlavor).
						Resource(corev1.ResourceCPU, "0", "4").
						Obj()).
					Obj(),
				utiltestingapi.MakeClusterQueue("lender-cq").
					Cohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas(testFlavor).
						Resource(corev1.ResourceCPU, "2").
						Obj()).
					Obj(),
			},
			admittedWorkloads: []*kueue.Workload{
				makeAdmittedWorkload("victim-low", testCQ, 10, "2", testNode1, now),
				makeAdmittedWorkload("victim-cohort", "borrower-cq", 40, "2", testNode2, now),
			},
			incomingCPU: "4",
			simResults: []simulator.SchedulingResult{
				{
					PodPlacements: simulator.PodPlacements{
						podKey(kueue.DefaultPodSetName, 0): {Error: errInsufficientCPU, Reasons: []string{errInsufficientCPU.Error()}},
					},
				},
				{
					PodPlacements: simulator.PodPlacements{
						podKey(kueue.DefaultPodSetName, 0): {Node: testNode1},
					},
					PreemptedWorkloads: []client.ObjectKey{
						{Namespace: testNamespace, Name: "victim-cohort"},
						{Namespace: testNamespace, Name: "victim-low"},
					},
				},
			},
			wantCalls: 2,
			wantCandidates: [][]client.ObjectKey{
				{
					{Namespace: testNamespace, Name: "victim-low"},
				},
				{
					{Namespace: testNamespace, Name: "victim-cohort"},
					{Namespace: testNamespace, Name: "victim-low"},
				},
			},
			wantPreemptionTargets: []client.ObjectKey{
				{Namespace: testNamespace, Name: "victim-cohort"},
				{Namespace: testNamespace, Name: "victim-low"},
			},
			wantMode: flavorassigner.Preempt,
		},
		"fair sharing preemption: lazily evaluates cohort candidates and stops once workload fits": {
			enableFairSharing: true,
			cqs: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue(testCQ).
					Cohort("cohort").
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas(testFlavor).
						Resource(corev1.ResourceCPU, "4").
						Obj()).
					Obj(),
				utiltestingapi.MakeClusterQueue("borrower-cq").
					Cohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas(testFlavor).
						Resource(corev1.ResourceCPU, "0", "4").
						Obj()).
					Obj(),
			},
			admittedWorkloads: []*kueue.Workload{
				makeAdmittedWorkload("borrower-low", "borrower-cq", 10, "2", testNode1, now),
				makeAdmittedWorkload("borrower-mid", "borrower-cq", 20, "2", testNode2, now),
			},
			incomingCPU: "2",
			simResults: []simulator.SchedulingResult{
				{
					PodPlacements: simulator.PodPlacements{
						podKey(kueue.DefaultPodSetName, 0): {Node: testNode1},
					},
					PreemptedWorkloads: []client.ObjectKey{
						{Namespace: testNamespace, Name: "borrower-low"},
					},
				},
			},
			wantCalls: 1,
			wantCandidates: [][]client.ObjectKey{
				{
					{Namespace: testNamespace, Name: "borrower-low"},
				},
			},
			wantPreemptionTargets: []client.ObjectKey{
				{Namespace: testNamespace, Name: "borrower-low"},
			},
			wantMode: flavorassigner.Preempt,
		},
		"node-level victims do not free enough quota: selects additional strategy candidates to satisfy quota": {
			enableFairSharing: true,
			cqs: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue(testCQ).
					Cohort("cohort").
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas(testFlavor).
						Resource(corev1.ResourceCPU, "4").
						Obj()).
					Obj(),
				utiltestingapi.MakeClusterQueue("borrower-cq").
					Cohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas(testFlavor).
						Resource(corev1.ResourceCPU, "0", "4").
						Obj()).
					Obj(),
			},
			admittedWorkloads: []*kueue.Workload{
				makeAdmittedWorkload("borrower-low", "borrower-cq", 10, "2", testNode1, now),
				makeAdmittedWorkload("borrower-mid", "borrower-cq", 20, "2", testNode2, now),
			},
			incomingCPU: "4",
			simResults: []simulator.SchedulingResult{
				{
					PodPlacements: simulator.PodPlacements{
						podKey(kueue.DefaultPodSetName, 0): {Node: testNode1},
					},
					PreemptedWorkloads: []client.ObjectKey{
						{Namespace: testNamespace, Name: "borrower-low"},
					},
				},
			},
			wantCalls: 1,
			wantCandidates: [][]client.ObjectKey{
				{
					{Namespace: testNamespace, Name: "borrower-low"},
					{Namespace: testNamespace, Name: "borrower-mid"},
				},
			},
			wantPreemptionTargets: []client.ObjectKey{
				{Namespace: testNamespace, Name: "borrower-low"},
				{Namespace: testNamespace, Name: "borrower-mid"},
			},
			wantMode: flavorassigner.Preempt,
		},
		"fair sharing preemption: keeps Preempt mode without targets when reprieval causes strategy verification to fail": {
			enableFairSharing: true,
			cqs: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue(testCQ).
					Cohort("cohort").
					FairWeight(resource.MustParse("2")).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas(testFlavor).
						Resource(corev1.ResourceCPU, "3").
						Obj()).
					Obj(),
				utiltestingapi.MakeClusterQueue("borrower-cq").
					Cohort("cohort").
					FairWeight(resource.MustParse("1")).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas(testFlavor).
						Resource(corev1.ResourceCPU, "3").
						Obj()).
					Obj(),
			},
			admittedWorkloads: []*kueue.Workload{
				makeAdmittedWorkload("p-small", testCQ, 10, "2", testNode1, now),
				makeAdmittedWorkload("t-hero", "borrower-cq", 10, "4", testNode2, now),
			},
			incomingCPU: "4",
			simResults: []simulator.SchedulingResult{
				{
					PodPlacements: simulator.PodPlacements{
						podKey(kueue.DefaultPodSetName, 0): {Node: testNode2},
					},
					// p-small was reprieved during simulation because 2 + 4 = 6 <= 6 cohort quota,
					// leaving only t-hero preempted, which fails fair-sharing verification.
					PreemptedWorkloads: []client.ObjectKey{
						{Namespace: testNamespace, Name: "t-hero"},
					},
				},
			},
			wantCalls: 2,
			wantCandidates: [][]client.ObjectKey{
				{
					{Namespace: testNamespace, Name: "p-small"},
					{Namespace: testNamespace, Name: "t-hero"},
				},
				nil,
			},
			wantPreemptionTargets: nil,
			wantMode:              flavorassigner.Preempt,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
				features.TopologyAwareScheduling:         true,
				features.SchedulerLibraryDeepIntegration: true,
			})

			log := testr.New(t)
			ctx := ctrllog.IntoContext(t.Context(), log)

			cache := schdcache.New(utiltesting.NewFakeClient())
			cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor(testFlavor).
				NodeLabel("tas", testFlavor).TopologyName("topology").Obj())
			cache.AddOrUpdateTopology(log, utiltestingapi.MakeTopology("topology").Levels(testBlockLabel, corev1.LabelHostname).Obj())
			for _, node := range []string{testNode1, testNode2} {
				cache.TASCache().SyncNode(testingnode.MakeNode(node).
					Label("tas", testFlavor).
					Label(testBlockLabel, testBlock).
					Label(corev1.LabelHostname, node).
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse(testNodeCPUs),
						corev1.ResourcePods: resource.MustParse("32"),
					}).
					Ready().
					Obj())
			}
			for _, cq := range tc.cqs {
				if err := cache.AddClusterQueue(ctx, cq); err != nil {
					t.Fatalf("adding ClusterQueue %q: %v", cq.Name, err)
				}
			}
			for _, w := range tc.admittedWorkloads {
				if !cache.AddOrUpdateWorkload(ctx, log, w) {
					t.Fatalf("adding workload %q to cache", w.Name)
				}
			}

			snapshot, err := cache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("building snapshot: %v", err)
			}

			testSim := &testSimulator{
				SchedulerSimulator: snapshot.SchedulerSimulator,
				results:            tc.simResults,
			}
			snapshot.SchedulerSimulator = testSim

			wl := workload.NewInfo(log, utiltestingapi.MakeWorkload(incomingName, testNamespace).
				Priority(incomingPriority).
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					RequiredTopologyRequest(corev1.LabelHostname).
					Request(corev1.ResourceCPU, tc.incomingCPU).
					Obj()).
				Obj())
			wl.ClusterQueue = testCQ

			var fsConfig *configapi.FairSharing
			if tc.enableFairSharing {
				fsConfig = &configapi.FairSharing{}
			}
			preemptor := preemption.New(
				utiltesting.NewFakeClient(), workload.Ordering{}, &utiltesting.EventRecorder{}, fsConfig, false,
				clocktesting.NewFakeClock(now), nil, preemptexpectations.New(), nil,
			)
			assigner := flavorassigner.New(
				wl, snapshot.ClusterQueue(testCQ), snapshot.ResourceFlavors, tc.enableFairSharing,
				preemption.NewOracle(preemptor, snapshot), nil,
				configapi.QuotaCheckBlockUndeclared, resources.NewResourceFormatter(), 0,
			)
			initialAssignment := assigner.AssignFlavors(ctx, log, nil)
			if gotMode := initialAssignment.RepresentativeMode(); gotMode != flavorassigner.Preempt {
				t.Fatalf("AssignFlavors() mode = %v, want %v", gotMode, flavorassigner.Preempt)
			}
			freeCapacityBefore := freeCapacity(t, snapshot)

			planner := NewPlanner(wl, snapshot, preemptor)
			gotPlan := planner.Plan(ctx, &initialAssignment)

			if gotPlan.Error != nil {
				t.Fatalf("Plan() unexpected error: %v", gotPlan.Error)
			}
			if testSim.calls != tc.wantCalls {
				t.Errorf("Plan() ScheduleWorkload calls = %d, want %d", testSim.calls, tc.wantCalls)
			}
			if diff := cmp.Diff(tc.wantCandidates, testSim.gotCandidates); diff != "" {
				t.Errorf("Plan() preemption candidates mismatch (-want +got):\n%s", diff)
			}
			var gotTargets []client.ObjectKey
			for _, target := range gotPlan.PreemptionTargets {
				gotTargets = append(gotTargets, client.ObjectKeyFromObject(target.WorkloadInfo.Obj))
			}
			if diff := cmp.Diff(tc.wantPreemptionTargets, gotTargets); diff != "" {
				t.Errorf("Plan() preemption targets mismatch (-want +got):\n%s", diff)
			}
			if gotMode := gotPlan.Assignment.RepresentativeMode(); gotMode != tc.wantMode {
				t.Errorf("Plan() RepresentativeMode() = %v, want %v", gotMode, tc.wantMode)
			}
			if freeCapacityAfter := freeCapacity(t, snapshot); freeCapacityAfter != freeCapacityBefore {
				t.Errorf("Plan() modified snapshot TAS free capacity: before %s, after %s", freeCapacityBefore, freeCapacityAfter)
			}
		})
	}
}

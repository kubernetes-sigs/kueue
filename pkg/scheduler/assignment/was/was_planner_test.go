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
	"iter"
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
	schedulingv1alpha3 "k8s.io/api/scheduling/v1alpha3"
	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/component-base/featuregate"
	"k8s.io/klog/v2"
	kubefeatures "k8s.io/kubernetes/pkg/features"
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
	"sigs.k8s.io/kueue/pkg/scheduler/assignment"
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

var errSimulator = errors.New("simulator failure")

type scheduleWorkloadCall struct {
	consumedCandidates []workload.Reference
	committedVictims   []workload.Reference
	simulateEmpty      bool
}

// simulationStep scripts a single ScheduleWorkload call in testSimulator.
type simulationStep struct {
	podPlacements simulator.PodPlacements
	// nodeVictims are the workloads whose pods must be evicted from nodes for
	// podPlacements to succeed. When a PreemptionFilter is passed, testSimulator
	// may also preempt additional candidates to satisfy MayFit() and reprieve
	// non-node victims in reverse order, producing SchedulingResult.PreemptionVictims.
	nodeVictims []workload.Reference
	err         error
}

func (s simulationStep) allPodsPlaced() bool {
	if s.err != nil || len(s.podPlacements) == 0 {
		return false
	}
	for _, p := range s.podPlacements {
		if !p.IsSuccess() {
			return false
		}
	}
	return true
}

func stepFromSchedulingResult(res simulator.SchedulingResult) simulationStep {
	return simulationStep{
		podPlacements: res.PodPlacements,
		nodeVictims:   res.PreemptionVictims,
		err:           res.Error,
	}
}

type testSimulator struct {
	simulator.SchedulerSimulator

	steps                     []simulationStep
	receivedCalls             []scheduleWorkloadCall
	scheduledPods             []*corev1.Pod
	addedPGs                  []*schedulingv1beta1.PodGroup
	removedPGs                []*schedulingv1beta1.PodGroup
	addedCPGs                 []*schedulingv1alpha3.CompositePodGroup
	removedCPGs               []*schedulingv1alpha3.CompositePodGroup
	failAddPGAt, failAddCPGAt int
	addPGCalls, addCPGCalls   int
	removeErr                 error
}

var _ simulator.SchedulerSimulator = (*testSimulator)(nil)

func (f *testSimulator) nextStep() simulationStep {
	idx := len(f.receivedCalls)
	switch {
	case idx < len(f.steps):
		return f.steps[idx]
	case len(f.steps) > 0:
		return f.steps[len(f.steps)-1]
	default:
		return simulationStep{}
	}
}

func (f *testSimulator) ScheduleWorkload(_ context.Context, pods []*corev1.Pod, opts ...simulator.ScheduleOption) simulator.SchedulingResult {
	f.scheduledPods = append(f.scheduledPods, pods...)
	step := f.nextStep()
	scheduleOpts := simulator.BuildScheduleOptions(opts...)

	victims := slices.Clone(step.nodeVictims)
	if scheduleOpts.PreemptionFilter != nil {
		for wlKey, wlInfo := range scheduleOpts.CommittedPreemptionVictims {
			scheduleOpts.PreemptionFilter.PreemptWorkload(wlKey, wlInfo)
		}
	}
	var consumedCandidates []workload.Reference
	if scheduleOpts.PreemptionCandidates != nil {
		allPlaced := step.allPodsPlaced()
		consumed, nodeVictimsSeen := consumePreemptionCandidates(
			scheduleOpts.PreemptionCandidates,
			scheduleOpts.PreemptionFilter,
			step.nodeVictims,
			allPlaced,
		)
		consumedCandidates = make([]workload.Reference, len(consumed))
		for i, c := range consumed {
			consumedCandidates[i] = c.key
		}
		if allPlaced && nodeVictimsSeen && scheduleOpts.PreemptionFilter != nil {
			victims = reprievePreemptionCandidates(consumed, step.nodeVictims, scheduleOpts.PreemptionFilter)
		}
	}

	var committedVictims []workload.Reference
	if len(scheduleOpts.CommittedPreemptionVictims) > 0 {
		committedVictims = slices.Sorted(maps.Keys(scheduleOpts.CommittedPreemptionVictims))
	}
	f.receivedCalls = append(f.receivedCalls, scheduleWorkloadCall{
		consumedCandidates: consumedCandidates,
		committedVictims:   committedVictims,
		simulateEmpty:      scheduleOpts.SimulateEmpty,
	})
	return simulator.SchedulingResult{
		PodPlacements:     step.podPlacements,
		PreemptionVictims: victims,
		Error:             step.err,
	}
}

type candidateEntry struct {
	key  workload.Reference
	info *workload.Info
}

// consumePreemptionCandidates emulates scheduler-library's lazy prefix search over
// PreemptionCandidates: candidates are pulled one by one (populating PreemptionStrategy's
// internal state and removing each candidate from the quota snapshot via PreemptionFilter)
// until both node-level placement (nodeVictims) and quota (pf.MayFit()) are satisfied.
func consumePreemptionCandidates(
	candidates iter.Seq2[workload.Reference, *workload.Info],
	pf simulator.PreemptionFilter,
	nodeVictims []workload.Reference,
	allPlaced bool,
) ([]candidateEntry, bool) {
	var consumed []candidateEntry
	seen := sets.New[workload.Reference]()
	for wlKey, wlInfo := range candidates {
		consumed = append(consumed, candidateEntry{key: wlKey, info: wlInfo})
		seen.Insert(wlKey)
		if pf != nil {
			pf.PreemptWorkload(wlKey, wlInfo)
		}
		if allPlaced && seen.HasAll(nodeVictims...) && (pf == nil || pf.MayFit()) {
			return consumed, true
		}
	}
	return consumed, seen.HasAll(nodeVictims...)
}

// reprievePreemptionCandidates emulates scheduler-library's post-placement reprieval pass:
// iterating backward over consumed candidates, any candidate needed for node placement stays
// preempted, while quota-only candidates are unpreempted and kept reprieved if pf.MayFit()
// still holds without them.
func reprievePreemptionCandidates(
	consumedCandidates []candidateEntry,
	nodeVictims []workload.Reference,
	pf simulator.PreemptionFilter,
) []workload.Reference {
	requiredOnNodes := sets.New(nodeVictims...)
	var finalPreempted []workload.Reference
	for _, c := range slices.Backward(consumedCandidates) {
		if requiredOnNodes.Has(c.key) {
			finalPreempted = append(finalPreempted, c.key)
			continue
		}
		pf.UnpreemptWorkload(c.key)
		if !pf.MayFit() {
			pf.PreemptWorkload(c.key, c.info)
			finalPreempted = append(finalPreempted, c.key)
		}
	}
	slices.Reverse(finalPreempted)
	return finalPreempted
}

func (f *testSimulator) AddPodGroup(ctx context.Context, pg *schedulingv1beta1.PodGroup) error {
	f.addPGCalls++
	if f.addPGCalls == f.failAddPGAt {
		return errSimulator
	}
	if f.SchedulerSimulator != nil {
		if err := f.SchedulerSimulator.AddPodGroup(ctx, pg); err != nil {
			return err
		}
	}
	f.addedPGs = append(f.addedPGs, pg)
	return nil
}

func (f *testSimulator) RemovePodGroup(ctx context.Context, pg *schedulingv1beta1.PodGroup) error {
	f.removedPGs = append(f.removedPGs, pg)
	if f.removeErr != nil {
		return f.removeErr
	}
	if f.SchedulerSimulator != nil {
		return f.SchedulerSimulator.RemovePodGroup(ctx, pg)
	}
	return nil
}

func (f *testSimulator) AddCompositePodGroup(ctx context.Context, cpg *schedulingv1alpha3.CompositePodGroup) error {
	f.addCPGCalls++
	if f.addCPGCalls == f.failAddCPGAt {
		return errSimulator
	}
	if f.SchedulerSimulator != nil {
		if err := f.SchedulerSimulator.AddCompositePodGroup(ctx, cpg); err != nil {
			return err
		}
	}
	f.addedCPGs = append(f.addedCPGs, cpg)
	return nil
}

func (f *testSimulator) RemoveCompositePodGroup(ctx context.Context, cpg *schedulingv1alpha3.CompositePodGroup) error {
	f.removedCPGs = append(f.removedCPGs, cpg)
	if f.removeErr != nil {
		return f.removeErr
	}
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
	podSetGroup   string
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

	type addedPodGroups struct{ cpgs, pgs int }
	groupedPodSets := []testPodSet{
		{name: "psA1", count: 2, cpu: "1", podSetGroup: "group1"},
		{name: "psA2", count: 2, cpu: "1", podSetGroup: "group1"},
		{name: "psB", count: 1, cpu: "1"},
	}
	groupedPodSetsUnchanged := []flavorassigner.PodSetAssignment{
		{Name: "psA1", FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{{Flavor: testFlavor, Mode: flavorassigner.Fit}}},
		{Name: "psA2", FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{{Flavor: testFlavor, Mode: flavorassigner.Fit}}},
		{Name: "psB", FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{{Flavor: testFlavor, Mode: flavorassigner.Fit}}},
	}

	cases := map[string]struct {
		// flavorQuotas are the ClusterQueue's flavors, in order. See newTestSnapshot.
		flavorQuotas     []kueue.FlavorQuotas
		podSets          []testPodSet
		unhealthyNodes   []string
		committedVictims []workload.Reference
		// initialMode documents the quota-based mode computed by the flavor assigner.
		initialMode flavorassigner.FlavorAssignmentMode
		// When set as Nil, ScheduleWorkload is expected not to be called.
		// PreemptionVictims specifies node-level victims required to place pods;
		// when PreemptionFilter is passed, testSimulator also computes quota victims.
		schedulingResult *simulator.SchedulingResult
		// preemptionResult, when set, scripts the node-level outcome of the preemption
		// ScheduleWorkload call after initial Fit simulation fails.
		preemptionResult *simulator.SchedulingResult
		// emptyClusterResult, when set, is the result returned by the ScheduleWorkload call
		// with WithSimulateEmpty(true) after preemption candidate simulation fails.
		emptyClusterResult *simulator.SchedulingResult
		// failAddCPGAt and failAddPGAt make the N-th Add call fail, counting from 1. 0 means no failure.
		failAddCPGAt, failAddPGAt int
		removeErr                 error
		omitPreemptionFilter      bool

		wantMode        flavorassigner.FlavorAssignmentMode
		wantFit         bool
		wantErrs        []string
		wantNoFitReason string
		// wantCalls, when set, checks the ScheduleWorkload calls made by Plan.
		wantCalls []scheduleWorkloadCall
		// wantPreemptionTargets checks the workload keys of the PreemptionTargets returned in Plan.
		wantPreemptionTargets []workload.Reference
		// wantResolvedNoFitReason configures what should be the NoFitReason
		// the Assignment resolves to when the UnadmittedWorkloadsObservability
		// FG ius enabled.
		// When set the UnadmittedWorkloadsObservability feature will be enabled in the test.
		wantResolvedNoFitReason string
		wantPodSets             []flavorassigner.PodSetAssignment
		wantAdded               *addedPodGroups
	}{
		"PreemptionFilter option not configured: planning error": {
			flavorQuotas:         []kueue.FlavorQuotas{cpuQuota(testFlavor, "20")},
			podSets:              []testPodSet{{name: kueue.DefaultPodSetName, count: 1, cpu: "1"}},
			initialMode:          flavorassigner.Fit,
			omitPreemptionFilter: true,
			wantMode:             flavorassigner.Fit,
			wantErrs:             []string{"PreemptionFilter option not configured for wasPlanner"},
			wantPodSets: []flavorassigner.PodSetAssignment{{
				Name:                     kueue.DefaultPodSetName,
				FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{{Flavor: testFlavor, Mode: flavorassigner.Fit}},
			}},
		},
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
				PreemptionVictims: []workload.Reference{workload.NewReference(testNamespace, victimName)},
			},
			wantMode: flavorassigner.Preempt,
			wantFit:  true,
			wantCalls: []scheduleWorkloadCall{
				{consumedCandidates: []workload.Reference{workload.NewReference(testNamespace, victimName)}},
			},
			wantPreemptionTargets: []workload.Reference{workload.NewReference(testNamespace, victimName)},
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
		"initial assignment requires preemption and ScheduleWorkload returns unknown victim: planning error": {
			flavorQuotas: []kueue.FlavorQuotas{cpuQuota(testFlavor, "3")},
			podSets:      []testPodSet{{name: kueue.DefaultPodSetName, count: 1, cpu: "2"}},
			initialMode:  flavorassigner.Preempt,
			schedulingResult: &simulator.SchedulingResult{
				PodPlacements: simulator.PodPlacements{
					podKey(kueue.DefaultPodSetName, 0): {Node: testNode1},
				},
				PreemptionVictims: []workload.Reference{workload.NewReference(testNamespace, "unknown-victim")},
			},
			wantMode: flavorassigner.Preempt,
			wantErrs: []string{`preempted workload "default/unknown-victim" is not a candidate in this preemption strategy`},
			wantCalls: []scheduleWorkloadCall{
				{consumedCandidates: []workload.Reference{workload.NewReference(testNamespace, victimName)}},
			},
			wantPodSets: []flavorassigner.PodSetAssignment{{
				Name:   kueue.DefaultPodSetName,
				Status: *flavorassigner.NewStatus("insufficient unused quota for cpu in flavor tas-flavor, 1 more needed"),
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
			wantMode: flavorassigner.Preempt,
			wantFit:  true,
			wantCalls: []scheduleWorkloadCall{
				{consumedCandidates: []workload.Reference{workload.NewReference(testNamespace, victimName)}},
			},
			wantPreemptionTargets: []workload.Reference{workload.NewReference(testNamespace, victimName)},
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
			wantMode: flavorassigner.Preempt,
			wantFit:  false,
			wantCalls: []scheduleWorkloadCall{
				{consumedCandidates: []workload.Reference{workload.NewReference(testNamespace, victimName)}},
				{simulateEmpty: true},
			},
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
			wantCalls: []scheduleWorkloadCall{
				{consumedCandidates: []workload.Reference{workload.NewReference(testNamespace, victimName)}},
				{simulateEmpty: true},
			},
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
		"initial assignment fits quota, ScheduleWorkload succeeds with committed victims: demotes to DeferredFit and fits": {
			flavorQuotas:     []kueue.FlavorQuotas{cpuQuota(testFlavor, "20")},
			podSets:          []testPodSet{{name: kueue.DefaultPodSetName, count: 1, cpu: "2"}},
			committedVictims: []workload.Reference{workload.NewReference(testNamespace, victimName)},
			initialMode:      flavorassigner.Fit,
			schedulingResult: &simulator.SchedulingResult{
				PodPlacements: simulator.PodPlacements{
					podKey(kueue.DefaultPodSetName, 0): {Node: testNode1},
				},
				PreemptionVictims: []workload.Reference{workload.NewReference(testNamespace, victimName)},
			},
			wantMode: flavorassigner.DeferredFit,
			wantFit:  false,
			wantCalls: []scheduleWorkloadCall{
				{committedVictims: []workload.Reference{workload.NewReference(testNamespace, victimName)}},
			},
			wantPodSets: []flavorassigner.PodSetAssignment{{
				Name:               kueue.DefaultPodSetName,
				TopologyAssignment: onNodes(domain(testNode1, 1)),
				FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{{
					Flavor: testFlavor,
					Mode:   flavorassigner.Fit,
				}},
			}},
		},
		"initial assignment fits quota, committed victims forwarded to the simulator but not needed for fitting: stays Fit": {
			flavorQuotas:     []kueue.FlavorQuotas{cpuQuota(testFlavor, "20")},
			podSets:          []testPodSet{{name: kueue.DefaultPodSetName, count: 1, cpu: "2"}},
			committedVictims: []workload.Reference{workload.NewReference(testNamespace, victimName)},
			initialMode:      flavorassigner.Fit,
			schedulingResult: &simulator.SchedulingResult{
				PodPlacements: simulator.PodPlacements{
					podKey(kueue.DefaultPodSetName, 0): {Node: testNode2},
				},
			},
			wantMode: flavorassigner.Fit,
			wantFit:  true,
			wantCalls: []scheduleWorkloadCall{
				{committedVictims: []workload.Reference{workload.NewReference(testNamespace, victimName)}},
			},
			wantPodSets: []flavorassigner.PodSetAssignment{{
				Name:               kueue.DefaultPodSetName,
				TopologyAssignment: onNodes(domain(testNode2, 1)),
				FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{{
					Flavor: testFlavor,
					Mode:   flavorassigner.Fit,
				}},
			}},
		},
		"committed victims forwarded to preemption strategy, stripped from targets, and omitted from empty-cluster fallback": {
			flavorQuotas:     []kueue.FlavorQuotas{cpuQuota(testFlavor, "20")},
			podSets:          []testPodSet{{name: kueue.DefaultPodSetName, count: 1, cpu: "2"}},
			committedVictims: []workload.Reference{workload.NewReference(testNamespace, "committed-other")},
			initialMode:      flavorassigner.Fit,
			schedulingResult: &simulator.SchedulingResult{PodPlacements: simulator.PodPlacements{
				podKey(kueue.DefaultPodSetName, 0): {Error: errInsufficientCPU, Reasons: []string{errInsufficientCPU.Error()}},
			}},
			preemptionResult: &simulator.SchedulingResult{PodPlacements: simulator.PodPlacements{
				podKey(kueue.DefaultPodSetName, 0): {Error: errInsufficientCPU, Reasons: []string{errInsufficientCPU.Error()}},
			}},
			emptyClusterResult: &simulator.SchedulingResult{PodPlacements: simulator.PodPlacements{
				podKey(kueue.DefaultPodSetName, 0): {Node: testNode1},
			}},
			wantMode: flavorassigner.Preempt,
			wantFit:  false,
			wantCalls: []scheduleWorkloadCall{
				{committedVictims: []workload.Reference{workload.NewReference(testNamespace, "committed-other")}},
				{
					consumedCandidates: []workload.Reference{workload.NewReference(testNamespace, victimName)},
					committedVictims:   []workload.Reference{workload.NewReference(testNamespace, "committed-other")},
				},
				{simulateEmpty: true},
			},
			wantPodSets: []flavorassigner.PodSetAssignment{{
				Name:               kueue.DefaultPodSetName,
				Status:             *flavorassigner.NewStatus(podFailure(kueue.DefaultPodSetName, 0, errInsufficientCPU.Error())),
				TopologyAssignment: onNodes(domain(testNode1, 1)),
				FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{{
					Flavor: testFlavor,
					Mode:   flavorassigner.Fit,
				}},
			}},
		},
		"workload with unhealthy nodes fits via committed victim: demotes to DeferredFit without preemption": {
			flavorQuotas:     []kueue.FlavorQuotas{cpuQuota(testFlavor, "20")},
			podSets:          []testPodSet{{name: kueue.DefaultPodSetName, count: 1, cpu: "2"}},
			unhealthyNodes:   []string{testNode1},
			committedVictims: []workload.Reference{workload.NewReference(testNamespace, victimName)},
			initialMode:      flavorassigner.Fit,
			schedulingResult: &simulator.SchedulingResult{
				PodPlacements: simulator.PodPlacements{
					podKey(kueue.DefaultPodSetName, 0): {Node: testNode2},
				},
				PreemptionVictims: []workload.Reference{workload.NewReference(testNamespace, victimName)},
			},
			wantMode: flavorassigner.DeferredFit,
			wantFit:  false,
			wantCalls: []scheduleWorkloadCall{
				{committedVictims: []workload.Reference{workload.NewReference(testNamespace, victimName)}},
			},
			wantPodSets: []flavorassigner.PodSetAssignment{{
				Name:               kueue.DefaultPodSetName,
				TopologyAssignment: onNodes(domain(testNode2, 1)),
				FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{{
					Flavor: testFlavor,
					Mode:   flavorassigner.Fit,
				}},
			}},
		},
		"workload with unhealthy nodes fails even with committed victim: skips preemption and returns NoFit": {
			flavorQuotas:     []kueue.FlavorQuotas{cpuQuota(testFlavor, "20")},
			podSets:          []testPodSet{{name: kueue.DefaultPodSetName, count: 1, cpu: "2"}},
			unhealthyNodes:   []string{testNode1},
			committedVictims: []workload.Reference{workload.NewReference(testNamespace, victimName)},
			initialMode:      flavorassigner.Fit,
			schedulingResult: &simulator.SchedulingResult{PodPlacements: simulator.PodPlacements{
				podKey(kueue.DefaultPodSetName, 0): {Error: errInsufficientCPU, Reasons: []string{errInsufficientCPU.Error()}},
			}},
			wantMode:                flavorassigner.NoFit,
			wantNoFitReason:         kueue.WorkloadQuotaReservedReasonSimulationFailed,
			wantResolvedNoFitReason: kueue.WorkloadQuotaReservedReasonSimulationFailed,
			wantCalls: []scheduleWorkloadCall{
				{committedVictims: []workload.Reference{workload.NewReference(testNamespace, victimName)}},
			},
			wantPodSets: []flavorassigner.PodSetAssignment{{
				Name: kueue.DefaultPodSetName,
				Status: *flavorassigner.NewStatus(
					podFailure(kueue.DefaultPodSetName, 0, errInsufficientCPU.Error()),
				).WithError(errInsufficientCPU).WithNoFitReason(kueue.WorkloadQuotaReservedReasonSimulationFailed),
				FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{{
					Flavor:      testFlavor,
					Mode:        flavorassigner.NoFit,
					NoFitReason: kueue.WorkloadQuotaReservedReasonSimulationFailed,
				}},
			}},
		},
		"initial assignment fits quota, ScheduleWorkload fails initially but succeeds with preemption: demotes to Preempt and fits": {
			flavorQuotas: []kueue.FlavorQuotas{cpuQuota(testFlavor, "20")},
			podSets:      []testPodSet{{name: kueue.DefaultPodSetName, count: 1, cpu: "2"}},
			initialMode:  flavorassigner.Fit,
			schedulingResult: &simulator.SchedulingResult{PodPlacements: simulator.PodPlacements{
				podKey(kueue.DefaultPodSetName, 0): {Error: errInsufficientCPU, Reasons: []string{errInsufficientCPU.Error()}},
			}},
			preemptionResult: &simulator.SchedulingResult{
				PodPlacements: simulator.PodPlacements{
					podKey(kueue.DefaultPodSetName, 0): {Node: testNode1},
				},
				PreemptionVictims: []workload.Reference{workload.NewReference(testNamespace, victimName)},
			},
			wantMode: flavorassigner.Preempt,
			wantFit:  true,
			wantCalls: []scheduleWorkloadCall{
				{},
				{consumedCandidates: []workload.Reference{workload.NewReference(testNamespace, victimName)}},
			},
			wantPreemptionTargets: []workload.Reference{workload.NewReference(testNamespace, victimName)},
			wantPodSets: []flavorassigner.PodSetAssignment{{
				Name:               kueue.DefaultPodSetName,
				Status:             *flavorassigner.NewStatus(podFailure(kueue.DefaultPodSetName, 0, errInsufficientCPU.Error())),
				TopologyAssignment: onNodes(domain(testNode1, 1)),
				FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{{
					Flavor: testFlavor,
					Mode:   flavorassigner.Fit,
				}},
			}},
		},
		"initial assignment fits quota, ScheduleWorkload fails initially and with candidates but succeeds on empty cluster: demotes to Preempt and reserves topology": {
			flavorQuotas: []kueue.FlavorQuotas{cpuQuota(testFlavor, "20")},
			podSets:      []testPodSet{{name: kueue.DefaultPodSetName, count: 1, cpu: "2"}},
			initialMode:  flavorassigner.Fit,
			schedulingResult: &simulator.SchedulingResult{PodPlacements: simulator.PodPlacements{
				podKey(kueue.DefaultPodSetName, 0): {Error: errInsufficientCPU, Reasons: []string{errInsufficientCPU.Error()}},
			}},
			preemptionResult: &simulator.SchedulingResult{PodPlacements: simulator.PodPlacements{
				podKey(kueue.DefaultPodSetName, 0): {Error: errInsufficientCPU, Reasons: []string{errInsufficientCPU.Error()}},
			}},
			emptyClusterResult: &simulator.SchedulingResult{PodPlacements: simulator.PodPlacements{
				podKey(kueue.DefaultPodSetName, 0): {Node: testNode1},
			}},
			wantMode: flavorassigner.Preempt,
			wantFit:  false,
			wantCalls: []scheduleWorkloadCall{
				{},
				{consumedCandidates: []workload.Reference{workload.NewReference(testNamespace, victimName)}},
				{simulateEmpty: true},
			},
			wantPodSets: []flavorassigner.PodSetAssignment{{
				Name:               kueue.DefaultPodSetName,
				Status:             *flavorassigner.NewStatus(podFailure(kueue.DefaultPodSetName, 0, errInsufficientCPU.Error())),
				TopologyAssignment: onNodes(domain(testNode1, 1)),
				FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{{
					Flavor: testFlavor,
					Mode:   flavorassigner.Fit,
				}},
			}},
		},
		"initial assignment requires preemption and workload has unhealthy nodes: skips preemption simulation": {
			flavorQuotas:   []kueue.FlavorQuotas{cpuQuota(testFlavor, "3")},
			podSets:        []testPodSet{{name: kueue.DefaultPodSetName, count: 1, cpu: "2"}},
			unhealthyNodes: []string{testNode1},
			initialMode:    flavorassigner.Preempt,
			wantMode:       flavorassigner.Preempt,
			wantFit:        false,
			wantPodSets: []flavorassigner.PodSetAssignment{{
				Name:   kueue.DefaultPodSetName,
				Status: *flavorassigner.NewStatus("insufficient unused quota for cpu in flavor tas-flavor, 1 more needed"),
				FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{{
					Flavor:                testFlavor,
					Mode:                  flavorassigner.Preempt,
					PreemptionPossibility: new(policy.Preempt),
				}},
			}},
		},
		"initial assignment fits quota, ScheduleWorkload fails and workload has unhealthy nodes: skips preemption and does not fit": {
			flavorQuotas:   []kueue.FlavorQuotas{cpuQuota(testFlavor, "20")},
			podSets:        []testPodSet{{name: kueue.DefaultPodSetName, count: 1, cpu: "2"}},
			unhealthyNodes: []string{testNode1},
			initialMode:    flavorassigner.Fit,
			schedulingResult: &simulator.SchedulingResult{PodPlacements: simulator.PodPlacements{
				podKey(kueue.DefaultPodSetName, 0): {Error: errInsufficientCPU, Reasons: []string{errInsufficientCPU.Error()}},
			}},
			wantMode:                flavorassigner.NoFit,
			wantNoFitReason:         kueue.WorkloadQuotaReservedReasonSimulationFailed,
			wantResolvedNoFitReason: kueue.WorkloadQuotaReservedReasonSimulationFailed,
			wantCalls:               []scheduleWorkloadCall{{}},
			wantPodSets: []flavorassigner.PodSetAssignment{{
				Name: kueue.DefaultPodSetName,
				Status: *flavorassigner.NewStatus(
					podFailure(kueue.DefaultPodSetName, 0, errInsufficientCPU.Error()),
				).WithError(errInsufficientCPU).WithNoFitReason(kueue.WorkloadQuotaReservedReasonSimulationFailed),
				FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{{
					Flavor:      testFlavor,
					Mode:        flavorassigner.NoFit,
					NoFitReason: kueue.WorkloadQuotaReservedReasonSimulationFailed,
				}},
			}},
		},
		"ScheduleWorkload fails completely: planning error": {
			flavorQuotas:     []kueue.FlavorQuotas{cpuQuota(testFlavor, "20")},
			podSets:          []testPodSet{{name: kueue.DefaultPodSetName, count: 2, cpu: "1"}},
			initialMode:      flavorassigner.Fit,
			schedulingResult: &simulator.SchedulingResult{Error: errSchedule},
			wantMode:         flavorassigner.Fit,
			wantErrs:         []string{"failed to schedule workload: " + errSchedule.Error()},
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
			wantAdded: &addedPodGroups{cpgs: 1, pgs: 2},
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
		"first CompositePodGroup fails to be added: planning error, nothing to clean up": {
			flavorQuotas: []kueue.FlavorQuotas{cpuQuota(testFlavor, "20")},
			podSets:      groupedPodSets,
			initialMode:  flavorassigner.Fit,
			failAddCPGAt: 1,
			wantMode:     flavorassigner.Fit,
			wantErrs:     []string{"virtualization error: unable to add composite pod group"},
			wantPodSets:  groupedPodSetsUnchanged,
			wantAdded:    &addedPodGroups{},
		},
		"second CompositePodGroup fails to be added: planning error, the added one is removed": {
			flavorQuotas: []kueue.FlavorQuotas{cpuQuota(testFlavor, "20")},
			podSets:      groupedPodSets,
			initialMode:  flavorassigner.Fit,
			failAddCPGAt: 2,
			wantMode:     flavorassigner.Fit,
			wantErrs:     []string{"virtualization error: unable to add composite pod group"},
			wantPodSets:  groupedPodSetsUnchanged,
			wantAdded:    &addedPodGroups{cpgs: 1},
		},
		"first PodGroup fails to be added: planning error, all CompositePodGroups removed": {
			flavorQuotas: []kueue.FlavorQuotas{cpuQuota(testFlavor, "20")},
			podSets:      groupedPodSets,
			initialMode:  flavorassigner.Fit,
			failAddPGAt:  1,
			wantMode:     flavorassigner.Fit,
			wantErrs:     []string{"virtualization error: unable to add pod group"},
			wantPodSets:  groupedPodSetsUnchanged,
			wantAdded:    &addedPodGroups{cpgs: 2},
		},
		"last PodGroup fails to be added: planning error, all added groups removed": {
			flavorQuotas: []kueue.FlavorQuotas{cpuQuota(testFlavor, "20")},
			podSets:      groupedPodSets,
			initialMode:  flavorassigner.Fit,
			failAddPGAt:  3,
			wantMode:     flavorassigner.Fit,
			wantErrs:     []string{"virtualization error: unable to add pod group"},
			wantPodSets:  groupedPodSetsUnchanged,
			wantAdded:    &addedPodGroups{cpgs: 2, pgs: 2},
		},
		"PodGroup fails to be added and the groups fail to be removed: planning error": {
			flavorQuotas: []kueue.FlavorQuotas{cpuQuota(testFlavor, "20")},
			podSets:      groupedPodSets,
			initialMode:  flavorassigner.Fit,
			failAddPGAt:  3,
			removeErr:    errSimulator,
			wantMode:     flavorassigner.Fit,
			wantErrs: []string{
				"virtualization error: unable to add pod group",
				"unable to remove pod group",
				"unable to remove composite pod group",
			},
			wantPodSets: groupedPodSetsUnchanged,
			wantAdded:   &addedPodGroups{cpgs: 2, pgs: 2},
		},
		"second CompositePodGroup fails to be added and the added one fails to be removed: planning error": {
			flavorQuotas: []kueue.FlavorQuotas{cpuQuota(testFlavor, "20")},
			podSets:      groupedPodSets,
			initialMode:  flavorassigner.Fit,
			failAddCPGAt: 2,
			removeErr:    errSimulator,
			wantMode:     flavorassigner.Fit,
			wantErrs: []string{
				"virtualization error: unable to add composite pod group",
				"unable to remove composite pod group",
			},
			wantPodSets: groupedPodSetsUnchanged,
			wantAdded:   &addedPodGroups{cpgs: 1},
		},
		"groups fail to be removed after scheduling: planning error": {
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
			removeErr: errSimulator,
			wantMode:  flavorassigner.Fit,
			wantErrs: []string{
				"virtualization error: unable to remove pod group",
				"unable to remove composite pod group",
			},
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
			wantAdded: &addedPodGroups{cpgs: 1, pgs: 2},
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
			testSim := &testSimulator{
				SchedulerSimulator: snapshot.SchedulerSimulator,
				failAddCPGAt:       tc.failAddCPGAt,
				failAddPGAt:        tc.failAddPGAt,
				removeErr:          tc.removeErr,
			}
			if tc.schedulingResult != nil {
				firstStep := stepFromSchedulingResult(*tc.schedulingResult)
				testSim.steps = []simulationStep{firstStep}
				if tc.preemptionResult != nil {
					testSim.steps = append(testSim.steps, stepFromSchedulingResult(*tc.preemptionResult))
				}
				if tc.emptyClusterResult != nil {
					testSim.steps = append(testSim.steps, stepFromSchedulingResult(*tc.emptyClusterResult))
				}
				if len(testSim.steps) == 1 && tc.initialMode == flavorassigner.Fit && tc.wantMode == flavorassigner.NoFit && len(tc.unhealthyNodes) == 0 {
					// When initial Fit placement fails, Plan demotes to Preempt and retries
					// twice (first with preemption candidates, then on an empty cluster).
					// For failure test cases that only specify a single schedulingResult,
					// reuse that same failure result for both fallback calls.
					testSim.steps = append(testSim.steps, firstStep, firstStep)
				}
			}
			snapshot.SchedulerSimulator = testSim

			var podSets []kueue.PodSet
			for _, ps := range tc.podSets {
				requiredLevel := ps.requiredLevel
				if requiredLevel == "" {
					requiredLevel = corev1.LabelHostname
				}
				psWrapper := utiltestingapi.MakePodSet(ps.name, ps.count).
					RequiredTopologyRequest(requiredLevel).
					Request(corev1.ResourceCPU, ps.cpu)
				if ps.podSetGroup != "" {
					psWrapper.PodSetGroup(ps.podSetGroup)
				}
				podSets = append(podSets, *psWrapper.Obj())
			}
			wl := workload.NewInfo(log, utiltestingapi.MakeWorkload(incomingName, testNamespace).
				Priority(incomingPriority).
				UnhealthyNodes(tc.unhealthyNodes...).
				PodSets(podSets...).
				Obj())
			wl.ClusterQueue = testCQ

			preemptor := preemption.New(
				utiltesting.NewFakeClient(), workload.Ordering{}, &utiltesting.EventRecorder{}, nil, false,
				clocktesting.NewFakeClock(time.Now()), nil, preemptexpectations.New(), nil,
			)
			filter := preemption.BuildFilter(log, snapshot, wl)
			freeCapacityBefore := freeCapacity(t, snapshot)

			var opts []assignment.PlannerOption
			if !tc.omitPreemptionFilter {
				opts = append(opts, assignment.WithPreemptionFilter(filter))
			}
			var revertRemoval func()
			if len(tc.committedVictims) > 0 {
				var toRemove []*workload.Info
				committedMap := make(map[workload.Reference]*workload.Info, len(tc.committedVictims))
				for _, cv := range tc.committedVictims {
					wlInfo := snapshot.ClusterQueue(testCQ).Workloads[cv]
					if wlInfo != nil {
						toRemove = append(toRemove, wlInfo)
					} else {
						wlInfo = workload.NewInfo(log, utiltestingapi.MakeWorkload(strings.TrimPrefix(string(cv), testNamespace+"/"), testNamespace).Obj())
					}
					committedMap[cv] = wlInfo
				}
				opts = append(opts, assignment.WithCommittedPreemptionVictims(committedMap))
				if len(toRemove) > 0 {
					revertRemoval = snapshot.SimulateWorkloadRemoval(toRemove)
				}
			}

			assigner := flavorassigner.New(
				wl, snapshot.ClusterQueue(testCQ), snapshot.ResourceFlavors, false,
				preemption.NewOracle(preemptor, snapshot), nil,
				configapi.QuotaCheckBlockUndeclared, resources.NewResourceFormatter(), 0,
			)
			initialAssignment := assigner.AssignFlavors(ctx, log, nil)
			if initialMode := initialAssignment.RepresentativeMode(); initialMode != tc.initialMode {
				t.Fatalf("AssignFlavors() misconfigured - incorrect initial mode: want %v, got %v", tc.initialMode, initialMode)
			}

			// When
			planner := NewPlanner(wl, snapshot, preemptor)
			gotPlan := planner.Plan(ctx, &initialAssignment, opts...)
			if revertRemoval != nil {
				revertRemoval()
			}

			// Then
			if gotPlan.Assignment != &initialAssignment {
				t.Errorf("Plan() returned a different assignment object than the initial one")
			}
			wantCallCount := len(testSim.steps)
			if len(testSim.receivedCalls) != wantCallCount {
				t.Errorf("Plan() called ScheduleWorkload unexpected number of times: want %d, got %d", wantCallCount, len(testSim.receivedCalls))
			}
			if tc.wantCalls != nil {
				if diff := cmp.Diff(tc.wantCalls, testSim.receivedCalls, cmp.AllowUnexported(scheduleWorkloadCall{}), cmpopts.EquateEmpty()); diff != "" {
					t.Errorf("Plan() ScheduleWorkload calls mismatch (-want +got):\n%s", diff)
				}
			}
			switch {
			case len(tc.wantErrs) == 0 && gotPlan.Error != nil:
				t.Errorf("Plan() returned unexpected error: %v", gotPlan.Error)
			case len(tc.wantErrs) != 0 && gotPlan.Error == nil:
				t.Errorf("Plan() did not return the expected errors %q", tc.wantErrs)
			default:
				for _, wantErr := range tc.wantErrs {
					if !strings.Contains(gotPlan.Error.Error(), wantErr) {
						t.Errorf("Plan() error mismatch: want it to contain %q, got %q", wantErr, gotPlan.Error.Error())
					}
				}
			}
			if tc.schedulingResult != nil && tc.schedulingResult.Error != nil && !errors.Is(gotPlan.Error, tc.schedulingResult.Error) {
				t.Errorf("Plan() error does not wrap the ScheduleWorkload error: want %v, got %v", tc.schedulingResult.Error, gotPlan.Error)
			}
			if (tc.failAddCPGAt != 0 || tc.failAddPGAt != 0 || tc.removeErr != nil) && !errors.Is(gotPlan.Error, errSimulator) {
				t.Errorf("Plan() error does not wrap the simulator error: want %v, got %v", errSimulator, gotPlan.Error)
			}
			if tc.wantAdded != nil {
				gotAdded := addedPodGroups{cpgs: len(testSim.addedCPGs), pgs: len(testSim.addedPGs)}
				if diff := cmp.Diff(*tc.wantAdded, gotAdded, cmp.AllowUnexported(addedPodGroups{})); diff != "" {
					t.Errorf("Plan() added group mismatch (-want +got):\n%s", diff)
				}
			}
			if diff := cmp.Diff(testSim.addedPGs, testSim.removedPGs); diff != "" {
				t.Errorf("Plan() removed PodGroups mismatch (-added +removed):\n%s", diff)
			}
			if diff := cmp.Diff(testSim.addedCPGs, testSim.removedCPGs); diff != "" {
				t.Errorf("Plan() removed CompositePodGroups mismatch (-added +removed):\n%s", diff)
			}
			if gotMode := gotPlan.Assignment.RepresentativeMode(); gotMode != tc.wantMode {
				t.Errorf("Plan() assignment mode mismatch: want %v, got %v", tc.wantMode, gotMode)
			}
			if tc.wantFit != gotPlan.CanFit() {
				t.Errorf("Plan() returned unexpected verdict - want fit: %v, got: %v", tc.wantFit, gotPlan.CanFit())
			}
			var gotTargets []workload.Reference
			for _, target := range gotPlan.PreemptionTargets {
				gotTargets = append(gotTargets, workload.Key(target.WorkloadInfo.Obj))
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

	planner := NewPlanner(wl, snapshot, preemptor)
	plan := planner.Plan(ctx, &initialAssignment, assignment.WithPreemptionFilter(preemption.BuildFilter(log, snapshot, wl)))
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

func TestPlan_PreemptionStrategies(t *testing.T) {
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
		simulationSteps       []simulationStep
		wantCalls             []scheduleWorkloadCall
		wantPreemptionTargets []workload.Reference
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
			simulationSteps: []simulationStep{
				{
					podPlacements: simulator.PodPlacements{
						podKey(kueue.DefaultPodSetName, 0): {Error: errInsufficientCPU, Reasons: []string{errInsufficientCPU.Error()}},
					},
				},
				{
					podPlacements: simulator.PodPlacements{
						podKey(kueue.DefaultPodSetName, 0): {Node: testNode1},
					},
					nodeVictims: []workload.Reference{
						workload.NewReference(testNamespace, "victim-cohort"),
						workload.NewReference(testNamespace, "victim-low"),
					},
				},
			},
			wantCalls: []scheduleWorkloadCall{
				{
					consumedCandidates: []workload.Reference{
						workload.NewReference(testNamespace, "victim-low"),
					},
				},
				{
					consumedCandidates: []workload.Reference{
						workload.NewReference(testNamespace, "victim-cohort"),
						workload.NewReference(testNamespace, "victim-low"),
					},
				},
			},
			wantPreemptionTargets: []workload.Reference{
				workload.NewReference(testNamespace, "victim-cohort"),
				workload.NewReference(testNamespace, "victim-low"),
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
			simulationSteps: []simulationStep{
				{
					podPlacements: simulator.PodPlacements{
						podKey(kueue.DefaultPodSetName, 0): {Node: testNode1},
					},
					nodeVictims: []workload.Reference{
						workload.NewReference(testNamespace, "borrower-low"),
					},
				},
			},
			wantCalls: []scheduleWorkloadCall{
				{
					consumedCandidates: []workload.Reference{
						workload.NewReference(testNamespace, "borrower-low"),
					},
				},
			},
			wantPreemptionTargets: []workload.Reference{
				workload.NewReference(testNamespace, "borrower-low"),
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
			simulationSteps: []simulationStep{
				{
					podPlacements: simulator.PodPlacements{
						podKey(kueue.DefaultPodSetName, 0): {Node: testNode1},
					},
					nodeVictims: []workload.Reference{
						workload.NewReference(testNamespace, "borrower-low"),
					},
				},
			},
			wantCalls: []scheduleWorkloadCall{
				{
					consumedCandidates: []workload.Reference{
						workload.NewReference(testNamespace, "borrower-low"),
						workload.NewReference(testNamespace, "borrower-mid"),
					},
				},
			},
			wantPreemptionTargets: []workload.Reference{
				workload.NewReference(testNamespace, "borrower-low"),
				workload.NewReference(testNamespace, "borrower-mid"),
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
				utiltestingapi.MakeClusterQueue("cq-b").
					Cohort("cohort").
					FairWeight(resource.MustParse("1")).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas(testFlavor).
						Resource(corev1.ResourceCPU, "3").
						Obj()).
					Obj(),
			},
			admittedWorkloads: []*kueue.Workload{
				makeAdmittedWorkload("a-small", testCQ, 10, "2", testNode1, now),
				makeAdmittedWorkload("b-hero", "cq-b", 10, "4", testNode2, now),
			},
			incomingCPU: "4",
			simulationSteps: []simulationStep{
				{
					podPlacements: simulator.PodPlacements{
						podKey(kueue.DefaultPodSetName, 0): {Node: testNode2},
					},
					// node-2 (8 CPUs) already has 4 free CPUs alongside b-hero (4 CPUs), so
					// no node-level victims are needed; PreemptionFilter pulls [a-small, b-hero]
					// for quota and reprieves a-small (2 + 4 <= 6 cohort quota), leaving [b-hero].
					nodeVictims: nil,
				},
			},
			wantCalls: []scheduleWorkloadCall{
				{
					consumedCandidates: []workload.Reference{
						workload.NewReference(testNamespace, "a-small"),
						workload.NewReference(testNamespace, "b-hero"),
					},
				},
				{simulateEmpty: true},
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
				steps:              tc.simulationSteps,
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
			gotPlan := planner.Plan(ctx, &initialAssignment, assignment.WithPreemptionFilter(preemption.BuildFilter(log, snapshot, wl)))

			if gotPlan.Error != nil {
				t.Fatalf("Plan() unexpected error: %v", gotPlan.Error)
			}
			if diff := cmp.Diff(tc.wantCalls, testSim.receivedCalls, cmp.AllowUnexported(scheduleWorkloadCall{})); diff != "" {
				t.Errorf("Plan() ScheduleWorkload calls mismatch (-want +got):\n%s", diff)
			}
			var gotTargets []workload.Reference
			for _, target := range gotPlan.PreemptionTargets {
				gotTargets = append(gotTargets, workload.Key(target.WorkloadInfo.Obj))
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

func TestPlan_CommittedVictims(t *testing.T) {
	features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
		kubefeatures.GenericWorkload:             true,
		features.TopologyAwareScheduling:         true,
		features.SchedulerLibraryIntegration:     true,
		features.SchedulerLibraryDeepIntegration: true,
	})

	simulatorFactory, err := wascache.NewWASSimulatorFactory(klog.NewContext(t.Context(), logr.Discard()), nil)
	if err != nil {
		t.Fatalf("NewWASSimulatorFactory() unexpected error: %v", err)
	}

	now := time.Now()
	const highPriority = 100
	nodeCPUs := map[string]string{
		testNode1: "9",
		testNode2: "6",
	}

	committed6Node1 := utiltestingapi.MakeWorkload("committed-victim", testNamespace).
		Priority(highPriority).
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
			RequiredTopologyRequest(corev1.LabelHostname).
			Request(corev1.ResourceCPU, "6").
			Obj()).
		ReserveQuotaAt(utiltestingapi.MakeAdmission(testCQ).
			PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
				Assignment(corev1.ResourceCPU, testFlavor, "6").
				TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
					Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{testNode1}, 1).Obj()).
					Obj()).
				Obj()).
			Obj(), now)

	candidate6Node2 := utiltestingapi.MakeWorkload("candidate-1", testNamespace).
		Priority(10).
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
			RequiredTopologyRequest(corev1.LabelHostname).
			Request(corev1.ResourceCPU, "6").
			Obj()).
		ReserveQuotaAt(utiltestingapi.MakeAdmission(testCQ).
			PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
				Assignment(corev1.ResourceCPU, testFlavor, "6").
				TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
					Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{testNode2}, 1).Obj()).
					Obj()).
				Obj()).
			Obj(), now)

	committed3Node1 := utiltestingapi.MakeWorkload("committed-victim", testNamespace).
		Priority(highPriority).
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
			RequiredTopologyRequest(corev1.LabelHostname).
			Request(corev1.ResourceCPU, "3").
			Obj()).
		ReserveQuotaAt(utiltestingapi.MakeAdmission(testCQ).
			PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
				Assignment(corev1.ResourceCPU, testFlavor, "3").
				TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
					Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{testNode1}, 1).Obj()).
					Obj()).
				Obj()).
			Obj(), now)

	blocker6Node2 := candidate6Node2.Clone().Name("blocker").Priority(highPriority)

	cases := map[string]struct {
		cqQuota           string
		admittedWorkloads []*kueue.Workload
		committedVictims  []workload.Reference
		requestedCPU      string
		initialMode       flavorassigner.FlavorAssignmentMode

		wantMode               flavorassigner.FlavorAssignmentMode
		wantPreemptionTargets  []workload.Reference
		wantTopologyAssignment *tas.TopologyAssignment
	}{
		"committed victim not needed for placement: fits immediately on free capacity": {
			cqQuota: "12",
			admittedWorkloads: []*kueue.Workload{
				committed6Node1.Clone().Obj(),
			},
			committedVictims: []workload.Reference{
				workload.NewReference(testNamespace, "committed-victim"),
			},
			requestedCPU:           "6",
			initialMode:            flavorassigner.Fit,
			wantMode:               flavorassigner.Fit,
			wantTopologyAssignment: onNodes(domain(testNode2, 1)),
		},
		"committed victim not needed for node placement, but needed for quota: deferred fit": {
			cqQuota: "12",
			admittedWorkloads: []*kueue.Workload{
				committed3Node1.Clone().Obj(),
				committed6Node1.Clone().Name("blocker").Obj(),
			},
			committedVictims: []workload.Reference{
				workload.NewReference(testNamespace, "committed-victim"),
			},
			requestedCPU:           "6",
			initialMode:            flavorassigner.Fit,
			wantMode:               flavorassigner.DeferredFit,
			wantTopologyAssignment: onNodes(domain(testNode2, 1)),
		},
		"committed victim needed for placement: deferred fit without preempting candidates": {
			cqQuota: "12",
			admittedWorkloads: []*kueue.Workload{
				committed6Node1.Clone().Obj(),
				candidate6Node2.Clone().Obj(),
			},
			committedVictims: []workload.Reference{
				workload.NewReference(testNamespace, "committed-victim"),
			},
			requestedCPU:           "6",
			initialMode:            flavorassigner.Fit,
			wantMode:               flavorassigner.DeferredFit,
			wantTopologyAssignment: onNodes(domain(testNode1, 1)),
		},
		"committed victim and one preemption candidate needed to fit when quota fits": {
			cqQuota: "18",
			admittedWorkloads: []*kueue.Workload{
				committed3Node1.Clone().Obj(),
				committed3Node1.Clone().Name("candidate-1").Priority(10).Obj(),
				committed3Node1.Clone().Name("candidate-2").Priority(20).Obj(),
				blocker6Node2.Clone().Obj(),
			},
			committedVictims: []workload.Reference{
				workload.NewReference(testNamespace, "committed-victim"),
			},
			requestedCPU: "6",
			initialMode:  flavorassigner.Fit,
			wantMode:     flavorassigner.Preempt,
			wantPreemptionTargets: []workload.Reference{
				workload.NewReference(testNamespace, "candidate-1"),
			},
			wantTopologyAssignment: onNodes(domain(testNode1, 1)),
		},
		"committed victim and one preemption candidate needed to fit when quota requires preemption": {
			cqQuota: "15",
			admittedWorkloads: []*kueue.Workload{
				committed3Node1.Clone().Obj(),
				committed3Node1.Clone().Name("candidate-1").Priority(10).Obj(),
				committed3Node1.Clone().Name("candidate-2").Priority(20).Obj(),
				blocker6Node2.Clone().Obj(),
			},
			committedVictims: []workload.Reference{
				workload.NewReference(testNamespace, "committed-victim"),
			},
			requestedCPU: "6",
			initialMode:  flavorassigner.Preempt,
			wantMode:     flavorassigner.Preempt,
			wantPreemptionTargets: []workload.Reference{
				workload.NewReference(testNamespace, "candidate-1"),
			},
			wantTopologyAssignment: onNodes(domain(testNode1, 1)),
		},
		"committed victim with lower priority is not re-yielded as a duplicate candidate": {
			cqQuota: "15",
			admittedWorkloads: []*kueue.Workload{
				committed3Node1.Clone().Priority(5).Obj(),
				committed3Node1.Clone().Name("candidate-1").Priority(10).Obj(),
				committed3Node1.Clone().Name("candidate-2").Priority(20).Obj(),
				blocker6Node2.Clone().Obj(),
			},
			committedVictims: []workload.Reference{
				workload.NewReference(testNamespace, "committed-victim"),
			},
			requestedCPU: "6",
			initialMode:  flavorassigner.Preempt,
			wantMode:     flavorassigner.Preempt,
			wantPreemptionTargets: []workload.Reference{
				workload.NewReference(testNamespace, "candidate-1"),
			},
			wantTopologyAssignment: onNodes(domain(testNode1, 1)),
		},
		"committed victim and preemption candidates insufficient: falls back to empty-cluster simulation": {
			cqQuota: "24",
			admittedWorkloads: []*kueue.Workload{
				committed3Node1.Clone().Obj(),
				committed6Node1.Clone().Name("blocker-1").Obj(),
				blocker6Node2.Clone().Obj(),
			},
			committedVictims: []workload.Reference{
				workload.NewReference(testNamespace, "committed-victim"),
			},
			requestedCPU:           "7",
			initialMode:            flavorassigner.Fit,
			wantMode:               flavorassigner.Preempt,
			wantTopologyAssignment: onNodes(domain(testNode1, 1)),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			log := testr.New(t)
			ctx := ctrllog.IntoContext(t.Context(), log)

			cache := schdcache.New(utiltesting.NewFakeClient(), schdcache.WithSimulatorFactory(simulatorFactory))
			cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor(testFlavor).
				NodeLabel("tas", testFlavor).TopologyName("topology").Obj())
			cache.AddOrUpdateTopology(log, utiltestingapi.MakeTopology("topology").Levels(testBlockLabel, corev1.LabelHostname).Obj())
			for _, node := range []string{testNode1, testNode2} {
				cache.TASCache().SyncNode(testingnode.MakeNode(node).
					Label("tas", testFlavor).
					Label(testBlockLabel, testBlock).
					Label(corev1.LabelHostname, node).
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse(nodeCPUs[node]),
						corev1.ResourcePods: resource.MustParse("32"),
					}).
					Ready().
					Obj())
			}
			if err := cache.AddClusterQueue(ctx, utiltestingapi.MakeClusterQueue(testCQ).
				Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
				ResourceGroup(cpuQuota(testFlavor, tc.cqQuota)).
				Obj()); err != nil {
				t.Fatalf("adding ClusterQueue: %v", err)
			}
			for _, w := range tc.admittedWorkloads {
				if w.UID == "" {
					w.UID = types.UID(w.Name + "-uid")
				}
				if !cache.AddOrUpdateWorkload(ctx, log, w) {
					t.Fatalf("adding workload %q to cache", w.Name)
				}
			}

			snapshot, err := cache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("building snapshot: %v", err)
			}
			var toRemove []*workload.Info
			committedMap := make(map[workload.Reference]*workload.Info, len(tc.committedVictims))
			for _, cv := range tc.committedVictims {
				wlInfo := snapshot.ClusterQueue(testCQ).Workloads[cv]
				if wlInfo == nil {
					t.Fatalf("workload %q not found in snapshot", cv)
				}
				toRemove = append(toRemove, wlInfo)
				committedMap[cv] = wlInfo
			}

			wl := workload.NewInfo(log, utiltestingapi.MakeWorkload(incomingName, testNamespace).
				UID("incoming-uid").
				Priority(incomingPriority).
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					RequiredTopologyRequest(corev1.LabelHostname).
					Request(corev1.ResourceCPU, tc.requestedCPU).
					Obj()).
				Obj())
			wl.ClusterQueue = testCQ

			preemptor := preemption.New(
				utiltesting.NewFakeClient(), workload.Ordering{}, &utiltesting.EventRecorder{}, nil, false,
				clocktesting.NewFakeClock(now), nil, preemptexpectations.New(), nil,
			)
			assigner := flavorassigner.New(
				wl, snapshot.ClusterQueue(testCQ), snapshot.ResourceFlavors, false,
				preemption.NewOracle(preemptor, snapshot), nil,
				configapi.QuotaCheckBlockUndeclared, resources.NewResourceFormatter(), 0,
			)
			// Mirror scheduler.getAssignments: build PreemptionFilter on the clean snapshot,
			// then remove committed victims from the active snapshot across both AssignFlavors and Plan.
			filter := preemption.BuildFilter(log, snapshot, wl)
			freeCapacityBefore := freeCapacity(t, snapshot)

			var revertRemoval func()
			if len(toRemove) > 0 {
				revertRemoval = snapshot.SimulateWorkloadRemoval(toRemove)
			}
			initialAssignment := assigner.AssignFlavors(ctx, log, nil)
			if gotInitialMode := initialAssignment.RepresentativeMode(); gotInitialMode != tc.initialMode {
				t.Fatalf("AssignFlavors() mode = %v, want %v", gotInitialMode, tc.initialMode)
			}

			opts := []assignment.PlannerOption{assignment.WithPreemptionFilter(filter)}
			if len(committedMap) > 0 {
				opts = append(opts, assignment.WithCommittedPreemptionVictims(committedMap))
			}
			planner := NewPlanner(wl, snapshot, preemptor)
			gotPlan := planner.Plan(ctx, &initialAssignment, opts...)
			if revertRemoval != nil {
				revertRemoval()
			}

			if gotPlan.Error != nil {
				t.Fatalf("Plan() unexpected error: %v", gotPlan.Error)
			}
			if gotMode := gotPlan.Assignment.RepresentativeMode(); gotMode != tc.wantMode {
				t.Errorf("Plan() RepresentativeMode() = %v, want %v", gotMode, tc.wantMode)
			}
			if gotFlavorMode := gotPlan.Assignment.PodSets[0].Flavors[corev1.ResourceCPU].Mode; gotFlavorMode != tc.wantMode {
				t.Errorf("Plan() PodSets[0].Flavors[cpu].Mode = %v, want %v", gotFlavorMode, tc.wantMode)
			}
			wantFit := tc.wantMode == flavorassigner.Fit || (tc.wantMode == flavorassigner.Preempt && len(tc.wantPreemptionTargets) > 0)
			if gotFit := gotPlan.CanFit(); gotFit != wantFit {
				t.Errorf("Plan() CanFit() = %v, want %v", gotFit, wantFit)
			}
			var gotTargets []workload.Reference
			for _, target := range gotPlan.PreemptionTargets {
				gotTargets = append(gotTargets, workload.Key(target.WorkloadInfo.Obj))
			}
			if diff := cmp.Diff(tc.wantPreemptionTargets, gotTargets); diff != "" {
				t.Errorf("Plan() preemption targets mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantTopologyAssignment, gotPlan.Assignment.PodSets[0].TopologyAssignment); diff != "" {
				t.Errorf("Plan() TopologyAssignment mismatch (-want +got):\n%s", diff)
			}
			if freeCapacityAfter := freeCapacity(t, snapshot); freeCapacityAfter != freeCapacityBefore {
				t.Errorf("Plan() modified snapshot TAS free capacity: before %s, after %s", freeCapacityBefore, freeCapacityAfter)
			}
		})
	}

	t.Run("reusing planner across full and partial counts resets filter cleanly", func(t *testing.T) {
		log := testr.New(t)
		ctx := ctrllog.IntoContext(t.Context(), log)

		cache := schdcache.New(utiltesting.NewFakeClient(), schdcache.WithSimulatorFactory(simulatorFactory))
		cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor(testFlavor).
			NodeLabel("tas", testFlavor).TopologyName("topology").Obj())
		cache.AddOrUpdateTopology(log, utiltestingapi.MakeTopology("topology").Levels(testBlockLabel, corev1.LabelHostname).Obj())
		for _, node := range []string{testNode1, testNode2} {
			cache.TASCache().SyncNode(testingnode.MakeNode(node).
				Label("tas", testFlavor).
				Label(testBlockLabel, testBlock).
				Label(corev1.LabelHostname, node).
				StatusAllocatable(corev1.ResourceList{
					corev1.ResourceCPU:  resource.MustParse(nodeCPUs[node]),
					corev1.ResourcePods: resource.MustParse("32"),
				}).
				Ready().
				Obj())
		}
		if err := cache.AddClusterQueue(ctx, utiltestingapi.MakeClusterQueue(testCQ).
			Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
			ResourceGroup(cpuQuota(testFlavor, "9")).
			Obj()); err != nil {
			t.Fatalf("adding ClusterQueue: %v", err)
		}
		// committed-victim uses 6 CPU on testNode1 (capacity 9, leaving 3 free); blocker uses 6 CPU on testNode2 (capacity 6, leaving 0 free).
		for _, w := range []*kueue.Workload{committed6Node1.Clone().UID("committed-victim-uid").Obj(), blocker6Node2.Clone().UID("blocker-uid").Obj()} {
			if !cache.AddOrUpdateWorkload(ctx, log, w) {
				t.Fatalf("adding workload %q to cache", w.Name)
			}
		}

		snapshot, err := cache.Snapshot(ctx)
		if err != nil {
			t.Fatalf("building snapshot: %v", err)
		}
		cvRef := workload.NewReference(testNamespace, "committed-victim")
		cvInfo := snapshot.ClusterQueue(testCQ).Workloads[cvRef]
		committedMap := preemption.PreemptedWorkloads{cvRef: cvInfo}

		wl := workload.NewInfo(log, utiltestingapi.MakeWorkload(incomingName, testNamespace).
			UID("incoming-uid").
			Priority(incomingPriority).
			PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).
				SetMinimumCount(1).
				RequiredTopologyRequest(corev1.LabelHostname).
				Request(corev1.ResourceCPU, "1").
				Obj()).
			Obj())
		wl.ClusterQueue = testCQ

		preemptor := preemption.New(
			utiltesting.NewFakeClient(), workload.Ordering{}, &utiltesting.EventRecorder{}, nil, false,
			clocktesting.NewFakeClock(now), nil, preemptexpectations.New(), nil,
		)
		filter := preemption.BuildFilter(log, snapshot, wl)
		revert := snapshot.SimulateWorkloadRemoval([]*workload.Info{cvInfo})
		defer revert()

		assigner := flavorassigner.New(
			wl, snapshot.ClusterQueue(testCQ), snapshot.ResourceFlavors, false,
			preemption.NewOracle(preemptor, snapshot), nil,
			configapi.QuotaCheckBlockUndeclared, resources.NewResourceFormatter(), 0,
		)
		planner := NewPlanner(wl, snapshot, preemptor)
		opts := []assignment.PlannerOption{
			assignment.WithCommittedPreemptionVictims(committedMap),
			assignment.WithPreemptionFilter(filter),
		}

		// Full count (3 CPU) fits on testNode1's 3 free CPU for node placement, EXCEPT CQ quota is 9
		// and blocker (6) + committed-victim (6) = 12 in filter, so 3 CPU needs committed-victim for quota -> DeferredFit!
		fullAsgn := assigner.AssignFlavors(ctx, log, nil)
		fullPlan := planner.Plan(ctx, &fullAsgn, opts...)
		if fullPlan.Error != nil {
			t.Fatalf("full Plan() unexpected error: %v", fullPlan.Error)
		}
		if got := fullPlan.Assignment.RepresentativeMode(); got != flavorassigner.DeferredFit {
			t.Fatalf("full Plan() mode = %v, want %v", got, flavorassigner.DeferredFit)
		}

		// Second call on the SAME planner with a reduced count (1 CPU): blocker (6) + committed-victim (6) = 12 > 9 in filter,
		// verifying filter.Reset() restored committed-victim's usage cleanly after the first Plan() call.
		reducedCounts := workload.PodSetsCounts{kueue.DefaultPodSetName: 1}
		reducedAsgn := assigner.AssignFlavors(ctx, log, reducedCounts)
		reducedPlan := planner.Plan(ctx, &reducedAsgn, opts...)
		if reducedPlan.Error != nil {
			t.Fatalf("reduced Plan() unexpected error: %v", reducedPlan.Error)
		}
		if got := reducedPlan.Assignment.RepresentativeMode(); got != flavorassigner.DeferredFit {
			t.Fatalf("reduced Plan() mode = %v, want %v (filter must retain committed-victim usage after Reset)", got, flavorassigner.DeferredFit)
		}
	})
}

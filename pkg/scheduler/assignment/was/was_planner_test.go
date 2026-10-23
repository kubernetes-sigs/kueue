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
	schedulingv1alpha3 "k8s.io/api/scheduling/v1alpha3"
	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"
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

type testSimulator struct {
	simulator.SchedulerSimulator

	result                    simulator.SchedulingResult
	calls                     int
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

func (f *testSimulator) ScheduleWorkload(_ context.Context, pods []*corev1.Pod, _ ...simulator.ScheduleOption) simulator.SchedulingResult {
	f.calls++
	f.scheduledPods = append(f.scheduledPods, pods...)
	return f.result
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

func groupNames[T interface{ GetName() string }](groups []T) []string {
	names := make([]string, 0, len(groups))
	for _, group := range groups {
		names = append(names, group.GetName())
	}
	return names
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
		flavorQuotas []kueue.FlavorQuotas
		podSets      []testPodSet
		// initialMode documents the quota-based mode computed by the flavor assigner.
		initialMode flavorassigner.FlavorAssignmentMode
		// When set as Nil, ScheduleWorkload is expected not to be called.
		schedulingResult *simulator.SchedulingResult
		// failAddCPGAt, failAddPGAt and removeErr configure testSimulator failures.
		failAddCPGAt, failAddPGAt int
		removeErr                 error

		wantMode        flavorassigner.FlavorAssignmentMode
		wantFit         bool
		wantErrs        []string
		wantNoFitReason string
		// wantResolvedNoFitReason configures what should be the NoFitReason
		// the Assignemtn resolves to when the UnadmittedWorkloadsObservability
		// FG ius enabled.
		// When set the UnadmittedWorkloadsObservability feature will be enabled in the test.
		wantResolvedNoFitReason string
		wantPodSets             []flavorassigner.PodSetAssignment
		wantAdded               *addedPodGroups
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
		"initial assignment requires preemption: not supported": {
			flavorQuotas: []kueue.FlavorQuotas{cpuQuota(testFlavor, "3")},
			podSets:      []testPodSet{{name: kueue.DefaultPodSetName, count: 1, cpu: "2"}},
			initialMode:  flavorassigner.Preempt,
			wantMode:     flavorassigner.Preempt,
			wantErrs:     []string{"WAS Planner does not support preemptions"},
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
			wantErrs:     []string{"virtualization error: unable to add pod group"},
			wantPodSets:  groupedPodSetsUnchanged,
			wantAdded:    &addedPodGroups{cpgs: 2, pgs: 2},
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
				testSim.result = *tc.schedulingResult
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
			sortGroupNames := cmpopts.SortSlices(func(a, b string) bool { return a < b })
			if diff := cmp.Diff(groupNames(testSim.addedPGs), groupNames(testSim.removedPGs), sortGroupNames, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Plan() removed PodGroups mismatch (-added +removed):\n%s", diff)
			}
			if diff := cmp.Diff(groupNames(testSim.addedCPGs), groupNames(testSim.removedCPGs), sortGroupNames, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Plan() removed CompositePodGroups mismatch (-added +removed):\n%s", diff)
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

	planner := NewPlanner(wl, snapshot)
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

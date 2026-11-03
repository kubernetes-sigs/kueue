//go:build !exclude_scheduler_library

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
	"k8s.io/apimachinery/pkg/types"
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

	victimName      = "victim"
	highBlockerName = "high-blocker"
	incomingName    = "incoming"

	// The incoming workload may preempt workloads with a lower priority only.
	lowPriority      = 0
	incomingPriority = 50
	highPriority     = 100
)

var errSimulator = errors.New("simulator failure")

// recordingSimulator wraps a real simulator.SchedulerSimulator to record
// PodGroup lifecycle calls and ScheduleWorkload invocations, while allowing
// TestPlan_SimulatorErrors to inject explicit simulator failures.
type recordingSimulator struct {
	simulator.SchedulerSimulator

	scheduleCalls int
	scheduledPods []*corev1.Pod
	addedPGs      []*schedulingv1beta1.PodGroup
	removedPGs    []*schedulingv1beta1.PodGroup
	addedCPGs     []*schedulingv1alpha3.CompositePodGroup
	removedCPGs   []*schedulingv1alpha3.CompositePodGroup

	overrideScheduleResult    *simulator.SchedulingResult
	failAddPGAt, failAddCPGAt int
	addPGCalls, addCPGCalls   int
	removeErr                 error
}

var _ simulator.SchedulerSimulator = (*recordingSimulator)(nil)

func (r *recordingSimulator) ScheduleWorkload(ctx context.Context, pods []*corev1.Pod, opts ...simulator.ScheduleOption) simulator.SchedulingResult {
	r.scheduleCalls++
	r.scheduledPods = append(r.scheduledPods, pods...)
	if r.overrideScheduleResult != nil {
		return *r.overrideScheduleResult
	}
	return r.SchedulerSimulator.ScheduleWorkload(ctx, pods, opts...)
}

func (r *recordingSimulator) AddPodGroup(ctx context.Context, pg *schedulingv1beta1.PodGroup) error {
	r.addPGCalls++
	if r.addPGCalls == r.failAddPGAt {
		return errSimulator
	}
	if err := r.SchedulerSimulator.AddPodGroup(ctx, pg); err != nil {
		return err
	}
	r.addedPGs = append(r.addedPGs, pg)
	return nil
}

func (r *recordingSimulator) RemovePodGroup(ctx context.Context, pg *schedulingv1beta1.PodGroup) error {
	r.removedPGs = append(r.removedPGs, pg)
	if r.removeErr != nil {
		return r.removeErr
	}
	return r.SchedulerSimulator.RemovePodGroup(ctx, pg)
}

func (r *recordingSimulator) AddCompositePodGroup(ctx context.Context, cpg *schedulingv1alpha3.CompositePodGroup) error {
	r.addCPGCalls++
	if r.addCPGCalls == r.failAddCPGAt {
		return errSimulator
	}
	if err := r.SchedulerSimulator.AddCompositePodGroup(ctx, cpg); err != nil {
		return err
	}
	r.addedCPGs = append(r.addedCPGs, cpg)
	return nil
}

func (r *recordingSimulator) RemoveCompositePodGroup(ctx context.Context, cpg *schedulingv1alpha3.CompositePodGroup) error {
	r.removedCPGs = append(r.removedCPGs, cpg)
	if r.removeErr != nil {
		return r.removeErr
	}
	return r.SchedulerSimulator.RemoveCompositePodGroup(ctx, cpg)
}

func enableWASFeatureGates(t *testing.T, observability bool) {
	t.Helper()
	features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
		kubefeatures.GenericWorkload:                 true,
		kubefeatures.TopologyAwareWorkloadScheduling: true,
		kubefeatures.CompositePodGroup:               true,
		features.SchedulerLibraryDeepIntegration:     true,
		features.UnadmittedWorkloadsObservability:    observability,
	})
}

func makeTestNode(name, cpus string) *corev1.Node {
	return testingnode.MakeNode(name).
		Label("tas", testFlavor).
		Label(testBlockLabel, testBlock).
		Label(corev1.LabelHostname, name).
		StatusAllocatable(corev1.ResourceList{
			corev1.ResourceCPU:  resource.MustParse(cpus),
			corev1.ResourcePods: resource.MustParse("32"),
		}).
		Ready().
		Obj()
}

func makeAdmittedWorkload(name string, cq kueue.ClusterQueueReference, prio int32, cpu, node string, admitTime time.Time) *kueue.Workload {
	return utiltestingapi.MakeWorkload(name, testNamespace).
		UID(types.UID("uid-"+name)).
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

// buildTestSnapshot builds a scheduler cache Snapshot backed by the real WAS
// SchedulerSimulator, syncing the given nodes, ClusterQueues, and admitted workloads
// (along with their running Pods in podTracker so both TAS and scheduler-library
// observe the same cluster state).
func buildTestSnapshot(
	ctx context.Context,
	t *testing.T,
	log logr.Logger,
	flavors []kueue.ResourceFlavorReference,
	nodes []*corev1.Node,
	cqs []*kueue.ClusterQueue,
	admittedWorkloads []*kueue.Workload,
) *schdcache.Snapshot {
	t.Helper()

	simFactory, err := wascache.NewWASSimulatorFactory(klog.NewContext(ctx, logr.Discard()), nil)
	if err != nil {
		t.Fatalf("initializing WAS simulator factory: %v", err)
	}
	cache := schdcache.New(utiltesting.NewFakeClient(), schdcache.WithSimulatorFactory(simFactory))
	for _, flavor := range flavors {
		cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor(string(flavor)).
			NodeLabel("tas", string(flavor)).TopologyName("topology").Obj())
	}
	cache.AddOrUpdateTopology(log, utiltestingapi.MakeTopology("topology").Levels(testBlockLabel, corev1.LabelHostname).Obj())
	for _, node := range nodes {
		cache.TASCache().SyncNode(node)
	}
	for _, cq := range cqs {
		if err := cache.AddClusterQueue(ctx, cq); err != nil {
			t.Fatalf("adding ClusterQueue %q: %v", cq.Name, err)
		}
	}
	for _, wl := range admittedWorkloads {
		if !cache.AddOrUpdateWorkload(ctx, log, wl) {
			t.Fatalf("adding workload %q to cache", wl.Name)
		}
		for _, pod := range wascache.VirtualPodsForWorkload(wl) {
			cache.TrackPod(ctx, pod)
		}
	}

	snapshot, err := cache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("building snapshot: %v", err)
	}
	return snapshot
}

// newTestSnapshot builds the default single-CQ snapshot with two 8-CPU nodes and
// a lower-priority "victim" workload occupying 2 CPU on node-1, unless custom
// nodes or admittedWorkloads are supplied.
func newTestSnapshot(
	ctx context.Context,
	t *testing.T,
	log logr.Logger,
	nodes []*corev1.Node,
	admittedWorkloads []*kueue.Workload,
	flavorQuotas ...kueue.FlavorQuotas,
) *schdcache.Snapshot {
	t.Helper()

	if nodes == nil {
		nodes = []*corev1.Node{
			makeTestNode(testNode1, testNodeCPUs),
			makeTestNode(testNode2, testNodeCPUs),
		}
	}
	if admittedWorkloads == nil {
		admittedWorkloads = []*kueue.Workload{
			makeAdmittedWorkload(victimName, testCQ, lowPriority, "2", testNode1, time.Now()),
		}
	}
	flavors := make([]kueue.ResourceFlavorReference, 0, len(flavorQuotas))
	for _, fq := range flavorQuotas {
		flavors = append(flavors, fq.Name)
	}
	cq := utiltestingapi.MakeClusterQueue(testCQ).
		Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
		ResourceGroup(flavorQuotas...).
		Obj()
	return buildTestSnapshot(ctx, t, log, flavors, nodes, []*kueue.ClusterQueue{cq}, admittedWorkloads)
}

func buildIncomingWorkload(log logr.Logger, podSets []testPodSet, unhealthyNodes ...string) *workload.Info {
	var apiPodSets []kueue.PodSet
	for _, ps := range podSets {
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
		apiPodSets = append(apiPodSets, *psWrapper.Obj())
	}
	wl := workload.NewInfo(log, utiltestingapi.MakeWorkload(incomingName, testNamespace).
		UID("uid-incoming").
		Priority(incomingPriority).
		UnhealthyNodes(unhealthyNodes...).
		PodSets(apiPodSets...).
		Obj())
	wl.ClusterQueue = testCQ
	return wl
}

// podKey returns the key of the virtual Pod that the planner builds for the
// given replica of the incoming workload's PodSet.
func podKey(psName kueue.PodSetReference, replicaIdx int) client.ObjectKey {
	wl := &kueue.Workload{Name: incomingName, Namespace: testNamespace}
	ps := &kueue.PodSet{Name: psName}
	pods, err := wascache.CandidateVirtualPodsForPodSet(wl, ps, int32(replicaIdx+1), wascache.CandidatePodOptions{})
	if err != nil {
		panic(fmt.Sprintf("building virtual Pods for PodSet %q: %v", psName, err))
	}
	return client.ObjectKeyFromObject(pods[replicaIdx])
}

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

type addedPodGroups struct{ cpgs, pgs int }

// TestPlan runs Plan end-to-end with the real flavor assigner, real Preemptor,
// and real WAS SchedulerSimulator (scheduler-library).
func TestPlan(t *testing.T) {
	now := time.Now()

	cases := map[string]struct {
		flavorQuotas      []kueue.FlavorQuotas
		nodes             []*corev1.Node
		admittedWorkloads []*kueue.Workload
		podSets           []testPodSet
		unhealthyNodes    []string
		initialMode       flavorassigner.FlavorAssignmentMode

		wantMode                flavorassigner.FlavorAssignmentMode
		wantFit                 bool
		wantNoFitReason         string
		wantScheduleCalls       int
		wantPreemptionTargets   []client.ObjectKey
		wantResolvedNoFitReason string
		wantPodSets             []flavorassigner.PodSetAssignment
		wantAdded               *addedPodGroups
	}{
		"initial assignment does not fit the quota: returned as is, without scheduling": {
			flavorQuotas:            []kueue.FlavorQuotas{cpuQuota(testFlavor, "3")},
			podSets:                 []testPodSet{{name: kueue.DefaultPodSetName, count: 1, cpu: "4"}},
			initialMode:             flavorassigner.NoFit,
			wantMode:                flavorassigner.NoFit,
			wantScheduleCalls:       0,
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
		"initial assignment requires preemption and ScheduleWorkload succeeds by evicting victim on node: fits": {
			flavorQuotas:          []kueue.FlavorQuotas{cpuQuota(testFlavor, "3")},
			nodes:                 []*corev1.Node{makeTestNode(testNode1, "3")},
			podSets:               []testPodSet{{name: kueue.DefaultPodSetName, count: 1, cpu: "2"}},
			initialMode:           flavorassigner.Preempt,
			wantMode:              flavorassigner.Preempt,
			wantFit:               true,
			wantScheduleCalls:     1,
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
			flavorQuotas: []kueue.FlavorQuotas{cpuQuota(testFlavor, "4")},
			nodes: []*corev1.Node{
				makeTestNode(testNode1, "2"),
				makeTestNode(testNode2, "4"),
			},
			podSets:               []testPodSet{{name: kueue.DefaultPodSetName, count: 1, cpu: "3"}},
			initialMode:           flavorassigner.Preempt,
			wantMode:              flavorassigner.Preempt,
			wantFit:               true,
			wantScheduleCalls:     1,
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
		"initial assignment requires preemption, ScheduleWorkload fails with candidates due to higher-priority pod on node but succeeds on empty cluster: keeps Preempt mode and reserves topology": {
			flavorQuotas: []kueue.FlavorQuotas{cpuQuota(testFlavor, "9")},
			nodes:        []*corev1.Node{makeTestNode(testNode1, "8")},
			admittedWorkloads: []*kueue.Workload{
				makeAdmittedWorkload(highBlockerName, testCQ, highPriority, "4", testNode1, now),
				makeAdmittedWorkload(victimName, testCQ, lowPriority, "2", testNode1, now),
			},
			podSets:           []testPodSet{{name: kueue.DefaultPodSetName, count: 1, cpu: "5"}},
			initialMode:       flavorassigner.Preempt,
			wantMode:          flavorassigner.Preempt,
			wantFit:           false,
			wantScheduleCalls: 2,
			wantPodSets: []flavorassigner.PodSetAssignment{{
				Name:               kueue.DefaultPodSetName,
				Status:             *flavorassigner.NewStatus("insufficient unused quota for cpu in flavor tas-flavor, 2 more needed"),
				TopologyAssignment: onNodes(domain(testNode1, 1)),
				FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{{
					Flavor:                testFlavor,
					Mode:                  flavorassigner.Preempt,
					PreemptionPossibility: new(policy.Preempt),
				}},
			}},
		},
		"initial assignment requires preemption and ScheduleWorkload fails even on empty cluster: does not fit": {
			flavorQuotas:            []kueue.FlavorQuotas{cpuQuota(testFlavor, "10")},
			podSets:                 []testPodSet{{name: kueue.DefaultPodSetName, count: 1, cpu: "9"}},
			initialMode:             flavorassigner.Preempt,
			wantMode:                flavorassigner.NoFit,
			wantScheduleCalls:       2,
			wantNoFitReason:         kueue.WorkloadQuotaReservedReasonSimulationFailed,
			wantResolvedNoFitReason: kueue.WorkloadQuotaReservedReasonSimulationFailed,
			wantPodSets: []flavorassigner.PodSetAssignment{{
				Name: kueue.DefaultPodSetName,
				Status: *flavorassigner.NewStatus(
					podFailure(kueue.DefaultPodSetName, 0, wascache.FailedReasonSkipped),
				).WithNoFitReason(kueue.WorkloadQuotaReservedReasonSimulationFailed),
				FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{{
					Flavor:                testFlavor,
					Mode:                  flavorassigner.NoFit,
					PreemptionPossibility: new(policy.Preempt),
					NoFitReason:           kueue.WorkloadQuotaReservedReasonSimulationFailed,
				}},
			}},
		},
		"initial assignment fits quota, ScheduleWorkload fails initially but succeeds with preemption: demotes to Preempt and fits": {
			flavorQuotas:          []kueue.FlavorQuotas{cpuQuota(testFlavor, "20")},
			nodes:                 []*corev1.Node{makeTestNode(testNode1, "8")},
			podSets:               []testPodSet{{name: kueue.DefaultPodSetName, count: 1, cpu: "7"}},
			initialMode:           flavorassigner.Fit,
			wantMode:              flavorassigner.Preempt,
			wantFit:               true,
			wantScheduleCalls:     2,
			wantPreemptionTargets: []client.ObjectKey{{Namespace: testNamespace, Name: victimName}},
			wantPodSets: []flavorassigner.PodSetAssignment{{
				Name:               kueue.DefaultPodSetName,
				Status:             *flavorassigner.NewStatus(podFailure(kueue.DefaultPodSetName, 0, wascache.FailedReasonSkipped)),
				TopologyAssignment: onNodes(domain(testNode1, 1)),
				FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{{
					Flavor: testFlavor,
					Mode:   flavorassigner.Fit,
				}},
			}},
		},
		"initial assignment fits quota, ScheduleWorkload fails initially and with candidates but succeeds on empty cluster: demotes to Preempt and reserves topology": {
			flavorQuotas: []kueue.FlavorQuotas{cpuQuota(testFlavor, "20")},
			nodes:        []*corev1.Node{makeTestNode(testNode1, "8")},
			admittedWorkloads: []*kueue.Workload{
				makeAdmittedWorkload(highBlockerName, testCQ, highPriority, "4", testNode1, now),
				makeAdmittedWorkload(victimName, testCQ, lowPriority, "2", testNode1, now),
			},
			podSets:           []testPodSet{{name: kueue.DefaultPodSetName, count: 1, cpu: "6"}},
			initialMode:       flavorassigner.Fit,
			wantMode:          flavorassigner.Preempt,
			wantFit:           false,
			wantScheduleCalls: 3,
			wantPodSets: []flavorassigner.PodSetAssignment{{
				Name:               kueue.DefaultPodSetName,
				Status:             *flavorassigner.NewStatus(podFailure(kueue.DefaultPodSetName, 0, wascache.FailedReasonSkipped)),
				TopologyAssignment: onNodes(domain(testNode1, 1)),
				FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{{
					Flavor: testFlavor,
					Mode:   flavorassigner.Fit,
				}},
			}},
		},
		"initial assignment requires preemption and workload has unhealthy nodes: skips preemption simulation": {
			flavorQuotas:      []kueue.FlavorQuotas{cpuQuota(testFlavor, "3")},
			podSets:           []testPodSet{{name: kueue.DefaultPodSetName, count: 1, cpu: "2"}},
			unhealthyNodes:    []string{testNode1},
			initialMode:       flavorassigner.Preempt,
			wantMode:          flavorassigner.Preempt,
			wantFit:           false,
			wantScheduleCalls: 0,
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
			flavorQuotas:            []kueue.FlavorQuotas{cpuQuota(testFlavor, "20")},
			nodes:                   []*corev1.Node{makeTestNode(testNode1, "8")},
			podSets:                 []testPodSet{{name: kueue.DefaultPodSetName, count: 1, cpu: "7"}},
			unhealthyNodes:          []string{testNode1},
			initialMode:             flavorassigner.Fit,
			wantMode:                flavorassigner.NoFit,
			wantScheduleCalls:       1,
			wantNoFitReason:         kueue.WorkloadQuotaReservedReasonSimulationFailed,
			wantResolvedNoFitReason: kueue.WorkloadQuotaReservedReasonSimulationFailed,
			wantPodSets: []flavorassigner.PodSetAssignment{{
				Name: kueue.DefaultPodSetName,
				Status: *flavorassigner.NewStatus(
					podFailure(kueue.DefaultPodSetName, 0, wascache.FailedReasonSkipped),
				).WithNoFitReason(kueue.WorkloadQuotaReservedReasonSimulationFailed),
				FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{{
					Flavor:      testFlavor,
					Mode:        flavorassigner.NoFit,
					NoFitReason: kueue.WorkloadQuotaReservedReasonSimulationFailed,
				}},
			}},
		},
		"no virtual pods to schedule: initial assignment returned unchanged, without scheduling": {
			flavorQuotas:      []kueue.FlavorQuotas{cpuQuota(testFlavor, "20")},
			podSets:           []testPodSet{{name: kueue.DefaultPodSetName, count: 0, cpu: "1"}},
			initialMode:       flavorassigner.Fit,
			wantMode:          flavorassigner.Fit,
			wantFit:           true,
			wantScheduleCalls: 0,
			wantPodSets: []flavorassigner.PodSetAssignment{{
				Name:                     kueue.DefaultPodSetName,
				FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{{Flavor: testFlavor, Mode: flavorassigner.Fit}},
			}},
		},
		"all pods placed on a single node: fits": {
			flavorQuotas:      []kueue.FlavorQuotas{cpuQuota(testFlavor, "20")},
			podSets:           []testPodSet{{name: kueue.DefaultPodSetName, count: 2, cpu: "4"}},
			initialMode:       flavorassigner.Fit,
			wantMode:          flavorassigner.Fit,
			wantFit:           true,
			wantScheduleCalls: 1,
			wantPodSets: []flavorassigner.PodSetAssignment{{
				Name:                     kueue.DefaultPodSetName,
				TopologyAssignment:       onNodes(domain(testNode2, 2)),
				FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{{Flavor: testFlavor, Mode: flavorassigner.Fit}},
			}},
		},
		"pods spread across nodes: fits, with a domain per node": {
			flavorQuotas:      []kueue.FlavorQuotas{cpuQuota(testFlavor, "20")},
			podSets:           []testPodSet{{name: kueue.DefaultPodSetName, count: 3, cpu: "4", requiredLevel: testBlockLabel}},
			initialMode:       flavorassigner.Fit,
			wantMode:          flavorassigner.Fit,
			wantFit:           true,
			wantScheduleCalls: 1,
			wantPodSets: []flavorassigner.PodSetAssignment{{
				Name:                     kueue.DefaultPodSetName,
				TopologyAssignment:       onNodes(domain(testNode1, 1), domain(testNode2, 2)),
				FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{{Flavor: testFlavor, Mode: flavorassigner.Fit}},
			}},
		},
		"a pod fails to be scheduled on the required hostname domain: the whole PodSet does not fit": {
			flavorQuotas:            []kueue.FlavorQuotas{cpuQuota(testFlavor, "20")},
			podSets:                 []testPodSet{{name: kueue.DefaultPodSetName, count: 2, cpu: "5"}},
			initialMode:             flavorassigner.Fit,
			wantMode:                flavorassigner.NoFit,
			wantScheduleCalls:       3,
			wantNoFitReason:         kueue.WorkloadQuotaReservedReasonSimulationFailed,
			wantResolvedNoFitReason: kueue.WorkloadQuotaReservedReasonSimulationFailed,
			wantPodSets: []flavorassigner.PodSetAssignment{{
				Name: kueue.DefaultPodSetName,
				Status: *flavorassigner.NewStatus(
					podFailure(kueue.DefaultPodSetName, 0, wascache.FailedReasonSkipped),
					podFailure(kueue.DefaultPodSetName, 1, wascache.FailedReasonSkipped),
				).WithNoFitReason(kueue.WorkloadQuotaReservedReasonSimulationFailed),
				FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{
					{Flavor: testFlavor, Mode: flavorassigner.NoFit, NoFitReason: kueue.WorkloadQuotaReservedReasonSimulationFailed},
				},
			}},
		},
		"multiple PodSets, all pods placed: fits, with a topology assignment per PodSet": {
			flavorQuotas: []kueue.FlavorQuotas{cpuQuota(testFlavor, "20")},
			podSets: []testPodSet{
				{name: "leader", count: 1, cpu: "7"},
				{name: "workers", count: 2, cpu: "2"},
			},
			initialMode:       flavorassigner.Fit,
			wantMode:          flavorassigner.Fit,
			wantFit:           true,
			wantScheduleCalls: 1,
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
		"multiple PodSets, one fails: the CompositePodGroup gang fails and the workload does not fit": {
			flavorQuotas: []kueue.FlavorQuotas{cpuQuota(testFlavor, "20")},
			podSets: []testPodSet{
				{name: "leader", count: 1, cpu: "1"},
				{name: "workers", count: 2, cpu: "5"},
			},
			initialMode:             flavorassigner.Fit,
			wantMode:                flavorassigner.NoFit,
			wantScheduleCalls:       3,
			wantNoFitReason:         kueue.WorkloadQuotaReservedReasonSimulationFailed,
			wantResolvedNoFitReason: kueue.WorkloadQuotaReservedReasonSimulationFailed,
			wantPodSets: []flavorassigner.PodSetAssignment{
				{
					Name: "leader",
					Status: *flavorassigner.NewStatus(
						podFailure("leader", 0, wascache.FailedReasonSkipped),
					).WithNoFitReason(kueue.WorkloadQuotaReservedReasonSimulationFailed),
					FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{
						{Flavor: testFlavor, Mode: flavorassigner.NoFit, NoFitReason: kueue.WorkloadQuotaReservedReasonSimulationFailed},
					},
				},
				{
					Name: "workers",
					Status: *flavorassigner.NewStatus(
						podFailure("workers", 0, wascache.FailedReasonSkipped),
						podFailure("workers", 1, wascache.FailedReasonSkipped),
					).WithNoFitReason(kueue.WorkloadQuotaReservedReasonSimulationFailed),
					FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{
						{Flavor: testFlavor, Mode: flavorassigner.NoFit, NoFitReason: kueue.WorkloadQuotaReservedReasonSimulationFailed},
					},
				},
			},
		},
		"all pods exceed node capacity: does not fit": {
			flavorQuotas:            []kueue.FlavorQuotas{cpuQuota(testFlavor, "20")},
			podSets:                 []testPodSet{{name: kueue.DefaultPodSetName, count: 2, cpu: "9"}},
			initialMode:             flavorassigner.Fit,
			wantMode:                flavorassigner.NoFit,
			wantScheduleCalls:       3,
			wantNoFitReason:         kueue.WorkloadQuotaReservedReasonSimulationFailed,
			wantResolvedNoFitReason: kueue.WorkloadQuotaReservedReasonSimulationFailed,
			wantPodSets: []flavorassigner.PodSetAssignment{{
				Name: kueue.DefaultPodSetName,
				Status: *flavorassigner.NewStatus(
					podFailure(kueue.DefaultPodSetName, 0, wascache.FailedReasonSkipped),
					podFailure(kueue.DefaultPodSetName, 1, wascache.FailedReasonSkipped),
				).WithNoFitReason(kueue.WorkloadQuotaReservedReasonSimulationFailed),
				FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{
					{Flavor: testFlavor, Mode: flavorassigner.NoFit, NoFitReason: kueue.WorkloadQuotaReservedReasonSimulationFailed},
				},
			}},
		},
		"multiple flavors, simulation fails on the selected flavor: resolved as SimulationFailed": {
			flavorQuotas:            []kueue.FlavorQuotas{cpuQuota(testNoFitFlavor, "1"), cpuQuota(testFlavor, "20")},
			podSets:                 []testPodSet{{name: kueue.DefaultPodSetName, count: 2, cpu: "5"}},
			initialMode:             flavorassigner.Fit,
			wantMode:                flavorassigner.NoFit,
			wantScheduleCalls:       3,
			wantNoFitReason:         kueue.WorkloadQuotaReservedReasonSimulationFailed,
			wantResolvedNoFitReason: kueue.WorkloadQuotaReservedReasonSimulationFailed,
			wantPodSets: []flavorassigner.PodSetAssignment{{
				Name: kueue.DefaultPodSetName,
				Status: *flavorassigner.NewStatus(
					podFailure(kueue.DefaultPodSetName, 0, wascache.FailedReasonSkipped),
					podFailure(kueue.DefaultPodSetName, 1, wascache.FailedReasonSkipped),
				).WithNoFitReason(kueue.WorkloadQuotaReservedReasonSimulationFailed),
				FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{
					{Flavor: testNoFitFlavor, Mode: flavorassigner.NoFit, NoFitReason: kueue.WorkloadQuotaReservedReasonExceedsMaxQuota},
					{Flavor: testFlavor, Mode: flavorassigner.NoFit, NoFitReason: kueue.WorkloadQuotaReservedReasonSimulationFailed},
				},
			}},
		},
		"a pod fails to be scheduled with observability disabled: flavor attempts are not marked": {
			flavorQuotas:      []kueue.FlavorQuotas{cpuQuota(testFlavor, "20")},
			podSets:           []testPodSet{{name: kueue.DefaultPodSetName, count: 2, cpu: "5"}},
			initialMode:       flavorassigner.Fit,
			wantMode:          flavorassigner.NoFit,
			wantScheduleCalls: 3,
			wantNoFitReason:   kueue.WorkloadQuotaReservedReasonSimulationFailed,
			wantPodSets: []flavorassigner.PodSetAssignment{{
				Name: kueue.DefaultPodSetName,
				Status: *flavorassigner.NewStatus(
					podFailure(kueue.DefaultPodSetName, 0, wascache.FailedReasonSkipped),
					podFailure(kueue.DefaultPodSetName, 1, wascache.FailedReasonSkipped),
				).WithNoFitReason(kueue.WorkloadQuotaReservedReasonSimulationFailed),
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
			enableWASFeatureGates(t, observability)

			log := testr.New(t)
			ctx := ctrllog.IntoContext(t.Context(), log)
			snapshot := newTestSnapshot(ctx, t, log, tc.nodes, tc.admittedWorkloads, tc.flavorQuotas...)
			recSim := &recordingSimulator{SchedulerSimulator: snapshot.SchedulerSimulator}
			snapshot.SchedulerSimulator = recSim

			wl := buildIncomingWorkload(log, tc.podSets, tc.unhealthyNodes...)
			preemptor := preemption.New(
				utiltesting.NewFakeClient(), workload.Ordering{}, &utiltesting.EventRecorder{}, nil, false,
				clocktesting.NewFakeClock(now), nil, preemptexpectations.New(), nil,
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

			planner := NewPlanner(wl, snapshot, preemptor)
			gotPlan := planner.Plan(ctx, &initialAssignment)

			if gotPlan.Error != nil {
				t.Fatalf("Plan() returned unexpected error: %v", gotPlan.Error)
			}
			if gotPlan.Assignment != &initialAssignment {
				t.Errorf("Plan() returned a different assignment object than the initial one")
			}
			if recSim.scheduleCalls != tc.wantScheduleCalls {
				t.Errorf("Plan() called ScheduleWorkload unexpected number of times: want %d, got %d", tc.wantScheduleCalls, recSim.scheduleCalls)
			}
			if tc.wantAdded != nil {
				gotAdded := addedPodGroups{cpgs: len(recSim.addedCPGs), pgs: len(recSim.addedPGs)}
				if diff := cmp.Diff(*tc.wantAdded, gotAdded, cmp.AllowUnexported(addedPodGroups{})); diff != "" {
					t.Errorf("Plan() added group mismatch (-want +got):\n%s", diff)
				}
			}
			if diff := cmp.Diff(recSim.addedPGs, recSim.removedPGs); diff != "" {
				t.Errorf("Plan() removed PodGroups mismatch (-added +removed):\n%s", diff)
			}
			if diff := cmp.Diff(recSim.addedCPGs, recSim.removedCPGs); diff != "" {
				t.Errorf("Plan() removed CompositePodGroups mismatch (-added +removed):\n%s", diff)
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
	enableWASFeatureGates(t, false)

	log := testr.New(t)
	ctx := ctrllog.IntoContext(t.Context(), log)
	snapshot := newTestSnapshot(ctx, t, log, nil, nil, cpuQuota(testFlavor, "20"))
	recSim := &recordingSimulator{SchedulerSimulator: snapshot.SchedulerSimulator}
	snapshot.SchedulerSimulator = recSim

	wl := buildIncomingWorkload(log, []testPodSet{
		{name: "ps1", count: 2, cpu: "1", podSetGroup: "group1"},
		{name: "ps2", count: 2, cpu: "1", podSetGroup: "group1"},
	})

	preemptor := preemption.New(
		utiltesting.NewFakeClient(), workload.Ordering{}, &utiltesting.EventRecorder{}, nil, false,
		clocktesting.NewFakeClock(time.Now()), nil, preemptexpectations.New(), nil,
	)
	assigner := flavorassigner.New(
		wl, snapshot.ClusterQueue(testCQ), snapshot.ResourceFlavors, false,
		preemption.NewOracle(preemptor, snapshot), nil,
		configapi.QuotaCheckBlockUndeclared, resources.NewResourceFormatter(), 0,
	)
	assignment := assigner.AssignFlavors(ctx, log, nil)

	planner := NewPlanner(wl, snapshot, preemptor)
	plan := planner.Plan(ctx, &assignment)
	if plan.Error != nil {
		t.Fatalf("Plan() unexpected error: %v", plan.Error)
	}
	if !plan.CanFit() {
		t.Fatalf("Plan() expected grouped workload to fit on real simulator")
	}

	if len(recSim.addedCPGs) != 1 {
		t.Errorf("expected 1 CompositePodGroup added, got %d", len(recSim.addedCPGs))
	}
	if diff := cmp.Diff(recSim.addedCPGs, recSim.removedCPGs); diff != "" {
		t.Errorf("removed CompositePodGroups mismatch (-added +removed):\n%s", diff)
	}
	if len(recSim.addedPGs) != 2 {
		t.Errorf("expected 2 PodGroups added, got %d", len(recSim.addedPGs))
	}
	if diff := cmp.Diff(recSim.addedPGs, recSim.removedPGs); diff != "" {
		t.Errorf("removed PodGroups mismatch (-added +removed):\n%s", diff)
	}

	if len(recSim.scheduledPods) != 4 {
		t.Fatalf("expected 4 scheduled pods, got %d", len(recSim.scheduledPods))
	}
	for _, pod := range recSim.scheduledPods {
		if pod.Spec.SchedulingGroup == nil || pod.Spec.SchedulingGroup.PodGroupName == nil {
			t.Errorf("pod %s has no SchedulingGroup.PodGroupName", pod.Name)
		}
	}
}

func TestPlan_PreemptionStrategies(t *testing.T) {
	now := time.Now()
	cases := map[string]struct {
		enableFairSharing     bool
		nodes                 []*corev1.Node
		cqs                   []*kueue.ClusterQueue
		admittedWorkloads     []*kueue.Workload
		incomingCPU           string
		wantScheduleCalls     int
		wantPreemptionTargets []client.ObjectKey
		wantTopology          *tas.TopologyAssignment
		wantMode              flavorassigner.FlavorAssignmentMode
		wantFit               bool
	}{
		"classical preemption with BorrowWithinCohort: tries borrowing strategy first, falls back to non-borrowing strategy": {
			enableFairSharing: false,
			nodes: []*corev1.Node{
				makeTestNode(testNode1, "3"),
				makeTestNode(testNode2, "4"),
			},
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
			incomingCPU:       "4",
			wantScheduleCalls: 2,
			wantPreemptionTargets: []client.ObjectKey{
				{Namespace: testNamespace, Name: "victim-cohort"},
				{Namespace: testNamespace, Name: "victim-low"},
			},
			wantTopology: onNodes(domain(testNode2, 1)),
			wantMode:     flavorassigner.Preempt,
			wantFit:      true,
		},
		"fair sharing preemption: lazily evaluates cohort candidates and stops once workload fits": {
			enableFairSharing: true,
			nodes: []*corev1.Node{
				makeTestNode(testNode1, "2"),
				makeTestNode(testNode2, "2"),
			},
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
			incomingCPU:       "2",
			wantScheduleCalls: 1,
			wantPreemptionTargets: []client.ObjectKey{
				{Namespace: testNamespace, Name: "borrower-low"},
			},
			wantTopology: onNodes(domain(testNode1, 1)),
			wantMode:     flavorassigner.Preempt,
			wantFit:      true,
		},
		"node-level victims do not free enough quota: selects additional strategy candidates to satisfy quota": {
			enableFairSharing: true,
			nodes: []*corev1.Node{
				makeTestNode(testNode1, "4"),
				makeTestNode(testNode2, "4"),
			},
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
			incomingCPU:       "4",
			wantScheduleCalls: 1,
			wantPreemptionTargets: []client.ObjectKey{
				{Namespace: testNamespace, Name: "borrower-low"},
				{Namespace: testNamespace, Name: "borrower-mid"},
			},
			wantTopology: onNodes(domain(testNode1, 1)),
			wantMode:     flavorassigner.Preempt,
			wantFit:      true,
		},
		"fair sharing preemption: keeps Preempt mode without targets when reprieval causes strategy verification to fail": {
			enableFairSharing: true,
			nodes: []*corev1.Node{
				makeTestNode(testNode1, "2"),
				makeTestNode(testNode2, testNodeCPUs),
			},
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
			incomingCPU:           "4",
			wantScheduleCalls:     2,
			wantPreemptionTargets: nil,
			wantTopology:          onNodes(domain(testNode2, 1)),
			wantMode:              flavorassigner.Preempt,
			wantFit:               false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			enableWASFeatureGates(t, false)

			log := testr.New(t)
			ctx := ctrllog.IntoContext(t.Context(), log)

			snapshot := buildTestSnapshot(
				ctx, t, log,
				[]kueue.ResourceFlavorReference{testFlavor},
				tc.nodes, tc.cqs, tc.admittedWorkloads,
			)
			recSim := &recordingSimulator{SchedulerSimulator: snapshot.SchedulerSimulator}
			snapshot.SchedulerSimulator = recSim

			wl := buildIncomingWorkload(log, []testPodSet{{name: kueue.DefaultPodSetName, count: 1, cpu: tc.incomingCPU}})

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
			if recSim.scheduleCalls != tc.wantScheduleCalls {
				t.Errorf("Plan() called ScheduleWorkload %d times, want %d", recSim.scheduleCalls, tc.wantScheduleCalls)
			}
			if gotPlan.CanFit() != tc.wantFit {
				t.Errorf("Plan() CanFit() = %v, want %v", gotPlan.CanFit(), tc.wantFit)
			}
			var gotTargets []client.ObjectKey
			for _, target := range gotPlan.PreemptionTargets {
				gotTargets = append(gotTargets, client.ObjectKeyFromObject(target.WorkloadInfo.Obj))
			}
			if diff := cmp.Diff(tc.wantPreemptionTargets, gotTargets); diff != "" {
				t.Errorf("Plan() preemption targets mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantTopology, gotPlan.Assignment.PodSets[0].TopologyAssignment); diff != "" {
				t.Errorf("Plan() TopologyAssignment mismatch (-want +got):\n%s", diff)
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

// TestPlan_SimulatorErrors verifies defensive error handling, rollback cleanup,
// and partial/error PodPlacement isolation using targeted simulator overrides.
func TestPlan_SimulatorErrors(t *testing.T) {
	errSchedule := errors.New("schedule library failure")
	errPlacement := errors.New("pod placement error")

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
		flavorQuotas           []kueue.FlavorQuotas
		podSets                []testPodSet
		initialMode            flavorassigner.FlavorAssignmentMode
		overrideScheduleResult *simulator.SchedulingResult
		failAddCPGAt           int
		failAddPGAt            int
		removeErr              error

		wantMode                flavorassigner.FlavorAssignmentMode
		wantErrs                []string
		wantNoFitReason         string
		wantResolvedNoFitReason string
		wantPodSets             []flavorassigner.PodSetAssignment
		wantAdded               *addedPodGroups
	}{
		"ScheduleWorkload fails completely: planning error": {
			flavorQuotas:           []kueue.FlavorQuotas{cpuQuota(testFlavor, "20")},
			podSets:                []testPodSet{{name: kueue.DefaultPodSetName, count: 2, cpu: "1"}},
			initialMode:            flavorassigner.Fit,
			overrideScheduleResult: &simulator.SchedulingResult{Error: errSchedule},
			wantMode:               flavorassigner.Fit,
			wantErrs:               []string{"failed to schedule workload: " + errSchedule.Error()},
			wantPodSets: []flavorassigner.PodSetAssignment{{
				Name:                     kueue.DefaultPodSetName,
				FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{{Flavor: testFlavor, Mode: flavorassigner.Fit}},
			}},
		},
		"initial assignment requires preemption and ScheduleWorkload returns unknown victim: planning error": {
			flavorQuotas: []kueue.FlavorQuotas{cpuQuota(testFlavor, "3")},
			podSets:      []testPodSet{{name: kueue.DefaultPodSetName, count: 1, cpu: "2"}},
			initialMode:  flavorassigner.Preempt,
			overrideScheduleResult: &simulator.SchedulingResult{
				PodPlacements: simulator.PodPlacements{
					podKey(kueue.DefaultPodSetName, 0): {Node: testNode1},
				},
				PreemptionVictims: []client.ObjectKey{{Namespace: testNamespace, Name: "unknown-victim"}},
			},
			wantMode: flavorassigner.Preempt,
			wantErrs: []string{`preempted workload "default/unknown-victim" is not a candidate in this preemption strategy`},
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
		"a pod placement errors out: the whole PodSet does not fit and reports the error": {
			flavorQuotas: []kueue.FlavorQuotas{cpuQuota(testFlavor, "20")},
			podSets:      []testPodSet{{name: kueue.DefaultPodSetName, count: 2, cpu: "1"}},
			initialMode:  flavorassigner.Fit,
			overrideScheduleResult: &simulator.SchedulingResult{PodPlacements: simulator.PodPlacements{
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
		"multiple PodSets, partial placement failure on one PodSet: only failed PodSet attempts are marked": {
			flavorQuotas: []kueue.FlavorQuotas{cpuQuota(testFlavor, "20")},
			podSets: []testPodSet{
				{name: "leader", count: 1, cpu: "1"},
				{name: "workers", count: 2, cpu: "1"},
			},
			initialMode: flavorassigner.Fit,
			overrideScheduleResult: &simulator.SchedulingResult{PodPlacements: simulator.PodPlacements{
				podKey("leader", 0):  {Node: testNode2},
				podKey("workers", 0): {Node: testNode1},
				podKey("workers", 1): {Error: errPlacement, Reasons: []string{"plugin failed"}},
			}},
			wantMode:                flavorassigner.NoFit,
			wantNoFitReason:         kueue.WorkloadQuotaReservedReasonSimulationFailed,
			wantResolvedNoFitReason: kueue.WorkloadQuotaReservedReasonSimulationFailed,
			wantPodSets: []flavorassigner.PodSetAssignment{
				{
					Name:                     "leader",
					FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{{Flavor: testFlavor, Mode: flavorassigner.Fit}},
				},
				{
					Name: "workers",
					Status: *flavorassigner.NewStatus(
						podFailure("workers", 1, "plugin failed"),
					).WithError(errPlacement).WithNoFitReason(kueue.WorkloadQuotaReservedReasonSimulationFailed),
					FlavorAssignmentAttempts: []flavorassigner.FlavorAssignmentAttempt{
						{Flavor: testFlavor, Mode: flavorassigner.NoFit, NoFitReason: kueue.WorkloadQuotaReservedReasonSimulationFailed},
					},
				},
			},
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
				{name: "leader", count: 1, cpu: "7"},
				{name: "workers", count: 2, cpu: "2"},
			},
			initialMode: flavorassigner.Fit,
			removeErr:   errSimulator,
			wantMode:    flavorassigner.Fit,
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
		t.Run(name, func(t *testing.T) {
			enableWASFeatureGates(t, observability)

			log := testr.New(t)
			ctx := ctrllog.IntoContext(t.Context(), log)
			snapshot := newTestSnapshot(ctx, t, log, nil, nil, tc.flavorQuotas...)
			recSim := &recordingSimulator{
				SchedulerSimulator:     snapshot.SchedulerSimulator,
				overrideScheduleResult: tc.overrideScheduleResult,
				failAddCPGAt:           tc.failAddCPGAt,
				failAddPGAt:            tc.failAddPGAt,
				removeErr:              tc.removeErr,
			}
			snapshot.SchedulerSimulator = recSim

			wl := buildIncomingWorkload(log, tc.podSets)
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

			planner := NewPlanner(wl, snapshot, preemptor)
			gotPlan := planner.Plan(ctx, &initialAssignment)

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
			if tc.overrideScheduleResult != nil && tc.overrideScheduleResult.Error != nil && !errors.Is(gotPlan.Error, tc.overrideScheduleResult.Error) {
				t.Errorf("Plan() error does not wrap the ScheduleWorkload error: want %v, got %v", tc.overrideScheduleResult.Error, gotPlan.Error)
			}
			if (tc.failAddCPGAt != 0 || tc.failAddPGAt != 0 || tc.removeErr != nil) && !errors.Is(gotPlan.Error, errSimulator) {
				t.Errorf("Plan() error does not wrap the simulator error: want %v, got %v", errSimulator, gotPlan.Error)
			}
			if tc.wantAdded != nil {
				gotAdded := addedPodGroups{cpgs: len(recSim.addedCPGs), pgs: len(recSim.addedPGs)}
				if diff := cmp.Diff(*tc.wantAdded, gotAdded, cmp.AllowUnexported(addedPodGroups{})); diff != "" {
					t.Errorf("Plan() added group mismatch (-want +got):\n%s", diff)
				}
			}
			if diff := cmp.Diff(recSim.addedPGs, recSim.removedPGs); diff != "" {
				t.Errorf("Plan() removed PodGroups mismatch (-added +removed):\n%s", diff)
			}
			if diff := cmp.Diff(recSim.addedCPGs, recSim.removedCPGs); diff != "" {
				t.Errorf("Plan() removed CompositePodGroups mismatch (-added +removed):\n%s", diff)
			}
			if gotMode := gotPlan.Assignment.RepresentativeMode(); gotMode != tc.wantMode {
				t.Errorf("Plan() assignment mode mismatch: want %v, got %v", tc.wantMode, gotMode)
			}
			if gotPlan.Assignment.NoFitReason != tc.wantNoFitReason {
				t.Errorf("Plan() assignment NoFitReason mismatch: want %q, got %q", tc.wantNoFitReason, gotPlan.Assignment.NoFitReason)
			}
			if diff := cmp.Diff(tc.wantPodSets, gotPlan.Assignment.PodSets, podSetAssignmentCmpOpts); diff != "" {
				t.Errorf("Plan() PodSet assignments mismatch (-want +got):\n%s", diff)
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

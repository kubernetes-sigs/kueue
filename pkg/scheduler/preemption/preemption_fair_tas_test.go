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

package preemption

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
	clocktesting "k8s.io/utils/clock/testing"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	utilslices "sigs.k8s.io/kueue/pkg/util/slices"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
)

const tasGPU = corev1.ResourceName("nvidia.com/gpu")

// TestFairPreemptionsForTASWholeNode reproduces a full 6-node GPU cluster where
// the only way to admit a whole-node workload is to clear one node completely.
//
// Every victim is an interchangeable 1-GPU workload, so CandidatesOrdering alone
// picks them in an order uncorrelated with their node and never frees one --
// kubernetes-sigs/kueue#10497.
func TestFairPreemptionsForTASWholeNode(t *testing.T) {
	now := time.Now().Truncate(time.Second)

	topology := utiltestingapi.MakeDefaultOneLevelTopology("tas-single-level")
	flavors := []*kueue.ResourceFlavor{
		utiltestingapi.MakeResourceFlavor("tas-default").
			NodeLabel("tas-node", "true").
			TopologyName("tas-single-level").
			Obj(),
	}

	// Each node is full: 8 GPUs, every GPU held by a 1-GPU workload. Only node-c
	// and node-e are occupied entirely by the borrowing tenant, so they are the
	// only two that preemption can ever clear.
	layout := []struct {
		node               string
		playground, incyte int
	}{
		{"node-a", 6, 2},
		{"node-b", 7, 1},
		{"node-c", 8, 0},
		{"node-d", 4, 4},
		{"node-e", 8, 0},
		{"node-f", 7, 1},
	}

	nodes := make([]corev1.Node, 0, len(layout))
	admitted := make([]kueue.Workload, 0, 48)
	for _, l := range layout {
		nodes = append(nodes, *testingnode.MakeNode(l.node).
			Label("tas-node", "true").
			Label(corev1.LabelHostname, l.node).
			StatusAllocatable(corev1.ResourceList{
				tasGPU:              resource.MustParse("8"),
				corev1.ResourcePods: resource.MustParse("20"),
			}).
			Ready().
			Obj())
		for i := range l.playground {
			admitted = append(admitted, tasGPUWorkload(
				fmt.Sprintf("playground-%s-%d", l.node, i), "env-playground", l.node, topology, 29, now))
		}
		for i := range l.incyte {
			admitted = append(admitted, tasGPUWorkload(
				fmt.Sprintf("incyte-%s-%d", l.node, i), "env-incyte", l.node, topology, 50, now))
		}
	}

	// playground borrows 16 over its nominal 24; incyte sits under its nominal 16
	// and so is not reclaimable at all; gilead's own 8 are entirely free.
	clusterQueues := []*kueue.ClusterQueue{
		tasGPUClusterQueue("env-playground", 24),
		tasGPUClusterQueue("env-incyte", 16),
		tasGPUClusterQueue("env-gilead", 8),
	}
	cohorts := []*kueue.Cohort{utiltestingapi.MakeCohort("shared").Obj()}

	// The trainer needs all 8 GPUs of a single node.
	incoming := utiltestingapi.MakeWorkload("trainer", "default").
		Priority(99).
		PodSets(*utiltestingapi.MakePodSet("trainers", 1).
			Request(tasGPU, "8").
			PreferredTopologyRequest(corev1.LabelHostname).
			Obj()).
		Obj()

	// node-c and node-e are equally good; the ordering breaks the tie on domain ID.
	wantPreempted := sets.New[string]()
	for i := range 8 {
		wantPreempted.Insert(targetKeyReason(
			workload.NewReference("default", fmt.Sprintf("playground-node-c-%d", i)),
			kueue.InCohortReclamationReason))
	}

	ctx, log := utiltesting.ContextWithLog(t)
	// Set name as UID so that candidates sorting is predictable.
	for i := range admitted {
		admitted[i].UID = types.UID(admitted[i].Name)
	}
	cl := utiltesting.NewClientBuilder().
		WithLists(&kueue.WorkloadList{Items: admitted}).
		Build()
	cqCache := schdcache.New(cl)
	for i := range nodes {
		cqCache.TASCache().SyncNode(&nodes[i])
	}
	cqCache.AddOrUpdateTopology(log, topology)
	for _, flv := range flavors {
		cqCache.AddOrUpdateResourceFlavor(log, flv)
	}
	for _, cohort := range cohorts {
		if err := cqCache.AddOrUpdateCohort(cohort); err != nil {
			t.Fatalf("Couldn't add Cohort to cache: %v", err)
		}
	}
	for _, cq := range clusterQueues {
		if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
			t.Fatalf("Couldn't add ClusterQueue to cache: %v", err)
		}
	}

	broadcaster := record.NewBroadcaster()
	recorder := broadcaster.NewRecorder(runtime.NewScheme(), corev1.EventSource{Component: constants.AdmissionName})
	preemptor := New(cl, workload.Ordering{}, recorder, &config.FairSharing{},
		false, clocktesting.NewFakeClock(now), nil, preemptexpectations.New(), nil)

	beforeSnapshot, err := cqCache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("unexpected error while building snapshot: %v", err)
	}
	snapshotWorkingCopy, err := cqCache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("unexpected error while building snapshot: %v", err)
	}
	beforeTASCapacity := tasFreeCapacity(t, beforeSnapshot)

	wlInfo := workload.NewInfo(incoming)
	wlInfo.ClusterQueue = "env-gilead"
	assignment := flavorassigner.Assignment{
		PodSets: []flavorassigner.PodSetAssignment{{
			Name:  "trainers",
			Count: 1,
			Flavors: flavorassigner.ResourceAssignment{
				tasGPU: &flavorassigner.FlavorAssignment{Name: "tas-default", Mode: flavorassigner.Preempt},
			},
		}},
	}
	targets := preemptor.GetTargets(log, *wlInfo, assignment, snapshotWorkingCopy)

	gotTargets := sets.New(utilslices.Map(targets, func(t **Target) string {
		return targetKeyReason(workload.Key((*t).WorkloadInfo.Obj), (*t).Reason)
	})...)
	if diff := cmp.Diff(wantPreempted, gotTargets, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("Issued preemptions (-want,+got):\n%s", diff)
	}
	// TASFlavorSnapshot has no exported fields, so its restoration is asserted
	// through the free capacity it reports rather than by diffing it.
	tasSnapCmpOpts := append(cmp.Options{cmpopts.IgnoreUnexported(schdcache.TASFlavorSnapshot{})}, snapCmpOpts...)
	if diff := cmp.Diff(beforeSnapshot, snapshotWorkingCopy, tasSnapCmpOpts); diff != "" {
		t.Errorf("Snapshot was modified (-initial,+end):\n%s", diff)
	}
	if diff := cmp.Diff(beforeTASCapacity, tasFreeCapacity(t, snapshotWorkingCopy)); diff != "" {
		t.Errorf("TAS free capacity was modified (-initial,+end):\n%s", diff)
	}
}

// TestFairPreemptionsForTASAcrossClusterQueues covers a whole-node workload whose
// only clearable node holds victims from three different ClusterQueues.
//
// Fair sharing walks ClusterQueues by share, not by node, so the highest-share
// queue spends its entire borrowing budget across several nodes and completes
// none of them. Only a domain-scoped retry converges on the one node that works.
func TestFairPreemptionsForTASAcrossClusterQueues(t *testing.T) {
	now := time.Now().Truncate(time.Second)

	topology := utiltestingapi.MakeDefaultOneLevelTopology("tas-single-level")
	flavor := utiltestingapi.MakeResourceFlavor("tas-default").
		NodeLabel("tas-node", "true").
		TopologyName("tas-single-level").
		Obj()

	// Every node is full and every node carries at least one env-incyte pod, so
	// node-b is the only one clearable: incyte holds just 1 GPU there, which is
	// exactly what incyte is borrowing over its nominal 16.
	layout := []struct {
		node                       string
		incyte, playground, gilead int
	}{
		{"node-a", 4, 4, 0},
		{"node-b", 1, 6, 1},
		{"node-c", 3, 5, 0},
		{"node-d", 2, 6, 0},
		{"node-e", 2, 5, 1},
		{"node-f", 5, 3, 0},
	}

	nodes := make([]corev1.Node, 0, len(layout))
	admitted := make([]kueue.Workload, 0, 48)
	for _, l := range layout {
		nodes = append(nodes, *testingnode.MakeNode(l.node).
			Label("tas-node", "true").
			Label(corev1.LabelHostname, l.node).
			StatusAllocatable(corev1.ResourceList{
				tasGPU:              resource.MustParse("8"),
				corev1.ResourcePods: resource.MustParse("20"),
			}).
			Ready().
			Obj())
		for i := range l.incyte {
			admitted = append(admitted, tasGPUWorkload(
				fmt.Sprintf("incyte-%s-%d", l.node, i), "env-incyte", l.node, topology, 1050, now))
		}
		for i := range l.playground {
			admitted = append(admitted, tasGPUWorkload(
				fmt.Sprintf("playground-%s-%d", l.node, i), "env-playground", l.node, topology, 1029, now))
		}
		for i := range l.gilead {
			admitted = append(admitted, tasGPUWorkload(
				fmt.Sprintf("gilead-%s-%d", l.node, i), "env-gilead", l.node, topology, 1010, now))
		}
	}
	for i := range admitted {
		admitted[i].UID = types.UID(admitted[i].Name)
	}

	// playground borrows 13 over its nominal 16 at weight 4; incyte borrows 1.
	// gilead stays within its nominal 16 once the incoming workload is added.
	clusterQueues := []*kueue.ClusterQueue{
		fsGPUClusterQueue("env-playground", 16, "4"),
		fsGPUClusterQueue("env-incyte", 16, "1"),
		fsGPUClusterQueue("env-gilead", 16, "1"),
	}

	incoming := utiltestingapi.MakeWorkload("trainer", "default").
		Priority(1099).
		PodSets(*utiltestingapi.MakePodSet("trainers", 1).
			Request(tasGPU, "8").
			PreferredTopologyRequest(corev1.LabelHostname).
			Obj()).
		Obj()

	wantPreempted := sets.New[string]()
	for i := range 6 {
		wantPreempted.Insert(targetKeyReason(
			workload.NewReference("default", fmt.Sprintf("playground-node-b-%d", i)),
			kueue.InCohortReclamationReason))
	}
	wantPreempted.Insert(targetKeyReason(
		workload.NewReference("default", "incyte-node-b-0"), kueue.InCohortReclamationReason))
	wantPreempted.Insert(targetKeyReason(
		workload.NewReference("default", "gilead-node-b-0"), kueue.InClusterQueueReason))

	ctx, log := utiltesting.ContextWithLog(t)
	cl := utiltesting.NewClientBuilder().
		WithLists(&kueue.WorkloadList{Items: admitted}).
		Build()
	cqCache := schdcache.New(cl)
	for i := range nodes {
		cqCache.TASCache().SyncNode(&nodes[i])
	}
	cqCache.AddOrUpdateTopology(log, topology)
	cqCache.AddOrUpdateResourceFlavor(log, flavor)
	if err := cqCache.AddOrUpdateCohort(utiltestingapi.MakeCohort("shared").Obj()); err != nil {
		t.Fatalf("Couldn't add Cohort to cache: %v", err)
	}
	for _, cq := range clusterQueues {
		if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
			t.Fatalf("Couldn't add ClusterQueue to cache: %v", err)
		}
	}

	broadcaster := record.NewBroadcaster()
	recorder := broadcaster.NewRecorder(runtime.NewScheme(), corev1.EventSource{Component: constants.AdmissionName})
	preemptor := New(cl, workload.Ordering{}, recorder,
		&config.FairSharing{
			PreemptionStrategies: []config.PreemptionStrategy{
				config.LessThanOrEqualToFinalShare, config.LessThanInitialShare,
			},
		},
		false, clocktesting.NewFakeClock(now), nil, preemptexpectations.New(), nil)

	beforeSnapshot, err := cqCache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("unexpected error while building snapshot: %v", err)
	}
	snapshotWorkingCopy, err := cqCache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("unexpected error while building snapshot: %v", err)
	}
	beforeTASCapacity := tasFreeCapacity(t, beforeSnapshot)

	wlInfo := workload.NewInfo(incoming)
	wlInfo.ClusterQueue = "env-gilead"
	assignment := flavorassigner.Assignment{
		PodSets: []flavorassigner.PodSetAssignment{{
			Name:  "trainers",
			Count: 1,
			Flavors: flavorassigner.ResourceAssignment{
				tasGPU: &flavorassigner.FlavorAssignment{Name: "tas-default", Mode: flavorassigner.Preempt},
			},
		}},
	}
	targets := preemptor.GetTargets(log, *wlInfo, assignment, snapshotWorkingCopy)

	gotTargets := sets.New(utilslices.Map(targets, func(t **Target) string {
		return targetKeyReason(workload.Key((*t).WorkloadInfo.Obj), (*t).Reason)
	})...)
	if diff := cmp.Diff(wantPreempted, gotTargets, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("Issued preemptions (-want,+got):\n%s", diff)
	}
	tasSnapCmpOpts := append(cmp.Options{cmpopts.IgnoreUnexported(schdcache.TASFlavorSnapshot{})}, snapCmpOpts...)
	if diff := cmp.Diff(beforeSnapshot, snapshotWorkingCopy, tasSnapCmpOpts); diff != "" {
		t.Errorf("Snapshot was modified (-initial,+end):\n%s", diff)
	}
	if diff := cmp.Diff(beforeTASCapacity, tasFreeCapacity(t, snapshotWorkingCopy)); diff != "" {
		t.Errorf("TAS free capacity was modified (-initial,+end):\n%s", diff)
	}
}

func tasFreeCapacity(t *testing.T, snapshot *schdcache.Snapshot) map[string]string {
	t.Helper()
	got := make(map[string]string)
	for cqName, cq := range snapshot.ClusterQueues() {
		for flavor, tasSnapshot := range cq.TASFlavors {
			serialized, err := tasSnapshot.SerializeFreeCapacityPerDomain()
			if err != nil {
				t.Fatalf("couldn't serialize TAS free capacity: %v", err)
			}
			got[fmt.Sprintf("%s/%s", cqName, flavor)] = serialized
		}
	}
	return got
}

func tasGPUClusterQueue(name string, nominalGPU int) *kueue.ClusterQueue {
	return utiltestingapi.MakeClusterQueue(name).
		Cohort("shared").
		Preemption(kueue.ClusterQueuePreemption{
			ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
			WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
		}).
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-default").
			Resource(tasGPU, fmt.Sprintf("%d", nominalGPU)).Obj()).
		Obj()
}

func fsGPUClusterQueue(name string, nominalGPU int, weight string) *kueue.ClusterQueue {
	return utiltestingapi.MakeClusterQueue(name).
		Cohort("shared").
		FairWeight(resource.MustParse(weight)).
		Preemption(kueue.ClusterQueuePreemption{
			ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
			BorrowWithinCohort: &kueue.BorrowWithinCohort{
				Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
			},
			WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
		}).
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-default").
			Resource(tasGPU, fmt.Sprintf("%d", nominalGPU)).Obj()).
		Obj()
}

func tasGPUWorkload(name, cq, node string, topology *kueue.Topology, priority int32, now time.Time) kueue.Workload {
	return *utiltestingapi.MakeWorkload(name, "default").
		Priority(priority).
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
			Request(tasGPU, "1").
			PreferredTopologyRequest(corev1.LabelHostname).
			Obj()).
		ReserveQuotaAt(utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(cq)).
			PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
				Assignment(tasGPU, "tas-default", "1").
				Count(1).
				TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(topology)).
					Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{node}, 1).Obj()).
					Obj()).
				Obj()).
			Obj(), now).
		Obj()
}

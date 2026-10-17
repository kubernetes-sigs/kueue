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

package scheduler

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
)

// TestDeferredTASReplacementDoesNotTriggerExtraPreemption covers the case where
// the capacity a failed-node replacement is waiting for is already claimed by a
// pending preemptor: v is being preempted on x6 for p, x runs on y1, and the
// replacement foo can only fit on x6. The deferral must keep foo waiting
// without invalidating p's plan, so p keeps waiting for v and no additional
// preemption is issued against x.
func TestDeferredTASReplacementDoesNotTriggerExtraPreemption(t *testing.T) {
	now := time.Now().Truncate(time.Second)

	singleLevelTopology := *utiltestingapi.MakeDefaultOneLevelTopology("tas-single-level")

	nodes := []corev1.Node{
		*testingnode.MakeNode("x6").
			Label("tas-node", "true").
			Label(corev1.LabelHostname, "x6").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("2"),
				corev1.ResourceMemory: resource.MustParse("4Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().Obj(),
		*testingnode.MakeNode("y1").
			Label("tas-node", "true").
			Label(corev1.LabelHostname, "y1").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("2"),
				corev1.ResourceMemory: resource.MustParse("4Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().Obj(),
	}

	flavor := *utiltestingapi.MakeResourceFlavor("tas-default").
		NodeLabel("tas-node", "true").
		TopologyName("tas-single-level").Obj()

	cq := *utiltestingapi.MakeClusterQueue("tas-main").
		Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-default").
			Resource(corev1.ResourceCPU, "50").
			Resource(corev1.ResourceMemory, "50Gi").Obj()).
		Obj()

	queues := []kueue.LocalQueue{
		*utiltestingapi.MakeLocalQueue("tas-main", "default").ClusterQueue("tas-main").Obj(),
	}

	cases := map[string]tasScheduleForTASCase{
		"deferred replacement keeps a waiting preemptor on its in-flight victim": {
			nodes:           nodes,
			topologies:      []kueue.Topology{singleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{flavor},
			clusterQueues:   []kueue.ClusterQueue{cq},
			workloads: []kueue.Workload{
				// v: in-flight preemption victim on x6, preempted for p.
				*utiltestingapi.MakeWorkload("v", "default").
					UID("wl-v").
					Queue("tas-main").
					Priority(1).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "2").
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "2000m").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&singleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x6"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(), now).
					AdmittedAt(true, now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-p, JobUID: job-p) due to prioritization in the ClusterQueue; preemptor path: /tas-main; preemptee path: /tas-main",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-p, JobUID: job-p) due to prioritization in the ClusterQueue; preemptor path: /tas-main; preemptee path: /tas-main",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				// x: running on y1.
				*utiltestingapi.MakeWorkload("x", "default").
					UID("wl-x").
					Queue("tas-main").
					Priority(1).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "2").
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "2000m").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&singleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"y1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				// p: pending preemptor waiting for v.
				*utiltestingapi.MakeWorkload("p", "default").
					UID("wl-p").
					JobUID("job-p").
					Queue("tas-main").
					Priority(3).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "2").
						Obj()).
					Obj(),
				// foo: failed-node replacement that only x6 can fit.
				*utiltestingapi.MakeWorkload("foo", "default").
					UnhealthyNodes("x0").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "2").
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "2000m").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&singleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x0"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(), now).
					AdmittedAt(true, now).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "2000m").
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&singleLevelTopology)).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x0"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-main": {"default/p"},
			},
			eventCmpOpts: cmp.Options{cmpopts.IgnoreFields(utiltesting.EventRecord{}, "Message")},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "SecondPassFailed", corev1.EventTypeWarning).Obj(),
				utiltesting.MakeEventRecord("default", "p", kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads, corev1.EventTypeWarning).Obj(),
			},
		},
	}

	runScheduleForTASCases(t, queues, now, cases)
}

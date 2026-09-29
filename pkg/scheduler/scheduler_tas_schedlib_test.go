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
	"k8s.io/component-base/featuregate"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestScheduleForTASSchedulerLibrary(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	const tasRackLabel = "cloud.provider.com/rack"

	defaultSingleNode := []corev1.Node{
		*testingnode.MakeNode("x1").
			Label("tas-node", "true").
			Label(corev1.LabelHostname, "x1").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
	}
	defaultSingleLevelTopology := *utiltestingapi.MakeDefaultOneLevelTopology("tas-single-level")
	defaultTASFlavor := *utiltestingapi.MakeResourceFlavor("tas-default").
		NodeLabel("tas-node", "true").
		TopologyName("tas-single-level").
		Obj()
	defaultClusterQueue := *utiltestingapi.MakeClusterQueue("tas-main").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-default").
			Resource(corev1.ResourceCPU, "50").
			Resource(corev1.ResourceMemory, "50Gi").Obj()).
		Obj()
	defaultProvCheck := *utiltestingapi.MakeAdmissionCheck("prov-check").
		ControllerName(kueue.ProvisioningRequestControllerName).
		Condition(metav1.Condition{
			Type:   kueue.AdmissionCheckActive,
			Status: metav1.ConditionTrue,
		}).
		Obj()
	clusterQueueWithProvReq := *utiltestingapi.MakeClusterQueue("tas-main").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-default").
			Resource(corev1.ResourceCPU, "50").
			Resource(corev1.ResourceMemory, "50Gi").Obj()).
		AdmissionChecks(kueue.AdmissionCheckReference(defaultProvCheck.Name)).
		Obj()
	queues := []kueue.LocalQueue{
		*utiltestingapi.MakeLocalQueue("tas-main", "default").ClusterQueue("tas-main").Obj(),
	}
	eventIgnoreMessage := cmpopts.IgnoreFields(utiltesting.EventRecord{}, "Message")

	cases := map[string]tasScheduleForTASCase{
		"SchedulerLibraryIntegration enabled: generic TAS workload admitted on healthy node": {
			nodes:           defaultSingleNode,
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "default").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/wl": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(utiltestingapi.MakePodSetAssignment("main").
						Assignment(corev1.ResourceCPU, "tas-default", "1").
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "wl", "QuotaReserved", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "wl", "Admitted", corev1.EventTypeNormal).Obj(),
			},
			featureGates: map[featuregate.Feature]bool{
				features.SchedulerLibraryIntegration: true,
			},
		},
		"SchedulerLibraryIntegration enabled: non-hostname lowest-level TAS excludes unschedulable node": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label("tas-node", "true").
					Label(tasRackLabel, "r1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("x2").
					Label("tas-node", "true").
					Label(tasRackLabel, "r2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Unschedulable().
					Ready().
					Obj(),
			},
			topologies: []kueue.Topology{
				*utiltestingapi.MakeTopology("tas-rack-only").
					Levels(tasRackLabel).
					Obj(),
			},
			resourceFlavors: []kueue.ResourceFlavor{
				*utiltestingapi.MakeResourceFlavor("tas-rack-flavor").
					NodeLabel("tas-node", "true").
					TopologyName("tas-rack-only").
					Obj(),
			},
			clusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("tas-main").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-rack-flavor").
						Resource(corev1.ResourceCPU, "50").Obj()).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-rack", "default").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(tasRackLabel).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/wl-rack": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(utiltestingapi.MakePodSetAssignment("main").
						Assignment(corev1.ResourceCPU, "tas-rack-flavor", "1").
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{tasRackLabel}).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"r1"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "wl-rack", "QuotaReserved", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "wl-rack", "Admitted", corev1.EventTypeNormal).Obj(),
			},
			featureGates: map[featuregate.Feature]bool{
				features.SchedulerLibraryIntegration: true,
			},
		},
		"SchedulerLibraryIntegration enabled: hostname lowest-level TAS filters unschedulable node via WAS": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label("tas-node", "true").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Unschedulable().
					Ready().
					Obj(),
				*testingnode.MakeNode("x2").
					Label("tas-node", "true").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-hostname", "default").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/wl-hostname": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(utiltestingapi.MakePodSetAssignment("main").
						Assignment(corev1.ResourceCPU, "tas-default", "1").
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x2"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "wl-hostname", "QuotaReserved", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "wl-hostname", "Admitted", corev1.EventTypeNormal).Obj(),
			},
			featureGates: map[featuregate.Feature]bool{
				features.SchedulerLibraryIntegration: true,
			},
		},
		"SchedulerLibraryIntegration enabled: ResourceFlavor toleration reaches the simulated pod": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label("tas-node", "true").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Taints(corev1.Taint{
						Key:    "example.com/gpu",
						Value:  "present",
						Effect: corev1.TaintEffectNoSchedule,
					}).
					Ready().
					Obj(),
			},
			topologies: []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{
				*utiltestingapi.MakeResourceFlavor("tas-default").
					NodeLabel("tas-node", "true").
					Toleration(corev1.Toleration{
						Key:      "example.com/gpu",
						Operator: corev1.TolerationOpExists,
					}).
					TopologyName("tas-single-level").
					Obj(),
			},
			clusterQueues: []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "1000m").
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "QuotaReserved", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "foo", "Admitted", corev1.EventTypeNormal).Obj(),
			},
			featureGates: map[featuregate.Feature]bool{
				features.SchedulerLibraryIntegration: true,
			},
		},
		"SchedulerLibraryIntegration enabled: ResourceFlavor toleration reaches the leader's simulated pod": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label("tas-node", "true").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("3"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Taints(corev1.Taint{
						Key:    "example.com/gpu",
						Value:  "present",
						Effect: corev1.TaintEffectNoSchedule,
					}).
					Ready().
					Obj(),
			},
			topologies: []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{
				*utiltestingapi.MakeResourceFlavor("tas-default").
					NodeLabel("tas-node", "true").
					Toleration(corev1.Toleration{
						Key:      "example.com/gpu",
						Operator: corev1.TolerationOpExists,
					}).
					TopologyName("tas-single-level").
					Obj(),
			},
			clusterQueues: []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(
						*utiltestingapi.MakePodSet("leader", 1).
							PodSetGroup("group").
							RequiredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltestingapi.MakePodSet("workers", 2).
							PodSetGroup("group").
							RequiredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(
						utiltestingapi.MakePodSetAssignment("leader").
							Assignment(corev1.ResourceCPU, "tas-default", "1000m").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
								Obj()).
							Obj(),
						utiltestingapi.MakePodSetAssignment("workers").
							Count(2).
							Assignment(corev1.ResourceCPU, "tas-default", "2000m").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 2).Obj()).
								Obj()).
							Obj(),
					).
					Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "QuotaReserved", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "foo", "Admitted", corev1.EventTypeNormal).Obj(),
			},
			featureGates: map[featuregate.Feature]bool{
				features.SchedulerLibraryIntegration: true,
				features.TASLeaderPodSetFeasibility:  true,
			},
		},
		"SchedulerLibraryIntegration enabled: admission check PodSetUpdates nodeSelector reaches the simulated pod": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label("tas-node", "true").
					Label("dedicated", "x1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("x2").
					Label("tas-node", "true").
					Label("dedicated", "x2").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{clusterQueueWithProvReq},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(
								utiltestingapi.MakePodSetAssignment("one").
									Assignment(corev1.ResourceCPU, "tas-default", "1000m").
									DelayedTopologyRequest(kueue.DelayedTopologyRequestStatePending).
									Obj(),
							).
							Obj(), now,
					).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "prov-check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{{
							Name:         "one",
							NodeSelector: map[string]string{"dedicated": "x2"},
						}},
					}).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(
						utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "tas-default", "1000m").
							DelayedTopologyRequest(kueue.DelayedTopologyRequestStateReady).
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x2"}, 1).Obj()).
								Obj()).
							Obj(),
					).
					Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "Admitted", corev1.EventTypeNormal).Obj(),
			},
			featureGates: map[featuregate.Feature]bool{
				features.SchedulerLibraryIntegration: true,
			},
		},
	}
	runScheduleForTASCases(t, queues, now, cases)
}

func TestScheduleForTASPreemptionSchedulerLibrary(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	singleNode := testingnode.MakeNode("x1").
		Label("tas-node", "true").
		Label(corev1.LabelHostname, "x1").
		StatusAllocatable(corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("5"),
			corev1.ResourceMemory: resource.MustParse("5Gi"),
			corev1.ResourcePods:   resource.MustParse("10"),
		}).
		Ready()
	defaultSingleNode := []corev1.Node{
		*singleNode.DeepCopy(),
	}
	defaultTwoNodes := []corev1.Node{
		*testingnode.MakeNode("x1").
			Label("tas-node", "true").
			Label(corev1.LabelHostname, "x1").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("5"),
				corev1.ResourceMemory: resource.MustParse("5Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("y1").
			Label("tas-node", "true").
			Label(corev1.LabelHostname, "y1").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("5"),
				corev1.ResourceMemory: resource.MustParse("5Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
	}
	defaultSingleLevelTopology := *utiltestingapi.MakeDefaultOneLevelTopology("tas-single-level")
	defaultTASFlavor := *utiltestingapi.MakeResourceFlavor("tas-default").
		NodeLabel("tas-node", "true").
		TopologyName("tas-single-level").
		Obj()
	defaultClusterQueueWithPreemption := *utiltestingapi.MakeClusterQueue("tas-main").
		Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-default").
			Resource(corev1.ResourceCPU, "50").
			Resource(corev1.ResourceMemory, "50Gi").Obj()).
		Obj()
	queues := []kueue.LocalQueue{
		*utiltestingapi.MakeLocalQueue("tas-main", "default").ClusterQueue("tas-main").Obj(),
		*utiltestingapi.MakeLocalQueue("tas-lq-a", "default").ClusterQueue("tas-cq-a").Obj(),
		*utiltestingapi.MakeLocalQueue("tas-lq-b", "default").ClusterQueue("tas-cq-b").Obj(),
		*utiltestingapi.MakeLocalQueue("tas-lq-c", "default").ClusterQueue("tas-cq-c").Obj(),
	}
	eventIgnoreMessage := cmpopts.IgnoreFields(utiltesting.EventRecord{}, "Message")

	cases := map[string]tasScheduleTestCase{
		"a simulator that will not release the victim does not stop the cycle": {
			// A simulator that cannot release the victim leaves the cycle stricter
			// than reality, which is safe. It must not stop the cycle.
			failSimulatorPreemption:  true,
			wantSimulatorPreemptions: []workload.Reference{"default/low-priority-admitted"},
			nodes:                    defaultSingleNode,
			topologies:               []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors:          []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:            []kueue.ClusterQueue{defaultClusterQueueWithPreemption},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					UID("wl-foo").
					JobUID("job-foo").
					Queue("tas-main").
					Priority(3).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "2").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("low-priority-admitted", "default").
					UID("low-priority-admitted-uid").
					Queue("tas-main").
					Priority(1).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "5").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "5").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					UID("wl-foo").
					JobUID("job-foo").
					Queue("tas-main").
					Priority(3).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "2").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            `couldn't assign flavors to pod set one: topology "tas-single-level" doesn't allow to fit any of 1 pod(s). Total nodes: 1; excluded: resource "cpu": 1. Pending the preemption of 1 workload(s)`,
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadAdmittedReasonNoReservation,
						Message:            "The workload has no reservation",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("2"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("low-priority-admitted", "default").
					UID("low-priority-admitted-uid").
					Queue("tas-main").
					Priority(1).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "5").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "5").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-foo, JobUID: job-foo) due to prioritization in the ClusterQueue; preemptor path: /tas-main; preemptee path: /tas-main",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-foo, JobUID: job-foo) due to prioritization in the ClusterQueue; preemptor path: /tas-main; preemptee path: /tas-main",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-main": {"default/foo"},
			},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "low-priority-admitted", "EvictedDueToPreempted", "Normal").
					Message("Preempted to accommodate a workload (UID: wl-foo, JobUID: job-foo) due to prioritization in the ClusterQueue; preemptor path: /tas-main; preemptee path: /tas-main").
					Obj(),
				utiltesting.MakeEventRecord("default", "low-priority-admitted", "Preempted", "Normal").
					Message("Preempted to accommodate a workload (UID: wl-foo, JobUID: job-foo) due to prioritization in the ClusterQueue; preemptor path: /tas-main; preemptee path: /tas-main; preemptor effective priority: 3 (base: 3, boost: 0); preemptee effective priority: 1 (base: 1, boost: 0)").
					Obj(),
				utiltesting.MakeEventRecord("default", "foo", "PreemptedWorkload", "Normal").
					Message("Preempted workload default/low-priority-admitted (UID: low-priority-admitted-uid) in ClusterQueue tas-main; preemptor effective priority: 3 (base: 3, boost: 0); preemptee effective priority: 1 (base: 1, boost: 0)").
					Obj(),
				utiltesting.MakeEventRecord("default", "foo", kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads, "Warning").
					Message(`couldn't assign flavors to pod set one: topology "tas-single-level" doesn't allow to fit any of 1 pod(s). Total nodes: 1; excluded: resource "cpu": 1. Pending the preemption of 1 workload(s)`).
					Obj(),
			},
		},
		"only low priority workload is preempted": {
			// This test case demonstrates the baseline scenario where there
			// is only one low-priority workload and it gets preempted.
			//
			// The simulated cluster has to lose the victim's Pods too, or a host
			// port it holds keeps the node looking unusable.
			wantSimulatorPreemptions: []workload.Reference{"default/low-priority-admitted"},
			nodes:                    defaultSingleNode,
			topologies:               []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors:          []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:            []kueue.ClusterQueue{defaultClusterQueueWithPreemption},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					UID("wl-foo").
					JobUID("job-foo").
					Queue("tas-main").
					Priority(3).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "2").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("low-priority-admitted", "default").
					UID("low-priority-admitted-uid").
					Queue("tas-main").
					Priority(1).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "5").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "5").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					UID("wl-foo").
					JobUID("job-foo").
					Queue("tas-main").
					Priority(3).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "2").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            `couldn't assign flavors to pod set one: topology "tas-single-level" doesn't allow to fit any of 1 pod(s). Total nodes: 1; excluded: resource "cpu": 1. Pending the preemption of 1 workload(s)`,
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadAdmittedReasonNoReservation,
						Message:            "The workload has no reservation",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("2"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("low-priority-admitted", "default").
					UID("low-priority-admitted-uid").
					Queue("tas-main").
					Priority(1).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "5").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "5").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-foo, JobUID: job-foo) due to prioritization in the ClusterQueue; preemptor path: /tas-main; preemptee path: /tas-main",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-foo, JobUID: job-foo) due to prioritization in the ClusterQueue; preemptor path: /tas-main; preemptee path: /tas-main",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-main": {"default/foo"},
			},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "low-priority-admitted", "EvictedDueToPreempted", "Normal").
					Message("Preempted to accommodate a workload (UID: wl-foo, JobUID: job-foo) due to prioritization in the ClusterQueue; preemptor path: /tas-main; preemptee path: /tas-main").
					Obj(),
				utiltesting.MakeEventRecord("default", "low-priority-admitted", "Preempted", "Normal").
					Message("Preempted to accommodate a workload (UID: wl-foo, JobUID: job-foo) due to prioritization in the ClusterQueue; preemptor path: /tas-main; preemptee path: /tas-main; preemptor effective priority: 3 (base: 3, boost: 0); preemptee effective priority: 1 (base: 1, boost: 0)").
					Obj(),
				utiltesting.MakeEventRecord("default", "foo", "PreemptedWorkload", "Normal").
					Message("Preempted workload default/low-priority-admitted (UID: low-priority-admitted-uid) in ClusterQueue tas-main; preemptor effective priority: 3 (base: 3, boost: 0); preemptee effective priority: 1 (base: 1, boost: 0)").
					Obj(),
				utiltesting.MakeEventRecord("default", "foo", kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads, "Warning").
					Message(`couldn't assign flavors to pod set one: topology "tas-single-level" doesn't allow to fit any of 1 pod(s). Total nodes: 1; excluded: resource "cpu": 1. Pending the preemption of 1 workload(s)`).
					Obj(),
			},
		},
		"the overlap recompute takes the other preemption's victim out of the simulator": {
			// The same cluster as the case above, with a simulator installed. The
			// recompute projects the other preemption's victim out of quota and TAS
			// usage, and has to project it out of the simulator too, or the simulator
			// keeps reporting the host ports and devices that victim holds as taken.
			//
			// Kept apart from the case above rather than folded into it: setting
			// wantSimulatorPreemptions turns SchedulerLibraryIntegration on, and that
			// case is the only cover for the recompute with the gate off.
			featureGates: map[featuregate.Feature]bool{
				features.RecomputeAssignmentUponPreemptionTargetsOverlap: true,
				features.TASRecomputeAssignmentWithinSchedulingCycle:     true,
			},
			nodes:           defaultTwoNodes,
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			cohorts: []kueue.Cohort{
				*utiltestingapi.MakeCohort("tas-cohort-main").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-default").
						Resource(corev1.ResourceCPU, "0").Obj()).
					Obj(),
			},
			clusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("tas-cq-a").
					Cohort("tas-cohort-main").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-default").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("5").BorrowingLimit("10").Append().
						Obj()).
					Obj(),
				*utiltestingapi.MakeClusterQueue("tas-cq-b").
					Cohort("tas-cohort-main").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-default").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("5").BorrowingLimit("10").Append().
						Obj()).
					Obj(),
				*utiltestingapi.MakeClusterQueue("tas-cq-c").
					Cohort("tas-cohort-main").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-default").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("0").BorrowingLimit("10").Append().
						Obj()).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-low-admitted", "default").
					UID("wl-low-admitted-uid").
					Queue("tas-lq-c").
					Priority(1).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-cq-c").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "3000m").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "3").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-mid-admitted", "default").
					UID("wl-mid-admitted-uid").
					Queue("tas-lq-c").
					Priority(2).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-cq-c").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "3000m").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"y1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "3").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-high-1", "default").
					UID("wl-high-1-uid").
					JobUID("job-high-1-uid").
					Queue("tas-lq-a").
					Priority(10).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "3").
						Obj()).
					Creation(now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-high-2", "default").
					UID("wl-high-2-uid").
					JobUID("job-high-2-uid").
					Queue("tas-lq-b").
					Priority(10).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "3").
						Obj()).
					Creation(now.Add(time.Second)).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-high-1", "default").
					UID("wl-high-1-uid").
					JobUID("job-high-1-uid").
					Queue("tas-lq-a").
					Priority(10).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "3").
						Obj()).
					Creation(now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            "couldn't assign flavors to pod set one: topology \"tas-single-level\" doesn't allow to fit any of 1 pod(s). Total nodes: 2; excluded: resource \"cpu\": 2. Pending the preemption of 1 workload(s)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadAdmittedReasonNoReservation,
						Message:            "The workload has no reservation",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("3"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-high-2", "default").
					UID("wl-high-2-uid").
					JobUID("job-high-2-uid").
					Queue("tas-lq-b").
					Priority(10).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "3").
						Obj()).
					Creation(now.Add(time.Second)).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            "couldn't assign flavors to pod set one: topology \"tas-single-level\" doesn't allow to fit any of 1 pod(s). Total nodes: 2; excluded: resource \"cpu\": 2. Pending the preemption of 1 workload(s)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadAdmittedReasonNoReservation,
						Message:            "The workload has no reservation",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("3"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-low-admitted", "default").
					UID("wl-low-admitted-uid").
					Queue("tas-lq-c").
					Priority(1).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-cq-c").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "3000m").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "3").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-high-1-uid, JobUID: job-high-1-uid) due to reclamation within the cohort; preemptor path: /tas-cohort-main/tas-cq-a; preemptee path: /tas-cohort-main/tas-cq-c",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-high-1-uid, JobUID: job-high-1-uid) due to reclamation within the cohort; preemptor path: /tas-cohort-main/tas-cq-a; preemptee path: /tas-cohort-main/tas-cq-c",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-mid-admitted", "default").
					UID("wl-mid-admitted-uid").
					Queue("tas-lq-c").
					Priority(2).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-cq-c").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "3000m").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"y1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "3").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-high-2-uid, JobUID: job-high-2-uid) due to reclamation within the cohort; preemptor path: /tas-cohort-main/tas-cq-b; preemptee path: /tas-cohort-main/tas-cq-c",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-high-2-uid, JobUID: job-high-2-uid) due to reclamation within the cohort; preemptor path: /tas-cohort-main/tas-cq-b; preemptee path: /tas-cohort-main/tas-cq-c",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-cq-a": {"default/wl-high-1"},
				"tas-cq-b": {"default/wl-high-2"},
			},
			// wl-low-admitted is released three times: once per preemptor evaluating
			// its own targets, and once more by the recompute.
			wantSimulatorReleasedTogether: []workload.Reference{"default/wl-low-admitted", "default/wl-mid-admitted"},
			wantSimulatorPreemptions: []workload.Reference{
				"default/wl-low-admitted",
				"default/wl-low-admitted",
				"default/wl-low-admitted",
				"default/wl-mid-admitted",
			},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "wl-low-admitted", "EvictedDueToPreempted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "wl-low-admitted", "Preempted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "wl-mid-admitted", "EvictedDueToPreempted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "wl-mid-admitted", "Preempted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "wl-high-1", "PreemptedWorkload", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "wl-high-1", kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads, corev1.EventTypeWarning).Obj(),
				utiltesting.MakeEventRecord("default", "wl-high-2", "PreemptedWorkload", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "wl-high-2", kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads, corev1.EventTypeWarning).Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
		},
	}
	runTASScheduleTestCases(t, tasScheduleTestConfig{
		queues: queues,
		now:    now,
	}, cases)
}

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
	"context"
	"errors"
	"fmt"
	"iter"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	schedulingv1alpha3 "k8s.io/api/scheduling/v1alpha3"
	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-base/featuregate"
	"k8s.io/klog/v2/ktesting"
	kubefeatures "k8s.io/kubernetes/pkg/features"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
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
			featureGates: map[featuregate.Feature]bool{
				features.SchedulerLibraryIntegration: true,
			},
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
			featureGates: map[featuregate.Feature]bool{
				features.SchedulerLibraryIntegration: true,
			},
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
			// The same cluster as the "preempting workload recomputes both overlapping
			// preemption targets and TAS topology assignment; both features enabled"
			// case in TestScheduleForTASPreemption, with a simulator installed. The
			// recompute projects the other preemption's victim out of quota and TAS
			// usage, and has to project it out of the simulator too, or the simulator
			// keeps reporting the host ports and devices that victim holds as taken.
			//
			// Kept apart from that case rather than folded into it: this case turns
			// SchedulerLibraryIntegration on, and that case is the only cover for the
			// recompute with SchedulerLibraryIntegration off.
			featureGates: map[featuregate.Feature]bool{
				features.RecomputeAssignmentUponPreemptionTargetsOverlap: true,
				features.TASRecomputeAssignmentWithinSchedulingCycle:     true,
				features.SchedulerLibraryIntegration:                     true,
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
						Message:            "couldn't assign flavors to pod set one: topology \"tas-single-level\" doesn't allow to fit any of 1 pod(s); all pods must fit in a single \"kubernetes.io/hostname\" topology domain. Total nodes: 2; excluded: resource \"cpu\": 2. Pending the preemption of 1 workload(s)",
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
						Message:            "couldn't assign flavors to pod set one: topology \"tas-single-level\" doesn't allow to fit any of 1 pod(s); all pods must fit in a single \"kubernetes.io/hostname\" topology domain. Total nodes: 2; excluded: resource \"cpu\": 2. Pending the preemption of 1 workload(s)",
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
		// A failed-node replacement whose only candidate node is held by an
		// in-flight eviction victim. Both Pods request the same host port, so the
		// victim's Pod is what keeps y1 looking unusable in the simulator: unless
		// the deferral recompute takes the victim Pods out too, the replacement
		// does not fit and is fail-fast evicted instead of deferred.
		"in-flight eviction frees the only candidate node; deferred with the simulator": {
			// This case only means something with the WAS simulator installed: the
			// victim Pod holds the host port that keeps y1 unusable. Since #16483
			// wantSimulatorPreemptions no longer turns SchedulerLibraryIntegration
			// on, so enable it here explicitly.
			featureGates: map[featuregate.Feature]bool{
				features.TASFailedNodeReplacementFailFast:                           true,
				features.SchedulerLibraryIntegration:                                true,
				features.TASNodeReplacementDeferFailFastWhenUsefulInflightEvictions: true,
			},
			wantSimulatorPreemptions: []workload.Reference{"default/victim"},
			nodes: []corev1.Node{
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
			},
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueWithPreemption},
			// The workload controller requeues admitted workloads whose node
			// failed, so foo starts the cycle with its second pass already pending.
			prequeuedSecondPasses: []workload.Reference{"default/foo"},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("victim", "default").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Containers(corev1.Container{
							Name: "c",
							Resources: corev1.ResourceRequirements{
								Requests: make(corev1.ResourceList),
							},
							Ports: []corev1.ContainerPort{{HostPort: 8080, Protocol: corev1.ProtocolTCP}},
						}).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "5").
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "5").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"y1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("foo", "default").
					UnhealthyNodes("x1").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Containers(corev1.Container{
							Name: "c",
							Resources: corev1.ResourceRequirements{
								Requests: make(corev1.ResourceList),
							},
							Ports: []corev1.ContainerPort{{HostPort: 8080, Protocol: corev1.ProtocolTCP}},
						}).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "2").
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "2").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					UnhealthyNodes("x1").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Containers(corev1.Container{
							Name: "c",
							Resources: corev1.ResourceRequirements{
								Requests: make(corev1.ResourceList),
							},
							Ports: []corev1.ContainerPort{{HostPort: 8080, Protocol: corev1.ProtocolTCP}},
						}).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "2").
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "2").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("victim", "default").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Containers(corev1.Container{
							Name: "c",
							Resources: corev1.ResourceRequirements{
								Requests: make(corev1.ResourceList),
							},
							Ports: []corev1.ContainerPort{{HostPort: 8080, Protocol: corev1.ProtocolTCP}},
						}).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "5").
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "5").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"y1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
			},
			wantSimulatorReleasedTogether: []workload.Reference{"default/victim"},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "SecondPassFailed", corev1.EventTypeWarning).Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
		},
		"SchedulerLibraryDeepIntegration: preemption when ClusterQueue quota fits but node is occupied": {
			featureGates: map[featuregate.Feature]bool{
				kubefeatures.GenericWorkload:             true,
				features.SchedulerLibraryIntegration:     true,
				features.SchedulerLibraryDeepIntegration: true,
			},
			nodes:           defaultSingleNode,
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("tas-main").
					Annotation(kueue.PreferredAssignmentPlannerAnnotation, string(kueue.KubeSchedulerDeepIntegration)).
					Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-default").
						Resource(corev1.ResourceCPU, "50").
						Resource(corev1.ResourceMemory, "50Gi").Obj()).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					UID("wl-foo").
					JobUID("job-foo").
					Queue("tas-main").
					Priority(3).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
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
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "2").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            `couldn't assign flavors to pod set one: pod: default/virtual-foo-one-0-2da20, failure reason: 0/1 nodes are available: 1 Insufficient cpu.. Pending the preemption of 1 workload(s)`,
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
					Message(`couldn't assign flavors to pod set one: pod: default/virtual-foo-one-0-2da20, failure reason: 0/1 nodes are available: 1 Insufficient cpu.. Pending the preemption of 1 workload(s)`).
					Obj(),
			},
		},
		"SchedulerLibraryDeepIntegration: preemption when ClusterQueue quota is full but cluster has a free node": {
			featureGates: map[featuregate.Feature]bool{
				kubefeatures.GenericWorkload:             true,
				features.SchedulerLibraryIntegration:     true,
				features.SchedulerLibraryDeepIntegration: true,
			},
			nodes:           defaultTwoNodes,
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("tas-main").
					Annotation(kueue.PreferredAssignmentPlannerAnnotation, string(kueue.KubeSchedulerDeepIntegration)).
					Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-default").
						Resource(corev1.ResourceCPU, "5").
						Resource(corev1.ResourceMemory, "50Gi").Obj()).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					UID("wl-foo").
					JobUID("job-foo").
					Queue("tas-main").
					Priority(3).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
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
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "2").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            `couldn't assign flavors to pod set one: insufficient unused quota for cpu in flavor tas-default, 2 more needed. Pending the preemption of 1 workload(s)`,
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
					Message(`couldn't assign flavors to pod set one: insufficient unused quota for cpu in flavor tas-default, 2 more needed. Pending the preemption of 1 workload(s)`).
					Obj(),
			},
		},
	}
	runTASScheduleTestCases(t, tasScheduleTestConfig{
		queues: queues,
		now:    now,
	}, cases)
}

// TestScheduleForTASFairSharingSchedulerLibrary covers the fair sharing
// verification of the preemption targets that remain after scheduler-library
// reprieves some of the candidates.
func TestScheduleForTASFairSharingSchedulerLibrary(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	singleNode := []corev1.Node{
		*testingnode.MakeNode("x1").
			Label("tas-node", "true").
			Label(corev1.LabelHostname, "x1").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:  resource.MustParse("5"),
				corev1.ResourcePods: resource.MustParse("10"),
			}).
			Ready().
			Obj(),
	}
	topology := *utiltestingapi.MakeDefaultOneLevelTopology("tas-single-level")
	resourceFlavor := *utiltestingapi.MakeResourceFlavor("tas-default").
		NodeLabel("tas-node", "true").
		TopologyName("tas-single-level").
		Obj()
	clusterQueues := []kueue.ClusterQueue{
		*utiltestingapi.MakeClusterQueue("tas-cq-a").
			Annotation(kueue.PreferredAssignmentPlannerAnnotation, string(kueue.KubeSchedulerDeepIntegration)).
			Cohort("tas-cohort").
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
			}).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-default").Resource(corev1.ResourceCPU, "3").Obj()).
			Obj(),
		*utiltestingapi.MakeClusterQueue("tas-cq-b").
			Annotation(kueue.PreferredAssignmentPlannerAnnotation, string(kueue.KubeSchedulerDeepIntegration)).
			Cohort("tas-cohort").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-default").Resource(corev1.ResourceCPU, "2").Obj()).
			Obj(),
	}
	queues := []kueue.LocalQueue{
		*utiltestingapi.MakeLocalQueue("tas-lq-a", "default").ClusterQueue("tas-cq-a").Obj(),
		*utiltestingapi.MakeLocalQueue("tas-lq-b", "default").ClusterQueue("tas-cq-b").Obj(),
	}
	eventIgnoreMessage := cmpopts.IgnoreFields(utiltesting.EventRecord{}, "Message")

	// The node and the cohort have 5 CPUs each, all used by reprievedCandidate and victim.
	//
	// preemptor needs 3 CPUs in tas-cq-a, so it can only be admitted by preempting.
	preemptor := utiltestingapi.MakeWorkload("preemptor", "default").
		UID("preemptor-uid").
		JobUID("preemptor-job-uid").
		Queue("tas-lq-a").
		Priority(2).
		Request(corev1.ResourceCPU, "3")
	// reprievedCandidate has a lower priority than preemptor, in the same
	// ClusterQueue. Preempting it too keeps tas-cq-a within its nominal quota after
	// preemptor is admitted, which is what lets fair sharing pick victim.
	// scheduler-library then reprieves it, because preemptor fits next to it once
	// victim is gone.
	reprievedCandidate := utiltestingapi.MakeWorkload("reprieved-candidate", "default").
		UID("reprieved-candidate-uid").
		Queue("tas-lq-a").
		Priority(1).
		Request(corev1.ResourceCPU, "2").
		ReserveQuotaAt(utiltestingapi.MakeAdmission("tas-cq-a").
			PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
				Assignment(corev1.ResourceCPU, "tas-default", "2").
				TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
					Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
					Obj()).
				Obj()).
			Obj(), now).
		AdmittedAt(true, now)
	// victim borrows 1 CPU beyond tas-cq-b's nominal quota. Preempting it alone
	// would leave tas-cq-a borrowing 2 CPUs, more than the 1 CPU tas-cq-b borrowed,
	// so fair sharing does not justify it.
	victim := utiltestingapi.MakeWorkload("victim", "default").
		UID("victim-uid").
		Queue("tas-lq-b").
		Request(corev1.ResourceCPU, "3").
		ReserveQuotaAt(utiltestingapi.MakeAdmission("tas-cq-b").
			PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
				Assignment(corev1.ResourceCPU, "tas-default", "3").
				TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
					Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
					Obj()).
				Obj()).
			Obj(), now).
		AdmittedAt(true, now)
	preemptorNotReserved := func(reason, message string) kueue.Workload {
		return *preemptor.Clone().
			Condition(metav1.Condition{
				Type:               kueue.WorkloadQuotaReserved,
				Status:             metav1.ConditionFalse,
				Reason:             reason,
				Message:            message,
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
				Name:      kueue.DefaultPodSetName,
				Resources: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("3")},
			}).
			Obj()
	}

	cases := map[string]tasScheduleTestCase{
		"victim left after reprieval is not preempted when fair sharing does not justify it": {
			featureGates: map[featuregate.Feature]bool{
				kubefeatures.GenericWorkload:             true,
				features.SchedulerLibraryIntegration:     true,
				features.SchedulerLibraryDeepIntegration: true,
				features.FairSharingVerifyFinalTargets:   true,
			},
			enableFairSharing: true,
			nodes:             singleNode,
			topologies:        []kueue.Topology{topology},
			resourceFlavors:   []kueue.ResourceFlavor{resourceFlavor},
			clusterQueues:     clusterQueues,
			workloads:         []kueue.Workload{*preemptor.Obj(), *reprievedCandidate.Obj(), *victim.Obj()},
			wantWorkloads: []kueue.Workload{
				preemptorNotReserved(kueue.WorkloadQuotaReservedReasonWaitingForQuota,
					"couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor tas-default, 3 more needed"),
				*reprievedCandidate.Obj(),
				*victim.Obj(),
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-cq-a": {"default/preemptor"},
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "preemptor", kueue.WorkloadQuotaReservedReasonWaitingForQuota, corev1.EventTypeWarning).Obj(),
			},
		},
		// Without the final verification, victim is preempted even though fair
		// sharing does not justify it, which causes preemption loops (#14543).
		"victim left after reprieval is preempted when FairSharingVerifyFinalTargets is disabled": {
			featureGates: map[featuregate.Feature]bool{
				kubefeatures.GenericWorkload:             true,
				features.SchedulerLibraryIntegration:     true,
				features.SchedulerLibraryDeepIntegration: true,
				features.FairSharingVerifyFinalTargets:   false,
			},
			enableFairSharing: true,
			nodes:             singleNode,
			topologies:        []kueue.Topology{topology},
			resourceFlavors:   []kueue.ResourceFlavor{resourceFlavor},
			clusterQueues:     clusterQueues,
			workloads:         []kueue.Workload{*preemptor.Obj(), *reprievedCandidate.Obj(), *victim.Obj()},
			wantWorkloads: []kueue.Workload{
				preemptorNotReserved(kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
					"couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor tas-default, 3 more needed. Pending the preemption of 1 workload(s)"),
				*reprievedCandidate.Obj(),
				*victim.Clone().
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadEvictedByPreemption,
						Message:            "Preempted to accommodate a workload (UID: preemptor-uid, JobUID: preemptor-job-uid) due to reclamation within the cohort; preemptor path: /tas-cohort/tas-cq-a; preemptee path: /tas-cohort/tas-cq-b",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.InCohortReclamationReason,
						Message:            "Preempted to accommodate a workload (UID: preemptor-uid, JobUID: preemptor-job-uid) due to reclamation within the cohort; preemptor path: /tas-cohort/tas-cq-a; preemptee path: /tas-cohort/tas-cq-b",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: kueue.WorkloadEvictedByPreemption, Count: 1}).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-cq-a": {"default/preemptor"},
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "preemptor", "PreemptedWorkload", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "preemptor", kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads, corev1.EventTypeWarning).Obj(),
				utiltesting.MakeEventRecord("default", "victim", "EvictedDueToPreempted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "victim", "Preempted", corev1.EventTypeNormal).Obj(),
			},
		},
	}
	runTASScheduleTestCases(t, tasScheduleTestConfig{
		queues: queues,
		now:    now,
	}, cases)
}

func TestScheduleErrorHandling(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	singleNode := []corev1.Node{
		*testingnode.MakeNode("x1").
			Label("tas-node", "true").
			Label(corev1.LabelHostname, "x1").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:  resource.MustParse("1"),
				corev1.ResourcePods: resource.MustParse("10"),
			}).
			Ready().
			Obj(),
	}
	defaultSingleLevelTopology := *utiltestingapi.MakeDefaultOneLevelTopology("tas-single-level")
	defaultTASFlavor := *utiltestingapi.MakeResourceFlavor("tas-default").
		NodeLabel("tas-node", "true").
		TopologyName("tas-single-level").
		Obj()
	queues := []kueue.LocalQueue{
		*utiltestingapi.MakeLocalQueue("tas-main", "default").ClusterQueue("tas-main").Obj(),
		*utiltestingapi.MakeLocalQueue("tas-second", "default").ClusterQueue("tas-second").Obj(),
	}
	eventIgnoreMessage := cmpopts.IgnoreFields(utiltesting.EventRecord{}, "Message")
	deepIntegrationGates := map[featuregate.Feature]bool{
		features.SchedulerLibraryIntegration:     true,
		features.SchedulerLibraryDeepIntegration: true,
	}
	// tasConflictClusterQueues and tasConflictWorkloads set up two Workloads from
	// different ClusterQueues without a Cohort that are both nominated onto the
	// same 1-CPU node within the same cycle.
	tasConflictClusterQueues := []kueue.ClusterQueue{
		*utiltestingapi.MakeClusterQueue("tas-main").
			Annotation(kueue.PreferredAssignmentPlannerAnnotation, string(kueue.KubeSchedulerDeepIntegration)).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-default").
				Resource(corev1.ResourceCPU, "1").Obj()).
			Obj(),
		*utiltestingapi.MakeClusterQueue("tas-second").
			Annotation(kueue.PreferredAssignmentPlannerAnnotation, string(kueue.KubeSchedulerDeepIntegration)).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-default").
				Resource(corev1.ResourceCPU, "1").Obj()).
			Obj(),
	}
	wl1Admission := *utiltestingapi.MakeAdmission("tas-main").
		PodSets(utiltestingapi.MakePodSetAssignment("main").
			Assignment(corev1.ResourceCPU, "tas-default", "1").
			TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
				Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
				Obj()).
			Obj()).
		Obj()
	wl1Base := utiltestingapi.MakeWorkload("wl-a", "default").
		Queue("tas-main").
		Priority(10).
		PodSets(*utiltestingapi.MakePodSet("main", 1).
			RequiredTopologyRequest(corev1.LabelHostname).
			Request(corev1.ResourceCPU, "1").
			Obj())
	wl1Admitted := *wl1Base.Clone().
		Admission(&wl1Admission).
		Condition(metav1.Condition{
			Type:               kueue.WorkloadQuotaReserved,
			Status:             metav1.ConditionTrue,
			Reason:             "QuotaReserved",
			Message:            "Quota reserved in ClusterQueue tas-main",
			LastTransitionTime: metav1.NewTime(now),
		}).
		Condition(metav1.Condition{
			Type:               kueue.WorkloadAdmitted,
			Status:             metav1.ConditionTrue,
			Reason:             "Admitted",
			Message:            "The workload is admitted",
			LastTransitionTime: metav1.NewTime(now),
		}).
		Obj()
	wl2Base := utiltestingapi.MakeWorkload("wl-b", "default").
		Queue("tas-second").
		Priority(1).
		PodSets(*utiltestingapi.MakePodSet("main", 1).
			RequiredTopologyRequest(corev1.LabelHostname).
			Request(corev1.ResourceCPU, "1").
			Obj())
	wl2NotReserved := func(reason, message string) kueue.Workload {
		return *wl2Base.Clone().
			Condition(metav1.Condition{
				Type:               kueue.WorkloadQuotaReserved,
				Status:             metav1.ConditionFalse,
				Reason:             reason,
				Message:            message,
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
				Name: "main",
				Resources: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("1"),
				},
			}).
			Obj()
	}
	tasConflictWorkloads := []kueue.Workload{
		*wl1Base.Clone().Obj(),
		*wl2Base.Clone().Obj(),
	}

	cases := map[string]tasScheduleTestCase{
		"error on initial getAssignments for a workload that requires preemption": {
			// The quota is used by a lower-priority Workload, so the initial
			// assignment is Preempt, and ScheduleWorkload fails with an error
			// during nomination. No preemption is issued.
			nodes:           singleNode,
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("tas-main").
					Annotation(kueue.PreferredAssignmentPlannerAnnotation, string(kueue.KubeSchedulerDeepIntegration)).
					Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-default").
						Resource(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
			simulatorOverride: func() simulator.SchedulerSimulator {
				return &mockWorkloadScheduler{
					placementNode: "x1",
					injectError:   errors.New("injected initial assignment failure"),
					swFailAt:      1,
				}
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("high", "default").
					Queue("tas-main").
					Priority(10).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("low-admitted", "default").
					Queue("tas-main").
					Priority(1).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("main").
								Assignment(corev1.ResourceCPU, "tas-default", "1").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("high", "default").
					Queue("tas-main").
					Priority(10).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonAssignmentError,
						Message:            "failed to schedule workload: injected initial assignment failure",
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
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("1"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("low-admitted", "default").
					Queue("tas-main").
					Priority(1).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("main").
								Assignment(corev1.ResourceCPU, "tas-default", "1").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-main": {"default/high"},
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "high", kueue.WorkloadQuotaReservedReasonAssignmentError, corev1.EventTypeWarning).Obj(),
			},
			featureGates: deepIntegrationGates,
		},
		"virtualization error on initial getAssignments for a workload without a TAS flavor": {
			nodes:           singleNode,
			resourceFlavors: []kueue.ResourceFlavor{*utiltestingapi.MakeResourceFlavor("non-tas").Obj()},
			clusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("tas-main").
					Annotation(kueue.PreferredAssignmentPlannerAnnotation, string(kueue.KubeSchedulerDeepIntegration)).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("non-tas").
						Resource(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "default").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "default").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonAssignmentError,
						Message:            `virtualization error: unable to build virtual pods: failed to get TAS flavor for PodSet "main": no TAS flavor assigned`,
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
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("1"),
						},
					}).
					Obj(),
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-main": {"default/wl"},
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "wl", kueue.WorkloadQuotaReservedReasonAssignmentError, corev1.EventTypeWarning).Obj(),
			},
			featureGates: deepIntegrationGates,
		},
		"error on recomputing assignments after a TAS conflict within the cycle": {
			// This test case simulates an error being thrown by ScheduleWorkload
			// during a call to wasPlanner.Plan
			// upon recomputing the assignment (inside processEntry).
			nodes:           singleNode,
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   tasConflictClusterQueues,
			workloads:       tasConflictWorkloads,
			simulatorOverride: func() simulator.SchedulerSimulator {
				return &mockWorkloadScheduler{
					placementNode: "x1",
					injectError:   errors.New("injected recompute failure"),
					swFailAt:      3,
				}
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/wl-a": wl1Admission,
			},
			wantWorkloads: []kueue.Workload{
				wl1Admitted,
				wl2NotReserved(kueue.WorkloadQuotaReservedReasonAssignmentError, "failed to schedule workload: injected recompute failure"),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-second": {"default/wl-b"},
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "wl-a", "QuotaReserved", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "wl-a", "Admitted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "wl-b", kueue.WorkloadQuotaReservedReasonAssignmentError, corev1.EventTypeWarning).Obj(),
			},
			featureGates: deepIntegrationGates,
		},
		"preferred topology is admitted on a KueueNative ClusterQueue when deep integration is enabled": {
			nodes:           singleNode,
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("tas-main").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-default").
						Resource(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "default").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/wl": wl1Admission,
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "default").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Admission(&wl1Admission).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue tas-main",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "wl", "QuotaReserved", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "wl", "Admitted", corev1.EventTypeNormal).Obj(),
			},
			featureGates: deepIntegrationGates,
		},
		"preferred topology fails with AssignmentError on a KubeSchedulerDeepIntegration ClusterQueue": {
			nodes:           singleNode,
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("tas-main").
					Annotation(kueue.PreferredAssignmentPlannerAnnotation, string(kueue.KubeSchedulerDeepIntegration)).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-default").
						Resource(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "default").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "default").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonAssignmentError,
						Message:            `virtualization error: unable to build virtual pod groups: workload "wl" is unsupportable by deep WAS: PreferredTopologyNotSupported (podset "main" specified preferred topology, which is not supported with SchedulerLibraryDeepIntegration)`,
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
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("1"),
						},
					}).
					Obj(),
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-main": {"default/wl"},
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "wl", kueue.WorkloadQuotaReservedReasonAssignmentError, corev1.EventTypeWarning).Obj(),
			},
			featureGates: deepIntegrationGates,
		},
		"preferred topology is admitted on a KubeSchedulerDeepIntegration ClusterQueue when deep integration is disabled": {
			nodes:           singleNode,
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("tas-main").
					Annotation(kueue.PreferredAssignmentPlannerAnnotation, string(kueue.KubeSchedulerDeepIntegration)).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-default").
						Resource(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "default").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/wl": wl1Admission,
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "default").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Admission(&wl1Admission).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue tas-main",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "wl", "QuotaReserved", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "wl", "Admitted", corev1.EventTypeNormal).Obj(),
			},
			featureGates: map[featuregate.Feature]bool{
				features.SchedulerLibraryIntegration:     true,
				features.SchedulerLibraryDeepIntegration: false,
			},
		},
	}
	runTASScheduleTestCases(t, tasScheduleTestConfig{
		queues: queues,
		now:    now,
	}, cases)
}

// TestScheduleNoFitReasonSchedulerLibrary verifies that, when the WAS planner
// fails to place the Workload on the selected flavor, the reported
// QuotaReserved reason reflects that failure instead of a more severe quota
// failure of a previously attempted flavor.
func TestScheduleNoFitReasonSchedulerLibrary(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	singleNode := []corev1.Node{
		*testingnode.MakeNode("x1").
			Label("tas-node", "true").
			Label(corev1.LabelHostname, "x1").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:  resource.MustParse("1"),
				corev1.ResourcePods: resource.MustParse("10"),
			}).
			Ready().
			Obj(),
	}
	topology := *utiltestingapi.MakeDefaultOneLevelTopology("tas-single-level")
	// The no-fit flavor is attempted first and does not fit the quota,
	// so the tas-main test flavor is selected by the flavor assigner.
	resourceFlavors := []kueue.ResourceFlavor{
		*utiltestingapi.MakeResourceFlavor("tas-no-fit").
			NodeLabel("tas-node", "no-fit").
			TopologyName("tas-single-level").
			Obj(),
		*utiltestingapi.MakeResourceFlavor("tas-default").
			NodeLabel("tas-node", "true").
			TopologyName("tas-single-level").
			Obj(),
	}
	clusterQueues := []kueue.ClusterQueue{
		*utiltestingapi.MakeClusterQueue("tas-main").
			Annotation(kueue.PreferredAssignmentPlannerAnnotation, string(kueue.KubeSchedulerDeepIntegration)).
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("tas-no-fit").Resource(corev1.ResourceCPU, "0").Obj(),
				*utiltestingapi.MakeFlavorQuotas("tas-default").Resource(corev1.ResourceCPU, "2").Obj(),
			).
			Obj(),
	}
	queues := []kueue.LocalQueue{
		*utiltestingapi.MakeLocalQueue("tas-main", "default").ClusterQueue("tas-main").Obj(),
	}
	deepIntegrationGates := map[featuregate.Feature]bool{
		kubefeatures.GenericWorkload:             true,
		features.SchedulerLibraryIntegration:     true,
		features.SchedulerLibraryDeepIntegration: true,
	}
	eventIgnoreMessage := cmpopts.IgnoreFields(utiltesting.EventRecord{}, "Message")

	wlBase := utiltestingapi.MakeWorkload("wl", "default").
		Queue("tas-main").
		PodSets(*utiltestingapi.MakePodSet("main", 1).
			RequiredTopologyRequest(corev1.LabelHostname).
			Request(corev1.ResourceCPU, "2").
			Obj())
	wlNotReserved := func(reason, message string) kueue.Workload {
		return *wlBase.Clone().
			Condition(metav1.Condition{
				Type:               kueue.WorkloadQuotaReserved,
				Status:             metav1.ConditionFalse,
				Reason:             reason,
				Message:            message,
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
				Name: "main",
				Resources: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("2"),
				},
			}).
			Obj()
	}

	cases := map[string]tasScheduleTestCase{
		"simulation fails on the selected flavor: reported as SimulationFailed": {
			nodes:           singleNode,
			topologies:      []kueue.Topology{topology},
			resourceFlavors: resourceFlavors,
			clusterQueues:   clusterQueues,
			workloads:       []kueue.Workload{*wlBase.Clone().Obj()},
			wantWorkloads: []kueue.Workload{
				wlNotReserved(kueue.WorkloadQuotaReservedReasonSimulationFailed,
					"couldn't assign flavors to pod set main: pod: default/virtual-wl-main-0-98764, failure reason: 0/1 nodes are available: 1 Insufficient cpu."),
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-main": {"default/wl"},
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "wl", kueue.WorkloadQuotaReservedReasonSimulationFailed, corev1.EventTypeWarning).Obj(),
			},
			featureGates: deepIntegrationGates,
		},
	}
	runTASScheduleTestCases(t, tasScheduleTestConfig{
		queues: queues,
		now:    now,
	}, cases)
}

type mockWorkloadScheduler struct {
	simulator.SchedulerSimulator

	placementNode types.NodeName

	injectError error
	// swFailAt marks at and after which call should the ShceduleWorkload
	// method return an error. When not set, ShceduleWorkload will awlays succeed.
	swFailAt int

	swCallsCount int
}

func (s *mockWorkloadScheduler) FindFeasibleNodes(
	_ context.Context,
	candidates iter.Seq[simulator.Candidate],
	_ *simulator.PodRequirements,
	_ *simulator.NodeExclusionStats,
) ([]simulator.MatchedCandidate, error) {
	var feasible []simulator.MatchedCandidate
	for candidate := range candidates {
		matched, ok := candidate.(simulator.MatchedCandidate)
		if !ok {
			return nil, fmt.Errorf("failed to cast candidate %T to simulator.MatchedCandidate", candidate)
		}
		feasible = append(feasible, matched)
	}
	return feasible, nil
}

func (s *mockWorkloadScheduler) ScheduleWorkload(_ context.Context, workloadPods []*corev1.Pod, _ ...simulator.ScheduleOption) simulator.SchedulingResult {
	s.swCallsCount++
	if s.swFailAt != 0 && s.swCallsCount >= s.swFailAt {
		return simulator.SchedulingResult{Error: s.injectError}
	}
	placements := make(simulator.PodPlacements, len(workloadPods))
	for _, pod := range workloadPods {
		placements[client.ObjectKeyFromObject(pod)] = &simulator.PodPlacement{Node: s.placementNode}
	}
	return simulator.SchedulingResult{PodPlacements: placements}
}

func (s *mockWorkloadScheduler) AddPodGroup(_ context.Context, _ *schedulingv1beta1.PodGroup) error {
	return nil
}

func (s *mockWorkloadScheduler) RemovePodGroup(_ context.Context, _ *schedulingv1beta1.PodGroup) error {
	return nil
}

func (s *mockWorkloadScheduler) AddCompositePodGroup(_ context.Context, _ *schedulingv1alpha3.CompositePodGroup) error {
	return nil
}

func (s *mockWorkloadScheduler) RemoveCompositePodGroup(_ context.Context, _ *schedulingv1alpha3.CompositePodGroup) error {
	return nil
}

// TestScheduleMixedClusterQueuePlanners verifies that ClusterQueues with
// different scheduling-assignment-planner annotations coexist in a single
// scheduling cycle.
func TestScheduleMixedClusterQueuePlanners(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	topology := *utiltestingapi.MakeDefaultOneLevelTopology("tas-single-level")

	makeNode := func(name, labelVal, cpu string) corev1.Node {
		return *testingnode.MakeNode(name).
			Label("pool", labelVal).
			Label(corev1.LabelHostname, name).
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:  resource.MustParse(cpu),
				corev1.ResourcePods: resource.MustParse("10"),
			}).
			Ready().
			Obj()
	}
	nodes := []corev1.Node{
		makeNode("x-native-default", "native-default", "2"),
		makeNode("x-native-explicit", "native-explicit", "2"),
		makeNode("x-was", "was", "2"),
		makeNode("x-shared", "shared", "2"),
	}
	makeFlavor := func(name, labelVal string) kueue.ResourceFlavor {
		return *utiltestingapi.MakeResourceFlavor(name).
			NodeLabel("pool", labelVal).
			TopologyName(topology.Name).
			Obj()
	}
	resourceFlavors := []kueue.ResourceFlavor{
		makeFlavor("tas-native-default", "native-default"),
		makeFlavor("tas-native-explicit", "native-explicit"),
		makeFlavor("tas-was", "was"),
		makeFlavor("tas-shared", "shared"),
	}
	clusterQueues := []kueue.ClusterQueue{
		*utiltestingapi.MakeClusterQueue("cq-native-default").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-native-default").
				Resource(corev1.ResourceCPU, "2").Obj()).
			Obj(),
		*utiltestingapi.MakeClusterQueue("cq-native-explicit").
			Annotation(kueue.PreferredAssignmentPlannerAnnotation, string(kueue.KueueNative)).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-native-explicit").
				Resource(corev1.ResourceCPU, "2").Obj()).
			Obj(),
		*utiltestingapi.MakeClusterQueue("cq-was").
			Annotation(kueue.PreferredAssignmentPlannerAnnotation, string(kueue.KubeSchedulerDeepIntegration)).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-was").
				Resource(corev1.ResourceCPU, "2").Obj()).
			Obj(),
		*utiltestingapi.MakeClusterQueue("cq-shared-native").
			Annotation(kueue.PreferredAssignmentPlannerAnnotation, string(kueue.KueueNative)).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-shared").
				Resource(corev1.ResourceCPU, "1").Obj()).
			Obj(),
		*utiltestingapi.MakeClusterQueue("cq-shared-was").
			Annotation(kueue.PreferredAssignmentPlannerAnnotation, string(kueue.KubeSchedulerDeepIntegration)).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-shared").
				Resource(corev1.ResourceCPU, "1").Obj()).
			Obj(),
	}
	queues := []kueue.LocalQueue{
		*utiltestingapi.MakeLocalQueue("lq-native-default", "default").ClusterQueue("cq-native-default").Obj(),
		*utiltestingapi.MakeLocalQueue("lq-native-explicit", "default").ClusterQueue("cq-native-explicit").Obj(),
		*utiltestingapi.MakeLocalQueue("lq-was", "default").ClusterQueue("cq-was").Obj(),
		*utiltestingapi.MakeLocalQueue("lq-shared-native", "default").ClusterQueue("cq-shared-native").Obj(),
		*utiltestingapi.MakeLocalQueue("lq-shared-was", "default").ClusterQueue("cq-shared-was").Obj(),
	}

	makeAdmission := func(cqName kueue.ClusterQueueReference, flavorName kueue.ResourceFlavorReference, nodeName string) kueue.Admission {
		return *utiltestingapi.MakeAdmission(cqName).
			PodSets(utiltestingapi.MakePodSetAssignment("main").
				Assignment(corev1.ResourceCPU, flavorName, "1").
				TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
					Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{nodeName}, 1).Obj()).
					Obj()).
				Obj()).
			Obj()
	}
	makeWL := func(name string, lqName kueue.LocalQueueName, preferred bool) *utiltestingapi.WorkloadWrapper {
		ps := utiltestingapi.MakePodSet("main", 1).Request(corev1.ResourceCPU, "1")
		if preferred {
			ps.PreferredTopologyRequest(corev1.LabelHostname)
		} else {
			ps.RequiredTopologyRequest(corev1.LabelHostname)
		}
		return utiltestingapi.MakeWorkload(name, "default").
			Queue(lqName).
			PodSets(*ps.Obj())
	}
	admittedWL := func(base *utiltestingapi.WorkloadWrapper, cqName string, adm kueue.Admission) kueue.Workload {
		return *base.Clone().
			Admission(&adm).
			Condition(metav1.Condition{
				Type:               kueue.WorkloadQuotaReserved,
				Status:             metav1.ConditionTrue,
				Reason:             "QuotaReserved",
				Message:            fmt.Sprintf("Quota reserved in ClusterQueue %s", cqName),
				LastTransitionTime: metav1.NewTime(now),
			}).
			Condition(metav1.Condition{
				Type:               kueue.WorkloadAdmitted,
				Status:             metav1.ConditionTrue,
				Reason:             "Admitted",
				Message:            "The workload is admitted",
				LastTransitionTime: metav1.NewTime(now),
			}).
			Obj()
	}

	wlNativeDefault := makeWL("wl-native-default", "lq-native-default", true)
	wlNativeExplicit := makeWL("wl-native-explicit", "lq-native-explicit", true)
	wlWAS := makeWL("wl-was", "lq-was", false)
	wlSharedNative := makeWL("wl-shared-native", "lq-shared-native", true)
	wlSharedWAS := makeWL("wl-shared-was", "lq-shared-was", false)

	admNativeDefault := makeAdmission("cq-native-default", "tas-native-default", "x-native-default")
	admNativeExplicit := makeAdmission("cq-native-explicit", "tas-native-explicit", "x-native-explicit")
	admWAS := makeAdmission("cq-was", "tas-was", "x-was")
	admSharedNative := makeAdmission("cq-shared-native", "tas-shared", "x-shared")
	admSharedWAS := makeAdmission("cq-shared-was", "tas-shared", "x-shared")

	workloads := []kueue.Workload{
		*wlNativeDefault.Clone().Obj(),
		*wlNativeExplicit.Clone().Obj(),
		*wlWAS.Clone().Obj(),
		*wlSharedNative.Clone().Obj(),
		*wlSharedWAS.Clone().Obj(),
	}
	wantNewAssignments := map[workload.Reference]kueue.Admission{
		"default/wl-native-default":  admNativeDefault,
		"default/wl-native-explicit": admNativeExplicit,
		"default/wl-shared-native":   admSharedNative,
		"default/wl-shared-was":      admSharedWAS,
		"default/wl-was":             admWAS,
	}
	wantWorkloads := []kueue.Workload{
		admittedWL(wlNativeDefault, "cq-native-default", admNativeDefault),
		admittedWL(wlNativeExplicit, "cq-native-explicit", admNativeExplicit),
		admittedWL(wlSharedNative, "cq-shared-native", admSharedNative),
		admittedWL(wlSharedWAS, "cq-shared-was", admSharedWAS),
		admittedWL(wlWAS, "cq-was", admWAS),
	}
	wantEvents := []utiltesting.EventRecord{
		utiltesting.MakeEventRecord("default", "wl-native-default", "QuotaReserved", corev1.EventTypeNormal).Obj(),
		utiltesting.MakeEventRecord("default", "wl-native-default", "Admitted", corev1.EventTypeNormal).Obj(),
		utiltesting.MakeEventRecord("default", "wl-native-explicit", "QuotaReserved", corev1.EventTypeNormal).Obj(),
		utiltesting.MakeEventRecord("default", "wl-native-explicit", "Admitted", corev1.EventTypeNormal).Obj(),
		utiltesting.MakeEventRecord("default", "wl-was", "QuotaReserved", corev1.EventTypeNormal).Obj(),
		utiltesting.MakeEventRecord("default", "wl-was", "Admitted", corev1.EventTypeNormal).Obj(),
		utiltesting.MakeEventRecord("default", "wl-shared-native", "QuotaReserved", corev1.EventTypeNormal).Obj(),
		utiltesting.MakeEventRecord("default", "wl-shared-native", "Admitted", corev1.EventTypeNormal).Obj(),
		utiltesting.MakeEventRecord("default", "wl-shared-was", "QuotaReserved", corev1.EventTypeNormal).Obj(),
		utiltesting.MakeEventRecord("default", "wl-shared-was", "Admitted", corev1.EventTypeNormal).Obj(),
	}
	eventIgnoreMessage := cmpopts.IgnoreFields(utiltesting.EventRecord{}, "Message")

	const usingPlannerLog = "Using Planner"
	checkPlannerLogs := func(want map[workload.Reference]kueue.AssignmentPlannerName) func(t *testing.T, entries ktesting.Log) {
		return func(t *testing.T, entries ktesting.Log) {
			t.Helper()
			gotPlannerLogs := make(map[workload.Reference]kueue.AssignmentPlannerName)
			for _, entry := range entries {
				if entry.Message != usingPlannerLog {
					continue
				}
				var wlRef workload.Reference
				for i := 0; i+1 < len(entry.WithKVList); i += 2 {
					if entry.WithKVList[i] == "workload" {
						wlRef = workload.Reference(fmt.Sprint(entry.WithKVList[i+1]))
					}
				}
				var planner kueue.AssignmentPlannerName
				for i := 0; i+1 < len(entry.ParameterKVList); i += 2 {
					if entry.ParameterKVList[i] == "planner" {
						planner = kueue.AssignmentPlannerName(fmt.Sprint(entry.ParameterKVList[i+1]))
					}
				}
				if prev, exists := gotPlannerLogs[wlRef]; exists {
					t.Errorf("workload %s delegated multiple times: previous %q, new %q", wlRef, prev, planner)
				}
				gotPlannerLogs[wlRef] = planner
			}
			if diff := cmp.Diff(want, gotPlannerLogs); diff != "" {
				t.Errorf("Unexpected planner delegation logs (-want,+got):\n%s", diff)
			}
		}
	}

	makeCase := func(deepIntegration bool, wantPlannerLogs map[workload.Reference]kueue.AssignmentPlannerName) tasScheduleTestCase {
		return tasScheduleTestCase{
			featureGates: map[featuregate.Feature]bool{
				kubefeatures.GenericWorkload:             true,
				features.SchedulerLibraryIntegration:     true,
				features.SchedulerLibraryDeepIntegration: deepIntegration,
			},
			nodes:              nodes,
			topologies:         []kueue.Topology{topology},
			resourceFlavors:    resourceFlavors,
			clusterQueues:      clusterQueues,
			workloads:          workloads,
			wantWorkloads:      wantWorkloads,
			wantNewAssignments: wantNewAssignments,
			wantEvents:         wantEvents,
			eventCmpOpts:       cmp.Options{eventIgnoreMessage},
			checkLogs:          checkPlannerLogs(wantPlannerLogs),
		}
	}

	cases := map[string]tasScheduleTestCase{
		"deep integration enabled routes each CQ to its configured planner in one cycle": makeCase(
			true,
			map[workload.Reference]kueue.AssignmentPlannerName{
				"default/wl-native-default":  kueue.KueueNative,
				"default/wl-native-explicit": kueue.KueueNative,
				"default/wl-was":             kueue.KubeSchedulerDeepIntegration,
				"default/wl-shared-native":   kueue.KueueNative,
				"default/wl-shared-was":      kueue.KubeSchedulerDeepIntegration,
			},
		),
		"deep integration disabled routes all CQs to native planner in one cycle": makeCase(
			false,
			map[workload.Reference]kueue.AssignmentPlannerName{
				"default/wl-native-default":  kueue.KueueNative,
				"default/wl-native-explicit": kueue.KueueNative,
				"default/wl-was":             kueue.KueueNative,
				"default/wl-shared-native":   kueue.KueueNative,
				"default/wl-shared-was":      kueue.KueueNative,
			},
		),
	}

	runTASScheduleTestCases(t, tasScheduleTestConfig{
		queues: queues,
		now:    now,
	}, cases)
}

// TestScheduleMixedClusterQueuePreemptionAndContention verifies preemption and
// TAS placement contention when a KueueNative ClusterQueue and a
// KubeSchedulerDeepIntegration ClusterQueue coexist in a single scheduling cycle
// across both dedicated and shared ResourceFlavors.
func TestScheduleMixedClusterQueuePreemptionAndContention(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	topology := *utiltestingapi.MakeDefaultOneLevelTopology("tas-single-level")

	makeNode := func(name, labelVal, cpu string) corev1.Node {
		return *testingnode.MakeNode(name).
			Label("pool", labelVal).
			Label(corev1.LabelHostname, name).
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:  resource.MustParse(cpu),
				corev1.ResourcePods: resource.MustParse("10"),
			}).
			Ready().
			Obj()
	}
	nodes := []corev1.Node{
		makeNode("x-native", "native", "4"),
		makeNode("x-was", "was", "4"),
		makeNode("x-shared", "shared", "4"),
	}
	makeFlavor := func(name, labelVal string) kueue.ResourceFlavor {
		return *utiltestingapi.MakeResourceFlavor(name).
			NodeLabel("pool", labelVal).
			TopologyName(topology.Name).
			Obj()
	}
	resourceFlavors := []kueue.ResourceFlavor{
		makeFlavor("tas-native", "native"),
		makeFlavor("tas-was", "was"),
		makeFlavor("tas-shared", "shared"),
	}
	queues := []kueue.LocalQueue{
		*utiltestingapi.MakeLocalQueue("lq-native", "default").ClusterQueue("cq-native").Obj(),
		*utiltestingapi.MakeLocalQueue("lq-was", "default").ClusterQueue("cq-was").Obj(),
	}

	makeCQsWithQuota := func(dedicatedNativeQuota, dedicatedWasQuota, sharedPoolNativeShare, sharedPoolWasShare string) []kueue.ClusterQueue {
		preemptionPolicy := kueue.ClusterQueuePreemption{
			WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
			ReclaimWithinCohort: kueue.PreemptionPolicyAny,
			BorrowWithinCohort: &kueue.BorrowWithinCohort{
				Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
			},
		}
		return []kueue.ClusterQueue{
			*utiltestingapi.MakeClusterQueue("cq-native").
				Cohort("mixed-cohort").
				Annotation(kueue.PreferredAssignmentPlannerAnnotation, string(kueue.KueueNative)).
				Preemption(preemptionPolicy).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("tas-native").Resource(corev1.ResourceCPU, dedicatedNativeQuota).Obj(),
					*utiltestingapi.MakeFlavorQuotas("tas-shared").Resource(corev1.ResourceCPU, sharedPoolNativeShare).Obj(),
				).
				Obj(),
			*utiltestingapi.MakeClusterQueue("cq-was").
				Cohort("mixed-cohort").
				Annotation(kueue.PreferredAssignmentPlannerAnnotation, string(kueue.KubeSchedulerDeepIntegration)).
				Preemption(preemptionPolicy).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("tas-was").Resource(corev1.ResourceCPU, dedicatedWasQuota).Obj(),
					*utiltestingapi.MakeFlavorQuotas("tas-shared").Resource(corev1.ResourceCPU, sharedPoolWasShare).Obj(),
				).
				Obj(),
		}
	}

	makeAdmission := func(cqName kueue.ClusterQueueReference, flavorName kueue.ResourceFlavorReference, nodeName, cpu string) kueue.Admission {
		return *utiltestingapi.MakeAdmission(cqName).
			PodSets(utiltestingapi.MakePodSetAssignment("main").
				Assignment(corev1.ResourceCPU, flavorName, cpu).
				TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
					Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{nodeName}, 1).Obj()).
					Obj()).
				Obj()).
			Obj()
	}
	makeWL := func(name string, lqName kueue.LocalQueueName, priority int32, pool, cpu string) *utiltestingapi.WorkloadWrapper {
		return utiltestingapi.MakeWorkload(name, "default").
			UID(types.UID(name + "-uid")).
			JobUID(name + "-job-uid").
			Queue(lqName).
			Priority(priority).
			PodSets(*utiltestingapi.MakePodSet("main", 1).
				NodeSelector(map[string]string{"pool": pool}).
				RequiredTopologyRequest(corev1.LabelHostname).
				Request(corev1.ResourceCPU, cpu).
				Obj())
	}
	admittedExistingWL := func(base *utiltestingapi.WorkloadWrapper, adm kueue.Admission) *utiltestingapi.WorkloadWrapper {
		return base.Clone().
			ReserveQuotaAt(&adm, now).
			AdmittedAt(true, now)
	}
	newlyAdmittedWL := func(base *utiltestingapi.WorkloadWrapper, cqName kueue.ClusterQueueReference, adm kueue.Admission) kueue.Workload {
		return *base.Clone().
			Admission(&adm).
			Condition(metav1.Condition{
				Type:               kueue.WorkloadQuotaReserved,
				Status:             metav1.ConditionTrue,
				Reason:             "QuotaReserved",
				Message:            fmt.Sprintf("Quota reserved in ClusterQueue %s", cqName),
				LastTransitionTime: metav1.NewTime(now),
			}).
			Condition(metav1.Condition{
				Type:               kueue.WorkloadAdmitted,
				Status:             metav1.ConditionTrue,
				Reason:             "Admitted",
				Message:            "The workload is admitted",
				LastTransitionTime: metav1.NewTime(now),
			}).
			Obj()
	}
	waitingWL := func(base *utiltestingapi.WorkloadWrapper, reason, message, cpu string) kueue.Workload {
		return *base.Clone().
			Condition(metav1.Condition{
				Type:               kueue.WorkloadQuotaReserved,
				Status:             metav1.ConditionFalse,
				Reason:             reason,
				Message:            message,
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
				Name:      "main",
				Resources: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu)},
			}).
			Obj()
	}
	preemptedVictimWL := func(base *utiltestingapi.WorkloadWrapper, preemptReason, message string) kueue.Workload {
		return *base.Clone().
			Condition(metav1.Condition{
				Type:               kueue.WorkloadEvicted,
				Status:             metav1.ConditionTrue,
				Reason:             kueue.WorkloadEvictedByPreemption,
				Message:            message,
				LastTransitionTime: metav1.NewTime(now),
			}).
			Condition(metav1.Condition{
				Type:               kueue.WorkloadPreempted,
				Status:             metav1.ConditionTrue,
				Reason:             preemptReason,
				Message:            message,
				LastTransitionTime: metav1.NewTime(now),
			}).
			SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: kueue.WorkloadEvictedByPreemption, Count: 1}).
			Obj()
	}

	deepGates := map[featuregate.Feature]bool{
		kubefeatures.GenericWorkload:             true,
		features.SchedulerLibraryIntegration:     true,
		features.SchedulerLibraryDeepIntegration: true,
	}
	eventIgnoreMessage := cmpopts.IgnoreFields(utiltesting.EventRecord{}, "Message")

	// Case 1 fixtures: separate dedicated flavors, both sides preempt independently.
	sepVictimNative := admittedExistingWL(
		makeWL("victim-native", "lq-native", 1, "native", "4"),
		makeAdmission("cq-native", "tas-native", "x-native", "4"),
	)
	sepVictimWAS := admittedExistingWL(
		makeWL("victim-was", "lq-was", 1, "was", "4"),
		makeAdmission("cq-was", "tas-was", "x-was", "4"),
	)
	sepPreemptorNative := makeWL("preemptor-native", "lq-native", 10, "native", "4")
	sepPreemptorWAS := makeWL("preemptor-was", "lq-was", 10, "was", "4")

	// Case 2 fixtures: shared flavor TAS contention (no preemption), WAS higher priority.
	contWASHigh := makeWL("wl-was", "lq-was", 10, "shared", "4")
	contNativeLow := makeWL("wl-native", "lq-native", 1, "shared", "4")
	admSharedWAS4 := makeAdmission("cq-was", "tas-shared", "x-shared", "4")

	// Case 3 fixtures: shared flavor TAS contention (no preemption), Native higher priority.
	contNativeHigh := makeWL("wl-native", "lq-native", 10, "shared", "4")
	contWASLow := makeWL("wl-was", "lq-was", 1, "shared", "4")
	admSharedNative4 := makeAdmission("cq-native", "tas-shared", "x-shared", "4")

	// Case 4 fixtures: shared flavor — WAS preemptor preempts BOTH a native victim and a WAS victim.
	sharedVictimNative2 := admittedExistingWL(
		makeWL("victim-native", "lq-native", 1, "shared", "2"),
		makeAdmission("cq-native", "tas-shared", "x-shared", "2"),
	)
	sharedVictimWAS2 := admittedExistingWL(
		makeWL("victim-was", "lq-was", 1, "shared", "2"),
		makeAdmission("cq-was", "tas-shared", "x-shared", "2"),
	)
	sharedPreemptorWAS4 := makeWL("preemptor-was", "lq-was", 10, "shared", "4")

	// Case 5 fixtures: shared flavor — Native preemptor preempts BOTH a WAS victim and a native victim.
	sharedPreemptorNative4 := makeWL("preemptor-native", "lq-native", 10, "shared", "4")

	// Case 6 fixtures: shared flavor — both WAS (prio 10) and Native (prio 5) try to preempt the SAME victim.
	sharedVictimSingle4 := admittedExistingWL(
		makeWL("victim-shared", "lq-was", 1, "shared", "4"),
		makeAdmission("cq-was", "tas-shared", "x-shared", "4"),
	)
	overlapPreemptorWAS := makeWL("preemptor-was", "lq-was", 10, "shared", "4")
	overlapPreemptorNative := makeWL("preemptor-native", "lq-native", 5, "shared", "4")

	cases := map[string]tasScheduleTestCase{
		"separate flavors: native and WAS CQs independently preempt their own victims in one cycle": {
			featureGates:    deepGates,
			nodes:           nodes,
			topologies:      []kueue.Topology{topology},
			resourceFlavors: resourceFlavors,
			clusterQueues:   makeCQsWithQuota("4", "4", "2", "2"),
			workloads: []kueue.Workload{
				*sepPreemptorNative.Clone().Obj(),
				*sepPreemptorWAS.Clone().Obj(),
				*sepVictimNative.Clone().Obj(),
				*sepVictimWAS.Clone().Obj(),
			},
			wantWorkloads: []kueue.Workload{
				waitingWL(
					sepPreemptorNative,
					kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
					"couldn't assign flavors to pod set main: flavor tas-shared doesn't match node affinity, insufficient unused quota for cpu in flavor tas-native, 4 more needed. Pending the preemption of 1 workload(s)",
					"4",
				),
				waitingWL(
					sepPreemptorWAS,
					kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
					"couldn't assign flavors to pod set main: flavor tas-shared doesn't match node affinity, insufficient unused quota for cpu in flavor tas-was, 4 more needed. Pending the preemption of 1 workload(s)",
					"4",
				),
				preemptedVictimWL(
					sepVictimNative,
					kueue.InClusterQueueReason,
					"Preempted to accommodate a workload (UID: preemptor-native-uid, JobUID: preemptor-native-job-uid) due to prioritization in the ClusterQueue; preemptor path: /mixed-cohort/cq-native; preemptee path: /mixed-cohort/cq-native",
				),
				preemptedVictimWL(
					sepVictimWAS,
					kueue.InClusterQueueReason,
					"Preempted to accommodate a workload (UID: preemptor-was-uid, JobUID: preemptor-was-job-uid) due to prioritization in the ClusterQueue; preemptor path: /mixed-cohort/cq-was; preemptee path: /mixed-cohort/cq-was",
				),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-native": {"default/preemptor-native"},
				"cq-was":    {"default/preemptor-was"},
			},
			wantSimulatorPreemptions: []workload.Reference{"default/victim-native"},
			eventCmpOpts:             cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "victim-native", "EvictedDueToPreempted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "victim-native", "Preempted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "preemptor-native", "PreemptedWorkload", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "preemptor-native", kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads, corev1.EventTypeWarning).Obj(),
				utiltesting.MakeEventRecord("default", "victim-was", "EvictedDueToPreempted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "victim-was", "Preempted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "preemptor-was", "PreemptedWorkload", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "preemptor-was", kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads, corev1.EventTypeWarning).Obj(),
			},
		},
		"shared flavor TAS contention: higher-priority WAS workload admits and lower-priority native workload waits": {
			featureGates:    deepGates,
			nodes:           nodes,
			topologies:      []kueue.Topology{topology},
			resourceFlavors: resourceFlavors,
			clusterQueues:   makeCQsWithQuota("4", "4", "4", "4"),
			workloads: []kueue.Workload{
				*contNativeLow.Clone().Obj(),
				*contWASHigh.Clone().Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/wl-was": admSharedWAS4,
			},
			wantWorkloads: []kueue.Workload{
				waitingWL(
					contNativeLow,
					kueue.WorkloadQuotaReservedReasonWaitingForQuota,
					`couldn't assign flavors to pod set main: topology "tas-single-level" doesn't allow to fit any of 1 pod(s); all pods must fit in a single "kubernetes.io/hostname" topology domain. Total nodes: 1; excluded: resource "cpu": 1`,
					"4",
				),
				newlyAdmittedWL(contWASHigh, "cq-was", admSharedWAS4),
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-native": {"default/wl-native"},
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "wl-was", "QuotaReserved", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "wl-was", "Admitted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "wl-native", kueue.WorkloadQuotaReservedReasonWaitingForQuota, corev1.EventTypeWarning).Obj(),
			},
		},
		"shared flavor TAS contention: higher-priority native workload admits and lower-priority WAS workload waits": {
			featureGates:    deepGates,
			nodes:           nodes,
			topologies:      []kueue.Topology{topology},
			resourceFlavors: resourceFlavors,
			clusterQueues:   makeCQsWithQuota("4", "4", "4", "4"),
			workloads: []kueue.Workload{
				*contNativeHigh.Clone().Obj(),
				*contWASLow.Clone().Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/wl-native": admSharedNative4,
			},
			wantWorkloads: []kueue.Workload{
				newlyAdmittedWL(contNativeHigh, "cq-native", admSharedNative4),
				waitingWL(contWASLow, kueue.WorkloadQuotaReservedReasonWaitingForQuota,
					"Workload no longer fits after processing another workload", "4"),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-was": {"default/wl-was"},
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "wl-native", "QuotaReserved", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "wl-native", "Admitted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "wl-was", kueue.WorkloadQuotaReservedReasonWaitingForQuota, corev1.EventTypeWarning).Obj(),
			},
		},
		"shared flavor preemption: WAS preemptor preempts both a native victim and a WAS victim": {
			featureGates:    deepGates,
			nodes:           nodes,
			topologies:      []kueue.Topology{topology},
			resourceFlavors: resourceFlavors,
			clusterQueues:   makeCQsWithQuota("4", "4", "0", "4"),
			workloads: []kueue.Workload{
				*sharedPreemptorWAS4.Clone().Obj(),
				*sharedVictimNative2.Clone().Obj(),
				*sharedVictimWAS2.Clone().Obj(),
			},
			wantWorkloads: []kueue.Workload{
				waitingWL(
					sharedPreemptorWAS4,
					kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
					"couldn't assign flavors to pod set main: flavor tas-was doesn't match node affinity, insufficient unused quota for cpu in flavor tas-shared, 4 more needed. Pending the preemption of 2 workload(s)",
					"4",
				),
				preemptedVictimWL(
					sharedVictimNative2,
					kueue.InCohortReclaimWhileBorrowingReason,
					"Preempted to accommodate a workload (UID: preemptor-was-uid, JobUID: preemptor-was-job-uid) due to reclamation within the cohort while borrowing; preemptor path: /mixed-cohort/cq-was; preemptee path: /mixed-cohort/cq-native",
				),
				preemptedVictimWL(
					sharedVictimWAS2,
					kueue.InClusterQueueReason,
					"Preempted to accommodate a workload (UID: preemptor-was-uid, JobUID: preemptor-was-job-uid) due to prioritization in the ClusterQueue; preemptor path: /mixed-cohort/cq-was; preemptee path: /mixed-cohort/cq-was",
				),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-was": {"default/preemptor-was"},
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "victim-native", "EvictedDueToPreempted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "victim-native", "Preempted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "victim-was", "EvictedDueToPreempted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "victim-was", "Preempted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "preemptor-was", "PreemptedWorkload", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "preemptor-was", "PreemptedWorkload", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "preemptor-was", kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads, corev1.EventTypeWarning).Obj(),
			},
		},
		"shared flavor preemption: native preemptor preempts both a WAS victim and a native victim": {
			featureGates:    deepGates,
			nodes:           nodes,
			topologies:      []kueue.Topology{topology},
			resourceFlavors: resourceFlavors,
			clusterQueues:   makeCQsWithQuota("4", "4", "4", "0"),
			workloads: []kueue.Workload{
				*sharedPreemptorNative4.Clone().Obj(),
				*sharedVictimNative2.Clone().Obj(),
				*sharedVictimWAS2.Clone().Obj(),
			},
			wantWorkloads: []kueue.Workload{
				waitingWL(
					sharedPreemptorNative4,
					kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
					"couldn't assign flavors to pod set main: flavor tas-native doesn't match node affinity, insufficient unused quota for cpu in flavor tas-shared, 4 more needed. Pending the preemption of 2 workload(s)",
					"4",
				),
				preemptedVictimWL(
					sharedVictimNative2,
					kueue.InClusterQueueReason,
					"Preempted to accommodate a workload (UID: preemptor-native-uid, JobUID: preemptor-native-job-uid) due to prioritization in the ClusterQueue; preemptor path: /mixed-cohort/cq-native; preemptee path: /mixed-cohort/cq-native",
				),
				preemptedVictimWL(
					sharedVictimWAS2,
					kueue.InCohortReclaimWhileBorrowingReason,
					"Preempted to accommodate a workload (UID: preemptor-native-uid, JobUID: preemptor-native-job-uid) due to reclamation within the cohort while borrowing; preemptor path: /mixed-cohort/cq-native; preemptee path: /mixed-cohort/cq-was",
				),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-native": {"default/preemptor-native"},
			},
			wantSimulatorPreemptions: []workload.Reference{"default/victim-native", "default/victim-was"},
			eventCmpOpts:             cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "victim-native", "EvictedDueToPreempted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "victim-native", "Preempted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "victim-was", "EvictedDueToPreempted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "victim-was", "Preempted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "preemptor-native", "PreemptedWorkload", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "preemptor-native", "PreemptedWorkload", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "preemptor-native", kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads, corev1.EventTypeWarning).Obj(),
			},
		},
		"shared flavor preemption contention: WAS and native preemptors target the same victim, higher-priority WAS wins": {
			featureGates:             deepGates,
			nodes:                    nodes,
			topologies:               []kueue.Topology{topology},
			resourceFlavors:          resourceFlavors,
			clusterQueues:            makeCQsWithQuota("4", "4", "2", "2"),
			wantSimulatorPreemptions: []workload.Reference{"default/victim-shared", "default/victim-shared"},
			workloads: []kueue.Workload{
				*overlapPreemptorNative.Clone().Obj(),
				*overlapPreemptorWAS.Clone().Obj(),
				*sharedVictimSingle4.Clone().Obj(),
			},
			wantWorkloads: []kueue.Workload{
				waitingWL(
					overlapPreemptorNative,
					kueue.WorkloadQuotaReservedReasonWaitingForQuota,
					"couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor tas-shared, 4 more needed, skipping flavor tas-native as it is not found in the nomination mapping for resource cpu",
					"4",
				),
				waitingWL(
					overlapPreemptorWAS,
					kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
					"couldn't assign flavors to pod set main: flavor tas-was doesn't match node affinity, insufficient unused quota for cpu in flavor tas-shared, 4 more needed. Pending the preemption of 1 workload(s)",
					"4",
				),
				preemptedVictimWL(
					sharedVictimSingle4,
					kueue.InClusterQueueReason,
					"Preempted to accommodate a workload (UID: preemptor-was-uid, JobUID: preemptor-was-job-uid) due to prioritization in the ClusterQueue; preemptor path: /mixed-cohort/cq-was; preemptee path: /mixed-cohort/cq-was",
				),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-was": {"default/preemptor-was"},
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-native": {"default/preemptor-native"},
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "victim-shared", "EvictedDueToPreempted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "victim-shared", "Preempted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "preemptor-was", "PreemptedWorkload", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "preemptor-was", kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads, corev1.EventTypeWarning).Obj(),
				utiltesting.MakeEventRecord("default", "preemptor-native", kueue.WorkloadQuotaReservedReasonWaitingForQuota, corev1.EventTypeWarning).Obj(),
			},
		},
	}

	runTASScheduleTestCases(t, tasScheduleTestConfig{
		queues: queues,
		now:    now,
	}, cases)
}

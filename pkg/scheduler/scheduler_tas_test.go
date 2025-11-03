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
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/component-base/featuregate"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	schdcache "sigs.k8s.io/kueue/pkg/cache"
	tasindexer "sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/queue"
	"sigs.k8s.io/kueue/pkg/util/routine"
	"sigs.k8s.io/kueue/pkg/util/slices"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestScheduleForTAS(t *testing.T) {
	const (
		tasBlockLabel = "cloud.com/topology-block"
		tasRackLabel  = "cloud.provider.com/rack"
	)
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

	//        b1                   b2
	//     /      \             /      \
	//    r1       r2          r1       r2
	//  /  |  \     |           |        |
	// x2  x3  x4  x1          x5       x6
	defaultNodes := []corev1.Node{
		*testingnode.MakeNode("b1-r1-x1").
			Label("tas-node", "true").
			Label(tasBlockLabel, "b1").
			Label(tasRackLabel, "r1").
			Label(corev1.LabelHostname, "x1").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("b1-r2-x2").
			Label("tas-node", "true").
			Label(tasBlockLabel, "b1").
			Label(tasRackLabel, "r2").
			Label(corev1.LabelHostname, "x2").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("b1-r2-x3").
			Label("tas-node", "true").
			Label(tasBlockLabel, "b1").
			Label(tasRackLabel, "r2").
			Label(corev1.LabelHostname, "x3").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("b1-r2-x4").
			Label("tas-node", "true").
			Label(tasBlockLabel, "b1").
			Label(tasRackLabel, "r2").
			Label(corev1.LabelHostname, "x4").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("b2-r2-x5").
			Label("tas-node", "true").
			Label(tasBlockLabel, "b2").
			Label(tasRackLabel, "r1").
			Label(corev1.LabelHostname, "x5").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("b2-r2-x6").
			Label("tas-node", "true").
			Label(tasBlockLabel, "b2").
			Label(tasRackLabel, "r2").
			Label(corev1.LabelHostname, "x6").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("2"),
				corev1.ResourceMemory: resource.MustParse("4Gi"),
				corev1.ResourcePods:   resource.MustParse("40"),
			}).
			Ready().
			Obj(),
	}

	defaultSingleLevelTopology := *utiltesting.MakeDefaultOneLevelTopology("tas-single-level")
	defaultTwoLevelTopology := *utiltesting.MakeTopology("tas-two-level").
		Levels(tasRackLabel, corev1.LabelHostname).
		Obj()
	defaultThreeLevelTopology := *utiltesting.MakeTopology("tas-three-level").
		Levels(tasBlockLabel, tasRackLabel, corev1.LabelHostname).
		Obj()
	defaultFlavor := *utiltesting.MakeResourceFlavor("default").Obj()
	defaultTASFlavor := *utiltesting.MakeResourceFlavor("tas-default").
		NodeLabel("tas-node", "true").
		TopologyName("tas-single-level").
		Obj()
	defaultTASTwoLevelFlavor := *utiltesting.MakeResourceFlavor("tas-default").
		NodeLabel("tas-node", "true").
		TopologyName("tas-two-level").
		Obj()
	defaultTASThreeLevelFlavor := *utiltesting.MakeResourceFlavor("tas-default").
		NodeLabel("tas-node", "true").
		TopologyName("tas-three-level").
		Obj()
	defaultClusterQueue := *utiltesting.MakeClusterQueue("tas-main").
		ResourceGroup(*utiltesting.MakeFlavorQuotas("tas-default").
			Resource(corev1.ResourceCPU, "50").
			Resource(corev1.ResourceMemory, "50Gi").Obj()).
		Obj()
	defaultProvCheck := *utiltesting.MakeAdmissionCheck("prov-check").
		ControllerName(kueue.ProvisioningRequestControllerName).
		Condition(metav1.Condition{
			Type:   kueue.AdmissionCheckActive,
			Status: metav1.ConditionTrue,
		}).
		Obj()
	defaultCustomCheck := *utiltesting.MakeAdmissionCheck("custom-check").
		ControllerName("custom-admission-check-controller").
		Condition(metav1.Condition{
			Type:   kueue.AdmissionCheckActive,
			Status: metav1.ConditionTrue,
		}).
		Obj()
	clusterQueueWithProvReq := *utiltesting.MakeClusterQueue("tas-main").
		ResourceGroup(*utiltesting.MakeFlavorQuotas("tas-default").
			Resource(corev1.ResourceCPU, "50").
			Resource(corev1.ResourceMemory, "50Gi").Obj()).
		AdmissionChecks(kueue.AdmissionCheckReference(defaultProvCheck.Name)).
		Obj()
	clusterQueueWithCustomCheck := *utiltesting.MakeClusterQueue("tas-main").
		ResourceGroup(*utiltesting.MakeFlavorQuotas("tas-default").
			Resource(corev1.ResourceCPU, "50").
			Resource(corev1.ResourceMemory, "50Gi").Obj()).
		AdmissionChecks(kueue.AdmissionCheckReference(defaultCustomCheck.Name)).
		Obj()
	queues := []kueue.LocalQueue{
		*utiltesting.MakeLocalQueue("tas-main", "default").ClusterQueue("tas-main").Obj(),
	}
	eventIgnoreMessage := cmpopts.IgnoreFields(utiltesting.EventRecord{}, "Message")
	cases := map[string]struct {
		nodes           []corev1.Node
		pods            []corev1.Pod
		topologies      []kueuealpha.Topology
		admissionChecks []kueue.AdmissionCheck
		resourceFlavors []kueue.ResourceFlavor
		clusterQueues   []kueue.ClusterQueue
		workloads       []kueue.Workload

		// wantNewAssignments is a summary of all new admissions in the cache after this cycle.
		wantNewAssignments map[workload.Reference]kueue.Admission
		// wantLeft is the workload keys that are left in the queues after this cycle.
		wantLeft map[kueue.ClusterQueueReference][]workload.Reference
		// wantInadmissibleLeft is the workload keys that are left in the inadmissible state after this cycle.
		wantInadmissibleLeft map[kueue.ClusterQueueReference][]workload.Reference
		// wantEvents asserts on the events, the comparison options are passed by eventCmpOpts
		wantEvents []utiltesting.EventRecord
		// eventCmpOpts are the comparison options for the events
		eventCmpOpts cmp.Options

		featureGates []featuregate.Feature
	}{
		"workload with a PodSet of size zero": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label("tas-node", "true").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("3"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			topologies:      []kueuealpha.Topology{defaultTwoLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASTwoLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(
						*utiltesting.MakePodSet("launcher", 1).
							PreferredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltesting.MakePodSet("worker", 0).
							PreferredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "1").
							Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main").
					PodSets(
						utiltesting.MakePodSetAssignment("launcher").
							Assignment(corev1.ResourceCPU, "tas-default", "1000m").
							TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
								Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
								Obj()).
							Obj(),
						utiltesting.MakePodSetAssignment("worker").
							Assignment(corev1.ResourceCPU, "tas-default", "0").
							Count(0).
							TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).Obj()).
							Obj(),
					).
					Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "QuotaReserved", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "foo", "Admitted", corev1.EventTypeNormal).Obj(),
			},
		},
		"workload in CQ with ProvisioningRequest; second pass; baseline scenario": {
			nodes:           defaultSingleNode,
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{clusterQueueWithProvReq},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main").
							PodSets(
								utiltesting.MakePodSetAssignment("one").
									Assignment(corev1.ResourceCPU, "tas-default", "1000m").
									DelayedTopologyRequest(kueue.DelayedTopologyRequestStatePending).
									Obj(),
							).
							Obj(),
					).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "prov-check",
						State: kueue.CheckStateReady,
					}).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main").
					PodSets(
						utiltesting.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "tas-default", "1000m").
							DelayedTopologyRequest(kueue.DelayedTopologyRequestStateReady).
							TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
								Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
								Obj()).
							Obj(),
					).
					Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "Admitted", corev1.EventTypeNormal).Obj(),
			},
		},
		"workload in CQ with two TAS flavors, only the second is using Provisioning Admission Check": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label("tas-group", "reservation").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{
				*utiltesting.MakeResourceFlavor("tas-reservation").
					NodeLabel("tas-group", "reservation").
					TopologyName("tas-single-level").
					Obj(),
				*utiltesting.MakeResourceFlavor("tas-provisioning").
					NodeLabel("tas-group", "provisioning").
					TopologyName("tas-single-level").
					Obj()},
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("tas-main").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("tas-reservation").
							Resource(corev1.ResourceCPU, "1").Obj(),
						*utiltesting.MakeFlavorQuotas("tas-provisioning").
							Resource(corev1.ResourceCPU, "1").Obj()).
					AdmissionCheckStrategy(kueue.AdmissionCheckStrategyRule{
						Name: "prov-check",
						OnFlavors: []kueue.ResourceFlavorReference{
							kueue.ResourceFlavorReference("tas-provisioning"),
						},
					}).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main").
					PodSets(utiltesting.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-reservation", "1000m").
						TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "QuotaReserved", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "foo", "Admitted", corev1.EventTypeNormal).Obj(),
			},
		},
		"workload with nodeToReplace annotation; second pass; baseline scenario": {
			nodes:           defaultNodes,
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueuealpha.Topology{defaultThreeLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASThreeLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Annotations(map[string]string{kueuealpha.NodeToReplaceAnnotation: "x0"}).
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "1000m").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x0"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
					).
					Admitted(true).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main").
					PodSets(utiltesting.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "1000m").
						TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
		},
		"workload with nodeToReplace annotation; second pass; preferred; fit in different rack": {
			nodes:           defaultNodes,
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueuealpha.Topology{defaultThreeLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASThreeLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Annotations(map[string]string{kueuealpha.NodeToReplaceAnnotation: "x0"}).
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 2).
						PreferredTopologyRequest(tasRackLabel).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "1000m").
								Count(2).
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x0"}, 1).Obj()).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
					).
					Admitted(true).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main").
					PodSets(utiltesting.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "1000m").
						Count(2).
						TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 2).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
		},
		"workload with nodeToReplace annotation; second pass; preferred; no fit": {
			nodes:           defaultNodes,
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueuealpha.Topology{defaultThreeLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASThreeLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Annotations(map[string]string{kueuealpha.NodeToReplaceAnnotation: "x0"}).
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						PreferredTopologyRequest(tasRackLabel).
						Request(corev1.ResourceCPU, "3").
						Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "3000m").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x0"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
					).
					Admitted(true).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main").
					PodSets(utiltesting.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "3000m").
						TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x0"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "SecondPassFailed", corev1.EventTypeWarning).
					Message("couldn't assign flavors to pod set one: topology \"tas-three-level\" doesn't allow to fit any of 1 pod(s)").
					Obj(),
			},
		},
		"workload with nodeToReplace annotation; second pass; preferred; no fit; FailFast": {
			nodes:           defaultNodes,
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueuealpha.Topology{defaultThreeLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASThreeLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Annotations(map[string]string{kueuealpha.NodeToReplaceAnnotation: "x0"}).
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						PreferredTopologyRequest(tasRackLabel).
						Request(corev1.ResourceCPU, "3").
						Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "3000m").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x0"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
					).
					Admitted(true).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main").
					PodSets(utiltesting.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "3000m").
						TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x0"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "EvictedDueToNodeFailures", corev1.EventTypeNormal).
					Message("Workload was evicted as there was no replacement for a failed node: x0").
					Obj(),
			},
			featureGates: []featuregate.Feature{features.TASFailedNodeReplacementFailFast},
		},
		"workload with nodeToReplace annotation; second pass; required rack; fit": {
			nodes:           defaultNodes,
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueuealpha.Topology{defaultThreeLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASThreeLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Annotations(map[string]string{kueuealpha.NodeToReplaceAnnotation: "x0"}).
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 2).
						RequiredTopologyRequest(tasRackLabel).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "1000m").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x0"}, 1).Obj()).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x2"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
					).
					Admitted(true).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main").
					PodSets(utiltesting.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "1000m").
						TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x2"}, 1).Obj()).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x3"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
		},
		"workload with nodeToReplace annotation; second pass; required rack for a single node; fit": {
			nodes:           defaultNodes,
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueuealpha.Topology{defaultThreeLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASThreeLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Annotations(map[string]string{kueuealpha.NodeToReplaceAnnotation: "x0"}).
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(tasRackLabel).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "1000m").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x0"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
					).
					Admitted(true).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main").
					PodSets(utiltesting.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "1000m").
						TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
		},
		"workload with nodeToReplace annotation; second pass; required rack; no fit": {
			nodes:           defaultNodes,
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueuealpha.Topology{defaultThreeLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASThreeLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Annotations(map[string]string{kueuealpha.NodeToReplaceAnnotation: "x0"}).
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 2).
						RequiredTopologyRequest(tasRackLabel).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "1000m").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x0"}, 1).Obj()).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
					).
					Admitted(true).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main").
					PodSets(utiltesting.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "1000m").
						TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x0"}, 1).Obj()).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "SecondPassFailed", corev1.EventTypeWarning).
					Message("couldn't assign flavors to pod set one: topology \"tas-three-level\" doesn't allow to fit any of 1 pod(s)").
					Obj(),
			},
		},
		"workload with nodeToReplace annotation; second pass; two podsets": {
			nodes:           defaultNodes,
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueuealpha.Topology{defaultThreeLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASThreeLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Annotations(map[string]string{kueuealpha.NodeToReplaceAnnotation: "x0"}).
					Queue("tas-main").
					PodSets(
						*utiltesting.MakePodSet("one", 1).
							PreferredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltesting.MakePodSet("two", 1).
							PreferredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "1").
							Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main").
							PodSets(
								utiltesting.MakePodSetAssignment("one").
									Assignment(corev1.ResourceCPU, "tas-default", "1000m").
									TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
										Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x0"}, 1).Obj()).
										Obj()).
									Obj(),
								utiltesting.MakePodSetAssignment("two").
									Assignment(corev1.ResourceCPU, "tas-default", "1000m").
									TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
										Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
										Obj()).
									Obj(),
							).
							Obj(),
					).
					Admitted(true).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main").
					PodSets(
						utiltesting.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "tas-default", "1000m").
							TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
								Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x2"}, 1).Obj()).
								Obj()).
							Obj(),
						utiltesting.MakePodSetAssignment("two").
							Assignment(corev1.ResourceCPU, "tas-default", "1000m").
							TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
								Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
								Obj()).
							Obj(),
					).
					Obj(),
			},
		},
		"workload with unhealthyNode; second pass; slices; baseline": {
			nodes:           defaultNodes,
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueuealpha.Topology{defaultThreeLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASThreeLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Annotations(map[string]string{kueuealpha.NodeToReplaceAnnotation: "x2"}).
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 2).
						PreferredTopologyRequest(tasBlockLabel).
						SliceSizeTopologyRequest(2).
						SliceRequiredTopologyRequest(tasRackLabel).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main").
							PodSets(utiltesting.MakePodSetAssignment("one").Count(2).
								Assignment(corev1.ResourceCPU, "tas-default", "2000m").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x2"}, 1).Obj()).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x3"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
					).
					Admitted(true).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main").
					PodSets(utiltesting.MakePodSetAssignment("one").Count(2).
						Assignment(corev1.ResourceCPU, "tas-default", "2000m").
						TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x3"}, 1).Obj()).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x4"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
		},
		"workload with unhealthyNode; second pass; slices; unhealthy node hosts incomplete slice": {
			nodes:           defaultNodes,
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueuealpha.Topology{defaultThreeLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASThreeLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Annotations(map[string]string{kueuealpha.NodeToReplaceAnnotation: "x2"}).
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 8).
						PreferredTopologyRequest(tasBlockLabel).
						SliceSizeTopologyRequest(8).
						SliceRequiredTopologyRequest(tasRackLabel).
						Request(corev1.ResourceCPU, "250").
						Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main").
							PodSets(utiltesting.MakePodSetAssignment("one").Count(8).
								Assignment(corev1.ResourceCPU, "tas-default", "2000m").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x2"}, 4).Obj()).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x3"}, 4).Obj()).
									Obj()).
								Obj()).
							Obj(),
					).
					Admitted(true).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main").
					PodSets(utiltesting.MakePodSetAssignment("one").Count(8).
						Assignment(corev1.ResourceCPU, "tas-default", "2000m").
						TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x3"}, 4).Obj()).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x4"}, 4).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
		},
		"workload with unhealthyNode; second pass; slices; unhealthy node hosts whole slices": {
			nodes:           defaultNodes,
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueuealpha.Topology{defaultThreeLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASThreeLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Annotations(map[string]string{kueuealpha.NodeToReplaceAnnotation: "x3"}).
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 12).
						RequiredTopologyRequest(tasBlockLabel).
						SliceSizeTopologyRequest(4).
						SliceRequiredTopologyRequest(tasRackLabel).
						Request(corev1.ResourceCPU, "250m").
						Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main").
							PodSets(utiltesting.MakePodSetAssignment("one").Count(12).
								Assignment(corev1.ResourceCPU, "tas-default", "3000m").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x2"}, 4).Obj()).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x3"}, 4).Obj()).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x4"}, 4).Obj()).
									Obj()).
								Obj()).
							Obj(),
					).
					Admitted(true).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main").
					PodSets(utiltesting.MakePodSetAssignment("one").Count(12).
						Assignment(corev1.ResourceCPU, "tas-default", "3000m").
						TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 4).Obj()).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x2"}, 4).Obj()).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x4"}, 4).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
		},
		"workload with unhealthyNode; second pass; slices; unhealthy node hosts whole slice and incomplete slice": {
			nodes:           defaultNodes,
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueuealpha.Topology{defaultThreeLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASThreeLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Annotations(map[string]string{kueuealpha.NodeToReplaceAnnotation: "x1"}).
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 8).
						RequiredTopologyRequest(tasBlockLabel).
						SliceSizeTopologyRequest(2).
						SliceRequiredTopologyRequest(tasRackLabel).
						Request(corev1.ResourceCPU, "200m").
						Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main").
							PodSets(utiltesting.MakePodSetAssignment("one").Count(8).
								Assignment(corev1.ResourceCPU, "tas-default", "1600m").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 3).Obj()).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x2"}, 5).Obj()).
									Obj()).
								Obj()).
							Obj(),
					).
					Admitted(true).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main").
					PodSets(utiltesting.MakePodSetAssignment("one").Count(8).
						Assignment(corev1.ResourceCPU, "tas-default", "1600m").
						TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x2"}, 5).Obj()).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x3"}, 3).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
		},
		"workload with unhealthyNode; second pass; slices; unhealthy node hosts the whole workload": {
			nodes:           defaultNodes,
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueuealpha.Topology{defaultThreeLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASThreeLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Annotations(map[string]string{kueuealpha.NodeToReplaceAnnotation: "x1"}).
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 4).
						RequiredTopologyRequest(tasRackLabel).
						SliceSizeTopologyRequest(2).
						SliceRequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "250").
						Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main").
							PodSets(utiltesting.MakePodSetAssignment("one").Count(4).
								Assignment(corev1.ResourceCPU, "tas-default", "1000m").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 4).Obj()).
									Obj()).
								Obj()).
							Obj(),
					).
					Admitted(true).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main").
					PodSets(utiltesting.MakePodSetAssignment("one").Count(4).
						Assignment(corev1.ResourceCPU, "tas-default", "1000m").
						TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x5"}, 4).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
		},
		"workload with unhealthyNode; second pass; slices; finds correct slice domain": {
			nodes:           defaultNodes,
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueuealpha.Topology{defaultThreeLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASThreeLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Annotations(map[string]string{kueuealpha.NodeToReplaceAnnotation: "x7"}).
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 8).
						PreferredTopologyRequest(tasBlockLabel).
						SliceSizeTopologyRequest(4).
						SliceRequiredTopologyRequest(tasBlockLabel).
						Request(corev1.ResourceCPU, "500m").
						Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main").
							PodSets(utiltesting.MakePodSetAssignment("one").Count(8).
								Assignment(corev1.ResourceCPU, "tas-default", "4000m").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 2).Obj()).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x3"}, 2).Obj()).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x5"}, 2).Obj()).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x7"}, 2).Obj()).
									Obj()).
								Obj()).
							Obj(),
					).
					Admitted(true).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main").
					PodSets(utiltesting.MakePodSetAssignment("one").Count(8).
						Assignment(corev1.ResourceCPU, "tas-default", "4000m").
						TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 2).Obj()).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x3"}, 2).Obj()).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x5"}, 2).Obj()).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x6"}, 2).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
		},
		"large workload in CQ with ProvisioningRequest; second pass": {
			// In this scenario we test a workload which is using 26 out of 50
			// available units of quota, to make sure we are not double counting
			// the quota in the second pass.
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label("tas-node", "true").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("50"),
						corev1.ResourceMemory: resource.MustParse("50Gi"),
						corev1.ResourcePods:   resource.MustParse("50"),
					}).
					Ready().
					Obj(),
			},
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{clusterQueueWithProvReq},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 26).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main").
							PodSets(
								utiltesting.MakePodSetAssignment("one").
									Assignment(corev1.ResourceCPU, "tas-default", "26").
									DelayedTopologyRequest(kueue.DelayedTopologyRequestStatePending).
									Count(26).
									Obj(),
							).
							Obj(),
					).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "prov-check",
						State: kueue.CheckStateReady,
					}).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main").
					PodSets(
						utiltesting.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "tas-default", "26").
							Count(26).
							DelayedTopologyRequest(kueue.DelayedTopologyRequestStateReady).
							TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
								Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 26).Obj()).
								Obj()).
							Obj(),
					).
					Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "Admitted", corev1.EventTypeNormal).Obj(),
			},
		},
		"workload in CQ with ProvisioningRequest when two TAS flavors; second pass": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label("tas-node", "true").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("y1").
					Label("tas-node-second", "true").
					Label(corev1.LabelHostname, "y1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor,
				*utiltesting.MakeResourceFlavor("tas-second").
					NodeLabel("tas-node-second", "true").
					TopologyName("tas-single-level").
					Obj()},
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("tas-main").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("tas-default").
							Resource(corev1.ResourceCPU, "50").Obj(),
						*utiltesting.MakeFlavorQuotas("tas-second").
							Resource(corev1.ResourceCPU, "50").Obj()).
					AdmissionChecks(kueue.AdmissionCheckReference(defaultProvCheck.Name)).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main").
							PodSets(
								utiltesting.MakePodSetAssignment("one").
									Assignment(corev1.ResourceCPU, "tas-second", "1000m").
									DelayedTopologyRequest(kueue.DelayedTopologyRequestStatePending).
									Obj(),
							).
							Obj(),
					).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "prov-check",
						State: kueue.CheckStateReady,
					}).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main").
					PodSets(
						utiltesting.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "tas-second", "1000m").
							DelayedTopologyRequest(kueue.DelayedTopologyRequestStateReady).
							TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
								Domain(utiltesting.MakeTopologyDomainAssignment([]string{"y1"}, 1).Obj()).
								Obj()).
							Obj(),
					).
					Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "Admitted", corev1.EventTypeNormal).Obj(),
			},
		},
		"workload in CQ with two TAS flavors - one with ProvisioningRequest, one regular; second pass": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label("tas-node", "true").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("y1").
					Label("tas-node-second", "true").
					Label(corev1.LabelHostname, "y1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor,
				*utiltesting.MakeResourceFlavor("tas-second").
					NodeLabel("tas-node-second", "true").
					TopologyName("tas-single-level").
					Obj()},
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("tas-main").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("tas-default").
							Resource(corev1.ResourceCPU, "50").Obj(),
						*utiltesting.MakeFlavorQuotas("tas-second").
							Resource(corev1.ResourceCPU, "50").Obj()).
					AdmissionCheckStrategy(kueue.AdmissionCheckStrategyRule{
						Name: kueue.AdmissionCheckReference(defaultProvCheck.Name),
						OnFlavors: []kueue.ResourceFlavorReference{
							"tas-second",
						},
					}).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(
						*utiltesting.MakePodSet("one", 1).
							RequiredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltesting.MakePodSet("two", 1).
							RequiredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "1").
							Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main").
							PodSets(
								utiltesting.MakePodSetAssignment("one").
									Assignment(corev1.ResourceCPU, "tas-default", "1").
									TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
										Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
										Obj()).
									Obj(),
								utiltesting.MakePodSetAssignment("two").
									Assignment(corev1.ResourceCPU, "tas-second", "1").
									DelayedTopologyRequest(kueue.DelayedTopologyRequestStatePending).
									Obj(),
							).Obj(),
					).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "prov-check",
						State: kueue.CheckStateReady,
					}).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main").
					PodSets(
						utiltesting.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "tas-default", "1").
							TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
								Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
								Obj()).
							Obj(),
						utiltesting.MakePodSetAssignment("two").
							Assignment(corev1.ResourceCPU, "tas-second", "1").
							DelayedTopologyRequest(kueue.DelayedTopologyRequestStateReady).
							TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
								Domain(utiltesting.MakeTopologyDomainAssignment([]string{"y1"}, 1).Obj()).
								Obj()).
							Obj(),
					).Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "Admitted", corev1.EventTypeNormal).Obj(),
			},
		},
		"workload in CQ with ProvisioningRequest gets QuotaReserved only; implicit defaulting": {
			nodes:           []corev1.Node{},
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{clusterQueueWithProvReq},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "prov-check",
						State: kueue.CheckStatePending,
					}).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main").
					PodSets(
						utiltesting.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "tas-default", "1000m").
							DelayedTopologyRequest(kueue.DelayedTopologyRequestStatePending).
							Obj(),
					).
					Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "QuotaReserved", corev1.EventTypeNormal).Obj(),
			},
		},
		"workload in CQ with ProvisioningRequest when two TAS flavors; second pass; implicit defaulting": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label("tas-node", "true").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("y1").
					Label("tas-node-second", "true").
					Label(corev1.LabelHostname, "y1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor,
				*utiltesting.MakeResourceFlavor("tas-second").
					NodeLabel("tas-node-second", "true").
					TopologyName("tas-single-level").
					Obj()},
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("tas-main").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("tas-default").
							Resource(corev1.ResourceCPU, "50").Obj(),
						*utiltesting.MakeFlavorQuotas("tas-second").
							Resource(corev1.ResourceCPU, "50").Obj()).
					AdmissionChecks(kueue.AdmissionCheckReference(defaultProvCheck.Name)).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main").
							PodSets(
								utiltesting.MakePodSetAssignment("one").
									Assignment(corev1.ResourceCPU, "tas-second", "1000m").
									DelayedTopologyRequest(kueue.DelayedTopologyRequestStatePending).
									Obj(),
							).
							Obj(),
					).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "prov-check",
						State: kueue.CheckStateReady,
					}).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main").
					PodSets(
						utiltesting.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "tas-second", "1000m").
							DelayedTopologyRequest(kueue.DelayedTopologyRequestStateReady).
							TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
								Domain(utiltesting.MakeTopologyDomainAssignment([]string{"y1"}, 1).Obj()).
								Obj()).
							Obj(),
					).
					Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "Admitted", corev1.EventTypeNormal).Obj(),
			},
		},
		"workload in CQ with ProvisioningRequest gets QuotaReserved only": {
			nodes:           []corev1.Node{},
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{clusterQueueWithProvReq},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "prov-check",
						State: kueue.CheckStatePending,
					}).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main").
					PodSets(
						utiltesting.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "tas-default", "1000m").
							DelayedTopologyRequest(kueue.DelayedTopologyRequestStatePending).
							Obj(),
					).
					Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "QuotaReserved", corev1.EventTypeNormal).Obj(),
			},
		},
		"workload with a custom AdmissionCheck gets TAS assigned": {
			nodes:           defaultSingleNode,
			admissionChecks: []kueue.AdmissionCheck{defaultCustomCheck},
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{clusterQueueWithCustomCheck},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "custom-check",
						State: kueue.CheckStatePending,
					}).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main").
					PodSets(utiltesting.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "1000m").
						TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "QuotaReserved", corev1.EventTypeNormal).Obj(),
			},
		},
		"workload which does not specify TAS annotation uses the only TAS flavor": {
			nodes:           defaultSingleNode,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor, defaultFlavor},
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("tas-main").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("tas-default").
							Resource(corev1.ResourceCPU, "50").Obj()).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main").
					PodSets(utiltesting.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "1000m").
						TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "QuotaReserved", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "foo", "Admitted", corev1.EventTypeNormal).Obj(),
			},
		},
		"workload requiring TAS skips the non-TAS flavor": {
			nodes:           defaultSingleNode,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor, defaultFlavor},
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("tas-main").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "50").Obj(),
						*utiltesting.MakeFlavorQuotas("tas-default").
							Resource(corev1.ResourceCPU, "50").Obj()).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main").
					PodSets(utiltesting.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "1000m").
						TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "QuotaReserved", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "foo", "Admitted", corev1.EventTypeNormal).Obj(),
			},
		},
		"workload which does not need TAS skips the TAS flavor": {
			nodes:           defaultSingleNode,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor, defaultFlavor},
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("tas-main").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("tas-default").
							Resource(corev1.ResourceCPU, "50").Obj(),
						*utiltesting.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "50").Obj()).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main").
					PodSets(utiltesting.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "default", "1000m").
						Obj()).
					Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "QuotaReserved", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "foo", "Admitted", corev1.EventTypeNormal).Obj(),
			},
		},
		"workload with mixed PodSets (requiring TAS and not)": {
			nodes:           defaultSingleNode,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor, defaultFlavor},
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("tas-main").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("tas-default").
							Resource(corev1.ResourceCPU, "50").Obj(),
						*utiltesting.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "50").Obj()).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(
						*utiltesting.MakePodSet("launcher", 1).
							Request(corev1.ResourceCPU, "500m").
							Obj(),
						*utiltesting.MakePodSet("worker", 1).
							RequiredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "500m").
							Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main").
					PodSets(
						utiltesting.MakePodSetAssignment("launcher").
							Assignment(corev1.ResourceCPU, "default", "500m").
							Obj(),
						utiltesting.MakePodSetAssignment("worker").
							Assignment(corev1.ResourceCPU, "tas-default", "500m").
							TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
								Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
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
		},
		"workload required TAS gets scheduled": {
			nodes:           defaultSingleNode,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main").
					PodSets(utiltesting.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "1000m").
						TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "QuotaReserved", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "foo", "Admitted", corev1.EventTypeNormal).Obj(),
			},
		},
		"workload requests topology level which is not present in topology": {
			nodes:           defaultSingleNode,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest("cloud.com/non-existing").
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-main": {"default/foo"},
			},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "Pending", "Warning").
					Message(`couldn't assign flavors to pod set one: Flavor "tas-default" does not contain the requested level`).
					Obj(),
			},
		},
		"workload requests topology level which is only present in second flavor": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label("tas-node", "true").
					Label("cloud.com/custom-level", "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			topologies: []kueuealpha.Topology{defaultSingleLevelTopology,
				*utiltesting.MakeTopology("tas-custom-topology").
					Levels("cloud.com/custom-level").
					Obj(),
			},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor,
				*utiltesting.MakeResourceFlavor("tas-custom-flavor").
					NodeLabel("tas-node", "true").
					TopologyName("tas-custom-topology").
					Obj(),
			},
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("tas-main").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("tas-default").
							Resource(corev1.ResourceCPU, "50").Obj(),
						*utiltesting.MakeFlavorQuotas("tas-custom-flavor").
							Resource(corev1.ResourceCPU, "50").Obj()).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest("cloud.com/custom-level").
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main").
					PodSets(utiltesting.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-custom-flavor", "1").
						TopologyAssignment(utiltesting.MakeTopologyAssignment([]string{"cloud.com/custom-level"}).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "QuotaReserved", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "foo", "Admitted", corev1.EventTypeNormal).Obj(),
			},
		},
		"workload does not get scheduled as it does not fit within the node capacity": {
			nodes:           defaultSingleNode,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 2).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-main": {"default/foo"},
			},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "Pending", "Warning").
					Message(`couldn't assign flavors to pod set one: topology "tas-single-level" allows to fit only 1 out of 2 pod(s)`).
					Obj(),
			},
		},
		"workload does not get scheduled as the node capacity is already used by another TAS workload": {
			nodes:           defaultSingleNode,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("bar-admitted", "default").
					Queue("tas-main").
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "1000m").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
					).
					Admitted(true).
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-main": {"default/foo"},
			},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "Pending", "Warning").
					Message(`couldn't assign flavors to pod set one: topology "tas-single-level" doesn't allow to fit any of 1 pod(s)`).
					Obj(),
			},
		},
		"workload does not get scheduled as the node capacity is already used by a non-TAS pod": {
			nodes: defaultSingleNode,
			pods: []corev1.Pod{
				*testingpod.MakePod("test-pending", "test-ns").NodeName("x1").
					StatusPhase(corev1.PodPending).
					Request(corev1.ResourceCPU, "600m").
					Obj(),
			},
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-main": {"default/foo"},
			},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "Pending", "Warning").
					Message(`couldn't assign flavors to pod set one: topology "tas-single-level" doesn't allow to fit any of 1 pod(s)`).
					Obj(),
			},
		},
		"workload gets scheduled as the usage of TAS pods and workloads is not double-counted": {
			nodes: defaultSingleNode,
			pods: []corev1.Pod{
				*testingpod.MakePod("test-running", "test-ns").NodeName("x1").
					StatusPhase(corev1.PodRunning).
					Request(corev1.ResourceCPU, "400m").
					NodeSelector(corev1.LabelHostname, "x1").
					Label(kueuealpha.TASLabel, "true").
					Obj(),
			},
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "500m").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("bar-admitted", "default").
					Queue("tas-main").
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "400m").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
					).
					Admitted(true).
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "400m").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main").
					PodSets(utiltesting.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "500m").
						TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "QuotaReserved", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "foo", "Admitted", corev1.EventTypeNormal).Obj(),
			},
		},
		"workload gets admitted next to already admitted workload, multiple resources used": {
			nodes:           defaultSingleNode,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "500m").
						Request(corev1.ResourceMemory, "500Mi").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("bar-admitted", "default").
					Queue("tas-main").
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "500m").
								Assignment(corev1.ResourceMemory, "tas-default", "500Mi").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
					).
					Admitted(true).
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "500m").
						Request(corev1.ResourceMemory, "500Mi").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main").
					PodSets(utiltesting.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "500m").
						Assignment(corev1.ResourceMemory, "tas-default", "500Mi").
						TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "QuotaReserved", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "foo", "Admitted", corev1.EventTypeNormal).Obj(),
			},
		},
		"workload with multiple PodSets requesting the same TAS flavor; multiple levels": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label("tas-node", "true").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("3"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("y1").
					Label("tas-node", "true").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "y1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("3"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			topologies:      []kueuealpha.Topology{defaultTwoLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASTwoLevelFlavor},
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("tas-main").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("tas-default").
							Resource(corev1.ResourceCPU, "50").Obj()).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(
						*utiltesting.MakePodSet("launcher", 1).
							PreferredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltesting.MakePodSet("worker", 1).
							PreferredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "7").
							Obj()).
					Obj(),
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-main": {"default/foo"},
			},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "Pending", "Warning").
					Message(`couldn't assign flavors to pod set worker: topology "tas-two-level" doesn't allow to fit any of 1 pod(s)`).
					Obj(),
			},
		},
		"scheduling workload with multiple PodSets requesting TAS flavor and will succeed": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label("tas-node", "true").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("8"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("y1").
					Label("tas-node", "true").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "y1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("8"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			topologies:      []kueuealpha.Topology{defaultTwoLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASTwoLevelFlavor},
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("tas-main").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("tas-default").
							Resource(corev1.ResourceCPU, "16").Obj()).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(
						*utiltesting.MakePodSet("launcher", 1).
							RequiredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltesting.MakePodSet("worker", 15).
							PreferredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "1").
							Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main").
					PodSets(
						utiltesting.MakePodSetAssignment("launcher").
							Assignment(corev1.ResourceCPU, "tas-default", "1000m").
							TopologyAssignment(utiltesting.MakeTopologyAssignment([]string{corev1.LabelHostname}).
								Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
								Obj()).
							Obj(),
						utiltesting.MakePodSetAssignment("worker").
							Assignment(corev1.ResourceCPU, "tas-default", "15000m").
							Count(15).
							TopologyAssignment(utiltesting.MakeTopologyAssignment([]string{corev1.LabelHostname}).
								Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 7).Obj()).
								Domain(utiltesting.MakeTopologyDomainAssignment([]string{"y1"}, 8).Obj()).
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
		},
		"scheduling workload with multiple PodSets requesting higher level topology": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label("tas-node", "true").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("8"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("y1").
					Label("tas-node", "true").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "y1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("8"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			topologies:      []kueuealpha.Topology{defaultTwoLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASTwoLevelFlavor},
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("tas-main").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("tas-default").
							Resource(corev1.ResourceCPU, "16").Obj()).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(
						*utiltesting.MakePodSet("launcher", 1).
							RequiredTopologyRequest(tasRackLabel).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltesting.MakePodSet("worker", 15).
							RequiredTopologyRequest(tasRackLabel).
							Request(corev1.ResourceCPU, "1").
							Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main").
					PodSets(
						utiltesting.MakePodSetAssignment("launcher").
							Assignment(corev1.ResourceCPU, "tas-default", "1000m").
							TopologyAssignment(utiltesting.MakeTopologyAssignment([]string{corev1.LabelHostname}).
								Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
								Obj()).
							Obj(),
						utiltesting.MakePodSetAssignment("worker").
							Assignment(corev1.ResourceCPU, "tas-default", "15000m").
							Count(15).
							TopologyAssignment(utiltesting.MakeTopologyAssignment([]string{corev1.LabelHostname}).
								Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 7).Obj()).
								Domain(utiltesting.MakeTopologyDomainAssignment([]string{"y1"}, 8).Obj()).
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
		},
		"scheduling workload when the node for another admitted workload is deleted": {
			// Here we have the "bar-admitted" workload, which is admitted and
			// is using the "x1" node, which is deleted. Still, we have the y1
			// node which allows to schedule the "foo" workload.
			nodes: []corev1.Node{
				*testingnode.MakeNode("y1").
					Label("tas-node", "true").
					Label(corev1.LabelHostname, "y1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("tas-main").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("tas-default").
							Resource(corev1.ResourceCPU, "50").Obj()).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(
						*utiltesting.MakePodSet("one", 1).
							PreferredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Obj(),
				*utiltesting.MakeWorkload("bar-admitted", "default").
					Queue("tas-main").
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "1000m").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
					).
					Admitted(true).
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main").
					PodSets(utiltesting.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "1000m").
						TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"y1"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "QuotaReserved", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "foo", "Admitted", corev1.EventTypeNormal).Obj(),
			},
		},
		"scheduling workload on a tainted node when the toleration is on ResourceFlavor": {
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
			topologies: []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{
				*utiltesting.MakeResourceFlavor("tas-default").
					NodeLabel("tas-node", "true").
					Toleration(corev1.Toleration{
						Key:      "example.com/gpu",
						Operator: corev1.TolerationOpExists,
					}).
					TopologyName("tas-single-level").
					Obj(),
			},
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("tas-main").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("tas-default").
							Resource(corev1.ResourceCPU, "50").Obj()).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(
						*utiltesting.MakePodSet("one", 1).
							PreferredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main").
					PodSets(utiltesting.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "1000m").
						TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "QuotaReserved", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "foo", "Admitted", corev1.EventTypeNormal).Obj(),
			},
		},
		"TAS workload gets scheduled as trimmed by partial admission": {
			nodes:           defaultSingleNode,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 5).
						SetMinimumCount(1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main").
					PodSets(utiltesting.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "1").
						TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "QuotaReserved", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "foo", "Admitted", corev1.EventTypeNormal).Obj(),
			},
		},
		"workload does not get scheduled as the node capacity (.status.allocatable['pods']) is already used by non-TAS and TAS workloads": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label("tas-node", "true").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1000m"),
						corev1.ResourcePods: resource.MustParse("3"),
					}).
					Ready().
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("test-running", "test-ns").
					NodeName("x1").
					StatusPhase(corev1.PodRunning).
					Request(corev1.ResourceCPU, "300m").
					Obj(),
			},
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "300m").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("bar-admitted", "default").
					Queue("tas-main").
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "150m").
								Count(2).
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 2).Obj()).
									Obj()).
								Obj()).
							Obj(),
					).
					Admitted(true).
					PodSets(*utiltesting.MakePodSet("one", 2).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "150m").
						Obj()).
					Obj(),
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-main": {"default/foo"},
			},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "Pending", "Warning").
					Message(`couldn't assign flavors to pod set one: topology "tas-single-level" doesn't allow to fit any of 1 pod(s)`).
					Obj(),
			},
		},
		"workload with zero value request gets scheduled": {
			nodes:           defaultSingleNode,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "0").
						Request(corev1.ResourceMemory, "10Mi").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main").
					PodSets(utiltesting.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "0").
						Assignment(corev1.ResourceMemory, "tas-default", "10Mi").
						TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "QuotaReserved", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "foo", "Admitted", corev1.EventTypeNormal).Obj(),
			},
		},
		"workload with unhealthyNode annotation; second pass; preferred; no fit when using slices; FailFast": {
			featureGates:    []featuregate.Feature{features.TASFailedNodeReplacementFailFast},
			nodes:           defaultNodes,
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueuealpha.Topology{defaultThreeLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASThreeLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Annotation(kueuealpha.NodeToReplaceAnnotation, "x0").
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 2).
						PreferredTopologyRequest(tasBlockLabel).
						SliceSizeTopologyRequest(2).
						SliceRequiredTopologyRequest(tasRackLabel).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main").
							PodSets(utiltesting.MakePodSetAssignment("one").Count(2).
								Assignment(corev1.ResourceCPU, "tas-default", "2000m").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x0"}, 1).Obj()).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
					).
					Admitted(true).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main").
					PodSets(utiltesting.MakePodSetAssignment("one").Count(2).
						Assignment(corev1.ResourceCPU, "tas-default", "2000m").
						TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x0"}, 1).Obj()).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "EvictedDueToNodeFailures", corev1.EventTypeNormal).
					Message("Workload was evicted as there was no replacement for a failed node: x0").Obj(),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, true)
			for _, fg := range tc.featureGates {
				features.SetFeatureGateDuringTest(t, fg, true)
			}
			ctx, log := utiltesting.ContextWithLog(t)

			clientBuilder := utiltesting.NewClientBuilder().
				WithLists(
					&kueue.AdmissionCheckList{Items: tc.admissionChecks},
					&kueue.WorkloadList{Items: tc.workloads},
					&kueuealpha.TopologyList{Items: tc.topologies},
					&corev1.PodList{Items: tc.pods},
					&corev1.NodeList{Items: tc.nodes},
					&kueue.LocalQueueList{Items: queues}).
				WithObjects(utiltesting.MakeNamespace("default")).
				WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge}).
				WithStatusSubresource(&kueue.Workload{}, &kueue.ClusterQueue{}, &kueue.LocalQueue{})

			for _, ac := range tc.admissionChecks {
				clientBuilder = clientBuilder.WithStatusSubresource(ac.DeepCopy())
			}
			_ = tasindexer.SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder))
			cl := clientBuilder.Build()
			recorder := &utiltesting.EventRecorder{}
			cqCache := schdcache.New(cl)
			now := time.Now()
			fakeClock := testingclock.NewFakeClock(now)
			qManager := queue.NewManager(cl, cqCache, queue.WithClock(fakeClock))
			topologyByName := slices.ToMap(tc.topologies, func(i int) (kueue.TopologyReference, kueuealpha.Topology) {
				return kueue.TopologyReference(tc.topologies[i].Name), tc.topologies[i]
			})
			for _, ac := range tc.admissionChecks {
				cqCache.AddOrUpdateAdmissionCheck(log, &ac)
			}
			for _, flavor := range tc.resourceFlavors {
				cqCache.AddOrUpdateResourceFlavor(log, &flavor)
				if flavor.Spec.TopologyName != nil {
					t := topologyByName[*flavor.Spec.TopologyName]
					cqCache.AddOrUpdateTopology(log, &t)
				}
			}
			for _, cq := range tc.clusterQueues {
				if err := cqCache.AddClusterQueue(ctx, &cq); err != nil {
					t.Fatalf("Inserting clusterQueue %s in cache: %v", cq.Name, err)
				}
				if err := qManager.AddClusterQueue(ctx, &cq); err != nil {
					t.Fatalf("Inserting clusterQueue %s in manager: %v", cq.Name, err)
				}
				if err := cl.Create(ctx, &cq); err != nil {
					t.Fatalf("couldn't create the cluster queue: %v", err)
				}
			}
			for _, q := range queues {
				if err := qManager.AddLocalQueue(ctx, &q); err != nil {
					t.Fatalf("Inserting queue %s/%s in manager: %v", q.Namespace, q.Name, err)
				}
			}
			initiallyAdmittedWorkloads := sets.New[workload.Reference]()
			for _, w := range tc.workloads {
				if workload.IsAdmitted(&w) && !workload.HasNodeToReplace(&w) {
					initiallyAdmittedWorkloads.Insert(workload.Key(&w))
				}
			}
			for _, w := range tc.workloads {
				if qManager.QueueSecondPassIfNeeded(ctx, &w) {
					fakeClock.Step(time.Second)
				}
			}
			scheduler := New(qManager, cqCache, cl, recorder)
			wg := sync.WaitGroup{}
			scheduler.setAdmissionRoutineWrapper(routine.NewWrapper(
				func() { wg.Add(1) },
				func() { wg.Done() },
			))

			ctx, cancel := context.WithTimeout(ctx, queueingTimeout)
			go qManager.CleanUpOnContext(ctx)
			defer cancel()

			scheduler.schedule(ctx)
			wg.Wait()
			snapshot, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}
			gotAssignments := make(map[workload.Reference]kueue.Admission)
			for cqName, c := range snapshot.ClusterQueues() {
				for name, w := range c.Workloads {
					if initiallyAdmittedWorkloads.Has(workload.Key(w.Obj)) {
						continue
					}
					switch {
					case !workload.HasQuotaReservation(w.Obj):
						t.Fatalf("Workload %s is not admitted by a clusterQueue, but it is found as member of clusterQueue %s in the cache", name, cqName)
					case w.Obj.Status.Admission.ClusterQueue != cqName:
						t.Fatalf("Workload %s is admitted by clusterQueue %s, but it is found as member of clusterQueue %s in the cache", name, w.Obj.Status.Admission.ClusterQueue, cqName)
					default:
						gotAssignments[name] = *w.Obj.Status.Admission
					}
				}
			}
			if diff := cmp.Diff(tc.wantNewAssignments, gotAssignments, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unexpected assigned clusterQueues in cache (-want,+got):\n%s", diff)
			}
			qDump := qManager.Dump()
			if diff := cmp.Diff(tc.wantLeft, qDump, cmpDump...); diff != "" {
				t.Errorf("Unexpected elements left in the queue (-want,+got):\n%s", diff)
			}
			qDumpInadmissible := qManager.DumpInadmissible()
			if diff := cmp.Diff(tc.wantInadmissibleLeft, qDumpInadmissible, cmpDump...); diff != "" {
				t.Errorf("Unexpected elements left in inadmissible workloads (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantEvents, recorder.RecordedEvents, tc.eventCmpOpts...); diff != "" {
				t.Errorf("unexpected events (-want/+got):\n%s", diff)
			}
		})
	}
}

func TestScheduleForTASPreemption(t *testing.T) {
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
		*singleNode.Clone().Obj(),
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
	defaultSingleLevelTopology := *utiltesting.MakeDefaultOneLevelTopology("tas-single-level")
	defaultTASFlavor := *utiltesting.MakeResourceFlavor("tas-default").
		NodeLabel("tas-node", "true").
		TopologyName("tas-single-level").
		Obj()
	defaultClusterQueueWithPreemption := *utiltesting.MakeClusterQueue("tas-main").
		Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
		ResourceGroup(*utiltesting.MakeFlavorQuotas("tas-default").
			Resource(corev1.ResourceCPU, "50").
			Resource(corev1.ResourceMemory, "50Gi").Obj()).
		Obj()
	queues := []kueue.LocalQueue{
		*utiltesting.MakeLocalQueue("tas-main", "default").ClusterQueue("tas-main").Obj(),
	}
	cases := map[string]struct {
		nodes           []corev1.Node
		pods            []corev1.Pod
		topologies      []kueuealpha.Topology
		resourceFlavors []kueue.ResourceFlavor
		clusterQueues   []kueue.ClusterQueue
		workloads       []kueue.Workload
		wantWorkloads   []kueue.Workload

		// wantNewAssignments is a summary of all new admissions in the cache after this cycle.
		wantNewAssignments map[workload.Reference]kueue.Admission
		// wantLeft is the workload keys that are left in the queues after this cycle.
		wantLeft map[kueue.ClusterQueueReference][]workload.Reference
		// wantInadmissibleLeft is the workload keys that are left in the inadmissible state after this cycle.
		wantInadmissibleLeft map[kueue.ClusterQueueReference][]workload.Reference
		// wantEvents asserts on the events, the comparison options are passed by eventCmpOpts
		wantEvents []utiltesting.EventRecord
		// eventCmpOpts are the comparison options for the events
		eventCmpOpts cmp.Options
	}{
		"workload preempted due to quota is using deleted Node": {
			// In this scenario the preemption target, based on quota, is a
			// using an already deleted node (z).
			nodes:           defaultTwoNodes,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("tas-main").
					Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
					ResourceGroup(*utiltesting.MakeFlavorQuotas("tas-default").
						Resource(corev1.ResourceCPU, "10").
						Resource(corev1.ResourceMemory, "10Gi").Obj()).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					UID("wl-foo").
					JobUID("job-foo").
					Queue("tas-main").
					Priority(3).
					PodSets(*utiltesting.MakePodSet("one", 10).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("low-priority-admitted", "default").
					Queue("tas-main").
					Priority(1).
					ReserveQuotaAt(
						utiltesting.MakeAdmission("tas-main").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "5").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"z1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "5").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					UID("wl-foo").
					JobUID("job-foo").
					Queue("tas-main").
					Priority(3).
					PodSets(*utiltesting.MakePodSet("one", 10).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set one: insufficient unused quota for cpu in flavor tas-default, 5 more needed. Pending the preemption of 1 workload(s)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("10"),
						},
					}).
					Obj(),
				*utiltesting.MakeWorkload("low-priority-admitted", "default").
					Queue("tas-main").
					Priority(1).
					ReserveQuotaAt(
						utiltesting.MakeAdmission("tas-main").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "5").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"z1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "5").
						Obj()).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-foo, JobUID: job-foo) due to prioritization in the ClusterQueue",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-foo, JobUID: job-foo) due to prioritization in the ClusterQueue",
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
					Message("Preempted to accommodate a workload (UID: wl-foo, JobUID: job-foo) due to prioritization in the ClusterQueue").
					Obj(),
				utiltesting.MakeEventRecord("default", "low-priority-admitted", "Preempted", "Normal").
					Message("Preempted to accommodate a workload (UID: wl-foo, JobUID: job-foo) due to prioritization in the ClusterQueue").
					Obj(),
				utiltesting.MakeEventRecord("default", "foo", "Pending", "Warning").
					Message(`couldn't assign flavors to pod set one: insufficient unused quota for cpu in flavor tas-default, 5 more needed. Pending the preemption of 1 workload(s)`).
					Obj(),
			},
		},
		"only low priority workload is preempted": {
			// This test case demonstrates the baseline scenario where there
			// is only one low-priority workload and it gets preempted.
			nodes:           defaultSingleNode,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueWithPreemption},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					UID("wl-foo").
					JobUID("job-foo").
					Queue("tas-main").
					Priority(3).
					PodSets(*utiltesting.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "2").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("low-priority-admitted", "default").
					Queue("tas-main").
					Priority(1).
					ReserveQuotaAt(
						utiltesting.MakeAdmission("tas-main").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "5").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "5").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					UID("wl-foo").
					JobUID("job-foo").
					Queue("tas-main").
					Priority(3).
					PodSets(*utiltesting.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "2").
						Obj()).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set one: topology \"tas-single-level\" doesn't allow to fit any of 1 pod(s). Pending the preemption of 1 workload(s)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("2"),
						},
					}).
					Obj(),
				*utiltesting.MakeWorkload("low-priority-admitted", "default").
					Queue("tas-main").
					Priority(1).
					ReserveQuotaAt(
						utiltesting.MakeAdmission("tas-main").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "5").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "5").
						Obj()).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-foo, JobUID: job-foo) due to prioritization in the ClusterQueue",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-foo, JobUID: job-foo) due to prioritization in the ClusterQueue",
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
					Message("Preempted to accommodate a workload (UID: wl-foo, JobUID: job-foo) due to prioritization in the ClusterQueue").
					Obj(),
				utiltesting.MakeEventRecord("default", "low-priority-admitted", "Preempted", "Normal").
					Message("Preempted to accommodate a workload (UID: wl-foo, JobUID: job-foo) due to prioritization in the ClusterQueue").
					Obj(),
				utiltesting.MakeEventRecord("default", "foo", "Pending", "Warning").
					Message(`couldn't assign flavors to pod set one: topology "tas-single-level" doesn't allow to fit any of 1 pod(s). Pending the preemption of 1 workload(s)`).
					Obj(),
			},
		},
		"With pods count usage pressure on nodes: only low priority workload is preempted": {
			// This test case demonstrates the baseline scenario where there
			// is only one low-priority workload and it gets preempted even if node has pods count usage pressure.
			nodes: []corev1.Node{
				*singleNode.Clone().
					StatusAllocatable(corev1.ResourceList{corev1.ResourcePods: resource.MustParse("1")}).
					Obj(),
			},
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueWithPreemption},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("high-priority-waiting", "default").
					UID("wl-high-priority-waiting").
					JobUID("job-high-priority-waiting").
					Queue("tas-main").
					Priority(3).
					PodSets(*utiltesting.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "2").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("low-priority-admitted", "default").
					Queue("tas-main").
					Priority(1).
					ReserveQuotaAt(
						utiltesting.MakeAdmission("tas-main").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "5").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "5").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("high-priority-waiting", "default").
					UID("wl-high-priority-waiting").
					JobUID("job-high-priority-waiting").
					Queue("tas-main").
					Priority(3).
					PodSets(*utiltesting.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "2").
						Obj()).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set one: topology \"tas-single-level\" doesn't allow to fit any of 1 pod(s). Pending the preemption of 1 workload(s)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("2"),
						},
					}).
					Obj(),
				*utiltesting.MakeWorkload("low-priority-admitted", "default").
					Queue("tas-main").
					Priority(1).
					ReserveQuotaAt(
						utiltesting.MakeAdmission("tas-main").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "5").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "5").
						Obj()).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-high-priority-waiting, JobUID: job-high-priority-waiting) due to prioritization in the ClusterQueue",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-high-priority-waiting, JobUID: job-high-priority-waiting) due to prioritization in the ClusterQueue",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-main": {"default/high-priority-waiting"},
			},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "low-priority-admitted", "EvictedDueToPreempted", "Normal").
					Message("Preempted to accommodate a workload (UID: wl-high-priority-waiting, JobUID: job-high-priority-waiting) due to prioritization in the ClusterQueue").
					Obj(),
				utiltesting.MakeEventRecord("default", "low-priority-admitted", "Preempted", "Normal").
					Message("Preempted to accommodate a workload (UID: wl-high-priority-waiting, JobUID: job-high-priority-waiting) due to prioritization in the ClusterQueue").
					Obj(),
				utiltesting.MakeEventRecord("default", "high-priority-waiting", "Pending", "Warning").
					Message(`couldn't assign flavors to pod set one: topology "tas-single-level" doesn't allow to fit any of 1 pod(s). Pending the preemption of 1 workload(s)`).
					Obj(),
			},
		},
		"low priority workload is preempted, mid-priority workload survives": {
			// This test case demonstrates the targets are selected according
			// to priorities, similarly as for regular preemption.
			nodes:           defaultSingleNode,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueWithPreemption},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					UID("wl-foo").
					JobUID("job-foo").
					Queue("tas-main").
					Priority(3).
					PodSets(*utiltesting.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "2").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("mid-priority-admitted", "default").
					Queue("tas-main").
					Priority(2).
					ReserveQuotaAt(
						utiltesting.MakeAdmission("tas-main").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "2").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "2").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("low-priority-admitted", "default").
					Queue("tas-main").
					Priority(1).
					ReserveQuotaAt(
						utiltesting.MakeAdmission("tas-main").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "2").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "2").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					UID("wl-foo").
					JobUID("job-foo").
					Queue("tas-main").
					Priority(3).
					PodSets(*utiltesting.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "2").
						Obj()).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set one: topology \"tas-single-level\" doesn't allow to fit any of 1 pod(s). Pending the preemption of 1 workload(s)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("2"),
						},
					}).
					Obj(),
				*utiltesting.MakeWorkload("low-priority-admitted", "default").
					Queue("tas-main").
					Priority(1).
					ReserveQuotaAt(
						utiltesting.MakeAdmission("tas-main").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "2").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "2").
						Obj()).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-foo, JobUID: job-foo) due to prioritization in the ClusterQueue",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-foo, JobUID: job-foo) due to prioritization in the ClusterQueue",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltesting.MakeWorkload("mid-priority-admitted", "default").
					Queue("tas-main").
					Priority(2).
					ReserveQuotaAt(
						utiltesting.MakeAdmission("tas-main").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "2").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "2").
						Obj()).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-main": {"default/foo"},
			},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "low-priority-admitted", "EvictedDueToPreempted", "Normal").
					Message("Preempted to accommodate a workload (UID: wl-foo, JobUID: job-foo) due to prioritization in the ClusterQueue").
					Obj(),
				utiltesting.MakeEventRecord("default", "low-priority-admitted", "Preempted", "Normal").
					Message("Preempted to accommodate a workload (UID: wl-foo, JobUID: job-foo) due to prioritization in the ClusterQueue").
					Obj(),
				utiltesting.MakeEventRecord("default", "foo", "Pending", "Warning").
					Message(`couldn't assign flavors to pod set one: topology "tas-single-level" doesn't allow to fit any of 1 pod(s). Pending the preemption of 1 workload(s)`).
					Obj(),
			},
		},
		"low priority workload is preempted even though there is enough capacity, but fragmented": {
			// In this test we fill in two nodes 4/5 which leaves 2 units empty.
			// It would be enough to schedule both pods of wl3, one pod per
			// node, but the workload requires to run on a single node, thus
			// the lower priority workload is chosen as target.
			nodes:           defaultTwoNodes,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueWithPreemption},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					UID("wl-foo").
					JobUID("job-foo").
					Queue("tas-main").
					Priority(3).
					PodSets(*utiltesting.MakePodSet("one", 2).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("mid-priority-admitted", "default").
					Queue("tas-main").
					Priority(2).
					ReserveQuotaAt(
						utiltesting.MakeAdmission("tas-main").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "4").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("low-priority-admitted", "default").
					Queue("tas-main").
					Priority(1).
					ReserveQuotaAt(
						utiltesting.MakeAdmission("tas-main").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "4").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"y1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					UID("wl-foo").
					JobUID("job-foo").
					Queue("tas-main").
					Priority(3).
					PodSets(*utiltesting.MakePodSet("one", 2).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set one: topology \"tas-single-level\" allows to fit only 1 out of 2 pod(s). Pending the preemption of 1 workload(s)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("2"),
						},
					}).
					Obj(),
				*utiltesting.MakeWorkload("low-priority-admitted", "default").
					Queue("tas-main").
					Priority(1).
					ReserveQuotaAt(
						utiltesting.MakeAdmission("tas-main").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "4").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"y1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-foo, JobUID: job-foo) due to prioritization in the ClusterQueue",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-foo, JobUID: job-foo) due to prioritization in the ClusterQueue",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltesting.MakeWorkload("mid-priority-admitted", "default").
					Queue("tas-main").
					Priority(2).
					ReserveQuotaAt(
						utiltesting.MakeAdmission("tas-main").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "4").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-main": {"default/foo"},
			},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "low-priority-admitted", "EvictedDueToPreempted", "Normal").
					Message("Preempted to accommodate a workload (UID: wl-foo, JobUID: job-foo) due to prioritization in the ClusterQueue").
					Obj(),
				utiltesting.MakeEventRecord("default", "low-priority-admitted", "Preempted", "Normal").
					Message("Preempted to accommodate a workload (UID: wl-foo, JobUID: job-foo) due to prioritization in the ClusterQueue").
					Obj(),
				utiltesting.MakeEventRecord("default", "foo", "Pending", "Warning").
					Message(`couldn't assign flavors to pod set one: topology "tas-single-level" allows to fit only 1 out of 2 pod(s). Pending the preemption of 1 workload(s)`).
					Obj(),
			},
		},
		"workload with equal priority awaits for other workloads to complete": {
			// In this test case the waiting workload cannot preempt the running
			// workload as they are both with the same priority. Still, the
			// waiting workload does not let the second workload in the queue
			// to get in, as it is awaiting for the running workloads to
			// complete.
			nodes:           defaultSingleNode,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueWithPreemption},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("low-priority-which-would-fit", "default").
					Queue("tas-main").
					Priority(1).
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("mid-priority-waiting", "default").
					Queue("tas-main").
					Priority(2).
					PodSets(*utiltesting.MakePodSet("one", 2).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("mid-priority-admitted", "default").
					Queue("tas-main").
					Priority(2).
					ReserveQuotaAt(
						utiltesting.MakeAdmission("tas-main").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "4").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("low-priority-which-would-fit", "default").
					Queue("tas-main").
					Priority(1).
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("mid-priority-admitted", "default").
					Queue("tas-main").
					Priority(2).
					ReserveQuotaAt(
						utiltesting.MakeAdmission("tas-main").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "4").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("mid-priority-waiting", "default").
					Queue("tas-main").
					Priority(2).
					PodSets(*utiltesting.MakePodSet("one", 2).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set one: topology \"tas-single-level\" allows to fit only 1 out of 2 pod(s)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("2"),
						},
					}).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-main": {"default/low-priority-which-would-fit"},
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-main": {"default/mid-priority-waiting"},
			},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "mid-priority-waiting", "Pending", "Warning").
					Message(`couldn't assign flavors to pod set one: topology "tas-single-level" allows to fit only 1 out of 2 pod(s)`).
					Obj(),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, true)
			ctx, log := utiltesting.ContextWithLog(t)
			testWls := make([]kueue.Workload, 0, len(tc.workloads))
			for _, wl := range tc.workloads {
				testWls = append(testWls, *wl.DeepCopy())
			}
			clientBuilder := utiltesting.NewClientBuilder().
				WithLists(
					&kueue.WorkloadList{Items: testWls},
					&kueuealpha.TopologyList{Items: tc.topologies},
					&corev1.PodList{Items: tc.pods},
					&corev1.NodeList{Items: tc.nodes},
					&kueue.LocalQueueList{Items: queues}).
				WithObjects(
					utiltesting.MakeNamespace("default"),
				).
				WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge}).
				WithStatusSubresource(&kueue.Workload{})
			_ = tasindexer.SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder))
			cl := clientBuilder.Build()
			recorder := &utiltesting.EventRecorder{}
			cqCache := schdcache.New(cl)
			qManager := queue.NewManager(cl, cqCache)
			topologyByName := slices.ToMap(tc.topologies, func(i int) (kueue.TopologyReference, kueuealpha.Topology) {
				return kueue.TopologyReference(tc.topologies[i].Name), tc.topologies[i]
			})
			for _, flavor := range tc.resourceFlavors {
				cqCache.AddOrUpdateResourceFlavor(log, &flavor)
				if flavor.Spec.TopologyName != nil {
					t := topologyByName[*flavor.Spec.TopologyName]
					cqCache.AddOrUpdateTopology(log, &t)
				}
			}
			for _, cq := range tc.clusterQueues {
				if err := cqCache.AddClusterQueue(ctx, &cq); err != nil {
					t.Fatalf("Inserting clusterQueue %s in cache: %v", cq.Name, err)
				}
				if err := qManager.AddClusterQueue(ctx, &cq); err != nil {
					t.Fatalf("Inserting clusterQueue %s in manager: %v", cq.Name, err)
				}
				if err := cl.Create(ctx, &cq); err != nil {
					t.Fatalf("couldn't create the cluster queue: %v", err)
				}
			}
			for _, q := range queues {
				if err := qManager.AddLocalQueue(ctx, &q); err != nil {
					t.Fatalf("Inserting queue %s/%s in manager: %v", q.Namespace, q.Name, err)
				}
			}
			initiallyAdmittedWorkloads := sets.New[workload.Reference]()
			for _, w := range testWls {
				if workload.IsAdmitted(&w) {
					initiallyAdmittedWorkloads.Insert(workload.Key(&w))
				}
			}
			scheduler := New(qManager, cqCache, cl, recorder, WithClock(t, testingclock.NewFakeClock(now)))
			wg := sync.WaitGroup{}
			scheduler.setAdmissionRoutineWrapper(routine.NewWrapper(
				func() { wg.Add(1) },
				func() { wg.Done() },
			))

			ctx, cancel := context.WithTimeout(ctx, queueingTimeout)
			go qManager.CleanUpOnContext(ctx)
			defer cancel()

			scheduler.schedule(ctx)
			wg.Wait()
			snapshot, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}

			gotWorkloads := &kueue.WorkloadList{}
			err = cl.List(ctx, gotWorkloads)
			if err != nil {
				t.Fatalf("Unexpected list workloads error: %v", err)
			}

			defaultWorkloadCmpOpts := cmp.Options{
				cmpopts.EquateEmpty(),
				cmpopts.IgnoreFields(kueue.Workload{}, "ObjectMeta.ResourceVersion"),
				cmpopts.SortSlices(func(a, b metav1.Condition) bool {
					return a.Type < b.Type
				}),
			}

			if diff := cmp.Diff(tc.wantWorkloads, gotWorkloads.Items, defaultWorkloadCmpOpts); diff != "" {
				t.Errorf("Unexpected scheduled workloads (-want,+got):\n%s", diff)
			}

			gotAssignments := make(map[workload.Reference]kueue.Admission)
			for cqName, c := range snapshot.ClusterQueues() {
				for name, w := range c.Workloads {
					if initiallyAdmittedWorkloads.Has(workload.Key(w.Obj)) {
						continue
					}
					switch {
					case !workload.HasQuotaReservation(w.Obj):
						t.Fatalf("Workload %s is not admitted by a clusterQueue, but it is found as member of clusterQueue %s in the cache", name, cqName)
					case w.Obj.Status.Admission.ClusterQueue != cqName:
						t.Fatalf("Workload %s is admitted by clusterQueue %s, but it is found as member of clusterQueue %s in the cache", name, w.Obj.Status.Admission.ClusterQueue, cqName)
					default:
						gotAssignments[name] = *w.Obj.Status.Admission
					}
				}
			}
			if diff := cmp.Diff(tc.wantNewAssignments, gotAssignments, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unexpected assigned clusterQueues in cache (-want,+got):\n%s", diff)
			}
			qDump := qManager.Dump()
			if diff := cmp.Diff(tc.wantLeft, qDump, cmpDump...); diff != "" {
				t.Errorf("Unexpected elements left in the queue (-want,+got):\n%s", diff)
			}
			qDumpInadmissible := qManager.DumpInadmissible()
			if diff := cmp.Diff(tc.wantInadmissibleLeft, qDumpInadmissible, cmpDump...); diff != "" {
				t.Errorf("Unexpected elements left in inadmissible workloads (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantEvents, recorder.RecordedEvents, tc.eventCmpOpts...); diff != "" {
				t.Errorf("unexpected events (-want/+got):\n%s", diff)
			}
		})
	}
}

func TestScheduleForTASCohorts(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	defaultNodeX1 := *testingnode.MakeNode("x1").
		Label("tas-node", "true").
		Label(corev1.LabelHostname, "x1").
		StatusAllocatable(corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("3"),
			corev1.ResourceMemory: resource.MustParse("5Gi"),
			corev1.ResourcePods:   resource.MustParse("10"),
		}).
		Ready().
		Obj()
	defaultNodeY1 := *testingnode.MakeNode("y1").
		Label("tas-node", "true").
		Label(corev1.LabelHostname, "y1").
		StatusAllocatable(corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("5"),
			corev1.ResourceMemory: resource.MustParse("3Gi"),
			corev1.ResourcePods:   resource.MustParse("10"),
		}).
		Ready().
		Obj()
	defaultTwoNodes := []corev1.Node{defaultNodeX1, defaultNodeY1}
	defaultSingleLevelTopology := *utiltesting.MakeDefaultOneLevelTopology("tas-single-level")
	defaultTASFlavor := *utiltesting.MakeResourceFlavor("tas-default").
		NodeLabel("tas-node", "true").
		TopologyName("tas-single-level").
		Obj()
	defaultClusterQueueA := *utiltesting.MakeClusterQueue("tas-cq-a").
		Cohort("tas-cohort-main").
		Preemption(kueue.ClusterQueuePreemption{
			WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
			ReclaimWithinCohort: kueue.PreemptionPolicyAny,
		}).
		ResourceGroup(*utiltesting.MakeFlavorQuotas("tas-default").
			Resource(corev1.ResourceCPU, "4").
			Resource(corev1.ResourceMemory, "4Gi").Obj()).
		Obj()
	defaultClusterQueueB := *utiltesting.MakeClusterQueue("tas-cq-b").
		Cohort("tas-cohort-main").
		Preemption(kueue.ClusterQueuePreemption{
			ReclaimWithinCohort: kueue.PreemptionPolicyAny,
		}).
		ResourceGroup(*utiltesting.MakeFlavorQuotas("tas-default").
			Resource(corev1.ResourceCPU, "4").
			Resource(corev1.ResourceMemory, "4Gi").Obj()).
		Obj()
	defaultClusterQueueC := *utiltesting.MakeClusterQueue("tas-cq-c").
		Cohort("tas-cohort-main").
		Preemption(kueue.ClusterQueuePreemption{
			ReclaimWithinCohort: kueue.PreemptionPolicyAny,
		}).
		ResourceGroup(*utiltesting.MakeFlavorQuotas("tas-default").
			Resource(corev1.ResourceCPU, "4").
			Resource(corev1.ResourceMemory, "4Gi").Obj()).
		Obj()
	queues := []kueue.LocalQueue{
		*utiltesting.MakeLocalQueue("tas-lq-a", "default").ClusterQueue("tas-cq-a").Obj(),
		*utiltesting.MakeLocalQueue("tas-lq-b", "default").ClusterQueue("tas-cq-b").Obj(),
		*utiltesting.MakeLocalQueue("tas-lq-c", "default").ClusterQueue("tas-cq-c").Obj(),
	}
	eventIgnoreMessage := cmpopts.IgnoreFields(utiltesting.EventRecord{}, "Message")
	cases := map[string]struct {
		nodes           []corev1.Node
		pods            []corev1.Pod
		topologies      []kueuealpha.Topology
		resourceFlavors []kueue.ResourceFlavor
		clusterQueues   []kueue.ClusterQueue
		workloads       []kueue.Workload
		wantWorkloads   []kueue.Workload

		// wantNewAssignments is a summary of all new admissions in the cache after this cycle.
		wantNewAssignments map[workload.Reference]kueue.Admission
		// wantLeft is the workload keys that are left in the queues after this cycle.
		wantLeft map[kueue.ClusterQueueReference][]workload.Reference
		// wantInadmissibleLeft is the workload keys that are left in the inadmissible state after this cycle.
		wantInadmissibleLeft map[kueue.ClusterQueueReference][]workload.Reference
		// wantEvents asserts on the events, the comparison options are passed by eventCmpOpts
		wantEvents []utiltesting.EventRecord
		// eventCmpOpts are the comparison options for the events
		eventCmpOpts cmp.Options
	}{
		"workload which requires borrowing gets scheduled": {
			nodes:           defaultTwoNodes,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueA, defaultClusterQueueB},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "default").
					Queue("tas-lq-a").
					PodSets(*utiltesting.MakePodSet("one", 6).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "default").
					Queue("tas-lq-a").
					PodSets(*utiltesting.MakePodSet("one", 6).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue tas-cq-a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltesting.MakeAdmission("tas-cq-a").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Count(6).
								Assignment(corev1.ResourceCPU, "tas-default", "6").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"y1"}, 5).Obj()).
									Obj()).
								Obj()).
							Obj(),
					).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/a1": *utiltesting.MakeAdmission("tas-cq-a").
					PodSets(utiltesting.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "6").
						Count(6).
						TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"y1"}, 5).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "a1", "QuotaReserved", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "a1", "Admitted", corev1.EventTypeNormal).Obj(),
			},
		},
		"reclaim within cohort; single borrowing workload gets preempted": {
			// This is a baseline scenario for reclamation within cohort where
			// a single borrowing workload gets preempted.
			nodes:           defaultTwoNodes,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueA, defaultClusterQueueB},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1-admitted", "default").
					Queue("tas-lq-a").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("tas-cq-a").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "5").
								Count(5).
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 5).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltesting.MakePodSet("one", 5).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b1", "default").
					UID("wl-b1").
					JobUID("job-b1").
					Queue("tas-lq-b").
					PodSets(*utiltesting.MakePodSet("one", 4).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1-admitted", "default").
					Queue("tas-lq-a").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("tas-cq-a").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "5").
								Count(5).
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 5).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltesting.MakePodSet("one", 5).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-b1, JobUID: job-b1) due to reclamation within the cohort",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-b1, JobUID: job-b1) due to reclamation within the cohort",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltesting.MakeWorkload("b1", "default").
					UID("wl-b1").
					JobUID("job-b1").
					Queue("tas-lq-b").
					PodSets(*utiltesting.MakePodSet("one", 4).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set one: insufficient unused quota for cpu in flavor tas-default, 1 more needed. Pending the preemption of 1 workload(s)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("4"),
						},
					}).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-cq-b": {"default/b1"},
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "b1", "Pending", corev1.EventTypeWarning).Obj(),
				utiltesting.MakeEventRecord("default", "a1-admitted", "Preempted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "a1-admitted", "EvictedDueToPreempted", corev1.EventTypeNormal).Obj(),
			},
		},
		"reclaim within cohort; single workload is preempted out three candidates": {
			// In this scenario the CQA is borrowing and has three candidates.
			// The test demonstrates the heuristic to select only a subset of
			// workloads that need to be preempted.
			nodes:           defaultTwoNodes,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueA, defaultClusterQueueB},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1-admitted", "default").
					Queue("tas-lq-a").
					Priority(1).
					ReserveQuotaAt(
						utiltesting.MakeAdmission("tas-cq-a").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "5").
								Count(5).
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 5).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltesting.MakePodSet("one", 5).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("a2-admitted", "default").
					Queue("tas-lq-a").
					Priority(2).
					ReserveQuotaAt(
						utiltesting.MakeAdmission("tas-cq-a").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "2").
								Count(2).
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"y1"}, 2).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltesting.MakePodSet("one", 2).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("a3-admitted", "default").
					Queue("tas-lq-a").
					Priority(2).
					ReserveQuotaAt(
						utiltesting.MakeAdmission("tas-cq-a").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "1").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"y1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b1", "default").
					UID("wl-b1").
					JobUID("job-b1").
					Queue("tas-lq-b").
					Priority(3).
					PodSets(*utiltesting.MakePodSet("one", 3).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1-admitted", "default").
					Queue("tas-lq-a").
					Priority(1).
					ReserveQuotaAt(
						utiltesting.MakeAdmission("tas-cq-a").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "5").
								Count(5).
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 5).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltesting.MakePodSet("one", 5).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-b1, JobUID: job-b1) due to reclamation within the cohort",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-b1, JobUID: job-b1) due to reclamation within the cohort",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltesting.MakeWorkload("a2-admitted", "default").
					Queue("tas-lq-a").
					Priority(2).
					ReserveQuotaAt(
						utiltesting.MakeAdmission("tas-cq-a").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "2").
								Count(2).
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"y1"}, 2).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltesting.MakePodSet("one", 2).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("a3-admitted", "default").
					Queue("tas-lq-a").
					Priority(2).
					ReserveQuotaAt(
						utiltesting.MakeAdmission("tas-cq-a").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "1").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"y1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b1", "default").
					UID("wl-b1").
					JobUID("job-b1").
					Queue("tas-lq-b").
					Priority(3).
					PodSets(*utiltesting.MakePodSet("one", 3).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set one: insufficient unused quota for cpu in flavor tas-default, 3 more needed. Pending the preemption of 1 workload(s)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("3"),
						},
					}).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-cq-b": {"default/b1"},
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "a1-admitted", "EvictedDueToPreempted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "a1-admitted", "Preempted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "b1", "Pending", corev1.EventTypeWarning).Obj(),
			},
		},
		"reclaim within cohort; preempting with partial admission": {
			// The new workload requires 4 units of CPU and memory on a single
			// node. There is no such node, so it gets schedule after partial
			// admission to 3 units of CPU and memory. It preempts one workload
			// to fit.
			nodes:           defaultTwoNodes,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueA, defaultClusterQueueB},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1-admitted", "default").
					Queue("tas-lq-a").
					Priority(3).
					ReserveQuotaAt(
						utiltesting.MakeAdmission("tas-cq-a").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "5").
								Count(5).
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"y1"}, 5).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltesting.MakePodSet("one", 4).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("a2-admitted", "default").
					Queue("tas-lq-a").
					Priority(2).
					ReserveQuotaAt(
						utiltesting.MakeAdmission("tas-cq-a").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "3").
								Count(3).
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 3).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltesting.MakePodSet("one", 3).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b1", "default").
					UID("wl-b1").
					JobUID("job-b1").
					Queue("tas-lq-b").
					Priority(3).
					PodSets(*utiltesting.MakePodSet("one", 4).
						SetMinimumCount(3).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Request(corev1.ResourceMemory, "1").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1-admitted", "default").
					Queue("tas-lq-a").
					Priority(3).
					ReserveQuotaAt(
						utiltesting.MakeAdmission("tas-cq-a").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "5").
								Count(5).
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"y1"}, 5).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltesting.MakePodSet("one", 4).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("a2-admitted", "default").
					Queue("tas-lq-a").
					Priority(2).
					ReserveQuotaAt(
						utiltesting.MakeAdmission("tas-cq-a").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "3").
								Count(3).
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 3).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltesting.MakePodSet("one", 3).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-b1, JobUID: job-b1) due to reclamation within the cohort",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-b1, JobUID: job-b1) due to reclamation within the cohort",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltesting.MakeWorkload("b1", "default").
					UID("wl-b1").
					JobUID("job-b1").
					Queue("tas-lq-b").
					Priority(3).
					PodSets(*utiltesting.MakePodSet("one", 4).
						SetMinimumCount(3).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Request(corev1.ResourceMemory, "1").
						Obj()).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set one: insufficient unused quota for cpu in flavor tas-default, 2 more needed. Pending the preemption of 1 workload(s)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("4"),
							corev1.ResourceMemory: resource.MustParse("4"),
						},
					}).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-cq-b": {"default/b1"},
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "a2-admitted", "EvictedDueToPreempted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "a2-admitted", "Preempted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "b1", "Pending", corev1.EventTypeWarning).Obj(),
			},
		},
		"reclaim within cohort; capacity reserved by preempting workload does not allow to schedule last workload": {
			// The c1 workload is initially categorized as Fit, because there
			// was one CPU unit empty. However, the preempting workload b1 books
			// this capacity.
			nodes:           defaultTwoNodes,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueA, defaultClusterQueueB, defaultClusterQueueC},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1-admitted", "default").
					Queue("tas-lq-a").
					Priority(2).
					ReserveQuotaAt(
						utiltesting.MakeAdmission("tas-cq-a").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "5").
								Count(5).
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 3).Obj()).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"y1"}, 2).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltesting.MakePodSet("one", 5).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("a2-admitted", "default").
					Queue("tas-lq-a").
					Priority(1).
					ReserveQuotaAt(
						utiltesting.MakeAdmission("tas-cq-a").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "1").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"y1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("a3-admitted", "default").
					Queue("tas-lq-a").
					Priority(1).
					ReserveQuotaAt(
						utiltesting.MakeAdmission("tas-cq-a").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "1").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"y1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b1", "default").
					UID("wl-b1").
					JobUID("job-b1").
					Queue("tas-lq-b").
					Priority(3).
					PodSets(*utiltesting.MakePodSet("one", 3).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("c1", "default").
					Queue("tas-lq-c").
					Priority(1).
					PodSets(*utiltesting.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1-admitted", "default").
					Queue("tas-lq-a").
					Priority(2).
					ReserveQuotaAt(
						utiltesting.MakeAdmission("tas-cq-a").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "5").
								Count(5).
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 3).Obj()).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"y1"}, 2).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltesting.MakePodSet("one", 5).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("a2-admitted", "default").
					Queue("tas-lq-a").
					Priority(1).
					ReserveQuotaAt(
						utiltesting.MakeAdmission("tas-cq-a").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "1").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"y1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-b1, JobUID: job-b1) due to reclamation within the cohort",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-b1, JobUID: job-b1) due to reclamation within the cohort",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltesting.MakeWorkload("a3-admitted", "default").
					Queue("tas-lq-a").
					Priority(1).
					ReserveQuotaAt(
						utiltesting.MakeAdmission("tas-cq-a").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "1").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"y1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-b1, JobUID: job-b1) due to reclamation within the cohort",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-b1, JobUID: job-b1) due to reclamation within the cohort",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltesting.MakeWorkload("b1", "default").
					UID("wl-b1").
					JobUID("job-b1").
					Queue("tas-lq-b").
					Priority(3).
					PodSets(*utiltesting.MakePodSet("one", 3).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set one: topology \"tas-single-level\" allows to fit only 1 out of 3 pod(s). Pending the preemption of 2 workload(s)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("3"),
						},
					}).
					Obj(),
				*utiltesting.MakeWorkload("c1", "default").
					Queue("tas-lq-c").
					Priority(1).
					PodSets(*utiltesting.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "Workload no longer fits after processing another workload",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("1"),
						},
					}).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-cq-b": {"default/b1"},
				"tas-cq-c": {"default/c1"},
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "a2-admitted", "EvictedDueToPreempted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "a3-admitted", "EvictedDueToPreempted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "b1", "Pending", corev1.EventTypeWarning).Obj(),
				utiltesting.MakeEventRecord("default", "c1", "Pending", corev1.EventTypeWarning).Obj(),
				utiltesting.MakeEventRecord("default", "a2-admitted", "Preempted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "a3-admitted", "Preempted", corev1.EventTypeNormal).Obj(),
			},
		},
		"two small workloads considered; both get scheduled on different nodes": {
			nodes:           defaultTwoNodes,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueA, defaultClusterQueueB},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "default").
					Priority(2).
					Queue("tas-lq-a").
					PodSets(*utiltesting.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "5").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b1", "default").
					Priority(1).
					Queue("tas-lq-b").
					PodSets(*utiltesting.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceMemory, "5").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "default").
					Priority(2).
					Queue("tas-lq-a").
					PodSets(*utiltesting.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "5").
						Obj()).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue tas-cq-a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltesting.MakeAdmission("tas-cq-a").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Count(1).
								Assignment(corev1.ResourceCPU, "tas-default", "5").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"y1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
					).
					Obj(),
				*utiltesting.MakeWorkload("b1", "default").
					Priority(1).
					Queue("tas-lq-b").
					PodSets(*utiltesting.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceMemory, "5").
						Obj()).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue tas-cq-b",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltesting.MakeAdmission("tas-cq-b").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Count(1).
								Assignment(corev1.ResourceMemory, "tas-default", "5").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
					).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/a1": *utiltesting.MakeAdmission("tas-cq-a").
					PodSets(utiltesting.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "5").
						TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"y1"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
				"default/b1": *utiltesting.MakeAdmission("tas-cq-b").
					PodSets(utiltesting.MakePodSetAssignment("one").
						Assignment(corev1.ResourceMemory, "tas-default", "5").
						TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "b1", "QuotaReserved", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "b1", "Admitted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "a1", "QuotaReserved", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "a1", "Admitted", corev1.EventTypeNormal).Obj(),
			},
		},
		"two small workloads considered; both get scheduled on the same node": {
			nodes:           defaultTwoNodes,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueA, defaultClusterQueueB},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "default").
					Priority(2).
					Queue("tas-lq-a").
					PodSets(*utiltesting.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b1", "default").
					Priority(1).
					Queue("tas-lq-b").
					PodSets(*utiltesting.MakePodSet("one", 2).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "default").
					Priority(2).
					Queue("tas-lq-a").
					PodSets(*utiltesting.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue tas-cq-a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltesting.MakeAdmission("tas-cq-a").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Count(1).
								Assignment(corev1.ResourceCPU, "tas-default", "1").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
					).
					Obj(),
				*utiltesting.MakeWorkload("b1", "default").
					Priority(1).
					Queue("tas-lq-b").
					PodSets(*utiltesting.MakePodSet("one", 2).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue tas-cq-b",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltesting.MakeAdmission("tas-cq-b").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Count(2).
								Assignment(corev1.ResourceCPU, "tas-default", "2").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 2).Obj()).
									Obj()).
								Obj()).
							Obj(),
					).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/a1": *utiltesting.MakeAdmission("tas-cq-a").
					PodSets(utiltesting.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "1").
						TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
				"default/b1": *utiltesting.MakeAdmission("tas-cq-b").
					PodSets(utiltesting.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "2").
						Count(2).
						TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 2).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "b1", "QuotaReserved", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "b1", "Admitted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "a1", "QuotaReserved", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "a1", "Admitted", corev1.EventTypeNormal).Obj(),
			},
		},
		"two small workloads considered; there is only space for one of them on the initial node": {
			nodes:           defaultTwoNodes,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueA, defaultClusterQueueB},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "default").
					Priority(2).
					Queue("tas-lq-a").
					PodSets(*utiltesting.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b1", "default").
					Priority(1).
					Queue("tas-lq-b").
					PodSets(*utiltesting.MakePodSet("one", 3).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "default").
					Priority(2).
					Queue("tas-lq-a").
					PodSets(*utiltesting.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue tas-cq-a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltesting.MakeAdmission("tas-cq-a").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Count(1).
								Assignment(corev1.ResourceCPU, "tas-default", "1").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
					).
					Obj(),
				*utiltesting.MakeWorkload("b1", "default").
					Priority(1).
					Queue("tas-lq-b").
					PodSets(*utiltesting.MakePodSet("one", 3).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "Workload no longer fits after processing another workload",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("3"),
						},
					}).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/a1": *utiltesting.MakeAdmission("tas-cq-a").
					PodSets(utiltesting.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "1").
						TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-cq-b": {"default/b1"},
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "b1", "Pending", corev1.EventTypeWarning).Obj(),
				utiltesting.MakeEventRecord("default", "a1", "QuotaReserved", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "a1", "Admitted", corev1.EventTypeNormal).Obj(),
			},
		},
		"two workloads considered; there is enough space only for the first": {
			nodes:           defaultTwoNodes,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueA, defaultClusterQueueB},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "default").
					Priority(2).
					Queue("tas-lq-a").
					PodSets(*utiltesting.MakePodSet("one", 5).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b1", "default").
					Priority(1).
					Queue("tas-lq-b").
					PodSets(*utiltesting.MakePodSet("one", 5).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "default").
					Priority(2).
					Queue("tas-lq-a").
					PodSets(*utiltesting.MakePodSet("one", 5).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue tas-cq-a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltesting.MakeAdmission("tas-cq-a").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Count(5).
								Assignment(corev1.ResourceCPU, "tas-default", "5").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"y1"}, 5).Obj()).
									Obj()).
								Obj()).
							Obj(),
					).
					Obj(),
				*utiltesting.MakeWorkload("b1", "default").
					Priority(1).
					Queue("tas-lq-b").
					PodSets(*utiltesting.MakePodSet("one", 5).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "Workload no longer fits after processing another workload",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("5"),
						},
					}).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-cq-b": {"default/b1"},
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/a1": *utiltesting.MakeAdmission("tas-cq-a").
					PodSets(utiltesting.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "5").
						Count(5).
						TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"y1"}, 5).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "b1", "Pending", corev1.EventTypeWarning).Obj(),
				utiltesting.MakeEventRecord("default", "a1", "QuotaReserved", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "a1", "Admitted", corev1.EventTypeNormal).Obj(),
			},
		},
		"two workloads considered; both overlapping in the initial flavor assignment": {
			// Both of the workloads require 2 CPU units which will make them
			// both target the x1 node which is smallest in terms of CPU.
			nodes:           defaultTwoNodes,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueA, defaultClusterQueueB},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "default").
					Priority(2).
					Queue("tas-lq-a").
					PodSets(*utiltesting.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "2").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b1", "default").
					Priority(1).
					Queue("tas-lq-b").
					PodSets(*utiltesting.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "2").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "default").
					Priority(2).
					Queue("tas-lq-a").
					PodSets(*utiltesting.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "2").
						Obj()).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue tas-cq-a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltesting.MakeAdmission("tas-cq-a").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Count(1).
								Assignment(corev1.ResourceCPU, "tas-default", "2").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
					).
					Obj(),
				*utiltesting.MakeWorkload("b1", "default").
					Priority(1).
					Queue("tas-lq-b").
					PodSets(*utiltesting.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "2").
						Obj()).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "Workload no longer fits after processing another workload",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("2"),
						},
					}).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/a1": *utiltesting.MakeAdmission("tas-cq-a").
					PodSets(utiltesting.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "2").
						TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-cq-b": {"default/b1"},
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "a1", "QuotaReserved", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "a1", "Admitted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "b1", "Pending", corev1.EventTypeWarning).Obj(),
			},
		},
		"preempting workload with targets reserves capacity so that lower priority workload cannot use it": {
			nodes:           []corev1.Node{defaultNodeY1},
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues: []kueue.ClusterQueue{*utiltesting.MakeClusterQueue("tas-cq-a").
				Cohort("tas-cohort-main").
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				}).
				ResourceGroup(*utiltesting.MakeFlavorQuotas("tas-default").
					Resource(corev1.ResourceCPU, "8", "0").
					Resource(corev1.ResourceMemory, "4Gi", "0").Obj()).
				Obj(), defaultClusterQueueB},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1-admitted", "default").
					Queue("tas-lq-a").
					Priority(1).
					ReserveQuotaAt(
						utiltesting.MakeAdmission("tas-cq-a").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "2").
								Count(2).
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"y1"}, 2).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltesting.MakePodSet("one", 2).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("a2", "default").
					UID("wl-a2").
					JobUID("job-a2").
					Queue("tas-lq-a").
					Priority(2).
					PodSets(*utiltesting.MakePodSet("one", 4).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b1", "default").
					Queue("tas-lq-b").
					Priority(1).
					PodSets(*utiltesting.MakePodSet("one", 3).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1-admitted", "default").
					Queue("tas-lq-a").
					Priority(1).
					ReserveQuotaAt(
						utiltesting.MakeAdmission("tas-cq-a").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "2").
								Count(2).
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"y1"}, 2).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltesting.MakePodSet("one", 2).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-a2, JobUID: job-a2) due to prioritization in the ClusterQueue",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-a2, JobUID: job-a2) due to prioritization in the ClusterQueue",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltesting.MakeWorkload("a2", "default").
					UID("wl-a2").
					JobUID("job-a2").
					Queue("tas-lq-a").
					Priority(2).
					PodSets(*utiltesting.MakePodSet("one", 4).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set one: topology \"tas-single-level\" allows to fit only 3 out of 4 pod(s). Pending the preemption of 1 workload(s)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("4"),
						},
					}).
					Obj(),
				*utiltesting.MakeWorkload("b1", "default").
					Queue("tas-lq-b").
					Priority(1).
					PodSets(*utiltesting.MakePodSet("one", 3).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "Workload no longer fits after processing another workload",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("3"),
						},
					}).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-cq-a": {"default/a2"},
				"tas-cq-b": {"default/b1"},
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "a1-admitted", "EvictedDueToPreempted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "a2", "Pending", corev1.EventTypeWarning).Obj(),
				utiltesting.MakeEventRecord("default", "a1-admitted", "Preempted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "b1", "Pending", corev1.EventTypeWarning).Obj(),
			},
		},
		"preempting workload without targets reserves capacity so that lower priority workload cannot use it": {
			nodes:           []corev1.Node{defaultNodeY1},
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues: []kueue.ClusterQueue{*utiltesting.MakeClusterQueue("tas-cq-a").
				Cohort("tas-cohort-main").
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
				}).
				ResourceGroup(*utiltesting.MakeFlavorQuotas("tas-default").
					Resource(corev1.ResourceCPU, "8", "0").
					Resource(corev1.ResourceMemory, "4Gi", "0").Obj()).
				Obj(), defaultClusterQueueB},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1-admitted", "default").
					Queue("tas-lq-a").
					Priority(2).
					ReserveQuotaAt(
						utiltesting.MakeAdmission("tas-cq-a").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "2").
								Count(2).
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"y1"}, 2).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltesting.MakePodSet("one", 2).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("a2", "default").
					Queue("tas-lq-a").
					Priority(2).
					PodSets(*utiltesting.MakePodSet("one", 4).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b1", "default").
					Queue("tas-lq-b").
					Priority(1).
					PodSets(*utiltesting.MakePodSet("one", 3).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1-admitted", "default").
					Queue("tas-lq-a").
					Priority(2).
					ReserveQuotaAt(
						utiltesting.MakeAdmission("tas-cq-a").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "2").
								Count(2).
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"y1"}, 2).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltesting.MakePodSet("one", 2).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("a2", "default").
					Queue("tas-lq-a").
					Priority(2).
					PodSets(*utiltesting.MakePodSet("one", 4).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set one: topology \"tas-single-level\" allows to fit only 3 out of 4 pod(s)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("4"),
						},
					}).
					Obj(),
				*utiltesting.MakeWorkload("b1", "default").
					Queue("tas-lq-b").
					Priority(1).
					PodSets(*utiltesting.MakePodSet("one", 3).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "Workload no longer fits after processing another workload",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("3"),
						},
					}).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-cq-b": {"default/b1"},
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-cq-a": {"default/a2"},
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "a2", "Pending", corev1.EventTypeWarning).Obj(),
				utiltesting.MakeEventRecord("default", "b1", "Pending", corev1.EventTypeWarning).Obj(),
			},
		},
		"preempting workload without targets doesn't reserve capacity when it can always reclaim": {
			nodes:           []corev1.Node{defaultNodeY1},
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues: []kueue.ClusterQueue{*utiltesting.MakeClusterQueue("tas-cq-a").
				Cohort("tas-cohort-main").
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				}).
				ResourceGroup(*utiltesting.MakeFlavorQuotas("tas-default").
					Resource(corev1.ResourceCPU, "8", "0").
					Resource(corev1.ResourceMemory, "4Gi", "0").Obj()).
				Obj(), defaultClusterQueueB},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1-admitted", "default").
					Queue("tas-lq-a").
					Priority(2).
					ReserveQuotaAt(
						utiltesting.MakeAdmission("tas-cq-a").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "2").
								Count(2).
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"y1"}, 2).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltesting.MakePodSet("one", 2).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("a2", "default").
					Queue("tas-lq-a").
					Priority(2).
					PodSets(*utiltesting.MakePodSet("one", 4).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b1", "default").
					Queue("tas-lq-b").
					Priority(1).
					PodSets(*utiltesting.MakePodSet("one", 3).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1-admitted", "default").
					Queue("tas-lq-a").
					Priority(2).
					ReserveQuotaAt(
						utiltesting.MakeAdmission("tas-cq-a").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "2").
								Count(2).
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"y1"}, 2).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltesting.MakePodSet("one", 2).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("a2", "default").
					Queue("tas-lq-a").
					Priority(2).
					PodSets(*utiltesting.MakePodSet("one", 4).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set one: topology \"tas-single-level\" allows to fit only 3 out of 4 pod(s)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("4"),
						},
					}).
					Obj(),
				*utiltesting.MakeWorkload("b1", "default").
					Queue("tas-lq-b").
					Priority(1).
					PodSets(*utiltesting.MakePodSet("one", 3).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue tas-cq-b",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltesting.MakeAdmission("tas-cq-b").
							PodSets(utiltesting.MakePodSetAssignment("one").
								Count(3).
								Assignment(corev1.ResourceCPU, "tas-default", "3").
								TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltesting.MakeTopologyDomainAssignment([]string{"y1"}, 3).Obj()).
									Obj()).
								Obj()).
							Obj(),
					).
					Obj(),
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-cq-a": {"default/a2"},
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "b1", "QuotaReserved", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "b1", "Admitted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "a2", "Pending", "Warning").
					Message(`couldn't assign flavors to pod set one: topology "tas-single-level" allows to fit only 3 out of 4 pod(s)`).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/b1": *utiltesting.MakeAdmission("tas-cq-b").
					PodSets(utiltesting.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "3").
						Count(3).
						TopologyAssignment(utiltesting.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltesting.MakeTopologyDomainAssignment([]string{"y1"}, 3).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, true)
			ctx, log := utiltesting.ContextWithLog(t)
			testWls := make([]kueue.Workload, 0, len(tc.workloads))
			for _, wl := range tc.workloads {
				testWls = append(testWls, *wl.DeepCopy())
			}
			clientBuilder := utiltesting.NewClientBuilder().
				WithLists(
					&kueue.WorkloadList{Items: testWls},
					&kueuealpha.TopologyList{Items: tc.topologies},
					&corev1.PodList{Items: tc.pods},
					&corev1.NodeList{Items: tc.nodes},
					&kueue.LocalQueueList{Items: queues}).
				WithObjects(
					utiltesting.MakeNamespace("default"),
				).
				WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge}).
				WithStatusSubresource(&kueue.Workload{})
			_ = tasindexer.SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder))
			cl := clientBuilder.Build()
			recorder := &utiltesting.EventRecorder{}
			cqCache := schdcache.New(cl)
			qManager := queue.NewManager(cl, cqCache)
			topologyByName := slices.ToMap(tc.topologies, func(i int) (kueue.TopologyReference, kueuealpha.Topology) {
				return kueue.TopologyReference(tc.topologies[i].Name), tc.topologies[i]
			})
			for _, flavor := range tc.resourceFlavors {
				cqCache.AddOrUpdateResourceFlavor(log, &flavor)
				if flavor.Spec.TopologyName != nil {
					t := topologyByName[*flavor.Spec.TopologyName]
					cqCache.AddOrUpdateTopology(log, &t)
				}
			}
			for _, cq := range tc.clusterQueues {
				if err := cqCache.AddClusterQueue(ctx, &cq); err != nil {
					t.Fatalf("Inserting clusterQueue %s in cache: %v", cq.Name, err)
				}
				if err := qManager.AddClusterQueue(ctx, &cq); err != nil {
					t.Fatalf("Inserting clusterQueue %s in manager: %v", cq.Name, err)
				}
				if err := cl.Create(ctx, &cq); err != nil {
					t.Fatalf("couldn't create the cluster queue: %v", err)
				}
			}
			for _, q := range queues {
				if err := qManager.AddLocalQueue(ctx, &q); err != nil {
					t.Fatalf("Inserting queue %s/%s in manager: %v", q.Namespace, q.Name, err)
				}
			}
			initiallyAdmittedWorkloads := sets.New[workload.Reference]()
			for _, w := range testWls {
				if workload.IsAdmitted(&w) {
					initiallyAdmittedWorkloads.Insert(workload.Key(&w))
				}
			}
			scheduler := New(qManager, cqCache, cl, recorder, WithClock(t, testingclock.NewFakeClock(now)))
			wg := sync.WaitGroup{}
			scheduler.setAdmissionRoutineWrapper(routine.NewWrapper(
				func() { wg.Add(1) },
				func() { wg.Done() },
			))

			ctx, cancel := context.WithTimeout(ctx, queueingTimeout)
			go qManager.CleanUpOnContext(ctx)
			defer cancel()

			scheduler.schedule(ctx)
			wg.Wait()
			snapshot, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}

			gotWorkloads := &kueue.WorkloadList{}
			err = cl.List(ctx, gotWorkloads)
			if err != nil {
				t.Fatalf("Unexpected list workloads error: %v", err)
			}

			defaultWorkloadCmpOpts := cmp.Options{
				cmpopts.EquateEmpty(),
				cmpopts.IgnoreFields(kueue.Workload{}, "ObjectMeta.ResourceVersion"),
				cmpopts.SortSlices(func(a, b metav1.Condition) bool {
					return a.Type < b.Type
				}),
			}

			if diff := cmp.Diff(tc.wantWorkloads, gotWorkloads.Items, defaultWorkloadCmpOpts); diff != "" {
				t.Errorf("Unexpected scheduled workloads (-want,+got):\n%s", diff)
			}

			gotAssignments := make(map[workload.Reference]kueue.Admission)
			for cqName, c := range snapshot.ClusterQueues() {
				for name, w := range c.Workloads {
					if initiallyAdmittedWorkloads.Has(workload.Key(w.Obj)) {
						continue
					}
					switch {
					case !workload.HasQuotaReservation(w.Obj):
						t.Fatalf("Workload %s is not admitted by a clusterQueue, but it is found as member of clusterQueue %s in the cache", name, cqName)
					case w.Obj.Status.Admission.ClusterQueue != cqName:
						t.Fatalf("Workload %s is admitted by clusterQueue %s, but it is found as member of clusterQueue %s in the cache", name, w.Obj.Status.Admission.ClusterQueue, cqName)
					default:
						gotAssignments[name] = *w.Obj.Status.Admission
					}
				}
			}
			if diff := cmp.Diff(tc.wantNewAssignments, gotAssignments, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unexpected assigned clusterQueues in cache (-want,+got):\n%s", diff)
			}
			qDump := qManager.Dump()
			if diff := cmp.Diff(tc.wantLeft, qDump, cmpDump...); diff != "" {
				t.Errorf("Unexpected elements left in the queue (-want,+got):\n%s", diff)
			}
			qDumpInadmissible := qManager.DumpInadmissible()
			if diff := cmp.Diff(tc.wantInadmissibleLeft, qDumpInadmissible, cmpDump...); diff != "" {
				t.Errorf("Unexpected elements left in inadmissible workloads (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantEvents, recorder.RecordedEvents, append(tc.eventCmpOpts, cmpopts.SortSlices(utiltesting.SortEvents))...); diff != "" {
				t.Errorf("unexpected events (-want/+got):\n%s", diff)
			}
		})
	}
}

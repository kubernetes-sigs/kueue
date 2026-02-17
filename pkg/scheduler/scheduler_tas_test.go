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
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/component-base/featuregate"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	tasindexer "sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/routine"
	"sigs.k8s.io/kueue/pkg/util/slices"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestScheduleForTAS(t *testing.T) {
	now := time.Now().Truncate(time.Second)
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

	//    r1
	//  /  |  \
	// x1  x2  x3
	nodesWithZeroCapacityInstance := []corev1.Node{
		*testingnode.MakeNode("x1").
			Label("tas-node", "true").
			Label(corev1.LabelHostname, "x1").
			Label(tasRackLabel, "r1").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:  resource.MustParse("0"),
				corev1.ResourcePods: resource.MustParse("10"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("x2").
			Label("tas-node", "true").
			Label(corev1.LabelHostname, "x2").
			Label(tasRackLabel, "r1").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:  resource.MustParse("1"),
				corev1.ResourcePods: resource.MustParse("10"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("x3").
			Label("tas-node", "true").
			Label(corev1.LabelHostname, "x3").
			Label(tasRackLabel, "r1").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:  resource.MustParse("1"),
				corev1.ResourcePods: resource.MustParse("10"),
			}).
			Ready().
			Obj(),
	}

	defaultSingleLevelTopology := *utiltestingapi.MakeDefaultOneLevelTopology("tas-single-level")
	defaultTwoLevelTopology := *utiltestingapi.MakeTopology("tas-two-level").
		Levels(tasRackLabel, corev1.LabelHostname).
		Obj()
	defaultThreeLevelTopology := *utiltestingapi.MakeTopology("tas-three-level").
		Levels(tasBlockLabel, tasRackLabel, corev1.LabelHostname).
		Obj()
	defaultFlavor := *utiltestingapi.MakeResourceFlavor("default").Obj()
	defaultTASFlavor := *utiltestingapi.MakeResourceFlavor("tas-default").
		NodeLabel("tas-node", "true").
		TopologyName("tas-single-level").
		Obj()
	defaultTASTwoLevelFlavor := *utiltestingapi.MakeResourceFlavor("tas-default").
		NodeLabel("tas-node", "true").
		TopologyName("tas-two-level").
		Obj()
	defaultTASThreeLevelFlavor := *utiltestingapi.MakeResourceFlavor("tas-default").
		NodeLabel("tas-node", "true").
		TopologyName("tas-three-level").
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
	defaultCustomCheck := *utiltestingapi.MakeAdmissionCheck("custom-check").
		ControllerName("custom-admission-check-controller").
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
	clusterQueueWithCustomCheck := *utiltestingapi.MakeClusterQueue("tas-main").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-default").
			Resource(corev1.ResourceCPU, "50").
			Resource(corev1.ResourceMemory, "50Gi").Obj()).
		AdmissionChecks(kueue.AdmissionCheckReference(defaultCustomCheck.Name)).
		Obj()
	queues := []kueue.LocalQueue{
		*utiltestingapi.MakeLocalQueue("tas-main", "default").ClusterQueue("tas-main").Obj(),
	}
	eventIgnoreMessage := cmpopts.IgnoreFields(utiltesting.EventRecord{}, "Message")
	cases := map[string]struct {
		nodes           []corev1.Node
		pods            []corev1.Pod
		topologies      []kueue.Topology
		admissionChecks []kueue.AdmissionCheck
		resourceFlavors []kueue.ResourceFlavor
		clusterQueues   []kueue.ClusterQueue
		workloads       []kueue.Workload
		patchStatusErr  error

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

		featureGates map[featuregate.Feature]bool
	}{
		// Verifies TAS reads memory from PodSpec (1.5Gi/pod) not from quota-derived values.
		// Each node has 2Gi memory, so 2 pods cannot fit on a single node.
		"initial scheduling; TAS uses podspec memory for placement": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label("tas-node", "true").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("4"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("x2").
					Label("tas-node", "true").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("4"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("one", 2).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Request(corev1.ResourceMemory, "1536Mi").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "2000m").
						Assignment(corev1.ResourceMemory, "tas-default", "3Gi").
						Count(2).
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x2"}, 1).Obj()).
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
			topologies:      []kueue.Topology{defaultTwoLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASTwoLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(
						*utiltestingapi.MakePodSet("launcher", 1).
							PreferredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltestingapi.MakePodSet("worker", 0).
							PreferredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "1").
							Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(
						utiltestingapi.MakePodSetAssignment("launcher").
							Assignment(corev1.ResourceCPU, "tas-default", "1000m").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
								Obj()).
							Obj(),
						utiltestingapi.MakePodSetAssignment("worker").
							Assignment(corev1.ResourceCPU, "tas-default", "0").
							Count(0).
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).Obj()).
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
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
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
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{
				*utiltestingapi.MakeResourceFlavor("tas-reservation").
					NodeLabel("tas-group", "reservation").
					TopologyName("tas-single-level").
					Obj(),
				*utiltestingapi.MakeResourceFlavor("tas-provisioning").
					NodeLabel("tas-group", "provisioning").
					TopologyName("tas-single-level").
					Obj()},
			clusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("tas-main").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("tas-reservation").
							Resource(corev1.ResourceCPU, "1").Obj(),
						*utiltestingapi.MakeFlavorQuotas("tas-provisioning").
							Resource(corev1.ResourceCPU, "1").Obj()).
					AdmissionCheckStrategy(kueue.AdmissionCheckStrategyRule{
						Name: "prov-check",
						OnFlavors: []kueue.ResourceFlavorReference{
							"tas-provisioning",
						},
					}).
					Obj(),
			},
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
						Assignment(corev1.ResourceCPU, "tas-reservation", "1000m").
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
		},
		"workload with unhealthyNode annotation; second pass; baseline scenario": {
			nodes:           defaultNodes,
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueue.Topology{defaultThreeLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASThreeLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					UnhealthyNodes("x0").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "1000m").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x0"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
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
		},
		"workload with unhealthyNode annotation; second pass; preferred; fit in different rack": {
			nodes:           defaultNodes,
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueue.Topology{defaultThreeLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASThreeLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					UnhealthyNodes("x0").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("one", 2).
						PreferredTopologyRequest(tasRackLabel).
						Request(corev1.ResourceCPU, "500m").
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "1000m").
								Count(2).
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x0"}, 1).Obj()).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "1000m").
						Count(2).
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 2).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
		},
		"workload with unhealthyNode annotation; second pass; preferred; no fit": {
			featureGates:    map[featuregate.Feature]bool{features.TASFailedNodeReplacementFailFast: false},
			nodes:           defaultNodes,
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueue.Topology{defaultThreeLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASThreeLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					UnhealthyNodes("x0").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						PreferredTopologyRequest(tasRackLabel).
						Request(corev1.ResourceCPU, "3").
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "3000m").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x0"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "3000m").
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x0"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "SecondPassFailed", corev1.EventTypeWarning).
					Message(`couldn't assign flavors to pod set one: topology "tas-three-level" doesn't allow to fit any of 1 pod(s). Total nodes: 6; excluded: resource "cpu": 6`).
					Obj(),
			},
		},
		// Verifies TAS uses full PodSpec resources (CPU+memory) for placement, not just quota-tracked
		// resources.
		"workload with unhealthyNode annotation; second pass; podspec constraints apply": {
			featureGates:    map[featuregate.Feature]bool{features.TASFailedNodeReplacementFailFast: false},
			nodes:           defaultNodes,
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueue.Topology{defaultThreeLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASThreeLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					UnhealthyNodes("x0").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						PreferredTopologyRequest(tasRackLabel).
						Request(corev1.ResourceCPU, "1").
						Request(corev1.ResourceMemory, "2Gi").
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "1000m").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x0"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "1000m").
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x6"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
		},
		"workload with unhealthyNode annotation; second pass; preferred; no fit; FailFast": {
			nodes:           defaultNodes,
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueue.Topology{defaultThreeLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASThreeLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					UnhealthyNodes("x0").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						PreferredTopologyRequest(tasRackLabel).
						Request(corev1.ResourceCPU, "3").
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "3000m").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x0"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "3000m").
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x0"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "EvictedDueToNodeFailures", corev1.EventTypeNormal).
					Message("Workload was evicted as there was no replacement for unhealthy node(s): x0").
					Obj(),
			},
		},
		"workload with unhealthyNode annotation; second pass; preferred; no fit; FailFast (not found error on patch)": {
			nodes:           defaultNodes,
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueue.Topology{defaultThreeLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASThreeLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					UnhealthyNodes("x0").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						PreferredTopologyRequest(tasRackLabel).
						Request(corev1.ResourceCPU, "3").
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "3000m").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x0"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			patchStatusErr: apierrors.NewNotFound(schema.GroupResource{}, "test"),
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "3000m").
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x0"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
		},
		"workload with unhealthyNode annotation; second pass; preferred; no fit; FailFast (test error on patch)": {
			nodes:           defaultNodes,
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueue.Topology{defaultThreeLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASThreeLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					UnhealthyNodes("x0").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						PreferredTopologyRequest(tasRackLabel).
						Request(corev1.ResourceCPU, "3").
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "3000m").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x0"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			patchStatusErr: errors.New("test error"),
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "3000m").
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x0"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "SecondPassFailed", corev1.EventTypeWarning).
					Message("couldn't assign flavors to pod set one: topology \"tas-three-level\" doesn't allow to fit any of 1 pod(s). Total nodes: 6; excluded: resource \"cpu\": 6").
					Obj(),
			},
		},
		"workload with unhealthyNode annotation; second pass; required rack; fit": {
			nodes:           defaultNodes,
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueue.Topology{defaultThreeLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASThreeLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					UnhealthyNodes("x0").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("one", 2).
						RequiredTopologyRequest(tasRackLabel).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "1000m").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x0"}, 1).Obj()).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x2"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "1000m").
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x2"}, 1).Obj()).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x3"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
		},
		"workload with unhealthyNode annotation; second pass; required rack for a single node; fit": {
			nodes:           defaultNodes,
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueue.Topology{defaultThreeLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASThreeLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					UnhealthyNodes("x0").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						RequiredTopologyRequest(tasRackLabel).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "1000m").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x0"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
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
		},
		"workload with unhealthyNode annotation; second pass; required rack; no fit": {
			featureGates:    map[featuregate.Feature]bool{features.TASFailedNodeReplacementFailFast: false},
			nodes:           defaultNodes,
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueue.Topology{defaultThreeLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASThreeLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					UnhealthyNodes("x0").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("one", 2).
						RequiredTopologyRequest(tasRackLabel).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "1000m").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x0"}, 1).Obj()).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "1000m").
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x0"}, 1).Obj()).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "SecondPassFailed", corev1.EventTypeWarning).
					Message(`couldn't assign flavors to pod set one: topology "tas-three-level" doesn't allow to fit any of 1 pod(s). Total nodes: 6; excluded: resource "cpu": 1, topologyDomain: 5`).
					Obj(),
			},
		},
		"workload with unhealthyNode annotation; second pass; two podsets": {
			nodes:           defaultNodes,
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueue.Topology{defaultThreeLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASThreeLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					UnhealthyNodes("x0").
					Queue("tas-main").
					PodSets(
						*utiltestingapi.MakePodSet("one", 1).
							PreferredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltestingapi.MakePodSet("two", 1).
							PreferredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "1").
							Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(
								utiltestingapi.MakePodSetAssignment("one").
									Assignment(corev1.ResourceCPU, "tas-default", "1000m").
									TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
										Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x0"}, 1).Obj()).
										Obj()).
									Obj(),
								utiltestingapi.MakePodSetAssignment("two").
									Assignment(corev1.ResourceCPU, "tas-default", "1000m").
									TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
										Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
										Obj()).
									Obj(),
							).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(
						utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "tas-default", "1000m").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x2"}, 1).Obj()).
								Obj()).
							Obj(),
						utiltestingapi.MakePodSetAssignment("two").
							Assignment(corev1.ResourceCPU, "tas-default", "1000m").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
								Obj()).
							Obj(),
					).
					Obj(),
			},
		},
		"workload with unhealthyNode; second pass; slices; baseline": {
			nodes:           defaultNodes,
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueue.Topology{defaultThreeLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASThreeLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					UnhealthyNodes("x2").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("one", 2).
						PreferredTopologyRequest(tasBlockLabel).
						SliceSizeTopologyRequest(2).
						SliceRequiredTopologyRequest(tasRackLabel).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").Count(2).
								Assignment(corev1.ResourceCPU, "tas-default", "2000m").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x2"}, 1).Obj()).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x3"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(utiltestingapi.MakePodSetAssignment("one").Count(2).
						Assignment(corev1.ResourceCPU, "tas-default", "2000m").
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x3"}, 1).Obj()).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x4"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
		},
		"workload with unhealthyNode; second pass; slices; unhealthy node hosts incomplete slice": {
			nodes:           defaultNodes,
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueue.Topology{defaultThreeLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASThreeLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					UnhealthyNodes("x2").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("one", 8).
						PreferredTopologyRequest(tasBlockLabel).
						SliceSizeTopologyRequest(8).
						SliceRequiredTopologyRequest(tasRackLabel).
						Request(corev1.ResourceCPU, "250m").
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").Count(8).
								Assignment(corev1.ResourceCPU, "tas-default", "2000m").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x2"}, 4).Obj()).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x3"}, 4).Obj()).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(utiltestingapi.MakePodSetAssignment("one").Count(8).
						Assignment(corev1.ResourceCPU, "tas-default", "2000m").
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x3"}, 4).Obj()).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x4"}, 4).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
		},
		"workload with unhealthyNode; second pass; slices; unhealthy node hosts whole slices": {
			nodes:           defaultNodes,
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueue.Topology{defaultThreeLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASThreeLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					UnhealthyNodes("x3").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("one", 12).
						RequiredTopologyRequest(tasBlockLabel).
						SliceSizeTopologyRequest(4).
						SliceRequiredTopologyRequest(tasRackLabel).
						Request(corev1.ResourceCPU, "250m").
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").Count(12).
								Assignment(corev1.ResourceCPU, "tas-default", "3000m").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x2"}, 4).Obj()).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x3"}, 4).Obj()).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x4"}, 4).Obj()).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(utiltestingapi.MakePodSetAssignment("one").Count(12).
						Assignment(corev1.ResourceCPU, "tas-default", "3000m").
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 4).Obj()).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x2"}, 4).Obj()).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x4"}, 4).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
		},
		"workload with unhealthyNode; second pass; slices; unhealthy node hosts whole slice and incomplete slice": {
			nodes:           defaultNodes,
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueue.Topology{defaultThreeLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASThreeLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					UnhealthyNodes("x1").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("one", 8).
						RequiredTopologyRequest(tasBlockLabel).
						SliceSizeTopologyRequest(2).
						SliceRequiredTopologyRequest(tasRackLabel).
						Request(corev1.ResourceCPU, "200m").
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").Count(8).
								Assignment(corev1.ResourceCPU, "tas-default", "1600m").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 3).Obj()).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x2"}, 5).Obj()).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(utiltestingapi.MakePodSetAssignment("one").Count(8).
						Assignment(corev1.ResourceCPU, "tas-default", "1600m").
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x2"}, 5).Obj()).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x3"}, 3).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
		},
		"workload with unhealthyNode; second pass; slices; unhealthy node hosts the whole workload": {
			nodes:           defaultNodes,
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueue.Topology{defaultThreeLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASThreeLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					UnhealthyNodes("x1").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("one", 4).
						RequiredTopologyRequest(tasRackLabel).
						SliceSizeTopologyRequest(2).
						SliceRequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "250m").
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").Count(4).
								Assignment(corev1.ResourceCPU, "tas-default", "1000m").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 4).Obj()).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(utiltestingapi.MakePodSetAssignment("one").Count(4).
						Assignment(corev1.ResourceCPU, "tas-default", "1000m").
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x5"}, 4).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
		},
		"workload with unhealthyNode; second pass; slices; finds correct slice domain": {
			nodes:           defaultNodes,
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueue.Topology{defaultThreeLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASThreeLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					UnhealthyNodes("x7").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("one", 8).
						PreferredTopologyRequest(tasBlockLabel).
						SliceSizeTopologyRequest(4).
						SliceRequiredTopologyRequest(tasBlockLabel).
						Request(corev1.ResourceCPU, "500m").
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").Count(8).
								Assignment(corev1.ResourceCPU, "tas-default", "4000m").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 2).Obj()).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x3"}, 2).Obj()).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x5"}, 2).Obj()).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x7"}, 2).Obj()).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(utiltestingapi.MakePodSetAssignment("one").Count(8).
						Assignment(corev1.ResourceCPU, "tas-default", "4000m").
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 2).Obj()).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x3"}, 2).Obj()).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x5"}, 2).Obj()).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x6"}, 2).Obj()).
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
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{clusterQueueWithProvReq},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("one", 26).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(
								utiltestingapi.MakePodSetAssignment("one").
									Assignment(corev1.ResourceCPU, "tas-default", "26").
									DelayedTopologyRequest(kueue.DelayedTopologyRequestStatePending).
									Count(26).
									Obj(),
							).
							Obj(), now,
					).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "prov-check",
						State: kueue.CheckStateReady,
					}).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(
						utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "tas-default", "26").
							Count(26).
							DelayedTopologyRequest(kueue.DelayedTopologyRequestStateReady).
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 26).Obj()).
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
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor,
				*utiltestingapi.MakeResourceFlavor("tas-second").
					NodeLabel("tas-node-second", "true").
					TopologyName("tas-single-level").
					Obj()},
			clusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("tas-main").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("tas-default").
							Resource(corev1.ResourceCPU, "50").Obj(),
						*utiltestingapi.MakeFlavorQuotas("tas-second").
							Resource(corev1.ResourceCPU, "50").Obj()).
					AdmissionChecks(kueue.AdmissionCheckReference(defaultProvCheck.Name)).
					Obj(),
			},
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
									Assignment(corev1.ResourceCPU, "tas-second", "1000m").
									DelayedTopologyRequest(kueue.DelayedTopologyRequestStatePending).
									Obj(),
							).
							Obj(), now,
					).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "prov-check",
						State: kueue.CheckStateReady,
					}).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(
						utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "tas-second", "1000m").
							DelayedTopologyRequest(kueue.DelayedTopologyRequestStateReady).
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"y1"}, 1).Obj()).
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
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor,
				*utiltestingapi.MakeResourceFlavor("tas-second").
					NodeLabel("tas-node-second", "true").
					TopologyName("tas-single-level").
					Obj()},
			clusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("tas-main").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("tas-default").
							Resource(corev1.ResourceCPU, "50").Obj(),
						*utiltestingapi.MakeFlavorQuotas("tas-second").
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
				*utiltestingapi.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(
						*utiltestingapi.MakePodSet("one", 1).
							RequiredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltestingapi.MakePodSet("two", 1).
							RequiredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "1").
							Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(
								utiltestingapi.MakePodSetAssignment("one").
									Assignment(corev1.ResourceCPU, "tas-default", "1").
									TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
										Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
										Obj()).
									Obj(),
								utiltestingapi.MakePodSetAssignment("two").
									Assignment(corev1.ResourceCPU, "tas-second", "1").
									DelayedTopologyRequest(kueue.DelayedTopologyRequestStatePending).
									Obj(),
							).Obj(), now,
					).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "prov-check",
						State: kueue.CheckStateReady,
					}).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(
						utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "tas-default", "1").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
								Obj()).
							Obj(),
						utiltestingapi.MakePodSetAssignment("two").
							Assignment(corev1.ResourceCPU, "tas-second", "1").
							DelayedTopologyRequest(kueue.DelayedTopologyRequestStateReady).
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"y1"}, 1).Obj()).
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
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{clusterQueueWithProvReq},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "prov-check",
						State: kueue.CheckStatePending,
					}).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(
						utiltestingapi.MakePodSetAssignment("one").
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
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor,
				*utiltestingapi.MakeResourceFlavor("tas-second").
					NodeLabel("tas-node-second", "true").
					TopologyName("tas-single-level").
					Obj()},
			clusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("tas-main").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("tas-default").
							Resource(corev1.ResourceCPU, "50").Obj(),
						*utiltestingapi.MakeFlavorQuotas("tas-second").
							Resource(corev1.ResourceCPU, "50").Obj()).
					AdmissionChecks(kueue.AdmissionCheckReference(defaultProvCheck.Name)).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(
								utiltestingapi.MakePodSetAssignment("one").
									Assignment(corev1.ResourceCPU, "tas-second", "1000m").
									DelayedTopologyRequest(kueue.DelayedTopologyRequestStatePending).
									Obj(),
							).
							Obj(), now,
					).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "prov-check",
						State: kueue.CheckStateReady,
					}).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(
						utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "tas-second", "1000m").
							DelayedTopologyRequest(kueue.DelayedTopologyRequestStateReady).
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"y1"}, 1).Obj()).
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
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "prov-check",
						State: kueue.CheckStatePending,
					}).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(
						utiltestingapi.MakePodSetAssignment("one").
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
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{clusterQueueWithCustomCheck},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
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
			},
		},
		"workload which does not specify TAS annotation uses the only TAS flavor": {
			nodes:           defaultSingleNode,
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor, defaultFlavor},
			clusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("tas-main").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("tas-default").
							Resource(corev1.ResourceCPU, "50").Obj()).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
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
		},
		"workload requiring TAS skips the non-TAS flavor": {
			nodes:           defaultSingleNode,
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor, defaultFlavor},
			clusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("tas-main").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "50").Obj(),
						*utiltestingapi.MakeFlavorQuotas("tas-default").
							Resource(corev1.ResourceCPU, "50").Obj()).
					Obj(),
			},
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
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "QuotaReserved", corev1.EventTypeNormal).
					Message(`Quota reserved in ClusterQueue tas-main, wait time since queued was 9223372037s; Flavors considered: one: default(NoFit;Flavor "default" does not support TopologyAwareScheduling)`).Obj(),
				utiltesting.MakeEventRecord("default", "foo", "Admitted", corev1.EventTypeNormal).
					Message("Admitted by ClusterQueue tas-main, wait time since reservation was 0s").Obj(),
			},
		},
		"workload which does not need TAS skips the TAS flavor": {
			nodes:           defaultSingleNode,
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor, defaultFlavor},
			clusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("tas-main").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("tas-default").
							Resource(corev1.ResourceCPU, "50").Obj(),
						*utiltestingapi.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "50").Obj()).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "default", "1000m").
						Obj()).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "QuotaReserved", corev1.EventTypeNormal).
					Message(`Quota reserved in ClusterQueue tas-main, wait time since queued was 9223372037s; Flavors considered: one: tas-default(NoFit;Flavor "tas-default" supports only TopologyAwareScheduling)`).Obj(),
				utiltesting.MakeEventRecord("default", "foo", "Admitted", corev1.EventTypeNormal).
					Message("Admitted by ClusterQueue tas-main, wait time since reservation was 0s").Obj(),
			},
		},
		"workload with mixed PodSets (requiring TAS and not)": {
			nodes:           defaultSingleNode,
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor, defaultFlavor},
			clusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("tas-main").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("tas-default").
							Resource(corev1.ResourceCPU, "50").Obj(),
						*utiltestingapi.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "50").Obj()).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(
						*utiltestingapi.MakePodSet("launcher", 1).
							Request(corev1.ResourceCPU, "500m").
							Obj(),
						*utiltestingapi.MakePodSet("worker", 1).
							RequiredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "500m").
							Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(
						utiltestingapi.MakePodSetAssignment("launcher").
							Assignment(corev1.ResourceCPU, "default", "500m").
							Obj(),
						utiltestingapi.MakePodSetAssignment("worker").
							Assignment(corev1.ResourceCPU, "tas-default", "500m").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
								Obj()).
							Obj(),
					).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "QuotaReserved", corev1.EventTypeNormal).
					Message(`Quota reserved in ClusterQueue tas-main, wait time since queued was 9223372037s; Flavors considered: launcher: tas-default(NoFit;Flavor "tas-default" supports only TopologyAwareScheduling)`).Obj(),
				utiltesting.MakeEventRecord("default", "foo", "Admitted", corev1.EventTypeNormal).
					Message("Admitted by ClusterQueue tas-main, wait time since reservation was 0s").Obj(),
			},
		},
		"workload required TAS gets scheduled": {
			nodes:           defaultSingleNode,
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
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
		},
		"workload requests topology level which is not present in topology": {
			nodes:           defaultSingleNode,
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
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
			topologies: []kueue.Topology{defaultSingleLevelTopology,
				*utiltestingapi.MakeTopology("tas-custom-topology").
					Levels("cloud.com/custom-level").
					Obj(),
			},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor,
				*utiltestingapi.MakeResourceFlavor("tas-custom-flavor").
					NodeLabel("tas-node", "true").
					TopologyName("tas-custom-topology").
					Obj(),
			},
			clusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("tas-main").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("tas-default").
							Resource(corev1.ResourceCPU, "50").Obj(),
						*utiltestingapi.MakeFlavorQuotas("tas-custom-flavor").
							Resource(corev1.ResourceCPU, "50").Obj()).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						RequiredTopologyRequest("cloud.com/custom-level").
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-custom-flavor", "1").
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{"cloud.com/custom-level"}).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "QuotaReserved", corev1.EventTypeNormal).
					Message(`Quota reserved in ClusterQueue tas-main, wait time since queued was 9223372037s; Flavors considered: one: tas-default(NoFit;Flavor "tas-default" does not contain the requested level)`).Obj(),
				utiltesting.MakeEventRecord("default", "foo", "Admitted", corev1.EventTypeNormal).
					Message("Admitted by ClusterQueue tas-main, wait time since reservation was 0s").Obj(),
			},
		},
		"workload does not get scheduled as it does not fit within the node capacity": {
			nodes:           defaultSingleNode,
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("one", 2).
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
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("bar-admitted", "default").
					Queue("tas-main").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "1000m").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
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
					Message(`couldn't assign flavors to pod set one: topology "tas-single-level" doesn't allow to fit any of 1 pod(s). Total nodes: 1; excluded: resource "cpu": 1`).
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
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
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
					Message(`couldn't assign flavors to pod set one: topology "tas-single-level" doesn't allow to fit any of 1 pod(s). Total nodes: 1; excluded: resource "cpu": 1`).
					Obj(),
			},
		},
		"workload gets admitted next to already admitted workload, multiple resources used": {
			nodes:           defaultSingleNode,
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "500m").
						Request(corev1.ResourceMemory, "500Mi").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("bar-admitted", "default").
					Queue("tas-main").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "500m").
								Assignment(corev1.ResourceMemory, "tas-default", "500Mi").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "500m").
						Request(corev1.ResourceMemory, "500Mi").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "500m").
						Assignment(corev1.ResourceMemory, "tas-default", "500Mi").
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
			topologies:      []kueue.Topology{defaultTwoLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASTwoLevelFlavor},
			clusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("tas-main").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("tas-default").
							Resource(corev1.ResourceCPU, "50").Obj()).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(
						*utiltestingapi.MakePodSet("launcher", 1).
							PreferredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltestingapi.MakePodSet("worker", 1).
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
					Message(`couldn't assign flavors to pod set worker: topology "tas-two-level" doesn't allow to fit any of 1 pod(s). Total nodes: 2; excluded: resource "cpu": 2`).
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
			topologies:      []kueue.Topology{defaultTwoLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASTwoLevelFlavor},
			clusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("tas-main").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("tas-default").
							Resource(corev1.ResourceCPU, "16").Obj()).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(
						*utiltestingapi.MakePodSet("launcher", 1).
							RequiredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltestingapi.MakePodSet("worker", 15).
							PreferredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "1").
							Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(
						utiltestingapi.MakePodSetAssignment("launcher").
							Assignment(corev1.ResourceCPU, "tas-default", "1000m").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
								Obj()).
							Obj(),
						utiltestingapi.MakePodSetAssignment("worker").
							Assignment(corev1.ResourceCPU, "tas-default", "15000m").
							Count(15).
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 7).Obj()).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"y1"}, 8).Obj()).
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
			topologies:      []kueue.Topology{defaultTwoLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASTwoLevelFlavor},
			clusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("tas-main").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("tas-default").
							Resource(corev1.ResourceCPU, "16").Obj()).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(
						*utiltestingapi.MakePodSet("launcher", 1).
							RequiredTopologyRequest(tasRackLabel).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltestingapi.MakePodSet("worker", 15).
							RequiredTopologyRequest(tasRackLabel).
							Request(corev1.ResourceCPU, "1").
							Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(
						utiltestingapi.MakePodSetAssignment("launcher").
							Assignment(corev1.ResourceCPU, "tas-default", "1000m").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
								Obj()).
							Obj(),
						utiltestingapi.MakePodSetAssignment("worker").
							Assignment(corev1.ResourceCPU, "tas-default", "15000m").
							Count(15).
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 7).Obj()).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"y1"}, 8).Obj()).
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
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("tas-main").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("tas-default").
							Resource(corev1.ResourceCPU, "50").Obj()).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(
						*utiltestingapi.MakePodSet("one", 1).
							PreferredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeWorkload("bar-admitted", "default").
					Queue("tas-main").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "1000m").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
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
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"y1"}, 1).Obj()).
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
			clusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("tas-main").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("tas-default").
							Resource(corev1.ResourceCPU, "50").Obj()).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(
						*utiltestingapi.MakePodSet("one", 1).
							PreferredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
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
		},
		"TAS workload gets scheduled as trimmed by partial admission": {
			nodes:           defaultSingleNode,
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("one", 5).
						SetMinimumCount(1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "1").
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
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "300m").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("bar-admitted", "default").
					Queue("tas-main").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "150m").
								Count(2).
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 2).Obj()).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltestingapi.MakePodSet("one", 2).
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
					Message(`couldn't assign flavors to pod set one: topology "tas-single-level" doesn't allow to fit any of 1 pod(s). Total nodes: 1; excluded: resource "pods": 1`).
					Obj(),
			},
		},
		"workload with zero value request gets scheduled": {
			nodes:           defaultSingleNode,
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "0").
						Request(corev1.ResourceMemory, "10Mi").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "0").
						Assignment(corev1.ResourceMemory, "tas-default", "10Mi").
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
		},
		"workload with unhealthyNode annotation; second pass; preferred; no fit when using slices; FailFast": {
			nodes:           defaultNodes,
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueue.Topology{defaultThreeLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASThreeLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					UnhealthyNodes("x0").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("one", 2).
						PreferredTopologyRequest(tasBlockLabel).
						SliceSizeTopologyRequest(2).
						SliceRequiredTopologyRequest(tasRackLabel).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").Count(2).
								Assignment(corev1.ResourceCPU, "tas-default", "2000m").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x0"}, 1).Obj()).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(utiltestingapi.MakePodSetAssignment("one").Count(2).
						Assignment(corev1.ResourceCPU, "tas-default", "2000m").
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x0"}, 1).Obj()).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "EvictedDueToNodeFailures", corev1.EventTypeNormal).
					Message("Workload was evicted as there was no replacement for unhealthy node(s): x0").Obj(),
			},
		},
		"does not admit workload when node does not match required affinity": {
			nodes:           defaultSingleNode,
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(
						*utiltestingapi.MakePodSet("one", 1).
							PreferredTopologyRequest(corev1.LabelHostname).
							RequiredDuringSchedulingIgnoredDuringExecution(
								[]corev1.NodeSelectorTerm{
									{
										MatchExpressions: []corev1.NodeSelectorRequirement{
											{
												Key:      "unused-key",
												Operator: corev1.NodeSelectorOpIn,
												Values:   []string{"value"},
											},
										},
									},
								},
							).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Obj(),
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-main": {"default/foo"},
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "foo", "Pending", corev1.EventTypeWarning).Obj(),
			},
		},
		"admits workload when node matches required affinity": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label("tas-node", "true").
					Label(corev1.LabelHostname, "x1").
					Label("expected-label", "expected-value").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(
						*utiltestingapi.MakePodSet("one", 1).
							PreferredTopologyRequest(corev1.LabelHostname).
							RequiredDuringSchedulingIgnoredDuringExecution(
								[]corev1.NodeSelectorTerm{
									{
										MatchExpressions: []corev1.NodeSelectorRequirement{
											{
												Key:      "expected-label",
												Operator: corev1.NodeSelectorOpIn,
												Values:   []string{"expected-value"},
											},
										},
									},
								},
							).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(utiltestingapi.MakePodSetAssignment("one").Count(1).
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
		},
		"admits workload when nodes are partially empty; unconstrained": {
			nodes:           nodesWithZeroCapacityInstance,
			topologies:      []kueue.Topology{defaultTwoLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASTwoLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(
						*utiltestingapi.MakePodSet("one", 2).
							UnconstrainedTopologyRequest().
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(
						utiltestingapi.MakePodSetAssignment("one").
							Count(2).
							Assignment(corev1.ResourceCPU, "tas-default", "2000m").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x2"}, 1).Obj()).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x3"}, 1).Obj()).
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
		"admits workload when nodes are partially empty; preferred": {
			nodes:           nodesWithZeroCapacityInstance,
			topologies:      []kueue.Topology{defaultTwoLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASTwoLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(
						*utiltestingapi.MakePodSet("one", 2).
							PreferredTopologyRequest(tasRackLabel).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(
						utiltestingapi.MakePodSetAssignment("one").
							Count(2).
							Assignment(corev1.ResourceCPU, "tas-default", "2000m").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x2"}, 1).Obj()).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x3"}, 1).Obj()).
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
		"admits workload when nodes are partially empty; required": {
			nodes:           nodesWithZeroCapacityInstance,
			topologies:      []kueue.Topology{defaultTwoLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASTwoLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(
						*utiltestingapi.MakePodSet("one", 2).
							RequiredTopologyRequest(tasRackLabel).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(
						utiltestingapi.MakePodSetAssignment("one").
							Count(2).
							Assignment(corev1.ResourceCPU, "tas-default", "2000m").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x2"}, 1).Obj()).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x3"}, 1).Obj()).
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
		"admits workload with zero-quantity request for resource not in ClusterQueue with TAS": {
			nodes:           defaultSingleNode,
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(
						*utiltestingapi.MakePodSet("one", 1).
							PreferredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "1").
							Request("example.com/gpu", "0").
							Obj(),
					).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(
						utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "tas-default", "1000m").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
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
	}
	for name, tc := range cases {
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s WorkloadRequestUseMergePatch enabled: %t", name, enabled), func(t *testing.T) {
				features.SetFeatureGateDuringTest(t, features.WorkloadRequestUseMergePatch, enabled)
				for fg, enable := range tc.featureGates {
					features.SetFeatureGateDuringTest(t, fg, enable)
				}
				ctx, log := utiltesting.ContextWithLog(t)
				testWls := make([]kueue.Workload, 0, len(tc.workloads))
				for _, wl := range tc.workloads {
					testWls = append(testWls, *wl.DeepCopy())
				}

				clientBuilder := utiltesting.NewClientBuilder().
					WithLists(
						&kueue.AdmissionCheckList{Items: tc.admissionChecks},
						&kueue.WorkloadList{Items: testWls},
						&kueue.TopologyList{Items: tc.topologies},
						&corev1.PodList{Items: tc.pods},
						&corev1.NodeList{Items: tc.nodes},
						&kueue.LocalQueueList{Items: queues}).
					WithObjects(utiltesting.MakeNamespace("default")).
					WithInterceptorFuncs(interceptor.Funcs{
						SubResourcePatch: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
							if tc.patchStatusErr != nil {
								return tc.patchStatusErr
							}
							return utiltesting.TreatSSAAsStrategicMerge(ctx, c, subResourceName, obj, patch, opts...)
						},
					}).
					WithStatusSubresource(&kueue.Workload{}, &kueue.ClusterQueue{}, &kueue.LocalQueue{})

				for _, ac := range tc.admissionChecks {
					clientBuilder = clientBuilder.WithStatusSubresource(ac.DeepCopy())
				}
				_ = tasindexer.SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder))
				cl := clientBuilder.Build()
				recorder := &utiltesting.EventRecorder{}
				cqCache := schdcache.New(cl)
				fakeClock := testingclock.NewFakeClock(now)
				qManager := qcache.NewManagerForUnitTests(cl, cqCache, qcache.WithClock(fakeClock))
				topologyByName := slices.ToMap(tc.topologies, func(i int) (kueue.TopologyReference, kueue.Topology) {
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
				for _, pod := range tc.pods {
					cqCache.TASCache().Update(&pod, log)
				}
				initiallyAdmittedWorkloads := sets.New[workload.Reference]()
				for _, w := range testWls {
					if workload.IsAdmitted(&w) && !workload.HasUnhealthyNodes(&w) {
						initiallyAdmittedWorkloads.Insert(workload.Key(&w))
					}
				}
				for _, w := range testWls {
					if qManager.QueueSecondPassIfNeeded(ctx, &w, 0) {
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
	}
	eventIgnoreMessage := cmpopts.IgnoreFields(utiltesting.EventRecord{}, "Message")
	cases := map[string]struct {
		nodes           []corev1.Node
		pods            []corev1.Pod
		topologies      []kueue.Topology
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

		featureGates map[featuregate.Feature]bool
	}{
		"workload preempted due to quota is using deleted Node": {
			// In this scenario the preemption target, based on quota, is a
			// using an already deleted node (z).
			nodes:           defaultTwoNodes,
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("tas-main").
					Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-default").
						Resource(corev1.ResourceCPU, "10").
						Resource(corev1.ResourceMemory, "10Gi").Obj()).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					UID("wl-foo").
					JobUID("job-foo").
					Queue("tas-main").
					Priority(3).
					PodSets(*utiltestingapi.MakePodSet("one", 10).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("low-priority-admitted", "default").
					Queue("tas-main").
					Priority(1).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "5").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"z1"}, 1).Obj()).
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
					PodSets(*utiltestingapi.MakePodSet("one", 10).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Condition(metav1.Condition{
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
				*utiltestingapi.MakeWorkload("low-priority-admitted", "default").
					Queue("tas-main").
					Priority(1).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "5").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"z1"}, 1).Obj()).
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
					Message("Preempted to accommodate a workload (UID: wl-foo, JobUID: job-foo) due to prioritization in the ClusterQueue; preemptor path: /tas-main; preemptee path: /tas-main").
					Obj(),
				utiltesting.MakeEventRecord("default", "foo", "Pending", "Warning").
					Message(`couldn't assign flavors to pod set one: insufficient unused quota for cpu in flavor tas-default, 5 more needed. Pending the preemption of 1 workload(s)`).
					Obj(),
			},
		},
		// Verifies TAS uses PodSpec memory (4Gi) for preemption fit, not quota-derived values.
		// Node has 5Gi, incoming workload needs 4Gi, so it must preempt the 2Gi admitted workload.
		"preemption; TAS uses podspec memory for fit": {
			nodes:           defaultSingleNode,
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueWithPreemption},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					UID("wl-foo").
					JobUID("job-foo").
					Queue("tas-main").
					Priority(3).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Request(corev1.ResourceMemory, "4Gi").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("low-priority-admitted", "default").
					Queue("tas-main").
					Priority(1).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "1").
								Assignment(corev1.ResourceMemory, "tas-default", "2Gi").
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
						Request(corev1.ResourceCPU, "1").
						Request(corev1.ResourceMemory, "2Gi").
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
						Request(corev1.ResourceCPU, "1").
						Request(corev1.ResourceMemory, "4Gi").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            `couldn't assign flavors to pod set one: topology "tas-single-level" doesn't allow to fit any of 1 pod(s). Total nodes: 1; excluded: resource "memory": 1. Pending the preemption of 1 workload(s)`,
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1"),
							corev1.ResourceMemory: resource.MustParse("4Gi"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("low-priority-admitted", "default").
					Queue("tas-main").
					Priority(1).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "1").
								Assignment(corev1.ResourceMemory, "tas-default", "2Gi").
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
						Request(corev1.ResourceCPU, "1").
						Request(corev1.ResourceMemory, "2Gi").
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
					Message("Preempted to accommodate a workload (UID: wl-foo, JobUID: job-foo) due to prioritization in the ClusterQueue; preemptor path: /tas-main; preemptee path: /tas-main").
					Obj(),
				utiltesting.MakeEventRecord("default", "foo", "Pending", "Warning").
					Message(`couldn't assign flavors to pod set one: topology "tas-single-level" doesn't allow to fit any of 1 pod(s). Total nodes: 1; excluded: resource "memory": 1. Pending the preemption of 1 workload(s)`).
					Obj(),
			},
		},
		"only low priority workload is preempted": {
			// This test case demonstrates the baseline scenario where there
			// is only one low-priority workload and it gets preempted.
			nodes:           defaultSingleNode,
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueWithPreemption},
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
						Reason:             "Pending",
						Message:            `couldn't assign flavors to pod set one: topology "tas-single-level" doesn't allow to fit any of 1 pod(s). Total nodes: 1; excluded: resource "cpu": 1. Pending the preemption of 1 workload(s)`,
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
					Message("Preempted to accommodate a workload (UID: wl-foo, JobUID: job-foo) due to prioritization in the ClusterQueue; preemptor path: /tas-main; preemptee path: /tas-main").
					Obj(),
				utiltesting.MakeEventRecord("default", "foo", "Pending", "Warning").
					Message(`couldn't assign flavors to pod set one: topology "tas-single-level" doesn't allow to fit any of 1 pod(s). Total nodes: 1; excluded: resource "cpu": 1. Pending the preemption of 1 workload(s)`).
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
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueWithPreemption},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("high-priority-waiting", "default").
					UID("wl-high-priority-waiting").
					JobUID("job-high-priority-waiting").
					Queue("tas-main").
					Priority(3).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "2").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("low-priority-admitted", "default").
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
				*utiltestingapi.MakeWorkload("high-priority-waiting", "default").
					UID("wl-high-priority-waiting").
					JobUID("job-high-priority-waiting").
					Queue("tas-main").
					Priority(3).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "2").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            `couldn't assign flavors to pod set one: topology "tas-single-level" doesn't allow to fit any of 1 pod(s). Total nodes: 1; excluded: resource "cpu": 1. Pending the preemption of 1 workload(s)`,
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
						Message:            "Preempted to accommodate a workload (UID: wl-high-priority-waiting, JobUID: job-high-priority-waiting) due to prioritization in the ClusterQueue; preemptor path: /tas-main; preemptee path: /tas-main",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-high-priority-waiting, JobUID: job-high-priority-waiting) due to prioritization in the ClusterQueue; preemptor path: /tas-main; preemptee path: /tas-main",
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
					Message("Preempted to accommodate a workload (UID: wl-high-priority-waiting, JobUID: job-high-priority-waiting) due to prioritization in the ClusterQueue; preemptor path: /tas-main; preemptee path: /tas-main").
					Obj(),
				utiltesting.MakeEventRecord("default", "low-priority-admitted", "Preempted", "Normal").
					Message("Preempted to accommodate a workload (UID: wl-high-priority-waiting, JobUID: job-high-priority-waiting) due to prioritization in the ClusterQueue; preemptor path: /tas-main; preemptee path: /tas-main").
					Obj(),
				utiltesting.MakeEventRecord("default", "high-priority-waiting", "Pending", "Warning").
					Message(`couldn't assign flavors to pod set one: topology "tas-single-level" doesn't allow to fit any of 1 pod(s). Total nodes: 1; excluded: resource "cpu": 1. Pending the preemption of 1 workload(s)`).
					Obj(),
			},
		},
		"low priority workload is preempted, mid-priority workload survives": {
			// This test case demonstrates the targets are selected according
			// to priorities, similarly as for regular preemption.
			nodes:           defaultSingleNode,
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueWithPreemption},
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
				*utiltestingapi.MakeWorkload("mid-priority-admitted", "default").
					Queue("tas-main").
					Priority(2).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "2").
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
						Request(corev1.ResourceCPU, "2").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("low-priority-admitted", "default").
					Queue("tas-main").
					Priority(1).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "2").
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
						Request(corev1.ResourceCPU, "2").
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
						Reason:             "Pending",
						Message:            `couldn't assign flavors to pod set one: topology "tas-single-level" doesn't allow to fit any of 1 pod(s). Total nodes: 1; excluded: resource "cpu": 1. Pending the preemption of 1 workload(s)`,
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
					Queue("tas-main").
					Priority(1).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "2").
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
						Request(corev1.ResourceCPU, "2").
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
				*utiltestingapi.MakeWorkload("mid-priority-admitted", "default").
					Queue("tas-main").
					Priority(2).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "2").
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
						Request(corev1.ResourceCPU, "2").
						Obj()).
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
					Message("Preempted to accommodate a workload (UID: wl-foo, JobUID: job-foo) due to prioritization in the ClusterQueue; preemptor path: /tas-main; preemptee path: /tas-main").
					Obj(),
				utiltesting.MakeEventRecord("default", "foo", "Pending", "Warning").
					Message(`couldn't assign flavors to pod set one: topology "tas-single-level" doesn't allow to fit any of 1 pod(s). Total nodes: 1; excluded: resource "cpu": 1. Pending the preemption of 1 workload(s)`).
					Obj(),
			},
		},
		"low priority workload is preempted even though there is enough capacity, but fragmented": {
			// In this test we fill in two nodes 4/5 which leaves 2 units empty.
			// It would be enough to schedule both pods of wl3, one pod per
			// node, but the workload requires to run on a single node, thus
			// the lower priority workload is chosen as target.
			nodes:           defaultTwoNodes,
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueWithPreemption},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					UID("wl-foo").
					JobUID("job-foo").
					Queue("tas-main").
					Priority(3).
					PodSets(*utiltestingapi.MakePodSet("one", 2).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("mid-priority-admitted", "default").
					Queue("tas-main").
					Priority(2).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "4").
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
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("low-priority-admitted", "default").
					Queue("tas-main").
					Priority(1).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "4").
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
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "default").
					UID("wl-foo").
					JobUID("job-foo").
					Queue("tas-main").
					Priority(3).
					PodSets(*utiltestingapi.MakePodSet("one", 2).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            `couldn't assign flavors to pod set one: topology "tas-single-level" allows to fit only 1 out of 2 pod(s). Pending the preemption of 1 workload(s)`,
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
					Queue("tas-main").
					Priority(1).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "4").
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
						Request(corev1.ResourceCPU, "4").
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
				*utiltestingapi.MakeWorkload("mid-priority-admitted", "default").
					Queue("tas-main").
					Priority(2).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "4").
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
						Request(corev1.ResourceCPU, "4").
						Obj()).
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
					Message("Preempted to accommodate a workload (UID: wl-foo, JobUID: job-foo) due to prioritization in the ClusterQueue; preemptor path: /tas-main; preemptee path: /tas-main").
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
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueWithPreemption},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("low-priority-which-would-fit", "default").
					Queue("tas-main").
					Priority(1).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("mid-priority-waiting", "default").
					Queue("tas-main").
					Priority(2).
					PodSets(*utiltestingapi.MakePodSet("one", 2).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("mid-priority-admitted", "default").
					Queue("tas-main").
					Priority(2).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "4").
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
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("low-priority-which-would-fit", "default").
					Queue("tas-main").
					Priority(1).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("mid-priority-admitted", "default").
					Queue("tas-main").
					Priority(2).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "4").
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
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("mid-priority-waiting", "default").
					Queue("tas-main").
					Priority(2).
					PodSets(*utiltestingapi.MakePodSet("one", 2).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            `couldn't assign flavors to pod set one: topology "tas-single-level" allows to fit only 1 out of 2 pod(s)`,
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
		"TAS disabled, preemption succeeds even with ResourceFlavor topologyName": {
			// This test verifies that when TopologyAwareScheduling feature gate is disabled,
			// preemption works correctly even if ResourceFlavor has topologyName configured.
			// The fix ensures that WorkloadsTopologyRequests is not called when TAS is disabled,
			// preventing "workload requires Topology, but there is no TAS cache information" errors.
			nodes:           defaultSingleNode,
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("tas-main").
					Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-default").
						Resource(corev1.ResourceCPU, "3").
						Resource(corev1.ResourceMemory, "3Gi").Obj()).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("high-priority", "default").
					UID("wl-high-priority").
					JobUID("job-high-priority").
					Queue("tas-main").
					Priority(2).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						Request(corev1.ResourceCPU, "3").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("low-priority", "default").
					Queue("tas-main").
					Priority(1).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("main").
								Assignment(corev1.ResourceCPU, "tas-default", "3").
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						Request(corev1.ResourceCPU, "3").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("high-priority", "default").
					UID("wl-high-priority").
					JobUID("job-high-priority").
					Queue("tas-main").
					Priority(2).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						Request(corev1.ResourceCPU, "3").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor tas-default, 3 more needed. Pending the preemption of 1 workload(s)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("3"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("low-priority", "default").
					Queue("tas-main").
					Priority(1).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(utiltestingapi.MakePodSetAssignment("main").
								Assignment(corev1.ResourceCPU, "tas-default", "3").
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						Request(corev1.ResourceCPU, "3").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "ByTest",
						Message:            "Admitted by ClusterQueue tas-main",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "AdmittedByTest",
						Message:            "Admitted by ClusterQueue tas-main",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-high-priority, JobUID: job-high-priority) due to prioritization in the ClusterQueue; preemptor path: /tas-main; preemptee path: /tas-main",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-high-priority, JobUID: job-high-priority) due to prioritization in the ClusterQueue; preemptor path: /tas-main; preemptee path: /tas-main",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-main": {"default/high-priority"},
			},
			wantInadmissibleLeft: nil,
			eventCmpOpts:         cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "low-priority", "EvictedDueToPreempted", "Normal").
					Message("Preempted to accommodate a workload (UID: wl-high-priority, JobUID: job-high-priority) due to prioritization in the ClusterQueue; preemptor path: /tas-main; preemptee path: /tas-main").
					Obj(),
				utiltesting.MakeEventRecord("default", "low-priority", "Preempted", "Normal").
					Message("Preempted to accommodate a workload (UID: wl-high-priority, JobUID: job-high-priority) due to prioritization in the ClusterQueue; preemptor path: /tas-main; preemptee path: /tas-main").
					Obj(),
				utiltesting.MakeEventRecord("default", "high-priority", "Pending", "Warning").
					Message("couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor tas-default, 3 more needed. Pending the preemption of 1 workload(s)").
					Obj(),
			},
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,
			},
		},
	}
	for name, tc := range cases {
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s WorkloadRequestUseMergePatch enabled: %t", name, enabled), func(t *testing.T) {
				features.SetFeatureGateDuringTest(t, features.WorkloadRequestUseMergePatch, enabled)
				for feature, value := range tc.featureGates {
					features.SetFeatureGateDuringTest(t, feature, value)
				}
				ctx, log := utiltesting.ContextWithLog(t)
				testWls := make([]kueue.Workload, 0, len(tc.workloads))
				for _, wl := range tc.workloads {
					testWls = append(testWls, *wl.DeepCopy())
				}
				clientBuilder := utiltesting.NewClientBuilder().
					WithLists(
						&kueue.WorkloadList{Items: testWls},
						&kueue.TopologyList{Items: tc.topologies},
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
				qManager := qcache.NewManagerForUnitTests(cl, cqCache)
				topologyByName := slices.ToMap(tc.topologies, func(i int) (kueue.TopologyReference, kueue.Topology) {
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
	defaultSingleLevelTopology := *utiltestingapi.MakeDefaultOneLevelTopology("tas-single-level")
	defaultTASFlavor := *utiltestingapi.MakeResourceFlavor("tas-default").
		NodeLabel("tas-node", "true").
		TopologyName("tas-single-level").
		Obj()
	defaultClusterQueueA := *utiltestingapi.MakeClusterQueue("tas-cq-a").
		Cohort("tas-cohort-main").
		Preemption(kueue.ClusterQueuePreemption{
			WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
			ReclaimWithinCohort: kueue.PreemptionPolicyAny,
		}).
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-default").
			Resource(corev1.ResourceCPU, "4").
			Resource(corev1.ResourceMemory, "4Gi").Obj()).
		Obj()
	defaultClusterQueueB := *utiltestingapi.MakeClusterQueue("tas-cq-b").
		Cohort("tas-cohort-main").
		Preemption(kueue.ClusterQueuePreemption{
			ReclaimWithinCohort: kueue.PreemptionPolicyAny,
		}).
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-default").
			Resource(corev1.ResourceCPU, "4").
			Resource(corev1.ResourceMemory, "4Gi").Obj()).
		Obj()
	defaultClusterQueueC := *utiltestingapi.MakeClusterQueue("tas-cq-c").
		Cohort("tas-cohort-main").
		Preemption(kueue.ClusterQueuePreemption{
			ReclaimWithinCohort: kueue.PreemptionPolicyAny,
		}).
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-default").
			Resource(corev1.ResourceCPU, "4").
			Resource(corev1.ResourceMemory, "4Gi").Obj()).
		Obj()
	queues := []kueue.LocalQueue{
		*utiltestingapi.MakeLocalQueue("tas-lq-a", "default").ClusterQueue("tas-cq-a").Obj(),
		*utiltestingapi.MakeLocalQueue("tas-lq-b", "default").ClusterQueue("tas-cq-b").Obj(),
		*utiltestingapi.MakeLocalQueue("tas-lq-c", "default").ClusterQueue("tas-cq-c").Obj(),
	}
	eventIgnoreMessage := cmpopts.IgnoreFields(utiltesting.EventRecord{}, "Message")
	cases := map[string]struct {
		nodes           []corev1.Node
		pods            []corev1.Pod
		topologies      []kueue.Topology
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
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueA, defaultClusterQueueB},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1", "default").
					Queue("tas-lq-a").
					PodSets(*utiltestingapi.MakePodSet("one", 6).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1", "default").
					Queue("tas-lq-a").
					PodSets(*utiltestingapi.MakePodSet("one", 6).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue tas-cq-a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltestingapi.MakeAdmission("tas-cq-a").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Count(6).
								Assignment(corev1.ResourceCPU, "tas-default", "6").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"y1"}, 5).Obj()).
									Obj()).
								Obj()).
							Obj(),
					).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/a1": *utiltestingapi.MakeAdmission("tas-cq-a").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "6").
						Count(6).
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"y1"}, 5).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "a1", "QuotaReserved", corev1.EventTypeNormal).
					Message("Quota reserved in ClusterQueue tas-cq-a, wait time since queued was 9223372037s; Flavors considered: one: tas-default(Fit;borrow=1)").Obj(),
				utiltesting.MakeEventRecord("default", "a1", "Admitted", corev1.EventTypeNormal).
					Message("Admitted by ClusterQueue tas-cq-a, wait time since reservation was 0s").Obj(),
			},
		},
		"reclaim within cohort; single borrowing workload gets preempted": {
			// This is a baseline scenario for reclamation within cohort where
			// a single borrowing workload gets preempted.
			nodes:           defaultTwoNodes,
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueA, defaultClusterQueueB},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1-admitted", "default").
					Queue("tas-lq-a").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-cq-a").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "5").
								Count(5).
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 5).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltestingapi.MakePodSet("one", 5).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "default").
					UID("wl-b1").
					JobUID("job-b1").
					Queue("tas-lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 4).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1-admitted", "default").
					Queue("tas-lq-a").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-cq-a").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "5").
								Count(5).
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 5).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltestingapi.MakePodSet("one", 5).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-b1, JobUID: job-b1) due to reclamation within the cohort; preemptor path: /tas-cohort-main/tas-cq-b; preemptee path: /tas-cohort-main/tas-cq-a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-b1, JobUID: job-b1) due to reclamation within the cohort; preemptor path: /tas-cohort-main/tas-cq-b; preemptee path: /tas-cohort-main/tas-cq-a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "default").
					UID("wl-b1").
					JobUID("job-b1").
					Queue("tas-lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 4).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Condition(metav1.Condition{
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
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueA, defaultClusterQueueB},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1-admitted", "default").
					Queue("tas-lq-a").
					Priority(1).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-cq-a").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "5").
								Count(5).
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 5).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltestingapi.MakePodSet("one", 5).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("a2-admitted", "default").
					Queue("tas-lq-a").
					Priority(2).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-cq-a").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "2").
								Count(2).
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"y1"}, 2).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltestingapi.MakePodSet("one", 2).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("a3-admitted", "default").
					Queue("tas-lq-a").
					Priority(2).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-cq-a").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "1").
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
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "default").
					UID("wl-b1").
					JobUID("job-b1").
					Queue("tas-lq-b").
					Priority(3).
					PodSets(*utiltestingapi.MakePodSet("one", 3).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1-admitted", "default").
					Queue("tas-lq-a").
					Priority(1).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-cq-a").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "5").
								Count(5).
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 5).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltestingapi.MakePodSet("one", 5).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-b1, JobUID: job-b1) due to reclamation within the cohort; preemptor path: /tas-cohort-main/tas-cq-b; preemptee path: /tas-cohort-main/tas-cq-a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-b1, JobUID: job-b1) due to reclamation within the cohort; preemptor path: /tas-cohort-main/tas-cq-b; preemptee path: /tas-cohort-main/tas-cq-a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("a2-admitted", "default").
					Queue("tas-lq-a").
					Priority(2).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-cq-a").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "2").
								Count(2).
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"y1"}, 2).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltestingapi.MakePodSet("one", 2).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("a3-admitted", "default").
					Queue("tas-lq-a").
					Priority(2).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-cq-a").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "1").
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
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "default").
					UID("wl-b1").
					JobUID("job-b1").
					Queue("tas-lq-b").
					Priority(3).
					PodSets(*utiltestingapi.MakePodSet("one", 3).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Condition(metav1.Condition{
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
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueA, defaultClusterQueueB},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1-admitted", "default").
					Queue("tas-lq-a").
					Priority(3).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-cq-a").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "5").
								Count(5).
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"y1"}, 5).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltestingapi.MakePodSet("one", 4).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("a2-admitted", "default").
					Queue("tas-lq-a").
					Priority(2).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-cq-a").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "3").
								Count(3).
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 3).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltestingapi.MakePodSet("one", 3).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "default").
					UID("wl-b1").
					JobUID("job-b1").
					Queue("tas-lq-b").
					Priority(3).
					PodSets(*utiltestingapi.MakePodSet("one", 4).
						SetMinimumCount(3).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Request(corev1.ResourceMemory, "1").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1-admitted", "default").
					Queue("tas-lq-a").
					Priority(3).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-cq-a").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "5").
								Count(5).
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"y1"}, 5).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltestingapi.MakePodSet("one", 4).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("a2-admitted", "default").
					Queue("tas-lq-a").
					Priority(2).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-cq-a").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "3").
								Count(3).
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 3).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltestingapi.MakePodSet("one", 3).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-b1, JobUID: job-b1) due to reclamation within the cohort; preemptor path: /tas-cohort-main/tas-cq-b; preemptee path: /tas-cohort-main/tas-cq-a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-b1, JobUID: job-b1) due to reclamation within the cohort; preemptor path: /tas-cohort-main/tas-cq-b; preemptee path: /tas-cohort-main/tas-cq-a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "default").
					UID("wl-b1").
					JobUID("job-b1").
					Queue("tas-lq-b").
					Priority(3).
					PodSets(*utiltestingapi.MakePodSet("one", 4).
						SetMinimumCount(3).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Request(corev1.ResourceMemory, "1").
						Obj()).
					Condition(metav1.Condition{
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
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueA, defaultClusterQueueB, defaultClusterQueueC},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1-admitted", "default").
					Queue("tas-lq-a").
					Priority(2).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-cq-a").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "5").
								Count(5).
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 3).Obj()).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"y1"}, 2).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltestingapi.MakePodSet("one", 5).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("a2-admitted", "default").
					Queue("tas-lq-a").
					Priority(1).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-cq-a").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "1").
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
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("a3-admitted", "default").
					Queue("tas-lq-a").
					Priority(1).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-cq-a").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "1").
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
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "default").
					UID("wl-b1").
					JobUID("job-b1").
					Queue("tas-lq-b").
					Priority(3).
					PodSets(*utiltestingapi.MakePodSet("one", 3).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("c1", "default").
					Queue("tas-lq-c").
					Priority(1).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1-admitted", "default").
					Queue("tas-lq-a").
					Priority(2).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-cq-a").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "5").
								Count(5).
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 3).Obj()).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"y1"}, 2).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltestingapi.MakePodSet("one", 5).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("a2-admitted", "default").
					Queue("tas-lq-a").
					Priority(1).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-cq-a").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "1").
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
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-b1, JobUID: job-b1) due to reclamation within the cohort; preemptor path: /tas-cohort-main/tas-cq-b; preemptee path: /tas-cohort-main/tas-cq-a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-b1, JobUID: job-b1) due to reclamation within the cohort; preemptor path: /tas-cohort-main/tas-cq-b; preemptee path: /tas-cohort-main/tas-cq-a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("a3-admitted", "default").
					Queue("tas-lq-a").
					Priority(1).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-cq-a").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "1").
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
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-b1, JobUID: job-b1) due to reclamation within the cohort; preemptor path: /tas-cohort-main/tas-cq-b; preemptee path: /tas-cohort-main/tas-cq-a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-b1, JobUID: job-b1) due to reclamation within the cohort; preemptor path: /tas-cohort-main/tas-cq-b; preemptee path: /tas-cohort-main/tas-cq-a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "default").
					UID("wl-b1").
					JobUID("job-b1").
					Queue("tas-lq-b").
					Priority(3).
					PodSets(*utiltestingapi.MakePodSet("one", 3).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            `couldn't assign flavors to pod set one: topology "tas-single-level" allows to fit only 1 out of 3 pod(s). Total nodes: 2; excluded: resource "cpu": 1. Pending the preemption of 2 workload(s)`,
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("3"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("c1", "default").
					Queue("tas-lq-c").
					Priority(1).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Condition(metav1.Condition{
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
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueA, defaultClusterQueueB},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1", "default").
					Priority(2).
					Queue("tas-lq-a").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "5").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "default").
					Priority(1).
					Queue("tas-lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceMemory, "5").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1", "default").
					Priority(2).
					Queue("tas-lq-a").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "5").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue tas-cq-a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltestingapi.MakeAdmission("tas-cq-a").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Count(1).
								Assignment(corev1.ResourceCPU, "tas-default", "5").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"y1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "default").
					Priority(1).
					Queue("tas-lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceMemory, "5").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue tas-cq-b",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltestingapi.MakeAdmission("tas-cq-b").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Count(1).
								Assignment(corev1.ResourceMemory, "tas-default", "5").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
					).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/a1": *utiltestingapi.MakeAdmission("tas-cq-a").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "5").
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"y1"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
				"default/b1": *utiltestingapi.MakeAdmission("tas-cq-b").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceMemory, "tas-default", "5").
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "b1", "QuotaReserved", corev1.EventTypeNormal).
					Message("Quota reserved in ClusterQueue tas-cq-b, wait time since queued was 9223372037s").Obj(),
				utiltesting.MakeEventRecord("default", "b1", "Admitted", corev1.EventTypeNormal).
					Message("Admitted by ClusterQueue tas-cq-b, wait time since reservation was 0s").Obj(),
				utiltesting.MakeEventRecord("default", "a1", "QuotaReserved", corev1.EventTypeNormal).
					Message("Quota reserved in ClusterQueue tas-cq-a, wait time since queued was 9223372037s; Flavors considered: one: tas-default(Fit;borrow=1)").Obj(),
				utiltesting.MakeEventRecord("default", "a1", "Admitted", corev1.EventTypeNormal).
					Message("Admitted by ClusterQueue tas-cq-a, wait time since reservation was 0s").Obj(),
			},
		},
		"two small workloads considered; both get scheduled on the same node": {
			nodes:           defaultTwoNodes,
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueA, defaultClusterQueueB},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1", "default").
					Priority(2).
					Queue("tas-lq-a").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "default").
					Priority(1).
					Queue("tas-lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 2).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1", "default").
					Priority(2).
					Queue("tas-lq-a").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue tas-cq-a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltestingapi.MakeAdmission("tas-cq-a").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Count(1).
								Assignment(corev1.ResourceCPU, "tas-default", "1").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "default").
					Priority(1).
					Queue("tas-lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 2).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue tas-cq-b",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltestingapi.MakeAdmission("tas-cq-b").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Count(2).
								Assignment(corev1.ResourceCPU, "tas-default", "2").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 2).Obj()).
									Obj()).
								Obj()).
							Obj(),
					).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/a1": *utiltestingapi.MakeAdmission("tas-cq-a").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "1").
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
				"default/b1": *utiltestingapi.MakeAdmission("tas-cq-b").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "2").
						Count(2).
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 2).Obj()).
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
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueA, defaultClusterQueueB},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1", "default").
					Priority(2).
					Queue("tas-lq-a").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "default").
					Priority(1).
					Queue("tas-lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 3).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1", "default").
					Priority(2).
					Queue("tas-lq-a").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue tas-cq-a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltestingapi.MakeAdmission("tas-cq-a").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Count(1).
								Assignment(corev1.ResourceCPU, "tas-default", "1").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "default").
					Priority(1).
					Queue("tas-lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 3).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Condition(metav1.Condition{
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
				"default/a1": *utiltestingapi.MakeAdmission("tas-cq-a").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "1").
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
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
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueA, defaultClusterQueueB},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1", "default").
					Priority(2).
					Queue("tas-lq-a").
					PodSets(*utiltestingapi.MakePodSet("one", 5).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "default").
					Priority(1).
					Queue("tas-lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 5).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1", "default").
					Priority(2).
					Queue("tas-lq-a").
					PodSets(*utiltestingapi.MakePodSet("one", 5).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue tas-cq-a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltestingapi.MakeAdmission("tas-cq-a").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Count(5).
								Assignment(corev1.ResourceCPU, "tas-default", "5").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"y1"}, 5).Obj()).
									Obj()).
								Obj()).
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "default").
					Priority(1).
					Queue("tas-lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 5).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Condition(metav1.Condition{
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
				"default/a1": *utiltestingapi.MakeAdmission("tas-cq-a").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "5").
						Count(5).
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"y1"}, 5).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "b1", "Pending", corev1.EventTypeWarning).
					Message("Workload no longer fits after processing another workload").Obj(),
				utiltesting.MakeEventRecord("default", "a1", "QuotaReserved", corev1.EventTypeNormal).
					Message("Quota reserved in ClusterQueue tas-cq-a, wait time since queued was 9223372037s; Flavors considered: one: tas-default(Fit;borrow=1)").Obj(),
				utiltesting.MakeEventRecord("default", "a1", "Admitted", corev1.EventTypeNormal).
					Message("Admitted by ClusterQueue tas-cq-a, wait time since reservation was 0s").Obj(),
			},
		},
		"two workloads considered; both overlapping in the initial flavor assignment": {
			// Both of the workloads require 2 CPU units which will make them
			// both target the x1 node which is smallest in terms of CPU.
			nodes:           defaultTwoNodes,
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueA, defaultClusterQueueB},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1", "default").
					Priority(2).
					Queue("tas-lq-a").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "2").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "default").
					Priority(1).
					Queue("tas-lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "2").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1", "default").
					Priority(2).
					Queue("tas-lq-a").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "2").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue tas-cq-a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltestingapi.MakeAdmission("tas-cq-a").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Count(1).
								Assignment(corev1.ResourceCPU, "tas-default", "2").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "default").
					Priority(1).
					Queue("tas-lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "2").
						Obj()).
					Condition(metav1.Condition{
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
				"default/a1": *utiltestingapi.MakeAdmission("tas-cq-a").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "2").
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
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
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues: []kueue.ClusterQueue{*utiltestingapi.MakeClusterQueue("tas-cq-a").
				Cohort("tas-cohort-main").
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				}).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-default").
					Resource(corev1.ResourceCPU, "8", "0").
					Resource(corev1.ResourceMemory, "4Gi", "0").Obj()).
				Obj(), defaultClusterQueueB},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1-admitted", "default").
					Queue("tas-lq-a").
					Priority(1).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-cq-a").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "2").
								Count(2).
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"y1"}, 2).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltestingapi.MakePodSet("one", 2).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("a2", "default").
					UID("wl-a2").
					JobUID("job-a2").
					Queue("tas-lq-a").
					Priority(2).
					PodSets(*utiltestingapi.MakePodSet("one", 4).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "default").
					Queue("tas-lq-b").
					Priority(1).
					PodSets(*utiltestingapi.MakePodSet("one", 3).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1-admitted", "default").
					Queue("tas-lq-a").
					Priority(1).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-cq-a").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "2").
								Count(2).
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"y1"}, 2).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltestingapi.MakePodSet("one", 2).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-a2, JobUID: job-a2) due to prioritization in the ClusterQueue; preemptor path: /tas-cohort-main/tas-cq-a; preemptee path: /tas-cohort-main/tas-cq-a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-a2, JobUID: job-a2) due to prioritization in the ClusterQueue; preemptor path: /tas-cohort-main/tas-cq-a; preemptee path: /tas-cohort-main/tas-cq-a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("a2", "default").
					UID("wl-a2").
					JobUID("job-a2").
					Queue("tas-lq-a").
					Priority(2).
					PodSets(*utiltestingapi.MakePodSet("one", 4).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            `couldn't assign flavors to pod set one: topology "tas-single-level" allows to fit only 3 out of 4 pod(s). Pending the preemption of 1 workload(s)`,
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("4"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "default").
					Queue("tas-lq-b").
					Priority(1).
					PodSets(*utiltestingapi.MakePodSet("one", 3).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Condition(metav1.Condition{
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
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues: []kueue.ClusterQueue{*utiltestingapi.MakeClusterQueue("tas-cq-a").
				Cohort("tas-cohort-main").
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
				}).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-default").
					Resource(corev1.ResourceCPU, "8", "0").
					Resource(corev1.ResourceMemory, "4Gi", "0").Obj()).
				Obj(), defaultClusterQueueB},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1-admitted", "default").
					Queue("tas-lq-a").
					Priority(2).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-cq-a").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "2").
								Count(2).
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"y1"}, 2).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltestingapi.MakePodSet("one", 2).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("a2", "default").
					Queue("tas-lq-a").
					Priority(2).
					PodSets(*utiltestingapi.MakePodSet("one", 4).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "default").
					Queue("tas-lq-b").
					Priority(1).
					PodSets(*utiltestingapi.MakePodSet("one", 3).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1-admitted", "default").
					Queue("tas-lq-a").
					Priority(2).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-cq-a").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "2").
								Count(2).
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"y1"}, 2).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltestingapi.MakePodSet("one", 2).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("a2", "default").
					Queue("tas-lq-a").
					Priority(2).
					PodSets(*utiltestingapi.MakePodSet("one", 4).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            `couldn't assign flavors to pod set one: topology "tas-single-level" allows to fit only 3 out of 4 pod(s)`,
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("4"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "default").
					Queue("tas-lq-b").
					Priority(1).
					PodSets(*utiltestingapi.MakePodSet("one", 3).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Condition(metav1.Condition{
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
			topologies:      []kueue.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues: []kueue.ClusterQueue{*utiltestingapi.MakeClusterQueue("tas-cq-a").
				Cohort("tas-cohort-main").
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				}).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-default").
					Resource(corev1.ResourceCPU, "8", "0").
					Resource(corev1.ResourceMemory, "4Gi", "0").Obj()).
				Obj(), defaultClusterQueueB},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1-admitted", "default").
					Queue("tas-lq-a").
					Priority(2).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-cq-a").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "2").
								Count(2).
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"y1"}, 2).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltestingapi.MakePodSet("one", 2).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("a2", "default").
					Queue("tas-lq-a").
					Priority(2).
					PodSets(*utiltestingapi.MakePodSet("one", 4).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "default").
					Queue("tas-lq-b").
					Priority(1).
					PodSets(*utiltestingapi.MakePodSet("one", 3).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1-admitted", "default").
					Queue("tas-lq-a").
					Priority(2).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-cq-a").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "2").
								Count(2).
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"y1"}, 2).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					PodSets(*utiltestingapi.MakePodSet("one", 2).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("a2", "default").
					Queue("tas-lq-a").
					Priority(2).
					PodSets(*utiltestingapi.MakePodSet("one", 4).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            `couldn't assign flavors to pod set one: topology "tas-single-level" allows to fit only 3 out of 4 pod(s)`,
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("4"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "default").
					Queue("tas-lq-b").
					Priority(1).
					PodSets(*utiltestingapi.MakePodSet("one", 3).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue tas-cq-b",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltestingapi.MakeAdmission("tas-cq-b").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Count(3).
								Assignment(corev1.ResourceCPU, "tas-default", "3").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"y1"}, 3).Obj()).
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
				"default/b1": *utiltestingapi.MakeAdmission("tas-cq-b").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "3").
						Count(3).
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"y1"}, 3).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
		},
	}
	for name, tc := range cases {
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s WorkloadRequestUseMergePatch enabled: %t", name, enabled), func(t *testing.T) {
				features.SetFeatureGateDuringTest(t, features.WorkloadRequestUseMergePatch, enabled)
				features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, true)
				ctx, log := utiltesting.ContextWithLog(t)

				testWls := make([]kueue.Workload, 0, len(tc.workloads))
				for _, wl := range tc.workloads {
					testWls = append(testWls, *wl.DeepCopy())
				}
				clientBuilder := utiltesting.NewClientBuilder().
					WithLists(
						&kueue.WorkloadList{Items: testWls},
						&kueue.TopologyList{Items: tc.topologies},
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
				qManager := qcache.NewManagerForUnitTests(cl, cqCache)
				topologyByName := slices.ToMap(tc.topologies, func(i int) (kueue.TopologyReference, kueue.Topology) {
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
}

func TestScheduleForTASWhenWorkloadModifiedConcurrently(t *testing.T) {
	now := time.Now().Truncate(time.Second)

	const (
		tasBlockLabel = "cloud.com/topology-block"
		tasRackLabel  = "cloud.provider.com/rack"
	)

	ns := utiltesting.MakeNamespaceWrapper(metav1.NamespaceDefault).Obj()
	topology := utiltestingapi.MakeTopology("topology").
		Levels(tasBlockLabel, tasRackLabel, corev1.LabelHostname).
		Obj()
	rf := utiltestingapi.MakeResourceFlavor("rf").
		NodeLabel("tas-node", "true").
		TopologyName(topology.Name).
		Obj()
	cq := utiltestingapi.MakeClusterQueue("cq").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas(rf.Name).
				Resource(corev1.ResourceCPU, "1").
				Obj(),
		).
		Obj()
	lq := utiltestingapi.MakeLocalQueue("lq", ns.Name).ClusterQueue(cq.Name).Obj()
	testCases := map[string]struct {
		workload      *kueue.Workload
		wantWorkloads []kueue.Workload
		wantEvents    []utiltesting.EventRecord
	}{
		"use patch in evictWorkloadAfterFailedTASReplacement": {
			workload: utiltestingapi.MakeWorkload("wl", ns.Name).
				ResourceVersion("1").
				UnhealthyNodes("x0").
				Queue("tas-main").
				PodSets(*utiltestingapi.MakePodSet("one", 1).
					PreferredTopologyRequest(tasRackLabel).
					Request(corev1.ResourceCPU, "1").
					Obj()).
				ReserveQuotaAt(
					utiltestingapi.MakeAdmission(cq.Name).
						PodSets(utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, kueue.ResourceFlavorReference(rf.Name), "1000m").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x0"}, 1).Obj()).
								Obj()).
							Obj()).
						Obj(),
					now,
				).
				AdmittedAt(true, now).
				Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", ns.Name).
					ResourceVersion("3").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						PreferredTopologyRequest(tasRackLabel).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission(cq.Name).
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, kueue.ResourceFlavorReference(rf.Name), "1000m").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x0"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "NodeFailures",
						Message:            "Workload was evicted as there was no replacement for unhealthy node(s): x0",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{
						Reason: "NodeFailures",
						Count:  1,
					}).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "wl", "EvictedDueToNodeFailures", corev1.EventTypeNormal).
					Message("Workload was evicted as there was no replacement for unhealthy node(s): x0").
					Obj(),
			},
		},
	}
	for name, tc := range testCases {
		for _, useMergePatch := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s when the WorkloadRequestUseMergePatch feature is %t", name, useMergePatch), func(t *testing.T) {
				features.SetFeatureGateDuringTest(t, features.WorkloadRequestUseMergePatch, useMergePatch)
				ctx, log := utiltesting.ContextWithLog(t)
				var patched bool
				clientBuilder := utiltesting.NewClientBuilder().
					WithObjects(ns.DeepCopy(), topology.DeepCopy(), rf.DeepCopy(), cq.DeepCopy(), lq.DeepCopy(), tc.workload.DeepCopy()).
					WithStatusSubresource(&kueue.Workload{}).
					WithInterceptorFuncs(interceptor.Funcs{
						SubResourcePatch: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
							if _, ok := obj.(*kueue.Workload); ok && subResourceName == "status" && !patched {
								patched = true
								// Simulate concurrent modification by another controller
								wlCopy := tc.workload.DeepCopy()
								if wlCopy.Labels == nil {
									wlCopy.Labels = make(map[string]string, 1)
								}
								wlCopy.Labels["test.kueue.x-k8s.io/timestamp"] = time.Now().String()
								if err := c.Update(ctx, wlCopy); err != nil {
									return err
								}
							}
							return utiltesting.TreatSSAAsStrategicMerge(ctx, c, subResourceName, obj, patch, opts...)
						},
					})

				_ = tasindexer.SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder))
				cl := clientBuilder.Build()
				recorder := &utiltesting.EventRecorder{}
				fakeClock := testingclock.NewFakeClock(now)
				cqCache := schdcache.New(cl)
				qManager := qcache.NewManagerForUnitTests(cl, cqCache, qcache.WithClock(fakeClock))
				cqCache.AddOrUpdateResourceFlavor(log, rf.DeepCopy())
				cqCache.AddOrUpdateTopology(log, topology.DeepCopy())
				if err := cqCache.AddClusterQueue(ctx, cq.DeepCopy()); err != nil {
					t.Fatalf("Inserting clusterQueue %s in cache: %v", cq.Name, err)
				}
				if err := qManager.AddClusterQueue(ctx, cq.DeepCopy()); err != nil {
					t.Fatalf("Inserting clusterQueue %s in manager: %v", cq.Name, err)
				}
				if err := qManager.AddLocalQueue(ctx, lq.DeepCopy()); err != nil {
					t.Fatalf("Inserting queue %s/%s in manager: %v", lq.Namespace, lq.Name, err)
				}
				if qManager.QueueSecondPassIfNeeded(ctx, tc.workload, 0) {
					fakeClock.Step(time.Second)
				}
				scheduler := New(qManager, cqCache, cl, recorder, WithClock(t, fakeClock))
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

				gotWorkloads := &kueue.WorkloadList{}
				err := cl.List(ctx, gotWorkloads)
				if err != nil {
					t.Fatalf("Unexpected list workloads error: %v", err)
				}

				opts := cmp.Options{
					cmpopts.EquateEmpty(),
					cmpopts.IgnoreFields(metav1.ObjectMeta{}, "Labels"),
					cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
					cmpopts.SortSlices(func(a, b metav1.Condition) bool { return a.Type < b.Type }),
				}

				// The fake client with patch.Apply cannot reset the UnhealthyNodes field (patch.Merge can).
				// However, other important Status fields (e.g. Conditions) still reflect the change,
				// so we deliberately ignore the UnhealthyNodes field here.
				if !features.Enabled(features.WorkloadRequestUseMergePatch) {
					opts = append(opts, cmpopts.IgnoreFields(kueue.WorkloadStatus{}, "UnhealthyNodes"))
				}

				if diff := cmp.Diff(tc.wantWorkloads, gotWorkloads.Items, opts); diff != "" {
					t.Errorf("Unexpected scheduled workloads (-want,+got):\n%s", diff)
				}

				if diff := cmp.Diff(tc.wantEvents, recorder.RecordedEvents,
					cmpopts.IgnoreFields(utiltesting.EventRecord{}, "Message"),
				); diff != "" {
					t.Errorf("Unexpected events (-want/+got):\n%s", diff)
				}
			})
		}
	}
}

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
	"fmt"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/component-base/featuregate"
	kubefeatures "k8s.io/kubernetes/pkg/features"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingdra "sigs.k8s.io/kueue/pkg/util/testingjobs/dra"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

func TestScheduleForTASDRA(t *testing.T) {
	now := time.Now().Truncate(time.Second)

	nodes := []corev1.Node{
		*testingnode.MakeNode("x1").
			Label("tas-node", "true").
			Label(corev1.LabelHostname, "x1").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:  resource.MustParse("1"),
				corev1.ResourcePods: resource.MustParse("10"),
			}).
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
	}
	topology := *utiltestingapi.MakeDefaultOneLevelTopology("tas-single-level")
	tasFlavor := *utiltestingapi.MakeResourceFlavor("tas-default").
		NodeLabel("tas-node", "true").
		TopologyName("tas-single-level").
		Obj()
	clusterQueue := *utiltestingapi.MakeClusterQueue("tas-main").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-default").
			Resource(corev1.ResourceCPU, "50").
			Resource("example.com/gpu", "4").Obj()).
		Obj()
	queues := []kueue.LocalQueue{
		*utiltestingapi.MakeLocalQueue("tas-main", "default").ClusterQueue("tas-main").Obj(),
	}
	// One GPU on each node, in its own pool, so a rule can name either node's devices.
	devices := []client.Object{
		testingdra.MakeDeviceClass("gpu.example.com").Obj(),
		utiltesting.MakeResourceClaimTemplate("gpu-template", "default").
			DeviceRequest("gpu", "gpu.example.com", 1).
			Obj(),
		utiltesting.MakeResourceClaimTemplate("tolerant-template", "default").
			DeviceRequest("gpu", "gpu.example.com", 1).
			WithToleration("example.com/maintenance", resourceapi.DeviceTaintEffectNoSchedule).
			Obj(),
		testingdra.MakeResourceSlice("x1-gpus", "gpu.example.com").NodeName("x1").Pool("x1-gpus", 1, 1).Device("gpu-0").Obj(),
		testingdra.MakeResourceSlice("x2-gpus", "gpu.example.com").NodeName("x2").Pool("x2-gpus", 1, 1).Device("gpu-0").Obj(),
	}
	draResources := map[workload.Reference]map[kueue.PodSetReference]corev1.ResourceList{
		"default/wl": {"main": {"example.com/gpu": resource.MustParse("1")}},
	}
	eventIgnoreMessage := cmpopts.IgnoreFields(utiltesting.EventRecord{}, "Message")

	cases := map[string]tasScheduleForTASCase{
		"devices are free: admitted onto the first node": {
			nodes:           nodes,
			topologies:      []kueue.Topology{topology},
			resourceFlavors: []kueue.ResourceFlavor{tasFlavor},
			clusterQueues:   []kueue.ClusterQueue{clusterQueue},
			objects:         devices,
			draResources:    draResources,
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "default").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						ResourceClaimTemplate("gpu", "gpu-template").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/wl": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(utiltestingapi.MakePodSetAssignment("main").
						Assignment(corev1.ResourceCPU, "tas-default", "1").
						Assignment("example.com/gpu", "tas-default", "1").
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
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
				features.KueueDRAIntegration:            true,
				features.KueueDRADeviceFeasibility:      true,
				features.TASNodeFeasibilityForAllLevels: true,
			},
		},
		"a DeviceTaintRule on the first node's devices: admitted onto the second": {
			nodes:           nodes,
			topologies:      []kueue.Topology{topology},
			resourceFlavors: []kueue.ResourceFlavor{tasFlavor},
			clusterQueues:   []kueue.ClusterQueue{clusterQueue},
			objects: append([]client.Object{
				utiltesting.MakeDeviceTaintRule("maintenance", "example.com/maintenance").
					Driver("gpu.example.com").Pool("x1-gpus").Obj(),
			}, devices...),
			draResources: draResources,
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "default").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						ResourceClaimTemplate("gpu", "gpu-template").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/wl": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(utiltestingapi.MakePodSetAssignment("main").
						Assignment(corev1.ResourceCPU, "tas-default", "1").
						Assignment("example.com/gpu", "tas-default", "1").
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x2"}, 1).Obj()).
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
				features.KueueDRAIntegration:             true,
				features.KueueDRADeviceFeasibility:       true,
				features.KueueDRAIntegrationDeviceTaints: true,
				features.TASNodeFeasibilityForAllLevels:  true,
			},
		},
		"a DeviceTaintRule on every device: stays pending": {
			nodes:           nodes,
			topologies:      []kueue.Topology{topology},
			resourceFlavors: []kueue.ResourceFlavor{tasFlavor},
			clusterQueues:   []kueue.ClusterQueue{clusterQueue},
			objects: append([]client.Object{
				utiltesting.MakeDeviceTaintRule("maintenance", "example.com/maintenance").
					Driver("gpu.example.com").Obj(),
			}, devices...),
			draResources: draResources,
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "default").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						ResourceClaimTemplate("gpu", "gpu-template").
						Obj()).
					Obj(),
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-main": {"default/wl"},
			},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "wl", kueue.WorkloadQuotaReservedReasonTopologyPlacementFailed, corev1.EventTypeWarning).
					Message(`couldn't assign flavors to pod set main: topology "tas-single-level" doesn't allow to fit any of 1 pod(s); all pods must fit in a single "kubernetes.io/hostname" topology domain. Total nodes: 2; excluded: draNoFit: 2`).
					Obj(),
			},
			featureGates: map[featuregate.Feature]bool{
				features.KueueDRAIntegration:             true,
				features.KueueDRADeviceFeasibility:       true,
				features.KueueDRAIntegrationDeviceTaints: true,
				features.TASNodeFeasibilityForAllLevels:  true,
			},
		},
		"SchedulerLibraryDeepIntegration: the scheduler library places the Workload without the device check": {
			nodes:           nodes[:1],
			topologies:      []kueue.Topology{topology},
			resourceFlavors: []kueue.ResourceFlavor{tasFlavor},
			clusterQueues:   []kueue.ClusterQueue{clusterQueue},
			objects: []client.Object{
				testingdra.MakeDeviceClass("gpu.example.com").Obj(),
				utiltesting.MakeResourceClaimTemplate("gpu-template", "default").
					DeviceRequest("gpu", "gpu.example.com", 1).
					Obj(),
			},
			draResources: draResources,
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "default").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						ResourceClaimTemplate("gpu", "gpu-template").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/wl": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(utiltestingapi.MakePodSetAssignment("main").
						Assignment(corev1.ResourceCPU, "tas-default", "1").
						Assignment("example.com/gpu", "tas-default", "1").
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
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
				features.KueueDRAIntegration:             true,
				features.KueueDRADeviceFeasibility:       true,
				features.TASNodeFeasibilityForAllLevels:  true,
				kubefeatures.GenericWorkload:             true,
				features.SchedulerLibraryIntegration:     true,
				features.SchedulerLibraryDeepIntegration: true,
				// The recompute admits a refused placement without checking it again, so
				// only without it does a device check on this placement keep it pending.
				features.TASRecomputeAssignmentWithinSchedulingCycle: false,
			},
		},
		"a claim tolerating the taint: admitted onto the first node": {
			nodes:           nodes,
			topologies:      []kueue.Topology{topology},
			resourceFlavors: []kueue.ResourceFlavor{tasFlavor},
			clusterQueues:   []kueue.ClusterQueue{clusterQueue},
			objects: append([]client.Object{
				utiltesting.MakeDeviceTaintRule("maintenance", "example.com/maintenance").
					Driver("gpu.example.com").Obj(),
			}, devices...),
			draResources: draResources,
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "default").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						ResourceClaimTemplate("gpu", "tolerant-template").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/wl": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(utiltestingapi.MakePodSetAssignment("main").
						Assignment(corev1.ResourceCPU, "tas-default", "1").
						Assignment("example.com/gpu", "tas-default", "1").
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
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
				features.KueueDRAIntegration:             true,
				features.KueueDRADeviceFeasibility:       true,
				features.KueueDRAIntegrationDeviceTaints: true,
				features.TASNodeFeasibilityForAllLevels:  true,
			},
		},
		"DRADeviceTaintRules off: the rule is ignored and admitted onto the first node": {
			nodes:           nodes,
			topologies:      []kueue.Topology{topology},
			resourceFlavors: []kueue.ResourceFlavor{tasFlavor},
			clusterQueues:   []kueue.ClusterQueue{clusterQueue},
			objects: append([]client.Object{
				utiltesting.MakeDeviceTaintRule("maintenance", "example.com/maintenance").
					Driver("gpu.example.com").Obj(),
			}, devices...),
			draResources: draResources,
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "default").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						ResourceClaimTemplate("gpu", "gpu-template").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/wl": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(utiltestingapi.MakePodSetAssignment("main").
						Assignment(corev1.ResourceCPU, "tas-default", "1").
						Assignment("example.com/gpu", "tas-default", "1").
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
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
				features.KueueDRAIntegration:             true,
				features.KueueDRADeviceFeasibility:       true,
				features.KueueDRAIntegrationDeviceTaints: true,
				features.TASNodeFeasibilityForAllLevels:  true,
				kubefeatures.DRADeviceTaintRules:         false,
			},
		},
		"KueueDRAIntegrationDeviceTaints off: the rule is ignored and admitted onto the first node": {
			nodes:           nodes,
			topologies:      []kueue.Topology{topology},
			resourceFlavors: []kueue.ResourceFlavor{tasFlavor},
			clusterQueues:   []kueue.ClusterQueue{clusterQueue},
			objects: append([]client.Object{
				utiltesting.MakeDeviceTaintRule("maintenance", "example.com/maintenance").
					Driver("gpu.example.com").Obj(),
			}, devices...),
			draResources: draResources,
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "default").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						ResourceClaimTemplate("gpu", "gpu-template").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/wl": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(utiltestingapi.MakePodSetAssignment("main").
						Assignment(corev1.ResourceCPU, "tas-default", "1").
						Assignment("example.com/gpu", "tas-default", "1").
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
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
				features.KueueDRAIntegration:             true,
				features.KueueDRADeviceFeasibility:       true,
				features.KueueDRAIntegrationDeviceTaints: false,
				features.TASNodeFeasibilityForAllLevels:  true,
			},
		},
	}
	runScheduleForTASCases(t, queues, now, cases)
}

// TestScheduleForTASDRADeviceState covers what the device check takes as used beyond the
// allocated ResourceClaims: the Pods of one PodSet, admitted Workloads whose claims
// kube-scheduler has not allocated yet, Workloads admitted earlier in the cycle, and
// what a preemption gives back.
func TestScheduleForTASDRADeviceState(t *testing.T) {
	now := time.Now().Truncate(time.Second)

	nodes := []corev1.Node{
		*testingnode.MakeNode("x1").
			Label("tas-node", "true").
			Label(corev1.LabelHostname, "x1").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:  resource.MustParse("1"),
				corev1.ResourcePods: resource.MustParse("10"),
			}).
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
	}
	topology := *utiltestingapi.MakeDefaultOneLevelTopology("tas-single-level")
	tasFlavor := *utiltestingapi.MakeResourceFlavor("tas-default").
		NodeLabel("tas-node", "true").
		TopologyName("tas-single-level").
		Obj()
	clusterQueue := *utiltestingapi.MakeClusterQueue("tas-main").
		Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-default").
			Resource(corev1.ResourceCPU, "50").
			Resource("example.com/gpu", "4").Obj()).
		Obj()
	secondClusterQueue := *utiltestingapi.MakeClusterQueue("tas-second").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-default").
			Resource(corev1.ResourceCPU, "50").
			Resource("example.com/gpu", "4").Obj()).
		Obj()
	queues := []kueue.LocalQueue{
		*utiltestingapi.MakeLocalQueue("tas-main", "default").ClusterQueue("tas-main").Obj(),
		*utiltestingapi.MakeLocalQueue("tas-second", "default").ClusterQueue("tas-second").Obj(),
	}
	// One GPU on each node, so the CPU leaves room for two Pods and only the GPU limits them.
	devices := []client.Object{
		testingdra.MakeDeviceClass("gpu.example.com").Obj(),
		utiltesting.MakeResourceClaimTemplate("gpu-template", "default").
			DeviceRequest("gpu", "gpu.example.com", 1).
			Obj(),
		testingdra.MakeResourceSlice("x1-gpus", "gpu.example.com").NodeName("x1").Pool("x1-gpus", 1, 1).Device("gpu-0").Obj(),
		testingdra.MakeResourceSlice("x2-gpus", "gpu.example.com").NodeName("x2").Pool("x2-gpus", 1, 1).Device("gpu-0").Obj(),
	}
	oneGPU := corev1.ResourceList{"example.com/gpu": resource.MustParse("1")}
	// Two GPUs on each node, for a leader and its workers.
	twoGPUDevices := []client.Object{
		testingdra.MakeDeviceClass("gpu.example.com").Obj(),
		utiltesting.MakeResourceClaimTemplate("gpu-template", "default").
			DeviceRequest("gpu", "gpu.example.com", 1).
			Obj(),
		testingdra.MakeResourceSlice("x1-gpus", "gpu.example.com").NodeName("x1").Pool("x1-gpus", 1, 1).Device("gpu-0").Device("gpu-1").Obj(),
		testingdra.MakeResourceSlice("x2-gpus", "gpu.example.com").NodeName("x2").Pool("x2-gpus", 1, 1).Device("gpu-0").Device("gpu-1").Obj(),
	}
	gates := map[featuregate.Feature]bool{
		features.KueueDRAIntegration:            true,
		features.KueueDRADeviceFeasibility:      true,
		features.TASNodeFeasibilityForAllLevels: true,
	}
	eventIgnoreMessage := cmpopts.IgnoreFields(utiltesting.EventRecord{}, "Message")

	threeNodes := append(slices.Clone(nodes), *testingnode.MakeNode("x3").
		Label("tas-node", "true").
		Label(corev1.LabelHostname, "x3").
		StatusAllocatable(corev1.ResourceList{
			corev1.ResourceCPU:  resource.MustParse("1"),
			corev1.ResourcePods: resource.MustParse("10"),
		}).
		Ready().
		Obj())
	threeNodeDevices := append(slices.Clone(devices),
		testingdra.MakeResourceSlice("x3-gpus", "gpu.example.com").NodeName("x3").Pool("x3-gpus", 1, 1).Device("gpu-0").Obj())
	cases := map[string]tasScheduleForTASCase{
		"an elastic scale-up takes the GPU that a later Workload in the cycle would get": {
			nodes:           nodes,
			topologies:      []kueue.Topology{topology},
			resourceFlavors: []kueue.ResourceFlavor{tasFlavor},
			clusterQueues:   []kueue.ClusterQueue{clusterQueue, secondClusterQueue},
			objects:         devices,
			draResources: map[workload.Reference]map[kueue.PodSetReference]corev1.ResourceList{
				"default/replacement": {"main": oneGPU},
				"default/second":      {"main": oneGPU},
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("old", "default").UID("old-uid").
					Queue("tas-main").Priority(10).Annotation(constants.ElasticJobAnnotation, "true").
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						UnconstrainedTopologyRequest().
						Request(corev1.ResourceCPU, "500m").
						ResourceClaimTemplate("gpu", "gpu-template").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("tas-main").
						PodSets(utiltestingapi.MakePodSetAssignment("main").
							Assignment(corev1.ResourceCPU, "tas-default", "500m").
							Assignment("example.com/gpu", "tas-default", "1").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
								Obj()).
							Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("replacement", "default").UID("replacement-uid").
					Queue("tas-main").Priority(10).Annotation(constants.ElasticJobAnnotation, "true").
					Annotation(workloadslicing.WorkloadSliceReplacementFor, "default/old").
					Annotation(kueue.WorkloadSliceNameAnnotation, "old").
					PodSets(*utiltestingapi.MakePodSet("main", 2).
						UnconstrainedTopologyRequest().
						Request(corev1.ResourceCPU, "500m").
						ResourceClaimTemplate("gpu", "gpu-template").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("second", "default").
					Queue("tas-second").
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "500m").
						ResourceClaimTemplate("gpu", "gpu-template").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/replacement": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(utiltestingapi.MakePodSetAssignment("main").
						Assignment(corev1.ResourceCPU, "tas-default", "1").
						Assignment("example.com/gpu", "tas-default", "2").
						Count(2).
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x2"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-second": {"default/second"},
			},
			// The reason of the pending event depends on UnadmittedWorkloadsObservability.
			eventCmpOpts: cmp.Options{eventIgnoreMessage, cmpopts.SortSlices(utiltesting.SortEvents), cmpopts.IgnoreSliceElements(func(e utiltesting.EventRecord) bool {
				return e.Key.Name == "second" && e.EventType == corev1.EventTypeWarning
			})},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "replacement", "QuotaReserved", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "replacement", "Admitted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "old", "WorkloadSliceReplaced", corev1.EventTypeNormal).Obj(),
			},
			featureGates: map[featuregate.Feature]bool{
				features.KueueDRAIntegration:                 true,
				features.KueueDRADeviceFeasibility:           true,
				features.TASNodeFeasibilityForAllLevels:      true,
				features.ElasticJobsViaWorkloadSlices:        true,
				features.ElasticJobsViaWorkloadSlicesWithTAS: true,
			},
		},
		"partial admission settles on the Pods that the node's devices take": {
			nodes:           nodes,
			topologies:      []kueue.Topology{topology},
			resourceFlavors: []kueue.ResourceFlavor{tasFlavor},
			// Quota for three GPUs rejects all four Pods before TAS runs, so partial admission
			// probes smaller counts first.
			clusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("tas-main").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-default").
						Resource(corev1.ResourceCPU, "50").
						Resource("example.com/gpu", "3").Obj()).
					Obj(),
			},
			// Three GPUs on x1 and none on x2, so three Pods fit on one node.
			objects: []client.Object{
				testingdra.MakeDeviceClass("gpu.example.com").Obj(),
				utiltesting.MakeResourceClaimTemplate("gpu-template", "default").
					DeviceRequest("gpu", "gpu.example.com", 1).
					Obj(),
				testingdra.MakeResourceSlice("x1-gpus", "gpu.example.com").NodeName("x1").Pool("x1-gpus", 1, 1).
					Device("gpu-0").Device("gpu-1").Device("gpu-2").Obj(),
			},
			draResources: map[workload.Reference]map[kueue.PodSetReference]corev1.ResourceList{
				"default/wl": {"main": oneGPU},
			},
			workloads: []kueue.Workload{
				// A UID turns on the cache of matching nodes, which partial admission reuses.
				*utiltestingapi.MakeWorkload("wl", "default").
					UID("wl-uid").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("main", 4).
						SetMinimumCount(1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "100m").
						ResourceClaimTemplate("gpu", "gpu-template").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/wl": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(utiltestingapi.MakePodSetAssignment("main").
						Assignment(corev1.ResourceCPU, "tas-default", "300m").
						Assignment("example.com/gpu", "tas-default", "3").
						Count(3).
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 3).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "wl", "QuotaReserved", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "wl", "Admitted", corev1.EventTypeNormal).Obj(),
			},
			featureGates: gates,
		},
		"a failed node is replaced while the Pod on the other node keeps its GPU": {
			nodes:           threeNodes,
			topologies:      []kueue.Topology{topology},
			resourceFlavors: []kueue.ResourceFlavor{tasFlavor},
			clusterQueues:   []kueue.ClusterQueue{clusterQueue, secondClusterQueue},
			objects:         threeNodeDevices,
			draResources: map[workload.Reference]map[kueue.PodSetReference]corev1.ResourceList{
				"default/second": {"main": oneGPU},
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "default").
					UnhealthyNodes("x1").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("main", 2).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "500m").
						ResourceClaimTemplate("gpu", "gpu-template").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("tas-main").
						PodSets(utiltestingapi.MakePodSetAssignment("main").
							Assignment(corev1.ResourceCPU, "tas-default", "1").
							Assignment("example.com/gpu", "tas-default", "2").
							Count(2).
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x2"}, 1).Obj()).
								Obj()).
							Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				// Processed after the replacement, it must not get the node the replacement takes.
				*utiltestingapi.MakeWorkload("second", "default").
					Queue("tas-second").
					Priority(-1).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "500m").
						ResourceClaimTemplate("gpu", "gpu-template").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/wl": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(utiltestingapi.MakePodSetAssignment("main").
						Assignment(corev1.ResourceCPU, "tas-default", "1").
						Assignment("example.com/gpu", "tas-default", "2").
						Count(2).
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x2"}, 1).Obj()).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x3"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
				"default/second": *utiltestingapi.MakeAdmission("tas-second").
					PodSets(utiltestingapi.MakePodSetAssignment("main").
						Assignment(corev1.ResourceCPU, "tas-default", "500m").
						Assignment("example.com/gpu", "tas-default", "1").
						Count(1).
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "second", "QuotaReserved", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "second", "Admitted", corev1.EventTypeNormal).Obj(),
			},
			featureGates: map[featuregate.Feature]bool{
				features.KueueDRAIntegration:            true,
				features.KueueDRADeviceFeasibility:      true,
				features.TASNodeFeasibilityForAllLevels: true,
				features.TASFailedNodeReplacement:       true,
			},
		},
		"a leader and two workers need three GPUs on one node: stays pending": {
			nodes:           nodes,
			topologies:      []kueue.Topology{topology},
			resourceFlavors: []kueue.ResourceFlavor{tasFlavor},
			clusterQueues:   []kueue.ClusterQueue{clusterQueue},
			objects:         twoGPUDevices,
			draResources: map[workload.Reference]map[kueue.PodSetReference]corev1.ResourceList{
				"default/wl": {"leader": oneGPU, "worker": oneGPU},
			},
			workloads: []kueue.Workload{*utiltestingapi.MakeWorkload("wl", "default").
				Queue("tas-main").
				PodSets(
					*utiltestingapi.MakePodSet("leader", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						PodSetGroup("g").
						Request(corev1.ResourceCPU, "100m").
						ResourceClaimTemplate("gpu", "gpu-template").
						Obj(),
					*utiltestingapi.MakePodSet("worker", 2).
						RequiredTopologyRequest(corev1.LabelHostname).
						PodSetGroup("g").
						Request(corev1.ResourceCPU, "100m").
						ResourceClaimTemplate("gpu", "gpu-template").
						Obj(),
				).
				Obj()},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-main": {"default/wl"},
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage, cmpopts.IgnoreFields(utiltesting.EventRecord{}, "Reason")},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "wl", "", corev1.EventTypeWarning).Obj(),
			},
			featureGates: gates,
		},
		"a leader and one worker need two GPUs on one node: admitted": {
			nodes:           nodes,
			topologies:      []kueue.Topology{topology},
			resourceFlavors: []kueue.ResourceFlavor{tasFlavor},
			clusterQueues:   []kueue.ClusterQueue{clusterQueue},
			objects:         twoGPUDevices,
			draResources: map[workload.Reference]map[kueue.PodSetReference]corev1.ResourceList{
				"default/wl": {"leader": oneGPU, "worker": oneGPU},
			},
			workloads: []kueue.Workload{*utiltestingapi.MakeWorkload("wl", "default").
				Queue("tas-main").
				PodSets(
					*utiltestingapi.MakePodSet("leader", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						PodSetGroup("g").
						Request(corev1.ResourceCPU, "100m").
						ResourceClaimTemplate("gpu", "gpu-template").
						Obj(),
					*utiltestingapi.MakePodSet("worker", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						PodSetGroup("g").
						Request(corev1.ResourceCPU, "100m").
						ResourceClaimTemplate("gpu", "gpu-template").
						Obj(),
				).
				Obj()},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/wl": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(
						utiltestingapi.MakePodSetAssignment("leader").
							Assignment(corev1.ResourceCPU, "tas-default", "100m").
							Assignment("example.com/gpu", "tas-default", "1").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
								Obj()).
							Obj(),
						utiltestingapi.MakePodSetAssignment("worker").
							Assignment(corev1.ResourceCPU, "tas-default", "100m").
							Assignment("example.com/gpu", "tas-default", "1").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
								Obj()).
							Obj(),
					).
					Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "wl", "QuotaReserved", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "wl", "Admitted", corev1.EventTypeNormal).Obj(),
			},
			featureGates: gates,
		},
		"two Pods that each take a GPU: placed one per node": {
			nodes:           nodes,
			topologies:      []kueue.Topology{topology},
			resourceFlavors: []kueue.ResourceFlavor{tasFlavor},
			clusterQueues:   []kueue.ClusterQueue{clusterQueue},
			objects:         devices,
			draResources: map[workload.Reference]map[kueue.PodSetReference]corev1.ResourceList{
				"default/wl": {"main": oneGPU},
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "default").
					Queue("tas-main").
					PodSets(*utiltestingapi.MakePodSet("main", 2).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "500m").
						ResourceClaimTemplate("gpu", "gpu-template").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/wl": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(utiltestingapi.MakePodSetAssignment("main").
						Assignment(corev1.ResourceCPU, "tas-default", "1").
						Assignment("example.com/gpu", "tas-default", "2").
						Count(2).
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x2"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "wl", "QuotaReserved", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "wl", "Admitted", corev1.EventTypeNormal).Obj(),
			},
			featureGates: gates,
		},
		"an admitted Workload's Pod holds its GPU before allocation: admitted onto the other node": {
			nodes:           nodes,
			topologies:      []kueue.Topology{topology},
			resourceFlavors: []kueue.ResourceFlavor{tasFlavor},
			clusterQueues:   []kueue.ClusterQueue{clusterQueue},
			objects:         devices,
			draResources: map[workload.Reference]map[kueue.PodSetReference]corev1.ResourceList{
				"default/wl": {"main": oneGPU},
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("running", "default").
					Queue("tas-main").
					Priority(0).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "500m").
						ResourceClaimTemplate("gpu", "gpu-template").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("tas-main").
						PodSets(utiltestingapi.MakePodSetAssignment("main").
							Assignment(corev1.ResourceCPU, "tas-default", "500m").
							Assignment("example.com/gpu", "tas-default", "1").
							Count(1).
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
								Obj()).
							Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl", "default").
					Queue("tas-main").
					Priority(0).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "500m").
						ResourceClaimTemplate("gpu", "gpu-template").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/wl": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(utiltestingapi.MakePodSetAssignment("main").
						Assignment(corev1.ResourceCPU, "tas-default", "500m").
						Assignment("example.com/gpu", "tas-default", "1").
						Count(1).
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x2"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "wl", "QuotaReserved", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "wl", "Admitted", corev1.EventTypeNormal).Obj(),
			},
			featureGates: gates,
		},
		"two Workloads in one cycle: the second goes to the other node": {
			nodes:           nodes,
			topologies:      []kueue.Topology{topology},
			resourceFlavors: []kueue.ResourceFlavor{tasFlavor},
			clusterQueues:   []kueue.ClusterQueue{clusterQueue, secondClusterQueue},
			objects:         devices,
			draResources: map[workload.Reference]map[kueue.PodSetReference]corev1.ResourceList{
				"default/first":  {"main": oneGPU},
				"default/second": {"main": oneGPU},
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("first", "default").
					Queue("tas-main").
					Priority(1).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "500m").
						ResourceClaimTemplate("gpu", "gpu-template").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("second", "default").
					Queue("tas-second").
					Priority(0).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "500m").
						ResourceClaimTemplate("gpu", "gpu-template").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/first": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(utiltestingapi.MakePodSetAssignment("main").
						Assignment(corev1.ResourceCPU, "tas-default", "500m").
						Assignment("example.com/gpu", "tas-default", "1").
						Count(1).
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
				"default/second": *utiltestingapi.MakeAdmission("tas-second").
					PodSets(utiltestingapi.MakePodSetAssignment("main").
						Assignment(corev1.ResourceCPU, "tas-default", "500m").
						Assignment("example.com/gpu", "tas-default", "1").
						Count(1).
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x2"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
			// The two admissions are written by separate goroutines, so their events interleave.
			eventCmpOpts: cmp.Options{eventIgnoreMessage, cmpopts.SortSlices(utiltesting.SortEvents)},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "first", "QuotaReserved", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "first", "Admitted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "second", "QuotaReserved", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "second", "Admitted", corev1.EventTypeNormal).Obj(),
			},
			featureGates: gates,
		},
		"every GPU is held by a lower-priority Workload: preempts one": {
			nodes:           nodes,
			topologies:      []kueue.Topology{topology},
			resourceFlavors: []kueue.ResourceFlavor{tasFlavor},
			clusterQueues:   []kueue.ClusterQueue{clusterQueue},
			objects:         devices,
			draResources: map[workload.Reference]map[kueue.PodSetReference]corev1.ResourceList{
				"default/wl": {"main": oneGPU},
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("low-x1", "default").
					Queue("tas-main").
					Priority(-1).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "500m").
						ResourceClaimTemplate("gpu", "gpu-template").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("tas-main").
						PodSets(utiltestingapi.MakePodSetAssignment("main").
							Assignment(corev1.ResourceCPU, "tas-default", "500m").
							Assignment("example.com/gpu", "tas-default", "1").
							Count(1).
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
								Obj()).
							Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				// The lowest priority, so the one victim is defined.
				*utiltestingapi.MakeWorkload("low-x2", "default").
					Queue("tas-main").
					Priority(-2).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "500m").
						ResourceClaimTemplate("gpu", "gpu-template").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("tas-main").
						PodSets(utiltestingapi.MakePodSetAssignment("main").
							Assignment(corev1.ResourceCPU, "tas-default", "500m").
							Assignment("example.com/gpu", "tas-default", "1").
							Count(1).
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x2"}, 1).Obj()).
								Obj()).
							Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl", "default").
					Queue("tas-main").
					Priority(0).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "500m").
						ResourceClaimTemplate("gpu", "gpu-template").
						Obj()).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-main": {"default/wl"},
			},
			// The reason of the preemptor's pending event depends on UnadmittedWorkloadsObservability.
			eventCmpOpts: cmp.Options{eventIgnoreMessage, cmpopts.IgnoreSliceElements(func(e utiltesting.EventRecord) bool {
				return e.Key.Name == "wl" && e.EventType == corev1.EventTypeWarning
			})},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "low-x2", "EvictedDueToPreempted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "low-x2", "Preempted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "wl", "PreemptedWorkload", corev1.EventTypeNormal).Obj(),
			},
			featureGates: gates,
		},
		"a preempted Workload's claim is still allocated after its Pod is gone: waits instead of preempting again": {
			nodes:           nodes,
			topologies:      []kueue.Topology{topology},
			resourceFlavors: []kueue.ResourceFlavor{tasFlavor},
			clusterQueues:   []kueue.ClusterQueue{clusterQueue},
			// The resourceclaim controller has not deallocated the claim of the Pod that
			// the earlier preemption removed.
			objects: append([]client.Object{
				utiltesting.MakeResourceClaim("gone-gpu", "default").
					DeviceRequest("gpu", "gpu.example.com", 1).
					Allocated("gpu", "gpu.example.com", "x1-gpus", "gpu-0").
					ReservedFor("gone").
					Obj(),
			}, devices...),
			draResources: map[workload.Reference]map[kueue.PodSetReference]corev1.ResourceList{
				"default/wl": {"main": oneGPU},
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("low-x2", "default").
					Queue("tas-main").
					Priority(-1).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "500m").
						ResourceClaimTemplate("gpu", "gpu-template").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("tas-main").
						PodSets(utiltestingapi.MakePodSetAssignment("main").
							Assignment(corev1.ResourceCPU, "tas-default", "500m").
							Assignment("example.com/gpu", "tas-default", "1").
							Count(1).
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x2"}, 1).Obj()).
								Obj()).
							Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl", "default").
					Queue("tas-main").
					Priority(0).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "500m").
						ResourceClaimTemplate("gpu", "gpu-template").
						Obj()).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-main": {"default/wl"},
			},
			// The reason of the pending event depends on UnadmittedWorkloadsObservability.
			eventCmpOpts: cmp.Options{eventIgnoreMessage, cmpopts.IgnoreFields(utiltesting.EventRecord{}, "Reason")},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "wl", "", corev1.EventTypeWarning).Obj(),
			},
			featureGates: gates,
		},
	}
	// The device check wraps whichever simulator is configured, so both are run.
	for _, library := range []bool{false, true} {
		t.Run(fmt.Sprintf("SchedulerLibraryIntegration=%t", library), func(t *testing.T) {
			libraryCases := make(map[string]tasScheduleForTASCase, len(cases))
			for name, tc := range cases {
				tc.featureGates = maps.Clone(tc.featureGates)
				tc.featureGates[features.SchedulerLibraryIntegration] = library
				libraryCases[name] = tc
			}
			runScheduleForTASCases(t, queues, now, libraryCases)
		})
	}
}

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
	"slices"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	batchv1 "k8s.io/api/batch/v1"
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
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
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
	preemptingClusterQueue := *utiltestingapi.MakeClusterQueue("tas-main").
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
	// The running Pods of the Jobs low-x1 and low-x2, whose allocated claims hold the GPUs.
	jobGVK := batchv1.SchemeGroupVersion.WithKind("Job")
	lowX1Pod := *testingpod.MakePod("low-x1-0", "default").
		UID("low-x1-0").
		Annotation(kueue.WorkloadAnnotation, "low-x1").
		OwnerReferenceWithUID("low-x1", jobGVK, "low-x1-job").
		NodeName("x1").
		Obj()
	lowX1Claim := utiltesting.MakeResourceClaim("low-x1-0-gpu", "default").
		DeviceRequest("gpu", "gpu.example.com", 1).
		Allocated("gpu", "gpu.example.com", "x1-gpus", "gpu-0").
		ReservedFor("low-x1-0").
		Obj()
	lowX2Pod := *testingpod.MakePod("low-x2-0", "default").
		UID("low-x2-0").
		Annotation(kueue.WorkloadAnnotation, "low-x2").
		OwnerReferenceWithUID("low-x2", jobGVK, "low-x2-job").
		NodeName("x2").
		Obj()
	lowX2Claim := utiltesting.MakeResourceClaim("low-x2-0-gpu", "default").
		DeviceRequest("gpu", "gpu.example.com", 1).
		Allocated("gpu", "gpu.example.com", "x2-gpus", "gpu-0").
		ReservedFor("low-x2-0").
		Obj()

	cases := map[string]tasScheduleForTASCase{
		"two PodSet groups that lose their node take different GPUs": {
			nodes:           nodes,
			topologies:      []kueue.Topology{topology},
			resourceFlavors: []kueue.ResourceFlavor{tasFlavor},
			clusterQueues:   []kueue.ClusterQueue{clusterQueue},
			objects:         devices,
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "default").
					UnhealthyNodes("x3").
					Queue("tas-main").
					PodSets(
						*utiltestingapi.MakePodSet("first", 1).
							RequiredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "100m").
							ResourceClaimTemplate("gpu", "gpu-template").
							Obj(),
						*utiltestingapi.MakePodSet("second", 1).
							RequiredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "100m").
							ResourceClaimTemplate("gpu", "gpu-template").
							Obj(),
					).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(
								utiltestingapi.MakePodSetAssignment("first").
									Assignment(corev1.ResourceCPU, "tas-default", "100m").
									Assignment("example.com/gpu", "tas-default", "1").
									TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
										Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x3"}, 1).Obj()).
										Obj()).
									Obj(),
								utiltestingapi.MakePodSetAssignment("second").
									Assignment(corev1.ResourceCPU, "tas-default", "100m").
									Assignment("example.com/gpu", "tas-default", "1").
									TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
										Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x3"}, 1).Obj()).
										Obj()).
									Obj(),
							).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/wl": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(
						utiltestingapi.MakePodSetAssignment("first").
							Assignment(corev1.ResourceCPU, "tas-default", "100m").
							Assignment("example.com/gpu", "tas-default", "1").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
								Obj()).
							Obj(),
						utiltestingapi.MakePodSetAssignment("second").
							Assignment(corev1.ResourceCPU, "tas-default", "100m").
							Assignment("example.com/gpu", "tas-default", "1").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x2"}, 1).Obj()).
								Obj()).
							Obj(),
					).
					Obj(),
			},
			featureGates: gates,
		},
		"KueueDRADeviceFeasibility off: two PodSet groups that lose their node go to one node": {
			nodes:           nodes,
			topologies:      []kueue.Topology{topology},
			resourceFlavors: []kueue.ResourceFlavor{tasFlavor},
			clusterQueues:   []kueue.ClusterQueue{clusterQueue},
			objects:         devices,
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "default").
					UnhealthyNodes("x3").
					Queue("tas-main").
					PodSets(
						*utiltestingapi.MakePodSet("first", 1).
							RequiredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "100m").
							ResourceClaimTemplate("gpu", "gpu-template").
							Obj(),
						*utiltestingapi.MakePodSet("second", 1).
							RequiredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "100m").
							ResourceClaimTemplate("gpu", "gpu-template").
							Obj(),
					).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(
								utiltestingapi.MakePodSetAssignment("first").
									Assignment(corev1.ResourceCPU, "tas-default", "100m").
									Assignment("example.com/gpu", "tas-default", "1").
									TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
										Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x3"}, 1).Obj()).
										Obj()).
									Obj(),
								utiltestingapi.MakePodSetAssignment("second").
									Assignment(corev1.ResourceCPU, "tas-default", "100m").
									Assignment("example.com/gpu", "tas-default", "1").
									TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
										Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x3"}, 1).Obj()).
										Obj()).
									Obj(),
							).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/wl": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(
						utiltestingapi.MakePodSetAssignment("first").
							Assignment(corev1.ResourceCPU, "tas-default", "100m").
							Assignment("example.com/gpu", "tas-default", "1").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
								Obj()).
							Obj(),
						utiltestingapi.MakePodSetAssignment("second").
							Assignment(corev1.ResourceCPU, "tas-default", "100m").
							Assignment("example.com/gpu", "tas-default", "1").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
								Obj()).
							Obj(),
					).
					Obj(),
			},
			featureGates: map[featuregate.Feature]bool{
				features.KueueDRAIntegration:            true,
				features.KueueDRADeviceFeasibility:      false,
				features.TASNodeFeasibilityForAllLevels: true,
			},
		},
		"a leader and a worker that lose their node: the leader takes its GPU first": {
			nodes: append([]corev1.Node{
				*testingnode.MakeNode("x4").
					Label("tas-node", "true").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			}, nodes...),
			topologies:      []kueue.Topology{topology},
			resourceFlavors: []kueue.ResourceFlavor{tasFlavor},
			clusterQueues:   []kueue.ClusterQueue{clusterQueue},
			// Only x1 has a fast GPU, and it is the first device the worker could take. x4 has the
			// GPU of the worker that stays there.
			objects: []client.Object{
				testingdra.MakeDeviceClass("gpu.example.com").Obj(),
				utiltesting.MakeResourceClaimTemplate("gpu-template", "default").
					DeviceRequest("gpu", "gpu.example.com", 1).
					Obj(),
				utiltesting.MakeResourceClaimTemplate("fast-template", "default").
					DeviceRequest("gpu", "gpu.example.com", 1).
					WithCELSelectors(`device.attributes["gpu.example.com"].role == "fast"`).
					Obj(),
				testingdra.MakeResourceSlice("x1-gpus", "gpu.example.com").NodeName("x1").Pool("x1-gpus", 1, 1).
					Device("gpu-0").Attribute("role", "fast").
					Device("gpu-1").Attribute("role", "slow").
					Obj(),
				testingdra.MakeResourceSlice("x2-gpus", "gpu.example.com").NodeName("x2").Pool("x2-gpus", 1, 1).
					Device("gpu-0").Attribute("role", "slow").
					Obj(),
				testingdra.MakeResourceSlice("x4-gpus", "gpu.example.com").NodeName("x4").Pool("x4-gpus", 1, 1).
					Device("gpu-0").Attribute("role", "slow").
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "default").
					UnhealthyNodes("x3").
					Queue("tas-main").
					PodSets(
						*utiltestingapi.MakePodSet("workers", 2).
							PreferredTopologyRequest(corev1.LabelHostname).
							PodSetGroup("g").
							Request(corev1.ResourceCPU, "100m").
							ResourceClaimTemplate("gpu", "gpu-template").
							Obj(),
						*utiltestingapi.MakePodSet("leader", 1).
							PreferredTopologyRequest(corev1.LabelHostname).
							PodSetGroup("g").
							Request(corev1.ResourceCPU, "100m").
							ResourceClaimTemplate("gpu", "fast-template").
							Obj(),
					).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(
								utiltestingapi.MakePodSetAssignment("workers").
									Assignment(corev1.ResourceCPU, "tas-default", "200m").
									Assignment("example.com/gpu", "tas-default", "2").
									Count(2).
									TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
										Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x3"}, 1).Obj()).
										Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x4"}, 1).Obj()).
										Obj()).
									Obj(),
								utiltestingapi.MakePodSetAssignment("leader").
									Assignment(corev1.ResourceCPU, "tas-default", "100m").
									Assignment("example.com/gpu", "tas-default", "1").
									TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
										Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x3"}, 1).Obj()).
										Obj()).
									Obj(),
							).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/wl": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(
						utiltestingapi.MakePodSetAssignment("workers").
							Assignment(corev1.ResourceCPU, "tas-default", "200m").
							Assignment("example.com/gpu", "tas-default", "2").
							Count(2).
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x4"}, 1).Obj()).
								Obj()).
							Obj(),
						utiltestingapi.MakePodSetAssignment("leader").
							Assignment(corev1.ResourceCPU, "tas-default", "100m").
							Assignment("example.com/gpu", "tas-default", "1").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
								Obj()).
							Obj(),
					).
					Obj(),
			},
			featureGates: gates,
		},
		"KueueDRADeviceFeasibility off: a leader and a worker that lose their node": {
			nodes: append([]corev1.Node{
				*testingnode.MakeNode("x4").
					Label("tas-node", "true").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			}, nodes...),
			topologies:      []kueue.Topology{topology},
			resourceFlavors: []kueue.ResourceFlavor{tasFlavor},
			clusterQueues:   []kueue.ClusterQueue{clusterQueue},
			objects: []client.Object{
				testingdra.MakeDeviceClass("gpu.example.com").Obj(),
				utiltesting.MakeResourceClaimTemplate("gpu-template", "default").
					DeviceRequest("gpu", "gpu.example.com", 1).
					Obj(),
				utiltesting.MakeResourceClaimTemplate("fast-template", "default").
					DeviceRequest("gpu", "gpu.example.com", 1).
					WithCELSelectors(`device.attributes["gpu.example.com"].role == "fast"`).
					Obj(),
				testingdra.MakeResourceSlice("x1-gpus", "gpu.example.com").NodeName("x1").Pool("x1-gpus", 1, 1).
					Device("gpu-0").Attribute("role", "fast").
					Device("gpu-1").Attribute("role", "slow").
					Obj(),
				testingdra.MakeResourceSlice("x2-gpus", "gpu.example.com").NodeName("x2").Pool("x2-gpus", 1, 1).
					Device("gpu-0").Attribute("role", "slow").
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "default").
					UnhealthyNodes("x3").
					Queue("tas-main").
					PodSets(
						*utiltestingapi.MakePodSet("workers", 2).
							PreferredTopologyRequest(corev1.LabelHostname).
							PodSetGroup("g").
							Request(corev1.ResourceCPU, "100m").
							ResourceClaimTemplate("gpu", "gpu-template").
							Obj(),
						*utiltestingapi.MakePodSet("leader", 1).
							PreferredTopologyRequest(corev1.LabelHostname).
							PodSetGroup("g").
							Request(corev1.ResourceCPU, "100m").
							ResourceClaimTemplate("gpu", "fast-template").
							Obj(),
					).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("tas-main").
							PodSets(
								utiltestingapi.MakePodSetAssignment("workers").
									Assignment(corev1.ResourceCPU, "tas-default", "200m").
									Assignment("example.com/gpu", "tas-default", "2").
									Count(2).
									TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
										Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x3"}, 1).Obj()).
										Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x4"}, 1).Obj()).
										Obj()).
									Obj(),
								utiltestingapi.MakePodSetAssignment("leader").
									Assignment(corev1.ResourceCPU, "tas-default", "100m").
									Assignment("example.com/gpu", "tas-default", "1").
									TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
										Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x3"}, 1).Obj()).
										Obj()).
									Obj(),
							).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/wl": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(
						utiltestingapi.MakePodSetAssignment("workers").
							Assignment(corev1.ResourceCPU, "tas-default", "200m").
							Assignment("example.com/gpu", "tas-default", "2").
							Count(2).
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x4"}, 2).Obj()).
								Obj()).
							Obj(),
						utiltestingapi.MakePodSetAssignment("leader").
							Assignment(corev1.ResourceCPU, "tas-default", "100m").
							Assignment("example.com/gpu", "tas-default", "1").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x4"}, 1).Obj()).
								Obj()).
							Obj(),
					).
					Obj(),
			},
			featureGates: map[featuregate.Feature]bool{
				features.KueueDRAIntegration:            true,
				features.KueueDRADeviceFeasibility:      false,
				features.TASNodeFeasibilityForAllLevels: true,
			},
		},
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
			featureGates: map[featuregate.Feature]bool{features.KueueDRADeviceFeasibility: true},
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
				features.KueueDRADeviceFeasibility:       true,
				features.KueueDRAIntegrationDeviceTaints: true,
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
				features.KueueDRADeviceFeasibility:       true,
				features.KueueDRAIntegrationDeviceTaints: true,
			},
		},
		"SchedulerLibraryDeepIntegration on: a ClusterQueue that TAS places for still checks the devices": {
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
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-main": {"default/wl"},
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "wl", kueue.WorkloadQuotaReservedReasonTopologyPlacementFailed, corev1.EventTypeWarning).Obj(),
			},
			featureGates: map[featuregate.Feature]bool{
				features.KueueDRAIntegration:             true,
				features.KueueDRADeviceFeasibility:       true,
				features.TASNodeFeasibilityForAllLevels:  true,
				kubefeatures.GenericWorkload:             true,
				features.SchedulerLibraryIntegration:     true,
				features.SchedulerLibraryDeepIntegration: true,
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
				features.KueueDRADeviceFeasibility:       true,
				features.KueueDRAIntegrationDeviceTaints: true,
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
				features.KueueDRADeviceFeasibility:       true,
				features.KueueDRAIntegrationDeviceTaints: true,
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
				features.KueueDRADeviceFeasibility:       true,
				features.KueueDRAIntegrationDeviceTaints: false,
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
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "wl", kueue.WorkloadQuotaReservedReasonTopologyPlacementFailed, corev1.EventTypeWarning).Obj(),
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
		"KueueDRADeviceFeasibility off: two Pods that each take a GPU go to one node": {
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
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 2).Obj()).
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
				features.KueueDRADeviceFeasibility:      false,
				features.TASNodeFeasibilityForAllLevels: true,
			},
		},
		"a leader that needs one GPU of its node keeps it from its workers for a later group": {
			nodes:           nodes,
			topologies:      []kueue.Topology{topology},
			resourceFlavors: []kueue.ResourceFlavor{tasFlavor},
			clusterQueues:   []kueue.ClusterQueue{clusterQueue},
			// Only x1 has a fast GPU, and it is the first device the workers could take.
			objects: []client.Object{
				testingdra.MakeDeviceClass("gpu.example.com").Obj(),
				utiltesting.MakeResourceClaimTemplate("gpu-template", "default").
					DeviceRequest("gpu", "gpu.example.com", 1).
					Obj(),
				utiltesting.MakeResourceClaimTemplate("fast-template", "default").
					DeviceRequest("gpu", "gpu.example.com", 1).
					WithCELSelectors(`device.attributes["gpu.example.com"].role == "fast"`).
					Obj(),
				testingdra.MakeResourceSlice("x1-gpus", "gpu.example.com").NodeName("x1").Pool("x1-gpus", 1, 1).
					Device("gpu-0").Attribute("role", "fast").
					Device("gpu-1").Attribute("role", "slow").
					Obj(),
				testingdra.MakeResourceSlice("x2-gpus", "gpu.example.com").NodeName("x2").Pool("x2-gpus", 1, 1).
					Device("gpu-0").Attribute("role", "slow").
					Obj(),
			},
			draResources: map[workload.Reference]map[kueue.PodSetReference]corev1.ResourceList{
				"default/wl": {"workers": oneGPU, "leader": oneGPU, "later": oneGPU},
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "default").
					Queue("tas-main").
					PodSets(
						*utiltestingapi.MakePodSet("workers", 1).
							RequiredTopologyRequest(corev1.LabelHostname).
							PodSetGroup("g").
							Request(corev1.ResourceCPU, "100m").
							ResourceClaimTemplate("gpu", "gpu-template").
							Obj(),
						*utiltestingapi.MakePodSet("leader", 1).
							RequiredTopologyRequest(corev1.LabelHostname).
							PodSetGroup("g").
							Request(corev1.ResourceCPU, "100m").
							ResourceClaimTemplate("gpu", "fast-template").
							Obj(),
						*utiltestingapi.MakePodSet("later", 1).
							RequiredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "100m").
							ResourceClaimTemplate("gpu", "gpu-template").
							Obj(),
					).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/wl": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(
						utiltestingapi.MakePodSetAssignment("workers").
							Assignment(corev1.ResourceCPU, "tas-default", "100m").
							Assignment("example.com/gpu", "tas-default", "1").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
								Obj()).
							Obj(),
						utiltestingapi.MakePodSetAssignment("leader").
							Assignment(corev1.ResourceCPU, "tas-default", "100m").
							Assignment("example.com/gpu", "tas-default", "1").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
								Obj()).
							Obj(),
						utiltestingapi.MakePodSetAssignment("later").
							Assignment(corev1.ResourceCPU, "tas-default", "100m").
							Assignment("example.com/gpu", "tas-default", "1").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x2"}, 1).Obj()).
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
		"an elastic scale-up of two PodSet groups: the second group's new Pod sees the GPU the first one's took": {
			nodes:           nodes,
			topologies:      []kueue.Topology{topology},
			resourceFlavors: []kueue.ResourceFlavor{tasFlavor},
			clusterQueues:   []kueue.ClusterQueue{clusterQueue},
			// The old slice's two Pods are not bound yet, so Kueue's promises hold two of the three
			// GPUs on x1, which leaves one GPU for the two new Pods there.
			objects: []client.Object{
				testingdra.MakeDeviceClass("gpu.example.com").Obj(),
				utiltesting.MakeResourceClaimTemplate("gpu-template", "default").
					DeviceRequest("gpu", "gpu.example.com", 1).
					Obj(),
				testingdra.MakeResourceSlice("x1-gpus", "gpu.example.com").NodeName("x1").Pool("x1-gpus", 1, 1).
					Device("gpu-0").Device("gpu-1").Device("gpu-2").
					Obj(),
				testingdra.MakeResourceSlice("x2-gpus", "gpu.example.com").NodeName("x2").Pool("x2-gpus", 1, 1).Device("gpu-0").Obj(),
			},
			draResources: map[workload.Reference]map[kueue.PodSetReference]corev1.ResourceList{
				"default/replacement": {"first": oneGPU, "second": oneGPU},
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("old", "default").UID("old-uid").
					Queue("tas-main").Annotation(constants.ElasticJobAnnotation, "true").
					PodSets(
						*utiltestingapi.MakePodSet("first", 1).
							UnconstrainedTopologyRequest().
							Request(corev1.ResourceCPU, "100m").
							ResourceClaimTemplate("gpu", "gpu-template").
							Obj(),
						*utiltestingapi.MakePodSet("second", 1).
							UnconstrainedTopologyRequest().
							Request(corev1.ResourceCPU, "100m").
							ResourceClaimTemplate("gpu", "gpu-template").
							Obj(),
					).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("tas-main").
						PodSets(
							utiltestingapi.MakePodSetAssignment("first").
								Assignment(corev1.ResourceCPU, "tas-default", "100m").
								Assignment("example.com/gpu", "tas-default", "1").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj(),
							utiltestingapi.MakePodSetAssignment("second").
								Assignment(corev1.ResourceCPU, "tas-default", "100m").
								Assignment("example.com/gpu", "tas-default", "1").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj(),
						).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("replacement", "default").UID("replacement-uid").
					Queue("tas-main").Annotation(constants.ElasticJobAnnotation, "true").
					Annotation(workloadslicing.WorkloadSliceReplacementFor, "default/old").
					Annotation(kueue.WorkloadSliceNameAnnotation, "old").
					PodSets(
						*utiltestingapi.MakePodSet("first", 2).
							UnconstrainedTopologyRequest().
							Request(corev1.ResourceCPU, "100m").
							ResourceClaimTemplate("gpu", "gpu-template").
							Obj(),
						*utiltestingapi.MakePodSet("second", 2).
							UnconstrainedTopologyRequest().
							Request(corev1.ResourceCPU, "100m").
							ResourceClaimTemplate("gpu", "gpu-template").
							Obj(),
					).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/replacement": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(
						utiltestingapi.MakePodSetAssignment("first").
							Assignment(corev1.ResourceCPU, "tas-default", "200m").
							Assignment("example.com/gpu", "tas-default", "2").
							Count(2).
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 2).Obj()).
								Obj()).
							Obj(),
						utiltestingapi.MakePodSetAssignment("second").
							Assignment(corev1.ResourceCPU, "tas-default", "200m").
							Assignment("example.com/gpu", "tas-default", "2").
							Count(2).
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x2"}, 1).Obj()).
								Obj()).
							Obj(),
					).
					Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage, cmpopts.SortSlices(utiltesting.SortEvents)},
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
		"two PodSet groups that each take a GPU: placed one per node": {
			nodes:           nodes,
			topologies:      []kueue.Topology{topology},
			resourceFlavors: []kueue.ResourceFlavor{tasFlavor},
			clusterQueues:   []kueue.ClusterQueue{clusterQueue},
			objects:         devices,
			draResources: map[workload.Reference]map[kueue.PodSetReference]corev1.ResourceList{
				"default/wl": {"first": oneGPU, "second": oneGPU},
			},
			workloads: []kueue.Workload{
				// A UID turns on the cache of matching nodes, which must not hide the GPU the
				// first group took from the second.
				*utiltestingapi.MakeWorkload("wl", "default").
					UID("wl-uid").
					Queue("tas-main").
					PodSets(
						*utiltestingapi.MakePodSet("first", 1).
							RequiredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "100m").
							ResourceClaimTemplate("gpu", "gpu-template").
							Obj(),
						*utiltestingapi.MakePodSet("second", 1).
							RequiredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "100m").
							ResourceClaimTemplate("gpu", "gpu-template").
							Obj(),
					).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/wl": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(
						utiltestingapi.MakePodSetAssignment("first").
							Assignment(corev1.ResourceCPU, "tas-default", "100m").
							Assignment("example.com/gpu", "tas-default", "1").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
								Obj()).
							Obj(),
						utiltestingapi.MakePodSetAssignment("second").
							Assignment(corev1.ResourceCPU, "tas-default", "100m").
							Assignment("example.com/gpu", "tas-default", "1").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x2"}, 1).Obj()).
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
		"hostname labels that differ from node names: two PodSet groups placed one per node": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label("tas-node", "true").
					Label(corev1.LabelHostname, "host-x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("x2").
					Label("tas-node", "true").
					Label(corev1.LabelHostname, "host-x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			topologies:      []kueue.Topology{topology},
			resourceFlavors: []kueue.ResourceFlavor{tasFlavor},
			clusterQueues:   []kueue.ClusterQueue{clusterQueue},
			objects:         devices,
			draResources: map[workload.Reference]map[kueue.PodSetReference]corev1.ResourceList{
				"default/wl": {"first": oneGPU, "second": oneGPU},
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "default").
					Queue("tas-main").
					PodSets(
						*utiltestingapi.MakePodSet("first", 1).
							RequiredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "100m").
							ResourceClaimTemplate("gpu", "gpu-template").
							Obj(),
						*utiltestingapi.MakePodSet("second", 1).
							RequiredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "100m").
							ResourceClaimTemplate("gpu", "gpu-template").
							Obj(),
					).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/wl": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(
						utiltestingapi.MakePodSetAssignment("first").
							Assignment(corev1.ResourceCPU, "tas-default", "100m").
							Assignment("example.com/gpu", "tas-default", "1").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"host-x1"}, 1).Obj()).
								Obj()).
							Obj(),
						utiltestingapi.MakePodSetAssignment("second").
							Assignment(corev1.ResourceCPU, "tas-default", "100m").
							Assignment("example.com/gpu", "tas-default", "1").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"host-x2"}, 1).Obj()).
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
		"a topology without a hostname level: two PodSet groups placed one per node": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label("tas-node", "true").
					Label("cloud.com/rack", "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("x2").
					Label("tas-node", "true").
					Label("cloud.com/rack", "r2").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			topologies: []kueue.Topology{*utiltestingapi.MakeTopology("tas-rack").Levels("cloud.com/rack").Obj()},
			resourceFlavors: []kueue.ResourceFlavor{*utiltestingapi.MakeResourceFlavor("tas-default").
				NodeLabel("tas-node", "true").
				TopologyName("tas-rack").
				Obj()},
			clusterQueues: []kueue.ClusterQueue{clusterQueue},
			objects:       devices,
			draResources: map[workload.Reference]map[kueue.PodSetReference]corev1.ResourceList{
				"default/wl": {"first": oneGPU, "second": oneGPU},
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "default").
					Queue("tas-main").
					PodSets(
						*utiltestingapi.MakePodSet("first", 1).
							RequiredTopologyRequest("cloud.com/rack").
							Request(corev1.ResourceCPU, "100m").
							ResourceClaimTemplate("gpu", "gpu-template").
							Obj(),
						*utiltestingapi.MakePodSet("second", 1).
							RequiredTopologyRequest("cloud.com/rack").
							Request(corev1.ResourceCPU, "100m").
							ResourceClaimTemplate("gpu", "gpu-template").
							Obj(),
					).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/wl": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(
						utiltestingapi.MakePodSetAssignment("first").
							Assignment(corev1.ResourceCPU, "tas-default", "100m").
							Assignment("example.com/gpu", "tas-default", "1").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{"cloud.com/rack"}).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"r1"}, 1).Obj()).
								Obj()).
							Obj(),
						utiltestingapi.MakePodSetAssignment("second").
							Assignment(corev1.ResourceCPU, "tas-default", "100m").
							Assignment("example.com/gpu", "tas-default", "1").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{"cloud.com/rack"}).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"r2"}, 1).Obj()).
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
		"partial admission shrinks the first PodSet group: the second group gets the GPU it frees": {
			nodes:           nodes,
			topologies:      []kueue.Topology{topology},
			resourceFlavors: []kueue.ResourceFlavor{tasFlavor},
			clusterQueues:   []kueue.ClusterQueue{clusterQueue},
			objects:         devices,
			draResources: map[workload.Reference]map[kueue.PodSetReference]corev1.ResourceList{
				"default/wl": {"first": oneGPU, "second": oneGPU},
			},
			workloads: []kueue.Workload{
				// With all its Pods the first group takes both GPUs. The UID turns on the cache
				// of matching nodes, so the second group must not reuse what it found then.
				*utiltestingapi.MakeWorkload("wl", "default").
					UID("wl-uid").
					Queue("tas-main").
					PodSets(
						*utiltestingapi.MakePodSet("first", 2).
							SetMinimumCount(1).
							PreferredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "100m").
							ResourceClaimTemplate("gpu", "gpu-template").
							Obj(),
						*utiltestingapi.MakePodSet("second", 1).
							RequiredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "100m").
							ResourceClaimTemplate("gpu", "gpu-template").
							Obj(),
					).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/wl": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(
						utiltestingapi.MakePodSetAssignment("first").
							Assignment(corev1.ResourceCPU, "tas-default", "100m").
							Assignment("example.com/gpu", "tas-default", "1").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
								Obj()).
							Obj(),
						utiltestingapi.MakePodSetAssignment("second").
							Assignment(corev1.ResourceCPU, "tas-default", "100m").
							Assignment("example.com/gpu", "tas-default", "1").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&topology)).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x2"}, 1).Obj()).
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
		"two PodSets in two flavors over the one GPU of a node: stays pending after the recompute": {
			nodes:      nodes[:1],
			topologies: []kueue.Topology{topology},
			// Both flavors select the same node, and the placement in one flavor does not
			// see the devices the other flavor's PodSets take.
			resourceFlavors: []kueue.ResourceFlavor{
				*utiltestingapi.MakeResourceFlavor("tas-a").
					NodeLabel("tas-node", "true").
					TopologyName("tas-single-level").
					Obj(),
				*utiltestingapi.MakeResourceFlavor("tas-b").
					NodeLabel("tas-node", "true").
					TopologyName("tas-single-level").
					Obj(),
			},
			// One GPU of quota in each flavor puts each PodSet in its own flavor.
			clusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("tas-main").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("tas-a").
							Resource(corev1.ResourceCPU, "50").
							Resource("example.com/gpu", "1").Obj(),
						*utiltestingapi.MakeFlavorQuotas("tas-b").
							Resource(corev1.ResourceCPU, "50").
							Resource("example.com/gpu", "1").Obj(),
					).
					Obj(),
			},
			objects: []client.Object{
				testingdra.MakeDeviceClass("gpu.example.com").Obj(),
				utiltesting.MakeResourceClaimTemplate("gpu-template", "default").
					DeviceRequest("gpu", "gpu.example.com", 1).
					Obj(),
				testingdra.MakeResourceSlice("x1-gpus", "gpu.example.com").NodeName("x1").Pool("x1-gpus", 1, 1).Device("gpu-0").Obj(),
			},
			draResources: map[workload.Reference]map[kueue.PodSetReference]corev1.ResourceList{
				"default/wl": {"first": oneGPU, "second": oneGPU},
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "default").
					Queue("tas-main").
					PodSets(
						*utiltestingapi.MakePodSet("first", 1).
							RequiredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "100m").
							ResourceClaimTemplate("gpu", "gpu-template").
							Obj(),
						*utiltestingapi.MakePodSet("second", 1).
							RequiredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "100m").
							ResourceClaimTemplate("gpu", "gpu-template").
							Obj(),
					).
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
			featureGates: map[featuregate.Feature]bool{
				features.KueueDRAIntegration:                         true,
				features.KueueDRADeviceFeasibility:                   true,
				features.TASNodeFeasibilityForAllLevels:              true,
				features.TASRecomputeAssignmentWithinSchedulingCycle: true,
			},
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
		"every GPU is promised to a lower-priority Workload whose Pods are not bound yet: preempts one": {
			nodes:           nodes,
			topologies:      []kueue.Topology{topology},
			resourceFlavors: []kueue.ResourceFlavor{tasFlavor},
			clusterQueues:   []kueue.ClusterQueue{preemptingClusterQueue},
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
		"every GPU is held by a lower-priority Workload: preempts one": {
			nodes:           nodes,
			topologies:      []kueue.Topology{topology},
			resourceFlavors: []kueue.ResourceFlavor{tasFlavor},
			clusterQueues:   []kueue.ClusterQueue{preemptingClusterQueue},
			objects:         append([]client.Object{lowX1Claim, lowX2Claim}, devices...),
			pods:            []corev1.Pod{lowX1Pod, lowX2Pod},
			draResources: map[workload.Reference]map[kueue.PodSetReference]corev1.ResourceList{
				"default/wl": {"main": oneGPU},
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("low-x1", "default").
					ControllerReference(jobGVK, "low-x1", "low-x1-job").
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
					ControllerReference(jobGVK, "low-x2", "low-x2-job").
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
		"KueueDRADeviceFeasibility off: every GPU is held by a lower-priority Workload: admitted without preempting": {
			nodes:           nodes,
			topologies:      []kueue.Topology{topology},
			resourceFlavors: []kueue.ResourceFlavor{tasFlavor},
			clusterQueues:   []kueue.ClusterQueue{preemptingClusterQueue},
			objects:         append([]client.Object{lowX1Claim, lowX2Claim}, devices...),
			pods:            []corev1.Pod{lowX1Pod, lowX2Pod},
			draResources: map[workload.Reference]map[kueue.PodSetReference]corev1.ResourceList{
				"default/wl": {"main": oneGPU},
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("low-x1", "default").
					ControllerReference(jobGVK, "low-x1", "low-x1-job").
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
					ControllerReference(jobGVK, "low-x2", "low-x2-job").
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
			// Without the device check, the GPUs the claims hold do not count.
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/wl": *utiltestingapi.MakeAdmission("tas-main").
					PodSets(utiltestingapi.MakePodSetAssignment("main").
						Assignment(corev1.ResourceCPU, "tas-default", "500m").
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
				features.KueueDRADeviceFeasibility:      false,
				features.TASNodeFeasibilityForAllLevels: true,
			},
		},
		"every GPU is held by a lower-priority Workload, with the cache of matching nodes: preempts one": {
			nodes:           nodes,
			topologies:      []kueue.Topology{topology},
			resourceFlavors: []kueue.ResourceFlavor{tasFlavor},
			clusterQueues:   []kueue.ClusterQueue{preemptingClusterQueue},
			objects:         append([]client.Object{lowX1Claim, lowX2Claim}, devices...),
			pods:            []corev1.Pod{lowX1Pod, lowX2Pod},
			draResources: map[workload.Reference]map[kueue.PodSetReference]corev1.ResourceList{
				"default/wl": {"main": oneGPU},
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("low-x1", "default").
					ControllerReference(jobGVK, "low-x1", "low-x1-job").
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
					ControllerReference(jobGVK, "low-x2", "low-x2-job").
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
				// A UID turns on the cache of matching nodes. What fits now must not answer
				// what fits once every Workload is preempted.
				*utiltestingapi.MakeWorkload("wl", "default").
					UID("wl-uid").
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
			clusterQueues:   []kueue.ClusterQueue{preemptingClusterQueue},
			// The resourceclaim controller has not deallocated the claim of the Pod that
			// the earlier preemption removed.
			objects: append([]client.Object{
				utiltesting.MakeResourceClaim("gone-gpu", "default").
					DeviceRequest("gpu", "gpu.example.com", 1).
					Allocated("gpu", "gpu.example.com", "x1-gpus", "gpu-0").
					ReservedFor("gone").
					Obj(),
				lowX2Claim,
			}, devices...),
			pods: []corev1.Pod{lowX2Pod},
			draResources: map[workload.Reference]map[kueue.PodSetReference]corev1.ResourceList{
				"default/wl": {"main": oneGPU},
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("low-x2", "default").
					ControllerReference(jobGVK, "low-x2", "low-x2-job").
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
			eventCmpOpts: cmp.Options{cmpopts.IgnoreFields(utiltesting.EventRecord{}, "Reason")},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "wl", "", corev1.EventTypeWarning).
					Message("Workload will fit after the ResourceClaim controller deallocates the devices of Pods that are gone").
					Obj(),
			},
			featureGates: gates,
		},
		"TASResidualPodAccounting off: a claim still allocated after its Pod is gone counts as held, so preempts one": {
			nodes:           nodes,
			topologies:      []kueue.Topology{topology},
			resourceFlavors: []kueue.ResourceFlavor{tasFlavor},
			clusterQueues:   []kueue.ClusterQueue{preemptingClusterQueue},
			// The resourceclaim controller has not deallocated the claim of the Pod that
			// the earlier preemption removed.
			objects: append([]client.Object{
				utiltesting.MakeResourceClaim("gone-gpu", "default").
					DeviceRequest("gpu", "gpu.example.com", 1).
					Allocated("gpu", "gpu.example.com", "x1-gpus", "gpu-0").
					ReservedFor("gone").
					Obj(),
				lowX2Claim,
			}, devices...),
			pods: []corev1.Pod{lowX2Pod},
			draResources: map[workload.Reference]map[kueue.PodSetReference]corev1.ResourceList{
				"default/wl": {"main": oneGPU},
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("low-x2", "default").
					ControllerReference(jobGVK, "low-x2", "low-x2-job").
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
			// The reason of the preemptor's pending event depends on UnadmittedWorkloadsObservability.
			eventCmpOpts: cmp.Options{eventIgnoreMessage, cmpopts.IgnoreSliceElements(func(e utiltesting.EventRecord) bool {
				return e.Key.Name == "wl" && e.EventType == corev1.EventTypeWarning
			})},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "low-x2", "EvictedDueToPreempted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "low-x2", "Preempted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "wl", "PreemptedWorkload", corev1.EventTypeNormal).Obj(),
			},
			featureGates: map[featuregate.Feature]bool{
				features.KueueDRAIntegration:            true,
				features.KueueDRADeviceFeasibility:      true,
				features.TASNodeFeasibilityForAllLevels: true,
				features.TASResidualPodAccounting:       false,
			},
		},
	}
	// The device check wraps whichever simulator is configured, so both are run.
	for _, library := range []bool{false, true} {
		t.Run(fmt.Sprintf("SchedulerLibraryIntegration=%t", library), func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.SchedulerLibraryIntegration, library)
			runScheduleForTASCases(t, queues, now, cases)
		})
	}
}

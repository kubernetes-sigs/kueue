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
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/component-base/featuregate"
	kubefeatures "k8s.io/kubernetes/pkg/features"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingdra "sigs.k8s.io/kueue/pkg/util/testingjobs/dra"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
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
		utiltesting.MakeResourceSlice("x1-gpus", "gpu.example.com").NodeName("x1").Pool("x1-gpus", 1, 1).Device("gpu-0").Obj(),
		utiltesting.MakeResourceSlice("x2-gpus", "gpu.example.com").NodeName("x2").Pool("x2-gpus", 1, 1).Device("gpu-0").Obj(),
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
					Message(`couldn't assign flavors to pod set main: topology "tas-single-level" doesn't allow to fit any of 1 pod(s). Total nodes: 2; excluded: draNoFit: 2`).
					Obj(),
			},
			featureGates: map[featuregate.Feature]bool{
				features.KueueDRAIntegration:             true,
				features.KueueDRADeviceFeasibility:       true,
				features.KueueDRAIntegrationDeviceTaints: true,
				features.TASNodeFeasibilityForAllLevels:  true,
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

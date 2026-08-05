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

	"github.com/go-logr/logr"
	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"

	"sigs.k8s.io/kueue/pkg/resources"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestTASCacheUpdateFlavorTolerationsPreservesUsage(t *testing.T) {
	tasCache := NewTASCache(nil, newDefaultSimulator(), resources.NewResourceFormatter())
	topology := utiltestingapi.MakeDefaultOneLevelTopology("default")
	tasCache.AddTopology(topology)

	flavor := utiltestingapi.MakeResourceFlavor("tas-flavor").
		NodeLabel("node-group", "tas").
		TopologyName(topology.Name).
		Obj()
	tasCache.AddOrUpdateFlavor(flavor)

	const wlKey = workload.Reference("default/wl")
	topologyRequests := []workload.TopologyDomainRequests{{
		Values: []string{"x1"},
		Count:  1,
		SinglePodRequests: resources.NewRequestsFromMap(map[corev1.ResourceName]int64{
			corev1.ResourceCPU: 1,
		}),
	}}
	originalFlavorCache := tasCache.Get("tas-flavor")
	originalFlavorCache.addUsage(logr.Discard(), wlKey, topologyRequests)

	toleration := corev1.Toleration{
		Key:      "example.com/dedicated",
		Operator: corev1.TolerationOpEqual,
		Value:    "tas",
		Effect:   corev1.TaintEffectNoSchedule,
	}
	flavor.Spec.Tolerations = []corev1.Toleration{toleration}
	tasCache.AddOrUpdateFlavor(flavor)

	updatedFlavorCache := tasCache.Get("tas-flavor")
	if updatedFlavorCache != originalFlavorCache {
		t.Fatal("TAS flavor cache was replaced while updating tolerations")
	}

	wantUsage := map[utiltas.TopologyDomainID]resources.Requests{
		"x1": resources.NewRequestsFromMap(map[corev1.ResourceName]int64{
			corev1.ResourceCPU:  1,
			corev1.ResourcePods: 1,
		}),
	}
	if diff := cmp.Diff([]corev1.Toleration{toleration}, updatedFlavorCache.flavor.Tolerations); diff != "" {
		t.Errorf("Unexpected tolerations after update (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(wantUsage, updatedFlavorCache.usage, cmp.Comparer(resources.Equal)); diff != "" {
		t.Errorf("Unexpected usage after adding toleration (-want +got):\n%s", diff)
	}
	if _, found := updatedFlavorCache.wlUsage[wlKey]; !found {
		t.Error("Workload usage was removed after adding toleration")
	}

	flavor.Spec.Tolerations = nil
	tasCache.AddOrUpdateFlavor(flavor)

	if len(updatedFlavorCache.flavor.Tolerations) != 0 {
		t.Errorf("Expected tolerations to be empty after removal, got %v", updatedFlavorCache.flavor.Tolerations)
	}
	if diff := cmp.Diff(wantUsage, updatedFlavorCache.usage, cmp.Comparer(resources.Equal)); diff != "" {
		t.Errorf("Unexpected usage after removing toleration (-want +got):\n%s", diff)
	}
	if _, found := updatedFlavorCache.wlUsage[wlKey]; !found {
		t.Error("Workload usage was removed after removing toleration")
	}
}

func TestTASCacheUpdateFlavorNodeLabelsPreservesUsage(t *testing.T) {
	tasCache := NewTASCache(nil, newDefaultSimulator(), resources.NewResourceFormatter())
	topology := utiltestingapi.MakeDefaultOneLevelTopology("default")
	tasCache.AddTopology(topology)

	flavor := utiltestingapi.MakeResourceFlavor("tas-flavor").
		NodeLabel("node-group", "tas").
		TopologyName(topology.Name).
		Obj()
	tasCache.AddOrUpdateFlavor(flavor)

	const wlKey = workload.Reference("default/wl")
	topologyRequests := []workload.TopologyDomainRequests{{
		Values: []string{"x1"},
		Count:  1,
		SinglePodRequests: resources.NewRequestsFromMap(map[corev1.ResourceName]int64{
			corev1.ResourceCPU: 1,
		}),
	}}
	originalFlavorCache := tasCache.Get("tas-flavor")
	originalFlavorCache.addUsage(logr.Discard(), wlKey, topologyRequests)

	updatedNodeLabels := map[string]string{"node-group": "other"}
	flavor.Spec.NodeLabels = updatedNodeLabels
	tasCache.AddOrUpdateFlavor(flavor)

	updatedFlavorCache := tasCache.Get("tas-flavor")
	if updatedFlavorCache != originalFlavorCache {
		t.Fatal("TAS flavor cache was replaced while updating nodeLabels")
	}

	wantUsage := map[utiltas.TopologyDomainID]resources.Requests{
		"x1": resources.NewRequestsFromMap(map[corev1.ResourceName]int64{
			corev1.ResourceCPU:  1,
			corev1.ResourcePods: 1,
		}),
	}
	if diff := cmp.Diff(updatedNodeLabels, updatedFlavorCache.flavor.NodeLabels); diff != "" {
		t.Errorf("Unexpected nodeLabels after update (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(wantUsage, updatedFlavorCache.usage, cmp.Comparer(resources.Equal)); diff != "" {
		t.Errorf("Unexpected usage after updating nodeLabels (-want +got):\n%s", diff)
	}
	if _, found := updatedFlavorCache.wlUsage[wlKey]; !found {
		t.Error("Workload usage was removed after updating nodeLabels")
	}
}

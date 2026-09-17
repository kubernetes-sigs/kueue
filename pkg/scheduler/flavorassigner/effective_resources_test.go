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

package flavorassigner

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestTASRequestCarriesEffectivePodTemplate(t *testing.T) {
	_, log := utiltesting.ContextWithLog(t)
	wl := utiltestingapi.MakeWorkload("wl", "ns").Limit(corev1.ResourceCPU, "2").Obj()
	info := workload.NewInfo(log, wl)
	cq := &schdcache.ClusterQueueSnapshot{TASFlavors: map[kueue.ResourceFlavorReference]*schdcache.TASFlavorSnapshot{"tas": {}}}
	assignment := &PodSetAssignment{Count: 1, Flavors: ResourceAssignment{corev1.ResourceCPU: {Name: "tas"}}}
	req, err := podSetTopologyRequest(assignment, info, cq, false, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := resources.NewRequestsFromPodSpec(&req.PodSet.Template.Spec).ResourceValue(corev1.ResourceCPU); got != 2000 {
		t.Errorf("simulator template CPU = %d, want 2000", got)
	}
	if got := req.SinglePodRequests.ResourceValue(corev1.ResourceCPU); got != 2000 {
		t.Errorf("TAS placement CPU = %d, want 2000", got)
	}
	if len(wl.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Requests) != 0 {
		t.Fatal("effective simulation template overwrote raw Workload")
	}
}

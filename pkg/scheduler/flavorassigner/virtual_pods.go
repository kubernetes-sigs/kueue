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
	"fmt"

	corev1 "k8s.io/api/core/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/was"
	"sigs.k8s.io/kueue/pkg/util/podset"
	"sigs.k8s.io/kueue/pkg/workload"
)

// CandidateVirtualPods builds candidate virtual pods for all PodSets in the assignment
// using the assigned flavor's node labels, tolerations, and admission check updates.
func (a *Assignment) CandidateVirtualPods(wl *workload.Info, cq *schdcache.ClusterQueueSnapshot) ([]*corev1.Pod, error) {
	var allPods []*corev1.Pod
	for _, psAssignment := range a.PodSets {
		if psAssignment.Status.IsError() {
			return nil, fmt.Errorf("podset %q is failing: %w", psAssignment.Name, psAssignment.Status.err)
		}
		podSet := podset.FindPodSetByName(wl.Obj.Spec.PodSets, psAssignment.Name)
		if podSet == nil {
			return nil, fmt.Errorf("podSet %q not found in workload %s", psAssignment.Name, wl.Obj.Name)
		}

		tasFlavor, err := onlyTASFlavor(psAssignment.Flavors, cq.TASFlavors)
		if err != nil {
			return nil, fmt.Errorf("failed to get TAS flavor for PodSet %q: %w", psAssignment.Name, err)
		}
		flavorSnapshot := cq.TASFlavors[*tasFlavor]
		if flavorSnapshot == nil {
			return nil, fmt.Errorf("TAS flavor snapshot for flavor %q not found in ClusterQueue %s", *tasFlavor, cq.Name)
		}
		flavorNodeLabels := flavorSnapshot.NodeLabels()
		flavorTolerations := flavorSnapshot.Tolerations()

		// Gather ready PodSetUpdates from admission checks
		var podSetUpdates []kueue.PodSetUpdate
		for _, ac := range wl.Obj.Status.AdmissionChecks {
			if ac.State != kueue.CheckStateReady {
				continue
			}
			for _, u := range ac.PodSetUpdates {
				if u.Name == podSet.Name {
					podSetUpdates = append(podSetUpdates, u)
				}
			}
		}

		opts := was.CandidatePodOptions{
			PodSpec:           wl.PodSpecByName(psAssignment.Name),
			FlavorNodeLabels:  flavorNodeLabels,
			FlavorTolerations: flavorTolerations,
			PodSetUpdates:     podSetUpdates,
		}

		pods, err := was.CandidateVirtualPodsForPodSet(wl.Obj, podSet, psAssignment.Count, opts)
		if err != nil {
			return nil, fmt.Errorf("failed to build candidate pods for PodSet %q: %w", podSet.Name, err)
		}
		allPods = append(allPods, pods...)
	}
	return allPods, nil
}

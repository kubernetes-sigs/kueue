//go:build !exclude_scheduler_library

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

package was

import (
	"iter"
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	schedlib "sigs.k8s.io/scheduler-library/pkg/upstreamsync/snapshot"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	"sigs.k8s.io/kueue/pkg/workload"
)

type preemptionVictim struct {
	wlKey      workload.Reference
	pods       []*corev1.Pod
	victimType victimType
}

type victimType int

const (
	candidateWorkload victimType = iota
	preemptedWorkload
	strayPod
)

var _ schedlib.PreemptionVictim = (*preemptionVictim)(nil)

func (v *preemptionVictim) Pods() []*corev1.Pod {
	return v.pods
}

func (s *wasSimulator) potentialVictims(candidates iter.Seq[workload.Reference]) iter.Seq[schedlib.PreemptionVictim] {
	if candidates == nil {
		return nil
	}
	return func(yield func(schedlib.PreemptionVictim) bool) {
		for wlKey := range candidates {
			// Pods with DeletionTimestamp count as individual
			// committed victims and not actual pods of the workload.
			// If all pods of the workload are filtered out,
			// it means it is a podless victim with only residual pods remaining.
			pods := filterActivePods(s.pods.PodsForWorkload(wlKey))
			// TODO: Handle podless victims (e.g. workloads waiting on AdmissionChecks that only hold quota).
			if len(pods) == 0 {
				continue
			}
			v := &preemptionVictim{
				wlKey: wlKey,
				pods:  pods,
			}
			if !yield(v) {
				return
			}
		}
	}
}

func (s *wasSimulator) committedVictims(victimWorkloads []workload.Reference) []schedlib.PreemptionVictim {
	if len(victimWorkloads) == 0 {
		return nil
	}
	cv := make([]schedlib.PreemptionVictim, 0, len(victimWorkloads))
	recordedVictims := sets.New[workload.Reference]()
	// Process victim Workloads
	for _, preemptedWlKey := range victimWorkloads {
		if recordedVictims.Has(preemptedWlKey) {
			continue
		}
		recordedVictims.Insert(preemptedWlKey)
		// TODO: Handle podless committed victims.
		if victimPods := s.pods.PodsForWorkload(preemptedWlKey); len(victimPods) > 0 {
			cv = append(cv, &preemptionVictim{
				wlKey:      preemptedWlKey,
				pods:       victimPods,
				victimType: preemptedWorkload,
			})
		}
	}
	// Process victim Pods not belonging to a victim Workload
	for _, pod := range s.pods.Pods() {
		if pod.DeletionTimestamp.IsZero() {
			continue
		}
		if wlName := pod.Annotations[kueue.WorkloadAnnotation]; wlName != "" && recordedVictims.Has(workload.NewReference(pod.Namespace, wlName)) {
			// victim Workloads are already accounted for in their entirety
			continue
		}
		cv = append(cv, &preemptionVictim{
			pods:       []*corev1.Pod{pod},
			victimType: strayPod,
		})
	}
	return cv
}

func toPreemptionVictims(victims []schedlib.PreemptionVictim) []workload.Reference {
	if len(victims) == 0 {
		return nil
	}
	preemptionVictims := make([]workload.Reference, 0, len(victims))
	for _, victim := range victims {
		if wlVictim, ok := victim.(*preemptionVictim); ok && wlVictim.victimType == candidateWorkload {
			preemptionVictims = append(preemptionVictims, wlVictim.wlKey)
		}
	}
	return preemptionVictims
}

type preemptionFilterAdapter struct {
	simulator.PreemptionFilter
}

var _ schedlib.PreemptionFilter = (*preemptionFilterAdapter)(nil)

func (a *preemptionFilterAdapter) PreemptVictim(victim schedlib.PreemptionVictim) {
	if wlVictim, ok := victim.(*preemptionVictim); ok && wlVictim.victimType != strayPod {
		a.PreemptWorkload(wlVictim.wlKey)
	}
}

func (a *preemptionFilterAdapter) UnpreemptVictim(victim schedlib.PreemptionVictim) {
	if wlVictim, ok := victim.(*preemptionVictim); ok && wlVictim.victimType != strayPod {
		a.UnpreemptWorkload(wlVictim.wlKey)
	}
}

func filterActivePods(pods []*corev1.Pod) []*corev1.Pod {
	return slices.DeleteFunc(pods, func(pod *corev1.Pod) bool {
		return !pod.DeletionTimestamp.IsZero()
	})
}

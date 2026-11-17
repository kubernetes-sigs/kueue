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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	schedlib "sigs.k8s.io/scheduler-library/pkg/upstreamsync/snapshot"

	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	"sigs.k8s.io/kueue/pkg/workload"
)

// preemptionVictim is a victim as scheduler-library sees it: Pods only. On its
// own, it is made of terminating Pods, which hold no quota, so the
// PreemptionFilter does not see it.
type preemptionVictim struct {
	pods []*corev1.Pod
}

var _ schedlib.PreemptionVictim = (*preemptionVictim)(nil)

func (v *preemptionVictim) Pods() []*corev1.Pod {
	return v.pods
}

// workloadVictim is the victim of a Workload, which holds quota.
type workloadVictim struct {
	preemptionVictim
	wlKey workload.Reference
}

var _ schedlib.PreemptionVictim = (*workloadVictim)(nil)

func (s *wasSimulator) potentialVictims(candidates iter.Seq[workload.Reference], committedVictims sets.Set[workload.Reference]) iter.Seq[schedlib.PreemptionVictim] {
	if candidates == nil {
		return nil
	}
	return func(yield func(schedlib.PreemptionVictim) bool) {
		for wlKey := range candidates {
			if committedVictims.Has(wlKey) {
				continue
			}
			v := &workloadVictim{
				pods:  s.pods.PodsForWorkload(wlKey),
				wlKey: wlKey,
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
	present := sets.New[workload.Reference]()
	for _, preemptedWlKey := range victimWorkloads {
		if present.Has(preemptedWlKey) {
			continue
		}
		present.Insert(preemptedWlKey)
		cv = append(cv, &workloadVictim{
			pods:  s.pods.PodsForWorkload(preemptedWlKey),
			wlKey: preemptedWlKey,
		})
	}
	return cv
}

// committedPodVictims returns the committed victim made of the given Pods,
// skipping those no longer tracked or replaced by a Pod of the same name.
func (s *wasSimulator) committedPodVictims(refs []simulator.PodRef) []schedlib.PreemptionVictim {
	var pods []*corev1.Pod
	for _, ref := range refs {
		if pod := s.pods.Pod(ref.Key); pod != nil && pod.UID == ref.UID {
			pods = append(pods, pod)
		}
	}
	if len(pods) == 0 {
		return nil
	}
	return []schedlib.PreemptionVictim{&preemptionVictim{pods: pods}}
}

func toPreemptionVictims(victims []schedlib.PreemptionVictim) []workload.Reference {
	if len(victims) == 0 {
		return nil
	}
	preemptionVictims := make([]workload.Reference, 0, len(victims))
	for _, victim := range victims {
		if wlVictim, ok := victim.(*workloadVictim); ok {
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
	if wlVictim, ok := victim.(*workloadVictim); ok {
		a.PreemptWorkload(wlVictim.wlKey)
	}
}

func (a *preemptionFilterAdapter) UnpreemptVictim(victim schedlib.PreemptionVictim) {
	if wlVictim, ok := victim.(*workloadVictim); ok {
		a.UnpreemptWorkload(wlVictim.wlKey)
	}
}

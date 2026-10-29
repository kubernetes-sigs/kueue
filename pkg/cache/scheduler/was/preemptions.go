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
	"sigs.k8s.io/controller-runtime/pkg/client"
	schedlib "sigs.k8s.io/scheduler-library/pkg/upstreamsync/snapshot"

	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
)

type preemptionVictim struct {
	wlKey client.ObjectKey
	pods  []*corev1.Pod
}

var _ schedlib.PreemptionVictim = (*preemptionVictim)(nil)

func (v *preemptionVictim) Pods() []*corev1.Pod {
	return v.pods
}

func (s *wasSimulator) potentialVictims(candidates iter.Seq[client.ObjectKey]) iter.Seq[schedlib.PreemptionVictim] {
	if candidates == nil {
		return nil
	}
	return func(yield func(schedlib.PreemptionVictim) bool) {
		for wlKey := range candidates {
			// TODO: Handle podless victims (e.g. workloads waiting on AdmissionChecks that only hold quota).
			pods := s.pods.PodsForWorkload(wlKey)
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

func toPreemptionVictims(victims []schedlib.PreemptionVictim) []client.ObjectKey {
	if len(victims) == 0 {
		return nil
	}
	preemptionVictims := make([]client.ObjectKey, 0, len(victims))
	for _, victim := range victims {
		if wlVictim, ok := victim.(*preemptionVictim); ok {
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
	if wlVictim, ok := victim.(*preemptionVictim); ok {
		a.PreemptWorkload(wlVictim.wlKey)
	}
}

func (a *preemptionFilterAdapter) UnpreemptVictim(victim schedlib.PreemptionVictim) {
	if wlVictim, ok := victim.(*preemptionVictim); ok {
		a.UnpreemptWorkload(wlVictim.wlKey)
	}
}

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
	"errors"
	"iter"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
	schedlib "sigs.k8s.io/scheduler-library/pkg/upstreamsync/snapshot"

	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
)

type preemptionVictim struct {
	wlKey client.ObjectKey
	pods  []*corev1.Pod
	// TODO(#18163):  after https://github.com/kubernetes-sigs/scheduler-library/pull/70
	// remove the field, as committed victims will no longer be returned as output.
	committedVictim bool
}

var _ schedlib.PreemptionVictim = (*preemptionVictim)(nil)

func (v *preemptionVictim) Pods() []*corev1.Pod {
	return v.pods
}

func (s *wasSimulator) potentialVictims(candidates iter.Seq[client.ObjectKey], committedVictims sets.Set[client.ObjectKey]) iter.Seq[schedlib.PreemptionVictim] {
	if candidates == nil {
		return nil
	}
	return func(yield func(schedlib.PreemptionVictim) bool) {
		for wlKey := range candidates {
			if committedVictims.Has(wlKey) {
				continue
			}
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

func (s *wasSimulator) committedVictims(victimWorkloads []client.ObjectKey) []schedlib.PreemptionVictim {
	victimCount := len(victimWorkloads) + len(s.pastCyclesCommittedVictims)
	if victimCount == 0 {
		return nil
	}
	allVictims := make(map[client.ObjectKey][]*corev1.Pod, victimCount)
	for _, preemptedWlKey := range victimWorkloads {
		if _, alreadyProcessed := allVictims[preemptedWlKey]; alreadyProcessed {
			continue
		}
		if victimPods := s.pods.PodsForWorkload(preemptedWlKey); len(victimPods) > 0 {
			allVictims[preemptedWlKey] = victimPods
		}
	}
	for preemptedWlKey, victimPods := range s.pastCyclesCommittedVictims {
		// pastCyclesCommittedVictims are absent from the tracker.
		// We can safely append the pods listed here under the workload, if the workload is already present.
		if len(victimPods) > 0 {
			allVictims[preemptedWlKey] = append(allVictims[preemptedWlKey], victimPods...)
		}
	}

	cv := make([]schedlib.PreemptionVictim, 0, len(allVictims))
	for wl, pods := range allVictims {
		cv = append(cv, &preemptionVictim{
			wlKey:           wl,
			pods:            pods,
			committedVictim: true,
		})
	}
	return cv
}

func (s *wasSimulator) toPreemptionVictims(victims []schedlib.PreemptionVictim) ([]client.ObjectKey, error) {
	if len(victims) == 0 {
		return nil, nil
	}
	var preemptionVictims []client.ObjectKey
	for _, victim := range victims {
		if wlVictim, ok := victim.(*preemptionVictim); !ok {
			return nil, errors.New("scheduler-library returned illegal preemption victim type")
		} else if !wlVictim.committedVictim {
			preemptionVictims = append(preemptionVictims, wlVictim.wlKey)
		}
	}
	return preemptionVictims, nil
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

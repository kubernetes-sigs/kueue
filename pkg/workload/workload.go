/*
Copyright 2021 Google LLC.

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

package workload

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"

	kueue "gke-internal.googlesource.com/gke-batch/kueue/api/v1alpha1"
)

// Info holds a QueuedWorkload object and some pre-processing.
type Info struct {
	Obj *kueue.QueuedWorkload
	// maps PodSet name to total resources requested by the set.
	TotalRequests map[string]Resources
	// Populated from queue.
	Capacity string
}

type Resources struct {
	Requests Requests
	Types    map[corev1.ResourceName]string
}

func NewInfo(w *kueue.QueuedWorkload) Info {
	return Info{
		Obj:           w,
		TotalRequests: totalRequests(w.Spec.Pods),
	}
}

func Key(w *kueue.QueuedWorkload) string {
	return fmt.Sprintf("%s/%s", w.Namespace, w.Name)
}

func totalRequests(podSets []kueue.PodSet) map[string]Resources {
	if len(podSets) == 0 {
		return nil
	}
	res := make(map[string]Resources)
	for _, ps := range podSets {
		setRes := Resources{}
		setRes.Requests = podRequests(&ps.Spec)
		setRes.Requests.scale(int64(ps.Count))
		if ps.AssignedTypes != nil {
			setRes.Types = map[corev1.ResourceName]string{}
			for r, t := range ps.AssignedTypes {
				setRes.Types[r] = t
			}
		}
		res[ps.Name] = setRes
	}
	return res
}

// The following resources calculations are inspired on
// https://github.com/kubernetes/kubernetes/blob/master/pkg/scheduler/framework/types.go

// Requests maps ResourceName to type to value; for CPU it is tracked in MilliCPU.
type Requests map[corev1.ResourceName]int64

func podRequests(spec *corev1.PodSpec) Requests {
	res := Requests{}
	for _, c := range spec.Containers {
		res.add(newRequests(c.Resources.Requests))
	}
	for _, c := range spec.InitContainers {
		res.setMax(newRequests(c.Resources.Requests))
	}
	res.add(newRequests(spec.Overhead))
	return res
}

func newRequests(rl corev1.ResourceList) Requests {
	r := Requests{}
	for name, quant := range rl {
		switch name {
		case corev1.ResourceCPU:
			r[name] = quant.MilliValue()
		default:
			r[name] = quant.Value()
		}
	}
	return r
}

func (r Requests) add(o Requests) {
	for name, val := range o {
		r[name] += val
	}
}

func (r Requests) setMax(o Requests) {
	for name, val := range o {
		r[name] = max(r[name], val)
	}
}

func (r Requests) scale(f int64) {
	for name := range r {
		r[name] *= f
	}
}

func max(v1, v2 int64) int64 {
	if v1 > v2 {
		return v1
	}
	return v2
}

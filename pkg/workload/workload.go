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
	Obj           *kueue.QueuedWorkload
	TotalRequests Resources
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

func totalRequests(podSets []kueue.PodSet) Resources {
	var res Resources
	for _, ps := range podSets {
		setRes := PodResources(&ps.Spec)
		setRes.Scale(int64(ps.Count))
		res.Add(setRes)
	}
	return res
}

// The following resources calculations are inspired on
// https://github.com/kubernetes/kubernetes/blob/master/pkg/scheduler/framework/types.go

type Resources struct {
	MilliCPU         int64
	Memory           int64
	EphemeralStorage int64
	Scalar           map[corev1.ResourceName]int64
}

func PodResources(spec *corev1.PodSpec) Resources {
	var res Resources
	for _, c := range spec.Containers {
		res.Add(NewResources(c.Resources.Requests))
	}
	for _, c := range spec.InitContainers {
		res.SetMax(NewResources(c.Resources.Requests))
	}
	res.Add(NewResources(spec.Overhead))
	return res
}

func NewResources(rl corev1.ResourceList) Resources {
	var r Resources
	for name, quant := range rl {
		switch name {
		case corev1.ResourceCPU:
			r.MilliCPU = quant.MilliValue()
		case corev1.ResourceMemory:
			r.Memory = quant.Value()
		case corev1.ResourceEphemeralStorage:
			r.EphemeralStorage = quant.Value()
		default:
			if r.Scalar == nil {
				r.Scalar = make(map[corev1.ResourceName]int64, 1)
			}
			r.Scalar[name] = quant.Value()
		}
	}
	return r
}

func (r *Resources) Add(o Resources) {
	r.MilliCPU += o.MilliCPU
	r.Memory += o.Memory
	r.EphemeralStorage += o.EphemeralStorage
	if r.Scalar == nil && o.Scalar != nil {
		r.Scalar = make(map[corev1.ResourceName]int64, len(o.Scalar))
	}
	for name, val := range o.Scalar {
		r.Scalar[name] += val
	}
}

func (r *Resources) SetMax(o Resources) {
	r.MilliCPU = max(r.MilliCPU, o.MilliCPU)
	r.Memory = max(r.Memory, o.Memory)
	r.EphemeralStorage = max(r.EphemeralStorage, o.EphemeralStorage)
	if r.Scalar == nil && o.Scalar != nil {
		r.Scalar = make(map[corev1.ResourceName]int64, len(o.Scalar))
	}
	for name, val := range o.Scalar {
		r.Scalar[name] = max(r.Scalar[name], val)
	}
}

func (r *Resources) Scale(f int64) {
	r.MilliCPU *= f
	r.Memory *= f
	r.EphemeralStorage *= f
	for name := range r.Scalar {
		r.Scalar[name] *= f
	}
}

func max(v1, v2 int64) int64 {
	if v1 > v2 {
		return v1
	}
	return v2
}

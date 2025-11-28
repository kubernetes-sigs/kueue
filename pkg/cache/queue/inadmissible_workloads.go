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

package queue

import (
	"sigs.k8s.io/kueue/pkg/workload"
)

// inadmissibleWorkloads is a thin wrapper around a map to encapsulate
// operations on inadmissible workloads and prevent direct map access.
type inadmissibleWorkloads map[workload.Reference]*workload.Info

// get retrieves a workload from the inadmissible workloads map.
// Returns the workload if it exists, otherwise returns nil.
func (iw inadmissibleWorkloads) get(key workload.Reference) *workload.Info {
	return iw[key]
}

// delete removes a workload from the inadmissible workloads map.
func (iw inadmissibleWorkloads) delete(key workload.Reference) {
	delete(iw, key)
}

// insert adds a workload to the inadmissible workloads map.
func (iw inadmissibleWorkloads) insert(key workload.Reference, wInfo *workload.Info) {
	iw[key] = wInfo
}

// len returns the number of inadmissible workloads.
func (iw inadmissibleWorkloads) len() int {
	return len(iw)
}

// empty returns true if there are no inadmissible workloads.
func (iw inadmissibleWorkloads) empty() bool {
	return len(iw) == 0
}

// forEach iterates over all inadmissible workloads and calls the provided function.
// The iteration can be stopped early by returning false from the function.
func (iw inadmissibleWorkloads) forEach(f func(key workload.Reference, wInfo *workload.Info) bool) {
	for key, wInfo := range iw {
		if !f(key, wInfo) {
			return
		}
	}
}

// replaceAll replaces all inadmissible workloads with the provided map.
func (iw *inadmissibleWorkloads) replaceAll(newMap map[workload.Reference]*workload.Info) {
	*iw = newMap
}

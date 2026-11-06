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
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/workload"
)

// sliceChainKey identifies a chain of workload slices: the slices of a job share its namespace,
// its controller and their slice name.
type sliceChainKey struct {
	namespace  string
	controller types.UID
	sliceName  string
}

// sliceChainOf returns the chain of workload slice wi. It returns false for Workloads that are not
// slices, and for TAS slices, whose Pods a newer slice may not cover on the same nodes.
func sliceChainOf(wi *workload.Info) (sliceChainKey, bool) {
	owner := metav1.GetControllerOfNoCopy(wi.Obj)
	name := wi.Obj.Annotations[kueue.WorkloadSliceNameAnnotation]
	if !features.Enabled(features.ElasticJobsViaWorkloadSlices) || owner == nil || name == "" || wi.IsUsingTAS() {
		return sliceChainKey{}, false
	}
	return sliceChainKey{namespace: wi.Obj.Namespace, controller: owner.UID, sliceName: name}, true
}

// covers reports whether slice a covers slice b: it charges at least b's usage, and either more of
// some resource or the same and was created after b. So the larger of two slices is charged, also
// when a replacement shrinks. Slices that each charge more of some resource, and slices with the
// same usage created in the same second, stay charged.
func covers(a, b *workload.Info) bool {
	ua, ub := a.ResourceUsage().Assigned, b.ResourceUsage().Assigned
	for fr, q := range ub {
		if ua[fr].Cmp(q) < 0 {
			return false
		}
	}
	for fr, q := range ua {
		if q.Cmp(ub[fr]) > 0 {
			return true
		}
	}
	return a.Obj.CreationTimestamp.After(b.Obj.CreationTimestamp.Time)
}

// workloadSliceTracker tracks the workload slices of a ClusterQueue by chain. A slice that another
// slice of its chain covers is not charged: the covering slice's usage covers its Pods.
type workloadSliceTracker struct {
	// charged holds the slices of each chain that are in the ClusterQueue's Workloads.
	charged map[sliceChainKey]sets.Set[workload.Reference]
	// covered holds the slices that are left out of the ClusterQueue's Workloads.
	covered map[workload.Reference]*workload.Info
}

func newWorkloadSliceTracker() workloadSliceTracker {
	return workloadSliceTracker{
		charged: make(map[sliceChainKey]sets.Set[workload.Reference]),
		covered: make(map[workload.Reference]*workload.Info),
	}
}

func (t *workloadSliceTracker) charge(chain sliceChainKey, k workload.Reference) {
	if t.charged[chain] == nil {
		t.charged[chain] = sets.New[workload.Reference]()
	}
	t.charged[chain].Insert(k)
}

func (t *workloadSliceTracker) uncharge(chain sliceChainKey, k workload.Reference) {
	t.charged[chain].Delete(k)
	if t.charged[chain].Len() == 0 {
		delete(t.charged, chain)
	}
}

func (t *workloadSliceTracker) cover(k workload.Reference, wi *workload.Info) {
	t.covered[k] = wi
}

// forget stops tracking k as a covered slice.
func (t *workloadSliceTracker) forget(k workload.Reference) {
	delete(t.covered, k)
}

func (t *workloadSliceTracker) coveredSlice(k workload.Reference) (*workload.Info, bool) {
	wi, found := t.covered[k]
	return wi, found
}

func (t *workloadSliceTracker) coveredSlicesOf(chain sliceChainKey) []*workload.Info {
	var out []*workload.Info
	for _, wi := range t.covered {
		if c, ok := sliceChainOf(wi); ok && c == chain {
			out = append(out, wi)
		}
	}
	return slices.Clip(out)
}

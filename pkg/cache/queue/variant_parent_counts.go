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
	"sync"

	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workload/concurrentadmission"
)

// variantParentCounts keeps incremental per-status counts of the pending
// ConcurrentAdmission Variants of each Parent. The Parent is not queued, while
// each of its Variants is, so the pending counts subtract the Variants and add
// their Parents back once.
//
// Like localQueueCounts, the struct carries its own lock, and mutations are
// additionally serialized by their callers holding ClusterQueue.rwm for writing.
type variantParentCounts struct {
	rwm sync.RWMutex
	// active counts the Variants in the heap, per Parent.
	active map[workload.Reference]int
	// inadmissible counts the Variants in inadmissibleWorkloads, per Parent.
	inadmissible map[workload.Reference]int
	// inflight counts the inflight Variant of the ClusterQueue, per Parent.
	inflight map[workload.Reference]int
}

func newVariantParentCounts() *variantParentCounts {
	return &variantParentCounts{
		active:       make(map[workload.Reference]int),
		inadmissible: make(map[workload.Reference]int),
		inflight:     make(map[workload.Reference]int),
	}
}

// variantParentKey returns the key of wInfo's Parent, or an empty key when
// wInfo is not a ConcurrentAdmission Variant. A Variant is in the namespace of
// its Parent.
func variantParentKey(wInfo *workload.Info) workload.Reference {
	if !features.Enabled(features.ConcurrentAdmission) {
		return ""
	}
	name := concurrentadmission.GetParentWorkloadName(wInfo.Obj)
	if name == "" {
		return ""
	}
	return workload.NewReference(wInfo.Obj.Namespace, name)
}

func addVariant(counts map[workload.Reference]int, wInfo *workload.Info) {
	if key := variantParentKey(wInfo); key != "" {
		counts[key]++
	}
}

func removeVariant(counts map[workload.Reference]int, wInfo *workload.Info) {
	key := variantParentKey(wInfo)
	if key == "" {
		return
	}
	counts[key]--
	if counts[key] <= 0 {
		delete(counts, key)
	}
}

func (s *variantParentCounts) addActive(wInfo *workload.Info) {
	s.rwm.Lock()
	defer s.rwm.Unlock()
	addVariant(s.active, wInfo)
}

func (s *variantParentCounts) removeActive(wInfo *workload.Info) {
	s.rwm.Lock()
	defer s.rwm.Unlock()
	removeVariant(s.active, wInfo)
}

func (s *variantParentCounts) addInadmissible(wInfo *workload.Info) {
	s.rwm.Lock()
	defer s.rwm.Unlock()
	addVariant(s.inadmissible, wInfo)
}

func (s *variantParentCounts) removeInadmissible(wInfo *workload.Info) {
	s.rwm.Lock()
	defer s.rwm.Unlock()
	removeVariant(s.inadmissible, wInfo)
}

func (s *variantParentCounts) updateActive(oldInfo, newInfo *workload.Info) {
	s.rwm.Lock()
	defer s.rwm.Unlock()
	if oldInfo != nil {
		removeVariant(s.active, oldInfo)
	}
	addVariant(s.active, newInfo)
}

func (s *variantParentCounts) updateInadmissible(oldInfo, newInfo *workload.Info) {
	s.rwm.Lock()
	defer s.rwm.Unlock()
	removeVariant(s.inadmissible, oldInfo)
	addVariant(s.inadmissible, newInfo)
}

func (s *variantParentCounts) moveToActive(wInfo *workload.Info) {
	s.rwm.Lock()
	defer s.rwm.Unlock()
	removeVariant(s.inadmissible, wInfo)
	addVariant(s.active, wInfo)
}

func (s *variantParentCounts) moveToInadmissible(wInfo *workload.Info) {
	s.rwm.Lock()
	defer s.rwm.Unlock()
	removeVariant(s.active, wInfo)
	addVariant(s.inadmissible, wInfo)
}

func (s *variantParentCounts) moveActiveToInflight(wInfo *workload.Info) {
	s.rwm.Lock()
	defer s.rwm.Unlock()
	removeVariant(s.active, wInfo)
	addVariant(s.inflight, wInfo)
}

func (s *variantParentCounts) removeInflight(wInfo *workload.Info) {
	s.rwm.Lock()
	defer s.rwm.Unlock()
	removeVariant(s.inflight, wInfo)
}

// excess returns how many active and inadmissible pending workloads are
// Variants beyond the first one of each Parent. A Parent counts as active when
// any of its Variants is active or inflight, and as inadmissible otherwise.
func (s *variantParentCounts) excess() (int, int) {
	s.rwm.RLock()
	defer s.rwm.RUnlock()
	return variantExcess(s.active, s.inadmissible, s.inflight)
}

// variantExcess returns how many Variants in the active, inflight and
// inadmissible per-Parent counts are beyond the first one of each Parent. A
// Parent counts as active when any of its Variants is active or inflight, and
// as inadmissible otherwise. A nil map counts as empty.
func variantExcess(active, inadmissible, inflight map[workload.Reference]int) (int, int) {
	var activeVariants, inadmissibleVariants int
	activeParents := len(active)
	for _, n := range active {
		activeVariants += n
	}
	for key, n := range inflight {
		activeVariants += n
		if active[key] == 0 {
			activeParents++
		}
	}
	inadmissibleParents := 0
	for key, n := range inadmissible {
		inadmissibleVariants += n
		if active[key] == 0 && inflight[key] == 0 {
			inadmissibleParents++
		}
	}
	return activeVariants - activeParents, inadmissibleVariants - inadmissibleParents
}

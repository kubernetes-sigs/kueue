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

	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workload/concurrentadmission"
)

// variantParentCounts keeps incremental per-status counts of the pending
// ConcurrentAdmission Variants of each Parent. The Parent is not queued, while
// each of its Variants is, so the pending counts subtract the Variants and add
// their Parents back once.
//
// Like schedulingHashCounts, the struct carries its own lock, and mutations are
// additionally serialized by their callers holding ClusterQueue.rwm for writing.
type variantParentCounts struct {
	rwm sync.RWMutex
	// active counts the Variants in the heap, per Parent UID.
	active map[types.UID]int
	// inadmissible counts the Variants in inadmissibleWorkloads, per Parent UID.
	inadmissible map[types.UID]int
	// inflight counts the Variants in PendingWorkloads.inflight, per Parent UID.
	inflight map[types.UID]int
}

func newVariantParentCounts() *variantParentCounts {
	return &variantParentCounts{
		active:       make(map[types.UID]int),
		inadmissible: make(map[types.UID]int),
		inflight:     make(map[types.UID]int),
	}
}

// variantParentUID returns the UID of wInfo's Parent, or an empty UID when
// wInfo is not a ConcurrentAdmission Variant.
func variantParentUID(wInfo *workload.Info) types.UID {
	if !features.Enabled(features.ConcurrentAdmission) {
		return ""
	}
	return concurrentadmission.GetParentWorkloadUID(wInfo.Obj)
}

func addVariant(counts map[types.UID]int, wInfo *workload.Info) {
	if uid := variantParentUID(wInfo); uid != "" {
		counts[uid]++
	}
}

func removeVariant(counts map[types.UID]int, wInfo *workload.Info) {
	uid := variantParentUID(wInfo)
	if uid == "" {
		return
	}
	counts[uid]--
	if counts[uid] <= 0 {
		delete(counts, uid)
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
	var activeVariants, inadmissibleVariants int
	activeParents := len(s.active)
	for _, n := range s.active {
		activeVariants += n
	}
	for uid, n := range s.inflight {
		activeVariants += n
		if s.active[uid] == 0 {
			activeParents++
		}
	}
	inadmissibleParents := 0
	for uid, n := range s.inadmissible {
		inadmissibleVariants += n
		if s.active[uid] == 0 && s.inflight[uid] == 0 {
			inadmissibleParents++
		}
	}
	return activeVariants - activeParents, inadmissibleVariants - inadmissibleParents
}

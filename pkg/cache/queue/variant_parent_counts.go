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

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
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
	// active counts the Variants in the heap, per Parent.
	active map[workload.Reference]int
	// inadmissible counts the Variants in inadmissibleWorkloads, per Parent.
	inadmissible map[workload.Reference]int
	// inflight counts the Variants in PendingWorkloads.inflight, per Parent.
	inflight map[workload.Reference]int

	// customLabels configures the per-label counts below.
	customLabels *metrics.CustomLabels
	// activeLabels, inadmissibleLabels and inflightLabels count the same Variants as active, inadmissible and inflight, per Parent and Workload custom label values.
	// They stay empty unless Workload custom labels are configured.
	activeLabels       variantLabelCounts
	inadmissibleLabels variantLabelCounts
	inflightLabels     variantLabelCounts
}

func newVariantParentCounts(cl *metrics.CustomLabels) *variantParentCounts {
	return &variantParentCounts{
		active:       make(map[workload.Reference]int),
		inadmissible: make(map[workload.Reference]int),
		inflight:     make(map[workload.Reference]int),

		customLabels:       cl,
		activeLabels:       make(variantLabelCounts),
		inadmissibleLabels: make(variantLabelCounts),
		inflightLabels:     make(variantLabelCounts),
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
	s.activeLabels.add(s.customLabels, wInfo)
}

func (s *variantParentCounts) removeActive(wInfo *workload.Info) {
	s.rwm.Lock()
	defer s.rwm.Unlock()
	removeVariant(s.active, wInfo)
	s.activeLabels.remove(s.customLabels, wInfo)
}

func (s *variantParentCounts) addInadmissible(wInfo *workload.Info) {
	s.rwm.Lock()
	defer s.rwm.Unlock()
	addVariant(s.inadmissible, wInfo)
	s.inadmissibleLabels.add(s.customLabels, wInfo)
}

func (s *variantParentCounts) removeInadmissible(wInfo *workload.Info) {
	s.rwm.Lock()
	defer s.rwm.Unlock()
	removeVariant(s.inadmissible, wInfo)
	s.inadmissibleLabels.remove(s.customLabels, wInfo)
}

func (s *variantParentCounts) updateActive(oldInfo, newInfo *workload.Info) {
	s.rwm.Lock()
	defer s.rwm.Unlock()
	if oldInfo != nil {
		removeVariant(s.active, oldInfo)
		s.activeLabels.remove(s.customLabels, oldInfo)
	}
	addVariant(s.active, newInfo)
	s.activeLabels.add(s.customLabels, newInfo)
}

func (s *variantParentCounts) updateInadmissible(oldInfo, newInfo *workload.Info) {
	s.rwm.Lock()
	defer s.rwm.Unlock()
	removeVariant(s.inadmissible, oldInfo)
	s.inadmissibleLabels.remove(s.customLabels, oldInfo)
	addVariant(s.inadmissible, newInfo)
	s.inadmissibleLabels.add(s.customLabels, newInfo)
}

func (s *variantParentCounts) moveToActive(wInfo *workload.Info) {
	s.rwm.Lock()
	defer s.rwm.Unlock()
	removeVariant(s.inadmissible, wInfo)
	s.inadmissibleLabels.remove(s.customLabels, wInfo)
	addVariant(s.active, wInfo)
	s.activeLabels.add(s.customLabels, wInfo)
}

func (s *variantParentCounts) moveToInadmissible(wInfo *workload.Info) {
	s.rwm.Lock()
	defer s.rwm.Unlock()
	removeVariant(s.active, wInfo)
	s.activeLabels.remove(s.customLabels, wInfo)
	addVariant(s.inadmissible, wInfo)
	s.inadmissibleLabels.add(s.customLabels, wInfo)
}

func (s *variantParentCounts) moveActiveToInflight(wInfo *workload.Info) {
	s.rwm.Lock()
	defer s.rwm.Unlock()
	removeVariant(s.active, wInfo)
	s.activeLabels.remove(s.customLabels, wInfo)
	addVariant(s.inflight, wInfo)
	s.inflightLabels.add(s.customLabels, wInfo)
}

func (s *variantParentCounts) removeInflight(wInfo *workload.Info) {
	s.rwm.Lock()
	defer s.rwm.Unlock()
	removeVariant(s.inflight, wInfo)
	s.inflightLabels.remove(s.customLabels, wInfo)
}

// excess returns how many active and inadmissible pending workloads are
// Variants beyond the first one of each Parent. A Parent counts as active when
// any of its Variants is active or inflight, and as inadmissible otherwise.
func (s *variantParentCounts) excess() (int, int) {
	s.rwm.RLock()
	defer s.rwm.RUnlock()
	return variantExcess(s.active, s.inadmissible, s.inflight)
}

// subtractExcess subtracts from the active and inadmissible trackers, keyed by Workload custom label values, the Variants beyond the first one of each Parent, like excess does for the totals.
func (s *variantParentCounts) subtractExcess(active, inadmissible *metrics.LabelValsTracker) {
	s.rwm.RLock()
	defer s.rwm.RUnlock()
	subtractVariantExcess(active, inadmissible, s.activeLabels, s.inadmissibleLabels, s.inflightLabels)
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

// variantLabelCounts counts the pending Variants of each Parent per Workload custom label values.
type variantLabelCounts map[workload.Reference]*metrics.LabelValsTracker

// add counts wInfo under its Parent when wInfo is a ConcurrentAdmission Variant and Workload custom labels are configured.
func (v variantLabelCounts) add(cl *metrics.CustomLabels, wInfo *workload.Info) {
	if !cl.KindConfigured(config.SourceKindWorkload) {
		return
	}
	key := variantParentKey(wInfo)
	if key == "" {
		return
	}
	tracker, ok := v[key]
	if !ok {
		tracker = metrics.NewLabelValsTracker()
		v[key] = tracker
	}
	metrics.TrackWorkload(cl, tracker, wInfo.Obj)
}

// remove reverts add.
func (v variantLabelCounts) remove(cl *metrics.CustomLabels, wInfo *workload.Info) {
	if !cl.KindConfigured(config.SourceKindWorkload) {
		return
	}
	key := variantParentKey(wInfo)
	tracker, ok := v[key]
	if !ok {
		return
	}
	metrics.UntrackWorkload(cl, tracker, wInfo.Obj)
	if tracker.Total() == 0 {
		delete(v, key)
	}
}

// subtractVariantExcess subtracts from the active and inadmissible trackers the Variants beyond the first one of each Parent and label values.
// The active tracker includes the inflight Variants.
// A Parent counts in one state only, like in variantExcess.
// It is active when any of its Variants is active or inflight, and inadmissible otherwise.
// Within that state, the Parent counts once under each label value that its Variants in that state carry.
// The label values of its Variants in the other state do not count.
// A nil map counts as empty.
func subtractVariantExcess(active, inadmissible *metrics.LabelValsTracker, activeVariants, inadmissibleVariants, inflightVariants variantLabelCounts) {
	activeParents := make(map[workload.Reference]struct{}, len(activeVariants)+len(inflightVariants))
	for key, tracker := range activeVariants {
		activeParents[key] = struct{}{}
		inflightTracker := inflightVariants[key]
		for labelVals, n := range tracker.Iter() {
			// The tracker keeps label values whose count dropped to zero.
			// The loop over inflightVariants handles their inflight Variants.
			if n == 0 {
				continue
			}
			if inflightTracker != nil {
				n += inflightTracker.Count(labelVals)
			}
			if n > 1 {
				active.Add(labelVals, 1-n)
			}
		}
	}
	for key, tracker := range inflightVariants {
		activeParents[key] = struct{}{}
		activeTracker := activeVariants[key]
		for labelVals, n := range tracker.Iter() {
			// The label values with active Variants are handled above.
			if activeTracker != nil && activeTracker.Count(labelVals) > 0 {
				continue
			}
			if n > 1 {
				active.Add(labelVals, 1-n)
			}
		}
	}
	for key, tracker := range inadmissibleVariants {
		_, parentActive := activeParents[key]
		for labelVals, n := range tracker.Iter() {
			if parentActive {
				inadmissible.Add(labelVals, -n)
			} else if n > 1 {
				inadmissible.Add(labelVals, 1-n)
			}
		}
	}
}

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
	"slices"
	"sync"

	"k8s.io/apimachinery/pkg/util/sets"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
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
	// parentLabelSets keeps, with a zero count, every label set a Parent has counted under.
	parentLabelSets *metrics.LabelValsTracker
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
		parentLabelSets:    metrics.NewLabelValsTracker(),
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

// hasParent returns whether any pending Variant of the Parent key is active, inadmissible or inflight.
func (s *variantParentCounts) hasParent(key workload.Reference) bool {
	s.rwm.RLock()
	defer s.rwm.RUnlock()
	return s.active[key] > 0 || s.inadmissible[key] > 0 || s.inflight[key] > 0
}

// excess returns how many active and inadmissible pending workloads are
// Variants beyond the first one of each Parent. A Parent counts as active when
// any of its Variants is active or inflight, and as inadmissible otherwise.
func (s *variantParentCounts) excess() (int, int) {
	s.rwm.RLock()
	defer s.rwm.RUnlock()
	return variantExcess(s.active, s.inadmissible, s.inflight)
}

// subtractExcess counts each Parent once in the active and inadmissible trackers, keyed by Workload custom label values, like excess does for the totals.
// The trackers keep a zero count for every label set a Parent has counted under, so the series of a label set no Parent counts under any more are cleared.
func (s *variantParentCounts) subtractExcess(active, inadmissible *metrics.LabelValsTracker, parents *parentLabels) {
	s.rwm.RLock()
	defer s.rwm.RUnlock()
	subtractVariantExcess(active, inadmissible, s.activeLabels, s.inadmissibleLabels, s.inflightLabels, parents, s.parentLabelSets)
	active.Merge(s.parentLabelSets)
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

// subtractVariantExcess replaces, in the active and inadmissible trackers, the Variants of each Parent with one count under one label set.
// The active tracker includes the inflight Variants.
// A Parent counts in one state only, like in variantExcess.
// It is active when any of its Variants is active or inflight, and inadmissible otherwise.
// It counts under the label values of the Parent Workload, so the per-label counts add up to the totals.
// The label sets it counts under are also added to reported with a zero count, so their series can be cleared later.
// A nil map or tracker counts as empty.
func subtractVariantExcess(
	active, inadmissible *metrics.LabelValsTracker,
	activeVariants, inadmissibleVariants, inflightVariants variantLabelCounts,
	parents *parentLabels,
	reported *metrics.LabelValsTracker,
) {
	activeParents := sets.New[workload.Reference]()
	for key := range activeVariants {
		activeParents.Insert(key)
	}
	for key := range inflightVariants {
		activeParents.Insert(key)
	}
	for key := range activeParents {
		subtractCounts(active, activeVariants[key])
		subtractCounts(active, inflightVariants[key])
		subtractCounts(inadmissible, inadmissibleVariants[key])
		addParent(active, reported, parents.get(key), activeVariants[key], inflightVariants[key])
	}
	for key, tracker := range inadmissibleVariants {
		if activeParents.Has(key) {
			continue
		}
		subtractCounts(inadmissible, tracker)
		addParent(inadmissible, reported, parents.get(key), tracker)
	}
}

// subtractCounts subtracts the counts of variants from tracker.
func subtractCounts(tracker, variants *metrics.LabelValsTracker) {
	if variants == nil {
		return
	}
	for labelVals, n := range variants.Iter() {
		tracker.Add(labelVals, -n)
	}
}

// addParent counts one Parent in tracker under the label set of parent.
// When the Parent Workload is unknown, it counts under the smallest label set of its Variants, so the choice does not depend on map order.
func addParent(tracker, reported, parent *metrics.LabelValsTracker, variants ...*metrics.LabelValsTracker) {
	chosen, found := metrics.EmptyValsSet(), false
	if parent != nil {
		variants = []*metrics.LabelValsTracker{parent}
	}
	for _, v := range variants {
		if v == nil {
			continue
		}
		for labelVals, n := range v.Iter() {
			if n > 0 && (!found || slices.Compare(labelVals.OrderedList(), chosen.OrderedList()) < 0) {
				chosen, found = labelVals, true
			}
		}
	}
	tracker.Add(chosen, 1)
	if reported != nil {
		reported.Add(chosen, 0)
	}
}

// parentLabels keeps the Workload custom label values of the ConcurrentAdmission Parents that the queue manager has seen.
type parentLabels struct {
	sync.RWMutex
	byParent map[workload.Reference]*metrics.LabelValsTracker
}

func newParentLabels() *parentLabels {
	return &parentLabels{byParent: make(map[workload.Reference]*metrics.LabelValsTracker)}
}

// store records the label values of the Parent wl and returns whether they changed.
func (p *parentLabels) store(cl *metrics.CustomLabels, wl *kueue.Workload) bool {
	tracker := metrics.NewLabelValsTracker()
	metrics.TrackWorkload(cl, tracker, wl)
	key := workload.Key(wl)
	p.Lock()
	defer p.Unlock()
	changed := true
	if old := p.byParent[key]; old != nil {
		for labelVals := range tracker.Iter() {
			changed = old.Count(labelVals) == 0
		}
	}
	p.byParent[key] = tracker
	return changed
}

// delete forgets the label values of the Parent key and returns whether they were known.
func (p *parentLabels) delete(key workload.Reference) bool {
	p.Lock()
	defer p.Unlock()
	_, ok := p.byParent[key]
	delete(p.byParent, key)
	return ok
}

// get returns the label values of the Parent as a tracker with one count, or nil when the Parent is unknown.
func (p *parentLabels) get(key workload.Reference) *metrics.LabelValsTracker {
	if p == nil {
		return nil
	}
	p.RLock()
	defer p.RUnlock()
	return p.byParent[key]
}

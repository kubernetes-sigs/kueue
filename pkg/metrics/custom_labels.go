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

package metrics

import (
	"iter"
	"slices"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utilmaps "sigs.k8s.io/kueue/pkg/util/maps"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
)

const MaxCustomLabelsForSourceKind = 6

// CustomLabels holds mutable state for custom metric labels.
type CustomLabels struct {
	m map[configapi.SourceKind]*SourceKindLabelStore
}

// Stores metadata and values for a set of custom labels defined for a source kind.
type SourceKindLabelStore struct {
	labelNames []string
	labelSpecs []configapi.ControllerMetricsCustomLabel
	values     *utilmaps.SyncMap[string, []string]
}

func WorkloadCustomLabelSources(entries []configapi.ControllerMetricsCustomLabel) (labels, annotations sets.Set[string]) {
	labels, annotations = sets.New[string](), sets.New[string]()
	for _, entry := range entries {
		if ptr.Deref(entry.SourceKind, configapi.DefaultCustomMetricLabelSourceKind) != configapi.SourceKindWorkload {
			continue
		}
		switch {
		case entry.SourceAnnotationKey != "":
			annotations.Insert(entry.SourceAnnotationKey)
		case entry.SourceLabelKey != "":
			labels.Insert(entry.SourceLabelKey)
		default:
			labels.Insert(entry.Name)
		}
	}
	return
}

func NewCustomLabels(entries []configapi.ControllerMetricsCustomLabel) *CustomLabels {
	if !features.Enabled(features.CustomMetricLabels) || len(entries) == 0 {
		return nil
	}

	requestedKinds := sets.New[configapi.SourceKind]()
	for _, entry := range entries {
		requestedKinds.Insert(ptr.Deref(entry.SourceKind, configapi.DefaultCustomMetricLabelSourceKind))
	}

	cl := &CustomLabels{m: make(map[configapi.SourceKind]*SourceKindLabelStore)}
	for kind := range requestedKinds {
		cl.m[kind] = newSourceKindLabelStore(kind, entries)
	}

	InitMetricVectors(cl)
	return cl
}

// LabelNames returns the computed metric label names (e.g. "custom_team")
// for the requested set of sources.
// The labels are ordered by source kind then by the order of definition in the config.
func (cl *CustomLabels) LabelNames(srcs ...configapi.SourceKind) []string {
	if !cl.enabled() || len(srcs) == 0 {
		return nil
	}

	labels := make([]string, 0)
	for store := range cl.labelStoreIter(srcs) {
		labels = append(labels, store.labelNames...)
	}

	if len(labels) == 0 {
		return nil
	}
	return labels
}

func (cl *CustomLabels) KindConfigured(kind configapi.SourceKind) (isConfigured bool) {
	if !cl.enabled() {
		return
	}
	_, isConfigured = cl.m[kind]
	return
}

func (cl *CustomLabels) MakeValsSet(kind configapi.SourceKind, labels, annotations map[string]string) (ls labelValsSet) {
	if !cl.enabled() || cl.m[kind] == nil {
		return Empty(kind)
	}
	vals := cl.m[kind].extractValues(labels, annotations)
	copy(ls.vals[:], vals)
	ls.labelSetSize = len(vals)
	ls.src = kind
	return
}

// CombineLabelValues returns a combined list of label values from the provided map,
// appended in SourceKind order.
func (cl *CustomLabels) CombineLabelValues(valuesPerSrc map[configapi.SourceKind][]string) (combined []string) {
	if !cl.enabled() || len(valuesPerSrc) == 0 {
		return
	}

	for _, kind := range cl.labelStoreIter(sets.KeySet(valuesPerSrc).UnsortedList()) {
		combined = append(combined, valuesPerSrc[kind]...)
	}
	return
}

func (cl *CustomLabels) UpdateRequired(kind configapi.SourceKind, ref string, labels, annotations map[string]string) bool {
	if !cl.enabled() {
		return false
	}
	store, supported := cl.m[kind]
	if !supported {
		return false
	}
	return !slices.Equal(
		store.get(ref),
		store.extractValues(labels, annotations),
	)
}

func (cl *CustomLabels) Store(kind configapi.SourceKind, ref string, labels, annotations map[string]string) bool {
	if !cl.enabled() || cl.m[kind] == nil {
		return false
	}
	return cl.m[kind].store(ref, labels, annotations)
}

func (cl *CustomLabels) Get(kind configapi.SourceKind, ref string) []string {
	return cl.GetFor(map[configapi.SourceKind]string{kind: ref})
}

// GetFor returns a list of label values, ordered by source kind then by the order of definition in the config.
func (cl *CustomLabels) GetFor(sourceMap map[configapi.SourceKind]string) []string {
	if !cl.enabled() || len(sourceMap) == 0 {
		return nil
	}

	vals := make([]string, 0)
	srcs := sets.KeySet(sourceMap).UnsortedList()
	for store, kind := range cl.labelStoreIter(srcs) {
		vals = append(vals, store.get(sourceMap[kind])...)
	}
	if len(vals) == 0 {
		return nil
	}
	return vals
}

func (cl *CustomLabels) Delete(kind configapi.SourceKind, ref string) {
	if !cl.enabled() || cl.m[kind] == nil {
		return
	}
	cl.m[kind].delete(ref)
}

func (cl *CustomLabels) enabled() bool {
	return cl != nil && features.Enabled(features.CustomMetricLabels)
}

func (cl *CustomLabels) labelStoreIter(srcs []configapi.SourceKind) iter.Seq2[*SourceKindLabelStore, configapi.SourceKind] {
	orderedSrcs := slices.Clone(srcs)
	slices.Sort(orderedSrcs)
	orderedSrcs = slices.Compact(orderedSrcs)
	return func(yield func(*SourceKindLabelStore, configapi.SourceKind) bool) {
		for _, kind := range orderedSrcs {
			if store, ok := cl.m[kind]; ok {
				if !yield(store, kind) {
					return
				}
			}
		}
	}
}

func (cl *CustomLabels) CQStore(key kueue.ClusterQueueReference, labels, annotations map[string]string) bool {
	return cl.Store(configapi.SourceKindClusterQueue, string(key), labels, annotations)
}

func (cl *CustomLabels) CQGet(key kueue.ClusterQueueReference) []string {
	return cl.Get(configapi.SourceKindClusterQueue, string(key))
}

func (cl *CustomLabels) CQDelete(key kueue.ClusterQueueReference) {
	cl.Delete(configapi.SourceKindClusterQueue, string(key))
}

func (cl *CustomLabels) LQStore(key utilqueue.LocalQueueReference, labels, annotations map[string]string) bool {
	return cl.Store(configapi.SourceKindLocalQueue, string(key), labels, annotations)
}

func (cl *CustomLabels) LQGet(key utilqueue.LocalQueueReference) []string {
	return cl.Get(configapi.SourceKindLocalQueue, string(key))
}

func (cl *CustomLabels) LQDelete(key utilqueue.LocalQueueReference) {
	cl.Delete(configapi.SourceKindLocalQueue, string(key))
}

func (cl *CustomLabels) CohortStore(key kueue.CohortReference, labels, annotations map[string]string) bool {
	return cl.Store(configapi.SourceKindCohort, string(key), labels, annotations)
}

func (cl *CustomLabels) CohortGet(key kueue.CohortReference) []string {
	return cl.Get(configapi.SourceKindCohort, string(key))
}

func (cl *CustomLabels) CohortDelete(key kueue.CohortReference) {
	cl.Delete(configapi.SourceKindCohort, string(key))
}

func newSourceKindLabelStore(sourceKind configapi.SourceKind, labelSpecs []configapi.ControllerMetricsCustomLabel) *SourceKindLabelStore {
	names, specs := parseLabels(sourceKind, labelSpecs)
	return &SourceKindLabelStore{
		labelNames: names,
		labelSpecs: specs,
		values:     utilmaps.NewSyncMap[string, []string](0),
	}
}

func parseLabels(targetKind configapi.SourceKind, labelSpecs []configapi.ControllerMetricsCustomLabel) (names []string, specs []configapi.ControllerMetricsCustomLabel) {
	names = make([]string, 0)
	specs = make([]configapi.ControllerMetricsCustomLabel, 0)
	for _, spec := range labelSpecs {
		if spec.SourceKind == nil {
			spec.SourceKind = ptr.To(configapi.DefaultCustomMetricLabelSourceKind)
		}
		if *spec.SourceKind == targetKind {
			names = append(names, "custom_"+spec.Name)
			specs = append(specs, spec)
		}
	}
	return
}

func (s *SourceKindLabelStore) extractValues(labels, annotations map[string]string) []string {
	if s == nil {
		return nil
	}
	vals := make([]string, 0, len(s.labelNames))
	for _, l := range s.labelSpecs {
		var value string
		switch {
		case l.SourceAnnotationKey != "":
			value = annotations[l.SourceAnnotationKey]
		case l.SourceLabelKey != "":
			value = labels[l.SourceLabelKey]
		default:
			value = labels[l.Name]
		}
		if len(l.TrackedValues) == 0 || slices.Contains(l.TrackedValues, value) {
			vals = append(vals, value)
		} else {
			vals = append(vals, configapi.UntrackedCustomLabelValue)
		}
	}
	return vals
}

func (s *SourceKindLabelStore) store(key string, labels, annotations map[string]string) bool {
	newVals := s.extractValues(labels, annotations)
	old, existed := s.values.Swap(key, newVals)
	return existed && !slices.Equal(old, newVals)
}

func (s *SourceKindLabelStore) get(key string) []string {
	vals, recorded := s.values.Get(key)
	if recorded {
		return vals
	}
	return make([]string, len(s.labelNames))
}

func (s *SourceKindLabelStore) delete(key ...string) {
	for _, k := range key {
		s.values.Delete(k)
	}
}

type LabelSetValsCounter struct {
	counts map[labelValsSet]int
	total  int
}

// Wrapper for a list representing values of a custom labels set.
type labelValsSet struct {
	src          configapi.SourceKind
	vals         [MaxCustomLabelsForSourceKind]string
	labelSetSize int
}

func NewLabelSetValsCounter() *LabelSetValsCounter {
	return &LabelSetValsCounter{
		counts: make(map[labelValsSet]int, 0),
		total:  0,
	}
}

func CombinedCounter(a, b *LabelSetValsCounter) *LabelSetValsCounter {
	return NewLabelSetValsCounter().merge(a).merge(b)
}

func ParallelIter(a, b *LabelSetValsCounter) iter.Seq2[labelValsSet, [2]int] {
	allValSets := sets.New[labelValsSet]()
	allValSets = allValSets.Union(sets.KeySet(a.counts))
	allValSets = allValSets.Union(sets.KeySet(b.counts))
	return func(yield func(labelValsSet, [2]int) bool) {
		for ls := range allValSets {
			if !yield(ls, [2]int{a.Get(ls), b.Get(ls)}) {
				return
			}
		}
	}
}

func (c *LabelSetValsCounter) Incr(ls labelValsSet) {
	c.Add(ls, 1)
}

func (c *LabelSetValsCounter) Add(ls labelValsSet, incr int) {
	c.counts[ls] += incr
	c.total += incr
}

func (c *LabelSetValsCounter) Get(ls labelValsSet) int {
	return c.counts[ls]
}

func (c *LabelSetValsCounter) Total() int {
	return c.total
}

func (c *LabelSetValsCounter) merge(other *LabelSetValsCounter) *LabelSetValsCounter {
	if other == nil {
		return c
	}
	for k, v := range other.counts {
		c.counts[k] += v
	}
	c.total += other.total
	return c
}

func Empty(kind configapi.SourceKind) labelValsSet {
	return labelValsSet{
		src: kind,
	}
}

func (s *labelValsSet) ExtractVals() []string {
	return s.vals[:]
}

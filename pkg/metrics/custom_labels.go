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
	"slices"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utilmaps "sigs.k8s.io/kueue/pkg/util/maps"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
)

// CustomLabelStore is a type-safe store for custom label values, used for stale series cleanup.
type CustomLabelStore[K ~string] struct {
	m *utilmaps.SyncMap[string, []string]
}

func newCustomLabelStore[K ~string]() CustomLabelStore[K] {
	return CustomLabelStore[K]{m: utilmaps.NewSyncMap[string, []string](0)}
}

// Store saves values and returns true if they changed.
func (s CustomLabelStore[K]) Store(key K, newVals []string) bool {
	old, existed := s.m.Swap(string(key), newVals)
	return existed && !slices.Equal(old, newVals)
}

// StoreAndClear is like Store but also calls clearFn on change.
func (s CustomLabelStore[K]) StoreAndClear(key K, newVals []string, clearFn func()) bool {
	changed := s.Store(key, newVals)
	if changed && clearFn != nil {
		clearFn()
	}
	return changed
}

func (s CustomLabelStore[K]) Get(key K) []string {
	vals, _ := s.m.Get(string(key))
	return vals
}

func (s CustomLabelStore[K]) Delete(key K) {
	s.m.Delete(string(key))
}

// CustomLabels holds mutable state for custom metric labels.
type CustomLabels struct {
	names        []string
	labelEntries []configapi.ControllerMetricsCustomLabel
	cq           CustomLabelStore[kueue.ClusterQueueReference]
	lq           CustomLabelStore[utilqueue.LocalQueueReference]
	cohort       CustomLabelStore[kueue.CohortReference]
}

func NewCustomLabels(entries []configapi.ControllerMetricsCustomLabel) *CustomLabels {
	cl := &CustomLabels{
		cq:     newCustomLabelStore[kueue.ClusterQueueReference](),
		lq:     newCustomLabelStore[utilqueue.LocalQueueReference](),
		cohort: newCustomLabelStore[kueue.CohortReference](),
	}
	if len(entries) > 0 {
		cl.labelEntries = entries
		cl.names = make([]string, len(entries))
		for i, e := range entries {
			cl.names[i] = "custom_" + e.Name
		}
		InitMetricVectors(cl.names)
	}
	return cl
}

// LabelNames returns the computed metric label names (e.g. "custom_team").
func (cl *CustomLabels) LabelNames() []string {
	if cl == nil {
		return nil
	}
	return cl.names
}

// ExtractValues reads custom label values from object metadata.
func (cl *CustomLabels) ExtractValues(labels, annotations map[string]string) []string {
	if cl == nil || len(cl.labelEntries) == 0 {
		return nil
	}
	vals := make([]string, len(cl.labelEntries))
	for i, e := range cl.labelEntries {
		switch {
		case e.SourceAnnotationKey != "":
			vals[i] = annotations[e.SourceAnnotationKey]
		case e.SourceLabelKey != "":
			vals[i] = labels[e.SourceLabelKey]
		default:
			vals[i] = labels[e.Name]
		}
	}
	return vals
}

func storeFor[K ~string](cl *CustomLabels, s CustomLabelStore[K], key K, labels, annotations map[string]string) bool {
	return s.Store(key, cl.ExtractValues(labels, annotations))
}

func storeAndClearFor[K ~string](cl *CustomLabels, s CustomLabelStore[K], key K, labels, annotations map[string]string, clearFn func()) bool {
	return s.StoreAndClear(key, cl.ExtractValues(labels, annotations), clearFn)
}

func getFor[K ~string](cl *CustomLabels, s CustomLabelStore[K], key K) []string {
	vals := s.Get(key)
	if vals == nil && len(cl.names) > 0 {
		return make([]string, len(cl.names))
	}
	return vals
}

func deleteFor[K ~string](s CustomLabelStore[K], key K) {
	s.Delete(key)
}

func (cl *CustomLabels) CQStore(key kueue.ClusterQueueReference, labels, annotations map[string]string) bool {
	if cl == nil {
		return false
	}
	return storeFor(cl, cl.cq, key, labels, annotations)
}

func (cl *CustomLabels) CQStoreAndClear(key kueue.ClusterQueueReference, labels, annotations map[string]string, clearFn func()) bool {
	if cl == nil {
		return false
	}
	return storeAndClearFor(cl, cl.cq, key, labels, annotations, clearFn)
}

func (cl *CustomLabels) CQGet(key kueue.ClusterQueueReference) []string {
	if cl == nil {
		return nil
	}
	return getFor(cl, cl.cq, key)
}

func (cl *CustomLabels) CQDelete(key kueue.ClusterQueueReference) {
	if cl == nil {
		return
	}
	deleteFor(cl.cq, key)
}

func (cl *CustomLabels) LQStore(key utilqueue.LocalQueueReference, labels, annotations map[string]string) bool {
	if cl == nil {
		return false
	}
	return storeFor(cl, cl.lq, key, labels, annotations)
}

func (cl *CustomLabels) LQStoreAndClear(key utilqueue.LocalQueueReference, labels, annotations map[string]string, clearFn func()) bool {
	if cl == nil {
		return false
	}
	return storeAndClearFor(cl, cl.lq, key, labels, annotations, clearFn)
}

func (cl *CustomLabels) LQGet(key utilqueue.LocalQueueReference) []string {
	if cl == nil {
		return nil
	}
	return getFor(cl, cl.lq, key)
}

func (cl *CustomLabels) LQDelete(key utilqueue.LocalQueueReference) {
	if cl == nil {
		return
	}
	deleteFor(cl.lq, key)
}

func (cl *CustomLabels) CohortStore(key kueue.CohortReference, labels, annotations map[string]string) bool {
	if cl == nil {
		return false
	}
	return storeFor(cl, cl.cohort, key, labels, annotations)
}

func (cl *CustomLabels) CohortStoreAndClear(key kueue.CohortReference, labels, annotations map[string]string, clearFn func()) bool {
	if cl == nil {
		return false
	}
	return storeAndClearFor(cl, cl.cohort, key, labels, annotations, clearFn)
}

func (cl *CustomLabels) CohortGet(key kueue.CohortReference) []string {
	if cl == nil {
		return nil
	}
	return getFor(cl, cl.cohort, key)
}

func (cl *CustomLabels) CohortDelete(key kueue.CohortReference) {
	if cl == nil {
		return
	}
	deleteFor(cl.cohort, key)
}

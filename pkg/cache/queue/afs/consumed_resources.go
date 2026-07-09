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

package afs

import (
	"time"

	corev1 "k8s.io/api/core/v1"

	utilmaps "sigs.k8s.io/kueue/pkg/util/maps"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
)

// ConsumedResourcesEntry stores the consumed resources for a LocalQueue with timestamp.
// StatusAccounted records whether the LocalQueue's persisted fair-sharing status has
// been folded into this entry: once history is merged, the entry's existence alone no
// longer tells whether that happened, so it must be tracked explicitly. Every writer
// must carry the flag forward (use Update), or the history would be merged repeatedly.
type ConsumedResourcesEntry struct {
	Resources       corev1.ResourceList
	LastUpdate      time.Time
	StatusAccounted bool
}

// AfsConsumedResources manages the fair sharing consumed resources cache
type AfsConsumedResources struct {
	resources *utilmaps.SyncMap[utilqueue.LocalQueueReference, ConsumedResourcesEntry]
}

// NewAfsConsumedResources creates a new AfsConsumedResources cache
func NewAfsConsumedResources() *AfsConsumedResources {
	return &AfsConsumedResources{
		resources: utilmaps.NewSyncMap[utilqueue.LocalQueueReference, ConsumedResourcesEntry](0),
	}
}

// Set unconditionally replaces the entry for a LocalQueue, resetting
// StatusAccounted. Controllers should use Update instead.
func (a *AfsConsumedResources) Set(lqKey utilqueue.LocalQueueReference, resources corev1.ResourceList, lastUpdate time.Time) {
	a.resources.Add(lqKey, ConsumedResourcesEntry{
		Resources:  resources,
		LastUpdate: lastUpdate,
	})
}

// Update atomically rewrites the entry for a LocalQueue with the result of fn.
// fn receives the current entry (zero-valued if absent) and whether it was
// present. It runs under the map's write lock, so it must be pure computation:
// it must not call back into the scheduler cache (lock ordering).
// All controller writers should go through Update so concurrent read-modify-writes
// cannot overwrite each other and StatusAccounted is preserved by construction.
func (a *AfsConsumedResources) Update(lqKey utilqueue.LocalQueueReference, fn func(entry ConsumedResourcesEntry, found bool) ConsumedResourcesEntry) ConsumedResourcesEntry {
	return a.resources.Update(lqKey, fn)
}

// Get retrieves the consumed resources for a LocalQueue
func (a *AfsConsumedResources) Get(lqKey utilqueue.LocalQueueReference) (ConsumedResourcesEntry, bool) {
	return a.resources.Get(lqKey)
}

// Delete removes the consumed resources entry for a LocalQueue
func (a *AfsConsumedResources) Delete(lqKey utilqueue.LocalQueueReference) {
	a.resources.Delete(lqKey)
}

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

// ConsumedResourcesEntry stores the consumed resources for a LocalQueue with timestamp
type ConsumedResourcesEntry struct {
	Resources  corev1.ResourceList
	LastUpdate time.Time
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

// Set updates the consumed resources for a LocalQueue
func (a *AfsConsumedResources) Set(lqKey utilqueue.LocalQueueReference, resources corev1.ResourceList, lastUpdate time.Time) {
	a.resources.Add(lqKey, ConsumedResourcesEntry{
		Resources:  resources,
		LastUpdate: lastUpdate,
	})
}

// SetIfAbsent stores the consumed resources for a LocalQueue only when no
// entry exists yet, and returns the entry present in the cache afterwards.
// The check and the store happen under a single lock, so an entry created
// concurrently (e.g. by workload settlement) is never overwritten.
func (a *AfsConsumedResources) SetIfAbsent(lqKey utilqueue.LocalQueueReference, resources corev1.ResourceList, lastUpdate time.Time) (ConsumedResourcesEntry, bool) {
	return a.resources.GetOrAdd(lqKey, ConsumedResourcesEntry{
		Resources:  resources,
		LastUpdate: lastUpdate,
	})
}

// Get retrieves the consumed resources for a LocalQueue
func (a *AfsConsumedResources) Get(lqKey utilqueue.LocalQueueReference) (ConsumedResourcesEntry, bool) {
	return a.resources.Get(lqKey)
}

// Delete removes the consumed resources entry for a LocalQueue
func (a *AfsConsumedResources) Delete(lqKey utilqueue.LocalQueueReference) {
	a.resources.Delete(lqKey)
}

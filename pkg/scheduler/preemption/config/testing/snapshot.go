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

package testing

import (
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/hierarchy"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
)

// SnapshotBuilder provides a fluent API for building scheduler cache snapshots with cohort and cluster queue hierarchies in tests.
type SnapshotBuilder struct {
	mgr hierarchy.Manager[*schdcache.ClusterQueueSnapshot, *schdcache.CohortSnapshot]
}

// NewSnapshotBuilder initializes an empty SnapshotBuilder.
func NewSnapshotBuilder() *SnapshotBuilder {
	return &SnapshotBuilder{
		mgr: hierarchy.NewManager(func(name kueue.CohortReference) *schdcache.CohortSnapshot {
			return &schdcache.CohortSnapshot{
				Name:   name,
				Cohort: hierarchy.NewCohort[*schdcache.ClusterQueueSnapshot, *schdcache.CohortSnapshot](),
			}
		}),
	}
}

// Cohort adds a Cohort to the snapshot hierarchy, optionally linking it to a parent Cohort.
func (b *SnapshotBuilder) Cohort(name, parent kueue.CohortReference) *SnapshotBuilder {
	b.mgr.AddCohort(name)
	if parent != "" {
		b.mgr.UpdateCohortEdge(name, parent)
	}
	return b
}

// ClusterQueue adds a ClusterQueueSnapshot to the snapshot hierarchy, optionally linking it to a parent Cohort.
func (b *SnapshotBuilder) ClusterQueue(name kueue.ClusterQueueReference, parent kueue.CohortReference) *SnapshotBuilder {
	b.mgr.AddClusterQueue(&schdcache.ClusterQueueSnapshot{Name: name})
	if parent != "" {
		b.mgr.UpdateClusterQueueEdge(name, parent)
	}
	return b
}

// Build creates and returns the configured scheduler cache Snapshot.
func (b *SnapshotBuilder) Build() *schdcache.Snapshot {
	return &schdcache.Snapshot{
		Manager: b.mgr,
	}
}

// Copyright The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package state

import (
	"fmt"

	"sigs.k8s.io/scheduler-library/pkg/upstreamsync"
	"sigs.k8s.io/scheduler-library/pkg/upstreamsync/snapshot"

	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/scheduler/backend/cache"
)

type ClusterState struct {
	Cache        cache.Cache
	snapshot     *snapshot.ClusterSnapshot
	snapshotData *cache.Snapshot
}

// New creates a new ClusterState with an internal Kubernetes scheduler cache, frameworks,
// and the snapshot instance shared with all frameworks via WithSnapshotSharedLister.
func New(c cache.Cache, profiles *upstreamsync.ProfileMap, snap *cache.Snapshot) *ClusterState {
	return &ClusterState{
		Cache:        c,
		snapshot:     snapshot.New(snap, profiles),
		snapshotData: snap,
	}
}

// GetAssociatedSnapshot returns the snapshot instance associated with this [ClusterState].
// Use [ClusterState.SyncSnapshot] to sync the snapshot state with the current cluster state.
func (s *ClusterState) GetAssociatedSnapshot() *snapshot.ClusterSnapshot {
	return s.snapshot
}

// SyncSnapshot uses the current cluster state to update the associated snapshot in-place.
// Any mutations done on the snapshot since last sync will be reverted.
func (s *ClusterState) SyncSnapshot(logger klog.Logger) error {
	if err := s.snapshot.ResetMutations(); err != nil {
		return fmt.Errorf("failed to reset mutations: %w", err)
	}
	if err := s.Cache.UpdateSnapshot(logger, s.snapshotData); err != nil {
		return fmt.Errorf("failed to update snapshot: %w", err)
	}
	return nil
}

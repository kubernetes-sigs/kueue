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
	Cache      cache.Cache
	profiles   *upstreamsync.ProfileMap
	sharedSnap *cache.Snapshot
}

// New creates a new ClusterState with an internal Kubernetes scheduler cache, frameworks,
// and the snapshot instance shared with all frameworks via WithSnapshotSharedLister.
func New(c cache.Cache, profiles *upstreamsync.ProfileMap, snap *cache.Snapshot) *ClusterState {
	return &ClusterState{
		Cache:      c,
		profiles:   profiles,
		sharedSnap: snap,
	}
}

// Snapshot constructs a snapshot from the current cluster state by updating the shared snapshot
// in-place. Calling Snapshot again invalidates any previously returned ClusterSnapshot — the caller
// must not use a previous snapshot after requesting a new one.
func (s *ClusterState) Snapshot(logger klog.Logger) (*snapshot.ClusterSnapshot, error) {
	snap := s.sharedSnap
	if err := s.Cache.UpdateSnapshot(logger, snap); err != nil {
		return nil, fmt.Errorf("failed to update snapshot: %w", err)
	}
	return snapshot.New(snap, s.profiles), nil
}

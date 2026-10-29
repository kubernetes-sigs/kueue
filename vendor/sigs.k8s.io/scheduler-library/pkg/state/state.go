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
	"context"
	"fmt"
	"sync"

	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	fwk "k8s.io/kube-scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/backend/cache"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"sigs.k8s.io/scheduler-library/pkg/upstreamsync"
	"sigs.k8s.io/scheduler-library/pkg/upstreamsync/snapshot"
)

// ClusterState is a struct holding state of cluster for running simulations.
// It has initial state of cluster, stored in Cache and simulated state in snapshot.
// Cache can be modified only by a user to change starting state of simulation.
type ClusterState struct {
	Cache                 cache.Cache
	snapshot              *snapshot.ClusterSnapshot
	snapshotData          *cache.Snapshot
	assumedPodCycleStates sync.Map // pod uid -> *fwk.CycleState
	profiles              *upstreamsync.ProfileMap
}

// New creates a new ClusterState with an internal Kubernetes scheduler cache, frameworks,
// and the snapshot instance shared with all frameworks via WithSnapshotSharedLister.
func New(c cache.Cache, profiles *upstreamsync.ProfileMap, snap *cache.Snapshot) *ClusterState {
	return &ClusterState{
		Cache:                 c,
		snapshot:              snapshot.New(snap, profiles),
		snapshotData:          snap,
		assumedPodCycleStates: sync.Map{},
		profiles:              profiles,
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

// AssumeAndReserve assumes the given pod in the cluster state cache and runs the Reserve plugin methods.
// If the reservation fails, the pod is unreserved and forgotten from the cache.
func (s *ClusterState) AssumeAndReserve(ctx context.Context, pod *v1.Pod, cycleState fwk.CycleState, nodeName string) error {
	if pod == nil {
		return fmt.Errorf("pod is nil")
	}
	if s.Cache == nil {
		return fmt.Errorf("cache is nil")
	}
	if s.profiles == nil {
		return fmt.Errorf("profiles is nil")
	}
	if cycleState == nil {
		return fmt.Errorf("cycle state is nil")
	}
	if nodeName == "" {
		return fmt.Errorf("node name is empty")
	}
	if pod.Spec.NodeName != "" && pod.Spec.NodeName != nodeName {
		return fmt.Errorf("pod %q has different node name in cache", pod.Name)
	}

	schedFramework, err := s.profiles.FrameworkForPod(pod)
	if err != nil {
		return fmt.Errorf("failed to get framework for pod: %w", err)
	}

	sched := upstreamsync.NewScheduler(s.snapshotData, 0, 0, 0, s.Cache)

	podInfo, err := framework.NewPodInfo(pod)
	if err != nil {
		return fmt.Errorf("failed to create pod info: %w", err)
	}

	_, status := sched.AssumeAndReserveInCache(ctx, cycleState, schedFramework, podInfo, nodeName)
	if !status.IsSuccess() {
		return status.AsError()
	}
	s.assumedPodCycleStates.Store(pod.UID, cycleState)

	return nil
}

// UnreserveAndForget runs the Unreserve plugin methods for the assumed pod and removes (forgets) it from the cluster state cache.
func (s *ClusterState) UnreserveAndForget(ctx context.Context, pod *v1.Pod) error {
	if pod == nil {
		return fmt.Errorf("pod is nil")
	}
	if s.Cache == nil {
		return fmt.Errorf("cache is nil")
	}
	if s.profiles == nil {
		return fmt.Errorf("profiles is nil")
	}
	schedFramework, err := s.profiles.FrameworkForPod(pod)
	if err != nil {
		return fmt.Errorf("failed to get framework for pod: %w", err)
	}
	podToUnreserve, err := s.Cache.GetPod(pod)
	if err != nil {
		return fmt.Errorf("failed to get pod: %w", err)
	}
	nodeName := podToUnreserve.Spec.NodeName
	if nodeName == "" {
		return fmt.Errorf("pod %q has no assigned node in cache", pod.Name)
	}
	if pod.Spec.NodeName != "" && nodeName != pod.Spec.NodeName {
		return fmt.Errorf("pod %q has different node name in cache", pod.Name)
	}
	podInfo, err := framework.NewPodInfo(podToUnreserve)
	if err != nil {
		return fmt.Errorf("failed to create pod info: %w", err)
	}
	rawCycleState, ok := s.assumedPodCycleStates.Load(pod.UID)
	if !ok {
		return fmt.Errorf("cycle state not found for pod: %q", pod.Name)
	}
	cycleState, ok := rawCycleState.(fwk.CycleState)
	if !ok {
		return fmt.Errorf("invalid cycle state type for pod: %q", pod.Name)
	}
	sched := upstreamsync.NewScheduler(s.snapshotData, 0, 0, 0, s.Cache)
	err = sched.UnreserveAndForgetFromCache(ctx, cycleState, schedFramework, podInfo, nodeName)
	if err != nil {
		return err
	}
	s.assumedPodCycleStates.Delete(pod.UID)

	return nil
}

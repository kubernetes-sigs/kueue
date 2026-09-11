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

// Package snapshot provides ClusterSnapshot, the in-memory, mutable view of the cluster that all
// the simulation runs against.
//
// Although the package sits under upstreamsync — ClusterSnapshot is expected to be absorbed by
// kube-scheduler's snapshot handling over time — its methods are the library's simulation API:
// ClusterSnapshot.MakePlacement, CanSchedulePod, SchedulePods, SchedulePodsByTemplate,
// PreemptPods, Unpreempt and Transaction.
//
// A ClusterSnapshot should be obtained from simulator.SchedulingSimulator, either via
// NewClusterSnapshot or via NewClusterState followed by state.ClusterState.Snapshot. Calling New
// directly requires the caller to build the scheduling profiles and initialize the scheduler
// metrics themselves. See package sigs.k8s.io/scheduler-library/pkg/simulator for the full flow.
package snapshot

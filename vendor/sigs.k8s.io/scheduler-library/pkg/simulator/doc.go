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

// Package simulator is the entry point of the scheduler library.
//
// Everything a consumer needs is reachable from SchedulingSimulator; the other packages either
// hold the types it returns or hold logic that is meant to be upstreamed to kube-scheduler
// (see the note on package layout below).
//
// # Getting started
//
// The setup is done once per consumer:
//
//		client, err := simulator.NewReadonlyClient(restConfig)
//		sim, err := simulator.NewSchedulingSimulator(ctx, cfg, client, nil)
//
//	  - simulator.NewReadonlyClient turns a *rest.Config into the only client type the library
//	    accepts. Its transport rejects every mutating request, so a simulation cannot touch the
//	    real cluster.
//	  - simulator.NewSchedulingSimulator starts the informers and holds the configuration used to
//	    build the scheduling profiles. cfg may be nil, which selects the default kube-scheduler
//	    profile, and so may informerFactory, which makes the simulator create its own.
//
// # Obtaining a snapshot
//
// A snapshot.ClusterSnapshot is an in-memory, mutable view of the cluster that all the
// simulation runs against. These two are the only supported ways to get one:
//
//   - SchedulingSimulator.NewClusterState followed by state.ClusterState.Snapshot, when the
//     simulation should start from the live cluster state (see "Tracking the cluster" below).
//   - SchedulingSimulator.NewClusterSnapshot, when the simulation should start from an
//     explicitly provided set of pods and nodes. Those are the whole world for that snapshot:
//     reflecting a later change means asking for a new one.
//
// Assembling a ClusterSnapshot by hand is not an option: all of its fields are unexported, and
// snapshot.New — exported only because pkg/state builds on it — takes an already built profile
// map and assumes the scheduler metrics are registered. The two entry points above are what
// builds the plugin chain out of the KubeSchedulerConfiguration and registers those metrics.
//
// # Tracking the cluster
//
// ClusterState is long-lived and is meant to follow the real cluster: its Cache is the upstream
// scheduler cache, fed by the consumer from its own informers or event handlers
// (AddPod/UpdatePod/RemovePod, AddNode/UpdateNode/RemoveNode). Each ClusterState.Snapshot call
// then folds the accumulated changes in incrementally instead of rebuilding the view from
// scratch, which is what makes keeping a long-running simulation up to date cheap.
//
// The changes are applied in place, to the snapshot shared by all the scheduling profiles, so a
// ClusterSnapshot obtained from an earlier Snapshot call must not be used afterwards.
//
// # Simulating
//
// All the simulation methods live on *snapshot.ClusterSnapshot and are gathered in the Simulator
// interface, which is what NewClusterSnapshot returns:
//
//   - MakePlacement turns node names into the *fwk.Placement that the other methods restrict
//     the simulation to.
//   - CanSchedulePod reports which of the nodes in the placement fit a single pod, without
//     changing the snapshot.
//   - SchedulePods schedules the given pods one by one and, unless DryRun is set, keeps the
//     result in the snapshot.
//   - SchedulePodsByTemplate schedules as many pods created from a pod template as fit.
//   - PreemptPods removes running pods from the snapshot, and Unpreempt puts them back.
//   - Transaction groups any of the above and commits or reverts them as a whole.
//
// # Package layout
//
// The types returned by this package deliberately live elsewhere:
//
//   - pkg/state holds ClusterState, the long-lived, mutable representation of the real cluster.
//   - pkg/upstreamsync holds logic copied from (or destined for) kube-scheduler; consumers are
//     not expected to call into it directly. pkg/upstreamsync/snapshot holds ClusterSnapshot,
//     which is intended to be absorbed by kube-scheduler's snapshot handling over time, which is
//     why it lives under upstreamsync despite carrying the consumer-facing methods listed above.
//   - pkg/framework wires the upstream scheduler framework for in-memory use.
package simulator

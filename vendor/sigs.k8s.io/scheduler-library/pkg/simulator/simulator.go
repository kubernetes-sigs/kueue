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

package simulator

import (
	"context"
	"fmt"

	"sigs.k8s.io/scheduler-library/pkg/framework"
	"sigs.k8s.io/scheduler-library/pkg/state"
	"sigs.k8s.io/scheduler-library/pkg/upstreamsync"
	"sigs.k8s.io/scheduler-library/pkg/upstreamsync/snapshot"

	v1 "k8s.io/api/core/v1"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	fwk "k8s.io/kube-scheduler/framework"
	"k8s.io/kubernetes/pkg/features"
	"k8s.io/kubernetes/pkg/scheduler"
	schedulerapi "k8s.io/kubernetes/pkg/scheduler/apis/config"
	"k8s.io/kubernetes/pkg/scheduler/backend/cache"
	schedFwk "k8s.io/kubernetes/pkg/scheduler/framework"
)

// Simulator is the set of "what-if" operations that run against a single in-memory view of the
// cluster. It is implemented by *snapshot.ClusterSnapshot — what SchedulingSimulator.NewClusterSnapshot
// returns and what state.ClusterState.Snapshot hands out — and exists to make the entry points of a
// simulation visible from this package; consumers are not expected to implement it.
type Simulator interface {
	// MakePlacement turns node names into the *fwk.Placement that the other methods restrict the simulation to.
	MakePlacement(candidateNodeNames []string) (*fwk.Placement, error)

	// CanSchedulePod reports which of the nodes in the placement fit a single pod, leaving the
	// snapshot untouched. The returned *schedFwk.Diagnosis explains why the remaining nodes were
	// rejected.
	CanSchedulePod(ctx context.Context, pod *v1.Pod, placement *fwk.Placement) ([]string, *schedFwk.Diagnosis, error)

	// SchedulePods schedules the given pods one by one onto the placement and, unless opts.DryRun is
	// set, keeps the result in the snapshot; every scheduled pod gets its Spec.NodeName set. The
	// returned slice holds one result per attempted pod.
	SchedulePods(ctx context.Context, pods []*v1.Pod, placement *fwk.Placement, opts snapshot.SchedulePodsOptions) ([]snapshot.SchedulingResult, error)

	// SchedulePodsByTemplate schedules as many pods created from the template as fit, up to maxPods.
	// It stops at the first pod that does not fit, as the next identical one would not fit either;
	// each SchedulingResult carries the generated pod, which is the only way to learn what was
	// scheduled.
	SchedulePodsByTemplate(ctx context.Context, template *v1.PodTemplateSpec, placement *fwk.Placement, maxPods int, opts snapshot.SchedulePodsByTemplateOptions) ([]snapshot.SchedulingResult, error)

	// PreemptPods removes the given running pods from the snapshot and returns the handle that puts
	// them back. The handle is single-use and is invalidated by any later permanent mutation of the
	// snapshot. If any pod fails to be preempted, the ones already removed by this call are restored
	// and an error is returned.
	PreemptPods(ctx context.Context, pods []*v1.Pod) (_ *snapshot.Unpreemption, err error)

	// Transaction groups any of the above and commits or reverts them as a whole: the mutations are
	// kept if transactionFn returns snapshot.Commit, and undone if it returns snapshot.Revert or an
	// error. Transactions cannot be nested.
	Transaction(ctx context.Context, transactionFn func() (snapshot.TransactionResult, error)) error

	// Unpreempt undoes the preemption the handle was returned for and reports the pods that were put
	// back. It fails if the handle has already been used, or if the snapshot has moved on since the
	// preemption.
	Unpreempt(u *snapshot.Unpreemption) ([]*v1.Pod, error)
}

// SchedulingSimulator is the entry point of the library: it owns the scheduler configuration and
// the informers, and creates the objects the simulation is run against (see NewClusterState and
// NewClusterSnapshot). It is meant to be created once and reused; every state and snapshot it
// creates gets its own scheduling profiles built from the same configuration.
type SchedulingSimulator struct {
	cfg             *schedulerapi.KubeSchedulerConfiguration
	informerFactory informers.SharedInformerFactory
	client          kubernetes.Interface
}

// NewSchedulingSimulator creates a new SchedulingSimulator.
// The cfg may be nil, in which case the default kube-scheduler profile is used, and so may the
// informerFactory, in which case one is created from the client. The informers are started and
// synced before returning, so the call blocks until the cluster state has been read.
func NewSchedulingSimulator(
	ctx context.Context,
	cfg *schedulerapi.KubeSchedulerConfiguration,
	client ReadonlyClient,
	informerFactory informers.SharedInformerFactory,
) (*SchedulingSimulator, error) {
	if client.client == nil {
		return nil, fmt.Errorf("client needs to be provided, got nil")
	}

	framework.InitMetricsOnce()

	if informerFactory == nil {
		informerFactory = scheduler.NewInformerFactory(client.client, 0, nil)
	}
	_ = informerFactory.Core().V1().Nodes().Informer()
	_ = informerFactory.Core().V1().Pods().Informer()
	informerFactory.StartWithContext(ctx)
	res := informerFactory.WaitForCacheSyncWithContext(ctx)
	if res.Err != nil {
		return nil, res.Err
	}

	return &SchedulingSimulator{
		cfg:             cfg,
		informerFactory: informerFactory,
		client:          client.client,
	}, nil
}

// NewClusterState initializes a new runtime cluster state.
func (s *SchedulingSimulator) NewClusterState(ctx context.Context) (*state.ClusterState, error) {
	snap := cache.NewEmptySnapshot()

	internalCache := cache.New(ctx, nil, utilfeature.DefaultFeatureGate.Enabled(features.GenericWorkload), utilfeature.DefaultFeatureGate.Enabled(features.CompositePodGroup))
	profiles, err := s.buildProfileMap(ctx, snap)
	if err != nil {
		return nil, err
	}

	return state.New(internalCache, profiles, snap), nil
}

// NewClusterSnapshot initializes a new snapshot with the provided pods and nodes.
func (s *SchedulingSimulator) NewClusterSnapshot(ctx context.Context, pods []*v1.Pod, nodes []*v1.Node) (Simulator, error) {
	snap := cache.NewSnapshot(pods, nodes)

	profiles, err := s.buildProfileMap(ctx, snap)
	if err != nil {
		return nil, err
	}

	return snapshot.New(snap, profiles), nil
}

func (s *SchedulingSimulator) buildProfileMap(ctx context.Context, snap *cache.Snapshot) (*upstreamsync.ProfileMap, error) {
	profiles, err := framework.NewProfileMap(ctx, s.client, s.informerFactory, snap, s.cfg)
	if err != nil {
		return nil, fmt.Errorf("schedlib: building scheduler: %w", err)
	}
	s.informerFactory.StartWithContext(ctx)
	res := s.informerFactory.WaitForCacheSyncWithContext(ctx)
	if res.Err != nil {
		return nil, res.Err
	}
	return profiles, nil
}

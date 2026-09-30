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
	schedulingv1alpha3 "k8s.io/api/scheduling/v1alpha3"
	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/client-go/informers"
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
	// set, keeps the result in the snapshot. The pods passed in are left untouched; the returned
	// slice holds one result per attempted pod, each carrying a copy of the pod the attempt was made
	// for, with the selected node set when it was scheduled. On a pod that was not scheduled
	// Spec.NodeName is left as it came in, so it is empty unless the caller already set one.
	SchedulePods(ctx context.Context, pods []*v1.Pod, placement *fwk.Placement, opts snapshot.SchedulePodsOptions) ([]snapshot.SchedulingResult, error)

	// SchedulePodsByTemplate schedules as many pods created from the template as fit, up to maxPods.
	// It stops at the first pod that does not fit, as the next identical one would not fit either;
	// each SchedulingResult carries the generated pod, which is the only way to learn what was
	// scheduled.
	SchedulePodsByTemplate(ctx context.Context, template *v1.PodTemplateSpec, placement *fwk.Placement, maxPods int, opts snapshot.SchedulePodsByTemplateOptions) ([]snapshot.SchedulingResult, error)

	// ScheduleWorkload schedules the given pods belonging to the same hierarchy using the workload-aware scheduling algorithm.
	// If the pods do not belong to the same hierarchy, it returns an error.
	// The order of the returned SchedulingResult slice is non-deterministic with respect to the input pods order.
	ScheduleWorkload(ctx context.Context, pods []*v1.Pod, opts snapshot.ScheduleWorkloadOptions) ([]snapshot.SchedulingResult, error)

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
// Note that initializing states and snapshots (NewClusterState and NewClusterSnapshot) is not
// safe for concurrent use on the same SchedulingSimulator instance.
type SchedulingSimulator struct {
	comps           *upstreamsync.FrameworkComponents
	informerFactory informers.SharedInformerFactory

	// informerCtx is what the informers run under. The factory is shared by every state and
	// snapshot the simulator creates, so their lifetime is the simulator's, not any one call's.
	informerCtx context.Context
}

// NewSchedulingSimulator creates a new SchedulingSimulator.
// The cfg may be nil, in which case the default kube-scheduler profile is used, and so may the
// informerFactory, in which case one is created from the client. The informers are started and
// synced before returning, so the call blocks until the cluster state has been read.
// The ctx bounds their lifetime, including the informers that NewClusterState and
// NewClusterSnapshot later register on the same factory.
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

	var opts []upstreamsync.Option
	if cfg != nil {
		opts = append(opts, upstreamsync.WithProfiles(cfg.Profiles...))
	}

	comps, err := upstreamsync.NewFrameworkComponents(ctx, client.client, informerFactory, opts...)
	if err != nil {
		return nil, fmt.Errorf("schedlib: initializing framework components: %w", err)
	}

	informerFactory.StartWithContext(ctx)
	res := informerFactory.WaitForCacheSyncWithContext(ctx)
	if res.Err != nil {
		return nil, res.Err
	}
	if err := comps.WaitForHandlersSync(ctx); err != nil {
		return nil, fmt.Errorf("schedlib: waiting for framework component handlers to sync: %w", err)
	}

	return &SchedulingSimulator{
		comps:           comps,
		informerFactory: informerFactory,
		informerCtx:     ctx,
	}, nil
}

// NewClusterState initializes a new runtime cluster state.
// It is not safe to call concurrently with other NewClusterState or NewClusterSnapshot calls.
func (s *SchedulingSimulator) NewClusterState(ctx context.Context) (*state.ClusterState, error) {
	snap := cache.NewEmptySnapshot()
	internalCache := cache.New(ctx, nil, utilfeature.DefaultFeatureGate.Enabled(features.GenericWorkload), utilfeature.DefaultFeatureGate.Enabled(features.CompositePodGroup))
	profiles, err := s.buildProfileMap(ctx, snap)
	if err != nil {
		return nil, err
	}

	return state.New(internalCache, profiles, snap), nil
}

// NewClusterSnapshot initializes a new snapshot with the provided pods, nodes, pod groups, and composite pod groups.
// It is not safe to call concurrently with other NewClusterState or NewClusterSnapshot calls.
func (s *SchedulingSimulator) NewClusterSnapshot(
	ctx context.Context,
	pods []*v1.Pod,
	nodes []*v1.Node,
	podGroups []*schedulingv1beta1.PodGroup,
	compositePodGroups []*schedulingv1alpha3.CompositePodGroup,
) (Simulator, error) {
	snap := cache.NewTestSnapshotWithCompositePodGroups(pods, nodes, podGroups, compositePodGroups)
	profiles, err := s.buildProfileMap(ctx, snap)
	if err != nil {
		return nil, err
	}

	return snapshot.New(snap, profiles), nil
}

func (s *SchedulingSimulator) buildProfileMap(ctx context.Context, snap *cache.Snapshot) (*upstreamsync.ProfileMap, error) {
	profiles, err := upstreamsync.NewFrameworkMap(ctx, s.comps, framework.DiscardRecorderFactory, snap)
	if err != nil {
		return nil, fmt.Errorf("schedlib: building scheduler: %w", err)
	}
	framework.ApplySimulationNeutralizers(profiles)

	// The informers the profiles registered belong to the simulator: a shared informer is started
	// once, and keeps the context of that first start for as long as it runs.
	s.informerFactory.StartWithContext(s.informerCtx)

	// Waiting for them is this call's business, but a shutting-down simulator leaves them unable
	// to sync, so the wait watches both contexts rather than only the caller's.
	waitCtx, stopWaiting := context.WithCancel(ctx)
	defer stopWaiting()
	stopWaitingOnShutdown := context.AfterFunc(s.informerCtx, stopWaiting)
	defer stopWaitingOnShutdown()

	if res := s.informerFactory.WaitForCacheSyncWithContext(waitCtx); res.Err != nil {
		if simErr := s.informerCtx.Err(); simErr != nil {
			return nil, fmt.Errorf("schedlib: the simulator's context is done: %w", simErr)
		}
		return nil, fmt.Errorf("schedlib: %w", res.AsError())
	}
	return profiles, nil
}

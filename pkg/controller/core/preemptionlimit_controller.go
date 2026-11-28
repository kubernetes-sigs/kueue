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

package core

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/limits"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/policy"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
)

// preemptionLimitPruneInterval is how often the expired preemption events of
// a PreemptionLimit are pruned.
const preemptionLimitPruneInterval = time.Minute

const preemptionLimitTopAggregatedCountsMaxItems = 100

// PreemptionLimitReconciler aligns the in-memory representation of the PreemptionLimits
// (see PreemptionLimitTracker) with their current specs. It runs on all replicas, so a
// replica that becomes the leader already tracks the PreemptionLimits, and only the leader
// reports the status.
type PreemptionLimitReconciler struct {
	client        client.Client
	tracker       *limits.PreemptionLimitTracker
	clock         clock.Clock
	roleTracker   *roletracker.RoleTracker
	manager       *qcache.Manager
	pruneInterval time.Duration
}

// PreemptionLimitReconcilerOption configures a PreemptionLimitReconciler.
type PreemptionLimitReconcilerOption func(*PreemptionLimitReconciler)

// WithPreemptionLimitPruneInterval overrides how often expired preemption events
// are pruned and status is updated. Must be positive.
func WithPreemptionLimitPruneInterval(interval time.Duration) PreemptionLimitReconcilerOption {
	return func(r *PreemptionLimitReconciler) {
		r.pruneInterval = interval
	}
}

// WithPreemptionLimitClock overrides the clock used by the reconciler. Must not be nil.
func WithPreemptionLimitClock(c clock.Clock) PreemptionLimitReconcilerOption {
	return func(r *PreemptionLimitReconciler) {
		r.clock = c
	}
}

var _ reconcile.Reconciler = (*PreemptionLimitReconciler)(nil)

func NewPreemptionLimitReconciler(
	client client.Client,
	tracker *limits.PreemptionLimitTracker,
	roleTracker *roletracker.RoleTracker,
	manager *qcache.Manager,
	opts ...PreemptionLimitReconcilerOption,
) *PreemptionLimitReconciler {
	r := &PreemptionLimitReconciler{
		client:        client,
		tracker:       tracker,
		clock:         realClock,
		roleTracker:   roleTracker,
		manager:       manager,
		pruneInterval: preemptionLimitPruneInterval,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=preemptionlimits,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=preemptionlimits/status,verbs=get;update;patch

// Reconcile keeps the in-memory state of the PreemptionLimit (see
// PreemptionLimitTracker) consistent with its current spec, and, on the leader,
// reports active counts in its status. Whenever the PreemptionLimit spec changes, and every
// preemptionLimitPruneInterval, it syncs the PreemptionLimit into the tracker:
//   - starts tracking a new PreemptionLimit, as preemptions are only recorded
//     for tracked ones;
//   - resets recorded events for a PreemptionLimit that was recreated or whose
//     aggregation or selectors changed, as past events no longer count toward it;
//   - otherwise keeps recorded events (e.g. on a limit or window change).
//
// It then prunes events that fell out of the window, and, on the leader, updates
// the status with the preemptions counted within the window. Once the PreemptionLimit
// is deleted, it forgets its tracked state and events.
//
// The status is a write to the API server that is shared by all replicas, and the
// preemption events are only recorded by the leader, so followers leave it to the leader.
func (r *PreemptionLimitReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var limit kueuealpha.PreemptionLimit
	if err := r.client.Get(ctx, req.NamespacedName, &limit); err != nil {
		if apierrors.IsNotFound(err) {
			// Preemptions picked before the deletion but recorded after it are
			// dropped by Record, as the PreemptionLimit is no longer tracked.
			removed := r.tracker.Forget(policy.PreemptionLimitReference(req.Name))

			if removed {
				ctrl.LoggerFrom(ctx).V(2).Info("PreemptionLimit is deleted, forgetting its preemption events")
				qcache.NotifyRetryInadmissible(r.manager, sets.New(r.manager.GetClusterQueueNames()...))
			}
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	now := r.clock.Now()
	if r.tracker.Sync(&limit) {
		qcache.NotifyRetryInadmissible(r.manager, sets.New(r.manager.GetClusterQueueNames()...))
	}
	r.tracker.Prune(now, policy.PreemptionLimitReference(limit.Name))

	if roletracker.GetRole(r.roleTracker) == roletracker.RoleFollower {
		// Requeue, so that the status is reported soon after this replica becomes the leader.
		return ctrl.Result{RequeueAfter: r.pruneInterval}, nil
	}

	limit.Status = r.preemptionLimitStatus(now, &limit)
	if err := r.client.Status().Update(ctx, &limit); err != nil {
		// "NotFound" signals that PreemptionLimit was deleted during processing
		// and deletion will be handled by a separate dedicated Reconcile which will
		// clean up in-memory state.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	return ctrl.Result{RequeueAfter: r.pruneInterval}, nil
}

func (r *PreemptionLimitReconciler) preemptionLimitStatus(now time.Time, limit *kueuealpha.PreemptionLimit) kueuealpha.PreemptionLimitStatus {
	counts := r.tracker.Counts(now, policy.PreemptionLimitReference(limit.Name))

	topCounts := make([]kueuealpha.PreemptionLimitTopAggregatedCount, 0, len(counts))
	for aggregationKey, count := range counts {
		topCounts = append(topCounts, kueuealpha.PreemptionLimitTopAggregatedCount{
			// No need to additionally validate key length because it is created from
			// existing validated values and maxLength is calculated as combination of
			// existing name length constraints.
			Key:   string(aggregationKey),
			Value: count,
		})
	}

	slices.SortFunc(topCounts, func(a, b kueuealpha.PreemptionLimitTopAggregatedCount) int {
		return cmp.Or(-cmp.Compare(a.Value, b.Value), cmp.Compare(a.Key, b.Key))
	})

	if len(topCounts) > preemptionLimitTopAggregatedCountsMaxItems {
		topCounts = topCounts[:preemptionLimitTopAggregatedCountsMaxItems]
	}

	return kueuealpha.PreemptionLimitStatus{
		Window: &kueuealpha.PreemptionLimitWindowStatus{
			LastUpdateTime:      metav1.NewTime(now),
			TopAggregatedCounts: topCounts,
		},
	}
}

// SetupWithManager sets up the controller with the Manager. The controller does not
// need the leader election, so the followers also keep the in-memory state of the
// PreemptionLimits, which the scheduler of a new leader relies on. See Reconcile for the
// part that is left to the leader.
func (r *PreemptionLimitReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.pruneInterval <= 0 {
		return errors.New("PreemptionLimit prune interval must be positive")
	}
	if r.clock == nil {
		return errors.New("PreemptionLimit clock must not be nil")
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("preemptionlimit_controller").
		// Make sure that update to the status part aren't triggering additional Reconcile.
		For(&kueuealpha.PreemptionLimit{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		WithOptions(controller.Options{
			NeedLeaderElection: new(false),
			LogConstructor:     roletracker.NewLogConstructor(r.roleTracker, "preemptionlimit-reconciler"),
		}).
		Complete(r)
}

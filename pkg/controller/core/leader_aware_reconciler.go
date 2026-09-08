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
	"context"
	"time"

	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
)

type ReconcilerWithFollowerObserver interface {
	Reconcile(ctx context.Context, req reconcile.Request, cl client.Client) (reconcile.Result, error)
	Observe(ctx context.Context, req reconcile.Request, cl client.Client) (reconcile.Result, error)
}

type leaderAwareReconcilerObserver struct {
	elected         <-chan struct{}
	delegate        ReconcilerWithFollowerObserver
	leaderClient    client.Client // full read-write client
	followerClient  client.Client // read-only client
	requeueDuration time.Duration
}

func (r *leaderAwareReconcilerObserver) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	select {
	case <-r.elected:
		return r.delegate.Reconcile(ctx, req, r.leaderClient)
	default:
		observeResult, err := r.delegate.Observe(ctx, req, r.followerClient)
		if err != nil {
			return observeResult, err
		}
		// Always schedule another pass after at most requeueDuration
		// so no events are missed during leadership failover
		//nolint:staticcheck // Requeue is deprecated in controller-runtime but still used for immediate retries
		if observeResult.Requeue || (observeResult.RequeueAfter > 0 && observeResult.RequeueAfter < r.requeueDuration) {
			return observeResult, nil
		}
		// Otherwise, schedule the default failover safety pass (requeueDuration).
		return ctrl.Result{RequeueAfter: r.requeueDuration}, nil
	}
}

func WithLeadingManagerAndObserver(mgr ctrl.Manager, reconciler ReconcilerWithFollowerObserver, cfg *config.Configuration) reconcile.Reconciler {
	alreadyElected := make(chan struct{})
	close(alreadyElected)
	elected := (<-chan struct{})(alreadyElected)
	var requeueDuration time.Duration

	if cfg != nil && cfg.LeaderElection != nil && ptr.Deref(cfg.LeaderElection.LeaderElect, false) {
		elected = mgr.Elected()
		requeueDuration = cfg.LeaderElection.LeaseDuration.Duration
	}

	fullClient := mgr.GetClient()
	readOnlyClient := clientutil.NewReadOnlyClient(fullClient)

	return &leaderAwareReconcilerObserver{
		elected:         elected,
		delegate:        reconciler,
		leaderClient:    fullClient,
		followerClient:  readOnlyClient,
		requeueDuration: requeueDuration,
	}
}

// WithLeadingManager returns a decorating reconcile.Reconciler that discards reconciliation requests
// for the controllers that are started with the controller.Options.NeedLeaderElection
// option set to false in non-leading replicas.
//
// Starting controllers in non-leading replicas is needed for these that update the data
// served by the visibility extension API server.
//
// This enables to:
//   - Keep the scheduling decisions under the responsibility of the leading replica alone,
//     to prevent any concurrency issues.
//   - Consume requests from the watch event queues, to prevent them from growing indefinitely
//     in the non-leading replicas.
//   - Transition to actually reconciling requests in the replica that may acquire
//     the leader election lease, in case the previously leading replica failed to renew it.
func WithLeadingManager(mgr ctrl.Manager, reconciler reconcile.Reconciler, obj client.Object, cfg *config.Configuration) reconcile.Reconciler {
	// Do not decorate the reconciler if leader election is disabled
	if cfg.LeaderElection == nil || !ptr.Deref(cfg.LeaderElection.LeaderElect, false) {
		return reconciler
	}

	return &leaderAwareReconciler{
		elected:         mgr.Elected(),
		client:          mgr.GetClient(),
		delegate:        reconciler,
		objectPrototype: obj,
		requeueDuration: cfg.LeaderElection.LeaseDuration.Duration,
	}
}

type leaderAwareReconciler struct {
	elected         <-chan struct{}
	client          client.Client
	delegate        reconcile.Reconciler
	objectPrototype client.Object
	// the duration used by non-leading replicas to requeue events,
	// so no events are missed over the period it takes for
	// leader election to fail over a new replica.
	requeueDuration time.Duration
}

var _ reconcile.Reconciler = (*leaderAwareReconciler)(nil)

func (r *leaderAwareReconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	select {
	case <-r.elected:
		// The manager has been elected leader, delegate reconciliation to the provided reconciler.
		return r.delegate.Reconcile(ctx, request)
	default:
		// The client writes what it read into the object it is given, and the decorated controllers
		// run with more than one worker, so the prototype is copied rather than reused.
		object := r.objectPrototype.DeepCopyObject().(client.Object)
		if err := r.client.Get(ctx, request.NamespacedName, object); err != nil {
			// Discard request if not found, to prevent from re-enqueueing indefinitely.
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
		// The manager hasn't been elected leader yet, requeue the reconciliation request
		// to prevent against any missed / discarded events over the period it takes
		// to fail over a new leading replica, which can take as much as the configured
		// lease duration, for it to acquire leadership.
		return ctrl.Result{RequeueAfter: r.requeueDuration}, nil
	}
}

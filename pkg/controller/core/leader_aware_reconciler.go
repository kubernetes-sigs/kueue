/*
Copyright 2024 The Kubernetes Authors.

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

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
)

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
		object:          obj,
		requeueDuration: cfg.LeaderElection.LeaseDuration.Duration,
	}
}

type leaderAwareReconciler struct {
	elected  <-chan struct{}
	client   client.Client
	delegate reconcile.Reconciler
	object   client.Object
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
		if err := r.client.Get(ctx, request.NamespacedName, r.object); err != nil {
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

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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
)

// defaultRequeueDuration defaults the duration used by non-leading replicas
// to requeue events, so no events are missed over the period it takes for
// leader election to fail over a new replica.
const defaultRequeueDuration = 15 * time.Second

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
func WithLeadingManager(mgr ctrl.Manager, reconciler reconcile.Reconciler, cfg *config.Configuration) reconcile.Reconciler {
	// Default to the recommended lease duration, that's used for core components
	requeueDuration := defaultRequeueDuration
	// Otherwise used the configured lease duration for the manager
	if le := cfg.LeaderElection; le != nil {
		zero := metav1.Duration{}
		if duration := le.LeaseDuration; duration != zero {
			requeueDuration = duration.Duration
		}
	}

	return &leaderAwareReconciler{
		elected:         mgr.Elected(),
		delegate:        reconciler,
		requeueDuration: requeueDuration,
	}
}

type leaderAwareReconciler struct {
	elected         <-chan struct{}
	delegate        reconcile.Reconciler
	requeueDuration time.Duration
}

var _ reconcile.Reconciler = (*leaderAwareReconciler)(nil)

func (r *leaderAwareReconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	select {
	case <-r.elected:
		// The manager has been elected leader, delegate reconciliation to the provided reconciler.
		return r.delegate.Reconcile(ctx, request)
	default:
		// The manager hasn't been elected leader yet, requeue the reconciliation request
		// to prevent against any missed / discarded events over the period it takes
		// to fail over a new leading replica, which can take as much as the configured
		// lease duration, for it to acquire leadership.
		return ctrl.Result{RequeueAfter: r.requeueDuration}, nil
	}
}

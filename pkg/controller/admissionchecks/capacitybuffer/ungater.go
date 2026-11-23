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

package capacitybuffer

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	utilclient "sigs.k8s.io/kueue/pkg/util/client"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
)

// Ungater removes CapacityBufferSchedulingGate from Pods when the CapacityBuffer API is
// not served, so the CapacityBuffer controller cannot run to ungate them.
type Ungater struct {
	client      client.Client
	roleTracker *roletracker.RoleTracker
}

var _ reconcile.Reconciler = (*Ungater)(nil)

func NewUngater(c client.Client, roleTracker *roletracker.RoleTracker) *Ungater {
	return &Ungater{client: c, roleTracker: roleTracker}
}

func (r *Ungater) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	pod := &corev1.Pod{}
	if err := r.client.Get(ctx, req.NamespacedName, pod); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}
	var ungated bool
	err := utilclient.Patch(ctx, r.client, pod, func() (bool, error) {
		ungated = utilpod.Ungate(pod, kueuealpha.CapacityBufferSchedulingGate)
		return ungated, nil
	})
	if err == nil && ungated {
		ctrl.LoggerFrom(ctx).V(3).Info("Removed the CapacityBuffer scheduling gate")
	}
	return reconcile.Result{}, client.IgnoreNotFound(err)
}

func (r *Ungater) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("capacitybuffer_ungater").
		For(&corev1.Pod{}, builder.WithPredicates(predicate.NewPredicateFuncs(hasCapacityBufferGate))).
		WithOptions(controller.Options{
			LogConstructor: roletracker.NewLogConstructor(r.roleTracker, "capacitybuffer-ungater"),
		}).
		Complete(r)
}

func hasCapacityBufferGate(obj client.Object) bool {
	pod, isPod := obj.(*corev1.Pod)
	return isPod && utilpod.HasGate(pod, kueuealpha.CapacityBufferSchedulingGate)
}

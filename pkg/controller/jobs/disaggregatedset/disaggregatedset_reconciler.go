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

package disaggregatedset

import (
	"context"

	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	disaggregatedsetv1 "sigs.k8s.io/lws/api/disaggregatedset/v1"

	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
)

const controllerName = "disaggregatedset"

// TODO(#11952): create one Workload per slice and manage the slice Pods as
// described in keps/11952-disaggregatedset-integration.
type Reconciler struct {
	roleTracker *roletracker.RoleTracker
}

func NewReconciler(_ context.Context, _ client.Client, _ client.FieldIndexer, _ events.EventRecorder, opts ...jobframework.Option) (jobframework.JobReconcilerInterface, error) {
	options := jobframework.ProcessOptions(opts...)

	return &Reconciler{
		roleTracker: options.RoleTracker,
	}, nil
}

var _ jobframework.JobReconcilerInterface = (*Reconciler)(nil)

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctrl.Log.V(3).Info("Setting up DisaggregatedSet reconciler")

	return ctrl.NewControllerManagedBy(mgr).
		For(&disaggregatedsetv1.DisaggregatedSet{}).
		Named(controllerName).
		WithOptions(controller.Options{
			LogConstructor: roletracker.NewLogConstructor(r.roleTracker, "disaggregatedset-reconciler"),
		}).
		Complete(r)
}

// +kubebuilder:rbac:groups=disaggregatedset.x-k8s.io,resources=disaggregatedsets,verbs=get;list;watch

func (r *Reconciler) Reconcile(ctx context.Context, _ reconcile.Request) (reconcile.Result, error) {
	ctrl.LoggerFrom(ctx).V(4).Info("Reconcile DisaggregatedSet")
	return reconcile.Result{}, nil
}

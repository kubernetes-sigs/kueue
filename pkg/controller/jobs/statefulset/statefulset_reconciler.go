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

package statefulset

import (
	"context"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
)

// +kubebuilder:rbac:groups="apps",resources=statefulsets,verbs=get;list;watch

var (
	_ jobframework.JobReconcilerInterface = (*Reconciler)(nil)
)

type Reconciler struct {
	client                       client.Client
	logName                      string
	manageJobsWithoutQueueName   bool
	managedJobsNamespaceSelector labels.Selector
	roleTracker                  *roletracker.RoleTracker
}

const controllerName = "statefulset"

func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.V(2).Info("Reconcile StatefulSet")

	sts := &appsv1.StatefulSet{}
	err := r.client.Get(ctx, req.NamespacedName, sts)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	return ctrl.Result{}, r.reconcileWorkload(ctx, sts)
}

func (r *Reconciler) reconcileWorkload(ctx context.Context, sts *appsv1.StatefulSet) error {
	workloadName := GetWorkloadName(GetOwnerUID(sts), sts.Name)
	wl := &kueue.Workload{}
	err := r.client.Get(ctx, client.ObjectKey{Namespace: sts.Namespace, Name: workloadName}, wl)
	if err != nil {
		return client.IgnoreNotFound(err)
	}

	hasOwnerReference, err := controllerutil.HasOwnerReference(wl.OwnerReferences, sts, r.client.Scheme())
	if err != nil {
		return err
	}

	var (
		shouldUpdate = false
		replicas     = ptr.Deref(sts.Spec.Replicas, 1)
	)

	switch {
	case hasOwnerReference && replicas == 0:
		shouldUpdate = true
		err = controllerutil.RemoveOwnerReference(sts, wl, r.client.Scheme())
	case !hasOwnerReference && replicas > 0:
		shouldUpdate = true
		err = controllerutil.SetOwnerReference(sts, wl, r.client.Scheme())
	}
	if err != nil || !shouldUpdate {
		return err
	}

	return r.client.Update(ctx, wl)
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctrl.Log.V(3).Info("Setting up StatefulSet reconciler")
	return ctrl.NewControllerManagedBy(mgr).
		For(&appsv1.StatefulSet{}).
		WithEventFilter(r).
		WithOptions(controller.Options{
			LogConstructor: roletracker.NewLogConstructor(r.roleTracker, controllerName),
		}).
		Complete(r)
}

func NewReconciler(_ context.Context, client client.Client, _ client.FieldIndexer, _ record.EventRecorder, opts ...jobframework.Option) (jobframework.JobReconcilerInterface, error) {
	options := jobframework.ProcessOptions(opts...)

	return &Reconciler{
		client:                       client,
		logName:                      "statefulset-reconciler",
		manageJobsWithoutQueueName:   options.ManageJobsWithoutQueueName,
		managedJobsNamespaceSelector: options.ManagedJobsNamespaceSelector,
		roleTracker:                  options.RoleTracker,
	}, nil
}

func (r *Reconciler) logger() logr.Logger {
	return roletracker.WithReplicaRole(ctrl.Log.WithName(r.logName), r.roleTracker)
}

var _ predicate.Predicate = (*Reconciler)(nil)

func (r *Reconciler) Generic(event.GenericEvent) bool {
	return false
}

func (r *Reconciler) Create(e event.CreateEvent) bool {
	return r.handle(e.Object)
}

func (r *Reconciler) Update(e event.UpdateEvent) bool {
	return r.handle(e.ObjectNew)
}

func (r *Reconciler) Delete(e event.DeleteEvent) bool {
	return r.handle(e.Object)
}

func (r *Reconciler) handle(obj client.Object) bool {
	sts, isSts := obj.(*appsv1.StatefulSet)
	if !isSts {
		return false
	}

	ctx := context.Background()
	log := r.logger().WithValues("statefulset", klog.KObj(sts))
	ctrl.LoggerInto(ctx, log)

	if frameworkName, managed := managedByAnotherFramework(sts); managed {
		log.V(3).Info("Skipping reconciliation because the object is managed by another framework", "framework", frameworkName)
		return false
	}

	suspend, err := jobframework.WorkloadShouldBeSuspended(ctx, sts, r.client, r.manageJobsWithoutQueueName, r.managedJobsNamespaceSelector)
	if err != nil {
		log.Error(err, "Failed to determine if the StatefulSet should be managed by Kueue")
	}

	return suspend
}

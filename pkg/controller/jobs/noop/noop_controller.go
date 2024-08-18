package noop

import (
	"context"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"sigs.k8s.io/kueue/pkg/controller/jobframework"
)

var (
	_ jobframework.JobReconcilerInterface = (*Reconciler)(nil)
)

type Reconciler struct {
	gvk schema.GroupVersionKind
}

func (r Reconciler) Reconcile(_ context.Context, _ reconcile.Request) (reconcile.Result, error) {
	return ctrl.Result{}, nil
}

func (r Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctrl.Log.V(3).Info("Skipped reconciler setup", "gvk", r.gvk)
	return nil
}

func NewReconcilerFactory(gvk schema.GroupVersionKind) jobframework.ReconcilerFactory {
	return func(client client.Client, record record.EventRecorder, opts ...jobframework.Option) jobframework.JobReconcilerInterface {
		return &Reconciler{gvk: gvk}
	}
}

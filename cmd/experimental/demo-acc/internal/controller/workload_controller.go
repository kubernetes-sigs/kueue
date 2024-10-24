/*
Copyright 2024.

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

package controller

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	kueueapi "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	"sigs.k8s.io/kueue/pkg/workload"
)

// The demo-acc will block the workload's admission until
// MakeReadyAfter time has passed from it's creation.
const (
	MakeReadyAfter = time.Minute
	ReadyMessage   = "The workload is now ready"
)

// WorkloadReconciler reconciles a Workload object
type WorkloadReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Workload object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.19.0/pkg/reconcile
func (r *WorkloadReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var wl kueueapi.Workload
	err := r.Get(ctx, req.NamespacedName, &wl)
	if err != nil {
		// Ignore not found
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Ignore the workloads that don't have quota reservation or are finished
	if !workload.HasQuotaReservation(&wl) || workload.IsFinished(&wl) {
		return ctrl.Result{}, nil
	}

	// Get the states managed by demo-acc
	managedStatesNames, err := admissioncheck.FilterForController(ctx, r.Client, wl.Status.AdmissionChecks, DemoACCName)

	// Ignore if none are managed by demo-acc
	if len(managedStatesNames) == 0 {
		return ctrl.Result{}, nil
	}

	log.V(2).Info("Reconcile Workload")

	//If we need to wait
	if remaining := time.Until(wl.CreationTimestamp.Add(MakeReadyAfter)); remaining > 0 {
		return ctrl.Result{RequeueAfter: remaining}, nil
	}

	// Mark the states 'Ready' if not done already
	needsUpdate := false
	wlPatch := workload.BaseSSAWorkload(&wl)
	for _, name := range managedStatesNames {
		if acs := workload.FindAdmissionCheck(wl.Status.AdmissionChecks, name); acs.State != kueueapi.CheckStateReady {
			workload.SetAdmissionCheckState(&wlPatch.Status.AdmissionChecks, kueueapi.AdmissionCheckState{
				Name:    name,
				State:   kueueapi.CheckStateReady,
				Message: ReadyMessage,
			})
			needsUpdate = true
		} else {
			workload.SetAdmissionCheckState(&wlPatch.Status.AdmissionChecks, *acs)
		}
	}
	if needsUpdate {
		return ctrl.Result{}, r.Status().Patch(ctx, wlPatch, client.Apply, client.FieldOwner(DemoACCName), client.ForceOwnership)
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkloadReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kueueapi.Workload{}).
		Complete(r)
}

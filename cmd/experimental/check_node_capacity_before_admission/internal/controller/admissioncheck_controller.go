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

package controller

import (
	"context"
	"fmt"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kueueapi "sigs.k8s.io/kueue/apis/kueue/v1beta1"
)

// Define the controller's name
const (
	ACCName = "experimental.kueue.x-k8s.io/check-node-capacity-before-admission"
)

// AdmissionCheckReconciler reconciles a AdmissionCheck object
type AdmissionCheckReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=admissionchecks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=admissionchecks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=admissionchecks/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the AdmissionCheck object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.19.1/pkg/reconcile
func (r *AdmissionCheckReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var ac kueueapi.AdmissionCheck
	err := r.Get(ctx, req.NamespacedName, &ac)
	if err != nil {
		// Ignore not found
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Ignore if not managed by check-node-capacity-before-admission
	if ac.Spec.ControllerName != ACCName {
		return ctrl.Result{}, nil
	}

	// For now, we only want to set it active if not already done
	log.V(2).Info("Reconcile AdmissionCheck")
	if !apimeta.IsStatusConditionTrue(ac.Status.Conditions, kueueapi.AdmissionCheckActive) {
		apimeta.SetStatusCondition(&ac.Status.Conditions, metav1.Condition{
			Type:               kueueapi.AdmissionCheckActive,
			Status:             metav1.ConditionTrue,
			Reason:             "Active",
			Message:            fmt.Sprintf("%s is running", ACCName),
			ObservedGeneration: ac.Generation,
		})
		log.V(2).Info("Update Active condition")
		return ctrl.Result{}, r.Status().Update(ctx, &ac)
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *AdmissionCheckReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		// Uncomment the following line adding a pointer to an instance of the controlled resource as an argument
		// For().
		For(&kueueapi.AdmissionCheck{}).
		// Named("admissioncheck").
		Complete(r)
}

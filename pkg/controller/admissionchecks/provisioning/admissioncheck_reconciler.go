/*
Copyright 2023 The Kubernetes Authors.

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

package provisioning

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
)

type acReconciler struct {
	client client.Client
	helper *storeHelper
	record record.EventRecorder
}

var _ reconcile.Reconciler = (*acReconciler)(nil)

func (a *acReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	ac := &kueue.AdmissionCheck{}
	if err := a.client.Get(ctx, req.NamespacedName, ac); err != nil || ac.Spec.ControllerName != ControllerName {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	currentCondition := ptr.Deref(apimeta.FindStatusCondition(ac.Status.Conditions, kueue.AdmissionCheckActive), metav1.Condition{})
	newCondition := metav1.Condition{
		Type:    kueue.AdmissionCheckActive,
		Status:  metav1.ConditionTrue,
		Reason:  "Active",
		Message: "The admission check is active",
	}

	if !parametersRefValid(ac.Spec.Parameters) {
		newCondition.Status = metav1.ConditionFalse
		newCondition.Reason = "BadParametersRef"
		newCondition.Message = "Unexpected parameters reference"
	} else if _, err := a.helper.ProvReqConfig(ctx, ac.Spec.Parameters.Name); err != nil {
		newCondition.Status = metav1.ConditionFalse
		newCondition.Reason = "UnknownParametersRef"
		newCondition.Message = err.Error()
	}

	if currentCondition.Status != newCondition.Status {
		apimeta.SetStatusCondition(&ac.Status.Conditions, newCondition)
		a.record.Eventf(ac, corev1.EventTypeNormal, "UpdatedAdmissionChecks", "Admission checks status is changed to %s", newCondition.Status)
		return reconcile.Result{}, a.client.Status().Update(ctx, ac)
	}
	return reconcile.Result{}, nil
}

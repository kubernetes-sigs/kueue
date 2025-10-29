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

package failurerecovery

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// +kubebuilder:rbac:groups="",resources=pods,verbs=get;watch
// +kubebuilder:rbac:groups="",resources=pods/status,verbs=update;patch

var (
	realClock                         = clock.RealClock{}
	forcefulPodTerminationGracePeriod = time.Minute
)

type TerminatingPodReconciler struct {
	client client.Client
	clock  clock.Clock
}

func NewTerminatingPodReconciler(
	client client.Client,
) *TerminatingPodReconciler {
	return &TerminatingPodReconciler{
		client: client,
		clock:  realClock,
	}
}

func (r *TerminatingPodReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	pod := &corev1.Pod{}

	if err := r.client.Get(ctx, req.NamespacedName, pod); client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, err
	}

	if pod.DeletionTimestamp == nil || pod.DeletionGracePeriodSeconds == nil {
		return ctrl.Result{}, nil
	}

	if pod.Status.Phase != corev1.PodRunning && pod.Status.Phase != corev1.PodPending {
		return ctrl.Result{}, nil
	}

	now := r.clock.Now()
	gracefulTerminationPeriod := time.Duration(*pod.DeletionGracePeriodSeconds) * time.Second
	totalGracePeriod := gracefulTerminationPeriod + forcefulPodTerminationGracePeriod
	if now.Before(pod.DeletionTimestamp.Add(totalGracePeriod)) {
		gracePeriodLeft := pod.DeletionTimestamp.Add(totalGracePeriod).Sub(now)
		return ctrl.Result{RequeueAfter: gracePeriodLeft}, nil
	}

	pod.Status.Phase = corev1.PodFailed
	if err := r.client.Status().Update(ctx, pod); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *TerminatingPodReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		Complete(r)
}

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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	utilnode "sigs.k8s.io/kueue/pkg/util/node"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
)

// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods/status,verbs=get;patch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch

var (
	realClock = clock.RealClock{}

	// TODO: Move to API.
	safeToForcefullyTerminateAnnotationName  = "kueue.x-k8s.io/safe-to-forcefully-terminate"
	safeToForcefullyTerminateAnnotationValue = "true"
)

type TerminatingPodReconciler struct {
	client                         client.Client
	clock                          clock.Clock
	forcefulTerminationGracePeriod time.Duration
}

type TerminatingPodReconcilerOptions struct {
	clock                          clock.Clock
	forcefulTerminationGracePeriod time.Duration
}

type TerminatingPodReconcilerOption func(*TerminatingPodReconcilerOptions)

func WithClock(c clock.Clock) TerminatingPodReconcilerOption {
	return func(o *TerminatingPodReconcilerOptions) {
		o.clock = c
	}
}

func WithForcefulTerminationGracePeriod(t time.Duration) TerminatingPodReconcilerOption {
	return func(o *TerminatingPodReconcilerOptions) {
		o.forcefulTerminationGracePeriod = t
	}
}

var defaultOptions = TerminatingPodReconcilerOptions{
	clock:                          realClock,
	forcefulTerminationGracePeriod: time.Minute,
}

func NewTerminatingPodReconciler(
	client client.Client,
	opts ...TerminatingPodReconcilerOption,
) (*TerminatingPodReconciler, error) {
	options := defaultOptions
	for _, opt := range opts {
		opt(&options)
	}

	return &TerminatingPodReconciler{
		client: client,
		clock:  options.clock,
	}, nil
}

func (r *TerminatingPodReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	pod := &corev1.Pod{}
	if err := r.client.Get(ctx, req.NamespacedName, pod); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Pod did not opt-in to be forcefully terminated
	annotationValue, hasAnnotation := pod.Annotations[safeToForcefullyTerminateAnnotationName]
	if !hasAnnotation || annotationValue != safeToForcefullyTerminateAnnotationValue {
		return ctrl.Result{}, nil
	}

	// Pod was not marked for termination
	if pod.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	// Pod is not in a running phase
	if utilpod.IsTerminated(pod) {
		return ctrl.Result{}, nil
	}

	node := &corev1.Node{}
	nodeKey := types.NamespacedName{Name: pod.Spec.NodeName}
	if err := r.client.Get(ctx, nodeKey, node); err != nil {
		return ctrl.Result{}, err
	}
	// Pod is not scheduled on an unreachable node
	if !utilnode.HasTaint(node, corev1.TaintNodeUnreachable) {
		return ctrl.Result{}, nil
	}

	now := r.clock.Now()
	gracefulTerminationPeriod := time.Duration(ptr.Deref(pod.DeletionGracePeriodSeconds, 0)) * time.Second
	totalGracePeriod := gracefulTerminationPeriod + r.forcefulTerminationGracePeriod
	if now.Before(pod.DeletionTimestamp.Add(totalGracePeriod)) {
		remainingTime := pod.DeletionTimestamp.Add(totalGracePeriod).Sub(now)
		return ctrl.Result{RequeueAfter: remainingTime}, nil
	}

	podPatch := pod.DeepCopy()
	podPatch.Status.Phase = corev1.PodFailed
	if err := r.client.Status().Patch(ctx, podPatch, client.MergeFrom(pod)); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *TerminatingPodReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		Complete(r)
}

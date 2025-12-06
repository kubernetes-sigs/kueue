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
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/core"
	utilclient "sigs.k8s.io/kueue/pkg/util/client"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utiltaints "sigs.k8s.io/kueue/pkg/util/taints"
)

// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods/status,verbs=get;patch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch

var realClock = clock.RealClock{}

const (
	KueueFailureRecoveryConditionType = "KueueFailureRecovery"
	KueueForcefulTerminationReason    = "KueueForcefullyTerminated"
)

type TerminatingPodReconciler struct {
	client                         client.Client
	clock                          clock.Clock
	forcefulTerminationGracePeriod time.Duration
	recorder                       record.EventRecorder
	roleTracker                    *roletracker.RoleTracker
}

type TerminatingPodReconcilerOptions struct {
	clock                          clock.Clock
	forcefulTerminationGracePeriod time.Duration
	roleTracker                    *roletracker.RoleTracker
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

// WithRoleTracker sets the roleTracker for HA logging.
func WithRoleTracker(tracker *roletracker.RoleTracker) TerminatingPodReconcilerOption {
	return func(o *TerminatingPodReconcilerOptions) {
		o.roleTracker = tracker
	}
}

var defaultOptions = TerminatingPodReconcilerOptions{
	clock:                          realClock,
	forcefulTerminationGracePeriod: time.Minute,
}

func NewTerminatingPodReconciler(
	client client.Client,
	recorder record.EventRecorder,
	opts ...TerminatingPodReconcilerOption,
) *TerminatingPodReconciler {
	options := defaultOptions
	for _, opt := range opts {
		opt(&options)
	}

	return &TerminatingPodReconciler{
		client:                         client,
		clock:                          options.clock,
		forcefulTerminationGracePeriod: options.forcefulTerminationGracePeriod,
		recorder:                       recorder,
		roleTracker:                    options.roleTracker,
	}
}

func (r *TerminatingPodReconciler) Generic(event.TypedGenericEvent[*corev1.Pod]) bool {
	return false
}

func (r *TerminatingPodReconciler) Create(e event.TypedCreateEvent[*corev1.Pod]) bool {
	return podEligibleForTermination(e.Object)
}

func (r *TerminatingPodReconciler) Update(u event.TypedUpdateEvent[*corev1.Pod]) bool {
	if !podEligibleForTermination(u.ObjectNew) {
		return false
	}

	// Pod was not marked for deletion in the update
	if !u.ObjectOld.DeletionTimestamp.IsZero() || u.ObjectNew.DeletionTimestamp.IsZero() {
		return false
	}

	return true
}

func (r *TerminatingPodReconciler) Delete(event.TypedDeleteEvent[*corev1.Pod]) bool {
	return false
}

func (r *TerminatingPodReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	pod := &corev1.Pod{}
	if err := r.client.Get(ctx, req.NamespacedName, pod); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Pod was updated in the meantime and should not be forcefully terminated
	if !podEligibleForTermination(pod) {
		return ctrl.Result{}, nil
	}

	// Forceful termination threshold not reached
	now := r.clock.Now()
	forcefulTerminationThreshold := pod.DeletionTimestamp.Add(r.forcefulTerminationGracePeriod)
	if now.Before(forcefulTerminationThreshold) {
		remainingTime := forcefulTerminationThreshold.Sub(now)
		return ctrl.Result{RequeueAfter: remainingTime}, nil
	}

	node := &corev1.Node{}
	nodeKey := types.NamespacedName{Name: pod.Spec.NodeName}
	if err := r.client.Get(ctx, nodeKey, node); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	// Pod is not scheduled on an unreachable node
	if !utiltaints.TaintKeyExists(node.Spec.Taints, corev1.TaintNodeUnreachable) {
		return ctrl.Result{}, nil
	}

	totalDeletionGracePeriod := time.Duration(ptr.Deref(pod.DeletionGracePeriodSeconds, 0)) + r.forcefulTerminationGracePeriod
	eventMessage := fmt.Sprintf(
		"Pod forcefully terminated after %s grace period due to unreachable node `%s` (triggered by `%s` annotation)",
		totalDeletionGracePeriod,
		node.Name,
		constants.SafeToForcefullyTerminateAnnotationKey,
	)

	err := utilclient.PatchStatus(ctx, r.client, pod, func() (bool, error) {
		pod.Status.Phase = corev1.PodFailed
		pod.Status.Conditions = append(pod.Status.Conditions, corev1.PodCondition{
			Type:    KueueFailureRecoveryConditionType,
			Status:  corev1.ConditionTrue,
			Reason:  KueueForcefulTerminationReason,
			Message: eventMessage,
		})
		return true, nil
	})

	if err != nil {
		return ctrl.Result{}, err
	}

	r.recorder.Event(pod, corev1.EventTypeWarning, KueueForcefulTerminationReason, eventMessage)

	return ctrl.Result{}, nil
}

func podEligibleForTermination(p *corev1.Pod) bool {
	annotationValue, hasAnnotation := p.Annotations[constants.SafeToForcefullyTerminateAnnotationKey]
	if !hasAnnotation || annotationValue != constants.SafeToForcefullyTerminateAnnotationValue {
		return false
	}

	if p.DeletionTimestamp.IsZero() {
		return false
	}

	if utilpod.IsTerminated(p) {
		return false
	}

	return true
}

const ControllerName = "failure-recovery-pod-termination-controller"

func (r *TerminatingPodReconciler) SetupWithManager(mgr ctrl.Manager, cfg *configapi.Configuration) (string, error) {
	return ControllerName, ctrl.NewControllerManagedBy(mgr).
		Named("pod_termination_controller").
		WatchesRawSource(source.TypedKind(
			mgr.GetCache(),
			&corev1.Pod{},
			&handler.TypedEnqueueRequestForObject[*corev1.Pod]{},
			r,
		)).
		WithOptions(controller.Options{
			NeedLeaderElection:      ptr.To(false),
			MaxConcurrentReconciles: mgr.GetControllerOptions().GroupKindConcurrency[kueue.GroupVersion.WithKind("Pod").GroupKind().String()],
		}).
		WithLogConstructor(roletracker.NewLogConstructor(r.roleTracker, ControllerName)).
		Complete(core.WithLeadingManager(mgr, r, &corev1.Pod{}, cfg))
}

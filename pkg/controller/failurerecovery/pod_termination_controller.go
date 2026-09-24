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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/source"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	utilclient "sigs.k8s.io/kueue/pkg/util/client"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utiltaints "sigs.k8s.io/kueue/pkg/util/taints"
)

// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups="",resources=pods/status,verbs=get;patch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch

var realClock = clock.RealClock{}

const (
	KueueFailureRecoveryConditionType = "KueueFailureRecovery"
	KueueForcefulTerminationReason    = "KueueForcefullyDeleted"
)

type TerminatingPodReconciler struct {
	client                         client.Client
	clock                          clock.Clock
	forcefulTerminationGracePeriod time.Duration
	recorder                       events.EventRecorder
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
	recorder events.EventRecorder,
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
	return !podEligibleForTermination(u.ObjectOld) && podEligibleForTermination(u.ObjectNew)
}

func (r *TerminatingPodReconciler) Delete(event.TypedDeleteEvent[*corev1.Pod]) bool {
	return false
}

func (r *TerminatingPodReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	pod := &corev1.Pod{}
	if err := r.client.Get(ctx, req.NamespacedName, pod); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Pod was updated in the meantime and should not be forcefully terminated
	if !podEligibleForTermination(pod) {
		log.V(4).Info("Terminating pod changed and is not eligible for forceful termination anymore")
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
		log.V(4).Info("Forceful termination threshold reached, but pod is not scheduled on an unreachable node", "node", klog.KObj(node))
		return ctrl.Result{}, nil
	}

	totalDeletionGracePeriod := time.Duration(ptr.Deref(pod.DeletionGracePeriodSeconds, 0)) + r.forcefulTerminationGracePeriod
	eventMessage := fmt.Sprintf(
		"Pod forcefully terminated after %s grace period due to unreachable node `%s` (triggered by `%s` annotation)",
		totalDeletionGracePeriod,
		node.Name,
		constants.SafeToForcefullyDeleteAnnotationKey,
	)

	recoveryCondition := corev1.PodCondition{
		Type:    KueueFailureRecoveryConditionType,
		Status:  corev1.ConditionTrue,
		Reason:  KueueForcefulTerminationReason,
		Message: eventMessage,
	}

	conditionChanged := false
	err := utilclient.PatchStatus(ctx, r.client, pod, func() (bool, error) {
		updated := false
		if !utilpod.IsTerminated(pod) {
			pod.Status.Phase = corev1.PodFailed
			updated = true
		}
		// Reports false when the condition is already up to date, so the event is not emitted again.
		conditionChanged = updatePodCondition(&pod.Status, &recoveryCondition)
		return updated || conditionChanged, nil
	})
	if err != nil {
		return ctrl.Result{}, err
	}

	if conditionChanged {
		r.recorder.Eventf(pod, nil, corev1.EventTypeWarning, KueueForcefulTerminationReason, "ForcefulTermination", "%s", eventMessage)
	}
	log.V(4).Info("Forcefully terminating pod", "pod", klog.KObj(pod), "message", eventMessage)

	// Forcefully delete the pod object
	if err = r.client.Delete(ctx, pod, &client.DeleteOptions{GracePeriodSeconds: new(int64(0))}); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	return ctrl.Result{}, nil
}

func podEligibleForTermination(p *corev1.Pod) bool {
	annotationValue, hasAnnotation := p.Annotations[constants.SafeToForcefullyDeleteAnnotationKey]
	if !hasAnnotation || annotationValue != constants.SafeToForcefullyDeleteAnnotationValue {
		return false
	}

	return !p.DeletionTimestamp.IsZero()
}

func (r *TerminatingPodReconciler) mapNodeToPods(ctx context.Context, node *corev1.Node) []ctrl.Request {
	log := log.FromContext(ctx)

	pods := &corev1.PodList{}
	if err := r.client.List(ctx, pods, client.MatchingFields{indexer.PodNodeNameKey: node.Name}); err != nil {
		log.Error(err, "Failed to list pods for node", "node", klog.KObj(node))
		return nil
	}

	var requests []ctrl.Request
	for _, pod := range pods.Items {
		if podEligibleForTermination(&pod) {
			requests = append(requests, ctrl.Request{
				NamespacedName: types.NamespacedName{
					Namespace: pod.Namespace,
					Name:      pod.Name,
				},
			})
		}
	}

	if len(requests) > 0 {
		log.V(4).Info("Reconciling pods affected by unreachable node", "pod_count", len(requests))
	}

	return requests
}

type nodeEventsPredicate struct{}

var _ predicate.TypedPredicate[*corev1.Node] = (*nodeEventsPredicate)(nil)

func (p *nodeEventsPredicate) Create(e event.TypedCreateEvent[*corev1.Node]) bool {
	return utiltaints.TaintKeyExists(e.Object.Spec.Taints, corev1.TaintNodeUnreachable)
}

func (p *nodeEventsPredicate) Update(e event.TypedUpdateEvent[*corev1.Node]) bool {
	return !utiltaints.TaintKeyExists(e.ObjectOld.Spec.Taints, corev1.TaintNodeUnreachable) &&
		utiltaints.TaintKeyExists(e.ObjectNew.Spec.Taints, corev1.TaintNodeUnreachable)
}

func (p *nodeEventsPredicate) Delete(event.TypedDeleteEvent[*corev1.Node]) bool {
	return false
}

func (p *nodeEventsPredicate) Generic(event.TypedGenericEvent[*corev1.Node]) bool {
	return false
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
		WatchesRawSource(source.TypedKind(
			mgr.GetCache(),
			&corev1.Node{},
			handler.TypedEnqueueRequestsFromMapFunc(r.mapNodeToPods),
			&nodeEventsPredicate{},
		)).
		WithOptions(controller.Options{
			NeedLeaderElection:      new(false),
			MaxConcurrentReconciles: mgr.GetControllerOptions().GroupKindConcurrency[corev1.SchemeGroupVersion.WithKind("Pod").GroupKind().String()],
		}).
		WithLogConstructor(roletracker.NewLogConstructor(r.roleTracker, ControllerName)).
		Complete(core.WithLeadingManager(mgr, r, &corev1.Pod{}, cfg))
}

// The helpers below are copied from k8s.io/kubernetes v1.36.1, pkg/api/v1/pod/util.go
// (functions UpdatePodCondition, GetPodCondition and GetPodConditionFromList):
// https://github.com/kubernetes/kubernetes/blob/v1.36.1/pkg/api/v1/pod/util.go
// They are embedded rather than imported to avoid depending on k8s.io/kubernetes,
// whose feature gates conflict with Kueue's at init time.

// updatePodCondition updates existing pod condition or creates a new one. Sets LastTransitionTime to now if the
// status has changed.
// Returns true if pod condition has changed or has been added.
func updatePodCondition(status *corev1.PodStatus, condition *corev1.PodCondition) bool {
	condition.LastTransitionTime = metav1.Now()
	// Try to find this pod condition.
	conditionIndex, oldCondition := getPodCondition(status, condition.Type)

	if oldCondition == nil {
		// We are adding new pod condition.
		status.Conditions = append(status.Conditions, *condition)
		return true
	}
	// We are updating an existing condition, so we need to check if it has changed.
	if condition.Status == oldCondition.Status {
		condition.LastTransitionTime = oldCondition.LastTransitionTime
	}

	isEqual := condition.Status == oldCondition.Status &&
		condition.Reason == oldCondition.Reason &&
		condition.Message == oldCondition.Message &&
		condition.LastProbeTime.Equal(&oldCondition.LastProbeTime) &&
		condition.LastTransitionTime.Equal(&oldCondition.LastTransitionTime)

	status.Conditions[conditionIndex] = *condition
	// Return true if one of the fields have changed.
	return !isEqual
}

// getPodCondition extracts the provided condition from the given status and returns that.
// Returns nil and -1 if the condition is not present, and the index of the located condition.
func getPodCondition(status *corev1.PodStatus, conditionType corev1.PodConditionType) (int, *corev1.PodCondition) {
	if status == nil {
		return -1, nil
	}
	return getPodConditionFromList(status.Conditions, conditionType)
}

// getPodConditionFromList extracts the provided condition from the given list of condition and
// returns the index of the condition and the condition. Returns -1 and nil if the condition is not present.
func getPodConditionFromList(conditions []corev1.PodCondition, conditionType corev1.PodConditionType) (int, *corev1.PodCondition) {
	if conditions == nil {
		return -1, nil
	}
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return i, &conditions[i]
		}
	}
	return -1, nil
}

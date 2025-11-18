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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	utilnode "sigs.k8s.io/kueue/pkg/util/node"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
)

// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods/status,verbs=get;patch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch

var (
	realClock = clock.RealClock{}
)

type TerminatingPodReconciler struct {
	client          client.Client
	terminationCfgs []*terminatePodConfigInternal
	clock           clock.Clock
}

type terminatePodConfigInternal struct {
	selector    labels.Selector
	gracePeriod metav1.Duration
}

type TerminatingPodReconcilerOptions struct {
	clock clock.Clock
}

type TerminatingPodReconcilerOption func(*TerminatingPodReconcilerOptions)

func WithClock(c clock.Clock) TerminatingPodReconcilerOption {
	return func(o *TerminatingPodReconcilerOptions) {
		o.clock = c
	}
}

var defaultOptions = TerminatingPodReconcilerOptions{
	clock: realClock,
}

func NewTerminatingPodReconciler(
	client client.Client,
	cfgs []configapi.TerminatePodConfig,
	opts ...TerminatingPodReconcilerOption,
) (*TerminatingPodReconciler, error) {
	options := defaultOptions
	for _, opt := range opts {
		opt(&options)
	}

	terminationCfgs := make([]*terminatePodConfigInternal, len(cfgs))
	for i, cfg := range cfgs {
		selector, err := metav1.LabelSelectorAsSelector(&cfg.PodLabelSelector)
		if err != nil {
			return nil, err
		}

		terminationCfgs[i] = &terminatePodConfigInternal{
			selector:    selector,
			gracePeriod: cfg.ForcefulTerminationGracePeriod,
		}
	}

	return &TerminatingPodReconciler{
		client:          client,
		terminationCfgs: terminationCfgs,
		clock:           options.clock,
	}, nil
}

func (r *TerminatingPodReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	pod := &corev1.Pod{}
	if err := r.client.Get(ctx, req.NamespacedName, pod); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	terminationCfg := strictestMatchingTerminationConfig(r.terminationCfgs, pod)
	// Pod does not match any of the configured pod label selector
	if terminationCfg == nil {
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

	// Pod is not managed by Kueue
	if !utilpod.IsManagedByKueue(pod) {
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
	totalGracePeriod := gracefulTerminationPeriod + terminationCfg.gracePeriod.Duration
	if now.Before(pod.DeletionTimestamp.Add(totalGracePeriod)) {
		gracePeriodLeft := pod.DeletionTimestamp.Add(totalGracePeriod).Sub(now)
		return ctrl.Result{RequeueAfter: gracePeriodLeft}, nil
	}

	podPatch := pod.DeepCopy()
	podPatch.Status.Phase = corev1.PodFailed
	if err := r.client.Status().Patch(ctx, podPatch, client.MergeFrom(pod)); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// Finds a matching config with the shortest grace period.
func strictestMatchingTerminationConfig(cfgs []*terminatePodConfigInternal, p *corev1.Pod) *terminatePodConfigInternal {
	var result *terminatePodConfigInternal

	for _, cfg := range cfgs {
		if cfg.selector.Matches(labels.Set(p.Labels)) &&
			(result == nil || cfg.gracePeriod.Duration < result.gracePeriod.Duration) {
			result = cfg
		}
	}

	return result
}

func (r *TerminatingPodReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		Complete(r)
}

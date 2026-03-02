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

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/cmd/experimental/kueue-populator/pkg/constants"
)

const ControllerName = "kueue-populator"

type KueuePopulatorReconciler struct {
	client            client.Client
	log               logr.Logger
	recorder          record.EventRecorder
	namespaceSelector labels.Selector
	localQueueName    string
}

var _ reconcile.Reconciler = (*KueuePopulatorReconciler)(nil)

type KueuePopulatorReconcilerOptions struct {
	NamespaceSelector labels.Selector
	LocalQueueName    string
}

type KueuePopulatorReconcilerOption func(*KueuePopulatorReconcilerOptions)

func WithNamespaceSelector(s labels.Selector) KueuePopulatorReconcilerOption {
	return func(o *KueuePopulatorReconcilerOptions) {
		o.NamespaceSelector = s
	}
}

func WithLocalQueueName(name string) KueuePopulatorReconcilerOption {
	return func(o *KueuePopulatorReconcilerOptions) {
		o.LocalQueueName = name
	}
}

var defaultPopulatorOptions = KueuePopulatorReconcilerOptions{}

func NewKueuePopulatorReconciler(
	client client.Client,
	recorder record.EventRecorder,
	opts ...KueuePopulatorReconcilerOption,
) *KueuePopulatorReconciler {
	options := defaultPopulatorOptions
	for _, opt := range opts {
		opt(&options)
	}
	return &KueuePopulatorReconciler{
		client:            client,
		log:               ctrl.Log.WithName("kueue-populator-reconciler"),
		recorder:          recorder,
		namespaceSelector: options.NamespaceSelector,
		localQueueName:    options.LocalQueueName,
	}
}

// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=clusterqueues,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=localqueues,verbs=get;list;watch;create

func (r *KueuePopulatorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	var ns corev1.Namespace
	if err := r.client.Get(ctx, req.NamespacedName, &ns); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !ns.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	if r.namespaceSelector != nil && !r.namespaceSelector.Matches(labels.Set(ns.Labels)) {
		return ctrl.Result{}, nil
	}

	var allCQs kueue.ClusterQueueList
	if err := r.client.List(ctx, &allCQs); err != nil {
		log.Error(err, "Failed to list ClusterQueues", "namespace", klog.KObj(&ns))
		return ctrl.Result{}, err
	}

	nsLabels := labels.Set(ns.Labels)
	var cqs kueue.ClusterQueueList
	for i := range allCQs.Items {
		cq := &allCQs.Items[i]
		var selector labels.Selector
		if cq.Spec.NamespaceSelector == nil {
			selector = labels.Everything()
		} else {
			var err error
			selector, err = metav1.LabelSelectorAsSelector(cq.Spec.NamespaceSelector)
			if err != nil {
				log.Error(err, "Failed to parse namespaceSelector for ClusterQueue", "clusterQueue", klog.KObj(cq))
				return ctrl.Result{}, err
			}
		}

		if selector.Matches(nsLabels) {
			cqs.Items = append(cqs.Items, *cq)
		}
	}

	for i := range cqs.Items {
		cq := &cqs.Items[i]
		if !cq.DeletionTimestamp.IsZero() {
			continue
		}
		if err := r.ensureLocalQueueExists(ctx, cq, &ns); err != nil {
			log.Error(err, "Failed to ensure LocalQueue exists in namespace", "clusterQueue", klog.KObj(cq), "namespace", klog.KObj(&ns))
		}
	}

	return ctrl.Result{}, nil
}

func (r *KueuePopulatorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return builder.TypedControllerManagedBy[reconcile.Request](mgr).
		For(&corev1.Namespace{}).
		Watches(
			&kueue.ClusterQueue{},
			handler.EnqueueRequestsFromMapFunc(r.mapClusterQueueToNamespaces),
		).
		Complete(r)
}

func (r *KueuePopulatorReconciler) mapClusterQueueToNamespaces(ctx context.Context, cqObj client.Object) []reconcile.Request {
	cq, ok := cqObj.(*kueue.ClusterQueue)
	if !ok {
		return nil
	}

	log := r.log.WithValues("clusterQueue", klog.KObj(cq))

	if !cq.DeletionTimestamp.IsZero() {
		return nil
	}

	var selector labels.Selector
	if cq.Spec.NamespaceSelector == nil {
		selector = labels.Everything()
	} else {
		var err error
		selector, err = metav1.LabelSelectorAsSelector(cq.Spec.NamespaceSelector)
		if err != nil {
			log.Error(err, "Failed to parse namespaceSelector")
			return nil
		}
	}

	var nsList corev1.NamespaceList
	if err := r.client.List(ctx, &nsList, client.MatchingLabelsSelector{Selector: selector}); err != nil {
		log.Error(err, "Failed to list namespaces")
		return nil
	}

	requests := make([]reconcile.Request, 0, len(nsList.Items))
	for _, ns := range nsList.Items {
		if r.namespaceSelector != nil && !r.namespaceSelector.Matches(labels.Set(ns.GetLabels())) {
			continue
		}
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: ns.Name},
		})
	}
	return requests
}

func (r *KueuePopulatorReconciler) ensureLocalQueueExists(ctx context.Context, cq *kueue.ClusterQueue, ns *corev1.Namespace) error {
	log := ctrl.LoggerFrom(ctx)

	targetLQ := types.NamespacedName{
		Namespace: ns.Name,
		Name:      r.localQueueName,
	}

	var lq kueue.LocalQueue
	if err := r.client.Get(ctx, targetLQ, &lq); err == nil {
		if lq.Spec.ClusterQueue == kueue.ClusterQueueReference(cq.Name) {
			return nil
		}
		r.recorder.Eventf(cq, corev1.EventTypeWarning, "LocalQueueExists", "Skipping LocalQueue creation in namespace %s, a LocalQueue with name %s already exists", ns.Name, targetLQ.Name)
		return nil
	} else if !apierrors.IsNotFound(err) {
		return err
	}

	log.Info("Creating LocalQueue", "namespace", ns.Name, "localQueue", targetLQ.Name)

	newLQ := &kueue.LocalQueue{
		ObjectMeta: metav1.ObjectMeta{
			Name:      targetLQ.Name,
			Namespace: targetLQ.Namespace,
			Labels: map[string]string{
				constants.AutoGeneratedLabel: "true",
			},
		},
		Spec: kueue.LocalQueueSpec{
			ClusterQueue: kueue.ClusterQueueReference(cq.Name),
		},
	}

	if err := r.client.Create(ctx, newLQ); err != nil {
		r.recorder.Eventf(cq, corev1.EventTypeWarning, "LocalQueueCreationFailed", "Failed to create LocalQueue %s in namespace %s: %v", newLQ.Name, newLQ.Namespace, err)
		return err
	}
	r.recorder.Eventf(cq, corev1.EventTypeNormal, "LocalQueueCreated", "Created LocalQueue %s in namespace %s", newLQ.Name, newLQ.Namespace)
	return nil
}

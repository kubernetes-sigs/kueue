/*
Copyright 2022 The Kubernetes Authors.

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

package core

import (
	"context"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"

	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/queue"
)

type podTemplateOptions struct {
	watchers []PodTemplateUpdateWatcher
}

// Option configures the reconciler.
type PodTemplateOption func(*podTemplateOptions)

// WithWorkloadUpdateWatchers allows to specify the podtemplate update watchers
func WithPodTemplateUpdateWatchers(value ...PodTemplateUpdateWatcher) PodTemplateOption {
	return func(o *podTemplateOptions) {
		o.watchers = value
	}
}

var podTemplateOptionDefaultOptions = podTemplateOptions{}

type PodTemplateUpdateWatcher interface {
	NotifyPodTemplateUpdate(oldPt, newPt *corev1.PodTemplate)
}

// PodTemplateReconciler reconciles a PodTemplate object
type PodTemplateReconciler struct {
	log      logr.Logger
	queues   *queue.Manager
	cache    *cache.Cache
	client   client.Client
	watchers []PodTemplateUpdateWatcher
}

func NewPodTemplateReconciler(client client.Client, queues *queue.Manager, cache *cache.Cache, opts ...PodTemplateOption) *PodTemplateReconciler {
	options := podTemplateOptionDefaultOptions
	for _, opt := range opts {
		opt(&options)
	}

	return &PodTemplateReconciler{
		log:      ctrl.Log.WithName("podtemplate-reconciler"),
		client:   client,
		queues:   queues,
		cache:    cache,
		watchers: options.watchers,
	}
}

//+kubebuilder:rbac:groups="",resources=podtemplates,verbs=get;list;watch;create

func (r *PodTemplateReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var pt corev1.PodTemplate
	if err := r.client.Get(ctx, req.NamespacedName, &pt); err != nil {
		// we'll ignore not-found errors, since there is nothing to do.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log := ctrl.LoggerFrom(ctx).WithValues("podtemplate", klog.KObj(&pt))
	log.V(2).Info("Reconciling PodTemplate")

	return ctrl.Result{}, nil
}

func (r *PodTemplateReconciler) Create(e event.CreateEvent) bool {
	pt, isPodTemplate := e.Object.(*corev1.PodTemplate)
	if !isPodTemplate {
		return true
	}
	defer r.notifyWatchers(nil, pt)
	return true
}

func (r *PodTemplateReconciler) Delete(e event.DeleteEvent) bool {
	return true
}

func (r *PodTemplateReconciler) Update(e event.UpdateEvent) bool {
	oldPt, isPodTemplate := e.ObjectOld.(*corev1.PodTemplate)
	if !isPodTemplate {
		return true
	}
	pt := e.ObjectNew.(*corev1.PodTemplate)
	defer r.notifyWatchers(oldPt, pt)
	return true
}

func (r *PodTemplateReconciler) Generic(e event.GenericEvent) bool {
	r.log.V(3).Info("Ignore generic event", "obj", klog.KObj(e.Object), "kind", e.Object.GetObjectKind().GroupVersionKind())
	return false
}

func (r *PodTemplateReconciler) notifyWatchers(oldPt, newPt *corev1.PodTemplate) {
	for _, w := range r.watchers {
		w.NotifyPodTemplateUpdate(oldPt, newPt)
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *PodTemplateReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.PodTemplate{}).
		WithEventFilter(r).
		Complete(r)
}

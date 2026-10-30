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

package deployment

import (
	"context"
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ctrlconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
)

// +kubebuilder:rbac:groups="apps",resources=deployments,verbs=get;list;watch;patch

var _ jobframework.JobReconcilerInterface = (*Reconciler)(nil)

type Reconciler struct {
	client client.Client
}

func NewReconciler(_ context.Context, client client.Client, _ client.FieldIndexer, _ events.EventRecorder, _ ...jobframework.Option) (jobframework.JobReconcilerInterface, error) {
	return &Reconciler{
		client: client,
	}, nil
}

func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	if !features.Enabled(features.DeploymentParentSuspension) {
		return ctrl.Result{}, nil
	}

	log := ctrl.LoggerFrom(ctx)
	log.V(3).Info("Reconcile Deployment for template drift")

	deploy := &appsv1.Deployment{}
	if err := r.client.Get(ctx, req.NamespacedName, deploy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if deploy.Annotations[ctrlconstants.PausedByKueueAnnotation] != "true" {
		return ctrl.Result{}, nil
	}

	drifted, err := r.templateDrifted(ctx, deploy)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !drifted {
		return ctrl.Result{}, nil
	}

	log.V(2).Info("Template drift detected on Kueue-paused Deployment, unpausing",
		"deployment", klog.KObj(deploy))
	return ctrl.Result{}, clientutil.Patch(ctx, r.client, deploy, func() (bool, error) {
		delete(deploy.Annotations, ctrlconstants.PausedByKueueAnnotation)
		deploy.Spec.Paused = false
		return true, nil
	})
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctrl.Log.V(3).Info("Setting up Deployment reconciler for template drift detection")
	return ctrl.NewControllerManagedBy(mgr).
		For(&appsv1.Deployment{}).
		WithEventFilter(r).
		Complete(r)
}

// templateDrifted returns true when the Deployment's pod template differs from
// the latest ReplicaSet's template, indicating the user edited the template
// while the Deployment was Kueue-paused.
func (r *Reconciler) templateDrifted(ctx context.Context, deploy *appsv1.Deployment) (bool, error) {
	var rsList appsv1.ReplicaSetList
	if err := r.client.List(ctx, &rsList,
		client.InNamespace(deploy.Namespace),
	); err != nil {
		return false, err
	}

	var latestRS *appsv1.ReplicaSet
	var latestRevision int64
	for i := range rsList.Items {
		rs := &rsList.Items[i]
		if !isOwnedBy(rs, deploy) {
			continue
		}
		rev := parseRevision(rs)
		if latestRS == nil || rev > latestRevision {
			latestRS = rs
			latestRevision = rev
		}
	}

	if latestRS == nil {
		return false, nil
	}

	return !equalIgnoreHash(deploy.Spec.Template, latestRS.Spec.Template), nil
}

// equalIgnoreHash compares two PodTemplateSpecs ignoring the pod-template-hash
// label that the Deployment controller adds to RS templates.
func equalIgnoreHash(template1, template2 corev1.PodTemplateSpec) bool {
	t1 := template1.DeepCopy()
	t2 := template2.DeepCopy()
	delete(t1.Labels, appsv1.DefaultDeploymentUniqueLabelKey)
	delete(t2.Labels, appsv1.DefaultDeploymentUniqueLabelKey)
	return equality.Semantic.DeepEqual(t1, t2)
}

func isOwnedBy(rs *appsv1.ReplicaSet, deploy *appsv1.Deployment) bool {
	owner := metav1.GetControllerOf(rs)
	return owner != nil &&
		owner.Kind == "Deployment" &&
		owner.APIVersion == appsv1.SchemeGroupVersion.String() &&
		owner.UID == deploy.UID
}

func parseRevision(rs *appsv1.ReplicaSet) int64 {
	v, _ := strconv.ParseInt(rs.Annotations["deployment.kubernetes.io/revision"], 10, 64)
	return v
}

var _ predicate.Predicate = (*Reconciler)(nil)

func (r *Reconciler) Generic(event.GenericEvent) bool {
	return false
}

func (r *Reconciler) Create(e event.CreateEvent) bool {
	return hasPausedByKueueAnnotation(e.Object)
}

func (r *Reconciler) Update(e event.UpdateEvent) bool {
	return hasPausedByKueueAnnotation(e.ObjectNew) || hasPausedByKueueAnnotation(e.ObjectOld)
}

func (r *Reconciler) Delete(event.DeleteEvent) bool {
	return false
}

func hasPausedByKueueAnnotation(obj client.Object) bool {
	deploy, ok := obj.(*appsv1.Deployment)
	if !ok {
		return false
	}
	return deploy.Annotations[ctrlconstants.PausedByKueueAnnotation] == "true"
}

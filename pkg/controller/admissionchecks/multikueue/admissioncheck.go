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

package multikueue

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/api/equality"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
)

type multiKueueStoreHelper = admissioncheck.ConfigHelper[*kueue.MultiKueueConfig, kueue.MultiKueueConfig]

func newMultiKueueStoreHelper(c client.Client) (*multiKueueStoreHelper, error) {
	return admissioncheck.NewConfigHelper[*kueue.MultiKueueConfig](c)
}

// ACReconciler implements the reconciler for all the admission checks controlled by multikueue.
// Its main task being to maintain the active state of the admission checks based on the heath
// of its referenced MultiKueueClusters.
type ACReconciler struct {
	client client.Client
	helper *multiKueueStoreHelper
}

var _ reconcile.Reconciler = (*ACReconciler)(nil)

func (a *ACReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	ac := &kueue.AdmissionCheck{}
	if err := a.client.Get(ctx, req.NamespacedName, ac); err != nil || ac.Spec.ControllerName != kueue.MultiKueueControllerName {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	log := ctrl.LoggerFrom(ctx)
	log.V(2).Info("Reconcile AdmissionCheck")

	newCondition := metav1.Condition{
		Type:               kueue.AdmissionCheckActive,
		Status:             metav1.ConditionTrue,
		Reason:             "Active",
		Message:            "The admission check is active",
		ObservedGeneration: ac.Generation,
	}

	if cfg, err := a.helper.ConfigFromRef(ctx, ac.Spec.Parameters); err != nil {
		newCondition.Status = metav1.ConditionFalse
		newCondition.Reason = "BadConfig"
		newCondition.Message = fmt.Sprintf("Cannot load the AdmissionChecks parameters: %s", err.Error())
	} else {
		var missingClusters []string
		var inactiveClusters []string
		// check the status of the clusters
		for _, clusterName := range cfg.Spec.Clusters {
			cluster := &kueue.MultiKueueCluster{}
			err := a.client.Get(ctx, types.NamespacedName{Name: clusterName}, cluster)
			if client.IgnoreNotFound(err) != nil {
				log.Error(err, "reading cluster", "multiKueueCluster", clusterName)
				return reconcile.Result{}, err
			}

			if err != nil {
				missingClusters = append(missingClusters, clusterName)
			} else if !apimeta.IsStatusConditionTrue(cluster.Status.Conditions, kueue.MultiKueueClusterActive) {
				inactiveClusters = append(inactiveClusters, clusterName)
			}
		}
		unusableClustersCount := len(missingClusters) + len(inactiveClusters)
		if unusableClustersCount > 0 {
			if unusableClustersCount < len(cfg.Spec.Clusters) {
				// keep it partially active
				newCondition.Reason = "SomeActiveClusters"
			} else {
				newCondition.Status = metav1.ConditionFalse
				newCondition.Reason = "NoUsableClusters"
			}

			var messageParts []string
			if len(missingClusters) > 0 {
				messageParts = []string{fmt.Sprintf("Missing clusters: %v", missingClusters)}
			}
			if len(inactiveClusters) > 0 {
				messageParts = append(messageParts, fmt.Sprintf("Inactive clusters: %v", inactiveClusters))
			}
			newCondition.Message = strings.Join(messageParts, ", ")
		}
	}

	needsUpdate := false
	oldCondition := apimeta.FindStatusCondition(ac.Status.Conditions, kueue.AdmissionCheckActive)
	if !cmpConditionState(oldCondition, &newCondition) {
		apimeta.SetStatusCondition(&ac.Status.Conditions, newCondition)
		needsUpdate = true
	}

	if needsUpdate {
		err := a.client.Status().Update(ctx, ac)
		if err != nil {
			log.V(2).Error(err, "Updating check condition", "newCondition", newCondition)
		}
		return reconcile.Result{}, err
	}

	return reconcile.Result{}, nil
}

// +kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=admissionchecks,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=multikueueconfigs,verbs=get;list;watch

func newACReconciler(c client.Client, helper *multiKueueStoreHelper) *ACReconciler {
	return &ACReconciler{
		client: c,
		helper: helper,
	}
}

func (a *ACReconciler) setupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("multikueue_admissioncheck").
		For(&kueue.AdmissionCheck{}).
		Watches(&kueue.MultiKueueConfig{}, &mkConfigHandler{client: a.client}).
		Watches(&kueue.MultiKueueCluster{}, &mkClusterHandler{client: a.client}).
		Complete(a)
}

type mkConfigHandler struct {
	client client.Client
}

var _ handler.EventHandler = (*mkConfigHandler)(nil)

func (m *mkConfigHandler) Create(ctx context.Context, event event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	mkc, isMKC := event.Object.(*kueue.MultiKueueConfig)
	if !isMKC {
		return
	}

	if err := queueReconcileForConfigUsers(ctx, mkc.Name, m.client, q); err != nil {
		ctrl.LoggerFrom(ctx).V(2).Error(err, "Failure on create event", "multiKueueConfig", klog.KObj(mkc))
	}
}

func (m *mkConfigHandler) Update(ctx context.Context, event event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	oldMKC, isOldMKC := event.ObjectOld.(*kueue.MultiKueueConfig)
	newMKC, isNewMKC := event.ObjectNew.(*kueue.MultiKueueConfig)
	if !isOldMKC || !isNewMKC || equality.Semantic.DeepEqual(oldMKC.Spec.Clusters, newMKC.Spec.Clusters) {
		return
	}

	if err := queueReconcileForConfigUsers(ctx, oldMKC.Name, m.client, q); err != nil {
		ctrl.LoggerFrom(ctx).V(2).Error(err, "Failure on update event", "multiKueueConfig", klog.KObj(oldMKC))
	}
}

func (m *mkConfigHandler) Delete(ctx context.Context, event event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	mkc, isMKC := event.Object.(*kueue.MultiKueueConfig)
	if !isMKC {
		return
	}

	if err := queueReconcileForConfigUsers(ctx, mkc.Name, m.client, q); err != nil {
		ctrl.LoggerFrom(ctx).V(2).Error(err, "Failure on delete event", "multiKueueConfig", klog.KObj(mkc))
	}
}

func (m *mkConfigHandler) Generic(ctx context.Context, event event.GenericEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	mkc, isMKC := event.Object.(*kueue.MultiKueueConfig)
	if !isMKC {
		return
	}

	if err := queueReconcileForConfigUsers(ctx, mkc.Name, m.client, q); err != nil {
		ctrl.LoggerFrom(ctx).V(2).Error(err, "Failure on generic event", "multiKueueConfig", klog.KObj(mkc))
	}
}

func queueReconcileForConfigUsers(ctx context.Context, config string, c client.Client, q workqueue.TypedRateLimitingInterface[reconcile.Request]) error {
	users := &kueue.AdmissionCheckList{}

	if err := c.List(ctx, users, client.MatchingFields{AdmissionCheckUsingConfigKey: config}); err != nil {
		return err
	}

	for _, user := range users.Items {
		req := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name: user.Name,
			},
		}
		q.Add(req)
	}

	return nil
}

type mkClusterHandler struct {
	client client.Client
}

var _ handler.EventHandler = (*mkClusterHandler)(nil)

func (m *mkClusterHandler) Create(ctx context.Context, event event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	mkc, isMKC := event.Object.(*kueue.MultiKueueCluster)
	if !isMKC {
		return
	}

	if err := m.queue(ctx, mkc, q); err != nil {
		ctrl.LoggerFrom(ctx).V(2).Error(err, "Failure on create event", "multiKueueCluster", klog.KObj(mkc))
	}
}

func (m *mkClusterHandler) Update(ctx context.Context, event event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	oldMKC, isOldMKC := event.ObjectOld.(*kueue.MultiKueueCluster)
	newMKC, isNewMKC := event.ObjectNew.(*kueue.MultiKueueCluster)
	if !isOldMKC || !isNewMKC {
		return
	}

	oldActive := apimeta.FindStatusCondition(oldMKC.Status.Conditions, kueue.MultiKueueClusterActive)
	newActive := apimeta.FindStatusCondition(newMKC.Status.Conditions, kueue.MultiKueueClusterActive)
	if !cmpConditionState(oldActive, newActive) {
		if err := m.queue(ctx, newMKC, q); err != nil {
			ctrl.LoggerFrom(ctx).V(2).Error(err, "Failure on update event", "multiKueueCluster", klog.KObj(oldMKC))
		}
	}
}

func (m *mkClusterHandler) Delete(ctx context.Context, event event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	mkc, isMKC := event.Object.(*kueue.MultiKueueCluster)
	if !isMKC {
		return
	}

	if err := m.queue(ctx, mkc, q); err != nil {
		ctrl.LoggerFrom(ctx).V(2).Error(err, "Failure on delete event", "multiKueueCluster", klog.KObj(mkc))
	}
}

func (m *mkClusterHandler) Generic(ctx context.Context, event event.GenericEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	mkc, isMKC := event.Object.(*kueue.MultiKueueCluster)
	if !isMKC {
		return
	}

	if err := m.queue(ctx, mkc, q); err != nil {
		ctrl.LoggerFrom(ctx).V(2).Error(err, "Failure on generic event", "multiKueueCluster", klog.KObj(mkc))
	}
}

func (m *mkClusterHandler) queue(ctx context.Context, cluster *kueue.MultiKueueCluster, q workqueue.TypedRateLimitingInterface[reconcile.Request]) error {
	users := &kueue.MultiKueueConfigList{}
	if err := m.client.List(ctx, users, client.MatchingFields{UsingMultiKueueClusters: cluster.Name}); err != nil {
		return err
	}

	for _, user := range users.Items {
		if err := queueReconcileForConfigUsers(ctx, user.Name, m.client, q); err != nil {
			return err
		}
	}
	return nil
}

func cmpConditionState(a, b *metav1.Condition) bool {
	if a == b {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.Status == b.Status && a.Reason == b.Reason && a.Message == b.Message
}

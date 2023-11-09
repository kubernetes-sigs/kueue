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

package multikueue

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
)

const (
	ControllerName = "kueue.x-k8s.io/multikueue"
)

type multiKueueStoreHelper = admissioncheck.ConfigHelper[*kueue.MultiKueueConfig, kueue.MultiKueueConfig]

func newMultiKueueStoreHelper(c client.Client) (*multiKueueStoreHelper, error) {
	return admissioncheck.NewConfigHelper[*kueue.MultiKueueConfig](c)
}

type AcReconciler struct {
	client client.Client
	helper *multiKueueStoreHelper

	lock        sync.RWMutex
	controllers map[string]*remoteController
	wlUpdateCh  chan event.GenericEvent

	rootContext context.Context

	// for testing only
	updateConfigOverride func(ctx context.Context, rc *remoteController, kubeconfigs map[string][]byte) error
}

var _ reconcile.Reconciler = (*AcReconciler)(nil)
var _ manager.Runnable = (*AcReconciler)(nil)

func (a *AcReconciler) controllerFor(acName string) (*remoteController, bool) {
	a.lock.RLock()
	defer a.lock.RUnlock()

	c, f := a.controllers[acName]
	return c, f
}

func (a *AcReconciler) setControllerFor(acName string, c *remoteController) {
	a.lock.Lock()
	defer a.lock.Unlock()

	if old, found := a.controllers[acName]; found {
		old.watchCancel()
	}
	if c != nil {
		a.controllers[acName] = c
	} else {
		delete(a.controllers, acName)
	}
}

func (a *AcReconciler) Start(ctx context.Context) error {
	a.rootContext = ctx
	return nil
}

func (a *AcReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	ac := &kueue.AdmissionCheck{}
	if err := a.client.Get(ctx, req.NamespacedName, ac); err != nil || ac.Spec.ControllerName != ControllerName {
		if apierrors.IsNotFound(err) || !ac.DeletionTimestamp.IsZero() {
			// stop/deleted a potential check controller
			if cc, existing := a.controllerFor(req.Name); existing {
				cc.watchCancel()
				a.setControllerFor(req.Name, nil)
				log.V(2).Info("Controller removed")
			}
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	var remCtrl *remoteController
	inactiveReason := ""

	log.V(2).Info("Reconcile AdmissionCheck")
	if cfg, err := a.helper.ConfigFromRef(ctx, ac.Spec.Parameters); err != nil {
		inactiveReason = fmt.Sprintf("Cannot load AC config: %s", err.Error())
	} else {
		kubeconfigs, err := a.getKubeConfigs(ctx, &cfg.Spec)
		if err != nil {
			a.setControllerFor(ac.Name, nil)
			inactiveReason = fmt.Sprintf("Cannot load kubeconfigs: %s", err.Error())
		} else {
			cc, existing := a.controllerFor(ac.Name)
			if !existing {
				cc = newRemoteController(a.rootContext, a.client, a.wlUpdateCh)
				a.setControllerFor(ac.Name, cc)
			}

			var err error
			if a.updateConfigOverride != nil {
				err = a.updateConfigOverride(ctx, cc, kubeconfigs)
			} else {
				err = cc.UpdateConfig(ctx, kubeconfigs)
			}

			if err != nil {
				inactiveReason = fmt.Sprintf("Cannot start remote controller: %s", err.Error())
				a.setControllerFor(ac.Name, nil)
			} else {
				remCtrl = cc
			}
		}
	}

	newCondition := metav1.Condition{
		Type:    kueue.AdmissionCheckActive,
		Status:  metav1.ConditionTrue,
		Reason:  "Active",
		Message: "The admission check is active",
	}
	if remCtrl.IsActive() {
		newCondition.Status = metav1.ConditionTrue
		newCondition.Reason = "Active"
		newCondition.Message = "The admission check is active"

	} else {
		newCondition.Status = metav1.ConditionFalse
		newCondition.Reason = "Inactive"
		newCondition.Message = inactiveReason
	}

	oldCondition := apimeta.FindStatusCondition(ac.Status.Conditions, kueue.AdmissionCheckActive)
	if oldCondition == nil || oldCondition.Status != newCondition.Status || oldCondition.Reason != newCondition.Reason || oldCondition.Message != newCondition.Message {
		apimeta.SetStatusCondition(&ac.Status.Conditions, newCondition)
		err := a.client.Status().Update(ctx, ac)
		if err != nil {
			log.V(2).Error(err, "Updateing check condition", "newCondition", newCondition)
		}
		return reconcile.Result{}, err
	}

	return reconcile.Result{}, nil
}

func (cc *AcReconciler) getKubeConfigs(ctx context.Context, spec *kueue.MultiKueueConfigSpec) (map[string][]byte, error) {
	ret := make(map[string][]byte, len(spec.Clusters))
	for _, c := range spec.Clusters {
		ref := c.KubeconfigRef
		sec := corev1.Secret{}
		secretObjKey := types.NamespacedName{
			Namespace: ref.SecretNamespace,
			Name:      ref.SecretName,
		}
		err := cc.client.Get(ctx, secretObjKey, &sec)
		if err != nil {
			return nil, fmt.Errorf("getting kubeconfig secret for %q: %w", c.Name, err)
		}

		kconfigBytes, found := sec.Data[ref.ConfigKey]
		if !found {
			return nil, fmt.Errorf("getting kubeconfig secret for %q: key %q not found in secret %q", c.Name, ref.ConfigKey, ref.SecretName)
		}
		ret[c.Name] = kconfigBytes
	}
	return ret, nil
}

// +kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=admissionchecks,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=multikueueconfigs,verbs=get;list;watch

func NewACController(c client.Client) *AcReconciler {
	return &AcReconciler{
		client:      c,
		controllers: make(map[string]*remoteController),
		wlUpdateCh:  make(chan event.GenericEvent, 10),
	}
}

func (a *AcReconciler) SetupWithManager(mgr ctrl.Manager) error {
	helper, err := newMultiKueueStoreHelper(a.client)
	if err != nil {
		return err
	}
	a.helper = helper

	wlRec := &wlReconciler{
		acr: a,
	}

	syncHndl := handler.Funcs{
		GenericFunc: func(_ context.Context, e event.GenericEvent, q workqueue.RateLimitingInterface) {
			q.Add(reconcile.Request{NamespacedName: types.NamespacedName{
				Namespace: e.Object.GetNamespace(),
				Name:      e.Object.GetName(),
			}})
		},
	}

	err = ctrl.NewControllerManagedBy(mgr).
		For(&kueue.Workload{}).
		WatchesRawSource(&source.Channel{Source: a.wlUpdateCh}, syncHndl).
		Complete(wlRec)
	if err != nil {
		return err
	}

	err = mgr.Add(a)
	if err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&kueue.AdmissionCheck{}).
		Watches(&kueue.MultiKueueConfig{}, &mkcHandler{client: a.client}).
		Watches(&corev1.Secret{}, &secretHandler{client: a.client}).
		Complete(a)
}

type mkcHandler struct {
	client client.Client
}

var _ handler.EventHandler = (*mkcHandler)(nil)

func (m *mkcHandler) Create(ctx context.Context, event event.CreateEvent, q workqueue.RateLimitingInterface) {
	mkc, isMKC := event.Object.(*kueue.MultiKueueConfig)
	if !isMKC {
		return
	}

	if err := queueReconcileForConfigUsers(ctx, mkc.Name, m.client, q); err != nil {
		ctrl.LoggerFrom(ctx).V(5).Error(err, "Failure on create event", "multiKueueConfig", klog.KObj(mkc))
	}
}

func (m *mkcHandler) Update(ctx context.Context, event event.UpdateEvent, q workqueue.RateLimitingInterface) {
	oldMKC, isOldMKC := event.ObjectOld.(*kueue.MultiKueueConfig)
	newMKC, isNewMKC := event.ObjectNew.(*kueue.MultiKueueConfig)
	if !isOldMKC || !isNewMKC || equality.Semantic.DeepEqual(oldMKC.Spec.Clusters, newMKC.Spec.Clusters) {
		return
	}

	if err := queueReconcileForConfigUsers(ctx, oldMKC.Name, m.client, q); err != nil {
		ctrl.LoggerFrom(ctx).V(5).Error(err, "Failure on delete event", "multiKueueConfig", klog.KObj(oldMKC))
	}
}

func (m *mkcHandler) Delete(ctx context.Context, event event.DeleteEvent, q workqueue.RateLimitingInterface) {
	mkc, isMKC := event.Object.(*kueue.MultiKueueConfig)
	if !isMKC {
		return
	}

	if err := queueReconcileForConfigUsers(ctx, mkc.Name, m.client, q); err != nil {
		ctrl.LoggerFrom(ctx).V(5).Error(err, "Failure on delete event", "multiKueueConfig", klog.KObj(mkc))
	}
}

func (m *mkcHandler) Generic(ctx context.Context, event event.GenericEvent, q workqueue.RateLimitingInterface) {
	mkc, isMKC := event.Object.(*kueue.MultiKueueConfig)
	if !isMKC {
		return
	}

	if err := queueReconcileForConfigUsers(ctx, mkc.Name, m.client, q); err != nil {
		ctrl.LoggerFrom(ctx).V(5).Error(err, "Failure on generic event", "multiKueueConfig", klog.KObj(mkc))
	}
}

func queueReconcileForConfigUsers(ctx context.Context, config string, c client.Client, q workqueue.RateLimitingInterface) error {
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

type secretHandler struct {
	client client.Client
}

var _ handler.EventHandler = (*secretHandler)(nil)

func (s *secretHandler) Create(ctx context.Context, event event.CreateEvent, q workqueue.RateLimitingInterface) {
	if err := s.queue(ctx, event.Object, q); err != nil {
		ctrl.LoggerFrom(ctx).V(5).Error(err, "Failure on create event", "secret", klog.KObj(event.Object))
	}
}

func (s *secretHandler) Update(ctx context.Context, event event.UpdateEvent, q workqueue.RateLimitingInterface) {
	if err := s.queue(ctx, event.ObjectOld, q); err != nil {
		ctrl.LoggerFrom(ctx).V(5).Error(err, "Failure on update event", "secret", klog.KObj(event.ObjectOld))
	}
}

func (s *secretHandler) Delete(ctx context.Context, event event.DeleteEvent, q workqueue.RateLimitingInterface) {
	if err := s.queue(ctx, event.Object, q); err != nil {
		ctrl.LoggerFrom(ctx).V(5).Error(err, "Failure on update event", "secret", klog.KObj(event.Object))
	}
}

func (s *secretHandler) Generic(ctx context.Context, event event.GenericEvent, q workqueue.RateLimitingInterface) {
	if err := s.queue(ctx, event.Object, q); err != nil {
		ctrl.LoggerFrom(ctx).V(5).Error(err, "Failure on generic event", "secret", klog.KObj(event.Object))
	}
}

func (s *secretHandler) queue(ctx context.Context, obj client.Object, q workqueue.RateLimitingInterface) error {
	users := &kueue.MultiKueueConfigList{}
	secret, isSecret := obj.(*corev1.Secret)
	if !isSecret {
		return errors.New("not a secret")
	}

	if err := s.client.List(ctx, users, client.MatchingFields{UsingKubeConfigs: strings.Join([]string{secret.Namespace, secret.Name}, "/")}); err != nil {
		return err
	}

	for _, user := range users.Items {
		if err := queueReconcileForConfigUsers(ctx, user.Name, s.client, q); err != nil {
			return err
		}
	}
	return nil
}

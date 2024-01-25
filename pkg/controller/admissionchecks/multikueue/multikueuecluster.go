/*
Copyright 2024 The Kubernetes Authors.

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
	"os"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
)

const (
	defaultOrigin = "multikueue"
)

type clientWithWatchBuilder func(config []byte, options client.Options) (client.WithWatch, error)

type remoteClient struct {
	localClient client.Client
	client      client.WithWatch
	wlUpdateCh  chan<- event.GenericEvent
	watchCancel func()
	kubeconfig  []byte
	origin      string

	// For unit testing only. There is now need of creating fully functional remote clients in the unit tests
	// and creating valid kubeconfig content is not trivial.
	// The full client creation and usage is validated in the integration and e2e tests.
	builderOverride clientWithWatchBuilder
}

func newRemoteClient(localClient client.Client, wlUpdateCh chan<- event.GenericEvent, origin string) *remoteClient {
	rc := &remoteClient{
		wlUpdateCh:  wlUpdateCh,
		localClient: localClient,
		origin:      origin,
	}
	return rc
}

func newClientWithWatch(kubeconfig []byte, options client.Options) (client.WithWatch, error) {
	restConfig, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return nil, err
	}
	return client.NewWithWatch(restConfig, options)
}

// setConfig - will try to recreate the k8s client and restart watching if the new config is different than
// the one currently used.
func (rc *remoteClient) setConfig(watchCtx context.Context, kubeconfig []byte) error {
	if equality.Semantic.DeepEqual(kubeconfig, rc.kubeconfig) {
		return nil
	}

	if rc.watchCancel != nil {
		rc.watchCancel()
		rc.watchCancel = nil
	}

	builder := newClientWithWatch
	if rc.builderOverride != nil {
		builder = rc.builderOverride
	}
	remoteClient, err := builder(kubeconfig, client.Options{Scheme: rc.localClient.Scheme()})
	if err != nil {
		return err
	}
	rc.client = remoteClient

	newWatcher, err := rc.client.Watch(watchCtx, &kueue.WorkloadList{})
	if err != nil {
		return nil
	}

	go func() {
		for r := range newWatcher.ResultChan() {
			rc.queueWorkloadEvent(watchCtx, r)
		}
	}()

	rc.watchCancel = newWatcher.Stop
	rc.kubeconfig = kubeconfig
	return nil
}

func (rc *remoteClient) queueWorkloadEvent(ctx context.Context, ev watch.Event) {
	wl, isWl := ev.Object.(*kueue.Workload)
	if !isWl {
		return
	}

	localWl := &kueue.Workload{}
	if err := rc.localClient.Get(ctx, client.ObjectKeyFromObject(wl), localWl); err == nil {
		rc.wlUpdateCh <- event.GenericEvent{Object: localWl}
	} else {
		if !apierrors.IsNotFound(err) {
			ctrl.LoggerFrom(ctx).Error(err, "reading local workload")
		}
	}
}

func (rc *remoteClient) runGC(ctx context.Context) {
	log := ctrl.LoggerFrom(ctx)
	lst := &kueue.WorkloadList{}
	err := rc.client.List(ctx, lst, client.MatchingLabels{MultiKueueOriginLabelKey: rc.origin})
	if err != nil {
		log.V(2).Error(err, "Listing remote workloads")
		return
	}

	for _, remoteWl := range lst.Items {
		localWl := &kueue.Workload{}
		err := rc.localClient.Get(ctx, client.ObjectKeyFromObject(&remoteWl), localWl)
		if client.IgnoreNotFound(err) != nil {
			log.V(2).Error(err, "Reading local workload")
			continue
		}

		// if it's not found, or pending deletion
		if err != nil || !localWl.DeletionTimestamp.IsZero() {
			// if the remote wl has a controller, delete that as well
			if controller := metav1.GetControllerOf(&remoteWl); controller != nil {
				adapterKey := schema.FromAPIVersionAndKind(controller.APIVersion, controller.Kind).String()
				if adapter, found := adapters[adapterKey]; !found {
					log.V(2).Info("No adapter found", "adapterKey", adapterKey, "controller", controller.Name)
				} else {
					err := adapter.DeleteRemoteObject(ctx, rc.client, types.NamespacedName{Name: controller.Name, Namespace: remoteWl.Namespace})
					if client.IgnoreNotFound(err) != nil {
						log.V(2).Error(err, "Deleting remote workload's owner", "remoteWl", klog.KObj(&remoteWl))
					}
				}
				if err := rc.client.Delete(ctx, &remoteWl); client.IgnoreNotFound(err) != nil {
					log.V(2).Error(err, "Deleting remote workload", "remoteWl", klog.KObj(&remoteWl))
				}
			}
		}
	}
}

// clustersReconciler implements the reconciler for all MultiKueueClusters.
// Its main task being to maintain the list of remote clients associated to each MultiKueueCluster.
type clustersReconciler struct {
	localClient     client.Client
	configNamespace string

	lock sync.RWMutex
	// The list of remote remoteClients, indexed by the cluster name.
	remoteClients map[string]*remoteClient
	wlUpdateCh    chan event.GenericEvent

	// gcInterval - time waiting between two GC runs.
	gcInterval time.Duration

	// rootContext - holds the context passed by the controller-runtime on Start.
	// It's used to create child contexts for MultiKueueClusters client watch routines
	// that will gracefully end when the controller-manager stops.
	rootContext context.Context

	// For unit testing only. There is now need of creating fully functional remote clients in the unit tests
	// and creating valid kubeconfig content is not trivial.
	// The full client creation and usage is validated in the integration and e2e tests.
	builderOverride clientWithWatchBuilder
}

var _ manager.Runnable = (*clustersReconciler)(nil)
var _ reconcile.Reconciler = (*clustersReconciler)(nil)

func (c *clustersReconciler) Start(ctx context.Context) error {
	c.rootContext = ctx
	go c.runGC(ctx)
	return nil
}

func (c *clustersReconciler) stopAndRemoveCluster(clusterName string) {
	c.lock.Lock()
	defer c.lock.Unlock()
	if rc, found := c.remoteClients[clusterName]; found {
		rc.watchCancel()
		delete(c.remoteClients, clusterName)
	}
}

func (c *clustersReconciler) setRemoteClientConfig(ctx context.Context, clusterName string, kubeconfig []byte, origin string) error {
	c.lock.Lock()
	defer c.lock.Unlock()

	client, found := c.remoteClients[clusterName]
	if !found {
		client = newRemoteClient(c.localClient, c.wlUpdateCh, origin)
		if c.builderOverride != nil {
			client.builderOverride = c.builderOverride
		}
		c.remoteClients[clusterName] = client
	}

	if err := client.setConfig(c.rootContext, kubeconfig); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "failed to set kubeConfig in the remote client")
		delete(c.remoteClients, clusterName)
		return err
	}
	return nil
}

func (a *clustersReconciler) controllerFor(acName string) (*remoteClient, bool) {
	a.lock.RLock()
	defer a.lock.RUnlock()

	c, f := a.remoteClients[acName]
	return c, f
}

func (c *clustersReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	cluster := &kueuealpha.MultiKueueCluster{}
	log := ctrl.LoggerFrom(ctx)

	err := c.localClient.Get(ctx, req.NamespacedName, cluster)
	if client.IgnoreNotFound(err) != nil {
		return reconcile.Result{}, err
	}

	log.V(2).Info("Reconcile MultiKueueCluster")

	if err != nil || !cluster.DeletionTimestamp.IsZero() {
		c.stopAndRemoveCluster(req.Name)
		return reconcile.Result{}, nil
	}

	// get the kubeconfig
	kubeConfig, retry, err := c.getKubeConfig(ctx, &cluster.Spec.KubeConfig)
	if retry {
		return reconcile.Result{}, err
	}
	if err != nil {
		log.Error(err, "reading kubeconfig")
		c.stopAndRemoveCluster(req.Name)
		return reconcile.Result{}, c.updateStatus(ctx, cluster, false, "BadConfig", err.Error())
	}

	origin := cluster.Spec.Origin
	if len(origin) == 0 {
		origin = defaultOrigin
	}

	if err := c.setRemoteClientConfig(ctx, cluster.Name, kubeConfig, origin); err != nil {
		log.Error(err, "setting kubeconfig")
		return reconcile.Result{}, c.updateStatus(ctx, cluster, false, "ClientConnectionFailed", err.Error())
	}
	return reconcile.Result{}, c.updateStatus(ctx, cluster, true, "Active", "Connected")
}

func (c *clustersReconciler) getKubeConfig(ctx context.Context, ref *kueuealpha.KubeConfig) ([]byte, bool, error) {
	if ref.LocationType == kueuealpha.SecretLocationType {
		return c.getKubeConfigFromSecret(ctx, ref.Location)
	}
	// Otherwise it's path
	return c.getKubeConfigFromPath(ref.Location)
}

func (c *clustersReconciler) getKubeConfigFromSecret(ctx context.Context, secretName string) ([]byte, bool, error) {
	sec := corev1.Secret{}
	secretObjKey := types.NamespacedName{
		Namespace: c.configNamespace,
		Name:      secretName,
	}
	err := c.localClient.Get(ctx, secretObjKey, &sec)
	if err != nil {
		return nil, !apierrors.IsNotFound(err), err
	}

	kconfigBytes, found := sec.Data[kueuealpha.MultiKueueConfigSecretKey]
	if !found {
		return nil, false, fmt.Errorf("key %q not found in secret %q", kueuealpha.MultiKueueConfigSecretKey, secretName)
	}

	return kconfigBytes, false, nil
}

func (c *clustersReconciler) getKubeConfigFromPath(path string) ([]byte, bool, error) {
	content, err := os.ReadFile(path)
	return content, false, err
}

func (c *clustersReconciler) updateStatus(ctx context.Context, cluster *kueuealpha.MultiKueueCluster, active bool, reason, message string) error {
	newCondition := metav1.Condition{
		Type:    kueuealpha.MultiKueueClusterActive,
		Status:  metav1.ConditionFalse,
		Reason:  reason,
		Message: message,
	}
	if active {
		newCondition.Status = metav1.ConditionTrue
	}

	// if the condition is up to date
	oldCondition := apimeta.FindStatusCondition(cluster.Status.Conditions, kueuealpha.MultiKueueClusterActive)
	if cmpConditionState(oldCondition, &newCondition) {
		return nil
	}

	apimeta.SetStatusCondition(&cluster.Status.Conditions, newCondition)
	return c.localClient.Status().Update(ctx, cluster)
}

func (c *clustersReconciler) runGC(ctx context.Context) {
	log := ctrl.LoggerFrom(ctx)
	if c.gcInterval == 0 {
		log.V(2).Info("Garbage Collection is disabled")
		return
	}
	log.V(2).Info("Starting Garbage Collector")
	for {
		select {
		case <-ctx.Done():
			log.V(2).Info("Garbage Collector Stopped")
			return
		case <-time.After(c.gcInterval):
			log.V(5).Info("Run Garbage Collection")
			for clusterName, rc := range c.clients {
				rc.runGC(ctrl.LoggerInto(ctx, log.WithValues("multiKueueCluster", clusterName)))
			}
		}
	}
}

// +kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=multikueueclusters,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=multikueueclusters/status,verbs=get;update;patch

func newClustersReconciler(c client.Client, namespace string, gcInterval time.Duration) *clustersReconciler {
	return &clustersReconciler{
		localClient:     c,
		configNamespace: namespace,
		remoteClients:   make(map[string]*remoteClient),
		wlUpdateCh:      make(chan event.GenericEvent, 10),
		gcInterval:      gcInterval,
	}
}

func (c *clustersReconciler) setupWithManager(mgr ctrl.Manager) error {
	err := mgr.Add(c)
	if err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&kueuealpha.MultiKueueCluster{}).
		Watches(&corev1.Secret{}, &secretHandler{client: c.localClient}).
		Complete(c)
}

type secretHandler struct {
	client client.Client
}

var _ handler.EventHandler = (*secretHandler)(nil)

func (s *secretHandler) Create(ctx context.Context, event event.CreateEvent, q workqueue.RateLimitingInterface) {
	secret, isSecret := event.Object.(*corev1.Secret)
	if !isSecret {
		ctrl.LoggerFrom(ctx).V(5).Error(errors.New("not a secret"), "Failure on create event")
		return
	}
	if err := s.queue(ctx, secret, q); err != nil {
		ctrl.LoggerFrom(ctx).V(5).Error(err, "Failure on create event", "secret", klog.KObj(event.Object))
	}
}

func (s *secretHandler) Update(ctx context.Context, event event.UpdateEvent, q workqueue.RateLimitingInterface) {
	secret, isSecret := event.ObjectNew.(*corev1.Secret)
	if !isSecret {
		ctrl.LoggerFrom(ctx).V(5).Error(errors.New("not a secret"), "Failure on update event")
		return
	}
	if err := s.queue(ctx, secret, q); err != nil {
		ctrl.LoggerFrom(ctx).V(5).Error(err, "Failure on update event", "secret", klog.KObj(event.ObjectOld))
	}
}

func (s *secretHandler) Delete(ctx context.Context, event event.DeleteEvent, q workqueue.RateLimitingInterface) {
	secret, isSecret := event.Object.(*corev1.Secret)
	if !isSecret {
		ctrl.LoggerFrom(ctx).V(5).Error(errors.New("not a secret"), "Failure on delete event")
		return
	}
	if err := s.queue(ctx, secret, q); err != nil {
		ctrl.LoggerFrom(ctx).V(5).Error(err, "Failure on delete event", "secret", klog.KObj(event.Object))
	}
}

func (s *secretHandler) Generic(ctx context.Context, event event.GenericEvent, q workqueue.RateLimitingInterface) {
	secret, isSecret := event.Object.(*corev1.Secret)
	if !isSecret {
		ctrl.LoggerFrom(ctx).V(5).Error(errors.New("not a secret"), "Failure on generic event")
		return
	}
	if err := s.queue(ctx, secret, q); err != nil {
		ctrl.LoggerFrom(ctx).V(5).Error(err, "Failure on generic event", "secret", klog.KObj(event.Object))
	}
}

func (s *secretHandler) queue(ctx context.Context, secret *corev1.Secret, q workqueue.RateLimitingInterface) error {
	users := &kueuealpha.MultiKueueClusterList{}
	if err := s.client.List(ctx, users, client.MatchingFields{UsingKubeConfigs: strings.Join([]string{secret.Namespace, secret.Name}, "/")}); err != nil {
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

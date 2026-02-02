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
	"errors"
	"fmt"
	"maps"
	"net"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	inventoryv1alpha1 "sigs.k8s.io/cluster-inventory-api/apis/v1alpha1"
	"sigs.k8s.io/cluster-inventory-api/pkg/credentials"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
)

const (
	eventChBufferSize = 10

	// this set will provide waiting time between 0 to 5m20s
	retryIncrement = 5 * time.Second
	retryMaxSteps  = 7
)

// retryAfter returns an exponentially increasing interval between
// 0 and 2^(retryMaxSteps-1) * retryIncrement
func retryAfter(failedAttempts uint) time.Duration {
	if failedAttempts == 0 {
		return 0
	}
	return (1 << (min(failedAttempts, retryMaxSteps) - 1)) * retryIncrement
}

type clientWithWatchBuilder func(config *clientConfig, options client.Options) (client.WithWatch, error)

type clientConfig struct {
	Kubeconfig []byte
	RestConfig *rest.Config
}

type remoteClient struct {
	clusterName  string
	localClient  client.Client
	client       client.WithWatch
	wlUpdateCh   chan<- event.GenericEvent
	watchEndedCh chan<- event.GenericEvent
	watchCancel  func()
	config       *clientConfig
	origin       string
	adapters     map[string]jobframework.MultiKueueAdapter

	connecting         atomic.Bool
	failedConnAttempts uint

	// For unit testing only. There is now need of creating fully functional remote clients in the unit tests
	// and creating valid kubeconfig content is not trivial.
	// The full client creation and usage is validated in the integration and e2e tests.
	builderOverride clientWithWatchBuilder
}

func newRemoteClient(localClient client.Client, wlUpdateCh, watchEndedCh chan<- event.GenericEvent, origin, clusterName string, adapters map[string]jobframework.MultiKueueAdapter) *remoteClient {
	rc := &remoteClient{
		clusterName:  clusterName,
		wlUpdateCh:   wlUpdateCh,
		watchEndedCh: watchEndedCh,
		localClient:  localClient,
		origin:       origin,
		adapters:     adapters,
	}
	rc.connecting.Store(true)
	return rc
}

func newClientWithWatch(config *clientConfig, options client.Options) (client.WithWatch, error) {
	if config.RestConfig != nil {
		return client.NewWithWatch(config.RestConfig, options)
	}
	restConfig, err := clientcmd.RESTConfigFromKubeConfig(config.Kubeconfig)
	if err != nil {
		return nil, err
	}
	return client.NewWithWatch(restConfig, options)
}

type workloadKueueWatcher struct{}

var _ jobframework.MultiKueueWatcher = (*workloadKueueWatcher)(nil)

func (*workloadKueueWatcher) GetEmptyList() client.ObjectList {
	return &kueue.WorkloadList{}
}

func (*workloadKueueWatcher) WorkloadKeysFor(o runtime.Object) ([]types.NamespacedName, error) {
	wl, isWl := o.(*kueue.Workload)
	if !isWl {
		return nil, errors.New("not a workload")
	}
	return []types.NamespacedName{client.ObjectKeyFromObject(wl)}, nil
}

// setConfig - will try to recreate the k8s client and restart watching if the new config is different than
// the one currently used or a reconnect was requested.
// If the encountered error is not permanent the duration after which a retry should be done is returned.
func (rc *remoteClient) setConfig(watchCtx context.Context, config *clientConfig) (*time.Duration, error) {
	configChanged := !equality.Semantic.DeepEqual(config, rc.config)
	if !configChanged && !rc.connecting.Load() {
		return nil, nil
	}

	rc.StopWatchers()
	if configChanged {
		rc.config = config
		rc.failedConnAttempts = 0
	}

	builder := newClientWithWatch
	if rc.builderOverride != nil {
		builder = rc.builderOverride
	}
	remoteClient, err := builder(config, client.Options{Scheme: rc.localClient.Scheme()})
	if err != nil {
		return nil, err
	}

	rc.client = remoteClient

	watchCtx, rc.watchCancel = context.WithCancel(watchCtx)
	err = rc.startWatcher(watchCtx, kueue.GroupVersion.WithKind("Workload").GroupKind().String(), &workloadKueueWatcher{})
	if err != nil {
		rc.failedConnAttempts++
		return ptr.To(retryAfter(rc.failedConnAttempts)), err
	}

	// add a watch for all the adapters implementing multiKueueWatcher
	for kind, adapter := range rc.adapters {
		watcher, implementsWatcher := adapter.(jobframework.MultiKueueWatcher)
		if !implementsWatcher {
			continue
		}
		err := rc.startWatcher(watchCtx, kind, watcher)
		if err != nil {
			// not being able to setup a watcher is not ideal but we can function with only the wl watcher.
			ctrl.LoggerFrom(watchCtx).Error(err, "Unable to start the watcher", "kind", kind)
			// however let's not accept this for now.
			rc.failedConnAttempts++
			return ptr.To(retryAfter(rc.failedConnAttempts)), err
		}
	}

	rc.connecting.Store(false)
	rc.failedConnAttempts = 0
	return nil, nil
}

func (rc *remoteClient) startWatcher(ctx context.Context, kind string, w jobframework.MultiKueueWatcher) error {
	log := ctrl.LoggerFrom(ctx).WithValues("watchKind", kind)
	newWatcher, err := rc.client.Watch(ctx, w.GetEmptyList(), client.MatchingLabels{kueue.MultiKueueOriginLabel: rc.origin})
	if err != nil {
		return err
	}

	go func() {
		log.V(2).Info("Starting watch")
		for r := range newWatcher.ResultChan() {
			switch r.Type {
			case watch.Error:
				switch s := r.Object.(type) {
				case *metav1.Status:
					log.V(3).Info("Watch error", "status", s.Status, "message", s.Message, "reason", s.Reason)
				default:
					log.V(3).Info("Watch error with unexpected type", "type", fmt.Sprintf("%T", s))
				}
			default:
				wlKeys, err := w.WorkloadKeysFor(r.Object)
				if err != nil {
					log.Error(err, "Cannot get workload keys", "jobKind", r.Object.GetObjectKind().GroupVersionKind())
				} else {
					for _, wlKey := range wlKeys {
						rc.queueWorkloadEvent(ctx, wlKey)
					}
				}
			}
		}
		log.V(2).Info("Watch ended", "ctxErr", ctx.Err())
		// If the context is not yet Done , queue a reconcile to attempt reconnection
		if ctx.Err() == nil {
			oldConnecting := rc.connecting.Swap(true)
			// reconnect if this is the first watch failing.
			if !oldConnecting {
				log.V(2).Info("Queue reconcile for reconnect", "cluster", rc.clusterName)
				rc.queueWatchEndedEvent(ctx)
			}
		}
	}()
	return nil
}

func (rc *remoteClient) StopWatchers() {
	if rc.watchCancel != nil {
		rc.watchCancel()
	}
}

func (rc *remoteClient) queueWorkloadEvent(ctx context.Context, wlKey types.NamespacedName) {
	localWl := &kueue.Workload{}
	if err := rc.localClient.Get(ctx, wlKey, localWl); err == nil {
		rc.wlUpdateCh <- event.GenericEvent{Object: localWl}
	} else if !apierrors.IsNotFound(err) {
		ctrl.LoggerFrom(ctx).Error(err, "reading local workload")
	}
}

func (rc *remoteClient) queueWatchEndedEvent(ctx context.Context) {
	cluster := &kueue.MultiKueueCluster{}
	if err := rc.localClient.Get(ctx, types.NamespacedName{Name: rc.clusterName}, cluster); err == nil {
		rc.watchEndedCh <- event.GenericEvent{Object: cluster}
	} else {
		ctrl.LoggerFrom(ctx).Error(err, "sending watch ended event")
	}
}

// runGC - lists all the remote workloads having the same multikueue-origin and remove those who
// no longer have a local correspondent (missing or awaiting deletion). If the remote workload
// is owned by a job, also delete the job.
func (rc *remoteClient) runGC(ctx context.Context) {
	log := ctrl.LoggerFrom(ctx)

	if rc.connecting.Load() {
		log.V(5).Info("Skip disconnected client")
		return
	}

	wls := &kueue.WorkloadList{}
	err := rc.client.List(ctx, wls, client.MatchingLabels{kueue.MultiKueueOriginLabel: rc.origin})
	if err != nil {
		log.Error(err, "Listing remote workloads")
		return
	}

	for _, remoteWl := range wls.Items {
		localWl := &kueue.Workload{}
		wlLog := log.WithValues("remoteWl", klog.KObj(&remoteWl))
		err := rc.localClient.Get(ctx, client.ObjectKeyFromObject(&remoteWl), localWl)
		if client.IgnoreNotFound(err) != nil {
			wlLog.Error(err, "Reading local workload")
			continue
		}

		if err == nil && localWl.DeletionTimestamp.IsZero() {
			// The local workload exists and isn't being deleted, so the remote workload is still relevant.
			continue
		}

		// if the remote wl has a controller(owning Job), delete the job
		if controller := metav1.GetControllerOf(&remoteWl); controller != nil {
			ownerKey := klog.KRef(remoteWl.Namespace, controller.Name)
			adapterKey := schema.FromAPIVersionAndKind(controller.APIVersion, controller.Kind).String()
			if adapter, found := rc.adapters[adapterKey]; !found {
				wlLog.V(2).Info("No adapter found", "adapterKey", adapterKey, "ownerKey", ownerKey)
			} else {
				wlLog.V(5).Info("MultiKueueGC deleting workload owner", "ownerKey", ownerKey, "ownerKind", controller)
				err := adapter.DeleteRemoteObject(ctx, rc.client, types.NamespacedName{Name: controller.Name, Namespace: remoteWl.Namespace})
				if client.IgnoreNotFound(err) != nil {
					wlLog.Error(err, "Deleting remote workload's owner", "ownerKey", ownerKey)
				}
			}
		}
		wlLog.V(5).Info("MultiKueueGC deleting remote workload")
		if err := rc.client.Delete(ctx, &remoteWl); client.IgnoreNotFound(err) != nil {
			wlLog.Error(err, "Deleting remote workload")
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

	// the multikueue-origin value used
	origin string

	// rootContext - holds the context passed by the controller-runtime on Start.
	// It's used to create child contexts for MultiKueueClusters client watch routines
	// that will gracefully end when the controller-manager stops.
	rootContext context.Context

	// For unit testing only. There is now need of creating fully functional remote clients in the unit tests
	// and creating valid kubeconfig content is not trivial.
	// The full client creation and usage is validated in the integration and e2e tests.
	builderOverride clientWithWatchBuilder

	// watchEndedCh - an event chan used to request the reconciliation of the clusters for which the watch loop
	// has ended (connection lost).
	watchEndedCh chan event.GenericEvent

	fsWatcher *KubeConfigFSWatcher

	adapters map[string]jobframework.MultiKueueAdapter

	clusterProfileCreds clusterProfileCreds

	roleTracker *roletracker.RoleTracker
}

type clusterProfileCreds interface {
	BuildConfigFromCP(clusterprofile *inventoryv1alpha1.ClusterProfile) (*rest.Config, error)
}

type NoOpClusterProfileCreds struct{}

func (NoOpClusterProfileCreds) BuildConfigFromCP(clusterprofile *inventoryv1alpha1.ClusterProfile) (*rest.Config, error) {
	return nil, errors.New("no credentials provider configured")
}

var _ clusterProfileCreds = (*credentials.CredentialsProvider)(nil)
var _ clusterProfileCreds = (*NoOpClusterProfileCreds)(nil)

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
		rc.StopWatchers()
		delete(c.remoteClients, clusterName)
	}
}

func (c *clustersReconciler) setRemoteClientConfig(ctx context.Context, clusterName string, config *clientConfig, origin string) (*time.Duration, error) {
	c.lock.Lock()
	defer c.lock.Unlock()

	client, found := c.remoteClients[clusterName]
	if !found {
		client = newRemoteClient(c.localClient, c.wlUpdateCh, c.watchEndedCh, origin, clusterName, c.adapters)
		if c.builderOverride != nil {
			client.builderOverride = c.builderOverride
		}
		c.remoteClients[clusterName] = client
	}

	clientLog := ctrl.LoggerFrom(c.rootContext).WithValues("clusterName", clusterName)
	clientCtx := ctrl.LoggerInto(c.rootContext, clientLog)

	if retryAfter, err := client.setConfig(clientCtx, config); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "failed to set kubeConfig in the remote client")
		return retryAfter, err
	}
	return nil, nil
}

func (c *clustersReconciler) controllerFor(acName string) (*remoteClient, bool) {
	c.lock.RLock()
	defer c.lock.RUnlock()

	rc, f := c.remoteClients[acName]
	return rc, f
}

func (c *clustersReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	cluster := &kueue.MultiKueueCluster{}

	err := c.localClient.Get(ctx, req.NamespacedName, cluster)
	if client.IgnoreNotFound(err) != nil {
		return reconcile.Result{}, err
	}

	log := ctrl.LoggerFrom(ctx)
	log.V(2).Info("Reconcile MultiKueueCluster")

	if err != nil || !cluster.DeletionTimestamp.IsZero() {
		c.stopAndRemoveCluster(req.Name)
		return reconcile.Result{}, nil //nolint:nilerr // nil is intentional, as either the cluster is deleted, or not found
	}

	clientConfig, reason, err := c.loadClientConfig(ctx, cluster)
	if err != nil {
		log.Error(err, "loading client config failed")
		c.stopAndRemoveCluster(req.Name)
		if updateErr := c.updateStatus(ctx, cluster, false, reason, fmt.Sprintf("load client config failed: %v", err)); updateErr != nil {
			return reconcile.Result{}, fmt.Errorf("failed to update MultiKueueCluster status: %w after failing to load client config: %w", updateErr, err)
		}
		// Skip triggering another reconciliation without config present,
		// it will be triggered again when the ClusterProfile or the Secret gets created
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("failed to load client config, reason: %s, error: %w", reason, err)
	}

	if retryAfter, err := c.setRemoteClientConfig(ctx, cluster.Name, clientConfig, c.origin); err != nil {
		log.Error(err, "setting client config", "retryAfter", retryAfter)
		if err := c.updateStatus(ctx, cluster, false, "ClientConnectionFailed", err.Error()); err != nil {
			return reconcile.Result{}, err
		} else {
			return reconcile.Result{RequeueAfter: ptr.Deref(retryAfter, 0)}, nil
		}
	}

	return reconcile.Result{}, client.IgnoreNotFound(c.updateStatus(ctx, cluster, true, "Active", "Connected"))
}

func (c *clustersReconciler) loadClientConfig(ctx context.Context, cluster *kueue.MultiKueueCluster) (*clientConfig, string, error) {
	log := ctrl.LoggerFrom(ctx)
	if cluster.Spec.ClusterSource.ClusterProfileRef != nil {
		if !features.Enabled(features.MultiKueueClusterProfile) {
			return nil, "MultiKueueClusterProfileFeatureDisabled", errors.New("MultiKueueClusterProfile feature gate is disabled")
		}
		restConfig, err := c.getRestConfigFromClusterProfile(ctx, cluster.Spec.ClusterSource.ClusterProfileRef)
		if err != nil {
			return nil, "BadClusterProfile", err
		}
		opts := validateRestConfigOptions{
			// ExecProvider is allowed for ClusterProfile credentials plugins.
			allowExecProvider: true,
		}
		if err := validateRestConfig(restConfig, opts); err != nil {
			return nil, "BadRestConfig", err
		}
		return &clientConfig{RestConfig: restConfig}, "", nil
	}

	kubeConfig, err := c.getKubeConfig(ctx, cluster.Spec.ClusterSource.KubeConfig)
	if err != nil {
		return nil, "BadKubeConfig", err
	}

	if features.Enabled(features.MultiKueueAllowInsecureKubeconfigs) {
		log.V(3).Info("Feature MultiKueueAllowInsecureKubeconfigs is enabled, skipping kubeconfig validation")
		return &clientConfig{Kubeconfig: kubeConfig}, "", nil
	}
	if err := validateKubeconfig(kubeConfig); err != nil {
		return nil, "InsecureKubeConfig", err
	}
	return &clientConfig{Kubeconfig: kubeConfig}, "", nil
}

// validateKubeconfig checks that the provided kubeconfig content is safe to use
// in a MultiKueueCluster context.
func validateKubeconfig(kubeconfig []byte) error {
	// Parse the raw kubeconfig
	config, err := clientcmd.Load(kubeconfig)
	if err != nil {
		return fmt.Errorf("failed to parse kubeconfig(apiconfig): %v", err)
	}
	if config.AuthInfos != nil {
		for _, authInfo := range config.AuthInfos {
			// Token is allowed due to service account tokens usage
			if authInfo.TokenFile != "" {
				return errors.New("tokenFile is not allowed")
			}
		}
	}
	// Validate each cluster config
	for name, cluster := range config.Clusters {
		// Require TLS
		if cluster.InsecureSkipTLSVerify {
			return fmt.Errorf("insecure TLS verification is not allowed for cluster %s", name)
		}
		// Require CA cert data (not file paths)
		if cluster.CertificateAuthority != "" {
			return fmt.Errorf("certificate-authority file paths are not allowed, use certificate-authority-data for cluster %s", name)
		}
		// Validate server URL
		if cluster.Server == "" {
			return fmt.Errorf("server URL is required for cluster %s", name)
		}
	}

	// Get the restconfig to validate the final settings
	restConfig, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return err
	}

	return validateRestConfig(restConfig, validateRestConfigOptions{})
}

type validateRestConfigOptions struct {
	allowExecProvider bool
}

func validateRestConfig(restConfig *rest.Config, opts validateRestConfigOptions) error {
	// Block dangerous auth mechanisms
	// BearerTokenFile is not allowed.
	// Instead BearerToken is allowed due to service account tokens usage.
	if restConfig.BearerTokenFile != "" {
		return errors.New("bearerTokenFile is not allowed")
	}
	// lowercase the host to allow FQDN with uppercase letters
	if !isValidHostname(strings.ToLower(restConfig.Host)) {
		return errors.New("untrusted server endpoint")
	}
	if restConfig.Username != "" || restConfig.Password != "" {
		return errors.New("basic auth is not allowed")
	}
	if restConfig.ExecProvider != nil && !opts.allowExecProvider {
		return errors.New("exec plugins are not allowed")
	}
	if restConfig.AuthProvider != nil {
		return errors.New("auth providers are not allowed")
	}
	return nil
}

func isValidHostname(server string) bool {
	u, err := url.Parse(server)
	if err != nil {
		return false
	}
	host, _, err := net.SplitHostPort(u.Host)
	if err != nil {
		host = u.Host // If no port is present
	}
	// Check if host is a valid IP
	if net.ParseIP(host) != nil {
		return true
	}
	// Check if host is a valid DNS hostname
	errs := validation.IsDNS1123Subdomain(host)
	return len(errs) == 0
}

func (c *clustersReconciler) getKubeConfig(ctx context.Context, ref *kueue.KubeConfig) ([]byte, error) {
	if ref == nil {
		return nil, errors.New("kubeconfig reference is nil")
	}

	if ref.LocationType == kueue.SecretLocationType {
		return c.getKubeConfigFromSecret(ctx, ref.Location)
	}
	// Otherwise it's path
	return c.getKubeConfigFromPath(ref.Location)
}

func (c *clustersReconciler) getRestConfigFromClusterProfile(ctx context.Context, profileRef *kueue.ClusterProfileReference) (*rest.Config, error) {
	cp := &inventoryv1alpha1.ClusterProfile{}
	if err := c.localClient.Get(ctx, types.NamespacedName{Name: profileRef.Name, Namespace: c.configNamespace}, cp); err != nil {
		return nil, err
	}

	return c.clusterProfileCreds.BuildConfigFromCP(cp)
}

func (c *clustersReconciler) getKubeConfigFromSecret(ctx context.Context, secretName string) ([]byte, error) {
	sec := corev1.Secret{}
	secretObjKey := types.NamespacedName{
		Namespace: c.configNamespace,
		Name:      secretName,
	}
	err := c.localClient.Get(ctx, secretObjKey, &sec)
	if err != nil {
		return nil, err
	}

	kconfigBytes, found := sec.Data[kueue.MultiKueueConfigSecretKey]
	if !found {
		return nil, fmt.Errorf("key %q not found in secret %q", kueue.MultiKueueConfigSecretKey, secretName)
	}

	return kconfigBytes, nil
}

func (c *clustersReconciler) getKubeConfigFromPath(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func (c *clustersReconciler) updateStatus(ctx context.Context, cluster *kueue.MultiKueueCluster, active bool, reason, message string) error {
	newCondition := metav1.Condition{
		Type:               kueue.MultiKueueClusterActive,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: cluster.Generation,
	}
	if active {
		newCondition.Status = metav1.ConditionTrue
	}

	// if the condition is up-to-date
	oldCondition := apimeta.FindStatusCondition(cluster.Status.Conditions, kueue.MultiKueueClusterActive)
	if cmpConditionState(oldCondition, &newCondition) {
		return nil
	}

	apimeta.SetStatusCondition(&cluster.Status.Conditions, newCondition)
	return c.localClient.Status().Update(ctx, cluster)
}

func (c *clustersReconciler) runGC(ctx context.Context) {
	log := ctrl.LoggerFrom(ctx).WithName("MultiKueueGC")
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
			log.V(4).Info("Run Garbage Collection for Lost Remote Workloads")
			for _, rc := range c.getRemoteClients() {
				rc.runGC(ctrl.LoggerInto(ctx, log.WithValues("multiKueueCluster", rc.clusterName)))
			}
		}
	}
}

func (c *clustersReconciler) getRemoteClients() []*remoteClient {
	c.lock.RLock()
	defer c.lock.RUnlock()
	return slices.Collect(maps.Values(c.remoteClients))
}

// +kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=multikueueclusters,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=multikueueclusters/status,verbs=get;update;patch

func newClustersReconciler(c client.Client, namespace string, gcInterval time.Duration, origin string, fsWatcher *KubeConfigFSWatcher, adapters map[string]jobframework.MultiKueueAdapter, cpCreds clusterProfileCreds, roleTracker *roletracker.RoleTracker) *clustersReconciler {
	return &clustersReconciler{
		localClient:         c,
		configNamespace:     namespace,
		remoteClients:       make(map[string]*remoteClient),
		wlUpdateCh:          make(chan event.GenericEvent, eventChBufferSize),
		gcInterval:          gcInterval,
		origin:              origin,
		watchEndedCh:        make(chan event.GenericEvent, eventChBufferSize),
		fsWatcher:           fsWatcher,
		adapters:            adapters,
		clusterProfileCreds: cpCreds,
		roleTracker:         roleTracker,
	}
}

func (c *clustersReconciler) setupWithManager(mgr ctrl.Manager) error {
	err := mgr.Add(c)
	if err != nil {
		return err
	}

	syncHndl := handler.Funcs{
		GenericFunc: func(_ context.Context, e event.GenericEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			q.Add(reconcile.Request{NamespacedName: types.NamespacedName{
				Name: e.Object.GetName(),
			}})
		},
	}

	fsWatcherHndl := handler.Funcs{
		GenericFunc: func(_ context.Context, e event.GenericEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			// batch the events
			q.AddAfter(reconcile.Request{NamespacedName: types.NamespacedName{
				Name: e.Object.GetName(),
			}}, 100*time.Millisecond)
		},
	}

	filterLog := mgr.GetLogger().WithName("MultiKueueCluster filter")
	filter := predicate.Funcs{
		CreateFunc: func(ce event.CreateEvent) bool {
			if cluster, isCluster := ce.Object.(*kueue.MultiKueueCluster); isCluster {
				if cluster.Spec.ClusterSource.KubeConfig != nil && cluster.Spec.ClusterSource.KubeConfig.LocationType == kueue.PathLocationType {
					err := c.fsWatcher.AddOrUpdate(cluster.Name, cluster.Spec.ClusterSource.KubeConfig.Location)
					if err != nil {
						filterLog.Error(err, "AddOrUpdate FS watch", "cluster", klog.KObj(cluster))
					}
				}
			}
			return true
		},
		UpdateFunc: func(ue event.UpdateEvent) bool {
			clusterNew, isClusterNew := ue.ObjectNew.(*kueue.MultiKueueCluster)
			clusterOld, isClusterOld := ue.ObjectOld.(*kueue.MultiKueueCluster)
			if !isClusterNew || !isClusterOld {
				return true
			}

			clusterNewHasKubeConfigPath := clusterNew.Spec.ClusterSource.KubeConfig != nil && clusterNew.Spec.ClusterSource.KubeConfig.LocationType == kueue.PathLocationType
			clusterOldHasKubeConfigPath := clusterOld.Spec.ClusterSource.KubeConfig != nil && clusterOld.Spec.ClusterSource.KubeConfig.LocationType == kueue.PathLocationType
			if clusterOldHasKubeConfigPath && !clusterNewHasKubeConfigPath {
				err := c.fsWatcher.Remove(clusterOld.Name)
				if err != nil {
					filterLog.Error(err, "Remove FS watch", "cluster", klog.KObj(clusterOld))
				}
			}

			if clusterNewHasKubeConfigPath {
				err := c.fsWatcher.AddOrUpdate(clusterNew.Name, clusterNew.Spec.ClusterSource.KubeConfig.Location)
				if err != nil {
					filterLog.Error(err, "AddOrUpdate FS watch", "cluster", klog.KObj(clusterNew))
				}
			}
			return true
		},
		DeleteFunc: func(de event.DeleteEvent) bool {
			if cluster, isCluster := de.Object.(*kueue.MultiKueueCluster); isCluster {
				if cluster.Spec.ClusterSource.KubeConfig != nil && cluster.Spec.ClusterSource.KubeConfig.LocationType == kueue.PathLocationType {
					err := c.fsWatcher.Remove(cluster.Name)
					if err != nil {
						filterLog.Error(err, "Remove FS watch", "cluster", klog.KObj(cluster))
					}
				}
			}
			return true
		},
	}

	controllerBuilder := ctrl.NewControllerManagedBy(mgr).
		Named("multikueue_cluster").
		For(&kueue.MultiKueueCluster{}).
		Watches(&corev1.Secret{}, &secretHandler{client: c.localClient}).
		WatchesRawSource(source.Channel(c.watchEndedCh, syncHndl)).
		WatchesRawSource(source.Channel(c.fsWatcher.reconcile, fsWatcherHndl)).
		WithEventFilter(filter).
		WithOptions(controller.Options{
			LogConstructor: roletracker.NewLogConstructor(c.roleTracker, "multikueue-cluster"),
		})
	if features.Enabled(features.MultiKueueClusterProfile) {
		systemNamespacePredicate := predicate.NewPredicateFuncs(func(obj client.Object) bool {
			return obj.GetNamespace() == c.configNamespace
		})

		controllerBuilder = controllerBuilder.
			Watches(&inventoryv1alpha1.ClusterProfile{}, &clusterProfileHandler{client: c.localClient}, builder.WithPredicates(systemNamespacePredicate))
	}

	return controllerBuilder.Complete(c)
}

type secretHandler struct {
	client client.Client
}

var _ handler.EventHandler = (*secretHandler)(nil)

func (s *secretHandler) Create(ctx context.Context, event event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	secret, isSecret := event.Object.(*corev1.Secret)
	if !isSecret {
		ctrl.LoggerFrom(ctx).V(5).Error(errors.New("not a secret"), "Failure on create event")
		return
	}
	if err := s.queue(ctx, secret, q); err != nil {
		ctrl.LoggerFrom(ctx).V(5).Error(err, "Failure on create event", "secret", klog.KObj(event.Object))
	}
}

func (s *secretHandler) Update(ctx context.Context, event event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	secret, isSecret := event.ObjectNew.(*corev1.Secret)
	if !isSecret {
		ctrl.LoggerFrom(ctx).V(5).Error(errors.New("not a secret"), "Failure on update event")
		return
	}
	if err := s.queue(ctx, secret, q); err != nil {
		ctrl.LoggerFrom(ctx).V(5).Error(err, "Failure on update event", "secret", klog.KObj(event.ObjectOld))
	}
}

func (s *secretHandler) Delete(ctx context.Context, event event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	secret, isSecret := event.Object.(*corev1.Secret)
	if !isSecret {
		ctrl.LoggerFrom(ctx).V(5).Error(errors.New("not a secret"), "Failure on delete event")
		return
	}
	if err := s.queue(ctx, secret, q); err != nil {
		ctrl.LoggerFrom(ctx).V(5).Error(err, "Failure on delete event", "secret", klog.KObj(event.Object))
	}
}

func (s *secretHandler) Generic(ctx context.Context, event event.GenericEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	secret, isSecret := event.Object.(*corev1.Secret)
	if !isSecret {
		ctrl.LoggerFrom(ctx).V(5).Error(errors.New("not a secret"), "Failure on generic event")
		return
	}
	if err := s.queue(ctx, secret, q); err != nil {
		ctrl.LoggerFrom(ctx).V(5).Error(err, "Failure on generic event", "secret", klog.KObj(event.Object))
	}
}

func (s *secretHandler) queue(ctx context.Context, secret *corev1.Secret, q workqueue.TypedRateLimitingInterface[reconcile.Request]) error {
	users := &kueue.MultiKueueClusterList{}
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

type clusterProfileHandler struct {
	client client.Client
}

var _ handler.EventHandler = (*clusterProfileHandler)(nil)

func (cp *clusterProfileHandler) Create(ctx context.Context, event event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	cp.handleEvent(ctx, event.Object, q)
}

func (cp *clusterProfileHandler) Update(ctx context.Context, event event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	cp.handleEvent(ctx, event.ObjectNew, q)
}

func (cp *clusterProfileHandler) Delete(ctx context.Context, event event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	cp.handleEvent(ctx, event.Object, q)
}

func (cp *clusterProfileHandler) Generic(ctx context.Context, event event.GenericEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (cp *clusterProfileHandler) handleEvent(ctx context.Context, object client.Object, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	clusterprofile, isClusterProfile := object.(*inventoryv1alpha1.ClusterProfile)
	if !isClusterProfile {
		ctrl.LoggerFrom(ctx).Error(errors.New("not a clusterprofile"), "Failure on event")
		return
	}

	mkcList := &kueue.MultiKueueClusterList{}
	if err := cp.client.List(ctx, mkcList, client.MatchingFields{UsingClusterProfiles: strings.Join([]string{clusterprofile.Namespace, clusterprofile.Name}, "/")}); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "Failure on event", "clusterprofile", klog.KObj(object))
		return
	}

	for _, mkc := range mkcList.Items {
		req := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name: mkc.Name,
			},
		}
		q.Add(req)
	}
}

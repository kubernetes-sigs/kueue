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

package tas

import (
	"context"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

const (
	// DefaultRequeueBatchPeriod is the delay for batching requeue requests.
	DefaultRequeueBatchPeriod = time.Minute
)

// nonTasReconcilerOption configures the NonTasUsageReconciler.
type nonTasReconcilerOption func(*NonTasUsageReconciler)

// withRequeueBatchPeriod sets the batch period for requeue requests.
func withRequeueBatchPeriod(d time.Duration) nonTasReconcilerOption {
	return func(r *NonTasUsageReconciler) {
		r.requeueBatchPeriod = d
	}
}

func newNonTasUsageReconciler(k8sClient client.Client, queues *qcache.Manager, cache *schdcache.Cache, opts ...nonTasReconcilerOption) *NonTasUsageReconciler {
	r := &NonTasUsageReconciler{
		k8sClient:          k8sClient,
		queues:             queues,
		cache:              cache,
		requeueBatchPeriod: DefaultRequeueBatchPeriod,
		requeueWorkqueue:   workqueue.NewTypedDelayingQueue[struct{}](),
		pendingNodes:       sets.New[string](),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// NonTasUsageReconciler monitors non-TAS pods to update TAS cache usage
// and requeue inadmissible workloads when capacity is released.
type NonTasUsageReconciler struct {
	k8sClient   client.Client
	queues      *qcache.Manager
	cache       *schdcache.Cache
	roleTracker *roletracker.RoleTracker

	requeueBatchPeriod time.Duration
	requeueWorkqueue   workqueue.TypedDelayingInterface[struct{}]

	requeueLock      sync.Mutex
	pendingNodes     sets.Set[string]
	requeueScheduled bool
}

var _ reconcile.Reconciler = (*NonTasUsageReconciler)(nil)
var _ predicate.TypedPredicate[*corev1.Pod] = (*NonTasUsageReconciler)(nil)

//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
//+kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list;watch

func (r *NonTasUsageReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := klog.FromContext(ctx).WithValues("pod", req.NamespacedName)
	log.V(3).Info("Non-TAS usage cache reconciling")
	var pod corev1.Pod
	if err := r.k8sClient.Get(ctx, req.NamespacedName, &pod); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, err
		}
		log.V(5).Info("Deleting not found pod from cache")
		if nodeName := r.cache.TASCache().DeletePodByKey(req.NamespacedName); nodeName != "" {
			r.scheduleRequeue(nodeName)
		}
		return ctrl.Result{}, nil
	}

	removed, nodeName := r.cache.TASCache().Update(&pod, log)
	if removed && nodeName != "" {
		r.scheduleRequeue(nodeName)
	}
	return ctrl.Result{}, nil
}

func (r *NonTasUsageReconciler) scheduleRequeue(nodeName string) {
	r.requeueLock.Lock()
	defer r.requeueLock.Unlock()
	if r.pendingNodes.Has(nodeName) {
		return
	}
	klog.V(5).InfoS("Scheduling requeue for freed node capacity", "node", nodeName)
	r.pendingNodes.Insert(nodeName)
	if !r.requeueScheduled {
		r.requeueScheduled = true
		r.requeueWorkqueue.AddAfter(struct{}{}, r.requeueBatchPeriod)
	}
}

func (r *NonTasUsageReconciler) requeueWorker(ctx context.Context) {
	for r.processNextRequeueItem(ctx) {
	}
}

func (r *NonTasUsageReconciler) processNextRequeueItem(ctx context.Context) bool {
	item, shutdown := r.requeueWorkqueue.Get()
	if shutdown {
		return false
	}
	defer r.requeueWorkqueue.Done(item)

	nodeNames := r.drainPendingNodes()
	if len(nodeNames) == 0 {
		return true
	}

	cqNames := r.clusterQueuesForNodes(ctx, nodeNames)
	if len(cqNames) == 0 {
		return true
	}
	log := klog.FromContext(ctx)
	log.V(3).Info("Requeuing inadmissible workloads after non-TAS capacity release",
		"clusterQueues", len(cqNames), "nodes", len(nodeNames))
	r.queues.QueueInadmissibleWorkloads(ctx, cqNames)
	return true
}

func (r *NonTasUsageReconciler) drainPendingNodes() []string {
	r.requeueLock.Lock()
	defer r.requeueLock.Unlock()
	r.requeueScheduled = false
	if len(r.pendingNodes) == 0 {
		return nil
	}
	nodes := r.pendingNodes.UnsortedList()
	r.pendingNodes = sets.New[string]()
	return nodes
}

func (r *NonTasUsageReconciler) clusterQueuesForNodes(ctx context.Context, nodeNames []string) sets.Set[kueue.ClusterQueueReference] {
	flavors := r.cache.TASCache().FlavorsForNodes(ctx, nodeNames)
	if len(flavors) == 0 {
		return nil
	}
	cqNames := sets.New[kueue.ClusterQueueReference]()
	for _, flavor := range flavors {
		for _, cq := range r.cache.ClusterQueuesUsingFlavor(flavor) {
			cqNames.Insert(cq)
		}
	}
	return cqNames
}

func filterPod(pod *corev1.Pod) bool {
	if utiltas.IsTAS(pod) {
		return false
	}
	if pod.Spec.NodeName == "" {
		return false
	}
	return true
}

func (r *NonTasUsageReconciler) Create(e event.TypedCreateEvent[*corev1.Pod]) bool {
	return filterPod(e.Object)
}

func (r *NonTasUsageReconciler) Update(e event.TypedUpdateEvent[*corev1.Pod]) bool {
	return filterPod(e.ObjectNew)
}

func (r *NonTasUsageReconciler) Delete(e event.TypedDeleteEvent[*corev1.Pod]) bool {
	return filterPod(e.Object)
}

func (r *NonTasUsageReconciler) Generic(event.TypedGenericEvent[*corev1.Pod]) bool {
	return false
}

func (r *NonTasUsageReconciler) SetupWithManager(mgr ctrl.Manager) (string, error) {
	if err := mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		log := ctrl.Log.WithName(TASNonTasUsageController).WithName("requeue-worker")
		ctx = klog.NewContext(ctx, log)

		go func() {
			<-ctx.Done()
			r.requeueWorkqueue.ShutDown()
		}()

		r.requeueWorker(ctx)
		return nil
	})); err != nil {
		return TASNonTasUsageController, err
	}

	return TASNonTasUsageController, ctrl.NewControllerManagedBy(mgr).
		Named(TASNonTasUsageController).
		WatchesRawSource(source.TypedKind(
			mgr.GetCache(),
			&corev1.Pod{},
			&handler.TypedEnqueueRequestForObject[*corev1.Pod]{},
			r,
		)).
		WithOptions(controller.Options{
			NeedLeaderElection:      ptr.To(false),
			MaxConcurrentReconciles: mgr.GetControllerOptions().GroupKindConcurrency[corev1.SchemeGroupVersion.WithKind("Pod").GroupKind().String()],
		}).
		WithLogConstructor(roletracker.NewLogConstructor(r.roleTracker, TASNonTasUsageController)).
		Complete(r)
}

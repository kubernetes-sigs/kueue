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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
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
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

const defaultRequeueBatchInterval = 10 * time.Second

func newNonTasUsageReconciler(k8sClient client.Client, queues *qcache.Manager, cache *schdcache.Cache, roleTracker *roletracker.RoleTracker) *NonTasUsageReconciler {
	return &NonTasUsageReconciler{
		k8sClient:            k8sClient,
		queues:               queues,
		cache:                cache,
		roleTracker:          roleTracker,
		requeueBatchInterval: defaultRequeueBatchInterval,
		pending:              pendingRequeue{nodes: sets.New[string]()},
	}
}

// pendingRequeue collects freed node names under a mutex and
// swaps the set atomically on drain.
type pendingRequeue struct {
	mu    sync.Mutex
	nodes sets.Set[string]
}

func (p *pendingRequeue) notify(nodeName string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nodes.Insert(nodeName)
}

func (p *pendingRequeue) drain() sets.Set[string] {
	p.mu.Lock()
	defer p.mu.Unlock()
	nodes := p.nodes
	p.nodes = sets.New[string]()
	return nodes
}

// NonTasUsageReconciler monitors pods to update the TAS cache with non-TAS
// usage and to requeue inadmissible workloads when capacity is freed.
type NonTasUsageReconciler struct {
	k8sClient   client.Client
	cache       *schdcache.Cache
	queues      *qcache.Manager
	roleTracker *roletracker.RoleTracker

	requeueBatchInterval time.Duration
	pending              pendingRequeue
}

var _ reconcile.Reconciler = (*NonTasUsageReconciler)(nil)
var _ predicate.TypedPredicate[*corev1.Pod] = (*NonTasUsageReconciler)(nil)
var _ manager.Runnable = (*NonTasUsageReconciler)(nil)
var _ manager.LeaderElectionRunnable = (*NonTasUsageReconciler)(nil)

func (r *NonTasUsageReconciler) NeedLeaderElection() bool {
	return false
}

//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch

func (r *NonTasUsageReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := klog.FromContext(ctx).WithValues("pod", req.NamespacedName)
	log.V(3).Info("Non-TAS usage cache reconciling")
	var pod corev1.Pod
	err := r.k8sClient.Get(ctx, req.NamespacedName, &pod)
	if err != nil {
		if client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, err
		}
		log.V(5).Info("Idempotently deleting not found pod")
		if removedNode := r.cache.TASCache().DeletePodByKey(req.NamespacedName, log); removedNode != "" {
			r.notifyFreedNode(removedNode)
		}
		return ctrl.Result{}, nil
	}

	if belongsToNonTASCache(&pod) {
		if freedNode := r.cache.TASCache().Update(&pod, log); freedNode != "" {
			r.notifyFreedNode(freedNode)
		}
	} else {
		if removedNode := r.cache.TASCache().DeletePodByKey(req.NamespacedName, log); removedNode != "" {
			r.notifyFreedNode(removedNode)
		}
	}
	return ctrl.Result{}, nil
}

func belongsToNonTASCache(pod *corev1.Pod) bool {
	if pod == nil {
		return false
	}
	if utiltas.IsTAS(pod) {
		return false
	}
	if len(pod.Spec.NodeName) == 0 {
		// Skip unscheduled pods as they don't use any capacity.
		return false
	}
	if utilpod.IsTerminated(pod) {
		return false
	}
	return true
}

func (r *NonTasUsageReconciler) notifyFreedNode(nodeName string) {
	r.pending.notify(nodeName)
}

// Start implements the Runnable interface. It drains accumulated freed node
// names at a slow interval to avoid clearing equivalence hashes on every pod
// event during sustained non-TAS pod churn.
func (r *NonTasUsageReconciler) Start(ctx context.Context) error {
	log := ctrl.LoggerFrom(ctx).WithName("non-tas-usage-requeue-drainer")
	ctx = ctrl.LoggerInto(ctx, log)
	ticker := time.NewTicker(r.requeueBatchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			r.drainPendingNodes(ctx)
		}
	}
}

func (r *NonTasUsageReconciler) drainPendingNodes(ctx context.Context) {
	nodes := r.pending.drain()

	if nodes.Len() == 0 {
		return
	}

	log := ctrl.LoggerFrom(ctx)
	cqNames := sets.New[kueue.ClusterQueueReference]()
	flavorCache := r.cache.CloneTASCache()
	for nodeName := range nodes {
		var node corev1.Node
		if err := r.k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, &node); err != nil {
			if client.IgnoreNotFound(err) == nil {
				log.V(3).
					Info("Node deleted, skipping requeue (RF controller handles this)", "node", nodeName)
				continue
			}
			log.Error(
				err,
				"Failed to get node for requeue, will retry next cycle",
				"node",
				nodeName,
			)
			r.notifyFreedNode(nodeName)
			continue
		}
		for name, fc := range flavorCache {
			if utiltas.NodeMatchesFlavor(node.Labels, fc.NodeLabels(), fc.TopologyLevels()) {
				for _, cq := range r.cache.ClusterQueuesUsingFlavor(name) {
					cqNames.Insert(cq)
				}
			}
		}
	}
	if cqNames.Len() > 0 {
		log.V(3).
			Info("Requeueing inadmissible workloads after non-TAS pod freed capacity",
				"nodes", sets.List(nodes), "clusterQueues", sets.List(cqNames))
		qcache.NotifyRetryInadmissible(r.queues, cqNames)
	}
}

func (r *NonTasUsageReconciler) Create(e event.TypedCreateEvent[*corev1.Pod]) bool {
	return belongsToNonTASCache(e.Object)
}

func (r *NonTasUsageReconciler) Update(e event.TypedUpdateEvent[*corev1.Pod]) bool {
	return belongsToNonTASCache(e.ObjectOld) != belongsToNonTASCache(e.ObjectNew)
}

func (r *NonTasUsageReconciler) Delete(e event.TypedDeleteEvent[*corev1.Pod]) bool {
	// Don't filter on terminal phase: if the informer skips the Running→Terminated
	// Update and delivers only the Delete event with the pod already Succeeded/Failed,
	// the pod's usage would never be removed from the cache. DeletePodByKey is
	// idempotent, so double-removal is safe.
	return len(e.Object.Spec.NodeName) > 0 && !utiltas.IsTAS(e.Object)
}

func (r *NonTasUsageReconciler) Generic(event.TypedGenericEvent[*corev1.Pod]) bool {
	return false
}

func (r *NonTasUsageReconciler) SetupWithManager(mgr ctrl.Manager) (string, error) {
	return TASNonTasUsageController, ctrl.NewControllerManagedBy(mgr).
		Named(TASNonTasUsageController).
		WatchesRawSource(source.TypedKind(
			mgr.GetCache(),
			&corev1.Pod{},
			&handler.TypedEnqueueRequestForObject[*corev1.Pod]{},
			r,
		)).
		WithOptions(controller.Options{
			NeedLeaderElection:      new(false),
			MaxConcurrentReconciles: mgr.GetControllerOptions().GroupKindConcurrency[corev1.SchemeGroupVersion.WithKind("Pod").GroupKind().String()],
		}).
		WithLogConstructor(roletracker.NewLogConstructor(r.roleTracker, TASNonTasUsageController)).
		Complete(r)
}

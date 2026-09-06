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
	"maps"
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
	"sigs.k8s.io/kueue/pkg/resources"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

const defaultRequeueBatchInterval = 10 * time.Second

type podUsageOption func(*PodUsageReconciler)

func withRequeueBatchInterval(d time.Duration) podUsageOption {
	return func(r *PodUsageReconciler) {
		if d > 0 {
			r.requeueBatchInterval = d
		}
	}
}

func newPodUsageReconciler(k8sClient client.Client, queues *qcache.Manager, cache *schdcache.Cache, roleTracker *roletracker.RoleTracker, opts ...podUsageOption) *PodUsageReconciler {
	r := &PodUsageReconciler{
		k8sClient:            k8sClient,
		queues:               queues,
		cache:                cache,
		roleTracker:          roleTracker,
		requeueBatchInterval: defaultRequeueBatchInterval,
		pending:              pendingRequeue{nodes: sets.New[string]()},
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
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

// PodUsageReconciler monitors all scheduled pods to update the TAS cache
// with non-TAS resource usage, to feed the scheduling simulator with pod
// state for feasibility checks, and to requeue inadmissible workloads when
// capacity is freed.
type PodUsageReconciler struct {
	k8sClient   client.Client
	cache       *schdcache.Cache
	queues      *qcache.Manager
	roleTracker *roletracker.RoleTracker

	requeueBatchInterval time.Duration
	pending              pendingRequeue
}

var _ reconcile.Reconciler = (*PodUsageReconciler)(nil)
var _ predicate.TypedPredicate[*corev1.Pod] = (*PodUsageReconciler)(nil)
var _ manager.Runnable = (*PodUsageReconciler)(nil)
var _ manager.LeaderElectionRunnable = (*PodUsageReconciler)(nil)

func (r *PodUsageReconciler) NeedLeaderElection() bool {
	return false
}

//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch

func (r *PodUsageReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := klog.FromContext(ctx).WithValues("pod", req.NamespacedName)
	log.V(3).Info("Pod usage cache reconciling")
	var pod corev1.Pod
	err := r.k8sClient.Get(ctx, req.NamespacedName, &pod)
	if err != nil {
		if client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, err
		}
		log.V(5).Info("Idempotently deleting not found pod")
		if removedNode := r.cache.TASCache().DeleteNonTASUsageByKey(req.NamespacedName, log); removedNode != "" {
			r.notifyFreedNode(removedNode)
		}
		r.cache.TASCache().UntrackPod(ctx, req.NamespacedName)
		return ctrl.Result{}, nil
	}

	if isScheduledAndRunning(&pod) {
		r.cache.TASCache().TrackPod(ctx, &pod)
	} else {
		r.cache.TASCache().UntrackPod(ctx, req.NamespacedName)
	}

	if belongsToNonTASCache(&pod) {
		if freedNode := r.cache.TASCache().UpdateNonTASUsage(&pod, log); freedNode != "" {
			r.notifyFreedNode(freedNode)
		}
	} else {
		if removedNode := r.cache.TASCache().DeleteNonTASUsageByKey(req.NamespacedName, log); removedNode != "" {
			r.notifyFreedNode(removedNode)
		}
	}
	return ctrl.Result{}, nil
}

func isScheduledAndRunning(pod *corev1.Pod) bool {
	return pod != nil && len(pod.Spec.NodeName) > 0 && !utilpod.IsTerminated(pod)
}

func belongsToNonTASCache(pod *corev1.Pod) bool {
	if pod == nil {
		return false
	}
	if utiltas.IsTAS(pod) {
		return false
	}
	if len(pod.Spec.NodeName) == 0 {
		return false
	}
	if utilpod.IsTerminated(pod) {
		return false
	}
	return true
}

func (r *PodUsageReconciler) notifyFreedNode(nodeName string) {
	r.pending.notify(nodeName)
}

// Start implements the Runnable interface. It drains accumulated freed node
// names at a slow interval to avoid clearing equivalence hashes on every pod
// event during sustained non-TAS pod churn.
func (r *PodUsageReconciler) Start(ctx context.Context) error {
	log := ctrl.LoggerFrom(ctx).WithName("pod-usage-requeue-drainer")
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

func (r *PodUsageReconciler) drainPendingNodes(ctx context.Context) {
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
		if logV := log.V(3); logV.Enabled() {
			logV.Info("Requeueing inadmissible workloads after non-TAS pod freed capacity",
				"nodes", sets.List(nodes), "clusterQueues", sets.List(cqNames))
		}
		qcache.NotifyRetryInadmissible(r.queues, cqNames)
	}
}

func (r *PodUsageReconciler) Create(e event.TypedCreateEvent[*corev1.Pod]) bool {
	return isScheduledAndRunning(e.Object)
}

func (r *PodUsageReconciler) Update(e event.TypedUpdateEvent[*corev1.Pod]) bool {
	return isScheduledAndRunning(e.ObjectOld) != isScheduledAndRunning(e.ObjectNew) ||
		belongsToNonTASCache(e.ObjectOld) != belongsToNonTASCache(e.ObjectNew) ||
		podUsageChanged(e.ObjectOld, e.ObjectNew)
}

func podUsageChanged(oldPod, newPod *corev1.Pod) bool {
	if !belongsToNonTASCache(oldPod) || !belongsToNonTASCache(newPod) {
		return false
	}
	if oldPod.Spec.NodeName != newPod.Spec.NodeName {
		return true
	}
	if oldPod.Generation == newPod.Generation {
		return false
	}
	return !maps.Equal(
		resources.ToMap(resources.NewRequestsFromPodSpec(&oldPod.Spec)),
		resources.ToMap(resources.NewRequestsFromPodSpec(&newPod.Spec)),
	)
}

func (r *PodUsageReconciler) Delete(e event.TypedDeleteEvent[*corev1.Pod]) bool {
	return len(e.Object.Spec.NodeName) > 0
}

func (r *PodUsageReconciler) Generic(event.TypedGenericEvent[*corev1.Pod]) bool {
	return false
}

func (r *PodUsageReconciler) SetupWithManager(mgr ctrl.Manager) (string, error) {
	return TASPodUsageController, ctrl.NewControllerManagedBy(mgr).
		Named(TASPodUsageController).
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
		WithLogConstructor(roletracker.NewLogConstructor(r.roleTracker, TASPodUsageController)).
		Complete(r)
}

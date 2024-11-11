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

package tas

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/queue"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

const (
	nodeBatchPeriod = time.Second
)

type rfReconciler struct {
	queues   *queue.Manager
	cache    *cache.Cache
	tasCache *cache.TASCache
	client   client.Client
	recorder record.EventRecorder
}

var _ reconcile.Reconciler = (*rfReconciler)(nil)
var _ predicate.Predicate = (*rfReconciler)(nil)

// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=topologies,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=resourceflavors,verbs=get;list;watch

func newRfReconciler(c client.Client, queues *queue.Manager, cache *cache.Cache, recorder record.EventRecorder) *rfReconciler {
	return &rfReconciler{
		client:   c,
		queues:   queues,
		cache:    cache,
		tasCache: cache.TASCache(),
		recorder: recorder,
	}
}

func (r *rfReconciler) setupWithManager(mgr ctrl.Manager, cache *cache.Cache, cfg *configapi.Configuration) (string, error) {
	nodeHandler := nodeHandler{
		tasCache: cache.TASCache(),
	}
	return TASResourceFlavorController, ctrl.NewControllerManagedBy(mgr).
		Named(TASResourceFlavorController).
		For(&kueue.ResourceFlavor{}).
		Watches(&corev1.Node{}, &nodeHandler).
		WithOptions(controller.Options{NeedLeaderElection: ptr.To(false)}).
		WithEventFilter(r).
		Complete(core.WithLeadingManager(mgr, r, &kueue.ClusterQueue{}, cfg))
}

var _ handler.EventHandler = (*nodeHandler)(nil)

// nodeHandler handles node update events.
type nodeHandler struct {
	tasCache *cache.TASCache
}

func (h *nodeHandler) Create(_ context.Context, e event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	node, isNode := e.Object.(*corev1.Node)
	if !isNode {
		return
	}
	h.queueReconcileForNode(node, q)
}

func (h *nodeHandler) Update(ctx context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	oldNode, isOldNode := e.ObjectOld.(*corev1.Node)
	newNode, isNewNode := e.ObjectNew.(*corev1.Node)
	if !isOldNode || !isNewNode {
		return
	}
	h.queueReconcileForNode(oldNode, q)
	h.queueReconcileForNode(newNode, q)
}

func (h *nodeHandler) Delete(_ context.Context, e event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	node, isNode := e.Object.(*corev1.Node)
	if !isNode {
		return
	}
	h.queueReconcileForNode(node, q)
}

func (h *nodeHandler) queueReconcileForNode(node *corev1.Node, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	if node == nil {
		return
	}
	// trigger reconcile for TAS flavors affected by the node being created or updated
	for name, flavor := range h.tasCache.Clone() {
		if nodeBelongsToFlavor(node, flavor.NodeLabels, flavor.Levels) {
			q.AddAfter(reconcile.Request{NamespacedName: types.NamespacedName{
				Name: string(name),
			}}, nodeBatchPeriod)
		}
	}
}

func (h *nodeHandler) Generic(context.Context, event.GenericEvent, workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (r *rfReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := ctrl.LoggerFrom(ctx).WithValues("name", req.NamespacedName.Name)
	log.V(2).Info("Reconcile TAS Resource Flavor")

	flv := &kueue.ResourceFlavor{}
	if err := r.client.Get(ctx, req.NamespacedName, flv); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return reconcile.Result{}, err
		}
		r.tasCache.Delete(kueue.ResourceFlavorReference(req.NamespacedName.Name))
	}
	if flv.Spec.TopologyName != nil {
		if r.tasCache.Get(kueue.ResourceFlavorReference(flv.Name)) == nil {
			topology := kueuealpha.Topology{}
			if err := r.client.Get(ctx, types.NamespacedName{Name: string(*flv.Spec.TopologyName)}, &topology); err != nil {
				return reconcile.Result{}, err
			}
			levels := utiltas.Levels(&topology)
			tasInfo := r.tasCache.NewTASFlavorCache(kueue.TopologyReference(topology.Name), levels, flv.Spec.NodeLabels)
			r.tasCache.Set(kueue.ResourceFlavorReference(flv.Name), tasInfo)
		}

		// requeue inadmissible workloads as a change to the resource flavor
		// or the set of nodes can allow admitting a workload which was
		// previously inadmissible.
		if cqNames := r.cache.ActiveClusterQueues(); len(cqNames) > 0 {
			r.queues.QueueInadmissibleWorkloads(ctx, cqNames)
		}
	}
	return reconcile.Result{}, nil
}

func (r *rfReconciler) Create(event event.CreateEvent) bool {
	rf, isRf := event.Object.(*kueue.ResourceFlavor)
	if isRf {
		return rf.Spec.TopologyName != nil
	}
	return true
}

func (r *rfReconciler) Delete(event event.DeleteEvent) bool {
	rf, isRf := event.Object.(*kueue.ResourceFlavor)
	if isRf {
		if rf.Spec.TopologyName != nil {
			r.tasCache.Delete(kueue.ResourceFlavorReference(rf.Name))
		}
		return false
	}
	return true
}

func (r *rfReconciler) Update(event event.UpdateEvent) bool {
	oldRf, isOldRf := event.ObjectOld.(*kueue.ResourceFlavor)
	newRf, isNewRf := event.ObjectNew.(*kueue.ResourceFlavor)
	if isOldRf && isNewRf {
		switch {
		case ptr.Equal(oldRf.Spec.TopologyName, newRf.Spec.TopologyName):
			return false
		case oldRf.Spec.TopologyName == nil:
			return true
		default:
			// topologyName was set so is changed or removed
			r.tasCache.Delete(kueue.ResourceFlavorReference(*oldRf.Spec.TopologyName))
			return newRf.Spec.TopologyName != nil
		}
	}
	return true
}

func (r *rfReconciler) Generic(event event.GenericEvent) bool {
	return false
}

func nodeBelongsToFlavor(node *corev1.Node, nodeLabels map[string]string, levels []string) bool {
	for k, v := range nodeLabels {
		if node.Labels[k] != v {
			return false
		}
	}
	for i := range levels {
		if _, ok := node.Labels[levels[i]]; !ok {
			return false
		}
	}
	return true
}

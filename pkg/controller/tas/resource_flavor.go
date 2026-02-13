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

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
)

type rfReconciler struct {
	logName     string
	queues      *qcache.Manager
	cache       *schdcache.Cache
	client      client.Client
	recorder    record.EventRecorder
	roleTracker *roletracker.RoleTracker
}

var _ reconcile.Reconciler = (*rfReconciler)(nil)
var _ predicate.TypedPredicate[*kueue.ResourceFlavor] = (*rfReconciler)(nil)

// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=topologies,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=resourceflavors,verbs=get;list;watch

func newRfReconciler(c client.Client, queues *qcache.Manager, cache *schdcache.Cache, recorder record.EventRecorder, roleTracker *roletracker.RoleTracker) *rfReconciler {
	return &rfReconciler{
		logName:     TASResourceFlavorController,
		client:      c,
		queues:      queues,
		cache:       cache,
		recorder:    recorder,
		roleTracker: roleTracker,
	}
}

func (r *rfReconciler) logger() logr.Logger {
	return roletracker.WithReplicaRole(ctrl.Log.WithName(r.logName), r.roleTracker)
}

func (r *rfReconciler) setupWithManager(mgr ctrl.Manager, cache *schdcache.Cache, cfg *configapi.Configuration) (string, error) {
	nodeHandler := nodeHandler{
		cache: cache,
	}
	return TASResourceFlavorController, builder.TypedControllerManagedBy[reconcile.Request](mgr).
		Named("tas_resource_flavor_controller").
		WatchesRawSource(source.TypedKind(
			mgr.GetCache(),
			&kueue.ResourceFlavor{},
			&handler.TypedEnqueueRequestForObject[*kueue.ResourceFlavor]{},
			r,
		)).
		Watches(&corev1.Node{}, &nodeHandler).
		WithOptions(controller.Options{
			NeedLeaderElection:      ptr.To(false),
			MaxConcurrentReconciles: mgr.GetControllerOptions().GroupKindConcurrency[kueue.GroupVersion.WithKind("ResourceFlavor").GroupKind().String()],
		}).
		WithLogConstructor(roletracker.NewLogConstructor(r.roleTracker, TASResourceFlavorController)).
		Complete(core.WithLeadingManager(mgr, r, &kueue.ResourceFlavor{}, cfg))
}

var _ handler.EventHandler = (*nodeHandler)(nil)

// nodeHandler handles node update events.
type nodeHandler struct {
	cache *schdcache.Cache
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

	if checkNodeSchedulingPropertiesChanged(newNode, oldNode) == nodeUnchanged {
		ctrl.LoggerFrom(ctx).V(5).Info("Skipping node update as scheduling properties are unchanged", "node", newNode.Name)
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
	for name, cache := range h.cache.CloneTASCache() {
		if nodeBelongsToFlavor(node, cache.NodeLabels(), cache.TopologyLevels()) {
			q.AddAfter(reconcile.Request{NamespacedName: types.NamespacedName{
				Name: string(name),
			}}, constants.UpdatesBatchPeriod)
		}
	}
}

func (h *nodeHandler) Generic(context.Context, event.GenericEvent, workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (r *rfReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.V(2).Info("Reconcile TAS Resource Flavor")

	flv := &kueue.ResourceFlavor{}
	if err := r.client.Get(ctx, req.NamespacedName, flv); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return reconcile.Result{}, err
		}
	}
	if flv.Spec.TopologyName != nil {
		// requeue inadmissible workloads as a change to the resource flavor
		// or the set of nodes can allow admitting a workload which was
		// previously inadmissible.
		if cqNames := r.cache.ActiveClusterQueues(); len(cqNames) > 0 {
			qcache.QueueInadmissibleWorkloads(ctx, r.queues, cqNames)
		}
	}
	return reconcile.Result{}, nil
}

func (r *rfReconciler) Create(event event.TypedCreateEvent[*kueue.ResourceFlavor]) bool {
	if event.Object.Spec.TopologyName != nil {
		log := r.logger().WithValues("flavor", event.Object.Name)
		log.V(2).Info("Topology TAS ResourceFlavor event")

		r.cache.AddOrUpdateResourceFlavor(log, event.Object)
		return true
	}
	return false
}

func (r *rfReconciler) Delete(event event.TypedDeleteEvent[*kueue.ResourceFlavor]) bool {
	return event.Object.Spec.TopologyName != nil
}

func (r *rfReconciler) Update(event event.TypedUpdateEvent[*kueue.ResourceFlavor]) bool {
	switch {
	case ptr.Equal(event.ObjectOld.Spec.TopologyName, event.ObjectNew.Spec.TopologyName):
		return false
	case event.ObjectOld.Spec.TopologyName == nil:
		return true
	default:
		// topologyName was set so is changed or removed
		return event.ObjectNew.Spec.TopologyName != nil
	}
}

func (r *rfReconciler) Generic(event event.TypedGenericEvent[*kueue.ResourceFlavor]) bool {
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

type eventType int64

const (
	nodeAllocatableChanged eventType = 1 << iota
	nodeLabelsChanged
	nodeTaintsChanged
	nodeConditionsChanged
	nodeAnnotationsChanged
	nodeSpecUnschedulableChanged

	nodeUnchanged eventType = 0
)

type nodeChangeExtractor func(newNode, oldNode *corev1.Node) eventType

var nodeChangeExtractors = []nodeChangeExtractor{
	extractNodeSpecUnschedulableChange,
	extractNodeAllocatableChange,
	extractNodeLabelsChange,
	extractNodeTaintsChange,
	extractNodeConditionsChange,
	extractNodeAnnotationsChange,
}

func checkNodeSchedulingPropertiesChanged(newNode, oldNode *corev1.Node) eventType {
	var et eventType
	for _, fn := range nodeChangeExtractors {
		et |= fn(newNode, oldNode)
	}
	return et
}

func extractNodeAllocatableChange(newNode, oldNode *corev1.Node) eventType {
	if equality.Semantic.DeepEqual(oldNode.Status.Allocatable, newNode.Status.Allocatable) {
		return nodeUnchanged
	}
	return nodeAllocatableChanged
}

func extractNodeLabelsChange(newNode, oldNode *corev1.Node) eventType {
	if equality.Semantic.DeepEqual(newNode.GetLabels(), oldNode.GetLabels()) {
		return nodeUnchanged
	}
	return nodeLabelsChanged
}

func extractNodeTaintsChange(newNode, oldNode *corev1.Node) eventType {
	if equality.Semantic.DeepEqual(newNode.Spec.Taints, oldNode.Spec.Taints) {
		return nodeUnchanged
	}
	return nodeTaintsChanged
}

func extractNodeConditionsChange(newNode, oldNode *corev1.Node) eventType {
	strip := func(conditions []corev1.NodeCondition) map[corev1.NodeConditionType]corev1.ConditionStatus {
		conditionStatuses := make(map[corev1.NodeConditionType]corev1.ConditionStatus, len(conditions))
		for i := range conditions {
			conditionStatuses[conditions[i].Type] = conditions[i].Status
		}
		return conditionStatuses
	}
	if equality.Semantic.DeepEqual(strip(oldNode.Status.Conditions), strip(newNode.Status.Conditions)) {
		return nodeUnchanged
	}
	return nodeConditionsChanged
}

func extractNodeSpecUnschedulableChange(newNode, oldNode *corev1.Node) eventType {
	if newNode.Spec.Unschedulable == oldNode.Spec.Unschedulable {
		return nodeUnchanged
	}
	return nodeSpecUnschedulableChanged
}

func extractNodeAnnotationsChange(newNode, oldNode *corev1.Node) eventType {
	if equality.Semantic.DeepEqual(oldNode.GetAnnotations(), newNode.GetAnnotations()) {
		return nodeUnchanged
	}
	return nodeAnnotationsChanged
}

package tas

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
)

const (
	NodeFailureControllerName = "kueue-node-failure-controller"
)

// nodeFailureReconciler reconciles Nodes to detect failures and update affected Workloads
type nodeFailureReconciler struct {
	client   client.Client
	log      logr.Logger
	recorder record.EventRecorder
}

//+kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;patch;update

func newNodeFailureReconciler(client client.Client, recorder record.EventRecorder) *nodeFailureReconciler {
	return &nodeFailureReconciler{
		client:   client,
		log:      ctrl.Log.WithName(NodeFailureControllerName),
		recorder: recorder,
	}
}

func (r *nodeFailureReconciler) SetupWithManager(mgr ctrl.Manager, cfg *config.Configuration) (string, error) {
	return NodeFailureControllerName, builder.ControllerManagedBy(mgr).
		Named(NodeFailureControllerName).
		For(&corev1.Node{}).
		Complete(r)
}

func (r *nodeFailureReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	fmt.Printf("Reconciling node %s\n", req.Name)
	log := ctrl.LoggerFrom(ctx).WithValues("node", req.Name)
	ctx = ctrl.LoggerInto(ctx, log)

	var node corev1.Node
	log.V(3).Info("Getting node", "name", req.Name, "namespacename", req.NamespacedName)
	err := r.client.Get(ctx, req.NamespacedName, &node)
	nodeFound := !apierrors.IsNotFound(err)
	if err != nil && nodeFound {
		log.Error(err, "Failed to get Node")
		return ctrl.Result{}, err
	}

	nodeFailed := false
	if nodeFound {
		nodeFailed = r.isNodeFailed(&node)
		log.V(3).Info("Reconciling node", "found", nodeFound, "failed", nodeFailed)
	} else {
		log.V(3).Info("Node not found, assuming deleted")
	}
	nodeFailedOrDeleted := !nodeFound || nodeFailed

	// Update workloads regardless of whether the node was found or not,
	// to handle both failure detection and recovery/deletion cleanup.
	updateErr := r.updateWorkloadsForNode(ctx, req.Name, nodeFailedOrDeleted)
	if updateErr != nil {
		// Requeue if updating workloads failed (e.g., pod list failed)
		return ctrl.Result{}, updateErr
	}

	// No need to requeue based on node status alone, as we watch Node objects.
	return ctrl.Result{}, nil
}

// updateWorkloadsForNode finds workloads with pods on the specified node
// and updates their status based on whether the node is currently considered failed.
func (r *nodeFailureReconciler) updateWorkloadsForNode(ctx context.Context, nodeName string, nodeFailedOrDeleted bool) error {
	log := ctrl.LoggerFrom(ctx)
	var podList corev1.PodList
	var allPods corev1.PodList
	// Use the indexer configured for the manager for efficient lookup
	r.client.List(ctx, &allPods)
	fmt.Printf("All pods %v\n", allPods.Items)

	if err := r.client.List(ctx, &podList, client.MatchingFields{indexer.PodNodeNameIndexKey: nodeName}); err != nil {
		log.Error(err, "Failed to list pods on node", "nodeName", nodeName)
		return err
	}
	log.V(3).Info("Found", "pods", strconv.Itoa(len(podList.Items)), "on node ", nodeName)
	// Use a map to update each workload only once per reconciliation cycle
	workloadsToPatch := make(map[types.NamespacedName]*kueue.Workload)
	workloadStatusChanged := make(map[types.NamespacedName]bool)
	var workloadPatchErrors []error

	// First pass: Identify workloads and determine if their status needs changing
	for i := range podList.Items {
		pod := &podList.Items[i]
		// Skip pods not managed by Kueue or already finished/terminating
		if v, ok := pod.GetLabels()[constants.ManagedByKueueLabelKey]; !ok || v != constants.ManagedByKueueLabelValue || utilpod.IsTerminated(pod) {
			continue
		}
		wlName, found := pod.Annotations[kueuealpha.WorkloadAnnotation]
		if !found {
			continue
		}
		wlKey := types.NamespacedName{Name: wlName, Namespace: pod.Namespace}

		// If we haven't fetched this workload yet in this cycle
		if _, exists := workloadsToPatch[wlKey]; !exists {
			var wl kueue.Workload
			if err := r.client.Get(ctx, wlKey, &wl); err != nil {
				if apierrors.IsNotFound(err) {
					log.V(4).Info("Workload not found for pod, skipping", "workload", wlKey, "pod", klog.KObj(pod))
				} else {
					log.Error(err, "Failed to get workload for pod", "workload", wlKey, "pod", klog.KObj(pod))
					workloadPatchErrors = append(workloadPatchErrors, err) // Collect error
				}
				continue // Skip this pod/workload
			}
			workloadsToPatch[wlKey] = &wl // Store fetched workload
		}

		// Determine if the status needs an update based on this node's state
		wl := workloadsToPatch[wlKey]
		currentStatusFailed := slices.Contains(wl.Status.FailedNodes, nodeName)

		if nodeFailedOrDeleted && !currentStatusFailed {
			workloadStatusChanged[wlKey] = true // Mark for update: need to add node
		} else if !nodeFailedOrDeleted && currentStatusFailed {
			workloadStatusChanged[wlKey] = true // Mark for update: need to remove node
		}
	}

	// Second pass: Patch workloads that require status changes
	for wlKey, wl := range workloadsToPatch {
		if !workloadStatusChanged[wlKey] {
			continue // Skip if no change needed for this workload based on this node
		}

		originalStatus := wl.Status.DeepCopy() // Use the status from the fetched object

		if nodeFailedOrDeleted {
			// Add nodeName if it's not already present (double-check, though logic above should handle)
			if !slices.Contains(wl.Status.FailedNodes, nodeName) {
				wl.Status.FailedNodes = append(wl.Status.FailedNodes, nodeName)
				slices.Sort(wl.Status.FailedNodes) // Keep sorted
				log.V(3).Info("Adding node to workload failed list", "workload", wlKey, "nodeName", nodeName)
			}
		} else {
			// Remove nodeName if it is present
			if idx := slices.Index(wl.Status.FailedNodes, nodeName); idx != -1 {
				wl.Status.FailedNodes = slices.Delete(wl.Status.FailedNodes, idx, idx+1)
				log.V(3).Info("Removing node from workload failed list", "workload", wlKey, "nodeName", nodeName)
			}
		}

		// Patch only if the slice content actually changed
		if !slices.Equal(originalStatus.FailedNodes, wl.Status.FailedNodes) {
			if err := r.client.Status().Patch(ctx, wl, client.Apply, client.FieldOwner(constants.KueueName)); err != nil {
				log.Error(err, "Failed to patch workload status", "workload", wlKey)
				workloadPatchErrors = append(workloadPatchErrors, err) // Collect error
			} else {
				log.V(2).Info("Successfully patched workload status", "workload", wlKey, "failedNodes", wl.Status.FailedNodes)
				if nodeFailedOrDeleted {
					r.recorder.Eventf(wl, corev1.EventTypeWarning, "NodeFailed", "Pod(s) running on failed node %s", nodeName)
				} else {
					r.recorder.Eventf(wl, corev1.EventTypeNormal, "NodeRecovered", "Node %s recovered, removed from failed list", nodeName)
				}
			}
		}
	}

	// If any errors occurred during workload Get or Patch, return the first one to trigger requeue
	if len(workloadPatchErrors) > 0 {
		return fmt.Errorf("encountered %d errors updating workloads for node %s: %w", len(workloadPatchErrors), nodeName, workloadPatchErrors[0])
	}

	return nil
}

// isNodeFailed determines if a node is considered failed based on Taints and Ready condition.
func (r *nodeFailureReconciler) isNodeFailed(node *corev1.Node) bool {
	for _, taint := range node.Spec.Taints {
		if (taint.Key == corev1.TaintNodeNotReady && taint.Effect == corev1.TaintEffectNoSchedule) ||
			(taint.Key == corev1.TaintNodeUnreachable && taint.Effect == corev1.TaintEffectNoExecute) {
			return true
		}
	}
	for _, cond := range node.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			return cond.Status != corev1.ConditionTrue
		}
	}
	r.log.V(3).Info("NodeReady condition not found, assuming node is failed", "nodeName", node.Name)
	return true
}

/*var _ handler.EventHandler = (*nodeHandler)(nil)

type nodeHandler struct{}

func (h *resourceFlavorHandler) Generic(_ context.Context, e event.GenericEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *resourceFlavorHandler) Create(context.Context, event.CreateEvent, workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *resourceFlavorHandler) Update(context.Context, event.UpdateEvent, workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *resourceFlavorHandler) Delete(_ context.Context, e event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	resourceFlavor, isResourceFlavor := e.Object.(*kueue.ResourceFlavor)
	if !isResourceFlavor || resourceFlavor.Spec.TopologyName == nil {
		return
	}
	q.AddAfter(reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name: string(*resourceFlavor.Spec.TopologyName),
		},
	}, constants.UpdatesBatchPeriod)
}*/

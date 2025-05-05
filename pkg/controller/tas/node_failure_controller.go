package tas

import (
	"context"
	"fmt"
	"slices"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
)

const (
	NodeFailureControllerName = "kueue-node-failure-controller"
)

// nodeFailureReconciler reconciles Nodes to detect failures and update affected Workloads
type nodeFailureReconciler struct {
	client client.Client
	log    logr.Logger
}

//+kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;patch;update

func newNodeFailureReconciler(client client.Client) *nodeFailureReconciler {
	return &nodeFailureReconciler{
		client: client,
		log:    ctrl.Log.WithName(NodeFailureControllerName),
	}
}

func (r *nodeFailureReconciler) SetupWithManager(mgr ctrl.Manager, cfg *config.Configuration) (string, error) {
	return NodeFailureControllerName, builder.ControllerManagedBy(mgr).
		Named(NodeFailureControllerName).
		For(&corev1.Node{}).
		Complete(r)
}

func (r *nodeFailureReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
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
		return ctrl.Result{}, updateErr
	}

	return ctrl.Result{}, nil
}

// updateWorkloadsForNode finds workloads with pods on the specified node
// and updates their status based on whether the node is currently considered failed.
func (r *nodeFailureReconciler) updateWorkloadsForNode(ctx context.Context, nodeName string, nodeFailedOrDeleted bool) error {
	log := ctrl.LoggerFrom(ctx)
	var podList corev1.PodList
	if err := r.client.List(ctx, &podList, client.MatchingFields{indexer.PodNodeNameIndexKey: nodeName}); err != nil {
		log.Error(err, "Failed to list pods on node", "nodeName", nodeName)
		return err
	}

	workloadsToPatch := make(map[types.NamespacedName]*kueue.Workload)
	workloadStatusChanged := make(map[types.NamespacedName]bool)
	var workloadPatchErrors []error

	for i := range podList.Items {
		pod := &podList.Items[i]
		if _, found := pod.GetLabels()[kueuealpha.TASLabel]; !found {
			//skip non Kueue TAS pods
			continue
		}
		if utilpod.IsTerminated(pod) {
			continue
		}

		wlName, found := pod.Annotations[kueuealpha.WorkloadAnnotation]
		if !found {
			continue
		}
		wlKey := types.NamespacedName{Name: wlName, Namespace: pod.Namespace}
		if _, exists := workloadsToPatch[wlKey]; !exists {
			var wl kueue.Workload
			if err := r.client.Get(ctx, wlKey, &wl); err != nil {
				if apierrors.IsNotFound(err) {
					log.V(4).Info("Workload not found for pod, skipping", "workload", wlKey, "pod", klog.KObj(pod))
				} else {
					log.Error(err, "Failed to get workload for pod", "workload", wlKey, "pod", klog.KObj(pod))
					workloadPatchErrors = append(workloadPatchErrors, err)
				}
				continue
			}
			workloadsToPatch[wlKey] = &wl
		}
		wl := workloadsToPatch[wlKey]
		currentStatusFailed := slices.Contains(wl.Status.FailedNodes, nodeName)

		if nodeFailedOrDeleted && !currentStatusFailed {
			workloadStatusChanged[wlKey] = true
		} else if !nodeFailedOrDeleted && currentStatusFailed {
			workloadStatusChanged[wlKey] = true
		}
	}

	// Patch workloads that require status changes
	for wlKey, wl := range workloadsToPatch {
		if !workloadStatusChanged[wlKey] {
			continue
		}
		originalStatus := wl.Status.DeepCopy()
		if nodeFailedOrDeleted {
			if !slices.Contains(wl.Status.FailedNodes, nodeName) {
				wl.Status.FailedNodes = append(wl.Status.FailedNodes, nodeName)
				slices.Sort(wl.Status.FailedNodes)
				log.V(4).Info("Adding node to workload failed list", "workload", wlKey, "nodeName", nodeName)
			}
		} else {
			if idx := slices.Index(wl.Status.FailedNodes, nodeName); idx != -1 {
				wl.Status.FailedNodes = slices.Delete(wl.Status.FailedNodes, idx, idx+1)
				log.V(4).Info("Removing node from workload failed list", "workload", wlKey, "nodeName", nodeName)
			}
		}

		// Patch only if the slice content actually changed
		if !slices.Equal(originalStatus.FailedNodes, wl.Status.FailedNodes) {
			if err := r.client.Status().Update(ctx, wl); err != nil {
				log.Error(err, "Failed to patch workload status", "workload", wlKey)
				workloadPatchErrors = append(workloadPatchErrors, err)
			} else {
				log.V(3).Info("Successfully patched workload status", "workload", wlKey, "failedNodes", wl.Status.FailedNodes)
				if nodeFailedOrDeleted {
					log.V(3).Info("Pod(s) running on failed node", "workloadName", wl.Name, "nodeName", nodeName)
				} else {
					log.V(3).Info("Node recovered, removed from failed list", "workloadName", wl.Name, "nodeName", nodeName)
				}
			}
		}
	}
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

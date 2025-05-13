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
	"errors"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

// nodeFailureReconciler reconciles Nodes to detect failures and update affected Workloads
type nodeFailureReconciler struct {
	client client.Client
	clock  clock.Clock
	log    logr.Logger
}

func (r *nodeFailureReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	r.log.V(3).Info("Getting node", "name", req.Name, "namespacename", req.NamespacedName)
	var node corev1.Node
	err := r.client.Get(ctx, req.NamespacedName, &node)
	nodeExists := !apierrors.IsNotFound(err)
	if err != nil && nodeExists {
		r.log.V(2).Error(err, "Failed to get Node")
		return ctrl.Result{}, err
	}

	markFailed := false
	if nodeExists {
		readyStatus, readyTransitionTime := r.getNodeConditionState(&node, corev1.NodeReady)
		if readyStatus == corev1.ConditionTrue {
			return ctrl.Result{}, nil
		}
		if readyTransitionTime != nil {
			timeSinceNotReady := r.clock.Now().Sub(readyTransitionTime.Time)
			if timeSinceNotReady >= NodeFailureDelay {
				markFailed = true
			} else {
				return ctrl.Result{RequeueAfter: NodeFailureDelay - timeSinceNotReady}, nil
			}
		} else { // Node exists, not ready, but no transition time? Treat as failed immediately.
			markFailed = true
			r.log.V(2).Info("Node is not ready and NodeReady condition is missing or has an unexpected status, marking as failed immediately", "nodeName", node.Name)
		}
	} else {
		r.log.V(3).Info("Node not found, assuming deleted")
		markFailed = true
	}

	if markFailed {
		if patchErr := r.patchWorkloadsForFailedNode(ctx, req.Name); patchErr != nil {
			return ctrl.Result{}, patchErr
		}
	}
	return ctrl.Result{}, nil
}

var _ reconcile.Reconciler = (*nodeFailureReconciler)(nil)
var _ predicate.TypedPredicate[*corev1.Node] = (*nodeFailureReconciler)(nil)

func (r *nodeFailureReconciler) Generic(event.TypedGenericEvent[*corev1.Node]) bool {
	return false
}

func (r *nodeFailureReconciler) Create(e event.TypedCreateEvent[*corev1.Node]) bool {
	newNode := e.Object
	if !utiltas.IsNodeStatusConditionTrue(newNode.Status.Conditions, corev1.NodeReady) {
		r.log.V(4).Info("NodeReady is not true", "node", klog.KObj(newNode))
		return true
	}
	r.log.V(5).Info("Node creation does not warrant reconcile for failure detection", "node", klog.KObj(newNode))
	return false
}

func (r *nodeFailureReconciler) Update(e event.TypedUpdateEvent[*corev1.Node]) bool {
	newNode := e.ObjectNew
	if !utiltas.IsNodeStatusConditionTrue(newNode.Status.Conditions, corev1.NodeReady) {
		r.log.V(4).Info("NodeReady is not true", "node", klog.KObj(newNode))
		return true
	}
	r.log.V(5).Info("Node update does not warrant reconcile for failure detection", "node", klog.KObj(newNode))
	return false
}

func (r *nodeFailureReconciler) Delete(e event.TypedDeleteEvent[*corev1.Node]) bool {
	return true
}

//+kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;patch

func newNodeFailureReconciler(client client.Client) *nodeFailureReconciler {
	return &nodeFailureReconciler{
		client: client,
		log:    ctrl.Log.WithName(TASNodeFailureController),
		clock:  clock.RealClock{},
	}
}

func (r *nodeFailureReconciler) SetupWithManager(mgr ctrl.Manager, cfg *config.Configuration) (string, error) {
	return TASNodeFailureController, builder.ControllerManagedBy(mgr).
		Named(TASNodeFailureController).
		WatchesRawSource(source.TypedKind(
			mgr.GetCache(),
			&corev1.Node{},
			&handler.TypedEnqueueRequestForObject[*corev1.Node]{},
			r,
		)).
		Complete(r)
}

// getWorkloadsOnNode lists all pods running on the given node and returns a set of Workload keys
func (r *nodeFailureReconciler) getWorkloadsOnNode(ctx context.Context, nodeName string) (sets.Set[types.NamespacedName], error) {
	var podList corev1.PodList
	if err := r.client.List(ctx, &podList, client.MatchingFields{indexer.PodNodeNameIndexKey: nodeName}); err != nil {
		r.log.V(2).Error(err, "Failed to list pods on node", "nodeName", nodeName)
		return nil, err
	}
	workloadsToProcess := sets.New[types.NamespacedName]()
	for _, pod := range podList.Items {
		if _, found := pod.GetLabels()[kueuealpha.TASLabel]; !found {
			// skip non TAS pods
			continue
		}
		if utilpod.IsTerminated(&pod) {
			continue
		}
		wlName, found := pod.Annotations[kueuealpha.WorkloadAnnotation]
		if !found {
			continue
		}
		workloadsToProcess.Insert(types.NamespacedName{Name: wlName, Namespace: pod.Namespace})
	}
	return workloadsToProcess, nil
}

// patchWorkloadsForFailedNode finds workloads with pods on the specified node
// and patches their status to indicate the node has failed.
func (r *nodeFailureReconciler) patchWorkloadsForFailedNode(ctx context.Context, nodeName string) error {
	workloadsToProcess, err := r.getWorkloadsOnNode(ctx, nodeName)
	if err != nil {
		return err
	}
	var workloadProcessingErrors []error
	for _, wlKey := range workloadsToProcess.UnsortedList() {
		var wl kueue.Workload
		if err := r.client.Get(ctx, wlKey, &wl); err != nil {
			if apierrors.IsNotFound(err) {
				r.log.V(4).Info("Workload not found, skipping", "workload", wlKey)
			} else {
				r.log.V(2).Error(err, "Failed to get workload", "workload", wlKey)
				workloadProcessingErrors = append(workloadProcessingErrors, err)
			}
			continue
		}

		err := clientutil.Patch(ctx, r.client, &wl, true, func() (bool, error) {
			currentAnnotations := wl.GetAnnotations()
			if currentAnnotations == nil {
				currentAnnotations = make(map[string]string)
			}
			existingFailedNode, ok := currentAnnotations[kueuealpha.NodeToReplaceAnnotation]
			if ok && existingFailedNode == nodeName {
				return false, nil
			}
			currentAnnotations[kueuealpha.NodeToReplaceAnnotation] = nodeName
			wl.SetAnnotations(currentAnnotations)
			r.log.V(4).Info("Adding failed node to workload annotation", "workload", wlKey, "nodeName", nodeName)
			return true, nil
		})
		if err != nil {
			r.log.V(2).Error(err, "Failed to patch workload with annotation", "workload", wlKey)
			workloadProcessingErrors = append(workloadProcessingErrors, err)
		} else {
			r.log.V(3).Info("Successfully patched workload with annotation", "workload", wlKey, "failedNodesAnnotation", wl.GetAnnotations()[kueuealpha.NodeToReplaceAnnotation])
		}
	}
	if len(workloadProcessingErrors) > 0 {
		return errors.Join(workloadProcessingErrors...)
	}
	return nil
}

// getNodeConditionState returns the status and last transition time of the condition.
func (r *nodeFailureReconciler) getNodeConditionState(node *corev1.Node, conditionType corev1.NodeConditionType) (status corev1.ConditionStatus, transitionTime *metav1.Time) {
	for _, cond := range node.Status.Conditions {
		if cond.Type == conditionType {
			return cond.Status, &cond.LastTransitionTime
		}
	}
	r.log.V(3).Info("NodeReady condition not found, assuming node is failed", "nodeName", node.Name)
	return corev1.ConditionUnknown, nil
}

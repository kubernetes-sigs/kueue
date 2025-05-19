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
	"fmt"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
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
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
	utilnode "sigs.k8s.io/kueue/pkg/util/node"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	"sigs.k8s.io/kueue/pkg/workload"
)

// nodeFailureReconciler reconciles Nodes to detect failures and update affected Workloads
type nodeFailureReconciler struct {
	client   client.Client
	clock    clock.Clock
	log      logr.Logger
	recorder record.EventRecorder
}

func (r *nodeFailureReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	r.log.V(3).Info("Getting node", "nodeName", req.NamespacedName)
	var node corev1.Node
	err := r.client.Get(ctx, req.NamespacedName, &node)
	nodeExists := !apierrors.IsNotFound(err)
	if err != nil && nodeExists {
		r.log.V(2).Error(err, "Failed to get Node")
		return ctrl.Result{}, err
	}

	if nodeExists {
		readyCondition := utilnode.GetNodeCondition(&node, corev1.NodeReady)
		if readyCondition != nil {
			if readyCondition.Status == corev1.ConditionTrue {
				return ctrl.Result{}, nil
			}
			timeSinceNotReady := r.clock.Now().Sub(readyCondition.LastTransitionTime.Time)
			if NodeFailureDelay > timeSinceNotReady {
				return ctrl.Result{RequeueAfter: NodeFailureDelay - timeSinceNotReady}, nil
			}
			r.log.V(3).Info("Node is not ready and NodeFailureDelay timer expired, marking as failed", "nodeName", req.NamespacedName)
		} else {
			r.log.V(3).Info("Node is not ready and NodeReady condition is missing, marking as failed immediately", "nodeName", req.NamespacedName)
		}
	} else {
		r.log.V(3).Info("Node not found, assuming deleted")
	}

	patchErr := r.patchWorkloadsForUnavailableNode(ctx, req.Name)
	return ctrl.Result{}, patchErr
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

func newNodeFailureReconciler(client client.Client, recorder record.EventRecorder) *nodeFailureReconciler {
	return &nodeFailureReconciler{
		client:   client,
		log:      ctrl.Log.WithName(TASNodeFailureController),
		clock:    clock.RealClock{},
		recorder: recorder,
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

// getWorkloadsOnNode gets all workloads that have the given node assigned in TAS topology assignment
func (r *nodeFailureReconciler) getWorkloadsOnNode(ctx context.Context, nodeName string) (sets.Set[types.NamespacedName], error) {
	var allWorkloads kueue.WorkloadList
	if err := r.client.List(ctx, &allWorkloads); err != nil {
		return nil, fmt.Errorf("failed to list workloads: %w", err)
	}
	workloadsToProcess := sets.New[types.NamespacedName]()
	for _, wl := range allWorkloads.Items {
		if !isAdmittedByTAS(&wl) {
			continue
		}
		for _, podSetAssignment := range wl.Status.Admission.PodSetAssignments {
			topologyAssignment := podSetAssignment.TopologyAssignment
			if topologyAssignment == nil {
				continue
			}
			if !utiltas.IsLowestLevelHostname(topologyAssignment.Levels) {
				continue
			}
			for _, domain := range topologyAssignment.Domains {
				if nodeName == domain.Values[len(domain.Values)-1] {
					workloadsToProcess.Insert(types.NamespacedName{Name: wl.Name, Namespace: wl.Namespace})
				}
			}
		}
	}
	return workloadsToProcess, nil
}

// patchWorkloadsForUnavailableNode finds workloads with pods on the specified node
// and patches their status to indicate the node is unavailable.
func (r *nodeFailureReconciler) patchWorkloadsForUnavailableNode(ctx context.Context, nodeName string) error {
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
		currentAnnotations := wl.GetAnnotations()
		if currentAnnotations == nil {
			currentAnnotations = make(map[string]string)
		}
		existingFailedNode, annotationPresent := currentAnnotations[kueuealpha.NodeToReplaceAnnotation]
		if annotationPresent && existingFailedNode != nodeName && !workload.IsEvicted(&wl) {
			r.log.V(3).Info("Evicting workload due to multiple node failures", "workload", wlKey)
			if evictionErr := r.startEviction(ctx, &wl); evictionErr != nil {
				r.log.Error(evictionErr, "Failed to complete eviction process", "workload", wlKey)
				workloadProcessingErrors = append(workloadProcessingErrors, evictionErr)
			}
			if err := r.client.Get(ctx, wlKey, &wl); err != nil {
				r.log.V(2).Error(err, "Failed to re-fetch workload after eviction", "workload", wlKey)
				workloadProcessingErrors = append(workloadProcessingErrors, err)
			}
		}
		err := clientutil.Patch(ctx, r.client, &wl, true, func() (bool, error) {
			switch {
			case annotationPresent && (workload.IsEvicted(&wl) || existingFailedNode != nodeName):
				delete(currentAnnotations, kueuealpha.NodeToReplaceAnnotation)
				wl.SetAnnotations(currentAnnotations)
				r.log.V(4).Info("Removing unavailable node from workload annotation", "workload", wlKey, "nodeName", existingFailedNode)
			case annotationPresent && existingFailedNode == nodeName:
				return false, nil
			default:
				currentAnnotations[kueuealpha.NodeToReplaceAnnotation] = nodeName
				wl.SetAnnotations(currentAnnotations)
				r.log.V(4).Info("Adding unavailable node to workload annotation", "workload", wlKey, "nodeName", nodeName)
			}
			return true, nil
		})
		if err != nil {
			r.log.V(2).Error(err, "Failed to patch workload with annotation", "workload", wlKey)
			workloadProcessingErrors = append(workloadProcessingErrors, err)
		} else {
			r.log.V(3).Info("Successfully patched workload with annotation", "workload", wlKey, "unavailableNodesAnnotation", wl.GetAnnotations()[kueuealpha.NodeToReplaceAnnotation])
		}
	}
	if len(workloadProcessingErrors) > 0 {
		return errors.Join(workloadProcessingErrors...)
	}
	return nil
}

func (r *nodeFailureReconciler) startEviction(ctx context.Context, wl *kueue.Workload) error {
	message := "Workload eviction triggered due to multiple TAS assigned node failures"
	workload.SetEvictedCondition(wl, kueue.WorkloadEvictedDueToTASNodeFailures, message)
	workload.ResetChecksOnEviction(wl, r.clock.Now())
	err := workload.ApplyAdmissionStatus(ctx, r.client, wl, true, r.clock)
	if err == nil {
		cqName := wl.Status.Admission.ClusterQueue
		workload.ReportEvictedWorkload(r.recorder, wl, cqName, kueue.WorkloadEvictedDueToTASNodeFailures, message)
	}
	return err
}

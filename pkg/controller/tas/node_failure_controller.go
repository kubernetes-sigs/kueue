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
	"slices"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
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

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	"sigs.k8s.io/kueue/pkg/workload"
)

const (
	nodeMultipleFailuresEvictionMessageFormat = "Workload eviction triggered due to multiple TAS assigned node failures, including: %s"
	podTerminationCheckPeriod                 = 1 * time.Second
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
	var affectedWorkloads sets.Set[types.NamespacedName]
	err := r.client.Get(ctx, req.NamespacedName, &node)
	nodeExists := !apierrors.IsNotFound(err)
	if err != nil && nodeExists {
		r.log.V(2).Error(err, "Failed to get Node")
		return ctrl.Result{}, err
	}

	if !nodeExists || utiltas.GetNodeCondition(&node, corev1.NodeReady) == nil {
		r.log.V(3).Info("Node not found or NodeReady condition is missing, marking as failed immediately")
		affectedWorkloads, err = r.getWorkloadsOnNode(ctx, req.Name)
		if err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, r.handleFailedNode(ctx, req.Name, affectedWorkloads)
	}
	readyCondition := utiltas.GetNodeCondition(&node, corev1.NodeReady)
	if readyCondition.Status == corev1.ConditionTrue {
		affectedWorkloads, err = r.getWorkloadsOnNode(ctx, req.Name)
		if err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, r.handleHealthyNode(ctx, req.Name, affectedWorkloads)
	}
	if features.Enabled(features.TASReplaceNodeOnPodTermination) {
		return r.reconcileForReplaceNodeOnPodTermination(ctx, req.Name)
	}
	timeSinceNotReady := r.clock.Now().Sub(readyCondition.LastTransitionTime.Time)
	if NodeFailureDelay > timeSinceNotReady {
		return ctrl.Result{RequeueAfter: NodeFailureDelay - timeSinceNotReady}, nil
	}
	r.log.V(3).Info("Node is not ready and NodeFailureDelay timer expired, marking as failed", "nodeName", req.NamespacedName)
	affectedWorkloads, err = r.getWorkloadsOnNode(ctx, req.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	patchErr := r.handleFailedNode(ctx, req.Name, affectedWorkloads)
	return ctrl.Result{}, patchErr
}

var _ reconcile.Reconciler = (*nodeFailureReconciler)(nil)
var _ predicate.TypedPredicate[*corev1.Node] = (*nodeFailureReconciler)(nil)

func (r *nodeFailureReconciler) Generic(event.TypedGenericEvent[*corev1.Node]) bool {
	return false
}

func (r *nodeFailureReconciler) Create(e event.TypedCreateEvent[*corev1.Node]) bool {
	return true
}

func (r *nodeFailureReconciler) Update(e event.TypedUpdateEvent[*corev1.Node]) bool {
	newReady := utiltas.IsNodeStatusConditionTrue(e.ObjectNew.Status.Conditions, corev1.NodeReady)
	oldReady := utiltas.IsNodeStatusConditionTrue(e.ObjectOld.Status.Conditions, corev1.NodeReady)
	if oldReady != newReady {
		r.log.V(4).Info("Node Ready status changed, triggering reconcile", "node", klog.KObj(e.ObjectNew), "oldReady", oldReady, "newReady", newReady)
		return true
	}
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
		Named("tas_node_failure_controller").
		WatchesRawSource(source.TypedKind(
			mgr.GetCache(),
			&corev1.Node{},
			&handler.TypedEnqueueRequestForObject[*corev1.Node]{},
			r,
		)).
		WithOptions(controller.Options{
			NeedLeaderElection:      ptr.To(false),
			MaxConcurrentReconciles: mgr.GetControllerOptions().GroupKindConcurrency[corev1.SchemeGroupVersion.WithKind("Node").GroupKind().String()],
		}).
		Complete(core.WithLeadingManager(mgr, r, &corev1.Node{}, cfg))
}

// getWorkloadsOnNode gets all workloads that have the given node assigned in TAS topology assignment
func (r *nodeFailureReconciler) getWorkloadsOnNode(ctx context.Context, nodeName string) (sets.Set[types.NamespacedName], error) {
	var allWorkloads kueue.WorkloadList
	if err := r.client.List(ctx, &allWorkloads); err != nil {
		return nil, fmt.Errorf("failed to list workloads: %w", err)
	}
	tasWorkloadsOnNode := sets.New[types.NamespacedName]()
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
					tasWorkloadsOnNode.Insert(types.NamespacedName{Name: wl.Name, Namespace: wl.Namespace})
				}
			}
		}
	}
	return tasWorkloadsOnNode, nil
}

func (r *nodeFailureReconciler) getWorkloadsForImmediateReplacement(ctx context.Context, nodeName string) (sets.Set[types.NamespacedName], error) {
	tasWorkloadsOnNode, err := r.getWorkloadsOnNode(ctx, nodeName)
	if err != nil {
		return nil, fmt.Errorf("failed to list all workloads on node: %w", err)
	}

	affectedWorkloads := sets.New[types.NamespacedName]()
	for wlKey := range tasWorkloadsOnNode {
		var podsForWl corev1.PodList
		if err := r.client.List(ctx, &podsForWl, client.InNamespace(wlKey.Namespace), client.MatchingFields{indexer.WorkloadNameKey: wlKey.Name}); err != nil {
			return nil, fmt.Errorf("failed to list pods for workload %s: %w", wlKey, err)
		}
		allPodsTerminate := true
		for _, pod := range podsForWl.Items {
			if pod.Spec.NodeName == nodeName && pod.DeletionTimestamp.IsZero() && !utilpod.IsTerminated(&pod) {
				allPodsTerminate = false
				break
			}
		}
		if allPodsTerminate {
			affectedWorkloads.Insert(wlKey)
		}
	}
	return affectedWorkloads, nil
}

// evictWorkloadIfNeeded idempotently evicts the workload when the node has failed.
// It returns whether the node was evicted, and whether an error was encountered.
func (r *nodeFailureReconciler) evictWorkloadIfNeeded(ctx context.Context, wl *kueue.Workload, nodeName string) (bool, error) {
	nodesToReplace := wl.Status.NodesToReplace
	if len(nodesToReplace) > 0 && !slices.Contains(nodesToReplace, nodeName) && !workload.IsEvicted(wl) {
		log := r.log.WithValues("failedNodes", nodesToReplace)
		log.V(3).Info("Evicting workload due to multiple node failures")
		allFailedNodes := append(slices.Clone(nodesToReplace), nodeName)
		evictionMsg := fmt.Sprintf(nodeMultipleFailuresEvictionMessageFormat, strings.Join(allFailedNodes, ", "))
		if evictionErr := workload.Evict(ctx, r.client, r.recorder, wl, kueue.WorkloadEvictedDueToNodeFailures, "", evictionMsg, r.clock); evictionErr != nil {
			log.V(2).Error(evictionErr, "Failed to complete eviction process")
			return false, evictionErr
		} else {
			return true, nil
		}
	}
	return false, nil
}

// handleFailedNode finds workloads with pods on the specified node
// and patches their status to indicate the node is to replace.
func (r *nodeFailureReconciler) handleFailedNode(ctx context.Context, nodeName string, affectedWorkloads sets.Set[types.NamespacedName]) error {
	var workloadProcessingErrors []error
	for wlKey := range affectedWorkloads {
		log := r.log.WithValues("workload", wlKey, "nodeName", nodeName)
		// fetch workload.
		var wl kueue.Workload
		if err := r.client.Get(ctx, wlKey, &wl); err != nil {
			if apierrors.IsNotFound(err) {
				log.V(4).Info("Workload not found, skipping")
			} else {
				log.V(2).Error(err, "Failed to get workload")
				workloadProcessingErrors = append(workloadProcessingErrors, err)
			}
			continue
		}
		// evict workload when workload already has a different node marked for replacement
		evictedNow, err := r.evictWorkloadIfNeeded(ctx, &wl, nodeName)
		if err != nil {
			workloadProcessingErrors = append(workloadProcessingErrors, err)
			continue
		}
		if !evictedNow && !workload.IsEvicted(&wl) {
			if err := r.addNodeToReplace(ctx, wl, nodeName); err != nil {
				log.V(2).Error(err, "Failed to add node to nodesToReplace")
				workloadProcessingErrors = append(workloadProcessingErrors, err)
				continue
			}
		}
	}
	if len(workloadProcessingErrors) > 0 {
		return errors.Join(workloadProcessingErrors...)
	}
	return nil
}

func (r *nodeFailureReconciler) reconcileForReplaceNodeOnPodTermination(ctx context.Context, nodeName string) (ctrl.Result, error) {
	workloads, err := r.getWorkloadsForImmediateReplacement(ctx, nodeName)
	switch {
	case err != nil:
		return ctrl.Result{}, fmt.Errorf("could not get workloads for immediate replacement on node %s: %w", nodeName, err)
	case len(workloads) == 0:
		return ctrl.Result{RequeueAfter: podTerminationCheckPeriod}, nil
	default:
		r.log.V(3).Info("Node is not ready and has only terminating or failed pods. Marking as failed immediately", "nodeName", nodeName)
		patchErr := r.handleFailedNode(ctx, nodeName, workloads)
		return ctrl.Result{}, patchErr
	}
}

// handleHealthyNode clears the nodesToReplace field for each of the specified workloads.
func (r *nodeFailureReconciler) handleHealthyNode(ctx context.Context, nodeName string, affectedWorkloads sets.Set[types.NamespacedName]) error {
	var workloadProcessingErrors []error
	for wlKey := range affectedWorkloads {
		var wl kueue.Workload
		if err := r.client.Get(ctx, wlKey, &wl); err != nil {
			if apierrors.IsNotFound(err) {
				r.log.V(4).Info("Workload not found, skipping")
			} else {
				r.log.Error(err, "Failed to get workload")
				workloadProcessingErrors = append(workloadProcessingErrors, err)
			}
			continue
		}

		if !slices.Contains(wl.Status.NodesToReplace, nodeName) {
			continue
		}

		r.log.V(4).Info("Remove node from nodesToReplace", "nodeName", nodeName)
		if err := r.removeNodeToReplace(ctx, wl, nodeName); err != nil {
			r.log.Error(err, "Failed to patch workload status")
			workloadProcessingErrors = append(workloadProcessingErrors, err)
			continue
		}
		r.log.V(3).Info("Successfully removed node from the nodesToReplace field", "nodeName", nodeName)
	}
	if len(workloadProcessingErrors) > 0 {
		return errors.Join(workloadProcessingErrors...)
	}
	return nil
}

func (r *nodeFailureReconciler) removeNodeToReplace(ctx context.Context, wl kueue.Workload, nodeName string) error {
	if slices.Contains(wl.Status.NodesToReplace, nodeName) {
		wl.Status.NodesToReplace = slices.DeleteFunc(wl.Status.NodesToReplace, func(n string) bool { return n == nodeName })
		return workload.ApplyAdmissionStatus(ctx, r.client, &wl, true, r.clock)
	}
	return nil
}

func (r *nodeFailureReconciler) addNodeToReplace(ctx context.Context, wl kueue.Workload, nodeName string) error {
	if !slices.Contains(wl.Status.NodesToReplace, nodeName) {
		wl.Status.NodesToReplace = append(wl.Status.NodesToReplace, nodeName)
		return workload.ApplyAdmissionStatus(ctx, r.client, &wl, true, r.clock)
	}
	return nil
}

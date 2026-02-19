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
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
	corev1helpers "k8s.io/component-helpers/scheduling/corev1"
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

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/features"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

type workloadStatus int

const (
	nodeMultipleFailuresEvictionMessageFormat = "Workload eviction triggered due to multiple TAS assigned node failures, including: %s"
	podTerminationCheckPeriod                 = 1 * time.Second
)

const (
	// workloadHealthy indicates that the workload does not need to be evicted from the node.
	// This happens if the node is healthy, or if the workload has permanent tolerations for the node's taints.
	workloadHealthy workloadStatus = iota

	// workloadUnhealthy indicates that the workload needs to be evicted from the node,
	// and is ready for eviction (e.g. all of its pods on the node have fully terminated).
	workloadUnhealthy

	// workloadTemporarilyHealthy indicates that the workload will need to be evicted from the node,
	// but is not ready yet (e.g. pods are still terminating, or taints are only temporarily tolerated).
	workloadTemporarilyHealthy
)

type taintToleration int

const (
	toleratedTemporarily taintToleration = iota
	toleratedPermanently
	untoleratedTaint
)

// nodeFailureReconciler reconciles Nodes to detect failures and update affected Workloads
type nodeFailureReconciler struct {
	client      client.Client
	clock       clock.Clock
	logName     string
	recorder    record.EventRecorder
	roleTracker *roletracker.RoleTracker
}

func (r *nodeFailureReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.V(2).Info("Reconcile Node")

	var node corev1.Node
	err := r.client.Get(ctx, req.NamespacedName, &node)
	if client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, err
	}
	nodeExists := err == nil

	affectedWorkloads, err := r.getWorkloadsOnNode(ctx, req.Name)
	if err != nil {
		return ctrl.Result{}, err
	}

	if !nodeExists {
		log.V(3).Info("Node not found. Marking as failed immediately")
		return ctrl.Result{}, r.handleUnhealthyNode(ctx, req.Name, affectedWorkloads)
	}

	readyCondition := utiltas.GetNodeCondition(&node, corev1.NodeReady)
	if readyCondition == nil {
		log.V(3).Info("NodeReady condition is missing. Marking as failed immediately")
		return ctrl.Result{}, r.handleUnhealthyNode(ctx, req.Name, affectedWorkloads)
	}

	isReady := readyCondition.Status == corev1.ConditionTrue
	if isReady || features.Enabled(features.TASReplaceNodeOnPodTermination) {
		return r.reconcileWorkloadsOnNode(ctx, req.Name, &node, affectedWorkloads)
	}

	timeSinceNotReady := r.clock.Now().Sub(readyCondition.LastTransitionTime.Time)
	if NodeFailureDelay > timeSinceNotReady {
		return ctrl.Result{RequeueAfter: NodeFailureDelay - timeSinceNotReady}, nil
	}
	log.V(3).Info("Node is not ready and NodeFailureDelay timer expired, marking as failed")
	return ctrl.Result{}, r.handleUnhealthyNode(ctx, req.Name, affectedWorkloads)
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
		r.logger().V(4).Info("Node Ready status changed, triggering reconcile", "node", klog.KObj(e.ObjectNew), "oldReady", oldReady, "newReady", newReady)
		return true
	}
	if features.Enabled(features.TASReplaceNodeOnNodeTaints) && !equality.Semantic.DeepEqual(e.ObjectOld.Spec.Taints, e.ObjectNew.Spec.Taints) {
		r.logger().V(4).Info("Node taints changed, triggering reconcile", "node", klog.KObj(e.ObjectNew))
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

func newNodeFailureReconciler(client client.Client, recorder record.EventRecorder, roleTracker *roletracker.RoleTracker) *nodeFailureReconciler {
	return &nodeFailureReconciler{
		client:      client,
		logName:     TASNodeFailureController,
		clock:       clock.RealClock{},
		recorder:    recorder,
		roleTracker: roleTracker,
	}
}

func (r *nodeFailureReconciler) logger() logr.Logger {
	return roletracker.WithReplicaRole(ctrl.Log.WithName(r.logName), r.roleTracker)
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
			LogConstructor:          roletracker.NewLogConstructor(r.roleTracker, "tas-node-failure-reconciler"),
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
		if hasTASAssignmentOnNode(&wl, nodeName) {
			tasWorkloadsOnNode.Insert(types.NamespacedName{Name: wl.Name, Namespace: wl.Namespace})
		}
	}
	return tasWorkloadsOnNode, nil
}

func hasTASAssignmentOnNode(wl *kueue.Workload, nodeName string) bool {
	if !workload.IsAdmittedByTAS(wl) {
		return false
	}
	for _, podSetAssignment := range wl.Status.Admission.PodSetAssignments {
		topologyAssignment := podSetAssignment.TopologyAssignment
		if topologyAssignment == nil {
			continue
		}
		if !utiltas.IsLowestLevelHostname(topologyAssignment.Levels) {
			continue
		}
		for value := range utiltas.LowestLevelValues(topologyAssignment) {
			if value == nodeName {
				return true
			}
		}
	}
	return false
}

func (r *nodeFailureReconciler) getWorkloadStatus(ctx context.Context, nodeName string, node *corev1.Node, wlKey types.NamespacedName, wl *kueue.Workload) (workloadStatus, error) {
	if err := r.client.Get(ctx, wlKey, wl); err != nil {
		if apierrors.IsNotFound(err) {
			return workloadHealthy, nil
		}
		return workloadHealthy, err
	}

	shouldWait := false
	ready := utiltas.IsNodeStatusConditionTrue(node.Status.Conditions, corev1.NodeReady)

	if ready && features.Enabled(features.TASReplaceNodeOnNodeTaints) {
		// Ready node, check for taints
		untolerated, temporarilyTolerated := classifyNoExecuteTaints(ctx, node.Spec.Taints, workload.PodSetsOnNode(wl, nodeName))
		switch {
		case len(untolerated) > 0:
			if !features.Enabled(features.TASReplaceNodeOnPodTermination) {
				return workloadUnhealthy, nil
			}
			shouldWait = true
		case len(temporarilyTolerated) > 0:
			shouldWait = true
		default:
			return workloadHealthy, nil
		}
	} else if !ready && features.Enabled(features.TASReplaceNodeOnPodTermination) {
		// NotReady node
		shouldWait = true
	}

	if shouldWait {
		var wl kueue.Workload
		if err := r.client.Get(ctx, wlKey, &wl); err != nil {
			return workloadHealthy, fmt.Errorf("failed to get workload %s: %w", wlKey, err)
		}
		podsForWl, err := r.listPodsForWorkload(ctx, &wl)
		if err != nil {
			return workloadHealthy, fmt.Errorf("failed to list pods for workload %s: %w", wlKey, err)
		}
		allPodsTerminated := true
		for _, pod := range podsForWl {
			if pod.Spec.NodeName == nodeName && pod.DeletionTimestamp.IsZero() && !utilpod.IsTerminated(pod) {
				allPodsTerminated = false
				break
			}
		}
		if allPodsTerminated {
			return workloadUnhealthy, nil
		}
		return workloadTemporarilyHealthy, nil
	}
	return workloadHealthy, nil
}

// listPodsForWorkload returns pods belonging to a workload.
func (r *nodeFailureReconciler) listPodsForWorkload(ctx context.Context, wl *kueue.Workload) ([]*corev1.Pod, error) {
	return ListPodsForWorkloadSlice(ctx, r.client, wl.Namespace, workloadslicing.SliceName(wl))
}

// evictWorkloadIfNeeded idempotently evicts the workload when the node has failed.
// It returns whether the node was evicted, and whether an error was encountered.
func (r *nodeFailureReconciler) evictWorkloadIfNeeded(ctx context.Context, wl *kueue.Workload, nodeName string) (bool, error) {
	if workload.HasUnhealthyNodes(wl) && !workload.HasUnhealthyNode(wl, nodeName) && !workload.IsEvicted(wl) {
		unhealthyNodeNames := workload.UnhealthyNodeNames(wl)
		log := ctrl.LoggerFrom(ctx).WithValues("unhealthyNodes", unhealthyNodeNames)
		log.V(3).Info("Evicting workload due to multiple node failures")
		allUnhealthyNodeNames := append(unhealthyNodeNames, nodeName)
		evictionMsg := fmt.Sprintf(nodeMultipleFailuresEvictionMessageFormat, strings.Join(allUnhealthyNodeNames, ", "))
		if evictionErr := workload.Evict(ctx, r.client, r.recorder, wl, kueue.WorkloadEvictedDueToNodeFailures, evictionMsg, "", r.clock, r.roleTracker); evictionErr != nil {
			log.Error(evictionErr, "Failed to complete eviction process")
			return false, evictionErr
		} else {
			return true, nil
		}
	}
	return false, nil
}

// handleUnhealthyNode finds workloads with pods on the specified node
// and patches their status to indicate the node is to replace.
func (r *nodeFailureReconciler) handleUnhealthyNode(ctx context.Context, nodeName string, affectedWorkloads sets.Set[types.NamespacedName]) error {
	log := ctrl.LoggerFrom(ctx)
	var workloadProcessingErrors []error
	for wlKey := range affectedWorkloads {
		wlLog := log.WithValues("workload", klog.KRef(wlKey.Namespace, wlKey.Name))
		var wl kueue.Workload
		if err := r.client.Get(ctx, wlKey, &wl); err != nil {
			if apierrors.IsNotFound(err) {
				wlLog.V(4).Info("Workload not found, skipping")
			} else {
				wlLog.Error(err, "Failed to get workload")
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
			if err := r.addUnhealthyNode(ctx, &wl, nodeName); err != nil {
				wlLog.Error(err, "Failed to add node to unhealthyNodes")
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

func (r *nodeFailureReconciler) reconcileWorkloadsOnNode(ctx context.Context, nodeName string, node *corev1.Node, allTASWorkloads sets.Set[types.NamespacedName]) (ctrl.Result, error) {
	if allTASWorkloads.Len() == 0 {
		return ctrl.Result{}, nil
	}

	unhealthyWorkloads := sets.New[types.NamespacedName]()
	notUnhealthyWorkloads := sets.New[types.NamespacedName]()
	hasWaitingWorkloads := false

	for wlKey := range allTASWorkloads {
		var wl kueue.Workload
		status, err := r.getWorkloadStatus(ctx, nodeName, node, wlKey, &wl)
		if err != nil {
			return ctrl.Result{}, err
		}

		switch status {
		case workloadUnhealthy:
			unhealthyWorkloads.Insert(wlKey)
		case workloadTemporarilyHealthy:
			hasWaitingWorkloads = true
			notUnhealthyWorkloads.Insert(wlKey)
		case workloadHealthy:
			notUnhealthyWorkloads.Insert(wlKey)
		}
	}

	if len(unhealthyWorkloads) > 0 {
		if err := r.handleUnhealthyNode(ctx, nodeName, unhealthyWorkloads); err != nil {
			return ctrl.Result{}, err
		}
	}

	if len(notUnhealthyWorkloads) > 0 {
		if err := r.handleHealthyNode(ctx, nodeName, notUnhealthyWorkloads); err != nil {
			return ctrl.Result{}, err
		}
	}

	result := ctrl.Result{}
	if hasWaitingWorkloads {
		result.RequeueAfter = podTerminationCheckPeriod
	}

	return result, nil
}

// handleHealthyNode clears the unhealthyNodes field for each of the specified workloads.
func (r *nodeFailureReconciler) handleHealthyNode(ctx context.Context, nodeName string, affectedWorkloads sets.Set[types.NamespacedName]) error {
	log := ctrl.LoggerFrom(ctx)
	var workloadProcessingErrors []error
	for wlKey := range affectedWorkloads {
		log = log.WithValues("workload", klog.KRef(wlKey.Namespace, wlKey.Name))
		var wl kueue.Workload
		if err := r.client.Get(ctx, wlKey, &wl); err != nil {
			if apierrors.IsNotFound(err) {
				log.V(4).Info("Workload not found, skipping")
			} else {
				log.Error(err, "Failed to get workload")
				workloadProcessingErrors = append(workloadProcessingErrors, err)
			}
			continue
		}

		if !slices.Contains(wl.Status.UnhealthyNodes, kueue.UnhealthyNode{Name: nodeName}) {
			continue
		}

		log.V(4).Info("Remove node from unhealthyNodes")
		if err := r.removeUnhealthyNodes(ctx, &wl, nodeName); err != nil {
			log.Error(err, "Failed to patch workload status")
			workloadProcessingErrors = append(workloadProcessingErrors, err)
			continue
		}
		log.V(3).Info("Successfully removed node from the unhealthyNodes field")
	}
	if len(workloadProcessingErrors) > 0 {
		return errors.Join(workloadProcessingErrors...)
	}
	return nil
}

func (r *nodeFailureReconciler) removeUnhealthyNodes(ctx context.Context, wl *kueue.Workload, nodeName string) error {
	if workload.HasUnhealthyNode(wl, nodeName) {
		return workload.PatchAdmissionStatus(ctx, r.client, wl, r.clock, func(wl *kueue.Workload) (bool, error) {
			wl.Status.UnhealthyNodes = slices.DeleteFunc(wl.Status.UnhealthyNodes, func(n kueue.UnhealthyNode) bool {
				return n.Name == nodeName
			})
			return true, nil
		})
	}
	return nil
}

func (r *nodeFailureReconciler) addUnhealthyNode(ctx context.Context, wl *kueue.Workload, nodeName string) error {
	if !workload.HasUnhealthyNode(wl, nodeName) {
		return workload.PatchAdmissionStatus(ctx, r.client, wl, r.clock, func(wl *kueue.Workload) (bool, error) {
			wl.Status.UnhealthyNodes = append(wl.Status.UnhealthyNodes, kueue.UnhealthyNode{Name: nodeName})
			return true, nil
		})
	}
	return nil
}

func checkTaintTolerations(logger logr.Logger, taint *corev1.Taint, podSets []kueue.PodSet) taintToleration {
	isUntolerated := true
	isPermanentlyTolerated := true

	for _, ps := range podSets {
		if !corev1helpers.TolerationsTolerateTaint(logger, ps.Template.Spec.Tolerations, taint, false) {
			isUntolerated = false
			break
		}

		// check if the taint is permanently tolerated,
		// if the taint is not permanently tolerated, the node will be watched
		permanentTolerations := make([]corev1.Toleration, 0, len(ps.Template.Spec.Tolerations))
		for _, t := range ps.Template.Spec.Tolerations {
			if t.TolerationSeconds == nil {
				permanentTolerations = append(permanentTolerations, t)
			}
		}
		if !corev1helpers.TolerationsTolerateTaint(logger, permanentTolerations, taint, false) {
			isPermanentlyTolerated = false
		}
	}

	if !isUntolerated {
		return untoleratedTaint
	}
	if !isPermanentlyTolerated {
		return toleratedTemporarily
	}
	return toleratedPermanently
}

func classifyNoExecuteTaints(ctx context.Context, taints []corev1.Taint, podSets []kueue.PodSet) (untolerated, temporarilyTolerated []corev1.Taint) {
	logger := ctrl.LoggerFrom(ctx)
	for _, taint := range taints {
		if taint.Effect != corev1.TaintEffectNoExecute {
			continue
		}

		switch checkTaintTolerations(logger, &taint, podSets) {
		case untoleratedTaint:
			untolerated = append(untolerated, taint)
		case toleratedTemporarily:
			temporarilyTolerated = append(temporarilyTolerated, taint)
		case toleratedPermanently:
			// if the taint is permanently tolerated, the node is considered healthy for this workload
		}
	}
	if len(untolerated) > 0 || len(temporarilyTolerated) > 0 {
		logger.V(3).Info("Classified NoExecute taints", "untolerated", untolerated, "temporarilyTolerated", temporarilyTolerated)
	}
	return untolerated, temporarilyTolerated
}

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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	"sigs.k8s.io/kueue/pkg/workload"
)

const (
	nodeMultipleFailuresEvictionMessageFormat = "Workload eviction triggered due to multiple TAS assigned node failures, including: %s"
	podTerminationCheckPeriod                 = 1 * time.Second
)




type nodeHealthStatus int

const (
	nodeHealthy nodeHealthStatus = iota
	nodeUnhealthy
	nodeKeepMonitoring
)

type nodeFailureReconciler struct {
	client      client.Client
	clock       clock.Clock
	recorder    record.EventRecorder
	roleTracker *roletracker.RoleTracker
}

func (r *nodeFailureReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.V(2).Info("Reconcile Node")

	var node corev1.Node
	var affectedWorkloads sets.Set[types.NamespacedName]

	err := r.client.Get(ctx, req.NamespacedName, &node)
	if client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, err
	}
	nodeExists := err == nil

	var errMsg string
	if !nodeExists {
		errMsg = "Node not found"
	} else if utiltas.GetNodeCondition(&node, corev1.NodeReady) == nil {
		errMsg = "NodeReady condition is missing"
	}

	if errMsg != "" {
		log.V(3).Info(fmt.Sprintf("%s. Marking as failed immediately", errMsg))
		affectedWorkloads, err = r.getWorkloadsOnNode(ctx, req.Name)
		if err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, r.handleUnhealthyNode(ctx, req.Name, affectedWorkloads)
	}
	readyCondition := utiltas.GetNodeCondition(&node, corev1.NodeReady)
	if readyCondition.Status == corev1.ConditionTrue {
		affectedWorkloads, err = r.getWorkloadsOnNode(ctx, req.Name)
		if err != nil {
			return ctrl.Result{}, err
		}
		keepMonitoring, err := r.handleHealthyNode(ctx, &node, affectedWorkloads)
		if err != nil {
			return ctrl.Result{}, err
		}
		if keepMonitoring {
			return ctrl.Result{RequeueAfter: podTerminationCheckPeriod}, nil
		}
		return ctrl.Result{}, nil
	}
	if features.Enabled(features.TASReplaceNodeOnPodTermination) {
		return r.reconcileForReplaceNodeOnPodTermination(ctx, req.Name)
	}
	timeSinceNotReady := r.clock.Now().Sub(readyCondition.LastTransitionTime.Time)
	if NodeFailureDelay > timeSinceNotReady {
		return ctrl.Result{RequeueAfter: NodeFailureDelay - timeSinceNotReady}, nil
	}
	log.V(3).Info("Node is not ready and NodeFailureDelay timer expired, marking as failed")
	affectedWorkloads, err = r.getWorkloadsOnNode(ctx, req.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	patchErr := r.handleUnhealthyNode(ctx, req.Name, affectedWorkloads)
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
//+kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;delete
//+kubebuilder:rbac:groups="",resources=pods/status,verbs=patch;update
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;patch

func newNodeFailureReconciler(client client.Client, recorder record.EventRecorder, roleTracker *roletracker.RoleTracker) *nodeFailureReconciler {
	return &nodeFailureReconciler{
		client:      client,
		clock:       clock.RealClock{},
		recorder:    recorder,
		roleTracker: roleTracker,
	}
}

func (r *nodeFailureReconciler) logger() logr.Logger {
	return roletracker.WithReplicaRole(ctrl.Log.WithName(TASNodeFailureController), r.roleTracker)
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
		log = log.WithValues("workload", klog.KRef(wlKey.Namespace, wlKey.Name))
		// fetch workload.
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
		// evict workload when workload already has a different node marked for replacement
		evictedNow, err := r.evictWorkloadIfNeeded(ctx, &wl, nodeName)
		if err != nil {
			workloadProcessingErrors = append(workloadProcessingErrors, err)
			continue
		}
		if !evictedNow && !workload.IsEvicted(&wl) {
			if err := r.addUnhealthyNode(ctx, &wl, nodeName); err != nil {
				log.Error(err, "Failed to add node to unhealthyNodes")
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
		ctrl.LoggerFrom(ctx).V(3).Info("Node is not ready and has only terminating or failed pods. Marking as failed immediately")
		patchErr := r.handleUnhealthyNode(ctx, nodeName, workloads)
		return ctrl.Result{}, patchErr
	}
}

// handleHealthyNode evaluate if a Ready node is unhealthy for its assigned workloads due to untolerated taints.
// It also clears the unhealthyNodes field for each of the specified workloads if it is no longer unhealthy.
// It returns whether any workload needs monitoring (leading to a requeue), and whether an error was encountered.
func (r *nodeFailureReconciler) handleHealthyNode(ctx context.Context, node *corev1.Node, affectedWorkloads sets.Set[types.NamespacedName]) (bool, error) {
	var workloadProcessingErrors []error
	keepMonitoring := false

	for wlKey := range affectedWorkloads {
		log := ctrl.LoggerFrom(ctx).WithValues("workload", klog.KRef(wlKey.Namespace, wlKey.Name))
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

		status, err := r.isNodeUnhealthyForWorkload(ctx, node, &wl)
		if err != nil {
			log.Error(err, "Failed to check if node is unhealthy for workload")
			workloadProcessingErrors = append(workloadProcessingErrors, err)
			continue
		}

		if status == nodeUnhealthy {
			podsToTerminate, totalPodsOnNode, err := r.getPodsToTerminate(ctx, node, &wl)
			if err != nil {
				log.Error(err, "Failed to get pods to terminate")
				workloadProcessingErrors = append(workloadProcessingErrors, err)
				continue
			}
			if len(podsToTerminate) > 0 {
				log.V(3).Info("Terminating all pending pods on unhealthy node", "podCount", len(podsToTerminate))
				r.terminatePods(ctx, podsToTerminate)
			}
			// evict workload when workload already has a different node marked for replacement
			evictedNow, err := r.evictWorkloadIfNeeded(ctx, &wl, node.Name)
			if err != nil {
				workloadProcessingErrors = append(workloadProcessingErrors, err)
				continue
			}

			if len(podsToTerminate) == 0 && totalPodsOnNode == 0 && !evictedNow {
				log.V(3).Info("Node marked Unhealthy but no pods found on node, treating as KeepMonitoring")
				keepMonitoring = true
			}
			if !evictedNow && !workload.IsEvicted(&wl) {
				if err := r.addUnhealthyNode(ctx, &wl, node.Name); err != nil {
					log.Error(err, "Failed to add node to unhealthyNodes")
					workloadProcessingErrors = append(workloadProcessingErrors, err)
					continue
				}
			}
		} else {
			if status == nodeKeepMonitoring {
				keepMonitoring = true
			}
			if !slices.Contains(wl.Status.UnhealthyNodes, kueue.UnhealthyNode{Name: node.Name}) {
				continue
			}

			log.V(4).Info("Remove node from unhealthyNodes")
			if err := r.removeUnhealthyNodes(ctx, &wl, node.Name); err != nil {
				log.Error(err, "Failed to patch workload status")
				workloadProcessingErrors = append(workloadProcessingErrors, err)
				continue
			}
			log.V(3).Info("Successfully removed node from the unhealthyNodes field")
		}
	}
	if len(workloadProcessingErrors) > 0 {
		return keepMonitoring, errors.Join(workloadProcessingErrors...)
	}
	return keepMonitoring, nil
}

func (r *nodeFailureReconciler) isNodeUnhealthyForWorkload(ctx context.Context, node *corev1.Node, wl *kueue.Workload) (nodeHealthStatus, error) {

	if !features.Enabled(features.TASReplaceNodeOnNodeTaints) {
		return nodeHealthy, nil
	}

	untoleratedTaints, toleratedTaints := classifyTaints(node.Spec.Taints, workload.PodSetsOnNode(wl, node.Name))

	// 1. Check Untolerated Taints (Immediate Unhealthy, unless TASReplaceNodeOnPodTermination is enabled)
	if len(untoleratedTaints) > 0 && !features.Enabled(features.TASReplaceNodeOnPodTermination) {
		return nodeUnhealthy, nil
	}

	// 2. Check for taints that require waiting for pod termination:
	// - Tolerated taints always wait for pod termination.
	// - Untolerated taints wait for pod termination only if TASReplaceNodeOnPodTermination is enabled.
	if len(toleratedTaints) == 0 && (len(untoleratedTaints) == 0 || !features.Enabled(features.TASReplaceNodeOnPodTermination)) {
		return nodeHealthy, nil
	}

	hasActivePods, err := r.hasActivePodsOnNode(ctx, wl, node)
	if err != nil {
		return nodeKeepMonitoring, err
	}

	if hasActivePods {
		return nodeKeepMonitoring, nil
	}

	return nodeUnhealthy, nil
}

func (r *nodeFailureReconciler) hasActivePodsOnNode(ctx context.Context, wl *kueue.Workload, node *corev1.Node) (bool, error) {
	podsOnNode, _, err := r.getPodsOnNode(ctx, wl, node)
	if err != nil {
		return false, err
	}

	for _, pod := range podsOnNode {
		if pod.DeletionTimestamp.IsZero() && !utilpod.IsTerminated(&pod) {
			// We treat Gated pods as active/non-pending for now, to avoid aggressive termination.
			if pod.Status.Phase != corev1.PodPending || len(pod.Spec.SchedulingGates) > 0 {
				return true, nil
			}
		}
	}
	return false, nil
}

func (r *nodeFailureReconciler) getPodsToTerminate(ctx context.Context, node *corev1.Node, wl *kueue.Workload) ([]corev1.Pod, int, error) {
	if !features.Enabled(features.TASReplaceNodeOnNodeTaints) {
		return nil, -1, nil
	}

	untoleratedTaints, _ := classifyTaints(node.Spec.Taints, workload.PodSetsOnNode(wl, node.Name))
	if len(untoleratedTaints) == 0 {
		return nil, -1, nil
	}

	podsOnNode, _, err := r.getPodsOnNode(ctx, wl, node)
	if err != nil {
		return nil, 0, err
	}

	var podsToDelete []corev1.Pod
	for _, pod := range podsOnNode {
		if !pod.DeletionTimestamp.IsZero() || utilpod.IsTerminated(&pod) {
			continue
		}
		if pod.Status.Phase == corev1.PodPending && len(pod.Spec.SchedulingGates) == 0 {
			podsToDelete = append(podsToDelete, pod)
		}
	}
	return podsToDelete, len(podsOnNode), nil
}

func (r *nodeFailureReconciler) terminatePods(ctx context.Context, pods []corev1.Pod) {
	log := ctrl.LoggerFrom(ctx)
	for _, p := range pods {
		pod := &corev1.Pod{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "v1",
				Kind:       "Pod",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      p.Name,
				Namespace: p.Namespace,
				UID:       p.UID,
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodFailed,
				Conditions: []corev1.PodCondition{
					{
						Type:               "TerminatedByKueue",
						Status:             corev1.ConditionTrue,
						Reason:             "UnschedulableOnAssignedNode",
						Message:            "Pod terminated by Kueue NodeFailureController due to node taint",
						LastTransitionTime: metav1.NewTime(r.clock.Now()),
					},
				},
			},
		}
		if err := r.client.Status().Patch(ctx, pod, client.Apply, client.FieldOwner(TASNodeFailureController), client.ForceOwnership); err != nil {
			if !apierrors.IsNotFound(err) {
				log.Error(err, "Failed to patch pending pod", "pod", klog.KObj(&p))
			}
		} else {
			r.recorder.Eventf(pod, corev1.EventTypeNormal, "PodTerminatedByKueue", "Pod terminated by Kueue NodeFailureController due to node taint")
		}
	}
}

func classifyTaints(taints []corev1.Taint, podSetsToCheck []kueue.PodSet) (untoleratedTaints, toleratedTaints []corev1.Taint) {
	if len(podSetsToCheck) == 0 {
		return nil, nil
	}
	untoleratedTaints = make([]corev1.Taint, 0, len(taints))
	toleratedTaints = make([]corev1.Taint, 0, len(taints))

	for _, t := range taints {
		if t.Effect == corev1.TaintEffectPreferNoSchedule {
			continue
		}
		untoleratedByAny := false
		for _, ps := range podSetsToCheck {
			_, untolerated := corev1helpers.FindMatchingUntoleratedTaint(klog.Background(), []corev1.Taint{t}, ps.Template.Spec.Tolerations, func(t *corev1.Taint) bool {
				return t.Effect == corev1.TaintEffectNoExecute || t.Effect == corev1.TaintEffectNoSchedule
			}, true)
			if untolerated {
				untoleratedByAny = true
				break
			}
		}
		if untoleratedByAny {
			untoleratedTaints = append(untoleratedTaints, t)
		} else {
			toleratedTaints = append(toleratedTaints, t)
		}
	}
	return untoleratedTaints, toleratedTaints
}

func (r *nodeFailureReconciler) removeUnhealthyNodes(ctx context.Context, wl *kueue.Workload, nodeName string) error {
	if workload.HasUnhealthyNode(wl, nodeName) {
		patch := client.MergeFrom(wl.DeepCopy())
		wl.Status.UnhealthyNodes = slices.DeleteFunc(wl.Status.UnhealthyNodes, func(n kueue.UnhealthyNode) bool {
			return n.Name == nodeName
		})
		return r.client.Status().Patch(ctx, wl, patch)
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

func isPodOnNode(pod *corev1.Pod, node *corev1.Node, levels []string) bool {
	if pod.Spec.NodeName != "" {
		return pod.Spec.NodeName == node.Name
	}
	if len(pod.Spec.NodeSelector) == 0 || len(levels) == 0 {
		return false
	}
	key := levels[len(levels)-1]
	val, ok := pod.Spec.NodeSelector[key]
	if !ok {
		return false
	}
	if nodeVal, ok := node.Labels[key]; !ok || nodeVal != val {
		return false
	}
	return true
}

func (r *nodeFailureReconciler) getPodsOnNode(ctx context.Context, wl *kueue.Workload, node *corev1.Node) ([]corev1.Pod, bool, error) {
	var podsForWl corev1.PodList
	if err := r.client.List(ctx, &podsForWl, client.InNamespace(wl.Namespace), client.MatchingFields{indexer.WorkloadNameKey: wl.Name}); err != nil {
		return nil, false, fmt.Errorf("list pods: %w", err)
	}

	psaMap := make(map[kueue.PodSetReference]*kueue.PodSetAssignment)
	for i := range wl.Status.Admission.PodSetAssignments {
		psaMap[wl.Status.Admission.PodSetAssignments[i].Name] = &wl.Status.Admission.PodSetAssignments[i]
	}

	var podsOnNode []corev1.Pod
	hasGatedPods := false
	for _, pod := range podsForWl.Items {
		if len(pod.Spec.SchedulingGates) > 0 {
			hasGatedPods = true
		}
		psName := kueue.PodSetReference(pod.Labels[constants.PodSetLabel])
		psa, found := psaMap[psName]
		if !found && len(wl.Status.Admission.PodSetAssignments) == 1 {
			psa = &wl.Status.Admission.PodSetAssignments[0]
			found = true
		}
		var levels []string
		if found && psa.TopologyAssignment != nil {
			levels = psa.TopologyAssignment.Levels
		}
		if isPodOnNode(&pod, node, levels) {
			podsOnNode = append(podsOnNode, pod)
		}
	}
	return podsOnNode, hasGatedPods, nil
}

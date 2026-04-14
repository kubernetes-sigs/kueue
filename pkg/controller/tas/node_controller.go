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
	"k8s.io/client-go/util/workqueue"
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
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	utilclient "sigs.k8s.io/kueue/pkg/util/client"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

const (
	nodeMultipleFailuresEvictionMessageFormat = "Workload eviction triggered due to multiple TAS assigned node failures, including: %s"
	reconcileBatchPeriod                      = 100 * time.Millisecond

	podTerminatedByKueueConditionType    = "TerminatedByKueue"
	podTerminatedByKueueConditionReason  = "UnschedulableOnAssignedNode"
	podTerminatedByKueueConditionMessage = "Pod terminated by Kueue NodeController due to node taint"
	podTerminatedByKueueEventReason      = "PodTerminatedByKueue"
)

type workloadStatus int

const (
	// workloadHealthy indicates that the workload does not need to be replaced on the node.
	// This happens if the node is healthy, or if the workload has permanent tolerations for the node's taints.
	workloadHealthy workloadStatus = iota

	// workloadUnhealthy indicates that the workload needs to be replaced or evicted from the node,
	// and is ready for it (e.g. all of its pods on the node have fully terminated).
	workloadUnhealthy

	// workloadHealthUnknown indicates that it's impossible to determine the workload health
	// from the node (e.g. error fetching resources).
	workloadHealthUnknown
)

type taintToleration int

const (
	toleratedTemporarily taintToleration = iota
	toleratedPermanently
	untoleratedTaint
)

type NodeUpdateWatcher interface {
	NotifyNodeUpdate(oldNode *corev1.Node, newNode *corev1.Node)
}

type nodeReconcilerOptions struct {
	watchers []NodeUpdateWatcher
}

// NodeReconcilerOption configures the reconciler.
type NodeReconcilerOption func(*nodeReconcilerOptions)

func WithWatchers(watchers ...NodeUpdateWatcher) NodeReconcilerOption {
	return func(o *nodeReconcilerOptions) {
		o.watchers = watchers
	}
}

// workloadHealthCheck holds the health status of a workload on a specific node and any pods that need termination.
type workloadHealthCheck struct {
	status          workloadStatus
	podsToTerminate []*corev1.Pod
}

// nodeReconciler reconciles Nodes to detect failures and update affected Workloads
type nodeReconciler struct {
	client      client.Client
	cache       *schdcache.Cache
	clock       clock.Clock
	logName     string
	recorder    record.EventRecorder
	roleTracker *roletracker.RoleTracker
	watchers    []NodeUpdateWatcher
}

func (r *nodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.V(2).Info("Reconcile Node")

	var node corev1.Node
	err := r.client.Get(ctx, req.NamespacedName, &node)
	if client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, err
	}
	nodeExists := err == nil

	var readyCondition *corev1.NodeCondition
	if nodeExists {
		readyCondition = utiltas.GetNodeCondition(&node, corev1.NodeReady)
	}

	var timerExpired bool
	if readyCondition != nil && readyCondition.Status != corev1.ConditionTrue {
		timeSinceNotReady := r.clock.Now().Sub(readyCondition.LastTransitionTime.Time)
		remainingTime := NodeFailureDelay - timeSinceNotReady
		timerExpired = remainingTime <= 0
		if !timerExpired && !features.Enabled(features.TASReplaceNodeOnPodTermination) {
			return ctrl.Result{RequeueAfter: remainingTime}, nil
		}
	}

	nodeSelectorPodsByWorkload, err := r.listPodsAssignedByNodeSelector(ctx, req.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	affectedWorkloads, err := r.getWorkloadsOnNode(ctx, req.Name, nodeSelectorPodsByWorkload)
	if err != nil {
		return ctrl.Result{}, err
	}

	if !nodeExists {
		log.V(3).Info("Node not found. Marking as failed immediately")
		_, err := r.handleUnhealthyNode(ctx, req.Name, affectedWorkloads)
		return ctrl.Result{}, err
	}

	if readyCondition == nil {
		log.V(3).Info("NodeReady condition is missing. Marking as failed immediately")
		_, err := r.handleUnhealthyNode(ctx, req.Name, affectedWorkloads)
		return ctrl.Result{}, err
	}

	if timerExpired {
		log.V(3).Info("Node is not ready and NodeFailureDelay timer expired, marking as failed")
		evictedWorkloads, err := r.handleUnhealthyNode(ctx, req.Name, affectedWorkloads)
		if err != nil {
			return ctrl.Result{}, err
		}
		// Only reconcile workloads that were not evicted.
		affectedWorkloads = affectedWorkloads.Difference(evictedWorkloads)
	}

	return r.reconcileWorkloadsOnNode(ctx, req.Name, &node, affectedWorkloads, nodeSelectorPodsByWorkload)
}

var _ reconcile.Reconciler = (*nodeReconciler)(nil)
var _ predicate.TypedPredicate[*corev1.Node] = (*nodeReconciler)(nil)

func (r *nodeReconciler) Generic(event.TypedGenericEvent[*corev1.Node]) bool {
	return false
}

func (r *nodeReconciler) Create(e event.TypedCreateEvent[*corev1.Node]) bool {
	r.cache.TASCache().SyncNode(e.Object)
	r.notifyWatchers(nil, e.Object)
	return features.Enabled(features.TASFailedNodeReplacement)
}

func (r *nodeReconciler) Update(e event.TypedUpdateEvent[*corev1.Node]) bool {
	r.cache.TASCache().SyncNode(e.ObjectNew)
	r.notifyWatchers(e.ObjectOld, e.ObjectNew)
	if !features.Enabled(features.TASFailedNodeReplacement) {
		return false
	}
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

func (r *nodeReconciler) Delete(e event.TypedDeleteEvent[*corev1.Node]) bool {
	r.cache.TASCache().DeleteNodeByName(e.Object.Name)
	r.notifyWatchers(e.Object, nil)
	return features.Enabled(features.TASFailedNodeReplacement)
}

//+kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=pods/status,verbs=patch
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;patch

func newNodeReconciler(
	client client.Client,
	recorder record.EventRecorder,
	cache *schdcache.Cache,
	roleTracker *roletracker.RoleTracker,
	opts ...NodeReconcilerOption,
) *nodeReconciler {
	options := nodeReconcilerOptions{}
	for _, opt := range opts {
		opt(&options)
	}
	return &nodeReconciler{
		client:      client,
		cache:       cache,
		logName:     TASNodeController,
		clock:       clock.RealClock{},
		recorder:    recorder,
		roleTracker: roleTracker,
		watchers:    options.watchers,
	}
}

func (r *nodeReconciler) logger() logr.Logger {
	return roletracker.WithReplicaRole(ctrl.Log.WithName(r.logName), r.roleTracker)
}

func (r *nodeReconciler) notifyWatchers(oldNode, newNode *corev1.Node) {
	for _, w := range r.watchers {
		w.NotifyNodeUpdate(oldNode, newNode)
	}
}

func (r *nodeReconciler) SetupWithManager(mgr ctrl.Manager, cfg *config.Configuration) (string, error) {
	podHandler := &nodeFailurePodHandler{client: r.client}
	return TASNodeController, builder.ControllerManagedBy(mgr).
		Named("tas_node_controller").
		WatchesRawSource(source.TypedKind(
			mgr.GetCache(),
			&corev1.Node{},
			&handler.TypedEnqueueRequestForObject[*corev1.Node]{},
			r,
		)).
		Watches(&corev1.Pod{}, podHandler).
		WithOptions(controller.Options{
			NeedLeaderElection:      ptr.To(false),
			MaxConcurrentReconciles: mgr.GetControllerOptions().GroupKindConcurrency[corev1.SchemeGroupVersion.WithKind("Node").GroupKind().String()],
			LogConstructor:          roletracker.NewLogConstructor(r.roleTracker, "tas-node-reconciler"),
		}).
		Complete(core.WithLeadingManager(mgr, r, &corev1.Node{}, cfg))
}

// / listPodsAssignedByNodeSelector returns a map of pods that are assigned to this node via nodeSelector, grouped by workload.
func (r *nodeReconciler) listPodsAssignedByNodeSelector(ctx context.Context, nodeName string) (map[types.NamespacedName][]*corev1.Pod, error) {
	var podList corev1.PodList
	if err := r.client.List(ctx, &podList, client.MatchingFields{indexer.PodNodeSelectorHostnameKey: nodeName}); err != nil {
		return nil, fmt.Errorf("failed to list pods for node selector %s: %w", nodeName, err)
	}

	return groupPodsByWorkload(podList.Items), nil
}

func groupPodsByWorkload(pods []corev1.Pod) map[types.NamespacedName][]*corev1.Pod {
	result := make(map[types.NamespacedName][]*corev1.Pod)
	for i := range pods {
		pod := &pods[i]
		if wlName, found := pod.Annotations[kueue.WorkloadAnnotation]; found {
			key := types.NamespacedName{Name: wlName, Namespace: pod.Namespace}
			result[key] = append(result[key], pod)
		}
	}
	return result
}

// getWorkloadsOnNode gets all workloads that have the given node assigned in TAS topology assignment
// or have "late" pods assigned to this node via nodeSelector.
func (r *nodeReconciler) getWorkloadsOnNode(ctx context.Context, nodeName string, nodeSelectorPodsByWorkload map[types.NamespacedName][]*corev1.Pod) (sets.Set[types.NamespacedName], error) {
	var workloadsOnNode kueue.WorkloadList
	if err := r.client.List(ctx, &workloadsOnNode, client.MatchingFields{indexer.AdmittedWorkloadNodesKey: nodeName}); err != nil {
		return nil, fmt.Errorf("failed to list workloads: %w", err)
	}
	tasWorkloadsOnNode := sets.New[types.NamespacedName]()
	for i := range workloadsOnNode.Items {
		wl := &workloadsOnNode.Items[i]
		tasWorkloadsOnNode.Insert(types.NamespacedName{Name: wl.Name, Namespace: wl.Namespace})
	}

	logger := r.logger().V(4).WithValues("node", nodeName)
	// Also find workloads from any pods that are assigned to this node by TopologyAssignment
	// but not yet bound. These might be stale "late" pods for a workload that has already
	// been reassigned to another node.
	for wlKey := range nodeSelectorPodsByWorkload {
		if tasWorkloadsOnNode.Has(wlKey) {
			continue
		}
		var wl kueue.Workload
		if err := r.client.Get(ctx, wlKey, &wl); err != nil {
			logger.V(4).Info("Failed to get workload", "workload", wlKey, "error", err)
			continue
		}
		if !workload.IsFinished(&wl) && !workload.IsEvicted(&wl) {
			tasWorkloadsOnNode.Insert(wlKey)
		}
	}

	return tasWorkloadsOnNode, nil
}

func (r *nodeReconciler) getWorkloadStatus(
	ctx context.Context,
	nodeName string,
	node *corev1.Node,
	wlKey types.NamespacedName,
	wl *kueue.Workload,
	nodeSelectorAssignedPods []*corev1.Pod,
) (workloadHealthCheck, error) {
	if err := r.client.Get(ctx, wlKey, wl); err != nil {
		if apierrors.IsNotFound(err) {
			return workloadHealthCheck{status: workloadHealthy}, nil
		}
		return workloadHealthCheck{status: workloadHealthUnknown}, err
	}

	ready := utiltas.IsNodeStatusConditionTrue(node.Status.Conditions, corev1.NodeReady)
	hasTASAssignment := utiltas.HasTASAssignmentOnNode(wl.Status.Admission.PodSetAssignments, nodeName)

	switch {
	case !hasTASAssignment:
		// If a pod arrives late (via nodeSelector) and its node is no longer part
		// of the topology assignment, we must always check pods
		// to catch and fail the stray pod.
		return r.checkPodsOnNode(ctx, nodeName, wl, false, hasTASAssignment, nodeSelectorAssignedPods)
	case !ready:
		if !features.Enabled(features.TASReplaceNodeOnPodTermination) {
			return workloadHealthCheck{status: workloadUnhealthy}, nil
		}
		return r.checkPodsOnNode(ctx, nodeName, wl, false, hasTASAssignment, nodeSelectorAssignedPods)
	case !features.Enabled(features.TASReplaceNodeOnNodeTaints):
		return workloadHealthCheck{status: workloadHealthy}, nil
	default:
		untolerated, temporarilyTolerated := classifyTaints(ctx, node.Spec.Taints, workload.PodSetsOnNode(wl, nodeName))

		if len(untolerated) > 0 {
			if !features.Enabled(features.TASReplaceNodeOnPodTermination) {
				return workloadHealthCheck{status: workloadUnhealthy}, nil
			}
			return r.checkPodsOnNode(ctx, nodeName, wl, true, hasTASAssignment, nodeSelectorAssignedPods)
		}
		if len(temporarilyTolerated) > 0 {
			return r.checkPodsOnNode(ctx, nodeName, wl, false, hasTASAssignment, nodeSelectorAssignedPods)
		}
		return workloadHealthCheck{status: workloadHealthy}, nil
	}
}

func (r *nodeReconciler) checkPodsOnNode(
	ctx context.Context,
	nodeName string,
	wl *kueue.Workload,
	hasUntoleratedTaints bool,
	hasTASAssignment bool,
	nodeSelectorAssignedPods []*corev1.Pod,
) (workloadHealthCheck, error) {
	podsToTerminate, hasProgressingPods, err := r.getPodsToTerminate(ctx, wl, nodeName, nodeSelectorAssignedPods)
	if err != nil {
		return workloadHealthCheck{status: workloadHealthUnknown}, fmt.Errorf("failed to get pods to terminate for node %s: %w", nodeName, err)
	}

	if !hasTASAssignment {
		// This node is not part of the workload's topology assignment. If pods are assigned
		// here (e.g., late pods with old node selectors), they are stray and must be terminated.
		return workloadHealthCheck{status: workloadHealthy, podsToTerminate: podsToTerminate}, nil
	}

	if hasProgressingPods && (!hasUntoleratedTaints || features.Enabled(features.TASReplaceNodeOnPodTermination)) {
		return workloadHealthCheck{status: workloadHealthy, podsToTerminate: podsToTerminate}, nil
	}

	return workloadHealthCheck{status: workloadUnhealthy, podsToTerminate: podsToTerminate}, nil
}

// evictWorkloadIfNeeded idempotently evicts the workload when the node has failed.
// It returns whether the node was evicted, and whether an error was encountered.
func (r *nodeReconciler) evictWorkloadIfNeeded(ctx context.Context, wl *kueue.Workload, nodeName string) (bool, error) {
	if workload.HasUnhealthyNodes(wl) && !workload.HasUnhealthyNode(wl, nodeName) && !workload.IsEvicted(wl) {
		unhealthyNodeNames := workload.UnhealthyNodeNames(wl)
		log := ctrl.LoggerFrom(ctx).WithValues("unhealthyNodes", unhealthyNodeNames)
		log.V(3).Info("Evicting workload due to multiple node failures")
		allUnhealthyNodeNames := append(unhealthyNodeNames, nodeName)
		evictionMsg := fmt.Sprintf(nodeMultipleFailuresEvictionMessageFormat, strings.Join(allUnhealthyNodeNames, ", "))
		exposeLqMetrics := r.cache.ShouldExposeLocalQueueMetricsForWorkload(log, wl)
		if evictionErr := workload.Evict(ctx, r.client, r.recorder, wl, kueue.WorkloadEvictedDueToNodeFailures, evictionMsg, "", r.clock, exposeLqMetrics, r.roleTracker, nil); evictionErr != nil {
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
// It also terminates any late pods assigned to the node via nodeSelector.
// It returns a set of workloads that were evicted during this process.
func (r *nodeReconciler) handleUnhealthyNode(ctx context.Context, nodeName string, affectedWorkloads sets.Set[types.NamespacedName]) (sets.Set[types.NamespacedName], error) {
	log := ctrl.LoggerFrom(ctx)
	var workloadProcessingErrors []error
	evictedWorkloads := sets.New[types.NamespacedName]()
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
		if evictedNow || workload.IsEvicted(&wl) {
			evictedWorkloads.Insert(wlKey)
			continue
		}
		if err := r.addUnhealthyNode(ctx, &wl, nodeName); err != nil {
			wlLog.Error(err, "Failed to add node to unhealthyNodes")
			workloadProcessingErrors = append(workloadProcessingErrors, err)
			continue
		}
	}
	if len(workloadProcessingErrors) > 0 {
		return evictedWorkloads, errors.Join(workloadProcessingErrors...)
	}
	return evictedWorkloads, nil
}

func (r *nodeReconciler) reconcileWorkloadsOnNode(
	ctx context.Context,
	nodeName string,
	node *corev1.Node,
	allTASWorkloads sets.Set[types.NamespacedName],
	nodeSelectorPodsByWorkload map[types.NamespacedName][]*corev1.Pod,
) (ctrl.Result, error) {
	if allTASWorkloads.Len() == 0 {
		return ctrl.Result{}, nil
	}

	unhealthyWorkloads := sets.New[types.NamespacedName]()
	notUnhealthyWorkloads := sets.New[types.NamespacedName]()
	podsToTerminateMap := make(map[types.NamespacedName][]*corev1.Pod)

	for wlKey := range allTASWorkloads {
		var wl kueue.Workload
		result, err := r.getWorkloadStatus(ctx, nodeName, node, wlKey, &wl, nodeSelectorPodsByWorkload[wlKey])
		if err != nil {
			return ctrl.Result{}, err
		}

		if len(result.podsToTerminate) > 0 {
			podsToTerminateMap[wlKey] = result.podsToTerminate
		}

		switch result.status {
		case workloadUnhealthy:
			unhealthyWorkloads.Insert(wlKey)
		case workloadHealthy:
			notUnhealthyWorkloads.Insert(wlKey)
		}
	}

	evictedWorkloads := sets.New[types.NamespacedName]()
	if len(unhealthyWorkloads) > 0 {
		evicted, err := r.handleUnhealthyNode(ctx, nodeName, unhealthyWorkloads)
		if err != nil {
			return ctrl.Result{}, err
		}
		evictedWorkloads = evicted
	}

	if len(notUnhealthyWorkloads) > 0 {
		if err := r.handleHealthyNode(ctx, nodeName, notUnhealthyWorkloads); err != nil {
			return ctrl.Result{}, err
		}
	}

	for wlKey, pods := range podsToTerminateMap {
		if evictedWorkloads.Has(wlKey) {
			continue
		}
		if err := r.markPodsFailed(ctx, pods); err != nil {
			return ctrl.Result{}, fmt.Errorf("marking pods as failed: %w", err)
		}
	}

	return ctrl.Result{}, nil
}

func (r *nodeReconciler) markPodsFailed(ctx context.Context, pods []*corev1.Pod) error {
	for _, p := range pods {
		err := utilclient.PatchStatus(ctx, r.client, p, func() (bool, error) {
			p.Status.Phase = corev1.PodFailed
			p.Status.Conditions = append(p.Status.Conditions, corev1.PodCondition{
				Type:               podTerminatedByKueueConditionType,
				Status:             corev1.ConditionTrue,
				Reason:             podTerminatedByKueueConditionReason,
				Message:            podTerminatedByKueueConditionMessage,
				LastTransitionTime: metav1.NewTime(r.clock.Now()),
			})
			return true, nil
		})
		if err != nil {
			if !apierrors.IsNotFound(err) {
				return err
			}
		} else {
			r.recorder.Eventf(p, corev1.EventTypeNormal, podTerminatedByKueueEventReason, podTerminatedByKueueConditionMessage)
		}
	}
	return nil
}

// handleHealthyNode clears the unhealthyNodes field for each of the specified workloads.
func (r *nodeReconciler) handleHealthyNode(ctx context.Context, nodeName string, affectedWorkloads sets.Set[types.NamespacedName]) error {
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

func (r *nodeReconciler) removeUnhealthyNodes(ctx context.Context, wl *kueue.Workload, nodeName string) error {
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

func (r *nodeReconciler) addUnhealthyNode(ctx context.Context, wl *kueue.Workload, nodeName string) error {
	if !workload.HasUnhealthyNode(wl, nodeName) {
		return workload.PatchAdmissionStatus(ctx, r.client, wl, r.clock, func(wl *kueue.Workload) (bool, error) {
			wl.Status.UnhealthyNodes = append(wl.Status.UnhealthyNodes, kueue.UnhealthyNode{Name: nodeName})
			return true, nil
		})
	}
	return nil
}

// getPodsToTerminate returns a list of pending pods that are assigned to the node by topology (but not yet scheduled),
// and a boolean indicating if there is at least one pod to retain (e.g. any scheduled pod).
func (r *nodeReconciler) getPodsToTerminate(ctx context.Context, wl *kueue.Workload, nodeName string, nodeSelectorAssignedPods []*corev1.Pod) ([]*corev1.Pod, bool, error) {
	hasProgressingPods, err := r.hasProgressingPods(ctx, wl, nodeName)
	if err != nil {
		return nil, false, fmt.Errorf("list scheduled pods: %w", err)
	}

	var podsToTerminate []*corev1.Pod
	for _, pod := range nodeSelectorAssignedPods {
		if len(pod.Spec.NodeName) == 0 && pod.Status.Phase == corev1.PodPending && !utilpod.HasGate(pod, kueue.TopologySchedulingGate) {
			podsToTerminate = append(podsToTerminate, pod)
		}
	}

	return podsToTerminate, hasProgressingPods, nil
}

func (r *nodeReconciler) hasProgressingPods(ctx context.Context, wl *kueue.Workload, nodeName string) (bool, error) {
	sliceName := workloadslicing.SliceName(wl)
	pods, err := ListPodsForWorkloadSlice(ctx, r.client, wl.Namespace, sliceName, client.MatchingFields{
		indexer.PodNodeNameKey: nodeName,
	})
	if err != nil {
		return false, err
	}
	hasProgressingPods := slices.ContainsFunc(pods, func(pod *corev1.Pod) bool {
		return pod.DeletionTimestamp.IsZero() && !utilpod.IsTerminated(pod)
	})
	return hasProgressingPods, nil
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

func classifyTaints(ctx context.Context, taints []corev1.Taint, podSets []kueue.PodSet) (untolerated, temporarilyTolerated []corev1.Taint) {
	logger := ctrl.LoggerFrom(ctx)
	for _, taint := range taints {
		if taint.Effect != corev1.TaintEffectNoExecute && taint.Effect != corev1.TaintEffectNoSchedule {
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
		logger.V(3).Info("Classified taints", "untolerated", untolerated, "temporarilyTolerated", temporarilyTolerated)
	}
	return untolerated, temporarilyTolerated
}

var _ handler.EventHandler = (*nodeFailurePodHandler)(nil)

type nodeFailurePodHandler struct {
	client client.Client
}

func (h *nodeFailurePodHandler) Create(_ context.Context, e event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.queueReconcileForPod(e.Object, q)
}

func (h *nodeFailurePodHandler) Update(_ context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.queueReconcileForPod(e.ObjectNew, q)
}

func (h *nodeFailurePodHandler) Delete(_ context.Context, e event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.queueReconcileForPod(e.Object, q)
}

func (h *nodeFailurePodHandler) Generic(_ context.Context, _ event.GenericEvent, _ workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *nodeFailurePodHandler) queueReconcileForPod(object client.Object, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	pod, isPod := object.(*corev1.Pod)
	if !isPod || !utiltas.IsTAS(pod) {
		return
	}

	// queue for potential stuck pending pods
	if len(pod.Spec.NodeName) == 0 && pod.Spec.NodeSelector != nil && len(pod.Spec.NodeSelector[corev1.LabelHostname]) > 0 {
		req := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name: pod.Spec.NodeSelector[corev1.LabelHostname],
			},
		}
		q.AddAfter(req, reconcileBatchPeriod)
	}

	// queue pods that are failed or being deleted
	if len(pod.Spec.NodeName) > 0 && (!pod.DeletionTimestamp.IsZero() || utilpod.IsTerminated(pod)) {
		req := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name: pod.Spec.NodeName,
			},
		}
		q.AddAfter(req, reconcileBatchPeriod)
	}
}

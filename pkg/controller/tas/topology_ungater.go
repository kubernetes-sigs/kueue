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
	"maps"
	"slices"
	"strconv"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
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

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	controllerconsts "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	utilclient "sigs.k8s.io/kueue/pkg/util/client"
	"sigs.k8s.io/kueue/pkg/util/expectations"
	"sigs.k8s.io/kueue/pkg/util/parallelize"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	utilslices "sigs.k8s.io/kueue/pkg/util/slices"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	"sigs.k8s.io/kueue/pkg/workload"
)

var (
	errPendingUngateOps = errors.New("pending ungate operations")
)

type topologyUngater struct {
	client            client.Client
	expectationsStore *expectations.Store
}

type podWithUngateInfo struct {
	pod        *corev1.Pod
	nodeLabels map[string]string
}

type podWithDomain struct {
	pod      *corev1.Pod
	domainID utiltas.TopologyDomainID
}

var _ reconcile.Reconciler = (*topologyUngater)(nil)
var _ predicate.TypedPredicate[*kueue.Workload] = (*topologyUngater)(nil)

// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get

func newTopologyUngater(c client.Client) *topologyUngater {
	return &topologyUngater{
		client:            c,
		expectationsStore: expectations.NewStore(TASTopologyUngater),
	}
}

func (r *topologyUngater) setupWithManager(mgr ctrl.Manager, cfg *configapi.Configuration) (string, error) {
	podHandler := podHandler{
		expectationsStore: r.expectationsStore,
	}
	return TASTopologyUngater, builder.TypedControllerManagedBy[reconcile.Request](mgr).
		Named("tas_topology_ungater").
		WatchesRawSource(source.TypedKind(
			mgr.GetCache(),
			&kueue.Workload{},
			&handler.TypedEnqueueRequestForObject[*kueue.Workload]{},
			r,
		)).
		Watches(&corev1.Pod{}, &podHandler).
		WithOptions(controller.Options{
			NeedLeaderElection:      ptr.To(false),
			MaxConcurrentReconciles: mgr.GetControllerOptions().GroupKindConcurrency[kueue.GroupVersion.WithKind("Workload").GroupKind().String()],
		}).
		Complete(core.WithLeadingManager(mgr, r, &kueue.Workload{}, cfg))
}

var _ handler.EventHandler = (*podHandler)(nil)

type podHandler struct {
	expectationsStore *expectations.Store
}

func (h *podHandler) Create(ctx context.Context, e event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.queueReconcileForPod(ctx, e.Object, false, q)
}

func (h *podHandler) Update(ctx context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.queueReconcileForPod(ctx, e.ObjectNew, false, q)
}

func (h *podHandler) Delete(ctx context.Context, e event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.queueReconcileForPod(ctx, e.Object, true, q)
}

func (h *podHandler) Generic(context.Context, event.GenericEvent, workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *podHandler) queueReconcileForPod(ctx context.Context, object client.Object, deleted bool, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	pod, isPod := object.(*corev1.Pod)
	if !isPod {
		return
	}
	if _, found := pod.Labels[kueuealpha.TASLabel]; !found {
		// skip non-TAS pods
		return
	}
	if wlName, found := pod.Annotations[kueuealpha.WorkloadAnnotation]; found {
		key := types.NamespacedName{
			Name:      wlName,
			Namespace: pod.Namespace,
		}
		// it is possible that the pod is removed before the gate removal, so
		// we also need to consider deleted pod as ungated.
		if !utilpod.HasGate(pod, kueuealpha.TopologySchedulingGate) || deleted {
			log := ctrl.LoggerFrom(ctx).WithValues("pod", klog.KObj(pod), "workload", key.String())
			h.expectationsStore.ObservedUID(log, key, pod.UID)
		}
		q.AddAfter(reconcile.Request{NamespacedName: key}, constants.UpdatesBatchPeriod)
	}
}

func (r *topologyUngater) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.V(2).Info("Reconcile Topology Ungater")

	wl := &kueue.Workload{}
	if err := r.client.Get(ctx, req.NamespacedName, wl); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return reconcile.Result{}, err
		}
		log.V(5).Info("workload not found")
		return reconcile.Result{}, nil
	}
	if !r.expectationsStore.Satisfied(log, req.NamespacedName) {
		log.V(3).Info("There are pending ungate operations")
		return reconcile.Result{}, errPendingUngateOps
	}
	if !isAdmittedByTAS(wl) {
		// this is a safeguard. In particular, it helps to prevent the race
		// condition if the workload is evicted before the reconcile is
		// triggered.
		log.V(5).Info("workload is not admitted by TAS")
		return reconcile.Result{}, nil
	}

	psNameToTopologyRequest := workload.PodSetNameToTopologyRequest(wl)
	allToUngate := make([]podWithUngateInfo, 0)
	groupedPodSetAssignments := make(map[string][]*kueue.PodSetAssignment)

	for i, psa := range wl.Status.Admission.PodSetAssignments {
		groupName := strconv.Itoa(i)
		if psNameToTopologyRequest[psa.Name] != nil && psNameToTopologyRequest[psa.Name].PodSetGroupName != nil {
			groupName = *psNameToTopologyRequest[psa.Name].PodSetGroupName
		}
		groupedPodSetAssignments[groupName] = append(groupedPodSetAssignments[groupName], &psa)
	}

	rankOffsets := make(map[kueue.PodSetReference]int32)
	maxRank := make(map[kueue.PodSetReference]int32)

	for _, psas := range groupedPodSetAssignments {
		if len(psas) > 1 {
			// In case of LeaderWorkerSet, in each Workload there will be
			// 1 leader and N workers. Leader will get rank 0 and workers
			// 1, 2, ..., N. To detect the leader we are selecting PodSet
			// which is smaller.
			smallerPsa := psas[0]
			largerPsa := psas[1]
			if *smallerPsa.Count > *largerPsa.Count {
				smallerPsa = psas[1]
				largerPsa = psas[0]
			}
			rankOffsets[smallerPsa.Name] = 0
			rankOffsets[largerPsa.Name] = *smallerPsa.Count
			maxRank[smallerPsa.Name] = *smallerPsa.Count
			maxRank[largerPsa.Name] = *largerPsa.Count + *smallerPsa.Count
		} else {
			rankOffsets[psas[0].Name] = 0
			maxRank[psas[0].Name] = *psas[0].Count
		}
	}
	for _, psa := range wl.Status.Admission.PodSetAssignments {
		if psa.TopologyAssignment != nil {
			pods, err := r.podsForPodSet(ctx, wl.Namespace, wl.Name, psa.Name)
			if err != nil {
				log.Error(err, "failed to list Pods for PodSet", "podset", psa.Name, "count", psa.Count)
				return reconcile.Result{}, err
			}
			gatedPodsToDomains := assignGatedPodsToDomains(log, &psa, pods, psNameToTopologyRequest[psa.Name], rankOffsets[psa.Name], maxRank[psa.Name])
			if len(gatedPodsToDomains) > 0 {
				toUngate := podsToUngateInfo(&psa, gatedPodsToDomains)
				log.V(2).Info("identified pods to ungate for podset", "podset", psa.Name, "count", len(toUngate))
				allToUngate = append(allToUngate, toUngate...)
			}
		}
	}

	if len(allToUngate) == 0 {
		return reconcile.Result{}, nil
	}
	log.V(2).Info("identified pods to ungate", "count", len(allToUngate))
	podsToUngateUIDs := utilslices.Map(allToUngate, func(p *podWithUngateInfo) types.UID { return p.pod.UID })
	r.expectationsStore.ExpectUIDs(log, req.NamespacedName, podsToUngateUIDs)

	err := parallelize.Until(ctx, len(allToUngate), func(i int) error {
		podWithUngateInfo := &allToUngate[i]
		var ungated bool
		e := utilclient.Patch(ctx, r.client, podWithUngateInfo.pod, true, func() (bool, error) {
			log.V(3).Info("ungating pod", "pod", klog.KObj(podWithUngateInfo.pod), "nodeLabels", podWithUngateInfo.nodeLabels)
			ungated = utilpod.Ungate(podWithUngateInfo.pod, kueuealpha.TopologySchedulingGate)
			if podWithUngateInfo.pod.Spec.NodeSelector == nil {
				podWithUngateInfo.pod.Spec.NodeSelector = make(map[string]string)
			}
			maps.Copy(podWithUngateInfo.pod.Spec.NodeSelector, podWithUngateInfo.nodeLabels)
			return true, nil
		})
		if e != nil {
			// We won't observe this cleanup in the event handler.
			r.expectationsStore.ObservedUID(log, req.NamespacedName, podWithUngateInfo.pod.UID)
			log.Error(e, "failed ungating pod", "pod", klog.KObj(podWithUngateInfo.pod))
		}
		if !ungated {
			// We don't expect an event in this case.
			r.expectationsStore.ObservedUID(log, req.NamespacedName, podWithUngateInfo.pod.UID)
		}
		return e
	})
	return reconcile.Result{}, err
}

func (r *topologyUngater) Create(event event.TypedCreateEvent[*kueue.Workload]) bool {
	return isAdmittedByTAS(event.Object)
}

func (r *topologyUngater) Delete(event event.TypedDeleteEvent[*kueue.Workload]) bool {
	return isAdmittedByTAS(event.Object)
}

func (r *topologyUngater) Update(event event.TypedUpdateEvent[*kueue.Workload]) bool {
	return isAdmittedByTAS(event.ObjectNew)
}

func (r *topologyUngater) Generic(event.TypedGenericEvent[*kueue.Workload]) bool {
	return false
}

func (r *topologyUngater) podsForPodSet(ctx context.Context, ns, wlName string, psName kueue.PodSetReference) ([]*corev1.Pod, error) {
	var pods corev1.PodList
	if err := r.client.List(ctx, &pods, client.InNamespace(ns), client.MatchingLabels{
		controllerconsts.PodSetLabel: string(psName),
	}, client.MatchingFields{
		indexer.WorkloadNameKey: wlName,
	}); err != nil {
		return nil, err
	}
	result := make([]*corev1.Pod, 0, len(pods.Items))
	for i := range pods.Items {
		if utilpod.IsTerminated(&pods.Items[i]) {
			// ignore failed or succeeded pods as they need to be replaced, and
			// so we don't want to count them as already ungated Pods.
			continue
		}
		result = append(result, &pods.Items[i])
	}
	return result, nil
}

func podsToUngateInfo(
	psa *kueue.PodSetAssignment,
	podToUngateWithDomain []podWithDomain) []podWithUngateInfo {
	domainIDToLabelValues := make(map[utiltas.TopologyDomainID][]string)
	for _, psaDomain := range psa.TopologyAssignment.Domains {
		domainID := utiltas.DomainID(psaDomain.Values)
		domainIDToLabelValues[domainID] = psaDomain.Values
	}
	toUngate := make([]podWithUngateInfo, len(podToUngateWithDomain))
	for i, pd := range podToUngateWithDomain {
		domainValues := domainIDToLabelValues[pd.domainID]
		nodeLabels := utiltas.NodeLabelsFromKeysAndValues(psa.TopologyAssignment.Levels, domainValues)
		toUngate[i] = podWithUngateInfo{
			pod:        pd.pod,
			nodeLabels: nodeLabels,
		}
	}
	return toUngate
}

func assignGatedPodsToDomains(
	log logr.Logger,
	psa *kueue.PodSetAssignment,
	pods []*corev1.Pod,
	psReq *kueue.PodSetTopologyRequest,
	offset int32,
	maxRank int32) []podWithDomain {
	if rankToGatedPod, ok := readRanksIfAvailable(log, psa, pods, psReq, offset, maxRank); ok {
		return assignGatedPodsToDomainsByRanks(psa, rankToGatedPod)
	}
	return assignGatedPodsToDomainsGreedy(log, psa, pods)
}

func assignGatedPodsToDomainsByRanks(
	psa *kueue.PodSetAssignment,
	rankToGatedPod map[int]*corev1.Pod) []podWithDomain {
	toUngate := make([]podWithDomain, 0)
	totalCount := 0
	for i := range psa.TopologyAssignment.Domains {
		totalCount += int(psa.TopologyAssignment.Domains[i].Count)
	}
	rankToDomainID := make([]utiltas.TopologyDomainID, totalCount)
	index := int32(0)
	for _, domain := range psa.TopologyAssignment.Domains {
		for s := range domain.Count {
			rankToDomainID[index+s] = utiltas.DomainID(domain.Values)
		}
		index += domain.Count
	}
	for rank, pod := range rankToGatedPod {
		toUngate = append(toUngate, podWithDomain{
			pod:      pod,
			domainID: rankToDomainID[rank],
		})
	}
	return toUngate
}

func assignGatedPodsToDomainsGreedy(
	log logr.Logger,
	psa *kueue.PodSetAssignment,
	pods []*corev1.Pod) []podWithDomain {
	levelKeys := psa.TopologyAssignment.Levels
	gatedPods := make([]*corev1.Pod, 0)
	domainIDToUngatedCnt := make(map[utiltas.TopologyDomainID]int32)
	for _, pod := range pods {
		if utilpod.HasGate(pod, kueuealpha.TopologySchedulingGate) {
			gatedPods = append(gatedPods, pod)
		} else {
			levelValues := utiltas.LevelValues(levelKeys, pod.Spec.NodeSelector)
			domainID := utiltas.DomainID(levelValues)
			domainIDToUngatedCnt[domainID]++
		}
	}
	log.V(3).Info("searching pods to ungate",
		"podSetName", psa.Name,
		"podSetCount", psa.Count,
		"domainIDToUngatedCount", domainIDToUngatedCnt,
		"levelKeys", levelKeys)
	toUngate := make([]podWithDomain, 0)
	for _, psaDomain := range psa.TopologyAssignment.Domains {
		domainID := utiltas.DomainID(psaDomain.Values)
		ungatedInDomainCnt := domainIDToUngatedCnt[domainID]
		remainingUngatedInDomain := max(psaDomain.Count-ungatedInDomainCnt, 0)
		if remainingUngatedInDomain > 0 {
			remainingGatedCnt := int32(max(len(gatedPods)-len(toUngate), 0))
			toUngateCnt := min(remainingUngatedInDomain, remainingGatedCnt)
			if toUngateCnt > 0 {
				podsToUngateInDomain := gatedPods[len(toUngate) : int32(len(toUngate))+toUngateCnt]
				for i := range podsToUngateInDomain {
					toUngate = append(toUngate, podWithDomain{
						pod:      podsToUngateInDomain[i],
						domainID: domainID,
					})
				}
			}
		}
	}
	return toUngate
}

func readRanksIfAvailable(log logr.Logger,
	psa *kueue.PodSetAssignment,
	pods []*corev1.Pod,
	psReq *kueue.PodSetTopologyRequest,
	offset int32,
	maxRank int32) (map[int]*corev1.Pod, bool) {
	if psReq == nil || psReq.PodIndexLabel == nil {
		return nil, false
	}
	result, err := readRanksForLabels(psa, pods, psReq, offset, maxRank)
	if err != nil {
		log.Error(err, "failed to read rank information from Pods")
		return nil, false
	}
	return result, true
}

func readRanksForLabels(
	psa *kueue.PodSetAssignment,
	pods []*corev1.Pod,
	psReq *kueue.PodSetTopologyRequest,
	offset int32,
	maxRank int32) (map[int]*corev1.Pod, error) {
	result := make(map[int]*corev1.Pod)
	podSetSize := int(*psa.Count)
	singleJobSize := podSetSize
	if psReq.SubGroupIndexLabel != nil {
		singleJobSize = podSetSize / int(*psReq.SubGroupCount)
	}

	for _, pod := range pods {
		podIndex, err := utilpod.ReadUIntFromLabelBelowBound(pod, *psReq.PodIndexLabel, int(maxRank))
		if err != nil {
			// the Pod has no rank information - ranks cannot be used
			return nil, err
		}
		rank := *podIndex - int(offset)
		if psReq.SubGroupIndexLabel != nil {
			jobIndex, err := utilpod.ReadUIntFromLabelBelowBound(pod, *psReq.SubGroupIndexLabel, int(*psReq.SubGroupCount))
			if err != nil {
				// the Pod has no Job index information - ranks cannot be used
				return nil, err
			}
			if *podIndex >= singleJobSize {
				// the pod index exceeds size, this scenario is not
				// supported by the rank-based ordering of pods.
				return nil, fmt.Errorf("pod index %v of Pod %q exceeds the single Job size: %v", *podIndex, klog.KObj(pod), singleJobSize)
			}
			rank = *podIndex + *jobIndex*singleJobSize - int(offset)
		}
		if rank >= podSetSize {
			// the rank exceeds the PodSet size, this scenario is not supported
			// by the rank-based ordering of pods.
			return nil, fmt.Errorf("rank %v of Pod %q exceeds PodSet size %v", rank, klog.KObj(pod), podSetSize)
		}
		if rank < 0 {
			return nil, fmt.Errorf("rank %v of Pod %q is below 0", rank, klog.KObj(pod))
		}
		if _, found := result[rank]; found {
			// there is a conflict in ranks, they cannot be used
			return nil, fmt.Errorf("conflicting rank %v found for pod %q", rank, klog.KObj(pod))
		}
		result[rank] = pod
	}
	return result, nil
}

func isAdmittedByTAS(w *kueue.Workload) bool {
	return w.Status.Admission != nil && workload.IsAdmitted(w) &&
		slices.ContainsFunc(w.Status.Admission.PodSetAssignments,
			func(psa kueue.PodSetAssignment) bool {
				return psa.TopologyAssignment != nil
			})
}

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
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/core"
	utilclient "sigs.k8s.io/kueue/pkg/util/client"
	"sigs.k8s.io/kueue/pkg/util/parallelize"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

const (
	ungateBatchPeriod = time.Second
)

type topologyUngater struct {
	client client.Client
}

type podWithUngateInfo struct {
	pod        *corev1.Pod
	nodeLabels map[string]string
}

var _ reconcile.Reconciler = (*topologyUngater)(nil)

// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get

func newTopologyUngater(c client.Client) *topologyUngater {
	return &topologyUngater{
		client: c,
	}
}

func (r *topologyUngater) setupWithManager(mgr ctrl.Manager, cfg *configapi.Configuration) (string, error) {
	podHandler := podHandler{}
	return TASTopologyUngater, ctrl.NewControllerManagedBy(mgr).
		Named(TASTopologyUngater).
		For(&kueue.Workload{}).
		Watches(&corev1.Pod{}, &podHandler).
		WithOptions(controller.Options{NeedLeaderElection: ptr.To(false)}).
		WithEventFilter(r).
		Complete(core.WithLeadingManager(mgr, r, &kueue.ClusterQueue{}, cfg))
}

var _ handler.EventHandler = (*podHandler)(nil)

type podHandler struct {
}

func (h *podHandler) Create(_ context.Context, e event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	pod, isPod := e.Object.(*corev1.Pod)
	if !isPod {
		return
	}
	h.queueReconcileForPod(pod, q)
}

func (h *podHandler) Update(ctx context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	oldPod, isOldPod := e.ObjectOld.(*corev1.Pod)
	newPod, isNewPod := e.ObjectNew.(*corev1.Pod)
	if !isOldPod || !isNewPod {
		return
	}
	h.queueReconcileForPod(oldPod, q)
	h.queueReconcileForPod(newPod, q)
}

func (h *podHandler) Delete(_ context.Context, e event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	pod, isPod := e.Object.(*corev1.Pod)
	if !isPod {
		return
	}
	h.queueReconcileForPod(pod, q)
}

func (h *podHandler) queueReconcileForPod(pod *corev1.Pod, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	if pod == nil {
		return
	}
	if !utilpod.HasGate(pod, kueuealpha.TopologySchedulingGate) {
		return
	}
	if wlName, found := pod.Annotations[kueuealpha.WorkloadAnnotation]; found {
		q.AddAfter(reconcile.Request{NamespacedName: types.NamespacedName{
			Name:      wlName,
			Namespace: pod.Namespace,
		}}, ungateBatchPeriod)
	}
}

func (h *podHandler) Generic(context.Context, event.GenericEvent, workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (r *topologyUngater) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := ctrl.LoggerFrom(ctx).WithValues("workload", req.NamespacedName.Name)
	log.V(2).Info("Reconcile Topology Ungater")

	wl := &kueue.Workload{}
	if err := r.client.Get(ctx, req.NamespacedName, wl); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return reconcile.Result{}, err
		}
		log.Info("workload not found")
		return reconcile.Result{}, nil
	}
	if wl.Status.Admission == nil {
		log.Info("workload is not admitted")
		return reconcile.Result{}, nil
	}

	allToUngate := make([]podWithUngateInfo, 0)
	for _, psa := range wl.Status.Admission.PodSetAssignments {
		if psa.TopologyAssignment != nil {
			toUngate, err := r.podsetPodsToUngate(ctx, log, wl, &psa)
			if err != nil {
				log.Error(err, "failed to identify pods to ungate", "podset", psa.Name, "count", psa.Count)
				return reconcile.Result{}, err
			} else {
				log.Info("identified pods to ungate for podset", "podset", psa.Name, "count", len(toUngate))
				allToUngate = append(allToUngate, toUngate...)
			}
		}
	}
	var err error
	if len(allToUngate) > 0 {
		log.V(2).Info("identified pods to ungate", "count", len(allToUngate))
		err = parallelize.Until(ctx, len(allToUngate), func(i int) error {
			podWithUngateInfo := &allToUngate[i]
			e := utilclient.Patch(ctx, r.client, podWithUngateInfo.pod, true, func() (bool, error) {
				log.V(3).Info("ungating pod", "pod", klog.KObj(podWithUngateInfo.pod), "nodeLabels", podWithUngateInfo.nodeLabels)
				utilpod.Ungate(podWithUngateInfo.pod, kueuealpha.TopologySchedulingGate)
				if podWithUngateInfo.pod.Spec.NodeSelector == nil {
					podWithUngateInfo.pod.Spec.NodeSelector = make(map[string]string)
				}
				for labelKey, labelValue := range podWithUngateInfo.nodeLabels {
					podWithUngateInfo.pod.Spec.NodeSelector[labelKey] = labelValue
				}
				return true, nil
			})
			if e != nil {
				log.Error(e, "failed ungating pod", "pod", klog.KObj(podWithUngateInfo.pod))
			}
			return e
		})
		if err != nil {
			return reconcile.Result{}, err
		}
	}
	return reconcile.Result{}, nil
}

func (r *topologyUngater) Create(event event.CreateEvent) bool {
	wl, isWl := event.Object.(*kueue.Workload)
	if isWl {
		return isTASWorkload(wl)
	}
	return true
}

func (r *topologyUngater) Delete(event event.DeleteEvent) bool {
	wl, isWl := event.Object.(*kueue.Workload)
	if isWl {
		return isTASWorkload(wl)
	}
	return true
}

func (r *topologyUngater) Update(event event.UpdateEvent) bool {
	_, isOldWl := event.ObjectOld.(*kueue.Workload)
	newWl, isNewWl := event.ObjectNew.(*kueue.Workload)
	if isOldWl && isNewWl {
		return isTASWorkload(newWl)
	}
	return true
}

func isTASWorkload(wl *kueue.Workload) bool {
	if wl.Status.Admission == nil {
		return false
	}
	for _, psa := range wl.Status.Admission.PodSetAssignments {
		if psa.TopologyAssignment != nil {
			return true
		}
	}
	return false
}

func (r *topologyUngater) Generic(event event.GenericEvent) bool {
	return false
}

func (r *topologyUngater) podsetPodsToUngate(ctx context.Context, log logr.Logger, wl *kueue.Workload, psa *kueue.PodSetAssignment) ([]podWithUngateInfo, error) {
	levelKeys := psa.TopologyAssignment.Levels
	domainIDToLabelValues := make(map[utiltas.TopologyDomainID][]string)
	domainIDToExpectedCount := make(map[utiltas.TopologyDomainID]int32)
	for _, psaDomain := range psa.TopologyAssignment.Domains {
		domainID := utiltas.DomainID(psaDomain.Values)
		domainIDToExpectedCount[domainID] = psaDomain.Count
		domainIDToLabelValues[domainID] = psaDomain.Values
	}
	pods, err := r.podsForDomain(ctx, wl.Namespace, wl.Name, psa.Name)
	if err != nil {
		return nil, err
	}
	gatedPods := make([]*corev1.Pod, 0)
	domainIDToUngatedCnt := make(map[utiltas.TopologyDomainID]int32)
	for i := range pods {
		pod := pods[i]
		isGated := utilpod.HasGate(pod, kueuealpha.TopologySchedulingGate)
		if isGated {
			gatedPods = append(gatedPods, pod)
		} else {
			levelValues := utiltas.LevelValues(levelKeys, pod.Spec.NodeSelector)
			domainID := utiltas.DomainID(levelValues)
			domainIDToUngatedCnt[domainID]++
		}
	}
	log.V(5).Info("searching pods to ungate",
		"podSetName", psa.Name,
		"podSetCount", psa.Count,
		"domainIDToUngatedCount", domainIDToUngatedCnt,
		"domainIDToLabelValues", domainIDToLabelValues,
		"levelKeys", levelKeys)
	toUngate := make([]podWithUngateInfo, 0)
	for domainID, expectedInDomainCnt := range domainIDToExpectedCount {
		ungatedInDomainCnt := domainIDToUngatedCnt[domainID]
		remainingUngatedInDomain := max(expectedInDomainCnt-ungatedInDomainCnt, 0)
		if remainingUngatedInDomain > 0 {
			domainValues := domainIDToLabelValues[domainID]

			nodeLabels := utiltas.NodeLabelsFromKeysAndValues(levelKeys, domainValues)
			remainingGatedCnt := int32(max(len(gatedPods)-len(toUngate), 0))
			toUngateCnt := min(remainingUngatedInDomain, remainingGatedCnt)
			if toUngateCnt > 0 {
				podsToUngateInDomain := gatedPods[len(toUngate) : int32(len(toUngate))+toUngateCnt]
				for i := range podsToUngateInDomain {
					toUngate = append(toUngate, podWithUngateInfo{
						pod:        podsToUngateInDomain[i],
						nodeLabels: nodeLabels,
					})
				}
			}
		}
	}
	return toUngate, nil
}

func (r *topologyUngater) podsForDomain(ctx context.Context, ns, wlName, psName string) ([]*corev1.Pod, error) {
	var pods corev1.PodList
	if err := r.client.List(ctx, &pods, client.InNamespace(ns), client.MatchingLabels{
		kueuealpha.PodSetLabel: psName,
	}, client.MatchingFields{
		workloadNameKey: wlName,
	}); err != nil {
		return nil, err
	}
	result := make([]*corev1.Pod, 0)
	for _, p := range pods.Items {
		result = append(result, &p)
	}
	return result, nil
}

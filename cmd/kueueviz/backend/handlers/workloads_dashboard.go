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

package handlers

import (
	"context"
	"fmt"

	"github.com/gin-gonic/gin"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	kueueapi "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
)

// WorkloadsDashboardWebSocketHandler streams workloads along with attached pod details
// Watches Workloads, Pods, ClusterQueues, LocalQueues, and ResourceFlavors for comprehensive updates
func (h *Handlers) WorkloadsDashboardWebSocketHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Extract namespace query parameter if provided
		namespace := c.Query("namespace")

		// Create a closure that captures the namespace parameter
		dataFetcher := func(ctx context.Context) (any, error) {
			return h.fetchDashboardData(ctx, namespace)
		}

		h.GenericWebSocketHandler(dataFetcher,
			WorkloadsGVK(),
			PodsGVK(),
			ClusterQueuesGVK(),
			LocalQueuesGVK(),
			ResourceFlavorsGVK(),
		)(c)
	}
}

func (h *Handlers) fetchDashboardData(ctx context.Context, namespace string) (map[string]any, error) {
	resourceFlavors, err := h.fetchResourceFlavors(ctx)
	if err != nil {
		return nil, err
	}
	clusterQueues, err := h.fetchClusterQueues(ctx)
	if err != nil {
		return nil, err
	}
	localQueues, err := h.fetchLocalQueues(ctx)
	if err != nil {
		return nil, err
	}
	workloads, err := h.fetchWorkloadsDashboardData(ctx, namespace)
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"flavors":       resourceFlavors,
		"clusterQueues": clusterQueues,
		"queues":        localQueues,
		"workloads":     workloads,
	}
	return result, nil
}

func (h *Handlers) fetchWorkloadsDashboardData(ctx context.Context, namespace string) (any, error) {
	wql := &kueueapi.WorkloadList{}
	err := h.client.List(ctx, wql, ctrlclient.InNamespace(namespace))

	if err != nil {
		return nil, fmt.Errorf("error fetching workloads in namespace %s: %w", namespace, err)
	}

	workloadsByUID := make(map[types.UID]string, len(wql.Items))
	processedWorkloads := make([]workloadResult, 0, len(wql.Items))

	podIndex, err := buildWorkloadPodsIndex(ctx, h.client, wql.Items)
	if err != nil {
		return nil, err
	}

	for _, workload := range wql.Items {
		workloadName := workload.Name
		workloadUID := workload.UID
		workloadPods := podIndex.podsFor(&workload)

		cond := meta.FindStatusCondition(workload.Status.Conditions, kueueapi.WorkloadPreempted)

		preemption := map[string]any{"preempted": false, "reason": ""}
		if cond != nil && cond.Status == metav1.ConditionTrue {
			preemption["preempted"] = true
			preemption["reason"] = cond.Reason
		}

		workloadsByUID[workloadUID] = workloadName
		processedWorkloads = append(processedWorkloads, workloadResult{
			Workload:   &workload,
			Preemption: preemption,
			Pods:       workloadPods,
		})
	}
	workloads := map[string]any{
		"items":            processedWorkloads,
		"workloads_by_uid": workloadsByUID,
	}

	return workloads, nil
}

// workloadPodsIndex stores dashboard pod details by namespace.
// The namespace key preserves the All namespaces dashboard view semantics.
type workloadPodsIndex struct {
	podsByNamespace map[string]namespacePods
}

// namespacePods stores the pod details of one namespace by controller UID and
// by the name of the Workload that a pod group Pod belongs to.
type namespacePods struct {
	byControllerUID map[string][]map[string]any
	byWorkloadName  map[string][]map[string]any
}

// buildWorkloadPodsIndex lists pods once per workload namespace and indexes them
// for lookup by each workload's job UID or name.
func buildWorkloadPodsIndex(ctx context.Context, client Client, workloads []kueueapi.Workload) (workloadPodsIndex, error) {
	workloadNamespaces := make(map[string]struct{})
	for i := range workloads {
		workloadNamespaces[workloads[i].Namespace] = struct{}{}
	}

	index := workloadPodsIndex{
		podsByNamespace: make(map[string]namespacePods, len(workloadNamespaces)),
	}
	for namespace := range workloadNamespaces {
		pl := &corev1.PodList{}
		if err := client.List(ctx, pl, ctrlclient.InNamespace(namespace)); err != nil {
			return workloadPodsIndex{}, fmt.Errorf("error fetching pods in namespace %s: %w", namespace, err)
		}

		pods := namespacePods{
			byControllerUID: make(map[string][]map[string]any),
			byWorkloadName:  make(map[string][]map[string]any),
		}
		for _, pod := range pl.Items {
			controllerUID := pod.GetLabels()["controller-uid"]
			workloadName := podWorkloadName(&pod)
			if controllerUID == "" && workloadName == "" {
				continue
			}
			podDetails := map[string]any{
				"name":   pod.GetName(),
				"status": pod.Status,
			}
			// Empty keys are skipped: they would match every Workload without a job UID.
			if controllerUID != "" {
				pods.byControllerUID[controllerUID] = append(pods.byControllerUID[controllerUID], podDetails)
			}
			if workloadName != "" {
				pods.byWorkloadName[workloadName] = append(pods.byWorkloadName[workloadName], podDetails)
			}
		}
		index.podsByNamespace[namespace] = pods
	}
	return index, nil
}

// podsFor returns the pod details of the workload. Pods created by a batch Job
// match the workload's job UID. Pod group Pods, including those of StatefulSets
// and LeaderWorkerSets, have no controller UID, so they are matched by the
// workload name.
func (i workloadPodsIndex) podsFor(wl *kueueapi.Workload) []map[string]any {
	pods := i.podsByNamespace[wl.Namespace]
	if jobPods := pods.byControllerUID[wl.Labels[controllerconstants.JobUIDLabel]]; len(jobPods) != 0 {
		return jobPods
	}
	return pods.byWorkloadName[wl.Name]
}

// podWorkloadName returns the name of the Workload that the pod belongs to. Like
// the Kueue pod controller, it prefers the prebuilt Workload name to the pod
// group name. StatefulSet and LeaderWorkerSet Pods set both to the same name.
func podWorkloadName(pod *corev1.Pod) string {
	if name := pod.GetAnnotations()[controllerconstants.PrebuiltWorkloadAnnotation]; name != "" {
		return name
	}
	if name := pod.GetLabels()[controllerconstants.PrebuiltWorkloadLabel]; name != "" {
		return name
	}
	return podGroupName(pod)
}

// podGroupName returns the pod group name of the pod. Kueue writes it to the
// annotation by default and to the label when the WorkloadIdentifierAnnotations
// feature gate is disabled.
func podGroupName(pod *corev1.Pod) string {
	if name := pod.GetAnnotations()[podconstants.GroupNameAnnotation]; name != "" {
		return name
	}
	return pod.GetLabels()[podconstants.GroupNameLabel]
}

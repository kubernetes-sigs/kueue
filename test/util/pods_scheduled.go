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

package util

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/onsi/gomega"
	awv1beta2 "github.com/project-codeflare/appwrapper/api/v1beta2"
	awutils "github.com/project-codeflare/appwrapper/pkg/utils"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	rayutils "github.com/ray-project/kuberay/ray-operator/controllers/ray/utils"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/util/rand"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

// CreateScheduledPods creates count pods with PodScheduled=True and the given labels.
func CreateScheduledPods(ctx context.Context, c client.Client, namespace string, podLabels map[string]string, count int) error {
	for i := range count {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("scheduled-pod-%s-%d", rand.String(5), i),
				Namespace: namespace,
				Labels:    podLabels,
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name:  "c",
					Image: GetAgnHostImage(),
				}},
			},
		}
		MustCreate(ctx, c, pod)
		pod.Status.Phase = corev1.PodPending
		pod.Status.Conditions = []corev1.PodCondition{{
			Type:   corev1.PodScheduled,
			Status: corev1.ConditionTrue,
		}}
		if err := c.Status().Update(ctx, pod); err != nil {
			return err
		}
	}
	return nil
}

// LabelsFromSelector parses a label selector string into a map of key=value pairs.
func LabelsFromSelector(selector string) (map[string]string, error) {
	if selector == "" {
		return nil, errors.New("empty selector")
	}
	sel, err := labels.Parse(selector)
	if err != nil {
		return nil, err
	}
	reqs, _ := sel.Requirements()
	m := make(map[string]string, len(reqs))
	for _, r := range reqs {
		if r.Operator() != selection.Equals {
			return nil, fmt.Errorf("unsupported selector operator %v", r.Operator())
		}
		vals := r.Values().UnsortedList()
		if len(vals) != 1 {
			return nil, fmt.Errorf("unsupported selector values %v", vals)
		}
		val := vals[0]
		if existing, ok := m[r.Key()]; ok {
			if existing != val {
				return nil, fmt.Errorf("conflicting selector values for label %q", r.Key())
			}
			continue
		}
		m[r.Key()] = val
	}
	return m, nil
}

// CreateScheduledPodsForSelector creates count pods matching the given label selector.
func CreateScheduledPodsForSelector(ctx context.Context, c client.Client, namespace, selector string, count int) error {
	podLabels, err := LabelsFromSelector(selector)
	if err != nil {
		return err
	}
	return CreateScheduledPods(ctx, c, namespace, podLabels, count)
}

// CreateScheduledPodsForJob creates scheduled pods for a batch Job.
func CreateScheduledPodsForJob(ctx context.Context, c client.Client, job *batchv1.Job, count int) error {
	podLabels := map[string]string{
		batchv1.JobNameLabel: job.Name,
	}
	return CreateScheduledPods(ctx, c, job.Namespace, podLabels, count)
}

// TriggerReconcile forces a job-framework reconciler to retry by updating a test annotation.
func TriggerReconcile(ctx context.Context, c client.Client, obj client.Object) error {
	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations["kueue.x-k8s.io/test-reconcile-trigger"] = strconv.FormatInt(time.Now().UnixNano(), 10)
	obj.SetAnnotations(annotations)
	return c.Update(ctx, obj)
}

// TriggerJobReconcile forces the job controller to reconcile by updating a test annotation.
func TriggerJobReconcile(ctx context.Context, c client.Client, job *batchv1.Job) error {
	return TriggerReconcile(ctx, c, job)
}

// TriggerReconcileEventually retries TriggerReconcile until it succeeds or times out.
func TriggerReconcileEventually(ctx context.Context, c client.Client, key client.ObjectKey, obj client.Object) {
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(c.Get(ctx, key, obj)).Should(gomega.Succeed())
		g.Expect(TriggerReconcile(ctx, c, obj)).Should(gomega.Succeed())
	}, Timeout, Interval).Should(gomega.Succeed())
}

// TriggerReconcileEventuallyWithOffset is like TriggerReconcileEventually with a caller stack offset.
func TriggerReconcileEventuallyWithOffset(ctx context.Context, c client.Client, key client.ObjectKey, obj client.Object, offset int) {
	gomega.EventuallyWithOffset(offset, func(g gomega.Gomega) {
		g.Expect(c.Get(ctx, key, obj)).Should(gomega.Succeed())
		g.Expect(TriggerReconcile(ctx, c, obj)).Should(gomega.Succeed())
	}, Timeout, Interval).Should(gomega.Succeed())
}

// TotalPodCountFromWorkload sums pod counts across all pod sets in a workload.
func TotalPodCountFromWorkload(wl *kueue.Workload) int {
	count := 0
	for _, ps := range wl.Spec.PodSets {
		count += int(ps.Count)
	}
	return count
}

// PodSetsFromWorkload copies pod set names and counts from a workload.
func PodSetsFromWorkload(wl *kueue.Workload) []kueue.PodSet {
	podSets := make([]kueue.PodSet, len(wl.Spec.PodSets))
	for i, ps := range wl.Spec.PodSets {
		podSets[i] = kueue.PodSet{Name: ps.Name, Count: ps.Count}
	}
	return podSets
}

// CreateScheduledPodsForWorkload creates scheduled pods for all pod sets in a workload.
func CreateScheduledPodsForWorkload(ctx context.Context, c client.Client, wl *kueue.Workload, namespace, selector string) error {
	return CreateScheduledPodsForSelector(ctx, c, namespace, selector, TotalPodCountFromWorkload(wl))
}

// EnsureAppWrapperComponentStatusAndScheduledPods initializes AppWrapper component status and seeds scheduled pods.
func EnsureAppWrapperComponentStatusAndScheduledPods(ctx context.Context, c client.Client, aw *awv1beta2.AppWrapper) error {
	if err := awutils.EnsureComponentStatusInitialized(aw); err != nil {
		return err
	}
	if err := c.Status().Update(ctx, aw); err != nil {
		return err
	}
	minCount := 0
	for _, cs := range aw.Status.ComponentStatus {
		for _, ps := range cs.PodSets {
			replicas := int32(1)
			if ps.Replicas != nil {
				replicas = *ps.Replicas
			}
			minCount += int(replicas)
		}
	}
	if minCount == 0 {
		return nil
	}
	selector := fmt.Sprintf("%s=%s", awv1beta2.AppWrapperLabel, aw.Name)
	return CreateScheduledPodsForSelector(ctx, c, aw.Namespace, selector, minCount)
}

// CreateScheduledPodsForRayCluster creates scheduled pods for each pod set in a Ray cluster.
func CreateScheduledPodsForRayCluster(ctx context.Context, c client.Client, namespace, rayClusterName string, podSets []kueue.PodSet) error {
	if rayClusterName == "" {
		return errors.New("ray cluster name is empty")
	}
	const headGroupPodSetName = "head"
	for _, ps := range podSets {
		podLabels := map[string]string{
			rayutils.RayClusterLabelKey: rayClusterName,
		}
		if string(ps.Name) == headGroupPodSetName {
			podLabels[rayutils.RayNodeTypeLabelKey] = string(rayv1.HeadNode)
		} else {
			podLabels[rayutils.RayNodeTypeLabelKey] = string(rayv1.WorkerNode)
			podLabels[rayutils.RayNodeGroupLabelKey] = string(ps.Name)
		}
		if err := CreateScheduledPods(ctx, c, namespace, podLabels, int(ps.Count)); err != nil {
			return err
		}
	}
	return nil
}

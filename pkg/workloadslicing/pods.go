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

package workloadslicing

import (
	"cmp"
	"context"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/util/parallelize"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	"sigs.k8s.io/kueue/pkg/workload"
)

// nameForPod returns the annotated Workload name, preferring a present slice-chain annotation.
func nameForPod(pod *corev1.Pod) string {
	if name, found := pod.Annotations[kueue.WorkloadSliceNameAnnotation]; found {
		return name
	}
	return pod.Annotations[kueue.WorkloadAnnotation]
}

// KeyForPod returns the annotated Workload key, or nil if the name is empty.
func KeyForPod(pod *corev1.Pod) *types.NamespacedName {
	if name := nameForPod(pod); name != "" {
		return &types.NamespacedName{Namespace: pod.Namespace, Name: name}
	}
	return nil
}

// ListPodsForWorkloadSlice lists pods belonging to a workload slice by querying
// the WorkloadSliceNameKey index. Additional list options (e.g., field and label selectors)
// can be passed for filtering. Returns pointers to avoid copying large Pod structs.
func ListPodsForWorkloadSlice(ctx context.Context, c client.Client, namespace, workloadSliceName string, opts ...client.ListOption) ([]*corev1.Pod, error) {
	var podList corev1.PodList
	listOpts := (&client.ListOptions{Namespace: namespace}).ApplyOptions(opts)
	fieldSelector := fields.OneTermEqualSelector(indexer.WorkloadSliceNameKey, workloadSliceName)
	// MatchingFields replaces existing selectors, so merge the slice constraint explicitly.
	if listOpts.FieldSelector != nil {
		fieldSelector = fields.AndSelectors(fieldSelector, listOpts.FieldSelector)
	}
	listOpts.FieldSelector = fieldSelector
	if err := c.List(ctx, &podList, listOpts); err != nil {
		return nil, err
	}
	result := make([]*corev1.Pod, len(podList.Items))
	for i := range podList.Items {
		result[i] = &podList.Items[i]
	}
	return result, nil
}

// runningPodsPerPodSet returns the Pods of the workload slice that hold their
// resources, by PodSet: the ungated Pods that are not terminated, including the
// terminating ones.
func runningPodsPerPodSet(ctx context.Context, c client.Client, wl *kueue.Workload) (map[kueue.PodSetReference][]*corev1.Pod, error) {
	pods, err := ListPodsForWorkloadSlice(ctx, c, wl.Namespace, SliceName(wl))
	if err != nil {
		return nil, err
	}
	running := make(map[kueue.PodSetReference][]*corev1.Pod)
	for _, p := range pods {
		if utilpod.IsTerminated(p) || utilpod.HasGate(p, kueue.ElasticJobSchedulingGate) {
			continue
		}
		ps := kueue.PodSetReference(p.Labels[constants.PodSetLabel])
		running[ps] = append(running[ps], p)
	}
	return running, nil
}

// DeletePodsAboveCounts deletes the newest running Pods of the workload slice
// above counts, by PodSet. Terminating Pods count toward counts and are not
// deleted again. It returns whether any Pod was deleted.
func DeletePodsAboveCounts(ctx context.Context, c client.Client, wl *kueue.Workload, counts workload.PodSetsCounts) (bool, error) {
	running, err := runningPodsPerPodSet(ctx, c, wl)
	if err != nil {
		return false, err
	}
	var toDelete []*corev1.Pod
	for ps, count := range counts {
		pods := running[ps]
		if int32(len(pods)) <= count {
			continue
		}
		pods = slices.DeleteFunc(pods, func(p *corev1.Pod) bool { return p.DeletionTimestamp != nil })
		if extra := int32(len(pods)) - count; extra > 0 {
			slices.SortFunc(pods, func(a, b *corev1.Pod) int {
				return cmp.Or(b.CreationTimestamp.Compare(a.CreationTimestamp.Time), strings.Compare(b.Name, a.Name))
			})
			toDelete = append(toDelete, pods[:extra]...)
		}
	}
	log := ctrl.LoggerFrom(ctx)
	err = parallelize.Until(ctx, len(toDelete), func(i int) error {
		log.V(3).Info("Deleting elastic pod above the reclaim target count", "pod", klog.KObj(toDelete[i]))
		return client.IgnoreNotFound(c.Delete(ctx, toDelete[i]))
	})
	return len(toDelete) > 0, err
}

// PodsAboveCounts reports whether any PodSet of the workload slice still has
// more running Pods than counts.
func PodsAboveCounts(ctx context.Context, c client.Client, wl *kueue.Workload, counts workload.PodSetsCounts) (bool, error) {
	running, err := runningPodsPerPodSet(ctx, c, wl)
	if err != nil {
		return false, err
	}
	for ps, count := range counts {
		if int32(len(running[ps])) > count {
			return true, nil
		}
	}
	return false, nil
}

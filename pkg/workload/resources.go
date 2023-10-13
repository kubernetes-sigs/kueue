/*
Copyright 2023 The Kubernetes Authors.

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
package workload

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/util/limitrange"
	"sigs.k8s.io/kueue/pkg/util/resource"
)

// We do not verify Pod's RuntimeClass legality here as this will be performed in admission controller.
// As a result, the pod's Overhead is not always correct. E.g. if we set a non-existent runtime class name to
// `pod.Spec.RuntimeClassName` and we also set the `pod.Spec.Overhead`, in real world, the pod creation will be
// rejected due to the mismatch with RuntimeClass. However, in the future we assume that they are correct.
func handlePodOverhead(ctx context.Context, cl client.Client, pts map[string]*corev1.PodTemplateSpec) []error {
	var errs []error
	for name, pt := range pts {
		podSpec := &pt.Spec
		if podSpec.RuntimeClassName != nil && len(podSpec.Overhead) == 0 {
			var runtimeClass nodev1.RuntimeClass
			if err := cl.Get(ctx, types.NamespacedName{Name: *podSpec.RuntimeClassName}, &runtimeClass); err != nil {
				errs = append(errs, fmt.Errorf("in pod template %s: %w", name, err))
				continue
			}
			if runtimeClass.Overhead != nil {
				podSpec.Overhead = runtimeClass.Overhead.PodFixed
			}
		}
	}
	return errs
}

func handlePodLimitRange(ctx context.Context, cl client.Client, wl *kueue.Workload, pts map[string]*corev1.PodTemplateSpec) error {
	// get the list of limit ranges
	var list corev1.LimitRangeList
	if err := cl.List(ctx, &list, &client.ListOptions{Namespace: wl.Namespace}, client.MatchingFields{indexer.LimitRangeHasContainerType: "true"}); err != nil {
		return err
	}

	if len(list.Items) == 0 {
		return nil
	}
	summary := limitrange.Summarize(list.Items...)
	containerLimits, found := summary[corev1.LimitTypeContainer]
	if !found {
		return nil
	}

	for _, pt := range pts {
		pod := pt
		for ci := range pod.Spec.InitContainers {
			res := &pod.Spec.InitContainers[ci].Resources
			res.Limits = resource.MergeResourceListKeepFirst(res.Limits, containerLimits.Default)
			res.Requests = resource.MergeResourceListKeepFirst(res.Requests, containerLimits.DefaultRequest)
		}
		for ci := range pod.Spec.Containers {
			res := &pod.Spec.Containers[ci].Resources
			res.Limits = resource.MergeResourceListKeepFirst(res.Limits, containerLimits.Default)
			res.Requests = resource.MergeResourceListKeepFirst(res.Requests, containerLimits.DefaultRequest)
		}
	}
	return nil
}

func handleLimitsToRequests(pts map[string]*corev1.PodTemplateSpec) {
	for _, pt := range pts {
		pod := &pt.Spec
		for ci := range pod.InitContainers {
			res := &pod.InitContainers[ci].Resources
			res.Requests = resource.MergeResourceListKeepFirst(res.Requests, res.Limits)
		}
		for ci := range pod.Containers {
			res := &pod.Containers[ci].Resources
			res.Requests = resource.MergeResourceListKeepFirst(res.Requests, res.Limits)
		}
	}
}

// AdjustResources adjusts the resource requests of a workload based on:
// - PodOverhead
// - LimitRanges
// - Limits
func AdjustResources(ctx context.Context, cl client.Client, wl *kueue.Workload, pts map[string]*corev1.PodTemplateSpec) {
	log := ctrl.LoggerFrom(ctx)
	for _, err := range handlePodOverhead(ctx, cl, pts) {
		log.Error(err, "Failures adjusting requests for pod overhead")
	}
	if err := handlePodLimitRange(ctx, cl, wl, pts); err != nil {
		log.Error(err, "Failed adjusting requests for LimitRanges")
	}
	handleLimitsToRequests(pts)
}

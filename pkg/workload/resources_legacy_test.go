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

package workload

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/util/limitrange"
	"sigs.k8s.io/kueue/pkg/util/resource"
)

// Preserve the previous mutating implementation as an independent oracle for
// the effective-resource migration. Production uses Info's resource view.
// We do not verify Pod's RuntimeClass legality here as this will be performed in admission controller.
// As a result, the pod's Overhead is not always correct. E.g. if we set a non-existent runtime class name to
// `pod.Spec.RuntimeClassName` and we also set the `pod.Spec.Overhead`, in real world, the pod creation will be
// rejected due to the mismatch with RuntimeClass. However, in the future we assume that they are correct.
func handlePodOverhead(ctx context.Context, cl client.Client, wl *kueue.Workload) []error {
	var errs []error
	for i := range wl.Spec.PodSets {
		podSpec := &wl.Spec.PodSets[i].Template.Spec
		if podSpec.RuntimeClassName != nil && len(podSpec.Overhead) == 0 {
			var runtimeClass nodev1.RuntimeClass
			if err := cl.Get(ctx, types.NamespacedName{Name: *podSpec.RuntimeClassName}, &runtimeClass); err != nil {
				errs = append(errs, fmt.Errorf("in podSet %s: %w", wl.Spec.PodSets[i].Name, err))
				continue
			}
			if runtimeClass.Overhead != nil {
				podSpec.Overhead = runtimeClass.Overhead.PodFixed
			}
		}
	}
	return errs
}

func handlePodLimitRange(ctx context.Context, cl client.Client, wl *kueue.Workload) error {
	// get the list of limit ranges
	var limitRanges corev1.LimitRangeList
	if err := cl.List(ctx, &limitRanges, &client.ListOptions{Namespace: wl.Namespace}, client.MatchingFields{indexer.LimitRangeHasContainerOrPodType: "true"}); err != nil {
		return err
	}

	if len(limitRanges.Items) == 0 {
		return nil
	}
	summary := limitrange.Summarize(limitRanges.Items...)
	podLimits, foundPodLimits := summary[corev1.LimitTypePod]
	containerLimits, foundContainerLimits := summary[corev1.LimitTypeContainer]
	if !foundPodLimits && !foundContainerLimits {
		return nil
	}

	for pi := range wl.Spec.PodSets {
		pod := &wl.Spec.PodSets[pi].Template.Spec
		if foundContainerLimits {
			for ci := range pod.InitContainers {
				res := &pod.InitContainers[ci].Resources
				res.Limits = resource.MergeResourceListKeepFirst(res.Limits, containerLimits.Default)
				res.Requests = resource.MergeResourceListKeepFirst(res.Requests, containerLimits.DefaultRequest)
			}
			for ci := range pod.Containers {
				res := &pod.Containers[ci].Resources
				res.Limits = resource.MergeResourceListKeepFirst(res.Limits, containerLimits.Default)
				res.Requests = resource.MergeResourceListKeepFirst(res.Requests, containerLimits.DefaultRequest)
			}
		}
		// Pod-level resources (KEP-2837) are an optional pointer, only set when the
		// PodLevelResources feature is enabled and used.
		if pod.Resources != nil && foundPodLimits {
			pod.Resources.Limits = resource.MergeResourceListKeepFirst(pod.Resources.Limits, podLimits.Default)
			pod.Resources.Requests = resource.MergeResourceListKeepFirst(pod.Resources.Requests, podLimits.DefaultRequest)
		}
	}
	return nil
}

func handleLimitsToRequests(wl *kueue.Workload) {
	for pi := range wl.Spec.PodSets {
		UseLimitsAsMissingRequestsInPod(&wl.Spec.PodSets[pi].Template.Spec)
	}
}

// AdjustResources adjusts the resource requests of a workload based on:
// - PodOverhead
// - LimitRanges
// - Limits
func AdjustResources(ctx context.Context, cl client.Client, wl *kueue.Workload) {
	log := ctrl.LoggerFrom(ctx)
	for _, err := range handlePodOverhead(ctx, cl, wl) {
		log.Error(err, "Failures adjusting requests for pod overhead")
	}
	// Copy limits into missing requests before applying the LimitRange
	// defaults, mirroring the API server, where requests default from limits
	// at object defaulting, before the LimitRanger admission plugin runs.
	// The Pods created after admission request their limits, so the Workload
	// must be accounted the same way.
	handleLimitsToRequests(wl)
	if err := handlePodLimitRange(ctx, cl, wl); err != nil {
		log.Error(err, "Failed adjusting requests for LimitRanges")
	}
}

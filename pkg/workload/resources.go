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
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation/field"
	resourcehelpers "k8s.io/component-helpers/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/util/limitrange"
	"sigs.k8s.io/kueue/pkg/util/resource"
)

var (
	PodSetsPath          = field.NewPath("spec").Child("podSets")
	ErrNamespaceMismatch = errors.New("workload namespace doesn't match ClusterQueue selector")
	ErrInternal          = errors.New("internal lookup failure")
)

const (
	RequestsMustNotExceedLimitMessage = "requests must not exceed its limits"

	ErrInvalidWLResources                        = "resources validation failed"
	ErrLimitRangeConstraintsUnsatisfiedResources = "resources didn't satisfy LimitRange constraints"
)

// UseLimitsAsMissingRequestsInPod adjust the resource requests to the limits value
// for resources that only set limits. It only covers the (init) containers; the
// pod-level requests are handled by DefaultPodLevelRequests.
func UseLimitsAsMissingRequestsInPod(pod *corev1.PodSpec) {
	for ci := range pod.InitContainers {
		res := &pod.InitContainers[ci].Resources
		res.Requests = resource.MergeResourceListKeepFirst(res.Requests, res.Limits)
	}
	for ci := range pod.Containers {
		res := &pod.Containers[ci].Resources
		res.Requests = resource.MergeResourceListKeepFirst(res.Requests, res.Limits)
	}
}

// DefaultPodLevelRequests fills the missing pod-level resource requests the way
// the API server does: from the aggregate requests of the containers, for the
// overcommittable resources the containers request, and from the pod-level
// limits for the remaining supported resources. Servers 1.37 and newer defer
// this defaulting until after admission, so their aggregates include the
// container defaults that LimitRanges apply; earlier servers run it before
// those defaults. ApplyLimitRangeAndPodLevelDefaults picks the matching order.
func DefaultPodLevelRequests(pod *corev1.PodSpec) {
	// Pod-level resources (KEP-2837) are an optional pointer, only set when the
	// PodLevelResources feature is enabled and used.
	if pod.Resources == nil || (len(pod.Resources.Requests) == 0 && len(pod.Resources.Limits) == 0) {
		return
	}
	podRequests := pod.Resources.Requests
	if podRequests == nil {
		podRequests = make(corev1.ResourceList)
	}
	aggregatedRequests := resourcehelpers.AggregateContainerRequests(&corev1.Pod{Spec: *pod}, resourcehelpers.PodResourcesOptions{})
	for name, quantity := range aggregatedRequests {
		if _, found := podRequests[name]; found || !resourcehelpers.IsSupportedPodLevelResource(name) {
			continue
		}
		// Only overcommittable resources default from the containers; among the
		// pod-level resources that excludes hugepages.
		if strings.HasPrefix(string(name), corev1.ResourceHugePagesPrefix) {
			continue
		}
		podRequests[name] = quantity.DeepCopy()
	}
	// When no containers specify requests for a resource, the pod-level
	// requests default to the pod-level limits, including the hugepage limits
	// defaulted by DefaultHugePagePodLevelLimits.
	for name, limit := range pod.Resources.Limits {
		if _, found := podRequests[name]; found || !resourcehelpers.IsSupportedPodLevelResource(name) {
			continue
		}
		podRequests[name] = limit.DeepCopy()
	}
	if len(podRequests) > 0 {
		pod.Resources.Requests = podRequests
	}
}

// DefaultHugePagePodLevelLimits mirrors the API server defaulting of pod-level
// hugepage limits from the aggregated container limits: when containers set
// hugepage limits and the pod-level resources are partly specified already,
// the pod-level limit is defaulted to the aggregate unless the pod-level
// requests or limits carry the resource. The pod-level request defaulting
// then copies the limit into the missing pod-level requests.
func DefaultHugePagePodLevelLimits(pod *corev1.PodSpec) {
	if pod.Resources == nil || (len(pod.Resources.Requests) == 0 && len(pod.Resources.Limits) == 0) {
		return
	}
	podLimits := pod.Resources.Limits
	if podLimits == nil {
		podLimits = make(corev1.ResourceList)
	}
	aggregatedLimits := resourcehelpers.AggregateContainerLimits(&corev1.Pod{Spec: *pod}, resourcehelpers.PodResourcesOptions{})
	for name, quantity := range aggregatedLimits {
		// Only hugepages default here; cpu and memory are overcommittable and
		// default in DefaultPodLevelRequests.
		if !resourcehelpers.IsSupportedPodLevelResource(name) || !strings.HasPrefix(string(name), corev1.ResourceHugePagesPrefix) {
			continue
		}
		// The pod-level hugepage limit is not defaulted when a pod-level
		// hugepage request is already set.
		if _, found := pod.Resources.Requests[name]; found {
			continue
		}
		if _, found := podLimits[name]; !found {
			podLimits[name] = quantity.DeepCopy()
		}
	}
	if len(podLimits) > 0 {
		pod.Resources.Limits = podLimits
	}
}

// ValidateResources validates that requested resources are less or equal
// to limits.
func ValidateResources(wi *Info) field.ErrorList {
	// requests should be less than limits.
	var allErrors field.ErrorList
	for i := range wi.Obj.Spec.PodSets {
		spec := wi.PodSpec(i)
		podSpecPath := PodSetsPath.Index(i).Child("template").Child("spec")
		for i := range spec.InitContainers {
			c := spec.InitContainers[i]
			if resNames := resources.NewRequestsFromResourceList(c.Resources.Requests).GreaterKeysRL(c.Resources.Limits); len(resNames) > 0 {
				allErrors = append(
					allErrors,
					field.Invalid(podSpecPath.Child("initContainers").Index(i), resNames, RequestsMustNotExceedLimitMessage),
				)
			}
		}

		for i := range spec.Containers {
			c := spec.Containers[i]
			if resNames := resources.NewRequestsFromResourceList(c.Resources.Requests).GreaterKeysRL(c.Resources.Limits); len(resNames) > 0 {
				allErrors = append(
					allErrors,
					field.Invalid(podSpecPath.Child("containers").Index(i), resNames, RequestsMustNotExceedLimitMessage),
				)
			}
		}

		// Pod-level resources (KEP-2837) are an optional pointer, only set when the
		// PodLevelResources feature is enabled and used.
		if podResources := spec.Resources; podResources != nil {
			if resNames := resources.NewRequestsFromResourceList(podResources.Requests).GreaterKeysRL(podResources.Limits); len(resNames) > 0 {
				allErrors = append(
					allErrors,
					field.Invalid(podSpecPath.Child("resources"), resNames, RequestsMustNotExceedLimitMessage),
				)
			}
		}
	}
	return allErrors
}

// ValidateLimitRange validates that the requested resources fit into the namespace defined
// limitRanges.
func ValidateLimitRange(ctx context.Context, c client.Client, wi *Info) field.ErrorList {
	var allErrs field.ErrorList
	limitRanges := corev1.LimitRangeList{}
	if err := c.List(ctx, &limitRanges, &client.ListOptions{Namespace: wi.Obj.Namespace}); err != nil {
		allErrs = append(allErrs, field.InternalError(field.NewPath(""), err))
		return allErrs
	}
	if len(limitRanges.Items) == 0 {
		return nil
	}
	summary := limitrange.Summarize(limitRanges.Items...)

	// verify
	for i := range wi.Obj.Spec.PodSets {
		spec := wi.PodSpec(i)
		allErrs = append(allErrs, summary.ValidatePodSpec(spec, PodSetsPath.Index(i).Child("template").Child("spec"))...)
	}
	return allErrs
}

// HasInternalError reports whether the field error list contains an internal error.
func HasInternalError(errs field.ErrorList) bool {
	for _, e := range errs {
		if e.Type == field.ErrorTypeInternal {
			return true
		}
	}
	return false
}

// ValidateAdmissibility checks if the workload's namespace matches the ClusterQueue's
// namespace selector, and if its resource requests are valid and satisfy LimitRanges.
// Returns the admissibility error if any.
func ValidateAdmissibility(
	ctx context.Context,
	c client.Client,
	wi *Info,
	cqNamespaceSelector labels.Selector,
) error {
	var ns corev1.Namespace
	if err := c.Get(ctx, types.NamespacedName{Name: wi.Obj.Namespace}, &ns); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("workload namespace %q does not exist: %w", wi.Obj.Namespace, err)
		}
		return fmt.Errorf("%w: %w", ErrInternal, err)
	}
	if cqNamespaceSelector != nil && !cqNamespaceSelector.Matches(labels.Set(ns.Labels)) {
		return ErrNamespaceMismatch
	}

	if errs := ValidateResources(wi); len(errs) > 0 {
		return fmt.Errorf("%s: %w", ErrInvalidWLResources, errs.ToAggregate())
	}

	if errs := ValidateLimitRange(ctx, c, wi); len(errs) > 0 {
		if HasInternalError(errs) {
			return fmt.Errorf("%w: %w", ErrInternal, errs.ToAggregate())
		}
		return fmt.Errorf("%s: %w", ErrLimitRangeConstraintsUnsatisfiedResources, errs.ToAggregate())
	}

	return nil
}

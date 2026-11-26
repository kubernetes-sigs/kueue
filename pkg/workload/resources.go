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
	"maps"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiresource "k8s.io/apimachinery/pkg/api/resource"
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

// EffectiveRequestsChanged reports whether the pods of the two PodSpecs have
// different effective requests. Missing pod-level requests are defaulted both
// before and after the adjustments in `in`, such as LimitRange defaults,
// because the order depends on a kube-apiserver feature gate.
func EffectiveRequestsChanged(newPodSpec, oldPodSpec *corev1.PodSpec, in AdjustmentInputs) bool {
	for _, beforeAdjustments := range []bool{false, true} {
		if !apiequality.Semantic.DeepEqual(
			effectiveRequests(newPodSpec, in, beforeAdjustments),
			effectiveRequests(oldPodSpec, in, beforeAdjustments),
		) {
			return true
		}
	}
	return false
}

// effectiveRequests returns the effective requests of the pods without the zero
// requests, which use no quota, so that adding one, for example `memory: 0`, is
// not a change. The missing pod-level requests are defaulted before the
// adjustments in `in` when beforeAdjustments is true, as kube-apiserver does
// when its PodLevelResourcesFixDefaulting feature gate is disabled, and after
// them otherwise.
func effectiveRequests(podSpec *corev1.PodSpec, in AdjustmentInputs, beforeAdjustments bool) corev1.ResourceList {
	spec := podSpec.DeepCopy()
	var podLimits corev1.ResourceList
	if spec.Resources != nil {
		podLimits, spec.Resources.Limits = spec.Resources.Limits, nil
	}
	if beforeAdjustments && len(podLimits) > 0 {
		UseLimitsAsMissingRequestsInPod(spec)
		spec.Resources.Requests = defaultPodLevelRequests(spec, podLimits)
	}
	effective := EffectivePodSpec(spec, in)
	if !beforeAdjustments && len(podLimits) > 0 {
		effective.Resources.Requests = defaultPodLevelRequests(&effective, podLimits)
	}
	requests := resources.PodRequests(&effective)
	maps.DeleteFunc(requests, func(_ corev1.ResourceName, q apiresource.Quantity) bool {
		return q.IsZero()
	})
	return requests
}

// defaultPodLevelRequests returns the pod-level requests that kube-apiserver
// defaults from the container requests and the pod-level limits.
func defaultPodLevelRequests(podSpec *corev1.PodSpec, podLimits corev1.ResourceList) corev1.ResourceList {
	requests := maps.Clone(podSpec.Resources.Requests)
	if requests == nil {
		requests = corev1.ResourceList{}
	}
	aggregate := resourcehelpers.AggregateContainerRequests(&corev1.Pod{Spec: *podSpec}, resourcehelpers.PodResourcesOptions{})
	for name, quantity := range aggregate {
		if _, found := requests[name]; !found && (name == corev1.ResourceCPU || name == corev1.ResourceMemory) {
			requests[name] = quantity
		}
	}
	for name, quantity := range podLimits {
		if _, found := requests[name]; !found && resourcehelpers.IsSupportedPodLevelResource(name) {
			requests[name] = quantity
		}
	}
	return requests
}

// UseLimitsAsMissingRequestsInPod adjust the resource requests to the limits value
// for resources that only set limits.
func UseLimitsAsMissingRequestsInPod(pod *corev1.PodSpec) {
	for ci := range pod.InitContainers {
		res := &pod.InitContainers[ci].Resources
		res.Requests = resource.MergeResourceListKeepFirst(res.Requests, res.Limits)
	}
	for ci := range pod.Containers {
		res := &pod.Containers[ci].Resources
		res.Requests = resource.MergeResourceListKeepFirst(res.Requests, res.Limits)
	}
	// Pod-level resources (KEP-2837) are an optional pointer, only set when the
	// PodLevelResources feature is enabled and used.
	if pod.Resources != nil {
		pod.Resources.Requests = resource.MergeResourceListKeepFirst(pod.Resources.Requests, pod.Resources.Limits)
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

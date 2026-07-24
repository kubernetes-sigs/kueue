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

	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
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

// AdjustResources adjusts the resource requests of a workload based on:
// - PodOverhead
// - LimitRanges
// - Limits
func AdjustResources(ctx context.Context, cl client.Client, wl *kueue.Workload) {
	log := ctrl.LoggerFrom(ctx)
	for _, err := range handlePodOverhead(ctx, cl, wl) {
		log.Error(err, "Failures adjusting requests for pod overhead")
	}
	if err := handlePodLimitRange(ctx, cl, wl); err != nil {
		log.Error(err, "Failed adjusting requests for LimitRanges")
	}
	handleLimitsToRequests(wl)
}

// ValidateResources validates that requested resources are less or equal
// to limits.
func ValidateResources(wi *Info) field.ErrorList {
	// requests should be less than limits.
	var allErrors field.ErrorList
	for i := range wi.Obj.Spec.PodSets {
		ps := &wi.Obj.Spec.PodSets[i]
		podSpecPath := PodSetsPath.Index(i).Child("template").Child("spec")
		for i := range ps.Template.Spec.InitContainers {
			c := ps.Template.Spec.InitContainers[i]
			if resNames := resources.NewRequestsFromResourceList(c.Resources.Requests).GreaterKeysRL(c.Resources.Limits); len(resNames) > 0 {
				allErrors = append(
					allErrors,
					field.Invalid(podSpecPath.Child("initContainers").Index(i), resNames, RequestsMustNotExceedLimitMessage),
				)
			}
		}

		for i := range ps.Template.Spec.Containers {
			c := ps.Template.Spec.Containers[i]
			if resNames := resources.NewRequestsFromResourceList(c.Resources.Requests).GreaterKeysRL(c.Resources.Limits); len(resNames) > 0 {
				allErrors = append(
					allErrors,
					field.Invalid(podSpecPath.Child("containers").Index(i), resNames, RequestsMustNotExceedLimitMessage),
				)
			}
		}

		// Pod-level resources (KEP-2837) are an optional pointer, only set when the
		// PodLevelResources feature is enabled and used.
		if podResources := ps.Template.Spec.Resources; podResources != nil {
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
		ps := &wi.Obj.Spec.PodSets[i]
		allErrs = append(allErrs, summary.ValidatePodSpec(&ps.Template.Spec, PodSetsPath.Index(i).Child("template").Child("spec"))...)
	}
	return allErrs
}

func hasInternalError(errs field.ErrorList) bool {
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
		if hasInternalError(errs) {
			return fmt.Errorf("%w: %w", ErrInternal, errs.ToAggregate())
		}
		return fmt.Errorf("%s: %w", ErrLimitRangeConstraintsUnsatisfiedResources, errs.ToAggregate())
	}

	return nil
}

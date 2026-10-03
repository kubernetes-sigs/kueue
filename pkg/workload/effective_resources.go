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
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/limitrange"
	utilmaps "sigs.k8s.io/kueue/pkg/util/maps"
	"sigs.k8s.io/kueue/pkg/util/resource"
	utiltolerations "sigs.k8s.io/kueue/pkg/util/tolerations"
)

// AdjustmentInputs carries the pre-resolved external inputs the effective
// resource view is computed from: the RuntimeClass overheads referenced by
// the workload's PodSets and the namespace LimitRange summary. Resolving is
// separated from applying so that the (client-free) computation can run
// wherever an Info is built.
type AdjustmentInputs struct {
	// PodOverheads maps a RuntimeClass name to its overhead.podFixed. Only
	// classes referenced by the workload and found in the cluster have an
	// entry.
	PodOverheads map[string]corev1.ResourceList
	// PodScheduling maps a RuntimeClass name to its scheduling constraints.
	// Only populated when the constraints still have to be resolved; see
	// resolveScheduling in ResolveAdjustmentInputs.
	PodScheduling map[string]*nodev1.Scheduling
	// LimitRangeSummary is the summarized namespace LimitRange, or nil when
	// the namespace has no container- or pod-type LimitRange items.
	LimitRangeSummary limitrange.Summary
}

// ResolveAdjustmentInputs reads the RuntimeClasses and LimitRanges the
// workload's effective resources depend on. The returned errors mirror the
// historical AdjustResources logging: one entry per PodSet whose RuntimeClass
// could not be read, plus at most one for the LimitRange listing.
func ResolveAdjustmentInputs(ctx context.Context, cl client.Client, wl *kueue.Workload) (AdjustmentInputs, []error) {
	var errs []error
	in := AdjustmentInputs{}
	if cl == nil {
		return in, nil
	}

	// A template copied from an existing Pod already carries what the admission
	// controller merged into it, so resolving the class again would only make it
	// diverge from that Pod.
	resolveScheduling := features.Enabled(features.RuntimeClassScheduling) && !OwnedByPods(wl)
	read := make(map[string]bool)
	for i := range wl.Spec.PodSets {
		ps := &wl.Spec.PodSets[i]
		podSpec := &ps.Template.Spec
		if podSpec.RuntimeClassName == nil {
			continue
		}
		name := *podSpec.RuntimeClassName
		if !read[name] {
			var runtimeClass nodev1.RuntimeClass
			if err := cl.Get(ctx, types.NamespacedName{Name: name}, &runtimeClass); err != nil {
				errs = append(errs, fmt.Errorf("in podSet %s: %w", ps.Name, err))
				continue
			}
			read[name] = true
			if runtimeClass.Overhead != nil {
				if in.PodOverheads == nil {
					in.PodOverheads = make(map[string]corev1.ResourceList)
				}
				in.PodOverheads[name] = runtimeClass.Overhead.PodFixed
			}
			if resolveScheduling && runtimeClass.Scheduling != nil {
				if in.PodScheduling == nil {
					in.PodScheduling = make(map[string]*nodev1.Scheduling)
				}
				in.PodScheduling[name] = runtimeClass.Scheduling
			}
		}
	}

	var limitRanges corev1.LimitRangeList
	if err := cl.List(ctx, &limitRanges, &client.ListOptions{Namespace: wl.Namespace}, client.MatchingFields{indexer.LimitRangeHasContainerOrPodType: "true"}); err != nil {
		errs = append(errs, err)
	} else if len(limitRanges.Items) > 0 {
		in.LimitRangeSummary = limitrange.Summarize(limitRanges.Items...)
	}

	return in, errs
}

// mergeRuntimeClassScheduling merges a RuntimeClass's scheduling constraints
// into podSpec the way the admission controller merges them into a Pod: the
// PodSet keeps its own values, tolerations are not duplicated, and a
// conflicting nodeSelector key is an error that leaves podSpec untouched.
func mergeRuntimeClassScheduling(podSpec *corev1.PodSpec, scheduling *nodev1.Scheduling) error {
	if err := utilmaps.HaveConflict(podSpec.NodeSelector, scheduling.NodeSelector); err != nil {
		return err
	}
	utilmaps.Copy(&podSpec.NodeSelector, scheduling.NodeSelector)
	podSpec.Tolerations = utiltolerations.Merge(podSpec.Tolerations, scheduling.Tolerations)
	return nil
}

// ValidateRuntimeClassScheduling reports PodSets whose nodeSelector conflicts
// with the one of their RuntimeClass. The admission controller rejects such a
// Pod, so no flavor can hold the Workload. A class that cannot be read is
// reported when the effective view is resolved, not here.
func ValidateRuntimeClassScheduling(ctx context.Context, c client.Client, wi *Info) field.ErrorList {
	if c == nil || !features.Enabled(features.RuntimeClassScheduling) || OwnedByPods(wi.Obj) {
		return nil
	}
	var allErrors field.ErrorList
	for i := range wi.Obj.Spec.PodSets {
		podSpec := &wi.Obj.Spec.PodSets[i].Template.Spec
		if podSpec.RuntimeClassName == nil {
			continue
		}
		var runtimeClass nodev1.RuntimeClass
		if err := c.Get(ctx, types.NamespacedName{Name: *podSpec.RuntimeClassName}, &runtimeClass); err != nil {
			continue
		}
		if runtimeClass.Scheduling == nil {
			continue
		}
		if err := mergeRuntimeClassScheduling(podSpec.DeepCopy(), runtimeClass.Scheduling); err != nil {
			allErrors = append(allErrors, field.Invalid(
				PodSetsPath.Index(i).Child("template").Child("spec").Child("nodeSelector"),
				podSpec.NodeSelector, err.Error()))
		}
	}
	return allErrors
}

// applyAdjustmentsToPodSpec rewrites the given PodSpec into its effective
// form: the RuntimeClass overhead and scheduling constraints, then limits
// copied into missing requests (mirroring API-server object defaulting), then
// the LimitRange defaults for whatever is still unset (mirroring the
// LimitRanger admission plugin).
func applyAdjustmentsToPodSpec(podSpec *corev1.PodSpec, in AdjustmentInputs) {
	if podSpec.RuntimeClassName != nil {
		if overhead, found := in.PodOverheads[*podSpec.RuntimeClassName]; found && len(podSpec.Overhead) == 0 {
			podSpec.Overhead = overhead.DeepCopy()
		}
		if sched, found := in.PodScheduling[*podSpec.RuntimeClassName]; found {
			// A conflict is reported by ValidateRuntimeClassScheduling.
			_ = mergeRuntimeClassScheduling(podSpec, sched)
		}
	}

	UseLimitsAsMissingRequestsInPod(podSpec)

	if in.LimitRangeSummary == nil {
		return
	}
	podLimits, foundPodLimits := in.LimitRangeSummary[corev1.LimitTypePod]
	containerLimits, foundContainerLimits := in.LimitRangeSummary[corev1.LimitTypeContainer]
	if foundContainerLimits {
		for ci := range podSpec.InitContainers {
			res := &podSpec.InitContainers[ci].Resources
			res.Limits = resource.MergeResourceListKeepFirst(res.Limits, containerLimits.Default)
			res.Requests = resource.MergeResourceListKeepFirst(res.Requests, containerLimits.DefaultRequest)
		}
		for ci := range podSpec.Containers {
			res := &podSpec.Containers[ci].Resources
			res.Limits = resource.MergeResourceListKeepFirst(res.Limits, containerLimits.Default)
			res.Requests = resource.MergeResourceListKeepFirst(res.Requests, containerLimits.DefaultRequest)
		}
	}
	// Pod-level resources (KEP-2837) are an optional pointer, only set when
	// the PodLevelResources feature is enabled and used.
	if podSpec.Resources != nil && foundPodLimits {
		podSpec.Resources.Limits = resource.MergeResourceListKeepFirst(podSpec.Resources.Limits, podLimits.Default)
		podSpec.Resources.Requests = resource.MergeResourceListKeepFirst(podSpec.Resources.Requests, podLimits.DefaultRequest)
	}
}

// EffectivePodSpecs returns the effective form of every PodSet template spec
// without touching the given workload.
func EffectivePodSpecs(wl *kueue.Workload, in AdjustmentInputs) []corev1.PodSpec {
	specs := make([]corev1.PodSpec, len(wl.Spec.PodSets))
	for i := range wl.Spec.PodSets {
		specs[i] = *wl.Spec.PodSets[i].Template.Spec.DeepCopy()
		applyAdjustmentsToPodSpec(&specs[i], in)
	}
	return specs
}

// WithAdjustmentInputs supplies the external defaults used to derive effective resources.
func WithAdjustmentInputs(in AdjustmentInputs) InfoOption {
	return func(o *InfoOptions) { o.adjustmentInputs = in }
}

// NewInfoFromClient resolves resource defaults before constructing an Info.
// The workload itself is retained without modification.
func NewInfoFromClient(ctx context.Context, cl client.Client, wl *kueue.Workload, opts ...InfoOption) *Info {
	info := &Info{}
	info.UpdateFromClient(ctx, cl, wl, opts...)
	return info
}

// UpdateFromClient refreshes external defaults and updates the effective resource
// view, total requests and scheduling hash together. It also refreshes defaults
// when the API workload's resource version has not changed.
func (i *Info) UpdateFromClient(ctx context.Context, cl client.Client, wl *kueue.Workload, opts ...InfoOption) {
	options := defaultOptions
	for _, opt := range opts {
		opt(&options)
	}
	log := ctrl.LoggerFrom(ctx)
	if options.effectivePodSpecs == nil {
		in, errs := ResolveAdjustmentInputs(ctx, cl, wl)
		for _, err := range errs {
			log.Error(err, "Could not resolve workload resource defaults", "workload", klog.KObj(wl))
		}
		opts = append([]InfoOption{WithAdjustmentInputs(in)}, opts...)
	}
	i.Update(log, wl, opts...)
}

// PodSpec returns the read-only effective PodSpec for a PodSet index.
func (i *Info) PodSpec(index int) *corev1.PodSpec {
	if index < len(i.EffectivePodSpecs) {
		return &i.EffectivePodSpecs[index]
	}
	return &i.Obj.Spec.PodSets[index].Template.Spec
}

// PodSpecByName returns the read-only effective PodSpec, or nil if the PodSet is absent.
func (i *Info) PodSpecByName(name kueue.PodSetReference) *corev1.PodSpec {
	for index := range i.Obj.Spec.PodSets {
		if i.Obj.Spec.PodSets[index].Name == name {
			return i.PodSpec(index)
		}
	}
	return nil
}

func effectivePodSpecs(wl *kueue.Workload, in AdjustmentInputs) []corev1.PodSpec {
	specs := EffectivePodSpecs(wl, in)
	for i := range specs {
		if !equality.Semantic.DeepEqual(specs[i], wl.Spec.PodSets[i].Template.Spec) {
			return specs
		}
	}
	return nil
}

// WithEffectivePodSpecs carries an existing read-only resource snapshot into a
// new Info. This keeps DRA preprocessing and scheduler assumptions consistent
// with the resources used to make the decision, even if defaults have changed.
// The snapshot must belong to the same Workload spec and PodSet ordering.
func WithEffectivePodSpecs(specs []corev1.PodSpec) InfoOption {
	return func(o *InfoOptions) { o.effectivePodSpecs = &specs }
}

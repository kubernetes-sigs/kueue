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
	"maps"
	"slices"

	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/podset"
	"sigs.k8s.io/kueue/pkg/util/limitrange"
	"sigs.k8s.io/kueue/pkg/util/resource"
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
	// Only classes referenced by the workload and found in the cluster have an
	// entry, and only while the workload's templates still describe Pods that
	// do not exist yet; see OwnedByPods.
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

	// A template copied from a Pod that already exists carries what the
	// RuntimeClass admission controller merged into it at creation, so
	// resolving the class again would only make it diverge from the Pod that
	// is already there.
	resolveScheduling := !OwnedByPods(wl)
	// Read each referenced class once; a class that cannot be read is retried
	// per PodSet so that every one of them reports the failure.
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
		// Report the conflict here, where the PodSet name is known; the merge
		// itself leaves a conflicting spec untouched.
		if sched, ok := in.PodScheduling[name]; ok {
			if err := mergeRuntimeClassScheduling(podSpec.DeepCopy(), sched); err != nil {
				errs = append(errs, fmt.Errorf("in podSet %s: %w", ps.Name, err))
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
// into podSpec the way the RuntimeClass admission controller merges them into
// a Pod: the PodSet keeps its own values, a toleration it already carries is
// not duplicated, and a nodeSelector key the class sets to a different value is
// a conflict, which leaves podSpec untouched.
func mergeRuntimeClassScheduling(podSpec *corev1.PodSpec, scheduling *nodev1.Scheduling) error {
	merged := podset.PodSetInfo{
		NodeSelector: maps.Clone(podSpec.NodeSelector),
		Tolerations:  slices.Clone(podSpec.Tolerations),
	}
	if err := merged.Merge(podset.PodSetInfo{
		NodeSelector: scheduling.NodeSelector,
		Tolerations:  scheduling.Tolerations,
	}); err != nil {
		return err
	}
	podSpec.NodeSelector = merged.NodeSelector
	podSpec.Tolerations = merged.Tolerations
	return nil
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
			// The error is reported by ResolveAdjustmentInputs; a conflicting
			// spec is left as the PodSet wrote it.
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

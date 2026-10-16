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
	versionutil "k8s.io/apimachinery/pkg/util/version"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
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
	// LimitRangeSummary is the summarized namespace LimitRange, or nil when
	// the namespace has no container- or pod-type LimitRange items.
	LimitRangeSummary limitrange.Summary
	// LegacyPodLevelDefaulting selects the pod-level defaulting of API servers
	// before 1.37: it runs before the container LimitRange defaults, and the
	// requests defaulting only when the pod has pod-level limits (the hugepage
	// limits defaulted from the container aggregates count as such). Starting
	// with 1.37, the pod-level defaulting runs after admission and reads the
	// container defaults.
	LegacyPodLevelDefaulting bool
}

// podLevelDefaultingAfterAdmissionMinor is the 1.x API server minor version that
// moved the pod-level request defaulting after admission
// (PodLevelResourcesFixDefaulting). The gate is enabled by default and its state
// is not observable by clients, so the server version decides which defaulting
// order to mirror.
const podLevelDefaultingAfterAdmissionMinor = 37

// ServerVersionFetcher provides the version of the API server the effective
// resources are computed for.
type ServerVersionFetcher interface {
	GetServerVersion() versionutil.Version
}

// UsesLegacyPodLevelDefaulting reports whether the given API server version
// defaults the pod-level resources before the container LimitRange defaults.
// An empty version (the fetcher has not reported one yet) is treated as 1.37+.
func UsesLegacyPodLevelDefaulting(serverVersion versionutil.Version) bool {
	components := serverVersion.Components()
	if len(components) < 2 {
		// The fetcher has not reported a version yet; assume the current behavior.
		return false
	}
	return components[0] < 1 || (components[0] == 1 && components[1] < podLevelDefaultingAfterAdmissionMinor)
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

	for i := range wl.Spec.PodSets {
		podSpec := &wl.Spec.PodSets[i].Template.Spec
		if podSpec.RuntimeClassName == nil || len(podSpec.Overhead) > 0 {
			continue
		}
		name := *podSpec.RuntimeClassName
		if _, found := in.PodOverheads[name]; found {
			continue
		}
		var runtimeClass nodev1.RuntimeClass
		if err := cl.Get(ctx, types.NamespacedName{Name: name}, &runtimeClass); err != nil {
			errs = append(errs, fmt.Errorf("in podSet %s: %w", wl.Spec.PodSets[i].Name, err))
			continue
		}
		if runtimeClass.Overhead != nil {
			if in.PodOverheads == nil {
				in.PodOverheads = make(map[string]corev1.ResourceList)
			}
			in.PodOverheads[name] = runtimeClass.Overhead.PodFixed
		}
	}

	summary, err := ResolveLimitRangeSummary(ctx, cl, wl.Namespace)
	if err != nil {
		errs = append(errs, err)
	} else {
		in.LimitRangeSummary = summary
	}

	return in, errs
}

// ResolveLimitRangeSummary reads and summarizes the container- and pod-type
// LimitRange items of a namespace, or returns nil when it has none. It relies
// on the LimitRange field index the Kueue controllers set up.
func ResolveLimitRangeSummary(ctx context.Context, cl client.Client, namespace string) (limitrange.Summary, error) {
	var limitRanges corev1.LimitRangeList
	if err := cl.List(ctx, &limitRanges, &client.ListOptions{Namespace: namespace}, client.MatchingFields{indexer.LimitRangeHasContainerOrPodType: "true"}); err != nil {
		return nil, err
	}
	if len(limitRanges.Items) == 0 {
		return nil, nil
	}
	return limitrange.Summarize(limitRanges.Items...), nil
}

// applyAdjustmentsToPodSpec rewrites the given PodSpec into its effective
// form: RuntimeClass overhead, limits copied into missing requests (mirroring
// API-server object defaulting), the container LimitRange defaults and the
// pod-level defaults (mirroring the API server and the LimitRanger admission
// plugin, in the order the cluster's server applies them), and finally the
// pod-level LimitRange defaults.
func applyAdjustmentsToPodSpec(podSpec *corev1.PodSpec, in AdjustmentInputs) {
	if podSpec.RuntimeClassName != nil && len(podSpec.Overhead) == 0 {
		if overhead, found := in.PodOverheads[*podSpec.RuntimeClassName]; found {
			podSpec.Overhead = overhead.DeepCopy()
		}
	}

	UseLimitsAsMissingRequestsInPod(podSpec)

	ApplyLimitRangeAndPodLevelDefaults(podSpec, in)

	// Pod-level resources (KEP-2837) are an optional pointer, only set when
	// the PodLevelResources feature is enabled and used.
	if in.LimitRangeSummary != nil {
		if podLimits, found := in.LimitRangeSummary[corev1.LimitTypePod]; found && podSpec.Resources != nil {
			podSpec.Resources.Limits = resource.MergeResourceListKeepFirst(podSpec.Resources.Limits, podLimits.Default)
			podSpec.Resources.Requests = resource.MergeResourceListKeepFirst(podSpec.Resources.Requests, podLimits.DefaultRequest)
		}
	}
}

// ApplyLimitRangeAndPodLevelDefaults applies the container LimitRange defaults and
// the pod-level defaulting to the pod spec, mirroring the order the API server
// applies them in: 1.37 and newer default the pod-level resources after
// admission, so the aggregates they derive from include the container defaults;
// the older servers default the pod-level resources first, and only run the
// pod-level request defaulting when the pod has pod-level limits (the hugepage
// limits defaulted from the container aggregates count as such).
func ApplyLimitRangeAndPodLevelDefaults(podSpec *corev1.PodSpec, in AdjustmentInputs) {
	if in.LegacyPodLevelDefaulting {
		if podSpec.Resources != nil {
			DefaultHugePagePodLevelLimits(podSpec)
			if len(podSpec.Resources.Limits) > 0 {
				DefaultPodLevelRequests(podSpec)
			}
		}
		applyContainerLimitRangeDefaults(podSpec, in.LimitRangeSummary)
		return
	}
	// The API server defers the pod-level defaulting until after admission, so
	// the container defaults below are part of the aggregates it reads.
	applyContainerLimitRangeDefaults(podSpec, in.LimitRangeSummary)
	DefaultHugePagePodLevelLimits(podSpec)
	DefaultPodLevelRequests(podSpec)
}

// applyContainerLimitRangeDefaults applies the container-type LimitRange defaults
// the LimitRanger admission plugin would set on the pod's (init) containers.
func applyContainerLimitRangeDefaults(podSpec *corev1.PodSpec, summary limitrange.Summary) {
	if summary == nil {
		return
	}
	containerLimits, found := summary[corev1.LimitTypeContainer]
	if !found {
		return
	}
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

// WithServerVersionFetcher makes the effective resources follow the pod-level
// defaulting of the API server the workload is reconciled against. Without a
// fetcher the current server behavior is assumed.
func WithServerVersionFetcher(f ServerVersionFetcher) InfoOption {
	return func(o *InfoOptions) { o.serverVersionFetcher = f }
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
		if options.serverVersionFetcher != nil {
			in.LegacyPodLevelDefaulting = UsesLegacyPodLevelDefaulting(options.serverVersionFetcher.GetServerVersion())
		}
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

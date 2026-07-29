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

// Package wasapi reads Kubernetes' Workload-Aware Scheduling (WAS) standalone
// PodGroup and Workload objects (group scheduling.k8s.io) without depending on
// any generated Go types for them.
//
// WAS's PodGroup/Workload API is still alpha and has already changed its
// package name once upstream (scheduling.k8s.io/v1alpha2 renamed to
// scheduling.k8s.io/v1alpha3 for the Kubernetes 1.37 cycle, with the eventual
// v1beta1 landing only on unreleased builds as of this writing). The JSON
// shape of the fields this package reads (spec.controllerRef,
// spec.podGroupTemplates[].schedulingPolicy.gang.minCount,
// spec.schedulingPolicy.gang.minCount) has stayed the same across those
// renames. Rather than vendoring a specific, likely-to-be-renamed-again
// package, this package resolves whichever scheduling.k8s.io PodGroup/Workload
// API version the cluster actually serves (via the RESTMapper) and reads it as
// unstructured data.
package wasapi

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// GroupName is the API group used by WAS's standalone PodGroup and Workload objects.
	GroupName = "scheduling.k8s.io"

	// PodGroupKind is the Kind of a standalone PodGroup object.
	PodGroupKind = "PodGroup"

	// WorkloadKind is the Kind of a standard Workload object.
	WorkloadKind = "Workload"
)

// PodGroupGroupKind is the GroupKind of a standalone PodGroup object.
var PodGroupGroupKind = schema.GroupKind{Group: GroupName, Kind: PodGroupKind}

// WorkloadGroupKind is the GroupKind of a standard Workload object.
var WorkloadGroupKind = schema.GroupKind{Group: GroupName, Kind: WorkloadKind}

// ResolveGVK resolves the GroupVersionKind the cluster currently serves for the
// given WAS GroupKind (PodGroupGroupKind or WorkloadGroupKind), using the
// RESTMapper's own version preference (which follows the API server's
// preferred version for the group). ok is false, with a nil error, when the
// API isn't installed on the cluster at all: callers should treat that the
// same as "not found" rather than as an error, since WAS APIs are optional and
// alpha.
func ResolveGVK(mapper apimeta.RESTMapper, gk schema.GroupKind) (gvk schema.GroupVersionKind, ok bool, err error) {
	mapping, err := mapper.RESTMapping(gk)
	if err != nil {
		if apimeta.IsNoMatchError(err) {
			return schema.GroupVersionKind{}, false, nil
		}
		return schema.GroupVersionKind{}, false, err
	}
	return mapping.GroupVersionKind, true, nil
}

// PodGroupGangMinCount returns the gang scheduling minCount configured on the
// standalone PodGroup named "name" in "namespace", reading whichever
// scheduling.k8s.io PodGroup API version the cluster serves.
//
// found is false, with a nil error, when: the PodGroup API isn't installed on
// the cluster, the named PodGroup doesn't exist (yet), or it exists but
// doesn't use a gang scheduling policy. Callers that want to treat a
// not-yet-created PodGroup as "the group hasn't reached its expected size
// yet" rather than an error should check found rather than err.
func PodGroupGangMinCount(ctx context.Context, c client.Client, namespace, name string) (minCount int32, found bool, err error) {
	gvk, ok, err := ResolveGVK(c.RESTMapper(), PodGroupGroupKind)
	if err != nil || !ok {
		return 0, false, err
	}

	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, obj); err != nil {
		if apierrors.IsNotFound(err) {
			return 0, false, nil
		}
		return 0, false, err
	}

	return gangMinCount(obj.Object, "spec", "schedulingPolicy")
}

// PodGroupTemplateGangMinCounts returns, for each PodGroupTemplate found in a
// Workload object in "namespace" whose spec.controllerRef matches
// ownerAPIGroup/ownerKind/ownerName, a map from the template's name to its
// gang scheduling minCount. ownerAPIGroup follows the same convention as
// TypedLocalObjectReference.apiGroup: empty for the core API group.
//
// The returned map is nil, with a nil error, when: the Workload API isn't
// installed on the cluster, or no Workload in the namespace has a matching
// controllerRef. It never returns an error for those cases, since a
// standard Workload object is optional input for a Kueue-managed job.
func PodGroupTemplateGangMinCounts(ctx context.Context, c client.Client, namespace, ownerAPIGroup, ownerKind, ownerName string) (map[string]int32, error) {
	gvk, ok, err := ResolveGVK(c.RESTMapper(), WorkloadGroupKind)
	if err != nil || !ok {
		return nil, err
	}

	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
	if err := c.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return nil, err
	}

	for i := range list.Items {
		wl := &list.Items[i]
		apiGroup, _, _ := unstructured.NestedString(wl.Object, "spec", "controllerRef", "apiGroup")
		kind, _, _ := unstructured.NestedString(wl.Object, "spec", "controllerRef", "kind")
		name, _, _ := unstructured.NestedString(wl.Object, "spec", "controllerRef", "name")
		if apiGroup != ownerAPIGroup || kind != ownerKind || name != ownerName {
			continue
		}
		return podGroupTemplateGangMinCounts(wl.Object)
	}
	return nil, nil
}

// podGroupTemplateGangMinCounts extracts the gang minCount of every
// PodGroupTemplate with a gang scheduling policy from a Workload object,
// indexed by template name. Templates without a gang policy (e.g. using the
// basic policy) are omitted.
func podGroupTemplateGangMinCounts(workload map[string]any) (map[string]int32, error) {
	templates, _, err := unstructured.NestedSlice(workload, "spec", "podGroupTemplates")
	if err != nil {
		return nil, err
	}

	counts := make(map[string]int32, len(templates))
	for _, t := range templates {
		template, ok := t.(map[string]any)
		if !ok {
			continue
		}
		name, _, err := unstructured.NestedString(template, "name")
		if err != nil {
			return nil, err
		}
		minCount, found, err := gangMinCount(template, "schedulingPolicy")
		if err != nil {
			return nil, err
		}
		if found {
			counts[name] = minCount
		}
	}
	return counts, nil
}

// gangMinCount reads a GangSchedulingPolicy's minCount, nested under the given
// path plus "gang"/"minCount" (e.g. "spec"/"schedulingPolicy" for a PodGroup,
// or just "schedulingPolicy" for a Workload's PodGroupTemplate). found is
// false when the object has no gang policy set (e.g. it uses the basic
// policy instead).
func gangMinCount(obj map[string]any, schedulingPolicyPath ...string) (minCount int32, found bool, err error) {
	path := append(append([]string{}, schedulingPolicyPath...), "gang", "minCount")
	value, found, err := unstructured.NestedInt64(obj, path...)
	if err != nil || !found {
		return 0, false, err
	}
	return int32(value), true, nil
}

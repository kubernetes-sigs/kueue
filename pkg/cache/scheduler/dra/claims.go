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

package dra

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuedra "sigs.k8s.io/kueue/pkg/dra"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	utilresource "sigs.k8s.io/kueue/pkg/util/resource"
)

// resourceClaimsForPod is every ResourceClaim the Pod will hold on a node: the ones its
// PodSet names, and the one kube-scheduler creates for DRA-backed extended resources.
type resourceClaimsForPod struct {
	// all includes the claim synthesized for the Pod's DRA-backed extended resources.
	all []*resourceapi.ResourceClaim
	// withoutExtendedResources drops that synthesized claim, for nodes that publish the
	// resources it covers and so supply them from their own allocatable.
	withoutExtendedResources []*resourceapi.ResourceClaim
	// extendedResources are the names the synthesized claim covers.
	extendedResources []corev1.ResourceName
}

func (r resourceClaimsForPod) isEmpty() bool {
	return len(r.all) == 0
}

// forNode is what to allocate on node. A DRA-backed extended resource the node advertises
// is served from its allocatable rather than from a device, so the synthesized claim is
// dropped there, which is how kube-scheduler's noderesources plugin decides per node.
func (r resourceClaimsForPod) forNode(node *corev1.Node) []*resourceapi.ResourceClaim {
	if len(r.extendedResources) == 0 {
		return r.all
	}
	for _, name := range r.extendedResources {
		if quantity, ok := node.Status.Allocatable[name]; !ok || quantity.IsZero() {
			return r.all
		}
	}
	return r.withoutExtendedResources
}

func (c *Checker) newResourceClaimsForPod(ctx context.Context, podTemplate *corev1.PodTemplateSpec) (resourceClaimsForPod, error) {
	claims, err := newResourceClaimsForPodResourceClaims(ctx, c.cl, podTemplate.Namespace, podTemplate.Spec.ResourceClaims)
	if err != nil {
		return resourceClaimsForPod{}, fmt.Errorf("building synthetic DRA claims: %w", err)
	}
	extended, extendedNames, err := newResourceClaimsForExtendedResources(ctx, c.cl, podTemplate)
	if err != nil {
		return resourceClaimsForPod{}, fmt.Errorf("building extended resource DRA claims: %w", err)
	}
	return resourceClaimsForPod{
		all:                      append(slices.Clone(claims), extended...),
		withoutExtendedResources: claims,
		extendedResources:        extendedNames,
	}, nil
}

func newResourceClaimsForPodResourceClaims(ctx context.Context, cl client.Client, namespace string, resourceClaims []corev1.PodResourceClaim) ([]*resourceapi.ResourceClaim, error) {
	var claims []*resourceapi.ResourceClaim
	for _, prc := range resourceClaims {
		spec, err := resolveClaimSpec(ctx, cl, namespace, prc)
		if err != nil {
			return nil, fmt.Errorf("resolving claim %q: %w", prc.Name, err)
		}
		if spec == nil {
			continue
		}
		claims = append(claims, &resourceapi.ResourceClaim{
			Name:      fmt.Sprintf("kueue-sim-%s", prc.Name),
			Namespace: namespace,
			Spec:      *spec,
		})
	}
	return claims, nil
}

// newResourceClaimsForExtendedResources builds the claim kube-scheduler creates for a Pod's
// DRA-backed extended resources, which does not exist yet when Kueue admits, along with
// the resources it covers. One request per DeviceClass carries the Pod's total, since
// without selectors only the total decides whether a node fits.
func newResourceClaimsForExtendedResources(ctx context.Context, cl client.Client, podTemplate *corev1.PodTemplateSpec) ([]*resourceapi.ResourceClaim, []corev1.ResourceName, error) {
	if !features.Enabled(features.KueueDRAIntegrationExtendedResource) {
		return nil, nil, nil
	}
	if !requestsExtendedResource(&podTemplate.Spec) {
		return nil, nil, nil
	}
	totals := extendedResourceTotals(&podTemplate.Spec)

	var requests []resourceapi.DeviceRequest
	var names []corev1.ResourceName
	// Sorted so the synthesized claim does not vary between calls.
	for _, resourceName := range slices.Sorted(maps.Keys(totals)) {
		deviceClass, err := kueuedra.ResolveDeviceClass(ctx, cl, resourceName)
		if err != nil {
			return nil, nil, err
		}
		if deviceClass == nil {
			// A device plugin advertises it, so the node filters already cover it.
			continue
		}
		names = append(names, resourceName)
		requests = append(requests, resourceapi.DeviceRequest{
			Name: fmt.Sprintf("request-%d", len(requests)),
			Exactly: &resourceapi.ExactDeviceRequest{
				DeviceClassName: deviceClass.Name,
				AllocationMode:  resourceapi.DeviceAllocationModeExactCount,
				Count:           totals[resourceName],
			},
		})
	}
	if len(requests) == 0 {
		return nil, nil, nil
	}

	return []*resourceapi.ResourceClaim{{
		Name:      "kueue-sim-extended-resources",
		Namespace: podTemplate.Namespace,
		Spec: resourceapi.ResourceClaimSpec{
			Devices: resourceapi.DeviceClaim{Requests: requests},
		},
	}}, names, nil
}

// resolveClaimSpec returns the spec to allocate for a PodResourceClaim, or nil when the
// PodResourceClaim names neither a claim nor a template, which the API allows.
//
// A direct ResourceClaim reference is rejected rather than resolved: the workload
// controller marks such Workloads inadmissible before they reach scheduling, which
// KueueDRAIntegration guarantees by being a dependency of this check. Failing here
// rather than skipping the claim keeps a broken guarantee loud instead of silently
// reporting every node as feasible.
func resolveClaimSpec(ctx context.Context, cl client.Client, namespace string, prc corev1.PodResourceClaim) (*resourceapi.ResourceClaimSpec, error) {
	switch {
	case prc.ResourceClaimTemplateName != nil:
		var tmpl resourceapi.ResourceClaimTemplate
		if err := cl.Get(ctx, client.ObjectKey{Namespace: namespace, Name: *prc.ResourceClaimTemplateName}, &tmpl); err != nil {
			return nil, err
		}
		return &tmpl.Spec.Spec, nil
	case prc.ResourceClaimName != nil:
		return nil, errors.New("a direct ResourceClaim reference is not supported")
	default:
		return nil, nil
	}
}

// requestsExtendedResource reports whether the Pod asks for any extended resource. Most
// Workloads ask for none, and totalling them costs more than looking.
func requestsExtendedResource(spec *corev1.PodSpec) bool {
	for i := range spec.InitContainers {
		if containerRequestsExtendedResource(&spec.InitContainers[i]) {
			return true
		}
	}
	for i := range spec.Containers {
		if containerRequestsExtendedResource(&spec.Containers[i]) {
			return true
		}
	}
	return false
}

func containerRequestsExtendedResource(container *corev1.Container) bool {
	for name := range container.Resources.Requests {
		if utilresource.IsExtendedResourceName(name) {
			return true
		}
	}
	return false
}

// extendedResourceTotals is how many devices of each extended resource the Pod holds at
// once. It errs high: kube-scheduler lets an init container reuse a later container's
// devices, which Pod-level requests do not model.
func extendedResourceTotals(spec *corev1.PodSpec) map[corev1.ResourceName]int64 {
	totals := make(map[corev1.ResourceName]int64)
	for name, count := range resources.ToMap(resources.NewRequestsFromPodSpec(spec)) {
		if utilresource.IsExtendedResourceName(name) && count > 0 {
			totals[name] = count
		}
	}
	return totals
}

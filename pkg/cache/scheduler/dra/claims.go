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
	"encoding/json"
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

	// allKey and withoutExtendedResourcesKey name the two claim sets, set on first use.
	allKey                      string
	withoutExtendedResourcesKey string
}

func (r resourceClaimsForPod) isEmpty() bool {
	return len(r.all) == 0
}

// forNode is what to allocate on node. A DRA-backed extended resource the node advertises
// is served from its allocatable rather than from a device, so the synthesized claim is
// dropped there, which is how kube-scheduler's noderesources plugin decides per node.
func (r resourceClaimsForPod) forNode(node *corev1.Node) []*resourceapi.ResourceClaim {
	if r.needsAll(node) {
		return r.all
	}
	return r.withoutExtendedResources
}

// needsAll reports whether node needs the synthesized claim, because it does not
// publish every extended resource that the claim covers.
func (r resourceClaimsForPod) needsAll(node *corev1.Node) bool {
	if len(r.extendedResources) == 0 {
		return true
	}
	for _, name := range r.extendedResources {
		if quantity, ok := node.Status.Allocatable[name]; !ok || quantity.IsZero() {
			return true
		}
	}
	return false
}

// keyForNode names the claims node needs, for caching counts. The two keys are
// computed on first use.
func (r *resourceClaimsForPod) keyForNode(node *corev1.Node) (string, error) {
	if r.needsAll(node) {
		return cachedClaimsKey(&r.allKey, r.all)
	}
	return cachedClaimsKey(&r.withoutExtendedResourcesKey, r.withoutExtendedResources)
}

func cachedClaimsKey(key *string, claims []*resourceapi.ResourceClaim) (string, error) {
	if *key != "" {
		return *key, nil
	}
	specs := make([]resourceapi.ResourceClaimSpec, len(claims))
	for i, claim := range claims {
		specs[i] = claim.Spec
	}
	encoded, err := json.Marshal(specs)
	if err != nil {
		return "", err
	}
	*key = string(encoded)
	return *key, nil
}

func (c *Checker) newResourceClaimsForPod(ctx context.Context, podTemplate *corev1.PodTemplateSpec) (resourceClaimsForPod, error) {
	claims, err := newResourceClaimsForPodResourceClaims(ctx, c.cl, podTemplate.Namespace, podTemplate.Spec.ResourceClaims)
	if err != nil {
		return resourceClaimsForPod{}, fmt.Errorf("building synthetic DRA claims: %w", err)
	}
	extended, extendedNames, err := newResourceClaimsForExtendedResources(ctx, c.cl, c.draBacked, podTemplate)
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
func newResourceClaimsForExtendedResources(
	ctx context.Context,
	cl client.Client,
	draBacked func(corev1.ResourceName) bool,
	podTemplate *corev1.PodTemplateSpec,
) ([]*resourceapi.ResourceClaim, []corev1.ResourceName, error) {
	if !features.Enabled(features.KueueDRAIntegrationExtendedResource) {
		return nil, nil, nil
	}
	if !requestsExtendedResource(&podTemplate.Spec, draBacked) {
		return nil, nil, nil
	}
	totals := extendedResourceTotals(&podTemplate.Spec)

	var requests []resourceapi.DeviceRequest
	var names []corev1.ResourceName
	// Sorted so the synthesized claim does not vary between calls.
	for _, resourceName := range slices.Sorted(maps.Keys(totals)) {
		if draBacked != nil && !draBacked(resourceName) {
			continue
		}
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

// requestsExtendedResource reports whether the Pod asks for any extended resource that
// draBacked accepts, or for any at all when it is nil. Most Workloads ask for none, and
// totalling them costs more than looking.
func requestsExtendedResource(spec *corev1.PodSpec, draBacked func(corev1.ResourceName) bool) bool {
	for i := range spec.InitContainers {
		if containerRequestsExtendedResource(&spec.InitContainers[i], draBacked) {
			return true
		}
	}
	for i := range spec.Containers {
		if containerRequestsExtendedResource(&spec.Containers[i], draBacked) {
			return true
		}
	}
	return false
}

func containerRequestsExtendedResource(container *corev1.Container, draBacked func(corev1.ResourceName) bool) bool {
	for name, quantity := range container.Resources.Requests {
		if !quantity.IsZero() && utilresource.IsExtendedResourceName(name) && (draBacked == nil || draBacked(name)) {
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
		if !utilresource.IsExtendedResourceName(name) || count.Sign() <= 0 {
			continue
		}
		// NewRequestsFromPodSpec builds this through AmountFromQuantity, which
		// caps a non-CPU value at MaxInt64 on the way in, so the count fits
		// DeviceRequest.Count.
		n, _ := count.Int64()
		totals[name] = n
	}
	return totals
}

// UsesDevices reports whether a Pod with this spec may take devices: through its
// ResourceClaims, or through extended resources that a DeviceClass may back.
func (c *Checker) UsesDevices(spec *corev1.PodSpec) bool {
	return len(spec.ResourceClaims) > 0 ||
		(features.Enabled(features.KueueDRAIntegrationExtendedResource) && requestsExtendedResource(spec, c.draBacked))
}

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
	"strings"

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
	// extendedResourcesClaim is that synthesized claim, nil when the Pod has no DRA-backed
	// extended resources.
	extendedResourcesClaim *resourceapi.ResourceClaim
	// extendedResources are the names extendedResourcesClaim covers, one per request and
	// in the order of its requests.
	extendedResources []corev1.ResourceName

	// allKey and withoutExtendedResourcesKey name the two claim sets, set on first use.
	allKey                      string
	withoutExtendedResourcesKey string
	// partialKeys name the claim sets of nodes that publish only some of
	// extendedResources, by the resources those nodes take from devices.
	partialKeys map[string]string
}

func (r resourceClaimsForPod) isEmpty() bool {
	return len(r.all) == 0
}

// forNode is what to allocate on node. A DRA-backed extended resource the node advertises
// is served from its allocatable rather than from a device, so its request is dropped from
// the synthesized claim there. Like kube-scheduler's dynamicresources plugin, this is
// decided per resource: a node can publish one extended resource through a device plugin
// and supply another through DRA.
func (r resourceClaimsForPod) forNode(node *corev1.Node) []*resourceapi.ResourceClaim {
	return r.withFromDevices(r.fromDevices(node))
}

// fromDevices returns the indexes in extendedResources of the resources node does not
// publish, so supplies from devices.
func (r resourceClaimsForPod) fromDevices(node *corev1.Node) []int {
	var indexes []int
	for i, name := range r.extendedResources {
		if quantity, ok := node.Status.Allocatable[name]; !ok || quantity.IsZero() {
			indexes = append(indexes, i)
		}
	}
	return indexes
}

// withFromDevices returns the claims with the synthesized claim cut down to the requests
// at the fromDevices indexes.
func (r resourceClaimsForPod) withFromDevices(fromDevices []int) []*resourceapi.ResourceClaim {
	switch len(fromDevices) {
	case len(r.extendedResources):
		return r.all
	case 0:
		return r.withoutExtendedResources
	}
	requests := make([]resourceapi.DeviceRequest, 0, len(fromDevices))
	for _, i := range fromDevices {
		requests = append(requests, r.extendedResourcesClaim.Spec.Devices.Requests[i])
	}
	partial := &resourceapi.ResourceClaim{
		ObjectMeta: r.extendedResourcesClaim.ObjectMeta,
		Spec: resourceapi.ResourceClaimSpec{
			Devices: resourceapi.DeviceClaim{Requests: requests},
		},
	}
	return append(slices.Clone(r.withoutExtendedResources), partial)
}

// keyForNode names the claims node needs, for caching counts. Each key is computed on
// first use.
func (r *resourceClaimsForPod) keyForNode(node *corev1.Node) (string, error) {
	fromDevices := r.fromDevices(node)
	switch len(fromDevices) {
	case len(r.extendedResources):
		return cachedClaimsKey(&r.allKey, r.all)
	case 0:
		return cachedClaimsKey(&r.withoutExtendedResourcesKey, r.withoutExtendedResources)
	}
	names := make([]string, len(fromDevices))
	for i, index := range fromDevices {
		names[i] = string(r.extendedResources[index])
	}
	subset := strings.Join(names, ",")
	if key, ok := r.partialKeys[subset]; ok {
		return key, nil
	}
	key, err := cachedClaimsKey(new(string), r.withFromDevices(fromDevices))
	if err != nil {
		return "", err
	}
	if r.partialKeys == nil {
		r.partialKeys = make(map[string]string)
	}
	r.partialKeys[subset] = key
	return key, nil
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
	all := claims
	if extended != nil {
		all = append(slices.Clone(claims), extended)
	}
	return resourceClaimsForPod{
		all:                      all,
		withoutExtendedResources: claims,
		extendedResourcesClaim:   extended,
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
) (*resourceapi.ResourceClaim, []corev1.ResourceName, error) {
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

	return &resourceapi.ResourceClaim{
		Name:      "kueue-sim-extended-resources",
		Namespace: podTemplate.Namespace,
		Spec: resourceapi.ResourceClaimSpec{
			Devices: resourceapi.DeviceClaim{Requests: requests},
		},
	}, names, nil
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

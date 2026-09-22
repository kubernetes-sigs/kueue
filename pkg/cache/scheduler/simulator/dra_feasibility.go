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

package simulator

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"maps"
	"slices"
	"sync"

	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	dracel "k8s.io/dynamic-resource-allocation/cel"
	"k8s.io/dynamic-resource-allocation/structured"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/dynamicresources"
	schedulerfeature "k8s.io/kubernetes/pkg/scheduler/framework/plugins/feature"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"sigs.k8s.io/kueue/pkg/dra"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	utilresource "sigs.k8s.io/kueue/pkg/util/resource"
)

// DRAChecker drops candidate nodes that cannot supply a Pod's ResourceClaims. It
// allocates with the engine kube-scheduler uses, so both reach the same answer.
type DRAChecker struct {
	inner     SimulatorSnapshot
	cl        client.Client
	celCache  *CELCache
	allocator lazyAllocator
}

// NewDRAChecker wraps inner with the device check. Pass the scheduler cache's CELCache
// rather than a fresh one: it only pays off by outliving the snapshot.
func NewDRAChecker(inner SimulatorSnapshot, cl client.Client, celCache *CELCache) *DRAChecker {
	c := &DRAChecker{
		inner:    inner,
		cl:       cl,
		celCache: celCache,
	}
	c.allocator.build = c.buildAllocator
	return c
}

// CELCache holds the compiled device selectors, keyed by the expression, so one survives
// the DRAChecker that compiled it and is compiled once for the process rather than once
// per scheduling cycle. The zero value is ready to use and safe to share.
type CELCache struct {
	once  sync.Once
	value *dracel.Cache
}

func (c *CELCache) get() *dracel.Cache {
	c.once.Do(func() {
		// Read on first use, not at construction, so the gates are parsed by now.
		fts := schedulerfeature.NewSchedulerFeaturesFromGates(utilfeature.DefaultFeatureGate)
		// The same size kube-scheduler's dynamicresources plugin uses.
		c.value = dracel.NewCache(10, dracel.Features{
			EnableConsumableCapacity: fts.EnableDRAConsumableCapacity,
			EnableListTypeAttributes: fts.EnableDRAListTypeAttributes,
		})
	})
	return c.value
}

// lazyAllocator builds the snapshot's allocator on first use. A Pod's claims go to
// Allocate rather than the constructor, so one allocator serves the whole snapshot
// and the device state cannot shift mid-cycle.
type lazyAllocator struct {
	build func(context.Context) (structured.Allocator, error)
	once  sync.Once
	value structured.Allocator
	err   error
}

func (l *lazyAllocator) get(ctx context.Context) (structured.Allocator, error) {
	l.once.Do(func() {
		l.value, l.err = l.build(ctx)
	})
	return l.value, l.err
}

// Simulate needs no DRA handling: the device filtering holds no state.
func (c *DRAChecker) Simulate(ctx context.Context, fn func()) error {
	return c.inner.Simulate(ctx, fn)
}

// PreemptWorkload frees the Workload's Pods but not the devices its claims hold: the
// allocator is built once per snapshot from the ResourceClaims as they stand. So
// preemption cannot make a Workload device-feasible, and the check stays restrictive
// rather than over-admitting. Releasing them is Beta work in keps/2941-DRA.
func (c *DRAChecker) PreemptWorkload(ctx context.Context, wlKey client.ObjectKey) (func() error, error) {
	return c.inner.PreemptWorkload(ctx, wlKey)
}

func (c *DRAChecker) FindFeasibleNodes(
	ctx context.Context,
	candidates iter.Seq[Candidate],
	requirements *PodRequirements,
	stats *NodeExclusionStats,
) ([]MatchedCandidate, error) {
	feasible, err := c.inner.FindFeasibleNodes(ctx, candidates, requirements, stats)
	if err != nil {
		return nil, err
	}

	if requirements.PodTemplate == nil {
		return feasible, nil
	}

	// The claims belong to the Workload rather than the cluster, so unlike the
	// allocator they are resolved on every call.
	claims, err := c.newResourceClaimsForPod(ctx, requirements.PodTemplate)
	if err != nil {
		return nil, err
	}
	if claims.isEmpty() {
		return feasible, nil
	}

	allocator, err := c.allocator.get(ctx)
	if err != nil {
		return nil, err
	}

	return c.filterByDevices(ctx, feasible, allocator, claims, stats)
}

// resourceClaimsForPod is every ResourceClaim the Pod will hold on a node: the ones its
// PodSet names, and the one kube-scheduler creates for DRA-backed extended resources.
type resourceClaimsForPod struct {
	all []*resourceapi.ResourceClaim
	// exceptExtended drops the extended resource claim, and extendedNames is what that
	// claim covers. A node publishing all of them supplies them itself.
	exceptExtended []*resourceapi.ResourceClaim
	extendedNames  []corev1.ResourceName
}

func (r resourceClaimsForPod) isEmpty() bool {
	return len(r.all) == 0
}

// forNode is what to allocate on node. A DRA-backed extended resource the node advertises
// is served from its allocatable rather than from a device, so the synthesized claim is
// dropped there, which is how kube-scheduler's noderesources plugin decides per node.
func (r resourceClaimsForPod) forNode(node *corev1.Node) []*resourceapi.ResourceClaim {
	if len(r.extendedNames) == 0 {
		return r.all
	}
	for _, name := range r.extendedNames {
		if quantity, ok := node.Status.Allocatable[name]; !ok || quantity.IsZero() {
			return r.all
		}
	}
	return r.exceptExtended
}

func (c *DRAChecker) newResourceClaimsForPod(ctx context.Context, podTemplate *corev1.PodTemplateSpec) (resourceClaimsForPod, error) {
	claims, err := newResourceClaimsForPodResourceClaims(ctx, c.cl, podTemplate.Namespace, podTemplate.Spec.ResourceClaims)
	if err != nil {
		return resourceClaimsForPod{}, fmt.Errorf("building synthetic DRA claims: %w", err)
	}
	extended, extendedNames, err := newResourceClaimsForExtendedResources(ctx, c.cl, podTemplate)
	if err != nil {
		return resourceClaimsForPod{}, fmt.Errorf("building extended resource DRA claims: %w", err)
	}
	return resourceClaimsForPod{
		all:            append(slices.Clone(claims), extended...),
		exceptExtended: claims,
		extendedNames:  extendedNames,
	}, nil
}

func (c *DRAChecker) buildAllocator(ctx context.Context) (structured.Allocator, error) {
	var sliceList resourceapi.ResourceSliceList
	if err := c.cl.List(ctx, &sliceList); err != nil {
		return nil, fmt.Errorf("listing ResourceSlices: %w", err)
	}
	deviceSlices := make([]*resourceapi.ResourceSlice, len(sliceList.Items))
	for i := range sliceList.Items {
		deviceSlices[i] = &sliceList.Items[i]
	}

	// Configured from the Kubernetes DRA gates rather than the Kueue ones, by the same
	// call kube-scheduler makes, so the two allocators stay in step.
	draFeatures := dynamicresources.AllocatorFeatures(schedulerfeature.NewSchedulerFeaturesFromGates(utilfeature.DefaultFeatureGate))

	// The allocated state is read with the allocator's own setting rather than the gate, so
	// the two cannot disagree on whether a shared device is partly or wholly consumed.
	allocatedState, err := buildAllocatedState(ctx, c.cl, draFeatures.ConsumableCapacity)
	if err != nil {
		return nil, fmt.Errorf("building allocated device state: %w", err)
	}

	classLister, err := newDeviceClassCache(ctx, c.cl)
	if err != nil {
		return nil, fmt.Errorf("listing DeviceClasses: %w", err)
	}

	allocator, err := structured.NewAllocator(ctx, draFeatures, allocatedState, classLister, deviceSlices, c.celCache.get())
	if err != nil {
		return nil, fmt.Errorf("creating DRA allocator: %w", err)
	}
	return allocator, nil
}

func (c *DRAChecker) filterByDevices(
	ctx context.Context,
	feasible []MatchedCandidate,
	allocator structured.Allocator,
	claims resourceClaimsForPod,
	stats *NodeExclusionStats,
) ([]MatchedCandidate, error) {
	logger := log.FromContext(ctx)
	var draFeasible []MatchedCandidate
	for _, candidate := range feasible {
		node := candidate.GetNode()
		if node == nil {
			// A candidate always carries its node, so this is a programming error
			// rather than a placement outcome. Guessing would admit an unchecked node.
			return nil, errors.New("candidate has no node, cannot evaluate DRA claims")
		}

		results, err := allocator.Allocate(ctx, node, claims.forNode(node))
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			// A failure usually means broken cluster configuration, such as a
			// DeviceClass whose CEL does not compile. Excluding only this node keeps
			// one bad DeviceClass from stalling all scheduling; the log surfaces it.
			logger.V(2).Info("Excluding node: DRA allocation failed", "node", node.Name, "error", err)
			stats.DRANoFit++
			continue
		}
		if results == nil {
			logger.V(5).Info("Node lacks matching DRA devices", "node", node.Name)
			stats.DRANoFit++
			continue
		}

		draFeasible = append(draFeasible, candidate)
	}
	return draFeasible, nil
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
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("kueue-sim-%s", prc.Name),
				Namespace: namespace,
			},
			Spec: *spec,
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
		deviceClass, err := dra.ResolveDeviceClass(ctx, cl, resourceName)
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
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kueue-sim-extended-resources",
			Namespace: podTemplate.Namespace,
		},
		Spec: resourceapi.ResourceClaimSpec{
			Devices: resourceapi.DeviceClaim{Requests: requests},
		},
	}}, names, nil
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

func buildAllocatedState(ctx context.Context, cl client.Client, consumableCapacity bool) (structured.AllocatedState, error) {
	allocatedDevices := sets.New[structured.DeviceID]()
	allocatedSharedDeviceIDs := sets.New[structured.SharedDeviceID]()
	aggregatedCapacity := structured.NewConsumedCapacityCollection()
	var claims resourceapi.ResourceClaimList
	if err := cl.List(ctx, &claims); err != nil {
		return structured.AllocatedState{}, fmt.Errorf("listing ResourceClaims: %w", err)
	}

	for i := range claims.Items {
		claim := &claims.Items[i]
		if claim.Status.Allocation == nil {
			continue
		}
		for _, result := range claim.Status.Allocation.Devices.Results {
			if ptr.Deref(result.AdminAccess, false) {
				continue
			}
			deviceID := structured.MakeDeviceID(result.Driver, result.Pool, result.Device)
			if consumableCapacity && result.ShareID != nil {
				sharedID := structured.MakeSharedDeviceID(deviceID, result.ShareID)
				allocatedSharedDeviceIDs.Insert(sharedID)
				if result.ConsumedCapacity != nil {
					cc := structured.NewDeviceConsumedCapacity(deviceID, result.ConsumedCapacity)
					aggregatedCapacity.Insert(cc)
				}
				continue
			}
			allocatedDevices.Insert(deviceID)
		}
	}

	return structured.AllocatedState{
		AllocatedDevices:         allocatedDevices,
		AllocatedSharedDeviceIDs: allocatedSharedDeviceIDs,
		AggregatedCapacity:       aggregatedCapacity,
	}, nil
}

// deviceClassCache serves the DeviceClasses read when the allocator was built. The
// upstream lister takes no context yet is called during allocation, so every class is
// resolved up front.
type deviceClassCache struct {
	all    []*resourceapi.DeviceClass
	byName map[string]*resourceapi.DeviceClass
}

func newDeviceClassCache(ctx context.Context, cl client.Client) (*deviceClassCache, error) {
	var deviceClasses resourceapi.DeviceClassList
	if err := cl.List(ctx, &deviceClasses); err != nil {
		return nil, err
	}
	cache := &deviceClassCache{
		all:    make([]*resourceapi.DeviceClass, len(deviceClasses.Items)),
		byName: make(map[string]*resourceapi.DeviceClass, len(deviceClasses.Items)),
	}
	for i := range deviceClasses.Items {
		dc := &deviceClasses.Items[i]
		cache.all[i] = dc
		cache.byName[dc.Name] = dc
	}
	return cache, nil
}

func (l *deviceClassCache) List() ([]*resourceapi.DeviceClass, error) {
	return l.all, nil
}

func (l *deviceClassCache) Get(className string) (*resourceapi.DeviceClass, error) {
	dc, found := l.byName[className]
	if !found {
		return nil, apierrors.NewNotFound(resourceapi.Resource("deviceclasses"), className)
	}
	return dc, nil
}

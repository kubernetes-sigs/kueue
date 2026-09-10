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
	"sync"

	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	dracel "k8s.io/dynamic-resource-allocation/cel"
	"k8s.io/dynamic-resource-allocation/structured"
	"k8s.io/kubernetes/pkg/features"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// DRAChecker drops candidate nodes that cannot supply a Pod's ResourceClaims. It
// allocates with the engine kube-scheduler uses, so both reach the same answer.
type DRAChecker struct {
	inner     SimulatorSnapshot
	cl        client.Client
	celCache  *dracel.Cache
	allocator lazyAllocator
}

func NewDRAChecker(inner SimulatorSnapshot, cl client.Client) *DRAChecker {
	c := &DRAChecker{
		inner: inner,
		cl:    cl,
		// The cache size matches the one kube-scheduler's dynamicresources
		// plugin uses, so identical selectors cost the same on both sides.
		celCache: dracel.NewCache(10, dracel.Features{
			EnableConsumableCapacity: utilfeature.DefaultFeatureGate.Enabled(features.DRAConsumableCapacity),
			EnableListTypeAttributes: utilfeature.DefaultFeatureGate.Enabled(features.DRAListTypeAttributes),
		}),
	}
	c.allocator.build = c.buildAllocator
	return c
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

	if requirements.PodTemplate == nil || !hasDRAClaims(requirements.PodTemplate) {
		return feasible, nil
	}

	// The claims belong to the Workload rather than the cluster, so unlike the
	// allocator they are resolved on every call. They are read by name, not listed.
	claims, err := buildSyntheticClaims(ctx, c.cl, requirements.PodTemplate.Namespace, requirements.PodTemplate)
	if err != nil {
		return nil, fmt.Errorf("building synthetic DRA claims: %w", err)
	}
	if len(claims) == 0 {
		return feasible, nil
	}

	allocator, err := c.allocator.get(ctx)
	if err != nil {
		return nil, err
	}

	return c.filterByDevices(ctx, feasible, allocator, claims, stats)
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

	allocatedState, err := buildAllocatedState(ctx, c.cl)
	if err != nil {
		return nil, fmt.Errorf("building allocated device state: %w", err)
	}

	classLister, err := newDeviceClassCache(ctx, c.cl)
	if err != nil {
		return nil, fmt.Errorf("listing DeviceClasses: %w", err)
	}

	allocator, err := structured.NewAllocator(ctx, draFeatures(), allocatedState, classLister, deviceSlices, c.celCache)
	if err != nil {
		return nil, fmt.Errorf("creating DRA allocator: %w", err)
	}
	return allocator, nil
}

func (c *DRAChecker) filterByDevices(
	ctx context.Context,
	feasible []MatchedCandidate,
	allocator structured.Allocator,
	claims []*resourceapi.ResourceClaim,
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

		results, err := allocator.Allocate(ctx, node, claims)
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

// draFeatures reads the Kubernetes DRA gates, not the Kueue ones: these decide which
// devices the allocator picks, and Kueue must pick what kube-scheduler would.
func draFeatures() structured.Features {
	return structured.Features{
		AdminAccess:            utilfeature.DefaultFeatureGate.Enabled(features.DRAAdminAccess),
		ConsumableCapacity:     utilfeature.DefaultFeatureGate.Enabled(features.DRAConsumableCapacity),
		DeviceBindingAndStatus: utilfeature.DefaultFeatureGate.Enabled(features.DRADeviceBindingConditions),
		DeviceTaints:           utilfeature.DefaultFeatureGate.Enabled(features.DRADeviceTaints),
		ListTypeAttributes:     utilfeature.DefaultFeatureGate.Enabled(features.DRAListTypeAttributes),
		PartitionableDevices:   utilfeature.DefaultFeatureGate.Enabled(features.DRAPartitionableDevices),
		PrioritizedList:        utilfeature.DefaultFeatureGate.Enabled(features.DRAPrioritizedList),
	}
}

func hasDRAClaims(podTemplate *corev1.PodTemplateSpec) bool {
	return len(podTemplate.Spec.ResourceClaims) > 0
}

func buildSyntheticClaims(ctx context.Context, cl client.Client, namespace string, podTemplate *corev1.PodTemplateSpec) ([]*resourceapi.ResourceClaim, error) {
	var claims []*resourceapi.ResourceClaim
	for _, prc := range podTemplate.Spec.ResourceClaims {
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

// resolveClaimSpec returns a nil spec and no error when the PodResourceClaim names
// neither a claim nor a template. The API allows that, so the caller skips it.
func resolveClaimSpec(ctx context.Context, cl client.Client, namespace string, prc corev1.PodResourceClaim) (*resourceapi.ResourceClaimSpec, error) {
	switch {
	case prc.ResourceClaimTemplateName != nil:
		var tmpl resourceapi.ResourceClaimTemplate
		if err := cl.Get(ctx, client.ObjectKey{Namespace: namespace, Name: *prc.ResourceClaimTemplateName}, &tmpl); err != nil {
			return nil, err
		}
		return &tmpl.Spec.Spec, nil
	case prc.ResourceClaimName != nil:
		var claim resourceapi.ResourceClaim
		if err := cl.Get(ctx, client.ObjectKey{Namespace: namespace, Name: *prc.ResourceClaimName}, &claim); err != nil {
			return nil, err
		}
		return &claim.Spec, nil
	default:
		return nil, nil
	}
}

func buildAllocatedState(ctx context.Context, cl client.Client) (structured.AllocatedState, error) {
	allocatedDevices := sets.New[structured.DeviceID]()
	allocatedSharedDeviceIDs := sets.New[structured.SharedDeviceID]()
	aggregatedCapacity := structured.NewConsumedCapacityCollection()
	enabledCC := utilfeature.DefaultFeatureGate.Enabled(features.DRAConsumableCapacity)

	var claimList resourceapi.ResourceClaimList
	if err := cl.List(ctx, &claimList); err != nil {
		return structured.AllocatedState{}, fmt.Errorf("listing ResourceClaims: %w", err)
	}

	for i := range claimList.Items {
		claim := &claimList.Items[i]
		if claim.Status.Allocation == nil {
			continue
		}
		for _, result := range claim.Status.Allocation.Devices.Results {
			if ptr.Deref(result.AdminAccess, false) {
				continue
			}
			deviceID := structured.MakeDeviceID(result.Driver, result.Pool, result.Device)
			if enabledCC && result.ShareID != nil {
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
	var list resourceapi.DeviceClassList
	if err := cl.List(ctx, &list); err != nil {
		return nil, err
	}
	cache := &deviceClassCache{
		all:    make([]*resourceapi.DeviceClass, len(list.Items)),
		byName: make(map[string]*resourceapi.DeviceClass, len(list.Items)),
	}
	for i := range list.Items {
		dc := &list.Items[i]
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

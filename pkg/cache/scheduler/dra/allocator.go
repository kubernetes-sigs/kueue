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
	"sync"

	"github.com/go-logr/logr"
	resourceapi "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	dracel "k8s.io/dynamic-resource-allocation/cel"
	"k8s.io/dynamic-resource-allocation/structured"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/dynamicresources"
	schedulerfeature "k8s.io/kubernetes/pkg/scheduler/framework/plugins/feature"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utilpodset "sigs.k8s.io/kueue/pkg/util/podset"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workload/evict"
	"sigs.k8s.io/kueue/pkg/workload/finish"
)

// CELCache holds the compiled device selectors, keyed by the expression, so one survives
// the Checker that compiled it and is compiled once for the process rather than once
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

// snapshotDevices is the device side of one snapshot, which simulations change in place
// and undo. A hold is the devices of one claim or one Pod. A promise is a hold for a Pod
// of an admitted Workload whose claims kube-scheduler has not allocated yet.
type snapshotDevices struct {
	// log reports a simulation that undoes what it did not do, which is a bug.
	log        logr.Logger
	allocators *nodeAllocators
	state      *deviceState
	// byWorkload are the promises of each Workload, which placing it again gives back.
	// Its bound Pods keep the devices of their claims.
	byWorkload map[workload.Reference][]*deviceHold
	// bound counts the bound Pods of each admitted Workload that use devices, by PodSet
	// and node.
	bound map[workload.Reference]map[kueue.PodSetReference]map[string]int32
	// released counts the simulations that gave each hold back.
	released map[*deviceHold]int
}

func (d *snapshotDevices) giveBack(holds []*deviceHold) {
	for _, hold := range holds {
		if d.released[hold] == 0 {
			d.state.release(*hold)
		}
		d.released[hold]++
	}
}

func (d *snapshotDevices) takeBack(holds []*deviceHold) {
	for _, hold := range holds {
		if d.released[hold] == 0 {
			d.log.Info("Taking back devices that were not given back")
			continue
		}
		d.released[hold]--
		if d.released[hold] == 0 {
			delete(d.released, hold)
			d.state.take(*hold)
		}
	}
}

// drop removes holds for good. A hold that ReplacePods took can be given back already,
// when the Workload is placed again.
func (d *snapshotDevices) drop(holds []*deviceHold) {
	for _, hold := range holds {
		if d.released[hold] == 0 {
			d.state.release(*hold)
		}
		delete(d.released, hold)
	}
}

// lazyDevices builds the snapshot's devices on first use.
type lazyDevices struct {
	build func(context.Context) (*snapshotDevices, error)
	once  sync.Once
	value *snapshotDevices
	err   error
}

func (l *lazyDevices) get(ctx context.Context) (*snapshotDevices, error) {
	l.once.Do(func() {
		l.value, l.err = l.build(ctx)
	})
	return l.value, l.err
}

// built returns the devices if they are built already, else nil.
func (l *lazyDevices) built() *snapshotDevices {
	return l.value
}

func (c *Checker) buildDevices(ctx context.Context) (*snapshotDevices, error) {
	deviceSlices, err := c.listDeviceSlices(ctx)
	if err != nil {
		return nil, err
	}
	pools := newSlicePools(deviceSlices)
	draFeatures := allocatorFeatures()
	devices := &snapshotDevices{
		log:        log.FromContext(ctx),
		state:      newDeviceState(draFeatures.ConsumableCapacity),
		byWorkload: make(map[workload.Reference][]*deviceHold),
		released:   make(map[*deviceHold]int),
	}
	if err := c.addAllocatedClaims(ctx, devices); err != nil {
		return nil, fmt.Errorf("building allocated device state: %w", err)
	}

	classLister, err := newDeviceClassCache(ctx, c.cl)
	if err != nil {
		return nil, fmt.Errorf("listing DeviceClasses: %w", err)
	}

	devices.allocators, err = newNodeAllocators(ctx, draFeatures, devices.state, classLister, deviceSlices, c.celCache.get(), pools)
	if err != nil {
		return nil, fmt.Errorf("creating DRA allocator: %w", err)
	}

	// Promises go in after the allocator exists, because they are allocated by it.
	devices.bound = c.owners.boundPods(c.UsesDevices)
	for _, wl := range c.assumedWorkloads {
		if err := c.addPromises(ctx, devices, wl); err != nil {
			return nil, err
		}
	}
	return devices, nil
}

// listDeviceSlices lists the ResourceSlices with the DeviceTaintRules applied.
func (c *Checker) listDeviceSlices(ctx context.Context) ([]*resourceapi.ResourceSlice, error) {
	var sliceList resourceapi.ResourceSliceList
	if err := c.cl.List(ctx, &sliceList); err != nil {
		return nil, fmt.Errorf("listing ResourceSlices: %w", err)
	}
	deviceSlices := make([]*resourceapi.ResourceSlice, len(sliceList.Items))
	for i := range sliceList.Items {
		deviceSlices[i] = &sliceList.Items[i]
	}
	return applyDeviceTaintRules(ctx, c.cl, c.deviceTaintRules, deviceSlices)
}

// allocatorFeatures reads the Kubernetes DRA gates by the call kube-scheduler makes, so
// the two allocators stay in step. Device taints also need KueueDRAIntegrationDeviceTaints.
func allocatorFeatures() structured.Features {
	draFeatures := dynamicresources.AllocatorFeatures(schedulerfeature.NewSchedulerFeaturesFromGates(utilfeature.DefaultFeatureGate))
	draFeatures.DeviceTaints = draFeatures.DeviceTaints && features.Enabled(features.KueueDRAIntegrationDeviceTaints)
	return draFeatures
}

// addAllocatedClaims takes the devices of every allocated ResourceClaim.
func (c *Checker) addAllocatedClaims(ctx context.Context, devices *snapshotDevices) error {
	var claims resourceapi.ResourceClaimList
	if err := c.cl.List(ctx, &claims); err != nil {
		return fmt.Errorf("listing ResourceClaims: %w", err)
	}
	for i := range claims.Items {
		claim := &claims.Items[i]
		if claim.Status.Allocation == nil {
			continue
		}
		devices.state.take(devices.state.holdOf(claim.Status.Allocation.Devices.Results))
	}
	return nil
}

// addPromises takes the devices of an admitted Workload's Pods that are not bound yet,
// on their assigned nodes. Bound Pods hold theirs through allocated claims, and the
// Pods of an evicted Workload are going away.
func (c *Checker) addPromises(ctx context.Context, devices *snapshotDevices, wl *kueue.Workload) error {
	if wl == nil || wl.Status.Admission == nil || finish.IsFinished(wl) || evict.IsEvicted(wl) {
		return nil
	}
	wlKey := workload.Key(wl)
	reclaimable := workload.ReclaimableCounts(wl)
	granted := workload.ExtractGrantedPodSetCounts(wl)
	for _, psa := range wl.Status.Admission.PodSetAssignments {
		ps := utilpodset.FindPodSetByName(wl.Spec.PodSets, psa.Name)
		if ps == nil || !c.UsesDevices(&ps.Template.Spec) {
			continue
		}
		// A PodSet whose Pods all finished frees its devices, as TAS frees its topology
		// usage. A partly finished PodSet keeps both, since its nodes are not recorded.
		if features.Enabled(features.ReclaimablePods) && reclaimable[psa.Name] >= granted[psa.Name] {
			continue
		}
		nodeCounts := utiltas.NodePodCounts(utiltas.InternalFrom(psa.TopologyAssignment))
		if len(nodeCounts) == 0 {
			continue
		}
		// Pods and claims come from different informers, so a Pod seen bound before its
		// claim allocation is seen is counted by neither, until the next snapshot. A claim
		// allocated before its Pod is seen bound is counted twice, which only delays.
		placement := c.unboundPods(devices, NewDevicePlacement(ps, wl.Namespace, nodeCounts), wlKey)
		holds, err := c.reserve(ctx, devices, placement, true)
		devices.byWorkload[wlKey] = append(devices.byWorkload[wlKey], holds...)
		if isContextError(err) {
			return err
		}
		if err != nil {
			// These devices can be admitted onto again, until the next snapshot, or
			// kube-scheduler at binding, sees them taken.
			log.FromContext(ctx).V(2).Info("Could not reserve the devices of an admitted Workload", "workload", klog.KObj(wl), "podSet", psa.Name, "error", err)
		}
	}
	return nil
}

// reserve allocates the claims of the placement's Pods on their nodes and takes the devices.
// It stops at the first Pod that does not fit, returning what it took so far, unless
// bestEffort, which goes on with the next node and returns the errors joined.
func (c *Checker) reserve(ctx context.Context, devices *snapshotDevices, placement DevicePlacement, bestEffort bool) ([]*deviceHold, error) {
	nodeCounts := c.byNodeName(placement.NodeCounts)
	// Sorted so that the same placement always takes the same devices.
	var nodeNames []string
	for _, nodeName := range slices.Sorted(maps.Keys(nodeCounts)) {
		if nodeCounts[nodeName] > 0 {
			nodeNames = append(nodeNames, nodeName)
		}
	}
	if len(nodeNames) == 0 {
		return nil, nil
	}
	claims, err := c.newResourceClaimsForPod(ctx, placement.PodTemplate)
	if err != nil || claims.isEmpty() {
		return nil, err
	}
	var holds []*deviceHold
	var errs []error
	for _, nodeName := range nodeNames {
		nodeHolds, err := c.reserveOnNode(ctx, devices, claims, nodeName, nodeCounts[nodeName])
		holds = append(holds, nodeHolds...)
		if err != nil {
			if !bestEffort || isContextError(err) {
				return holds, err
			}
			errs = append(errs, err)
		}
	}
	return holds, errors.Join(errs...)
}

// unboundPods leaves out of the placement the bound Pods of its owners, which hold
// their devices through their claims. A Pod can belong to more than one owner, as the
// slices of an elastic Job do, so the largest count on a node is left out.
func (c *Checker) unboundPods(devices *snapshotDevices, placement DevicePlacement, owners ...workload.Reference) DevicePlacement {
	counts := c.byNodeName(placement.NodeCounts)
	for node := range counts {
		var bound int32
		for _, owner := range owners {
			bound = max(bound, devices.bound[owner][placement.podSet][node])
		}
		counts[node] -= bound
	}
	placement.NodeCounts = counts
	return placement
}

// byNodeName keys Pod counts by node name. TAS keys them by node name for an injected
// hostname level, and by the hostname label for a declared one.
func (c *Checker) byNodeName(counts map[string]int32) map[string]int32 {
	byName := make(map[string]int32, len(counts))
	for value, count := range counts {
		if c.nodes[value] == nil {
			if node := c.byHostname[value]; node != nil {
				value = node.Name
			}
		}
		byName[value] += count
	}
	return byName
}

func (c *Checker) reserveOnNode(ctx context.Context, devices *snapshotDevices, claims resourceClaimsForPod, nodeName string, count int32) ([]*deviceHold, error) {
	node := c.nodes[nodeName]
	if node == nil {
		return nil, fmt.Errorf("node %q is not in the snapshot", nodeName)
	}
	var holds []*deviceHold
	for range count {
		allocator, err := devices.allocators.forNode(ctx, node)
		if err != nil {
			return holds, err
		}
		results, err := allocator.Allocate(ctx, node, claims.forNode(node))
		if err != nil {
			if isContextError(err) {
				return holds, err
			}
			return holds, fmt.Errorf("%w on node %q: %w", errDevicesDoNotFit, nodeName, err)
		}
		if results == nil {
			return holds, fmt.Errorf("%w on node %q", errDevicesDoNotFit, nodeName)
		}
		hold := devices.state.holdOfAllocations(results)
		devices.state.take(hold)
		holds = append(holds, &hold)
	}
	return holds, nil
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

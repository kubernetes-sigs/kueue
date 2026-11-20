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

	resourceapi "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	dracel "k8s.io/dynamic-resource-allocation/cel"
	"k8s.io/dynamic-resource-allocation/structured"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/dynamicresources"
	schedulerfeature "k8s.io/kubernetes/pkg/scheduler/framework/plugins/feature"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	"sigs.k8s.io/kueue/pkg/features"
	utilpodset "sigs.k8s.io/kueue/pkg/util/podset"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	"sigs.k8s.io/kueue/pkg/workload"
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
	allocators *nodeAllocators
	state      *deviceState
	// byWorkload are the holds of each Workload, promises included.
	byWorkload map[workload.Reference][]*deviceHold
	// byPod is what the allocated claims of each Pod hold, by Pod UID.
	byPod map[types.UID][]*deviceHold
	// freeing is what claims hold whose Pods are gone: the resourceclaim controller
	// deallocates them next.
	freeing []*deviceHold
	// released counts the simulations that gave each hold back, since a Workload and
	// its Pods can be given back at once.
	released map[*deviceHold]int

	// pools are the pools whose devices each node can use, so a count on a node holds
	// while their generations stay the same.
	pools *slicePools
	// counts are the Pod counts computed in this snapshot, so Workloads with the same
	// claims do not count them on every node again.
	counts map[countKey]countEntry
}

// countKey names a count: Pods with these claims, next to a leader with these, on a node.
type countKey struct {
	node, claims, leader string
}

type countEntry struct {
	generation uint64
	fit        int32
	// exact is whether fit is all that fit, rather than the limit it stopped at.
	exact bool
}

// cachedCount is count(limit) for key, computed once while the devices of the node's
// pools stay the same.
func (d *snapshotDevices) cachedCount(key countKey, limit int32, count func(limit int32) (int32, error)) (int32, error) {
	// countPods tries one Pod even at a limit of zero, so a cached count must too.
	limit = max(limit, 1)
	if e, found := d.counts[key]; found && e.generation == d.pools.generation(d.state, key.node) && (e.exact || e.fit >= limit) {
		return min(e.fit, limit), nil
	}
	fit, err := count(limit)
	if err != nil {
		return fit, err
	}
	// Read after counting: it takes and gives back devices, which moves the generations on.
	d.counts[key] = countEntry{generation: d.pools.generation(d.state, key.node), fit: fit, exact: fit < limit}
	return fit, nil
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
			ctrllog.Log.WithName("dra").Info("Taking back devices that were not given back")
			continue
		}
		d.released[hold]--
		if d.released[hold] == 0 {
			delete(d.released, hold)
			d.state.take(*hold)
		}
	}
}

// giveBackTemporarily gives back holds and returns the function that takes them back.
func (d *snapshotDevices) giveBackTemporarily(holds []*deviceHold) (takeBack func()) {
	d.giveBack(holds)
	return func() { d.takeBack(holds) }
}

// workloadHolds is every hold of an admitted or placed Workload.
func (d *snapshotDevices) workloadHolds() []*deviceHold {
	var holds []*deviceHold
	for _, wlHolds := range d.byWorkload {
		holds = append(holds, wlHolds...)
	}
	return holds
}

// drop removes holds for good, given back or not.
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

// built is the devices if they are built already, else nil.
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
		state:      newDeviceState(draFeatures.ConsumableCapacity),
		byWorkload: make(map[workload.Reference][]*deviceHold),
		byPod:      make(map[types.UID][]*deviceHold),
		released:   make(map[*deviceHold]int),
		counts:     make(map[countKey]countEntry),
		pools:      pools,
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
	bound := c.owners.boundPods()
	for _, wl := range c.assumedWorkloads {
		c.addPromises(ctx, devices, wl, bound[workload.Key(wl)])
	}
	// Workloads a simulation removed before the devices were built stay removed.
	for wlKey := range c.removed {
		devices.giveBack(devices.byWorkload[wlKey])
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

// addAllocatedClaims takes the devices of every allocated ResourceClaim, and records
// which Pods and which Workload hold them.
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
		hold := devices.state.holdOf(claim.Status.Allocation.Devices.Results)
		devices.state.take(hold)
		pods := claimPods(claim)
		for _, pod := range pods {
			devices.byPod[pod.UID] = append(devices.byPod[pod.UID], &hold)
		}
		if c.owners.claimIsFreeing(claim, pods) {
			devices.freeing = append(devices.freeing, &hold)
		}
		for _, wlKey := range c.owners.claimWorkloads(claim.Namespace, pods) {
			devices.byWorkload[wlKey] = append(devices.byWorkload[wlKey], &hold)
		}
	}
	return nil
}

// addPromises takes the devices of an admitted Workload's Pods that are not bound
// yet, on their assigned nodes, so that the next Workload is not admitted onto them.
// A bound Pod's devices are taken already, by its allocated claims.
func (c *Checker) addPromises(ctx context.Context, devices *snapshotDevices, wl *kueue.Workload, bound map[kueue.PodSetReference]map[string]int32) {
	if wl == nil || wl.Status.Admission == nil || finish.IsFinished(wl) {
		return
	}
	log := ctrllog.FromContext(ctx)
	wlKey := workload.Key(wl)
	reclaimable := workload.ReclaimableCounts(wl)
	for _, psa := range wl.Status.Admission.PodSetAssignments {
		ps := utilpodset.FindPodSetByName(wl.Spec.PodSets, psa.Name)
		if ps == nil || !UsesDevices(&ps.Template.Spec) {
			continue
		}
		// A PodSet whose Pods all finished frees its devices, as TAS frees its topology
		// usage. A partly finished PodSet keeps both, since its nodes are not recorded.
		if features.Enabled(features.ReclaimablePods) && reclaimable[psa.Name] >= ps.Count {
			continue
		}
		nodeCounts := c.byNodeName(utiltas.NodePodCounts(utiltas.InternalFrom(psa.TopologyAssignment)))
		if len(nodeCounts) == 0 {
			continue
		}
		// Pods and claims come from different informers, so a Pod seen bound before its
		// claim allocation is seen is counted by neither, until the next snapshot.
		for node, count := range bound[psa.Name] {
			nodeCounts[node] -= count
		}
		holds, err := c.reserve(ctx, devices, simulator.NewDevicePlacement(ps, wl.Namespace, nodeCounts), true)
		if err != nil {
			// Expected when the Workload is being evicted and its claims still hold the
			// devices. Otherwise these devices can be admitted onto again, until the next
			// snapshot, or kube-scheduler at binding, sees them taken.
			log.V(4).Info("Could not reserve the devices of an admitted Workload", "workload", klog.KObj(wl), "podSet", psa.Name, "error", err)
		}
		devices.byWorkload[wlKey] = append(devices.byWorkload[wlKey], holds...)
	}
}

// reserve allocates the claims of the placement's Pods on their nodes and takes the devices.
// It stops at the first Pod that does not fit, returning what it took so far, unless
// bestEffort, which goes on with the next node and returns the errors joined.
func (c *Checker) reserve(ctx context.Context, devices *snapshotDevices, placement simulator.DevicePlacement, bestEffort bool) ([]*deviceHold, error) {
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
			return holds, err
		}
		if results == nil {
			return holds, fmt.Errorf("node %q has no devices left for the Pod", nodeName)
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

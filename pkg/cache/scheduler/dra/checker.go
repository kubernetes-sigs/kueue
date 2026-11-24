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
	"iter"
	"slices"

	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	schedulingv1alpha3 "k8s.io/api/scheduling/v1alpha3"
	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/podtracker"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	kueuedra "sigs.k8s.io/kueue/pkg/dra"
	"sigs.k8s.io/kueue/pkg/workload"
)

// Checker drops candidate nodes that cannot supply a Pod's ResourceClaims, and bounds
// how many of the Pods fit on the rest. It allocates with the engine kube-scheduler
// uses, so both reach the same answer. It is the device check of KueueDRADeviceFeasibility,
// and the snapshot builds it only while that gate is on.
type Checker struct {
	inner    simulator.SchedulerSimulator
	cl       client.Client
	celCache *CELCache
	devices  lazyDevices

	// owners and assumedWorkloads say which Workload holds which devices, and which
	// devices admitted Workloads will take once kube-scheduler allocates their claims.
	owners           podOwners
	assumedWorkloads []*kueue.Workload
	nodes            map[string]*corev1.Node
	// byHostname finds a node from a declared kubernetes.io/hostname level, whose values
	// are labels that need not match node names.
	byHostname map[string]*corev1.Node

	// deviceTaintRules is whether the cluster serves DeviceTaintRules, decided once at
	// startup: listing a kind the API server lacks costs a discovery request each time.
	deviceTaintRules bool

	// draBacked reports whether a DeviceClass backs an extended resource. Nil means any
	// extended resource may be backed.
	draBacked func(corev1.ResourceName) bool
}

// SnapshotState is what the snapshot knows that decides which devices are taken.
type SnapshotState struct {
	Nodes            []*corev1.Node
	Pods             *podtracker.Tracker
	AssumedWorkloads []*kueue.Workload
	// Ownership defaults to matching Pods to Workloads by name.
	Ownership PodOwnership
	// DRABackedResources are the extended resources that a DeviceClass backs. The rest
	// come from device plugins and take no devices. Nil treats every extended resource
	// as one that a DeviceClass may back.
	DRABackedResources *kueuedra.ExtendedResourceCache
}

// NewChecker wraps inner with the device check. Pass the scheduler cache's CELCache
// rather than a fresh one: it only pays off by outliving the snapshot.
func NewChecker(inner simulator.SchedulerSimulator, cl client.Client, celCache *CELCache, deviceTaintRules bool, state SnapshotState) *Checker {
	c := &Checker{
		inner:            inner,
		cl:               cl,
		celCache:         celCache,
		owners:           newPodOwners(state),
		assumedWorkloads: state.AssumedWorkloads,
		nodes:            make(map[string]*corev1.Node, len(state.Nodes)),
		byHostname:       make(map[string]*corev1.Node, len(state.Nodes)),
		deviceTaintRules: deviceTaintRules,
	}
	for _, node := range state.Nodes {
		c.nodes[node.Name] = node
		if hostname := node.Labels[corev1.LabelHostname]; hostname != "" {
			if _, found := c.byHostname[hostname]; !found {
				c.byHostname[hostname] = node
			}
		}
	}
	if state.DRABackedResources != nil {
		c.draBacked = state.DRABackedResources.Has
	}
	c.devices.build = c.buildDevices
	return c
}

// Simulate needs no DRA handling: every change to the devices comes with its own undo.
func (c *Checker) Simulate(ctx context.Context, fn func()) error {
	return c.inner.Simulate(ctx, fn)
}

// PreemptWorkload frees the Workload's Pods but not the devices its claims hold: the
// allocator is built once per snapshot from the ResourceClaims as they stand. So
// preemption cannot make a Workload device-feasible, and the check stays restrictive
// rather than over-admitting. Releasing them is Beta work in keps/2941-DRA.
func (c *Checker) PreemptWorkload(ctx context.Context, wlKey workload.Reference) (func() error, error) {
	return c.inner.PreemptWorkload(ctx, wlKey)
}

func (c *Checker) PreemptPods(ctx context.Context, pods []simulator.PodRef) (func() error, error) {
	inner, ok := c.inner.(simulator.PodPreemptingSimulator)
	if !ok {
		return nil, errors.New("underlying simulator cannot preempt individual Pods")
	}
	return inner.PreemptPods(ctx, pods)
}

// DevicePlacement is how many Pods with this template go to each node, for the devices
// they take.
type DevicePlacement struct {
	PodTemplate *corev1.PodTemplateSpec
	NodeCounts  map[string]int32
	// podSet names the PodSet the Pods belong to, to find which of them are bound.
	podSet kueue.PodSetReference
}

// NewDevicePlacement places the PodSet's Pods. Its template takes the Workload's
// namespace, where the ResourceClaimTemplates it names live.
func NewDevicePlacement(ps *kueue.PodSet, namespace string, nodeCounts map[string]int32) DevicePlacement {
	template := ps.Template.DeepCopy()
	template.Namespace = namespace
	return DevicePlacement{PodTemplate: template, NodeCounts: nodeCounts, podSet: ps.Name}
}

// ReservePods takes the devices of Pods that TAS places for one PodSet group, so that
// the Workload's next group sees them taken. ok is false when they do not fit, and
// then nothing is taken.
func (c *Checker) ReservePods(ctx context.Context, placement DevicePlacement) (revert func(), ok bool, err error) {
	if !c.UsesDevices(&placement.PodTemplate.Spec) {
		return func() {}, true, nil
	}
	devices, err := c.devices.get(ctx)
	if err != nil {
		return nil, false, err
	}
	holds, err := c.reserve(ctx, devices, placement, false)
	if err != nil {
		devices.drop(holds)
		return notFit(ctx, err)
	}
	return func() { devices.drop(holds) }, true, nil
}

// ReplacePods takes the devices of a Workload's Pods placed in this cycle, in place of
// what Kueue promised it and the Workloads it replaces. Their bound Pods keep the devices
// of their claims, so only the Pods not bound yet take devices. ok is false when the
// Pods do not fit, and then nothing changes. A nil revert means that nothing changed.
func (c *Checker) ReplacePods(ctx context.Context, wlKey workload.Reference, replaced []workload.Reference, placements []DevicePlacement) (revert func(), ok bool, err error) {
	owners := append([]workload.Reference{wlKey}, replaced...)
	if !c.changesDevices(owners, placements) {
		return nil, true, nil
	}
	devices, err := c.devices.get(ctx)
	if err != nil {
		return nil, false, err
	}

	old := make(map[workload.Reference][]*deviceHold, len(owners))
	for _, owner := range owners {
		old[owner] = devices.byWorkload[owner]
		devices.giveBack(old[owner])
	}
	restoreOld := func() {
		for _, owner := range owners {
			devices.byWorkload[owner] = old[owner]
			devices.takeBack(old[owner])
		}
	}

	var taken []*deviceHold
	for _, placement := range placements {
		holds, err := c.reserve(ctx, devices, c.unboundPods(devices, placement, owners...), false)
		taken = append(taken, holds...)
		if err != nil {
			devices.drop(taken)
			restoreOld()
			return notFit(ctx, err)
		}
	}
	for _, owner := range replaced {
		delete(devices.byWorkload, owner)
	}
	devices.byWorkload[wlKey] = taken
	return func() {
		devices.drop(taken)
		restoreOld()
	}, true, nil
}

// PodsFit reports whether ReplacePods would find the devices of the Pods free, and
// changes nothing.
func (c *Checker) PodsFit(ctx context.Context, wlKey workload.Reference, replaced []workload.Reference, placements []DevicePlacement) (bool, error) {
	revert, ok, err := c.ReplacePods(ctx, wlKey, replaced, placements)
	if revert != nil {
		revert()
	}
	return ok, err
}

// changesDevices reports whether placing the Pods, or giving back what Kueue promised
// the owners, changes the devices left. It builds no device state to find out.
func (c *Checker) changesDevices(owners []workload.Reference, placements []DevicePlacement) bool {
	if slices.ContainsFunc(placements, func(p DevicePlacement) bool { return c.UsesDevices(&p.PodTemplate.Spec) }) {
		return true
	}
	devices := c.devices.built()
	return devices != nil && slices.ContainsFunc(owners, func(k workload.Reference) bool { return len(devices.byWorkload[k]) > 0 })
}

// errDevicesDoNotFit marks a placement that the devices left cannot take.
var errDevicesDoNotFit = errors.New("the devices left do not fit the Pods")

// notFit returns the result of a reservation that failed. A placement that the devices
// left cannot take is not an error.
func notFit(ctx context.Context, err error) (func(), bool, error) {
	if !errors.Is(err, errDevicesDoNotFit) {
		return nil, false, err
	}
	log.FromContext(ctx).V(4).Info("Pods do not fit the devices left", "error", err)
	return func() {}, false, nil
}

func (c *Checker) FindFeasibleNodes(
	ctx context.Context,
	candidates iter.Seq[simulator.Candidate],
	requirements *simulator.PodRequirements,
	stats *simulator.NodeExclusionStats,
) ([]simulator.MatchedCandidate, error) {
	feasible, err := c.inner.FindFeasibleNodes(ctx, candidates, requirements, stats)
	if err != nil {
		return nil, err
	}

	if requirements.PodTemplate == nil || !c.UsesDevices(&requirements.PodTemplate.Spec) {
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

	devices, err := c.devices.get(ctx)
	if err != nil {
		return nil, err
	}

	var leaderClaims resourceClaimsForPod
	if requirements.LeaderPodTemplate != nil {
		if leaderClaims, err = c.newResourceClaimsForPod(ctx, requirements.LeaderPodTemplate); err != nil {
			return nil, err
		}
	}
	return c.filterByDevices(ctx, feasible, devices, &claims, &leaderClaims, requirements.PodCount, stats)
}

func (c *Checker) filterByDevices(
	ctx context.Context,
	feasible []simulator.MatchedCandidate,
	devices *snapshotDevices,
	claims *resourceClaimsForPod,
	leaderClaims *resourceClaimsForPod,
	podCount int32,
	stats *simulator.NodeExclusionStats,
) ([]simulator.MatchedCandidate, error) {
	var draFeasible []simulator.MatchedCandidate
	for _, candidate := range feasible {
		node := candidate.GetNode()
		if node == nil {
			// A candidate always carries its node, so this is a programming error
			// rather than a placement outcome. Guessing would admit an unchecked node.
			return nil, errors.New("candidate has no node, cannot evaluate DRA claims")
		}
		limit, fits, err := devices.devicePodLimit(ctx, node, claims, leaderClaims, podCount)
		if err != nil {
			return nil, err
		}
		if !fits {
			stats.DRANoFit++
			continue
		}
		candidate.SetDevicePodLimit(limit)
		draFeasible = append(draFeasible, candidate)
	}
	return draFeasible, nil
}

// devicePodLimit returns how many Pods the node's devices can take, at most podCount, alone
// and next to the leader. fits is false when no Pod fits or allocation fails: excluding
// only this node keeps one broken DeviceClass from stalling all scheduling.
func (d *snapshotDevices) devicePodLimit(ctx context.Context, node *corev1.Node, claims, leaderClaims *resourceClaimsForPod, podCount int32) (simulator.DevicePodLimit, bool, error) {
	logger := log.FromContext(ctx)
	nodeClaims := claims.forNode(node)
	fit, err := d.countPods(ctx, node, nodeClaims, podCount)
	if isContextError(err) {
		return simulator.DevicePodLimit{}, false, err
	}
	if err != nil {
		logger.V(3).Info("Excluding node: DRA allocation failed", "node", klog.KObj(node), "error", err)
		return simulator.DevicePodLimit{}, false, nil
	}
	if fit == 0 {
		logger.V(5).Info("Node lacks matching DRA devices", "node", klog.KObj(node))
		return simulator.DevicePodLimit{}, false, nil
	}
	// A leader without claims takes no devices, so as many fit next to it as alone.
	limit := simulator.DevicePodLimit{Pods: fit, PodsWithLeader: fit}
	if leaderClaims.isEmpty() {
		return limit, true, nil
	}
	limit.PodsWithLeader, err = d.countPodsWithLeader(ctx, node, leaderClaims.forNode(node), nodeClaims, podCount)
	if isContextError(err) {
		return simulator.DevicePodLimit{}, false, err
	}
	if err != nil {
		// As for the workers: the leader does not go on this node.
		logger.V(3).Info("DRA allocation failed for the leader", "node", klog.KObj(node), "error", err)
		limit.PodsWithLeader = 0
	}
	return limit, true, nil
}

// isContextError reports whether err ends the scheduling cycle rather than one node.
func isContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// countPods returns how many Pods with these claims fit on node, at most limit, and at
// least one is tried. The allocator has no capacity query, so it allocates until no Pod
// fits, then gives every device back.
func (d *snapshotDevices) countPods(ctx context.Context, node *corev1.Node, claims []*resourceapi.ResourceClaim, limit int32) (int32, error) {
	var holds []*deviceHold
	defer func() {
		for _, hold := range holds {
			d.state.release(*hold)
		}
	}()
	var fit int32
	for fit == 0 || fit < limit {
		allocator, err := d.allocators.forNode(ctx, node)
		if err != nil {
			return fit, err
		}
		results, err := allocator.Allocate(ctx, node, claims)
		if err != nil {
			if fit > 0 && !isContextError(err) {
				// The first Pod fit, so the claims are sound. The error comes from a device
				// that is left, such as a selector that fails on it, so no more Pods fit.
				return fit, nil
			}
			return fit, err
		}
		if results == nil {
			return fit, nil
		}
		fit++
		hold := d.state.holdOfAllocations(results)
		if hold.isEmpty() {
			// A Pod that takes no device, as with admin access, leaves the node as it was,
			// so every Pod fits.
			return max(fit, limit), nil
		}
		d.state.take(hold)
		holds = append(holds, &hold)
	}
	return fit, nil
}

// countPodsWithLeader returns how many Pods with claims fit on node next to the leader,
// at most limit. The leader takes its devices first, as it is admitted with them.
// Zero means the leader does not fit, or no Pod fits next to it.
func (d *snapshotDevices) countPodsWithLeader(ctx context.Context, node *corev1.Node, leaderClaims, claims []*resourceapi.ResourceClaim, limit int32) (int32, error) {
	allocator, err := d.allocators.forNode(ctx, node)
	if err != nil {
		return 0, err
	}
	results, err := allocator.Allocate(ctx, node, leaderClaims)
	if err != nil || results == nil {
		return 0, err
	}
	hold := d.state.holdOfAllocations(results)
	d.state.take(hold)
	defer d.state.release(hold)
	return d.countPods(ctx, node, claims, limit)
}

func (c *Checker) ScheduleWorkload(ctx context.Context, wlPods []*corev1.Pod, opts ...simulator.ScheduleOption) simulator.SchedulingResult {
	return c.inner.ScheduleWorkload(ctx, wlPods, opts...)
}

func (c *Checker) AddPodGroup(ctx context.Context, pg *schedulingv1beta1.PodGroup) error {
	return c.inner.AddPodGroup(ctx, pg)
}

func (c *Checker) RemovePodGroup(ctx context.Context, pg *schedulingv1beta1.PodGroup) error {
	return c.inner.RemovePodGroup(ctx, pg)
}

func (c *Checker) AddCompositePodGroup(ctx context.Context, cpg *schedulingv1alpha3.CompositePodGroup) error {
	return c.inner.AddCompositePodGroup(ctx, cpg)
}

func (c *Checker) RemoveCompositePodGroup(ctx context.Context, cpg *schedulingv1alpha3.CompositePodGroup) error {
	return c.inner.RemoveCompositePodGroup(ctx, cpg)
}

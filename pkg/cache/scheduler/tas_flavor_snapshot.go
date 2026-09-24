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

package scheduler

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"maps"
	"math"
	"slices"
	"strings"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/component-helpers/scheduling/corev1/nodeaffinity"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/podset"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltolerations "sigs.k8s.io/kueue/pkg/util/tolerations"
	"sigs.k8s.io/kueue/pkg/workload"
)

var (
	errCodeAssumptionsViolated = errors.New("code assumptions violated")
)

// domainState is the per-snapshot mutable state of a domain during the
// assignment algorithm, addressed by domain.idx.
type domainState struct {
	// podCount is a temporary pod count of the topology domains during the
	// assignment algorithm.
	//
	// In the first phase of the algorithm (traversal to the top the topology to
	// determine the level to fit the workload) it denotes the number of pods
	// which can fit in a given domain.
	//
	// In the second phase of the algorithm (traversal to the bottom to
	// determine the actual assignments) it denotes the number of pods actually
	// assigned to the given domain.
	podCount int32

	// sliceCount is a temporary slice count of the topology domains during the
	// assignment algorithm that denotes the number of slices that can fit within
	// that domain.
	//
	// For domains that are below the requested topology level the algorithm
	// assigns 0 to that field as this field makes no sense for lower level
	// domains.
	sliceCount int32

	// Leader capacities are populated only for requests with a leader PodSet.
	podCountWithLeader   int32
	sliceCountWithLeader int32
	leaderCount          int32

	// sliceCountWithTail is the number of whole slices that fit in the domain
	// when it also holds the partial slice of a PodSet whose count is not a
	// multiple of the slice size, and noTailFit when the partial slice does
	// not fit in it at all. sliceCountWithLeaderAndTail additionally reserves
	// room for the leader.
	//
	// Both are only computed when the PodSet has a partial slice, and are
	// left at their zero value otherwise; see tas_partial_slices.go.
	sliceCountWithTail          int32
	sliceCountWithLeaderAndTail int32

	// affinityScore is the sum of weights of all preferred affinity terms that match the node.
	// For non-leaf domains, it is the sum of affinity scores of all children.
	affinityScore int64

	// spread is how much of this domain, and of its parent, the PodSet group
	// being placed already occupies. The two are a rule's numerator and
	// denominator, so they are only ever assigned together, by
	// populateSpreadCounts. Zero unless spreading counts were supplied.
	spread spreadOccupancy

	// capacityBound is set by recordUsageDomainCaps, and read only at the level
	// it writes.
	capacityBound domainCapacityBound
}

func (s *domainState) sliceCapacity(withLeader, withTail bool) int32 {
	switch {
	case withLeader && withTail:
		return s.sliceCountWithLeaderAndTail
	case withTail:
		return s.sliceCountWithTail
	case withLeader:
		return s.sliceCountWithLeader
	default:
		return s.sliceCount
	}
}

func (s *domainState) fitsSlices(sliceCount, leaderCount int32, hasTail bool) bool {
	if leaderCount > 0 && s.leaderCount < leaderCount {
		return false
	}
	return s.sliceCapacity(leaderCount > 0, hasTail) >= sliceCount
}

// domainCapacityBound bounds the counts rolled up from a domain's leaves by what
// the domain's own remaining capacity allows, field for field against podCount,
// leaderCount and podCountWithLeader. A leaf only sees its own node, so it
// reports room the domain may already owe; domainTASUsage explains why.
// Attributing that usage to the node holding each Pod would remove the need for
// this bound.
type domainCapacityBound struct {
	podCount           int32
	leaderCount        int32
	podCountWithLeader int32
}

// spreadOccupancy is a domain's topology-spreading occupancy alongside its
// parent's, which a rule at the domain's level compares it against.
type spreadOccupancy struct {
	// count is the number of Workloads of the PodSet group being placed that
	// already occupy this domain.
	count int32

	// parentCount is the same for this domain's parent, or for the whole
	// flavor when the domain is a root.
	parentCount int32
}

// leafCapacity is the per-snapshot mutable capacity data of a leaf domain,
// addressed by leafDomain.leafIdx.
type leafCapacity struct {
	// freeCapacity represents the total node capacity minus the non-TAS usage,
	// coming from Pods which are not managed by workloads admitted by TAS
	// (typically static Pods, DaemonSets, or Deployments).
	freeCapacity resources.Requests

	// tasUsage represents the usage associated with TAS workloads.
	tasUsage resources.Requests

	// cachedRemainingCapacity stores the pre-computed remaining capacity (freeCapacity - tasUsage) for this leaf.
	// It is lazily calculated using LazyRequests and updated incrementally during TAS usage changes, avoiding repeated
	// map cloning and resource subtraction during capacity checks (e.g. preemption).
	cachedRemainingCapacity resources.LazyRequests
}

// leafCandidate adapts a shared leafDomain to the simulator's
// MatchedCandidate interface. The affinity score the simulator writes is
// per-snapshot state, so the candidate carries a reference to the snapshot
// owning the state instead of mutating the shared leaf.
type leafCandidate struct {
	leaf *leafDomain
	s    *TASFlavorSnapshot
}

func (c *leafCandidate) GetID() utiltas.TopologyDomainID {
	return c.leaf.id
}

func (c *leafCandidate) GetNode() *corev1.Node {
	return c.leaf.node
}

func (c *leafCandidate) SetAffinityScore(score int64) {
	c.s.domainStates[c.leaf.idx].affinityScore = score
}

func (c *leafCandidate) GetAffinityScore() int64 {
	return c.s.domainStates[c.leaf.idx].affinityScore
}

type TASFlavorSnapshot struct {
	log logr.Logger

	// topologyName indicates the name of the topology specified in the
	// ResourceFlavor spec.topologyName field.
	topologyName kueue.TopologyReference

	// topologyTree is the static topology structure, shared with the other
	// snapshots of the flavor. It must not be mutated.
	*topologyTree

	// domainStates holds the per-snapshot mutable state for domains, indexed by
	// domain.idx.
	domainStates []domainState

	// leafCapacities holds the per-snapshot mutable capacity data for leaves,
	// indexed by leafDomain.leafIdx.
	leafCapacities []leafCapacity

	// leafCandidates adapts the shared leaves to the simulator's mutable
	// candidate interface, indexed by leafDomain.leafIdx.
	leafCandidates []leafCandidate

	// tolerations represents the list of tolerations defined for the resource flavor
	tolerations []corev1.Toleration

	// nodeLabels represents the list of node labels defined for the resource flavor
	nodeLabels map[string]string

	// matchingLeavesCache caches the set of qualified leaves for a PodSet
	// of a Workload to avoid recalculating selectors/taints during preemption simulations or
	// multiple worker PodSet placements within the same scheduling cycle snapshot.
	matchingLeavesCache map[podSetMatchKey]*matchingLeavesCacheEntry

	// domainTASUsage holds the TAS usage of the domains which span several
	// leaves, i.e. only when the hostname level is virtual. A
	// TopologyAssignment on such a topology names the domain, not the node:
	// kube-scheduler picks the node inside the domain after ungating and never
	// reports it back, so the usage cannot be charged to a leaf.
	domainTASUsage map[utiltas.TopologyDomainID]resources.Requests

	// domainFreeCapacities caches the summed free capacity of each usage
	// domain's leaves. It is filled on first read and never invalidated, which
	// holds because addNonTASUsage is the only writer of leaf free capacity and
	// snapshot() calls it before the snapshot is used.
	domainFreeCapacities map[utiltas.TopologyDomainID]resources.Requests

	// schedulerSimulator stores enough data to run a WAS scheduling simulation.
	schedulerSimulator simulator.SchedulerSimulator

	resourceFormatter *resources.ResourceFormatter
}

// domainStateOf returns the snapshot's mutable state of the given shared domain.
func (s *TASFlavorSnapshot) domainStateOf(d *domain) *domainState {
	return &s.domainStates[d.idx]
}

// leafCapacityOf returns the snapshot's mutable capacity data for the given
// shared leaf.
func (s *TASFlavorSnapshot) leafCapacityOf(l *leafDomain) *leafCapacity {
	return &s.leafCapacities[l.leafIdx]
}

// candidates yields the snapshot's candidate adapters for all leaves.
func (s *TASFlavorSnapshot) candidates() iter.Seq[*leafCandidate] {
	return func(yield func(*leafCandidate) bool) {
		for i := range s.leafCandidates {
			if !yield(&s.leafCandidates[i]) {
				return
			}
		}
	}
}

// shallowCloneWithState returns a shallow copy of d with independent state
// initialized from d's current state.
//
// WARNING: This may reallocate s.domainStates. Callers must not retain pointers
// returned by domainStateOf across this call.
func (s *TASFlavorSnapshot) shallowCloneWithState(d *domain) *domain {
	clone := *d
	clone.idx = len(s.domainStates)
	s.domainStates = append(s.domainStates, s.domainStates[d.idx])
	return &clone
}

type podSetMatchKey struct {
	WorkloadUID types.UID
	PodSetName  string
	// EmptyCluster marks the entry holding what would fit if every Workload were
	// preempted, so it cannot answer what fits now.
	EmptyCluster bool
	// Leader separates the leader's entry from the workers', which would otherwise
	// share a key because both are built from the workers' PodSet name.
	Leader bool
}

// matchingLeavesCacheEntry stores the cached list of matching leaves and accumulated
// exclusion stats for a specific podSetMatchKey.
type matchingLeavesCacheEntry struct {
	leaves []simulator.MatchedCandidate
	stats  *tasExclusionStats
}

type tasFlavorSnapshotOptions struct {
	resourceFormatter *resources.ResourceFormatter
}

type tasFlavorSnapshotOption func(*tasFlavorSnapshotOptions)

func withResourceFormatter(formatter *resources.ResourceFormatter) tasFlavorSnapshotOption {
	return func(o *tasFlavorSnapshotOptions) {
		o.resourceFormatter = formatter
	}
}

// newTASFlavorSnapshot creates a snapshot backed by the shared topology tree,
// with fresh per-snapshot state: the leaves start at their static capacity
// with no usage, and the assignment-algorithm scratch state is zeroed.
func newTASFlavorSnapshot(
	log logr.Logger,
	flavor flavorInformation,
	tree *topologyTree,
	schedulerSimulator simulator.SchedulerSimulator,
	opts ...tasFlavorSnapshotOption,
) *TASFlavorSnapshot {
	options := &tasFlavorSnapshotOptions{}
	for _, opt := range opts {
		if opt != nil {
			opt(options)
		}
	}

	snapshot := &TASFlavorSnapshot{
		log:                  log,
		topologyName:         flavor.TopologyName,
		topologyTree:         tree,
		domainStates:         make([]domainState, tree.domainCount),
		domainTASUsage:       make(map[utiltas.TopologyDomainID]resources.Requests),
		domainFreeCapacities: make(map[utiltas.TopologyDomainID]resources.Requests),
		leafCapacities:       make([]leafCapacity, len(tree.leaves)),
		leafCandidates:       make([]leafCandidate, len(tree.leaves)),
		tolerations:          slices.Clone(flavor.Tolerations),
		nodeLabels:           maps.Clone(flavor.NodeLabels),
		schedulerSimulator:   schedulerSimulator,
		resourceFormatter:    options.resourceFormatter,
	}
	for _, leaf := range tree.leaves {
		snapshot.leafCapacities[leaf.leafIdx].freeCapacity = leaf.capacity.Clone()
		snapshot.leafCandidates[leaf.leafIdx] = leafCandidate{leaf: leaf, s: snapshot}
	}
	return snapshot
}

// NodeLabels returns a copy of the flavor's node labels.
func (s *TASFlavorSnapshot) NodeLabels() map[string]string {
	return maps.Clone(s.nodeLabels)
}

// Tolerations returns a copy of the flavor's tolerations.
func (s *TASFlavorSnapshot) Tolerations() []corev1.Toleration {
	tolerations := slices.Clone(s.tolerations)
	for i := range tolerations {
		if tolerations[i].TolerationSeconds != nil {
			seconds := *tolerations[i].TolerationSeconds
			tolerations[i].TolerationSeconds = &seconds
		}
	}
	return tolerations
}

func (s *TASFlavorSnapshot) addNonTASUsage(domainID utiltas.TopologyDomainID, usage resources.Requests) {
	// domainID comes from topologyTree.nodeToDomain, while leaves is populated
	// from the same node set by newTopologyTree, so the corresponding leaf exists.
	leafCapacity := s.leafCapacityOf(s.leaves[domainID])
	leafCapacity.freeCapacity.Sub(usage)
	leafCapacity.cachedRemainingCapacity = resources.LazyRequests{}
}

func (s *TASFlavorSnapshot) updateTASUsage(domainID utiltas.TopologyDomainID, usage resources.Requests, op usageOp, count int32) {
	u := usage.Clone()
	u.Add(resources.NewRequestsFromMap(map[corev1.ResourceName]int64{corev1.ResourcePods: int64(count)}))
	if op == add {
		s.addTASUsage(domainID, u)
	} else {
		s.removeTASUsage(domainID, u)
	}
}

func (s *TASFlavorSnapshot) getRemainingCapacity(leaf *leafDomain) resources.Requests {
	leafCapacity := s.leafCapacityOf(leaf)
	if leafCapacity.cachedRemainingCapacity.IsEmpty() {
		leafCapacity.cachedRemainingCapacity = resources.NewLazyRequests(leafCapacity.freeCapacity)
		leafCapacity.cachedRemainingCapacity.Sub(leafCapacity.tasUsage)
	}
	return leafCapacity.cachedRemainingCapacity.Get()
}

// hasDomain reports whether the snapshot holds the domain the usage is
// recorded against, i.e. whether it holds nodes the flavor selects.
func (s *TASFlavorSnapshot) hasDomain(domainID utiltas.TopologyDomainID) bool {
	return s.usageDomain(domainID) != nil
}

// usageLevelIdx is the level TAS usage is recorded against, which is the level
// the TopologyAssignment names: the user-lowest one.
func (s *TASFlavorSnapshot) usageLevelIdx() int {
	if s.virtualHostname {
		return len(s.levelKeys) - 2
	}
	return len(s.levelKeys) - 1
}

// usageDomains returns the domains usage is recorded against, keyed the same
// way as the TopologyAssignment values.
func (s *TASFlavorSnapshot) usageDomains() domainByID {
	return s.domainsPerLevel[s.usageLevelIdx()]
}

// usageDomain returns the domain that usage keyed by domainID belongs to: the
// leaf itself when the topology declares the hostname level, otherwise the
// user-lowest domain, which spans several leaves. It never consults the leaf
// map, where a node name can collide with a domain ID.
func (s *TASFlavorSnapshot) usageDomain(domainID utiltas.TopologyDomainID) *domain {
	return s.usageDomains()[domainID]
}

// addTASUsageForHeldDomains adds usage only for the usage domains this snapshot
// holds. With TASHandleOverlappingFlavors, usages can cover far more domains
// than the flavor selects, so it walks whichever side is smaller.
func (s *TASFlavorSnapshot) addTASUsageForHeldDomains(usages map[utiltas.TopologyDomainID]resources.Requests) {
	if len(s.usageDomains()) < len(usages) {
		for domainID := range s.usageDomains() {
			if usage, found := usages[domainID]; found {
				s.addTASUsage(domainID, usage)
			}
		}
		return
	}
	for domainID, usage := range usages {
		if s.hasDomain(domainID) {
			s.addTASUsage(domainID, usage)
		}
	}
}

// assumedUsageForLeaf returns the usage this cycle already placed on the leaf.
// Without a virtual hostname level the leaf is the domain a TopologyAssignment
// names, so the domain-keyed side holds it.
func (s *TASFlavorSnapshot) assumedUsageForLeaf(usage *assumedUsage, leaf *leafDomain) resources.Requests {
	if s.virtualHostname {
		return usage.perLeaf[leaf.id]
	}
	return usage.perDomain[leaf.id]
}

// leavesOf yields the leaves dom holds: dom itself when the topology declares
// the hostname level and dom is therefore a leaf, otherwise its children.
func (s *TASFlavorSnapshot) leavesOf(dom *domain) iter.Seq[*leafDomain] {
	return func(yield func(*leafDomain) bool) {
		if len(dom.children) == 0 {
			if leaf := s.leaves[dom.id]; leaf != nil {
				yield(leaf)
			}
			return
		}
		for _, child := range dom.children {
			if leaf := s.leaves[child.id]; leaf != nil && !yield(leaf) {
				return
			}
		}
	}
}

// domainFreeCapacityOf returns the summed free capacity of the domain's leaves.
func (s *TASFlavorSnapshot) domainFreeCapacityOf(dom *domain) resources.Requests {
	if free, found := s.domainFreeCapacities[dom.id]; found {
		return free
	}
	free := resources.NewRequests()
	for leaf := range s.leavesOf(dom) {
		free.Add(s.leafCapacityOf(leaf).freeCapacity)
	}
	s.domainFreeCapacities[dom.id] = free
	return free
}

// domainRemainingCapacity is remainingCapacityForLeaf at the level usage is
// recorded against, which the leaves never see. See domainTASUsage for why.
func (s *TASFlavorSnapshot) domainRemainingCapacity(dom *domain, assumedUsage resources.Requests, simulateEmpty bool) resources.LazyRequests {
	remaining := resources.NewLazyRequests(s.domainFreeCapacityOf(dom))
	if !simulateEmpty {
		remaining.Sub(s.domainTASUsage[dom.id])
	}
	// Placements made earlier in this cycle are always subtracted: they belong to
	// the Workload being assigned, so preemption cannot reclaim them. fillLeafCounts
	// subtracts the leaf-keyed side the same way.
	remaining.Sub(assumedUsage)
	return remaining
}

// updateTASUsageForHeldDomains applies the requests only to the domains this
// snapshot has a leaf for. An overlapping flavor holds only some of them, so an
// unheld domain must not reach the skip report in addTASUsage and
// removeTASUsage, which means the backing node went away.
func (s *TASFlavorSnapshot) updateTASUsageForHeldDomains(usage workload.TASFlavorUsage, op usageOp) {
	for _, tr := range usage {
		domainID := utiltas.DomainID(tr.Values)
		if !s.hasDomain(domainID) {
			continue
		}
		s.updateTASUsage(domainID, tr.TotalRequests(), op, tr.Count)
	}
}

func (s *TASFlavorSnapshot) addTASUsage(domainID utiltas.TopologyDomainID, usage resources.Requests) {
	s.applyTASUsage(domainID, usage, add)
}

func (s *TASFlavorSnapshot) removeTASUsage(domainID utiltas.TopologyDomainID, usage resources.Requests) {
	s.applyTASUsage(domainID, usage, subtract)
}

// applyTASUsage records usage against the domain the TopologyAssignment names,
// as resolved by usageDomain.
func (s *TASFlavorSnapshot) applyTASUsage(domainID utiltas.TopologyDomainID, usage resources.Requests, op usageOp) {
	dom := s.usageDomain(domainID)
	if dom == nil {
		// this can happen if there is an admitted workload for which the
		// backing node was deleted or is no longer Ready (so the addCapacity
		// function was not called).
		s.log.V(3).Info("skip accounting for TAS usage in domain", "domain", domainID, "usage", usage)
		return
	}
	if s.virtualHostname {
		s.domainTASUsage[dom.id] = updateUsage(s.domainTASUsage[dom.id], usage, op)
		return
	}
	leafCapacity := s.leafCapacityOf(s.leaves[dom.id])
	leafCapacity.tasUsage = updateUsage(leafCapacity.tasUsage, usage, op)
	leafCapacity.cachedRemainingCapacity = resources.LazyRequests{}
}

// updateUsage applies op to tracked and returns it, allocating on first use.
// The result must be stored back, as tracked may have been nil.
func updateUsage(tracked, usage resources.Requests, op usageOp) resources.Requests {
	if tracked == nil {
		tracked = resources.NewRequests()
	}
	if op == add {
		tracked.Add(usage)
	} else {
		tracked.Sub(usage)
	}
	return tracked
}

type domainCapacityDetails struct {
	FreeCapacity map[corev1.ResourceName]string `json:"freeCapacity"`
	TasUsage     map[corev1.ResourceName]string `json:"tasUsage"`
}

func (s *TASFlavorSnapshot) resourceDetails(requests resources.Requests) map[corev1.ResourceName]string {
	if requests == nil {
		// A leaf keeps its requests nil until the first update, so a domain with
		// capacity, but without admitted TAS workloads, has a nil tasUsage.
		return map[corev1.ResourceName]string{}
	}
	details := make(map[corev1.ResourceName]string, requests.Len())
	requests.ForEach(func(resourceName corev1.ResourceName, value int64) {
		details[resourceName] = s.resourceFormatter.ResourceQuantityString(resourceName, value)
	})
	return details
}

func (s *TASFlavorSnapshot) SerializeFreeCapacityPerDomain() (string, error) {
	details := make(map[utiltas.TopologyDomainID]domainCapacityDetails, len(s.leaves))

	for domainID, leaf := range s.leaves {
		leafCapacity := s.leafCapacityOf(leaf)
		details[domainID] = domainCapacityDetails{
			FreeCapacity: s.resourceDetails(leafCapacity.freeCapacity),
			TasUsage:     s.resourceDetails(leafCapacity.tasUsage),
		}
	}
	// A virtual topology records usage on the domains, not the leaves, so
	// without this the dump reads as if nothing is used.
	if s.virtualHostname {
		for domainID, dom := range s.usageDomains() {
			details[domainID] = domainCapacityDetails{
				FreeCapacity: s.resourceDetails(s.domainFreeCapacityOf(dom)),
				TasUsage:     s.resourceDetails(s.domainTASUsage[domainID]),
			}
		}
	}

	jsonBytes, err := json.Marshal(details)
	if err != nil {
		return "", err
	}

	return string(jsonBytes), nil
}

type TASPodSetRequests struct {
	PodSet            *kueue.PodSet
	PodSetUpdates     []*kueue.PodSetUpdate
	SinglePodRequests resources.Requests
	// DRADelegation is nil when the PodSet requests no DRA-backed extended resource.
	DRADelegation   *DRADelegation
	Count           int32
	Flavor          kueue.ResourceFlavorReference
	Implied         bool
	PodSetGroupName *string
	// PreviousAssignment holds the topology assignment from a workload slice
	// that this workload is replacing.
	PreviousAssignment *kueue.TopologyAssignment
}

func (t *TASPodSetRequests) TotalRequests() resources.Requests {
	return t.SinglePodRequests.ScaledUp(int64(t.Count))
}

type FailureInfo struct {
	// PodSetName indicates the name of the PodSet for which computing the
	// TAS assignment failed.
	PodSetName kueue.PodSetReference

	// Reason indicates the reason why computing the TAS assignment failed.
	Reason string

	// Flavor indicates the resource flavor associated with the failure.
	Flavor kueue.ResourceFlavorReference
}

type TASAssignmentsResult map[kueue.PodSetReference]tasPodSetAssignmentResult

func (r TASAssignmentsResult) Failure() *FailureInfo {
	for psName, psAssignment := range r {
		if psAssignment.FailureReason != "" {
			return &FailureInfo{
				PodSetName: psName,
				Reason:     psAssignment.FailureReason,
				Flavor:     psAssignment.Flavor,
			}
		}
	}
	return nil
}

type tasPodSetAssignmentResult struct {
	TopologyAssignment *utiltas.TopologyAssignment
	FailureReason      string
	Flavor             kueue.ResourceFlavorReference
}

type FlavorTASRequests []TASPodSetRequests

// Fits checks if the snapshot has enough capacity to accommodate the workload
func (s *TASFlavorSnapshot) Fits(flavorUsage workload.TASFlavorUsage) bool {
	cachingEnabled := features.Enabled(features.TASCachingRemainingResources)
	for _, domainUsage := range flavorUsage {
		dom := s.usageDomain(utiltas.DomainID(domainUsage.Values))
		if dom == nil {
			return false
		}
		var fitCount int32
		for leaf := range s.leavesOf(dom) {
			remainingCapacity := s.remainingCapacityForLeaf(leaf, false, cachingEnabled)
			fitCount += domainUsage.SinglePodRequests.CountIn(remainingCapacity.Get())
			if fitCount >= domainUsage.Count {
				break
			}
		}
		if s.virtualHostname {
			remaining := s.domainRemainingCapacity(dom, nil, false)
			fitCount = min(fitCount, domainUsage.SinglePodRequests.CountIn(remaining.Get()))
		}
		if fitCount < domainUsage.Count {
			return false
		}
	}
	return true
}

type findTopologyAssignmentsOption struct {
	simulateEmpty          bool
	workload               *workload.Info
	aggregatedDomainUsages map[utiltas.TopologyDomainID]resources.Requests
	topologySpreadCounts   PodSetGroupNameToTreeCount
}

type tasExclusionStats struct {
	simulator.NodeExclusionStats
	TopologyDomain int
	Resources      map[corev1.ResourceName]int
}

type topologyAssignmentPodRequirements struct {
	podRequests
	podRequirements           simulator.PodRequirements
	leader                    *leaderRequirements
	assumedUsage              *assumedUsage
	requiredReplacementDomain utiltas.TopologyDomainID
	matchKey                  *podSetMatchKey
}

// leaderRequirements is what TAS needs to place the leader Pod of a PodSet group.
type leaderRequirements struct {
	// podRequests covers one leader Pod, its Pod count included.
	podRequests
	// podRequirements are the leader's own node filters, applied on top of the
	// workers' when choosing its domain. Nil when TASLeaderPodSetFeasibility is off.
	podRequirements *simulator.PodRequirements
}

// sliceShape describes how a PodSet is cut into slices.
//
// It is passed to the placement helpers as a single value rather than as a
// loose int32, so that it cannot be transposed with the neighbouring counts and
// so that the figures describing the cut stay together. The two always travel
// together: every capacity that accounts for the partial slice is expressed
// in whole slices of size, with tailSize pods charged on top.
type sliceShape struct {
	// size is the number of pods in a whole slice. It is 1 when slices are not
	// requested, in which case there is no partial slice either.
	size int32
	// tailSize is the number of pods in the partial slice, that is
	// count % size. It is zero when the count divides evenly into whole
	// slices, and whenever the feature is disabled.
	tailSize int32
}

// topologyAssignmentParameters stores placement-specific inputs that remain
// relevant after domain capacities are computed.
type topologyAssignmentParameters struct {
	sliceSizeAtLevel map[int]int32
	sliceSize        int32
	// count is the number of pods to place.
	count int32
	// tailSize is the number of pods in the partial slice, that is
	// count % sliceSize. It is zero when the count divides evenly into whole
	// slices. The placement algorithm treats the partial slice as a slice
	// that has to be held by a single domain like any other, and charges the
	// domain holding it for tailSize pods rather than for a whole slice.
	tailSize              int32
	leaderCount           int32
	requestedLevelIdx     int
	sliceLevelIdx         int
	required              bool
	unconstrained         bool
	multiLayerConstraints []kueue.PodsetSliceRequiredTopologyConstraint

	// spreadRules holds the topology-spreading rules for this PodSet group,
	// keyed by the level index they resolve to. Nil unless spreading counts
	// were supplied for the group.
	spreadRules map[int]utiltas.SpreadingRule

	// spreadCounts is the occupancy those rules are evaluated against, copied
	// into domainState by fillInCounts.
	spreadCounts *SpreadTreeCount
}

// shape returns how the PodSet is cut into slices. The two figures are always
// consumed together, see sliceShape.
func (p *topologyAssignmentParameters) shape() sliceShape {
	return sliceShape{size: p.sliceSize, tailSize: p.tailSize}
}

// findTopologyAssignmentState stores the derived state for a single run of the
// TAS placement algorithm.
type findTopologyAssignmentState struct {
	// leaderFeasibleLeaves holds the leaves the leader PodSet can run on. Nil means
	// the leader adds no restriction; empty means no leaf suits it. Not the same.
	leaderFeasibleLeaves sets.Set[utiltas.TopologyDomainID]

	topologyAssignmentParameters
	stats *tasExclusionStats
}

func (s *findTopologyAssignmentState) leaderFeasibleFor(leaf *leafDomain) bool {
	return s.leaderFeasibleLeaves == nil || s.leaderFeasibleLeaves.Has(leaf.id)
}

func newTASExclusionStats() *tasExclusionStats {
	return &tasExclusionStats{}
}

func (s *tasExclusionStats) hasExclusions() bool {
	return s.NodeSelector > 0 || s.Affinity > 0 || len(s.Taints) > 0 || s.TopologyDomain > 0 ||
		len(s.Resources) > 0 || s.SchedulerLibraryNoFit > 0 || s.DRANoFit > 0
}

func (s *tasExclusionStats) formatReasons() string {
	var reasons []string
	if s.NodeSelector > 0 {
		reasons = append(reasons, fmt.Sprintf("nodeSelector: %d", s.NodeSelector))
	}
	if s.Affinity > 0 {
		reasons = append(reasons, fmt.Sprintf("affinity: %d", s.Affinity))
	}
	if s.TopologyDomain > 0 {
		reasons = append(reasons, fmt.Sprintf("topologyDomain: %d", s.TopologyDomain))
	}
	if s.SchedulerLibraryNoFit > 0 {
		reasons = append(reasons, fmt.Sprintf("schedulerLibraryNoFit: %d", s.SchedulerLibraryNoFit))
	}
	if s.DRANoFit > 0 {
		reasons = append(reasons, fmt.Sprintf("draNoFit: %d", s.DRANoFit))
	}
	for _, taint := range slices.Sorted(maps.Keys(s.Taints)) {
		reasons = append(reasons, fmt.Sprintf("taint %q: %d", taint, s.Taints[taint]))
	}
	for _, resource := range slices.Sorted(maps.Keys(s.Resources)) {
		reasons = append(reasons, fmt.Sprintf("resource %q: %d", resource, s.Resources[resource]))
	}
	slices.Sort(reasons)
	return strings.Join(reasons, ", ")
}

func (s *tasExclusionStats) recordResourceExclusion(res corev1.ResourceName) {
	if s.Resources == nil {
		s.Resources = make(map[corev1.ResourceName]int)
	}
	s.Resources[res]++
}

func (s *tasExclusionStats) add(other *tasExclusionStats) {
	s.TotalNodes += other.TotalNodes
	s.NodeSelector += other.NodeSelector
	s.Affinity += other.Affinity
	s.TopologyDomain += other.TopologyDomain
	s.SchedulerLibraryNoFit += other.SchedulerLibraryNoFit
	s.DRANoFit += other.DRANoFit
	for k, v := range other.Taints {
		if s.Taints == nil {
			s.Taints = make(map[string]int)
		}
		s.Taints[k] += v
	}
	for k, v := range other.Resources {
		if s.Resources == nil {
			s.Resources = make(map[corev1.ResourceName]int)
		}
		s.Resources[k] += v
	}
}

type FindTopologyAssignmentsOption func(*findTopologyAssignmentsOption)

// WithSimulateEmpty sets parameter allows to look for the assignment under the
// assumption that all TAS workloads are preempted.
func WithSimulateEmpty(simulateEmpty bool) FindTopologyAssignmentsOption {
	return func(o *findTopologyAssignmentsOption) {
		o.simulateEmpty = simulateEmpty
	}
}

func WithWorkloadInfo(wl *workload.Info) FindTopologyAssignmentsOption {
	return func(o *findTopologyAssignmentsOption) {
		o.workload = wl
	}
}

// WithAggregatedDomainUsages supplies a cross-flavor assumedUsage so that
// per-PodSet TAS placements within a single workload account for reservations
// already made in sibling flavors sharing the same Topology (hostname leaf).
func WithAggregatedDomainUsages(m map[utiltas.TopologyDomainID]resources.Requests) FindTopologyAssignmentsOption {
	return func(o *findTopologyAssignmentsOption) {
		o.aggregatedDomainUsages = m
	}
}

// WithTopologySpreadCounts supplies the Workload counts for each PodSet group
// and topology domain in this flavor.
func WithTopologySpreadCounts(counts PodSetGroupNameToTreeCount) FindTopologyAssignmentsOption {
	return func(o *findTopologyAssignmentsOption) {
		o.topologySpreadCounts = counts
	}
}

// FindTopologyAssignmentsForFlavor returns TAS assignment, if possible, for all
// the TAS requests in the flavor handled by the snapshot.
func (s *TASFlavorSnapshot) FindTopologyAssignmentsForFlavor(ctx context.Context, flavorTASRequests FlavorTASRequests, options ...FindTopologyAssignmentsOption) TASAssignmentsResult {
	log := log.FromContext(ctx)
	opts := &findTopologyAssignmentsOption{}
	for _, option := range options {
		option(opts)
	}

	result := make(map[kueue.PodSetReference]tasPodSetAssignmentResult)
	var sharedDomainUsages map[utiltas.TopologyDomainID]resources.Requests
	if features.Enabled(features.TASHandleOverlappingFlavors) {
		sharedDomainUsages = opts.aggregatedDomainUsages
	}
	assumedUsage := newAssumedUsage(sharedDomainUsages)

	// opts.workload is unset on some call paths (e.g. simulateEmpty probing).
	var wlObj *kueue.Workload
	if opts.workload != nil {
		wlObj = opts.workload.Obj
	}

	groupedTASRequests := make(map[utiltas.PodSetGroupKey]FlavorTASRequests)
	groupsOrder := make([]utiltas.PodSetGroupKey, 0)

	for _, tr := range flavorTASRequests {
		groupKey := utiltas.GroupKeyForPodSet(tr.PodSet)

		if !slices.Contains(groupsOrder, groupKey) {
			groupsOrder = append(groupsOrder, groupKey)
		}
		groupedTASRequests[groupKey] = append(groupedTASRequests[groupKey], tr)
	}

	for _, groupKey := range groupsOrder {
		podSetGroupCounts := opts.topologySpreadCounts[groupKey]
		trs := groupedTASRequests[groupKey]
		// Without an admission there is nothing to replace; take the fresh-placement path.
		if workload.HasUnhealthyNodes(wlObj) && wlObj.Status.Admission != nil {
			for _, tr := range trs {
				// In case of looking for Node replacement, TopologyRequest has only
				// PodSets with the Node to replace, so we match PodSetAssignment
				psa := findPSA(wlObj, tr.PodSet.Name)
				if psa == nil || psa.TopologyAssignment == nil {
					continue
				}
				if features.Enabled(features.SkipReassignmentForPodOwnedWorkloads) && workload.OwnedBySinglePod(wlObj) {
					// The pod cannot relocate and the Workload cannot outlive it; keep
					// the existing assignment so admit clears UnhealthyNodes without
					// diverging from the node the pod actually runs on.
					result[tr.PodSet.Name] = tasPodSetAssignmentResult{TopologyAssignment: utiltas.InternalFrom(psa.TopologyAssignment)}
					continue
				}
				// We deepCopy the existing TopologyAssignment, so if we delete unwanted domain,
				// And there is no fit, we have the original newAssignment to retry with
				existingAssignment := psa.TopologyAssignment
				newAssignment, replacementAssignment, reason := s.findReplacementAssignment(ctx, &tr, utiltas.InternalFrom(existingAssignment), opts.workload, assumedUsage)
				result[tr.PodSet.Name] = tasPodSetAssignmentResult{TopologyAssignment: newAssignment, FailureReason: reason}
				if reason != "" {
					return result
				} else {
					log.V(3).Info("Found replacement assignment for workload", "existingAssignment", existingAssignment, "newAssignment", newAssignment)
				}
				addAssumedUsage(assumedUsage, replacementAssignment, &tr)
			}
		} else {
			leader, workers := findLeaderAndWorkers(trs)

			if features.Enabled(features.ElasticJobsViaWorkloadSlicesWithTAS) {
				elasticResult := s.handleElasticWorkload(ctx, workers, leader, assumedUsage, opts)
				if elasticResult.applied {
					maps.Copy(result, elasticResult.assignments)
					if elasticResult.assignments[workers.PodSet.Name].FailureReason != "" {
						return result
					}
					continue
				}
			}

			// Normal path: no previous assignment or stale assignment
			assignments, leafAssignments, reason := s.findTopologyAssignment(ctx, workers, leader, assumedUsage, opts.simulateEmpty, "", opts.workload, podSetGroupCounts)
			for _, tr := range trs {
				podSetName := tr.PodSet.Name
				result[podSetName] = tasPodSetAssignmentResult{TopologyAssignment: assignments[podSetName], FailureReason: reason}
			}

			if reason != "" {
				return result
			}
			for _, tr := range trs {
				addAssumedUsageForCycle(assumedUsage, assignments[tr.PodSet.Name], leafAssignments[tr.PodSet.Name], &tr)
			}
		}
	}

	return result
}

func findLeaderAndWorkers(trs FlavorTASRequests) (*TASPodSetRequests, TASPodSetRequests) {
	var leader *TASPodSetRequests = nil

	workers := trs[0]
	if len(trs) > 1 {
		leader = &trs[1]

		if leader.Count > workers.Count {
			leader = &trs[0]
			workers = trs[1]
		}
	}
	return leader, workers
}

// findReplacementAssignment finds the topology assignment for the replacement node
// it return new corrected topologyAssignment, a replacement topologyAssignment used to patched the old, faulty one, and
// reason if finding fails.
// It is only reachable for workloads with hostname-level assignments, as
// UnhealthyNodes is populated behind an IsLowestLevelHostname gate; the
// assignment values here and in its helpers are therefore node-scoped even
// when the topology has a virtual hostname level.
func (s *TASFlavorSnapshot) findReplacementAssignment(
	ctx context.Context,
	tr *TASPodSetRequests,
	existingAssignment *utiltas.TopologyAssignment,
	wl *workload.Info,
	assumedUsage *assumedUsage,
) (*utiltas.TopologyAssignment, *utiltas.TopologyAssignment, string) {
	tr.Count = deleteDomain(existingAssignment, wl.Obj.Status.UnhealthyNodes[0].Name)
	if isStale, staleDomain := s.IsTopologyAssignmentStale(existingAssignment); isStale {
		return nil, nil, fmt.Sprintf("Cannot replace the node, because the existing topologyAssignment is invalid, as it contains the stale domain %v", staleDomain)
	}
	requiredReplacementDomain := s.requiredReplacementDomain(tr, existingAssignment)
	trCopy := *tr
	sliceSize, reason := getSliceSizeWithSinglePodAsDefault(tr.PodSet.TopologyRequest)
	if reason != "" {
		return nil, nil, reason
	}
	if slicesRequested(tr.PodSet.TopologyRequest) && requiredReplacementDomain != "" && (tr.Count%sliceSize != 0) {
		trCopy.PodSet = tr.PodSet.DeepCopy()
		// Find the innermost constraint whose size divides the number of replacement
		// pods to preserve leaf-level grouping
		effectiveSliceSize := int32(1)
		var effectiveSliceTopology *string
		constraints := utiltas.PodSetSliceRequiredTopologyConstraints(tr.PodSet.TopologyRequest)
		for _, v := range slices.Backward(constraints) {
			if tr.Count%v.Size == 0 {
				effectiveSliceSize = v.Size
				effectiveSliceTopology = new(v.Topology)
				break
			}
		}
		trCopy.PodSet.TopologyRequest.PodsetSliceRequiredTopologyConstraints = nil
		// PodSetSliceSize is only read when PodSetSliceRequiredTopology is also set,
		// so both must be configured for the slice grouping to take effect.
		trCopy.PodSet.TopologyRequest.PodSetSliceRequiredTopology = effectiveSliceTopology
		trCopy.PodSet.TopologyRequest.PodSetSliceSize = new(effectiveSliceSize)
	}
	// Node replacement doesn't re-spread an already-admitted Workload, so no
	// counts are passed and a domain over its spreading limit isn't excluded.
	replacementAssignment, _, reason := s.findTopologyAssignment(ctx, trCopy, nil, assumedUsage, false, requiredReplacementDomain, wl, nil)
	if reason != "" {
		return nil, nil, reason
	}
	if replacementAssignment == nil || len(replacementAssignment[tr.PodSet.Name].Domains) == 0 {
		return nil, nil, fmt.Sprintf("cannot find replacement assignment for unhealthy node: %v", wl.Obj.Status.UnhealthyNodes[0].Name)
	}
	newAssignment := s.mergeTopologyAssignments(replacementAssignment[tr.PodSet.Name], existingAssignment)
	// Merging orders the domains by their level values, which may leave the
	// partial slice somewhere other than last.
	s.normalizeTailLast(newAssignment, tr.PodSet.TopologyRequest, sliceSize)
	if !s.assignmentSliceAligned(newAssignment, tr.PodSet.TopologyRequest, sliceSize) {
		// The repair could not keep the slices whole, which the rank-based
		// ungating relies on. Reject it and let the workload be rescheduled
		// from scratch instead of publishing a misaligned assignment.
		return nil, nil, fmt.Sprintf("cannot replace the node %v without splitting a PodSet slice", wl.Obj.Status.UnhealthyNodes[0].Name)
	}
	return newAssignment, replacementAssignment[tr.PodSet.Name], ""
}

// assumedUsage holds the usage of the placements made earlier in this
// scheduling cycle. Domain IDs are not unique across levels, so the entries
// keyed by leaf are kept apart from those keyed by the domain a
// TopologyAssignment names; a node named after a domain would otherwise share
// its entry.
type assumedUsage struct {
	// perDomain is keyed the way TopologyAssignment values are. With
	// TASHandleOverlappingFlavors it is shared with the sibling flavors, see
	// WithAggregatedDomainUsages.
	perDomain map[utiltas.TopologyDomainID]resources.Requests

	// perLeaf is keyed by leaf, and is filled only when the hostname level is
	// virtual. The leaf a Pod lands on is then known within the cycle, but is
	// not part of the published assignment. Reads of a nil map are valid, so it
	// stays nil on the topologies that never write it.
	perLeaf map[utiltas.TopologyDomainID]resources.Requests
}

// newAssumedUsage returns an assumedUsage recording domain-level usage into
// perDomain, which is shared with the sibling flavors when one is passed.
func newAssumedUsage(perDomain map[utiltas.TopologyDomainID]resources.Requests) *assumedUsage {
	if perDomain == nil {
		perDomain = make(map[utiltas.TopologyDomainID]resources.Requests)
	}
	return &assumedUsage{perDomain: perDomain}
}

// recordLeafUsage adds usage keyed by leaf, allocating on first use.
func (u *assumedUsage) recordLeafUsage(usagePerDomain map[utiltas.TopologyDomainID]resources.Requests) {
	if u.perLeaf == nil {
		u.perLeaf = make(map[utiltas.TopologyDomainID]resources.Requests, len(usagePerDomain))
	}
	addUsagePerDomain(u.perLeaf, usagePerDomain)
}

// addAssumedUsageForCycle records the usage of an assignment made in this cycle
// on both sides. A later PodSet needs the leaf-keyed entry to see the exact
// nodes taken, and the domain-keyed entry to see that the domain itself shrank:
// neither bound implies the other, because a leaf does not carry the usage
// recorded on its domain, and a domain does not know which of its nodes are
// taken.
func addAssumedUsageForCycle(assumedUsage *assumedUsage, published, leaves *utiltas.TopologyAssignment, tr *TASPodSetRequests) {
	if leaves != nil {
		assumedUsage.recordLeafUsage(utiltas.ComputeUsagePerDomain(leaves, tr.SinglePodRequests))
	}
	addAssumedUsage(assumedUsage, published, tr)
}

func addAssumedUsage(assumedUsage *assumedUsage, ta *utiltas.TopologyAssignment, tr *TASPodSetRequests) {
	addUsagePerDomain(assumedUsage.perDomain, utiltas.ComputeUsagePerDomain(ta, tr.SinglePodRequests))
}

func addUsagePerDomain(tracked map[utiltas.TopologyDomainID]resources.Requests, usagePerDomain map[utiltas.TopologyDomainID]resources.Requests) {
	for domainID, usage := range usagePerDomain {
		if tracked[domainID] == nil {
			tracked[domainID] = resources.NewRequests()
		}
		tracked[domainID].Add(usage)
	}
}

func findPSA(wl *kueue.Workload, psName kueue.PodSetReference) *kueue.PodSetAssignment {
	if wl.Status.Admission == nil {
		return nil
	}
	for _, psAssignment := range wl.Status.Admission.PodSetAssignments {
		if psAssignment.Name == psName {
			return &psAssignment
		}
	}
	return nil
}

func (s *TASFlavorSnapshot) requiredReplacementDomain(tr *TASPodSetRequests, ta *utiltas.TopologyAssignment) utiltas.TopologyDomainID {
	key := s.levelKeyWithImpliedFallback(tr)
	if key == nil {
		return ""
	}
	levelIdx, found := s.resolveLevelIdx(*key)
	if !found {
		return ""
	}

	// no domain to comply with so we don't require any domain at all
	// this happens when the faulty node was the only one in the assignment
	if len(ta.Domains) == 0 {
		return ""
	}

	sliceSize, reason := getSliceSizeWithSinglePodAsDefault(tr.PodSet.TopologyRequest)
	if reason != "" {
		return ""
	}
	if slicesRequested(tr.PodSet.TopologyRequest) && (tr.Count%sliceSize != 0) {
		// For multi-layer constraints, find the innermost broken constraint's domain.
		// This ensures the replacement is confined to the tightest topology level
		// that needs repair, preserving intermediate grouping invariants.
		constraints := utiltas.PodSetSliceRequiredTopologyConstraints(tr.PodSet.TopologyRequest)
		if len(constraints) > 1 {
			for _, v := range slices.Backward(constraints) {
				if tr.Count%v.Size != 0 {
					return s.findIncompleteSliceDomain(ta, tr.Count, v.Size, v.Topology)
				}
			}
		}
		return s.findIncompleteSliceDomain(ta, tr.Count, sliceSize, s.sliceLevelKeyWithDefault(tr.PodSet.TopologyRequest, s.explicitLowestLevel()))
	}

	if !isRequired(tr.PodSet.TopologyRequest) {
		return ""
	}

	nodeLevel := len(s.levelKeys) - 1
	domainValues := ta.Domains[0].Values
	if len(domainValues) == 0 {
		return ""
	}
	// Look up domain using full DomainID path (e.g., "b2,r1,b2-r1")
	domain, found := s.domainsPerLevel[nodeLevel][utiltas.DomainID(domainValues)]
	if !found {
		return ""
	}
	// Find a domain that complies with the required policy
	for i := nodeLevel; i > levelIdx; i-- {
		domain = domain.parent
	}
	return domain.id
}

// domainForAssignmentValues resolves the domain referenced by a serialized
// topologyAssignment entry. With a virtual hostname level the serialized
// values end one level above the leaves, so the lookup cannot use s.leaves.
func (s *TASFlavorSnapshot) domainForAssignmentValues(levels, values []string) *domain {
	if len(levels) == 0 || len(values) == 0 {
		return nil
	}
	startIdx := slices.Index(s.levelKeys, levels[0])
	if startIdx == -1 {
		return nil
	}
	levelIdx := startIdx + len(values) - 1
	if levelIdx >= len(s.domainsPerLevel) {
		return nil
	}
	return s.domainsPerLevel[levelIdx][utiltas.DomainID(values)]
}

// IsTopologyAssignmentStale indicates whether the topologyAssignment have Nodes
// that don't exists in the snapshot. It may be cause e.g. by Node deletion, or change
// in Node's NodeReady condition
func (s *TASFlavorSnapshot) IsTopologyAssignmentStale(ta *utiltas.TopologyAssignment) (bool, string) {
	for _, domain := range ta.Domains {
		if s.domainForAssignmentValues(ta.Levels, domain.Values) == nil {
			return true, domain.Values[0]
		}
	}
	return false, ""
}

// deleteDomain deletes the domain the has faulty node and returns number of affected pods by the node
func deleteDomain(currentTopologyAssignment *utiltas.TopologyAssignment, unhealthyNode string) int32 {
	var noAffectedPods int32 = 0
	updatedAssignment := make([]utiltas.TopologyDomainAssignment, 0, len(currentTopologyAssignment.Domains))
	for _, domain := range currentTopologyAssignment.Domains {
		if domain.Values[len(domain.Values)-1] == unhealthyNode {
			noAffectedPods = domain.Count
		} else {
			updatedAssignment = append(updatedAssignment, domain)
		}
	}
	currentTopologyAssignment.Domains = updatedAssignment
	return noAffectedPods
}

// sliceLevelUsage is the number of pods an assignment places in one domain at
// the slice level.
type sliceLevelUsage struct {
	domainID utiltas.TopologyDomainID
	count    int32
}

// sliceLevelUsages groups the assignment by domain at the slice level,
// preserving the order in which the domains appear in the assignment. That
// order is the lexicographic order of the level values, established by
// buildAssignment and preserved by mergeTopologyAssignments, and it is the
// order the ungater ranks pods in.
func (s *TASFlavorSnapshot) sliceLevelUsages(ta *utiltas.TopologyAssignment, sliceLevelIdx int) []sliceLevelUsage {
	var usages []sliceLevelUsage
	indexByDomain := make(map[utiltas.TopologyDomainID]int)

	for _, domainFromAssignment := range ta.Domains {
		domain := s.sliceLevelDomain(ta.Levels, domainFromAssignment.Values, sliceLevelIdx)
		if domain == nil {
			continue
		}
		if idx, seen := indexByDomain[domain.id]; seen {
			usages[idx].count += domainFromAssignment.Count
			continue
		}
		indexByDomain[domain.id] = len(usages)
		usages = append(usages, sliceLevelUsage{domainID: domain.id, count: domainFromAssignment.Count})
	}
	return usages
}

// findIncompleteSliceDomain returns the domain that lost pods and now holds an
// incomplete slice, so that the replacement pods can be confined to it.
//
// A healthy assignment holds a multiple of sliceSize pods in every domain at
// the slice level, except that with partial slices enabled the last domain may
// hold the trailing pods. A single unhealthy node perturbs exactly one domain,
// so the damaged one is the domain whose pod count is restored to its expected
// residue by the missing pods.
//
// The match is unique: an undamaged domain already holds its expected residue,
// so it could only match if missingCount were a multiple of sliceSize, and the
// callers only reach this function when it is not.
func (s *TASFlavorSnapshot) findIncompleteSliceDomain(ta *utiltas.TopologyAssignment, missingCount int32, sliceSize int32, topologyKey string) utiltas.TopologyDomainID {
	// this function assumes that all assignments are at the hostname level
	sliceLevelIdx, found := s.resolveLevelIdx(topologyKey)
	if !found {
		return ""
	}

	usages := s.sliceLevelUsages(ta, sliceLevelIdx)

	// The PodSet count is recovered from the assignment itself rather than read
	// from the PodSet, whose count may have moved on, e.g. for elastic jobs.
	total := missingCount
	for _, usage := range usages {
		total += usage.count
	}
	tailResidue := int32(0)
	if features.Enabled(features.TASPartialSlices) {
		tailResidue = total % sliceSize
	}

	for i, usage := range usages {
		expected := int32(0)
		if i == len(usages)-1 {
			// Only the last domain may hold a partial slice. When the
			// domain that held it lost all of its pods, this is the domain
			// that takes over as the last one, and confining the replacement
			// to it restores the invariant.
			expected = tailResidue
		}
		if (usage.count+missingCount)%sliceSize == expected {
			return usage.domainID
		}
	}
	return ""
}

// Algorithm overview:
// Phase 1:
//
//	determine pod counts and slice count for each topology domain. Start at the lowest level
//	and bubble up the numbers to the top level
//
// Phase 2:
//
//	a) sort domains using chosen strategy (i.e. starting from the highest free capacity)
//	b) select consecutive domains at requested level that can fit the workload
//	c) traverse the structure down level-by-level optimizing the number of used
//	domains at each level
//	d) build the assignment for the lowest level in the hierarchy
func (s *TASFlavorSnapshot) findTopologyAssignment(
	ctx context.Context,
	workersTasPodSetRequests TASPodSetRequests,
	leaderTasPodSetRequests *TASPodSetRequests,
	assumedUsage *assumedUsage,
	simulateEmpty bool,
	requiredReplacementDomain utiltas.TopologyDomainID,
	wl *workload.Info,
	podSetGroupCounts *SpreadTreeCount,
) (assignments, leafAssignments map[kueue.PodSetReference]*utiltas.TopologyAssignment, reason string) {
	requirements := &topologyAssignmentPodRequirements{
		podRequirements:           simulator.PodRequirements{SimulateEmpty: simulateEmpty},
		assumedUsage:              assumedUsage,
		requiredReplacementDomain: requiredReplacementDomain,
	}
	state := &findTopologyAssignmentState{
		count: workersTasPodSetRequests.Count,
		stats: &tasExclusionStats{},
	}
	requirements.podRequests = newPodRequests(workersTasPodSetRequests)

	if leaderTasPodSetRequests != nil {
		requirements.leader = &leaderRequirements{podRequests: newPodRequests(*leaderTasPodSetRequests)}
		// PodSet grouping validation requires the leader PodSet to have one replica.
		state.leaderCount = 1
	}

	info, reason := podSetInfo(workersTasPodSetRequests)
	if reason != "" {
		return nil, nil, reason
	}

	// If slice topology is not requested then we can assume that slice is a single pod
	sliceSize, reason := getSliceSizeWithSinglePodAsDefault(workersTasPodSetRequests.PodSet.TopologyRequest)
	if len(reason) > 0 {
		return nil, nil, reason
	}
	shape := newSliceShape(workersTasPodSetRequests.PodSet.TopologyRequest, state.count, sliceSize)
	state.sliceSize = shape.size
	state.tailSize = shape.tailSize

	state.required = isRequired(workersTasPodSetRequests.PodSet.TopologyRequest)
	state.unconstrained = isUnconstrained(workersTasPodSetRequests.PodSet.TopologyRequest, &workersTasPodSetRequests)

	topologyKey := s.levelKeyWithImpliedFallback(&workersTasPodSetRequests)
	if topologyKey == nil {
		return nil, nil, "topology level not specified"
	}
	requestedLevelIdx, found := s.resolveLevelIdx(*topologyKey)
	if !found {
		return nil, nil, fmt.Sprintf("no requested topology level: %s", *topologyKey)
	}
	state.requestedLevelIdx = requestedLevelIdx

	sliceTopologyKey := s.sliceLevelKeyWithDefault(workersTasPodSetRequests.PodSet.TopologyRequest, s.explicitLowestLevel())
	sliceLevelIdx, found := s.resolveLevelIdx(sliceTopologyKey)
	if !found {
		return nil, nil, fmt.Sprintf("no requested topology level for slices: %s", sliceTopologyKey)
	}
	state.sliceLevelIdx = sliceLevelIdx

	if state.requestedLevelIdx > state.sliceLevelIdx {
		return nil, nil, fmt.Sprintf("podset slice topology %s is above the podset topology %s", sliceTopologyKey, *topologyKey)
	}

	// Spreading only applies when the feature gate is on and counts were
	// supplied for the group (the node replacement path never supplies them).
	// Gated here, covering the whole block, so a flavor is never rejected by
	// validateSpreadingLevels while the rules themselves go unenforced.
	if features.Enabled(features.TASTopologySpreading) && podSetGroupCounts != nil && state.required {
		spec := wl.TopologySpreading[utiltas.GroupKeyForPodSet(workersTasPodSetRequests.PodSet)]
		if reason := s.validateSpreadingLevels(spec, state.requestedLevelIdx); len(reason) > 0 {
			return nil, nil, reason
		}
		state.spreadRules = s.resolveSpreadLevelRules(spec)
		state.spreadCounts = podSetGroupCounts
	}

	sliceSizeAtLevel, reason := s.buildSliceSizeAtLevel(workersTasPodSetRequests, state.sliceSize, state.sliceLevelIdx)
	if len(reason) > 0 {
		return nil, nil, reason
	}
	state.sliceSizeAtLevel = sliceSizeAtLevel

	if len(sliceSizeAtLevel) > 0 {
		state.multiLayerConstraints = utiltas.PodSetSliceRequiredTopologyConstraints(workersTasPodSetRequests.PodSet.TopologyRequest)
	}

	podRequirements, reason := s.buildPodRequirements(info, workersTasPodSetRequests.PodSet, workloadNamespace(wl))
	if reason != "" {
		return nil, nil, reason
	}
	// buildPodRequirements only knows the PodSet, so the caller's question is carried
	// over rather than overwritten.
	podRequirements.SimulateEmpty = simulateEmpty
	requirements.podRequirements = podRequirements
	if s.leafIsNode() && features.Enabled(features.TASCacheNodeMatchResults) && wl != nil && wl.Obj.UID != "" {
		requirements.matchKey = &podSetMatchKey{
			WorkloadUID: wl.Obj.UID,
			PodSetName:  string(workersTasPodSetRequests.PodSet.Name),
			// The default simulator answers both the same way, so it keeps one
			// entry for both.
			EmptyCluster: simulateEmpty && features.Enabled(features.SchedulerLibraryIntegration),
		}
	}

	if leaderTasPodSetRequests != nil && features.Enabled(features.TASLeaderPodSetFeasibility) {
		leaderInfo, reason := podSetInfo(*leaderTasPodSetRequests)
		if reason != "" {
			return nil, nil, reason
		}
		leaderPodRequirements, reason := s.buildPodRequirements(leaderInfo, leaderTasPodSetRequests.PodSet, workloadNamespace(wl))
		if reason != "" {
			return nil, nil, reason
		}
		leaderPodRequirements.SimulateEmpty = simulateEmpty
		requirements.leader.podRequirements = &leaderPodRequirements
	}

	// phase 1 - determine the number of pods and slices which can fit in each topology domain
	err := s.fillInCounts(ctx, requirements, state)
	if err != nil {
		return nil, nil, fmt.Sprintf("unable to calculate domain capacities for PodSet %s, error: %s", info.Name, err.Error())
	}

	// phase 2a: determine the level at which the assignment is done along with
	// the domains which can accommodate all pods/slices
	var currFitDomain []*domain
	var fitLevelIdx int
	var useBalancedPlacement bool

	// TODO: teach balanced placement about the partial slice. It
	// distributes whole slices only, so until then a PodSet that has one falls
	// back to the default path below, which places the partial slice for
	// what it is.
	if features.Enabled(features.TASBalancedPlacement) && !state.required && !state.unconstrained && !state.shape().hasTail() {
		var bestThreshold int32
		currFitDomain, bestThreshold = findBestDomainsForBalancedPlacement(s, &state.topologyAssignmentParameters)
		useBalancedPlacement = bestThreshold > 0
		if useBalancedPlacement {
			currFitDomain, fitLevelIdx, reason = applyBalancedPlacementAlgorithm(s, &state.topologyAssignmentParameters, bestThreshold, currFitDomain)
			if len(reason) > 0 {
				s.log.V(3).Info("Balanced placement algorithm failed, falling back to Best Fit", "reason", reason)
				useBalancedPlacement = false
			}
		}
	}

	if !useBalancedPlacement {
		fitLevelIdx, currFitDomain, reason = s.findLevelWithFitDomains(state.requestedLevelIdx, state)
		if len(reason) > 0 {
			return nil, nil, reason
		}
	}
	// phase 2b: traverse the tree down level-by-level optimizing the number of
	// topology domains at each level
	// if unconstrained is set, we'll only do it once
	currFitDomain = s.updateCountsToMinimumGeneric(currFitDomain, state.count, state.leaderCount, state.shape(), state.unconstrained, true)
	currentLevelIdx := fitLevelIdx
	for ; currentLevelIdx < min(len(s.domainsPerLevel)-1, state.sliceLevelIdx) && !useBalancedPlacement; currentLevelIdx++ {
		// If we are "above" the requested slice topology level and we don't run the balanced placement algorithm,
		// we're greedily assigning pods/slices to all domains without checking what we've assigned to parent domains.
		lowerDomains := s.lowerLevelDomains(currFitDomain)
		lowerDomains = s.filterOutBannedDomains(lowerDomains, state.spreadRules)
		sortedLowerDomains := s.sortedDomains(lowerDomains, state.unconstrained, state.spreadRules)
		currFitDomain = s.updateCountsToMinimumGeneric(sortedLowerDomains, state.count, state.leaderCount, state.shape(), state.unconstrained, true)
	}

	for ; currentLevelIdx < len(s.domainsPerLevel)-1; currentLevelIdx++ {
		// If we are "at" or "below" the requested slice topology level or we run the balanced placement algorithm
		// we have to carefully assign pods to domains based on what we've assigned to parent domains,
		// that's why we're iterating through each parent domain and assigning `domain.podCount` amount of pods
		// to its child domains.
		sliceSizeOnLevel := state.sliceSize
		if currentLevelIdx >= state.sliceLevelIdx {
			// Default to 1 (individual pod assignment) below the outermost
			// slice level, unless an additional slice layer specifies a
			// different size at this level.
			sliceSizeOnLevel = 1
			if sz, ok := state.sliceSizeAtLevel[currentLevelIdx+1]; ok {
				sliceSizeOnLevel = sz
			}
		}
		newCurrFitDomain := make([]*domain, 0)
		for _, domain := range currFitDomain {
			children := s.filterOutBannedDomains(domain.children, state.spreadRules)
			sortedLowerDomains := s.sortedDomains(children, state.unconstrained, state.spreadRules)

			if sliceSizeOnLevel > 1 {
				// For inner slice layers, recompute sliceCount on the
				// child domains based on the current inner slice size.
				// The pre-populated sliceCount was computed for the
				// outermost slice level and is not valid here.
				for _, d := range sortedLowerDomains {
					domainState := s.domainStateOf(d)
					domainState.sliceCount = domainState.podCount / sliceSizeOnLevel
					if state.leaderCount > 0 {
						domainState.sliceCountWithLeader = domainState.podCountWithLeader / sliceSizeOnLevel
					}
				}
			}

			domainState := s.domainStateOf(domain)
			// The pod count of a domain holding the partial slice is not a
			// multiple of the slice size, and the pods below the slice level
			// are distributed one by one, so the partial slice needs no
			// further tracking here.
			addCurrFitDomain := s.updateCountsToMinimumGeneric(
				sortedLowerDomains,
				domainState.podCount,
				domainState.leaderCount,
				sliceShape{size: sliceSizeOnLevel},
				state.unconstrained,
				sliceSizeOnLevel > 1,
			)
			newCurrFitDomain = append(newCurrFitDomain, addCurrFitDomain...)
		}
		currFitDomain = newCurrFitDomain
	}

	assignments = make(map[kueue.PodSetReference]*utiltas.TopologyAssignment)
	leafAssignments = make(map[kueue.PodSetReference]*utiltas.TopologyAssignment)

	if leaderTasPodSetRequests != nil {
		var leaderFitDomains []*domain
		var workerFitDomains []*domain
		for _, domain := range currFitDomain {
			// select domains with leaders
			if leaderCount := s.domainStateOf(domain).leaderCount; leaderCount > 0 {
				copiedDomain := s.shallowCloneWithState(domain)
				s.domainStateOf(copiedDomain).podCount = leaderCount
				leaderFitDomains = append(leaderFitDomains, copiedDomain)
			}

			// select domains with workers
			if s.domainStateOf(domain).podCount > 0 {
				workerFitDomains = append(workerFitDomains, domain)
			}
		}

		assignments[leaderTasPodSetRequests.PodSet.Name], leafAssignments[leaderTasPodSetRequests.PodSet.Name] = s.buildAssignment(leaderFitDomains)
		currFitDomain = workerFitDomains
	}

	workerPodSetName := workersTasPodSetRequests.PodSet.Name
	assignments[workerPodSetName], leafAssignments[workerPodSetName] = s.buildAssignment(currFitDomain)

	if state.tailSize > 0 {
		workerAssignment := assignments[workerPodSetName]
		topologyRequest := workersTasPodSetRequests.PodSet.TopologyRequest
		// The partial slice may have landed in any of the domains, while the
		// ungater expects it last in the published order.
		s.normalizeTailLast(workerAssignment, topologyRequest, state.sliceSize)
		if utiltas.CountPodsInAssignment(workerAssignment) != state.count || !s.assignmentSliceAligned(workerAssignment, topologyRequest, state.sliceSize) {
			// Defensive: the domains were selected knowing where the partial
			// slice would go, so this means the descent disagreed with that
			// choice. Reject the placement rather than publish an assignment
			// the ungater would read as splitting a slice across domains.
			return nil, nil, fmt.Sprintf("cannot place the partial slice of PodSet %s in a single topology domain", workerPodSetName)
		}
	}

	return assignments, leafAssignments, ""
}

// buildSliceSizeAtLevel builds a map from topology level index to the slice
// size used when distributing pods at that level, for multi-layer topology
// support.
//
// The outermost constraint layer (index 0 in PodsetSliceRequiredTopologyConstraints)
// is already handled by the caller as sliceSize/sliceLevelIdx, so this method
// processes the remaining (inner) layers. For each inner layer it:
//  1. Resolves the topology key to a level index and checks it is strictly
//     finer-grained than the previous layer.
//  2. Verifies the parent layer's size is evenly divisible by this layer's size,
//     so pods group cleanly at every level.
//  3. Fills all intermediate levels between the previous and current layer with
//     this layer's size, ensuring that intermediate levels also distribute in
//     multiples of the inner layer's size.
func (s *TASFlavorSnapshot) buildSliceSizeAtLevel(
	workersTasPodSetRequests TASPodSetRequests,
	sliceSize int32,
	sliceLevelIdx int,
) (map[int]int32, string) {
	sliceSizeAtLevel := make(map[int]int32)
	if workersTasPodSetRequests.PodSet.TopologyRequest == nil {
		return sliceSizeAtLevel, ""
	}

	prevSize := sliceSize
	prevLevelIdx := sliceLevelIdx

	// Skip the first (outermost) constraint layer — it is already represented
	// by sliceSize / sliceLevelIdx which the caller resolved from the annotation.
	// Process only the inner layers that introduce additional grouping.
	innerLayers := utiltas.PodSetSliceRequiredTopologyConstraints(workersTasPodSetRequests.PodSet.TopologyRequest)
	if len(innerLayers) > 1 {
		innerLayers = innerLayers[1:]
	} else {
		innerLayers = nil
	}

	for _, layer := range innerLayers {
		innerLevelIdx, innerFound := s.resolveLevelIdx(layer.Topology)
		if !innerFound {
			return nil, fmt.Sprintf("no requested topology level for additional slice layer: %s", layer.Topology)
		}
		if innerLevelIdx <= prevLevelIdx {
			return nil, fmt.Sprintf("additional slice layer topology %s must be at a lower level than %s", layer.Topology, s.levelKeys[prevLevelIdx])
		}
		if prevSize%layer.Size != 0 {
			return nil, fmt.Sprintf("additional slice layer size %d must evenly divide parent layer size %d", layer.Size, prevSize)
		}
		// Fill all levels from prevLevelIdx+1 through innerLevelIdx
		// so that intermediate levels also distribute in multiples
		// of this layer's size.
		for lvl := prevLevelIdx + 1; lvl <= innerLevelIdx; lvl++ {
			sliceSizeAtLevel[lvl] = layer.Size
		}
		prevSize = layer.Size
		prevLevelIdx = innerLevelIdx
	}

	return sliceSizeAtLevel, ""
}

func (s *TASFlavorSnapshot) HasLevel(r *kueue.PodSetTopologyRequest) bool {
	mainKey := s.levelKey(r)
	if mainKey == nil {
		return false
	}

	sliceKey := s.sliceLevelKeyWithDefault(r, s.explicitLowestLevel())

	_, mainTopologyFound := s.resolveLevelIdx(*mainKey)
	_, sliceTopologyFound := s.resolveLevelIdx(sliceKey)

	if !mainTopologyFound || !sliceTopologyFound {
		return false
	}

	// Also check multi-level topology constraints.
	if r != nil {
		for _, layer := range utiltas.PodSetSliceRequiredTopologyConstraints(r) {
			if _, found := s.resolveLevelIdx(layer.Topology); !found {
				return false
			}
		}
	}

	return true
}

func (s *TASFlavorSnapshot) sliceLevelKeyWithDefault(tr *kueue.PodSetTopologyRequest, defaultKey string) string {
	if constraints := utiltas.PodSetSliceRequiredTopologyConstraints(tr); len(constraints) > 0 {
		return constraints[0].Topology
	}
	return defaultKey
}

func (s *TASFlavorSnapshot) resolveLevelIdx(levelKey string) (int, bool) {
	// The injected virtual hostname level is internal only; it must not be
	// addressable by user requests on topologies which don't declare it.
	if s.virtualHostname && levelKey == corev1.LabelHostname {
		return -1, false
	}
	levelIdx := slices.Index(s.levelKeys, levelKey)
	if levelIdx == -1 {
		return levelIdx, false
	}
	return levelIdx, true
}

func (s *TASFlavorSnapshot) levelKeyWithImpliedFallback(tasRequests *TASPodSetRequests) *string {
	if key := s.levelKey(tasRequests.PodSet.TopologyRequest); key != nil {
		return key
	}
	if tasRequests.Implied {
		return new(s.explicitLowestLevel())
	}
	return nil
}

func (s *TASFlavorSnapshot) levelKey(topologyRequest *kueue.PodSetTopologyRequest) *string {
	if topologyRequest == nil {
		return nil
	}
	switch {
	case topologyRequest.Required != nil:
		return topologyRequest.Required
	case topologyRequest.Preferred != nil:
		return topologyRequest.Preferred
	case isSliceTopologyOnlyRequest(topologyRequest):
		return new(s.highestLevel())
	case ptr.Deref(topologyRequest.Unconstrained, false):
		return new(s.explicitLowestLevel())
	default:
		return nil
	}
}

func isRequired(tr *kueue.PodSetTopologyRequest) bool {
	return tr != nil && tr.Required != nil
}

func isUnconstrained(tr *kueue.PodSetTopologyRequest, tasRequests *TASPodSetRequests) bool {
	return (tr != nil && tr.Unconstrained != nil && *tr.Unconstrained) || tasRequests.Implied || isSliceTopologyOnlyRequest(tr)
}

func isSliceTopologyOnlyRequest(tr *kueue.PodSetTopologyRequest) bool {
	if tr == nil || tr.Required != nil || tr.Preferred != nil {
		return false
	}
	return len(utiltas.PodSetSliceRequiredTopologyConstraints(tr)) > 0
}

func slicesRequested(tr *kueue.PodSetTopologyRequest) bool {
	return len(utiltas.PodSetSliceRequiredTopologyConstraints(tr)) > 0
}

func getSliceSizeWithSinglePodAsDefault(tr *kueue.PodSetTopologyRequest) (int32, string) {
	constraints := utiltas.PodSetSliceRequiredTopologyConstraints(tr)
	if len(constraints) == 0 {
		return 1, ""
	}
	size := constraints[0].Size
	if size <= 0 {
		return 0, "slice topology requested, but slice size not provided"
	}
	return size, ""
}

// findBestFitDomain returns the first domain with the smallest podCount that is
// greater than or equal to count.
// When leaders are requested, domains that cannot fit them are ignored.
// If no domain fits, it returns the first domain to preserve the caller's
// established ordering.
func (s *TASFlavorSnapshot) findBestFitDomain(domains []*domain, count int32, leaderCount int32) *domain {
	countForDomain := func(d *domain) int32 {
		return s.domainStateOf(d).podCount
	}
	if leaderCount > 0 {
		countForDomain = func(d *domain) int32 {
			return s.domainStateOf(d).podCountWithLeader
		}
	}
	return s.findBestFitDomainBy(domains, count, countForDomain, leaderCount)
}

// findBestFitDomainForSlices returns the first domain with the smallest
// slice count that is greater than or equal to sliceCount.
// When leaders are requested, domains that cannot fit them are ignored.
// If no domain fits, it returns the first domain to preserve the caller's
// established ordering.
func (s *TASFlavorSnapshot) findBestFitDomainForSlices(domains []*domain, sliceCount int32, leaderCount int32) *domain {
	countForDomain := func(d *domain) int32 {
		return s.domainStateOf(d).sliceCount
	}
	if leaderCount > 0 {
		countForDomain = func(d *domain) int32 {
			return s.domainStateOf(d).sliceCountWithLeader
		}
	}
	return s.findBestFitDomainBy(domains, sliceCount, countForDomain, leaderCount)
}

type domainCountFunc func(d *domain) int32

func (s *TASFlavorSnapshot) findBestFitDomainBy(domains []*domain, needed int32, countForDomain domainCountFunc, leaderCount int32) *domain {
	candidates := s.topAffinityTierDomains(domains)
	bestDomain := candidates[0]
	bestDomainCount := int32(math.MaxInt32)
	found := false

	for _, domain := range candidates {
		if leaderCount > 0 && s.domainStateOf(domain).leaderCount < leaderCount {
			continue
		}
		domainCount := countForDomain(domain)

		if domainCount >= needed && domainCount < bestDomainCount {
			// choose the first occurrence of fitting domains
			// to make it consecutive with other podSet's
			bestDomain = domain
			bestDomainCount = domainCount
			found = true
		}
	}
	if !found {
		return candidates[0]
	}
	return bestDomain
}

// findLevelWithFitDomains finds the highest-priority set of domains at or
// above the searched level that can accommodate the requested slices and
// leaders.
//
// A PodSet whose count is not a multiple of the slice size also has a
// partial slice to place. It is not counted among the whole slices; instead
// the capacity of a domain is read as the number of whole slices it holds while
// it also holds the partial one, so that a set of domains is only selected
// when the partial slice has a home inside it.
func (s *TASFlavorSnapshot) findLevelWithFitDomains(
	searchLevelIdx int,
	state *findTopologyAssignmentState,
) (int, []*domain, string) {
	domains := s.domainsPerLevel[searchLevelIdx]
	if len(domains) == 0 {
		return 0, nil, fmt.Sprintf("no topology domains at level: %s", s.levelKeys[searchLevelIdx])
	}
	levelDomains := slices.Collect(maps.Values(domains))
	levelDomains = s.filterOutBannedDomains(levelDomains, state.spreadRules)
	if len(levelDomains) == 0 {
		return 0, nil, fmt.Sprintf("topology spreading excludes all topology domains at level: %s", s.levelKeys[searchLevelIdx])
	}
	var sortedDomain []*domain
	if state.leaderCount > 0 {
		sortedDomain = s.sortedDomainsWithLeader(levelDomains, state.unconstrained, state.spreadRules)
	} else {
		sortedDomain = s.sortedDomains(levelDomains, state.unconstrained, state.spreadRules)
	}
	topDomain := sortedDomain[0]

	sliceCount := state.count / state.sliceSize
	hasTail := state.tailSize > 0
	// requestedSliceCount counts the partial slice as a slice of its own,
	// which is how the PodSet reads in the failure messages.
	requestedSliceCount := sliceCount
	if hasTail {
		requestedSliceCount++
	}
	// The domains are sorted by the slices they hold without the partial
	// one, so the first of them is not necessarily able to hold it. The scan
	// below is what finds a domain that can, which is why it also runs when the
	// first domain does not fit the PodSet.
	if useBestFitAlgorithm(state.unconstrained) && s.domainStateOf(topDomain).leaderCount >= state.leaderCount &&
		(s.domainStateOf(topDomain).fitsSlices(sliceCount, state.leaderCount, hasTail) || hasTail) {
		// optimize the potentially last domain
		candidates := s.topSpreadTierDomains(sortedDomain, state.spreadRules)
		if hasTail {
			topDomain = s.findBestFitDomainForSlicesWithTail(candidates, sliceCount, state.leaderCount)
		} else {
			topDomain = s.findBestFitDomainForSlices(candidates, sliceCount, state.leaderCount)
		}
	}
	notFitReason := func(slicesFitCount, totalRequestsSlicesCount int32) string {
		if len(state.multiLayerConstraints) > 0 {
			return s.multiLayerNotFitMessage(searchLevelIdx, state.count, state.multiLayerConstraints, state.stats)
		}
		return s.notFitMessage(slicesFitCount, totalRequestsSlicesCount, state.sliceSize, state.stats)
	}

	if useLeastFreeCapacityAlgorithm(state.unconstrained) {
		for _, candidateDomain := range sortedDomain {
			if s.domainStateOf(candidateDomain).fitsSlices(sliceCount, state.leaderCount, hasTail) {
				return searchLevelIdx, []*domain{candidateDomain}, ""
			}
		}
		if state.required {
			maxCapacityFound := s.domainStateOf(sortedDomain[len(sortedDomain)-1]).podCount
			return 0, nil, notFitReason(maxCapacityFound, requestedSliceCount)
		}
	}
	if !s.domainStateOf(topDomain).fitsSlices(sliceCount, state.leaderCount, hasTail) {
		if state.required {
			// topDomain is the roomiest domain only while the order stays
			// capacity-descending. Preferred affinity and topology spreading
			// both reorder it, so a domain further back may still fit - scan
			// the rest before failing.
			if features.Enabled(features.TASRespectNodeAffinityPreferred) || len(state.spreadRules) > 0 {
				for i := 1; i < len(sortedDomain); i++ {
					d := sortedDomain[i]
					if s.domainStateOf(d).fitsSlices(sliceCount, state.leaderCount, hasTail) {
						candidates := s.topSpreadTierDomains(sortedDomain[i:], state.spreadRules)
						if hasTail {
							return searchLevelIdx, []*domain{s.findBestFitDomainForSlicesWithTail(candidates, sliceCount, state.leaderCount)}, ""
						}
						return searchLevelIdx, []*domain{s.findBestFitDomainForSlices(candidates, sliceCount, state.leaderCount)}, ""
					}
				}
			}
			return 0, nil, notFitReason(s.domainStateOf(topDomain).sliceCount, requestedSliceCount)
		}
		if searchLevelIdx > 0 && !state.unconstrained {
			return s.findLevelWithFitDomains(searchLevelIdx-1, state)
		}
		results := []*domain{}
		// assignedSlices[i] is what results[i] is expected to take, which the
		// partial slice has to fit next to.
		assignedSlices := []int32{}
		takesLeader := []bool{}
		remainingSliceCount := sliceCount
		remainingLeaderCount := state.leaderCount
		// Prioritize before selecting the fitting set, since later descent cannot
		// recover a feasible leader domain omitted here. updateCountsToMinimumGeneric
		// repeats this for each newly produced domain set during descent.
		sortedDomain = s.prioritizeLeaderDomain(sortedDomain, state.count, state.leaderCount, state.shape(), true)

		// Assign leaders first from a domain that preserves total worker capacity.
		// After assigning all leaders, sort the remaining domains by worker capacity
		// and assign the remaining workers.
		idx := 0
		for ; remainingLeaderCount > 0 && idx < len(sortedDomain) && s.domainStateOf(sortedDomain[idx]).leaderCount > 0; idx++ {
			domain := sortedDomain[idx]
			if useBestFitAlgorithm(state.unconstrained) && s.domainStateOf(sortedDomain[idx]).sliceCountWithLeader >= remainingSliceCount {
				// optimize the last domain
				domain = s.findBestFitDomainForSlices(s.topSpreadTierDomains(sortedDomain[idx:], state.spreadRules), remainingSliceCount, remainingLeaderCount)
			}
			results = append(results, domain)
			assignedSlices = append(assignedSlices, min(s.domainStateOf(domain).sliceCountWithLeader, remainingSliceCount))
			takesLeader = append(takesLeader, true)

			remainingLeaderCount -= s.domainStateOf(domain).leaderCount
			remainingSliceCount -= s.domainStateOf(domain).sliceCountWithLeader
		}
		if remainingLeaderCount > 0 {
			return 0, nil, notFitReason(state.leaderCount-remainingLeaderCount, requestedSliceCount)
		}

		// At this point we have assigned all leaders, so we sort remaining domains based on worker capacity
		// and assign remaining workers.
		sortedDomain = s.sortedDomains(sortedDomain[idx:], state.unconstrained, state.spreadRules)
		for idx := 0; remainingSliceCount > 0 && idx < len(sortedDomain); idx++ {
			domain := sortedDomain[idx]
			if useBestFitAlgorithm(state.unconstrained) && s.domainStateOf(sortedDomain[idx]).sliceCount >= remainingSliceCount {
				// optimize the last domain
				domain = s.findBestFitDomainForSlices(s.topSpreadTierDomains(sortedDomain[idx:], state.spreadRules), remainingSliceCount, 0)
			}
			results = append(results, domain)
			assignedSlices = append(assignedSlices, min(s.domainStateOf(domain).sliceCount, remainingSliceCount))
			takesLeader = append(takesLeader, false)

			remainingSliceCount -= s.domainStateOf(domain).sliceCount
		}
		if remainingSliceCount > 0 {
			return 0, nil, notFitReason(sliceCount-remainingSliceCount, requestedSliceCount)
		}
		if hasTail && !s.selectionHoldsTail(results, assignedSlices, takesLeader) {
			// None of the selected domains has room for the partial slice
			// next to the whole ones, so the set has to be widened by a domain
			// that can hold it. Every domain is a candidate, not only those
			// past the last one visited: best fit takes the closing domain out
			// of order, leaving earlier ones unused.
			//
			// One of them is always able to hold the partial slice when a
			// placement exists at all. A domain holding a whole slice holds the
			// partial one too, since that is smaller, so the greedy above
			// leaves a domain out only when the slices it holds are not needed.
			extra := s.firstDomainHostingTail(sortedDomain, results)
			if extra == nil {
				return 0, nil, notFitReason(sliceCount, requestedSliceCount)
			}
			results = append(results, extra)
		}
		return searchLevelIdx, results, ""
	}
	return searchLevelIdx, []*domain{topDomain}, ""
}

// topAffinityTierDomains truncates the candidate list to include only the domains
// sharing the highest affinity score present in the slice.
//
// Since candidates are already sorted by affinity score descending, this helper scans
// consecutive matches from the beginning and truncates the slice as soon as the score drops.
// This prevents the capacity-focused BestFit algorithm from optimizing across affinity tiers,
// guaranteeing that affinity scores take absolute precedence over capacity minimization.
func (s *TASFlavorSnapshot) topAffinityTierDomains(candidates []*domain) []*domain {
	if !features.Enabled(features.TASRespectNodeAffinityPreferred) || len(candidates) == 0 {
		return candidates
	}
	score := s.domainStateOf(candidates[0]).affinityScore
	for i, c := range candidates {
		if s.domainStateOf(c).affinityScore != score {
			return candidates[:i]
		}
	}
	return candidates
}

func useBestFitAlgorithm(unconstrained bool) bool {
	// following the matrix from KEP#2724
	return !useLeastFreeCapacityAlgorithm(unconstrained)
}

func useLeastFreeCapacityAlgorithm(unconstrained bool) bool {
	// following the matrix from KEP#2724
	return unconstrained && features.Enabled(features.TASProfileMixed)
}

// consumeWithLeadersGeneric handles the case when leaders still need to be assigned
// while distributing either pods or slices across domains. It updates the provided
// domain and the remaining counters accordingly and returns whether the assignment
// is complete.
//
// Parameters:
//   - domain: the domain being consumed
//   - remainingDomains: the slice of domains that are still eligible for best-fit optimization
//   - withLeader: pointer to the per-snapshot capacity with a leader present
//   - primary: pointer to the per-snapshot primary unit being distributed
//   - sliceSize: factor to set the pod count when finalizing or partially consuming
//     (use 1 for pods, the actual sliceSize for slices)
//   - slices: whether we're distributing slices (true) or pods (false)
func (s *TASFlavorSnapshot) consumeWithLeadersGeneric(
	domain *domain,
	remainingDomains []*domain,
	remainingPrimary *int32,
	remainingLeaderCount *int32,
	unconstrained bool,
	withLeader *int32,
	primary *int32,
	sliceSize int32,
	slices bool,
) (*domain, bool) {
	if useBestFitAlgorithm(unconstrained) && *withLeader >= *remainingPrimary && s.domainStateOf(domain).leaderCount >= *remainingLeaderCount {
		// optimize the last domain
		if slices {
			domain = s.findBestFitDomainForSlices(remainingDomains, *remainingPrimary, *remainingLeaderCount)
			withLeader = &s.domainStateOf(domain).sliceCountWithLeader
			primary = &s.domainStateOf(domain).sliceCount
		} else {
			domain = s.findBestFitDomain(remainingDomains, *remainingPrimary, *remainingLeaderCount)
			withLeader = &s.domainStateOf(domain).podCountWithLeader
			primary = &s.domainStateOf(domain).podCount
		}
	}

	domainState := s.domainStateOf(domain)
	if *withLeader >= *remainingPrimary && domainState.leaderCount >= *remainingLeaderCount {
		*primary = *remainingPrimary
		domainState.leaderCount = *remainingLeaderCount
		domainState.podCount = *remainingPrimary * sliceSize
		return domain, true
	}
	if *withLeader > *remainingPrimary {
		*withLeader = *remainingPrimary
	}
	if domainState.leaderCount > *remainingLeaderCount {
		domainState.leaderCount = *remainingLeaderCount
	}
	*primary = *withLeader
	domainState.podCount = *withLeader * sliceSize
	*remainingLeaderCount -= domainState.leaderCount
	*remainingPrimary -= *withLeader
	return domain, false
}

// prioritizeLeaderDomain preserves the capacity summarized by fillInCountsHelper.
// That summary subtracts the smallest eligible child leader penalty, so descent
// must select a leader-capable domain whose penalty fits within the available slack.
//
// When the shape has a partial slice, the summary subtracts the cost of
// holding the leader and the partial slice together, so the two are costed
// together here as well: a domain that is the only home left for the partial
// slice is not given the leader.
func (s *TASFlavorSnapshot) prioritizeLeaderDomain(domains []*domain, count, leaderCount int32, shape sliceShape, slicesEnabled bool) []*domain {
	if leaderCount == 0 || len(domains) < 2 {
		return domains
	}

	requiredCapacity := count
	availableCapacity := int32(0)
	if slicesEnabled {
		requiredCapacity /= shape.size
		for _, domain := range domains {
			availableCapacity += s.domainStateOf(domain).sliceCount
		}
	} else {
		for _, domain := range domains {
			availableCapacity += s.domainStateOf(domain).podCount
		}
	}

	hasTail := slicesEnabled && shape.hasTail()
	var tailCosts cheapestChild
	if hasTail {
		tailCosts = s.cheapestTailDomains(domains)
	}

	for i, domain := range domains {
		domainState := s.domainStateOf(domain)
		if domainState.leaderCount < leaderCount {
			continue
		}
		leaderPenalty := domainState.podCount - domainState.podCountWithLeader
		if slicesEnabled {
			leaderPenalty = domainState.sliceCount - domainState.sliceCountWithLeader
		}
		if hasTail {
			penalty, ok := s.leaderPenaltyWithTail(domains, &tailCosts, i)
			if !ok {
				continue
			}
			leaderPenalty = penalty
		}
		if availableCapacity-leaderPenalty < requiredCapacity {
			continue
		}
		if i == 0 {
			return domains
		}

		result := slices.Clone(domains)
		copy(result[1:i+1], result[:i])
		result[0] = domain
		return result
	}
	return domains
}

// updateCountsToMinimumGeneric distributes count over the domains, in whole
// slices or in single pods, and returns the domains it used.
//
// The shape's partial slice is placed alongside the whole ones. It is empty
// unless slices are being distributed and the PodSet count is not a multiple of
// the slice size.
func (s *TASFlavorSnapshot) updateCountsToMinimumGeneric(domains []*domain, count int32, leaderCount int32, shape sliceShape, unconstrained bool, distributeSlices bool) []*domain {
	domains = s.prioritizeLeaderDomain(domains, count, leaderCount, shape, distributeSlices)
	result := make([]*domain, 0)
	remainingPrimary := count
	if distributeSlices {
		remainingPrimary = count / shape.size
	}
	remainingLeaderCount := leaderCount
	// The partial slice is only tracked while whole slices are distributed.
	// Below the slice level the pods of the domain holding it, including its
	// own, are distributed one by one.
	tailPending := distributeSlices && shape.hasTail()
	tailHosted := false

	for i, dom := range domains {
		if tailPending && !tailHosted {
			if tailDom, ok := s.closingDomainWithTail(domains[i:], remainingPrimary, remainingLeaderCount, shape, unconstrained); ok {
				return append(result, tailDom)
			}
		}
		if remainingLeaderCount > 0 {
			var d *domain
			var completed bool
			if distributeSlices {
				d, completed = s.consumeWithLeadersGeneric(
					dom,
					domains[i:],
					&remainingPrimary,
					&remainingLeaderCount,
					unconstrained,
					&s.domainStateOf(dom).sliceCountWithLeader,
					&s.domainStateOf(dom).sliceCount,
					shape.size,
					true,
				)
			} else {
				d, completed = s.consumeWithLeadersGeneric(
					dom,
					domains[i:],
					&remainingPrimary,
					&remainingLeaderCount,
					unconstrained,
					&s.domainStateOf(dom).podCountWithLeader,
					&s.domainStateOf(dom).podCount,
					1,
					false,
				)
			}
			result = append(result, d)
			tailHosted = tailHosted || (tailPending && s.hostsTailWithAssignedSlices(d))
			if completed {
				return s.finishSliceDistribution(result, domains, shape, count, tailPending)
			}
			continue
		}

		// No leaders remaining: handle tail without leaders
		if distributeSlices {
			if useBestFitAlgorithm(unconstrained) && s.domainStateOf(dom).sliceCount >= remainingPrimary {
				// optimize the last domain
				dom = s.findBestFitDomainForSlices(domains[i:], remainingPrimary, 0)
			}
			domainState := s.domainStateOf(dom)
			domainState.leaderCount = 0
			if domainState.sliceCount >= remainingPrimary {
				domainState.podCount = remainingPrimary * shape.size
				domainState.sliceCount = remainingPrimary
				result = append(result, dom)
				return s.finishSliceDistribution(result, domains, shape, count, tailPending)
			}
			domainState.podCount = domainState.sliceCount * shape.size
			remainingPrimary -= domainState.sliceCount
			result = append(result, dom)
			tailHosted = tailHosted || (tailPending && s.hostsTailWithAssignedSlices(dom))
			continue
		}

		// pods (slices=false)
		if useBestFitAlgorithm(unconstrained) && s.domainStateOf(dom).podCount >= remainingPrimary {
			// optimize the last domain
			dom = s.findBestFitDomain(domains[i:], remainingPrimary, 0)
		}
		domainState := s.domainStateOf(dom)
		domainState.leaderCount = 0
		if domainState.podCount >= remainingPrimary {
			domainState.podCount = remainingPrimary
			result = append(result, dom)
			return result
		}
		remainingPrimary -= domainState.podCount
		result = append(result, dom)
	}
	// Error logs are not verbosity-gated; dumping leaves scales with cluster size.
	s.log.Error(errCodeAssumptionsViolated, "unexpected remainingCount",
		"remainingCount", remainingPrimary,
		"remainingLeaderCount", remainingLeaderCount,
		"count", count,
		"leaderCount", leaderCount,
		"sliceSize", shape.size,
		"unconstrained", unconstrained,
		"topologyName", s.topologyName,
		"domainCount", len(domains),
		"leafCount", len(s.leaves))
	s.logLeafDomainsIfVerbose()
	return nil
}

// logLeafDomainsIfVerbose logs leaf domain IDs at V(6).
// The list scales with node count, so it stays off the Error path.
func (s *TASFlavorSnapshot) logLeafDomainsIfVerbose() {
	logV := s.log.V(6)
	if !logV.Enabled() {
		return
	}
	logV.Info("TAS flavor snapshot leaf domains",
		"topologyName", s.topologyName,
		"leafDomains", slices.Sorted(maps.Keys(s.leaves)))
}

// buildTopologyAssignmentForLevels build TopologyAssignment for levels within [levelIdx, endIdx)
func (s *TASFlavorSnapshot) buildTopologyAssignmentForLevels(domains []*domain, levelIdx, endIdx int) *utiltas.TopologyAssignment {
	assignment := &utiltas.TopologyAssignment{
		Domains: make([]utiltas.TopologyDomainAssignment, 0),
	}
	assignment.Levels = s.levelKeys[levelIdx:endIdx]
	for _, domain := range domains {
		if s.domainStateOf(domain).podCount == 0 {
			// It may happen when PodSet count is 0 or when using LeastFreeCapacity algorithm.
			continue
		}
		assignment.Domains = append(assignment.Domains, utiltas.TopologyDomainAssignment{
			Values: domain.levelValues[levelIdx:endIdx],
			Count:  s.domainStateOf(domain).podCount,
		})
	}
	return assignment
}

// buildAssignment returns the assignment published on the Workload, and for a
// virtual hostname level the leaf-level assignment it was rolled up from. The
// leaves are known only inside the cycle that picked them, and let a later
// PodSet see the exact nodes taken instead of a domain-wide bound.
func (s *TASFlavorSnapshot) buildAssignment(domains []*domain) (published, leaves *utiltas.TopologyAssignment) {
	// lex sort domains by their levelValues instead of IDs, as leaves' IDs can only contain the hostname
	slices.SortFunc(domains, s.compareDomainLevelValues)
	levelIdx, endIdx := 0, len(s.levelKeys)
	switch {
	case s.virtualHostname:
		leaves = s.buildTopologyAssignmentForLevels(domains, len(s.levelKeys)-1, len(s.levelKeys))
		// Publish at the declared levels; the injected level is internal only.
		domains = s.rollUpToParents(domains)
		endIdx = len(s.levelKeys) - 1
		slices.SortFunc(domains, s.compareDomainLevelValues)
	case s.declaresHostnameLevel():
		// assign only hostname values if topology defines it
		levelIdx = len(s.levelKeys) - 1
	}
	return s.buildTopologyAssignmentForLevels(domains, levelIdx, endIdx), leaves
}

// rollUpToParents groups the selected leaves by parent, summing their assigned
// counts into the parent. Summing is required because the parent's podCount
// holds the phase-1 capacity count when the fit level is the leaf level itself.
func (s *TASFlavorSnapshot) rollUpToParents(leaves []*domain) []*domain {
	parentIDs := sets.New[utiltas.TopologyDomainID]()
	var parents []*domain
	for _, leaf := range leaves {
		parent := leaf.parent
		if parent == nil {
			continue
		}
		parentState := s.domainStateOf(parent)
		if !parentIDs.Has(parent.id) {
			parentIDs.Insert(parent.id)
			parents = append(parents, parent)
			parentState.podCount = 0
			parentState.leaderCount = 0
		}
		leafState := s.domainStateOf(leaf)
		parentState.podCount += leafState.podCount
		parentState.leaderCount += leafState.leaderCount
	}
	return parents
}

func (s *TASFlavorSnapshot) lowerLevelDomains(domains []*domain) []*domain {
	result := make([]*domain, 0, len(domains))
	for _, domain := range domains {
		result = append(result, domain.children...)
	}
	return result
}

func (s *TASFlavorSnapshot) compareDomainLevelValues(a, b *domain) int {
	if s.leafIsNode() && a.parent == b.parent {
		return strings.Compare(a.levelValues[len(a.levelValues)-1], b.levelValues[len(b.levelValues)-1])
	}
	return compareDomainLevelValues(a, b)
}

func compareDomainLevelValues(a, b *domain) int {
	return slices.CompareFunc(a.levelValues, b.levelValues, strings.Compare)
}

func (s *TASFlavorSnapshot) sortedDomainsWithLeader(domains []*domain, unconstrained bool, spreadRules map[int]utiltas.SpreadingRule) []*domain {
	isLeastFreeCapacity := useLeastFreeCapacityAlgorithm(unconstrained)
	respectNodeAffinityPreferred := features.Enabled(features.TASRespectNodeAffinityPreferred)
	result := slices.Clone(domains)
	slices.SortFunc(result, func(a, b *domain) int {
		aDomainState, bDomainState := s.domainStateOf(a), s.domainStateOf(b)
		if aDomainState.leaderCount != bDomainState.leaderCount {
			return cmp.Compare(bDomainState.leaderCount, aDomainState.leaderCount)
		}

		if respectNodeAffinityPreferred && aDomainState.affinityScore != bDomainState.affinityScore {
			return cmp.Compare(bDomainState.affinityScore, aDomainState.affinityScore)
		}

		if aDomainState.sliceCountWithLeader != bDomainState.sliceCountWithLeader {
			if isLeastFreeCapacity {
				// Start from the domain with the least amount of free resources.
				// Ascending order.
				return cmp.Compare(aDomainState.sliceCountWithLeader, bDomainState.sliceCountWithLeader)
			}
			return cmp.Compare(bDomainState.sliceCountWithLeader, aDomainState.sliceCountWithLeader)
		}

		if aDomainState.podCountWithLeader != bDomainState.podCountWithLeader {
			return cmp.Compare(aDomainState.podCountWithLeader, bDomainState.podCountWithLeader)
		}

		return s.compareDomainLevelValues(a, b)
	})
	return s.sortedBySpreadPriority(result, spreadRules)
}

// This function sorts domains based on a specified algorithm: BestFit or LeastFreeCapacity.
//
// The sorting criteria are:
// - **BestFit**: `sliceCount` (descending), `podCount` (ascending), `levelValues` (ascending)
// - **LeastFreeCapacity**: `sliceCount` (ascending), `podCount` (ascending), `levelValues` (ascending)
//
// `podCount` is always sorted ascending. This prioritizes domains that can accommodate slices with minimal leftover pod capacity.
//
// Any spreadRules are applied last and take precedence over all of the above,
// so a domain does not win a spreading decision just by having more room.
func (s *TASFlavorSnapshot) sortedDomains(domains []*domain, unconstrained bool, spreadRules map[int]utiltas.SpreadingRule) []*domain {
	isLeastFreeCapacity := useLeastFreeCapacityAlgorithm(unconstrained)
	respectNodeAffinityPreferred := features.Enabled(features.TASRespectNodeAffinityPreferred)
	result := slices.Clone(domains)
	slices.SortFunc(result, func(a, b *domain) int {
		aDomainState, bDomainState := s.domainStateOf(a), s.domainStateOf(b)
		if respectNodeAffinityPreferred && aDomainState.affinityScore != bDomainState.affinityScore {
			return cmp.Compare(bDomainState.affinityScore, aDomainState.affinityScore)
		}

		if aDomainState.sliceCount != bDomainState.sliceCount {
			if isLeastFreeCapacity {
				// Start from the domain with the least amount of free resources.
				// Ascending order.
				return cmp.Compare(aDomainState.sliceCount, bDomainState.sliceCount)
			}
			return cmp.Compare(bDomainState.sliceCount, aDomainState.sliceCount)
		}

		if aDomainState.podCount != bDomainState.podCount {
			return cmp.Compare(aDomainState.podCount, bDomainState.podCount)
		}

		return s.compareDomainLevelValues(a, b)
	})
	return s.sortedBySpreadPriority(result, spreadRules)
}

// fillInCounts computes per-domain pod, slice, and leader capacities from the
// pod requirements, then rolls those capacities up the topology tree.
func (s *TASFlavorSnapshot) fillInCounts(ctx context.Context, requirements *topologyAssignmentPodRequirements, state *findTopologyAssignmentState) error {
	// cleanup the state in case some remaining values are present from computing
	// assignments for previous PodSets. Truncating to discard the state
	// slots of domain copies made for the previous PodSet.
	s.domainStates = s.domainStates[:s.domainCount]
	clear(s.domainStates)
	cachingRemainingResourcesEnabled := features.Enabled(features.TASCachingRemainingResources)
	switch {
	case features.Enabled(features.TASCacheNodeMatchResults):
		matchingLeaves, stats, err := s.getMatchingLeaves(ctx, requirements)
		if err != nil {
			return err
		}
		state.stats.add(stats)
		if err := s.fillLeaderFeasibleLeaves(ctx, requirements, state); err != nil {
			return err
		}
		for _, ml := range matchingLeaves {
			leaf := s.leaves[ml.GetID()]
			s.domainStateOf(&leaf.domain).affinityScore += ml.GetAffinityScore()
			s.fillLeafCounts(leaf, requirements, state, cachingRemainingResourcesEnabled)
		}
		s.fillLeaderOnlyLeafCounts(requirements, state, matchingLeaves, cachingRemainingResourcesEnabled)
	case s.leafIsNode():
		feasibleLeaves, err := s.schedulerSimulator.FindFeasibleNodes(ctx, simulator.AsCandidates(s.candidates()), &requirements.podRequirements, &state.stats.NodeExclusionStats)
		if err != nil {
			return err
		}
		if err := s.fillLeaderFeasibleLeaves(ctx, requirements, state); err != nil {
			return err
		}
		for _, ml := range feasibleLeaves {
			leaf := s.leaves[ml.GetID()]
			s.domainStateOf(&leaf.domain).affinityScore += ml.GetAffinityScore()
			s.fillLeafCounts(leaf, requirements, state, cachingRemainingResourcesEnabled)
		}
		s.fillLeaderOnlyLeafCounts(requirements, state, feasibleLeaves, cachingRemainingResourcesEnabled)
	default:
		// A leaf spans several nodes, so it has none to check for feasibility.
		state.stats.TotalNodes += len(s.leaves)
		for candidate := range s.candidates() {
			s.fillLeafCounts(candidate.leaf, requirements, state, cachingRemainingResourcesEnabled)
		}
	}

	if s.virtualHostname {
		s.recordUsageDomainCaps(requirements)
	}
	for _, root := range s.roots {
		s.fillInCountsHelper(root, state.shape(), state.sliceLevelIdx, 0, state.sliceSizeAtLevel, state.leaderCount > 0)
	}
	// Populated here, rather than by the caller, because this function clears
	// domainStates at its start - anywhere earlier would be wiped out.
	s.populateSpreadCounts(state.spreadCounts)
	return nil
}

// recordUsageDomainCaps evaluates every usage domain against its own remaining
// capacity, the way fillLeafCounts evaluates a leaf against its node.
// fillInCountsHelper applies the bounds when it rolls the leaves up.
func (s *TASFlavorSnapshot) recordUsageDomainCaps(requirements *topologyAssignmentPodRequirements) {
	for domainID, dom := range s.usageDomains() {
		remaining := s.domainRemainingCapacity(dom, requirements.assumedUsage.perDomain[domainID], requirements.podRequirements.SimulateEmpty)
		domainState := s.domainStateOf(dom)
		domainState.capacityBound.podCount = requirements.requests.CountIn(remaining.Get())
		if requirements.leader == nil {
			continue
		}

		domainState.capacityBound.leaderCount = 0
		if requirements.leader.requests.CountIn(remaining.Get()) > 0 {
			domainState.capacityBound.leaderCount = 1
			remaining.Sub(requirements.leader.requests)
		}
		domainState.capacityBound.podCountWithLeader = requirements.requests.CountIn(remaining.Get())
	}
}

// forgetMatchingLeaves drops the cached leaf sets. The simulator's answers feed them,
// so anything that changes what it reports has to call this.
func (s *TASFlavorSnapshot) forgetMatchingLeaves() {
	clear(s.matchingLeavesCache)
}

// podSetInfo merges a PodSet with its updates into the form the node filters read.
func podSetInfo(tasPodSetRequests TASPodSetRequests) (podset.PodSetInfo, string) {
	info := podset.FromPodSet(tasPodSetRequests.PodSet)
	for _, podSetUpdate := range tasPodSetRequests.PodSetUpdates {
		if err := info.Merge(podset.FromUpdate(podSetUpdate)); err != nil {
			return info, fmt.Sprintf("invalid podSetUpdate for PodSet %s, error: %s", tasPodSetRequests.PodSet.Name, err.Error())
		}
	}
	return info, ""
}

// workloadNamespace returns the namespace the PodSet's claims live in, empty when
// there is no Workload to take it from.
func workloadNamespace(wl *workload.Info) string {
	if wl == nil {
		return ""
	}
	return wl.Obj.Namespace
}

// buildPodRequirements turns a PodSet into the node filters TAS applies to it, in the
// field form the default simulator reads and in the Pod template the scheduler library
// reads. A non-empty second return value is the reason the PodSet cannot be placed.
func (s *TASFlavorSnapshot) buildPodRequirements(info podset.PodSetInfo, podSet *kueue.PodSet, namespace string) (simulator.PodRequirements, string) {
	var podRequirements simulator.PodRequirements
	podRequirements.Tolerations = utiltolerations.Merge(info.Tolerations, s.tolerations)

	if s.leafIsNode() {
		sel, err := labels.ValidatedSelectorFromSet(info.NodeSelector)
		if err != nil {
			return podRequirements, fmt.Sprintf("invalid node selectors: %s, reason: %s", info.NodeSelector, err)
		}
		podRequirements.Selector = sel
	} else {
		podRequirements.Selector = labels.Everything()
	}

	if info.Affinity != nil && info.Affinity.NodeAffinity != nil {
		if requiredAffinity := info.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution; requiredAffinity != nil {
			affinitySelector, err := nodeaffinity.NewNodeSelector(requiredAffinity)
			if err != nil {
				return podRequirements, fmt.Sprintf("invalid affinity node selectors: %s, reason: %s", requiredAffinity, err)
			}
			podRequirements.AffinitySelector = affinitySelector
		}
		if features.Enabled(features.TASRespectNodeAffinityPreferred) {
			preferredAffinity := info.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution
			if len(preferredAffinity) > 0 {
				prefTerms, err := nodeaffinity.NewPreferredSchedulingTerms(preferredAffinity)
				if err != nil {
					return podRequirements, fmt.Sprintf("invalid preferred node affinity terms: %v, reason: %s", preferredAffinity, err)
				}
				podRequirements.PreferredSchedulingTerms = prefTerms
			}
		}
	}

	// The template must carry the same constraints as the field form rather than the
	// bare PodSet template: the flavor's tolerations and the nodeSelector and
	// tolerations set by admission checks.
	podRequirements.PodTemplate = podSet.Template.DeepCopy()
	podRequirements.PodTemplate.Spec.Tolerations = podRequirements.Tolerations
	podRequirements.PodTemplate.Spec.NodeSelector = info.NodeSelector
	// A PodSet template carries no namespace, and the simulator resolves the
	// Workload's namespaced ResourceClaims through it.
	podRequirements.PodTemplate.Namespace = namespace
	return podRequirements, ""
}

// fillLeaderFeasibleLeaves records which leaves suit the leader. It asks about every
// leaf rather than only the workers', because the group shares the domain the assignment
// names, not the leaf: with a required level above the leaf, or with the hostname level
// injected, the leader and the workers can sit on different nodes of the same domain.
// A leaf spanning several nodes has no node to ask about, and TAS does not filter the
// workers per node there either.
func (s *TASFlavorSnapshot) fillLeaderFeasibleLeaves(
	ctx context.Context,
	requirements *topologyAssignmentPodRequirements,
	state *findTopologyAssignmentState,
) error {
	if requirements.leader == nil || requirements.leader.podRequirements == nil || !s.leafIsNode() {
		return nil
	}
	if leaves, found := s.cachedLeaderLeaves(requirements); found {
		state.leaderFeasibleLeaves = leaves
		return nil
	}
	allLeaves := slices.Collect(s.candidates())
	// FindFeasibleNodes writes affinity scores into the snapshot's domain state, and
	// this pass only wants the feasible set, so the workers' scores are put back.
	scores := make([]int64, len(allLeaves))
	for i, leaf := range allLeaves {
		scores[i] = leaf.GetAffinityScore()
	}
	leaderStats := newTASExclusionStats()
	leaderLeaves, err := s.schedulerSimulator.FindFeasibleNodes(ctx,
		simulator.AsCandidates(slices.Values(allLeaves)),
		requirements.leader.podRequirements,
		&leaderStats.NodeExclusionStats)
	for i, leaf := range allLeaves {
		leaf.SetAffinityScore(scores[i])
	}
	if err != nil {
		return err
	}
	state.leaderFeasibleLeaves = sets.New[utiltas.TopologyDomainID]()
	for _, leaf := range leaderLeaves {
		state.leaderFeasibleLeaves.Insert(leaf.GetID())
	}
	if key, ok := requirements.leaderMatchKey(); ok {
		s.storeMatchingLeaves(key, leaderLeaves, leaderStats)
	}
	return nil
}

// cachedLeaderLeaves returns the leaves an earlier call found for these leader filters,
// as a fresh set so that a caller cannot write through it into the cache.
func (s *TASFlavorSnapshot) cachedLeaderLeaves(requirements *topologyAssignmentPodRequirements) (sets.Set[utiltas.TopologyDomainID], bool) {
	key, ok := requirements.leaderMatchKey()
	if !ok {
		return nil, false
	}
	entry, found := s.matchingLeavesCache[key]
	if !found {
		return nil, false
	}
	leaves := sets.New[utiltas.TopologyDomainID]()
	for _, leaf := range entry.leaves {
		leaves.Insert(leaf.GetID())
	}
	return leaves, true
}

// leaderMatchKey is the workers' key marked as the leader's. The leader is asked about
// every leaf, so repeating that per preemption simulation is the most expensive part of
// placing a group.
func (r *topologyAssignmentPodRequirements) leaderMatchKey() (podSetMatchKey, bool) {
	if r.matchKey == nil {
		return podSetMatchKey{}, false
	}
	key := *r.matchKey
	key.Leader = true
	return key, true
}

// fillLeaderOnlyLeafCounts records the leaves that suit the leader but not the workers.
// fillLeafCounts only visits the workers' leaves, so without this the leader's own node
// is never counted and its domain looks like it has nowhere to put the leader.
func (s *TASFlavorSnapshot) fillLeaderOnlyLeafCounts(
	requirements *topologyAssignmentPodRequirements,
	state *findTopologyAssignmentState,
	workersLeaves []simulator.MatchedCandidate,
	cachingRemainingResourcesEnabled bool,
) {
	if state.leaderFeasibleLeaves == nil {
		return
	}
	workerFeasible := sets.New[utiltas.TopologyDomainID]()
	for _, leaf := range workersLeaves {
		workerFeasible.Insert(leaf.GetID())
	}
	for id := range state.leaderFeasibleLeaves.Difference(workerFeasible) {
		leaf := s.leaves[id]
		if !utiltas.DomainID(leaf.levelValues).BelongsTo(requirements.requiredReplacementDomain) {
			continue
		}
		remainingCapacity := s.availableCapacityForLeaf(leaf, requirements, cachingRemainingResourcesEnabled)
		if requirements.leader.forLeaf(leaf).CountIn(remainingCapacity.Get()) > 0 {
			// podCount stays zero: the domain gains a place for the leader, not room
			// for workers.
			s.domainStateOf(&leaf.domain).leaderCount = 1
		}
	}
}

func (s *TASFlavorSnapshot) getMatchingLeaves(ctx context.Context, requirements *topologyAssignmentPodRequirements) ([]simulator.MatchedCandidate, *tasExclusionStats, error) {
	if !s.leafIsNode() {
		stats := newTASExclusionStats()
		stats.TotalNodes += len(s.leaves)
		result := make([]simulator.MatchedCandidate, 0, len(s.leaves))
		for candidate := range s.candidates() {
			result = append(result, candidate)
		}
		return result, stats, nil
	}
	if requirements.matchKey != nil {
		cached, found := s.matchingLeavesCache[*requirements.matchKey]
		if found {
			return cached.leaves, cached.stats, nil
		}
	}

	leafStats := newTASExclusionStats()
	var err error
	feasibleLeaves, err := s.schedulerSimulator.FindFeasibleNodes(ctx, simulator.AsCandidates(s.candidates()), &requirements.podRequirements, &leafStats.NodeExclusionStats)
	if err != nil {
		return nil, nil, err
	}
	if requirements.matchKey != nil {
		s.storeMatchingLeaves(*requirements.matchKey, feasibleLeaves, leafStats)
	}
	return feasibleLeaves, leafStats, nil
}

// storeMatchingLeaves records what the simulator reported for one key. The workers' and
// the leader's passes both go through here, so every entry carries its stats.
func (s *TASFlavorSnapshot) storeMatchingLeaves(key podSetMatchKey, leaves []simulator.MatchedCandidate, stats *tasExclusionStats) {
	if s.matchingLeavesCache == nil {
		s.matchingLeavesCache = make(map[podSetMatchKey]*matchingLeavesCacheEntry)
	}
	s.matchingLeavesCache[key] = &matchingLeavesCacheEntry{leaves: leaves, stats: stats}
}

// availableCapacityForLeaf is the leaf's remaining capacity less what this scheduling
// cycle has already placed on it. The two steps belong together: reading the capacity
// without subtracting the in-cycle usage counts the same node twice.
func (s *TASFlavorSnapshot) availableCapacityForLeaf(leaf *leafDomain, requirements *topologyAssignmentPodRequirements, cachingRemainingResourcesEnabled bool) resources.LazyRequests {
	// In-cycle assignments are keyed by leaf, so this picks up the exact nodes an
	// earlier PodSet took. Domain-keyed entries, which come from assignments recovered
	// from the Workload, are applied in recordUsageDomainCaps.
	remaining := s.remainingCapacityForLeaf(leaf, requirements.podRequirements.SimulateEmpty, cachingRemainingResourcesEnabled)
	remaining.Sub(s.assumedUsageForLeaf(requirements.assumedUsage, leaf))
	return remaining
}

func (s *TASFlavorSnapshot) remainingCapacityForLeaf(leaf *leafDomain, simulateEmpty, cachingRemainingResourcesEnabled bool) resources.LazyRequests {
	leafCapacity := s.leafCapacityOf(leaf)
	if cachingRemainingResourcesEnabled {
		if simulateEmpty {
			return resources.NewLazyRequests(leafCapacity.freeCapacity)
		}
		return resources.NewLazyRequests(s.getRemainingCapacity(leaf))
	}
	remainingCapacity := resources.NewLazyRequests(leafCapacity.freeCapacity)
	if !simulateEmpty {
		remainingCapacity.Sub(leafCapacity.tasUsage)
	}
	return remainingCapacity
}

func (s *TASFlavorSnapshot) fillLeafCounts(leaf *leafDomain, requirements *topologyAssignmentPodRequirements, state *findTopologyAssignmentState, cachingRemainingResourcesEnabled bool) {
	// leaf.id contains only the hostname for hostname-level topologies, while
	// levelValues retain the full domain path needed for this ancestry check.
	if !utiltas.DomainID(leaf.levelValues).BelongsTo(requirements.requiredReplacementDomain) {
		state.stats.TopologyDomain++
		return
	}
	remainingCapacity := s.availableCapacityForLeaf(leaf, requirements, cachingRemainingResourcesEnabled)
	var limitingRes corev1.ResourceName
	leafDomainState := s.domainStateOf(&leaf.domain)
	leafDomainState.podCount, limitingRes = requirements.forLeaf(leaf).CountInWithLimitingResource(remainingCapacity.Get())

	// Track resource exclusions: if this node can't fit even one pod,
	// identify which resource is the bottleneck.
	if leafDomainState.podCount == 0 && limitingRes != "" {
		state.stats.recordResourceExclusion(limitingRes)
	}
	if requirements.leader == nil {
		return
	}

	leafDomainState.leaderCount = 0
	if state.leaderFeasibleFor(leaf) &&
		requirements.leader.forLeaf(leaf).CountIn(remainingCapacity.Get()) > 0 {
		leafDomainState.leaderCount = 1
		remainingCapacity.Sub(requirements.leader.forLeaf(leaf))
	}

	leafDomainState.podCountWithLeader = requirements.forLeaf(leaf).CountIn(remainingCapacity.Get())
}

func (s *TASFlavorSnapshot) fillInCountsHelper(domain *domain, shape sliceShape, sliceLevelIdx int, level int, sliceSizeAtLevel map[int]int32, leaderRequired bool) {
	domainState := s.domainStateOf(domain)
	// logic for a leaf
	if len(domain.children) == 0 {
		if level == sliceLevelIdx {
			// initialize the sliceCount if leaf is the request slice level
			domainState.sliceCount = domainState.podCount / shape.size
			if leaderRequired {
				domainState.sliceCountWithLeader = domainState.podCountWithLeader / shape.size
			}
			if shape.hasTail() {
				fillTailCountsAtSliceLevel(domainState, shape)
			}
		}
		return
	}
	// logic for a parent
	childrenCapacity := int32(0)
	sliceCapacity := int32(0)
	hasWithLeaderCapacityContributor := false
	minPodCountWithLeaderDifference := int32(math.MaxInt32)
	minSliceCountWithLeaderDifference := int32(math.MaxInt32)
	leaderCount := int32(0)
	affinityScore := int64(0)
	// tracksTail is false below the slice level, where a domain is too
	// fine-grained to hold a slice and the slice counts carry no meaning.
	tracksTail := shape.hasTail() && level < sliceLevelIdx
	var tailPenalties tailPenaltyTracker

	// When multi-layer constraints exist, children at a constrained level
	// can only contribute pods in multiples of the inner slice size.
	// Round down each child's effective contribution so that the parent's
	// capacity accurately reflects what can actually be grouped.
	childLevel := level + 1
	innerSize, hasInnerConstraint := sliceSizeAtLevel[childLevel]

	for childIdx, child := range domain.children {
		s.fillInCountsHelper(child, shape, sliceLevelIdx, childLevel, sliceSizeAtLevel, leaderRequired)

		childDomainState := s.domainStateOf(child)
		childPodCount := childDomainState.podCount
		if hasInnerConstraint {
			childPodCount = (childDomainState.podCount / innerSize) * innerSize
		}

		childrenCapacity += childPodCount
		sliceCapacity += childDomainState.sliceCount
		leaderEligible := leaderRequired && childDomainState.leaderCount > 0
		if leaderEligible {
			childPodCountWithLeader := childDomainState.podCountWithLeader
			if hasInnerConstraint {
				childPodCountWithLeader = (childPodCountWithLeader / innerSize) * innerSize
			}
			hasWithLeaderCapacityContributor = true
			minPodCountWithLeaderDifference = min(childPodCount-childPodCountWithLeader, minPodCountWithLeaderDifference)
			minSliceCountWithLeaderDifference = min(childDomainState.sliceCount-childDomainState.sliceCountWithLeader, minSliceCountWithLeaderDifference)
			leaderCount = max(childDomainState.leaderCount, leaderCount)
		}
		if tracksTail {
			tailPenalties.add(childIdx, childDomainState, leaderEligible)
		}
		affinityScore += childDomainState.affinityScore
	}
	domainState.podCount = childrenCapacity
	domainState.sliceCount = sliceCapacity
	domainState.affinityScore = affinityScore
	childrenSliceCapacity := sliceCapacity
	if s.virtualHostname && level == s.usageLevelIdx() {
		domainState.podCount = min(domainState.podCount, domainState.capacityBound.podCount)
		domainState.sliceCount = min(sliceCapacity, domainState.podCount/shape.size)
	}
	if level == sliceLevelIdx {
		domainState.sliceCount = domainState.podCount / shape.size
	}
	if leaderRequired {
		sliceCountWithLeader := int32(0)
		if hasWithLeaderCapacityContributor {
			domainState.podCountWithLeader = childrenCapacity - minPodCountWithLeaderDifference
			sliceCountWithLeader = sliceCapacity - minSliceCountWithLeaderDifference
		} else {
			domainState.podCountWithLeader = 0
		}
		domainState.leaderCount = leaderCount
		if s.virtualHostname && level == s.usageLevelIdx() {
			domainState.leaderCount = min(domainState.leaderCount, domainState.capacityBound.leaderCount)
			// The leader's cost is measured against a leaf, so it can exceed the
			// bounded pod count and drive the difference below zero.
			domainState.podCountWithLeader = min(max(0, domainState.podCountWithLeader), domainState.capacityBound.podCountWithLeader)
			sliceCountWithLeader = min(max(0, sliceCountWithLeader), domainState.podCountWithLeader/shape.size)
		}
		if level == sliceLevelIdx {
			sliceCountWithLeader = domainState.podCountWithLeader / shape.size
		}
		domainState.sliceCountWithLeader = sliceCountWithLeader
	}
	if shape.hasTail() && level <= sliceLevelIdx {
		fillTailCounts(domainState, shape, level == sliceLevelIdx, childrenSliceCapacity, &tailPenalties)
	}
}

func (s *TASFlavorSnapshot) notFitMessage(slicesFitCount, totalRequestsSlicesCount, sliceSize int32, stats *tasExclusionStats) string {
	var builder strings.Builder

	unit := "slice"
	if sliceSize == 1 {
		unit = "pod"
	}

	if slicesFitCount == 0 {
		fmt.Fprintf(&builder, "topology %q doesn't allow to fit any of %d %s(s)", s.topologyName, totalRequestsSlicesCount, unit)
	} else {
		fmt.Fprintf(&builder, "topology %q allows to fit only %d out of %d %s(s)", s.topologyName, slicesFitCount, totalRequestsSlicesCount, unit)
	}

	// Append exclusion stats if available.
	if stats.hasExclusions() {
		fmt.Fprintf(&builder, ". Total nodes: %d; excluded: %s", stats.TotalNodes, stats.formatReasons())
	}

	return builder.String()
}

func (s *TASFlavorSnapshot) countSlicesInSubtree(d *domain, currentLevel, targetLevel int, sliceSize int32) int32 {
	if currentLevel == targetLevel {
		return s.domainStateOf(d).podCount / sliceSize
	}
	var total int32
	for _, child := range d.children {
		total += s.countSlicesInSubtree(child, currentLevel+1, targetLevel, sliceSize)
	}
	return total
}

func (s *TASFlavorSnapshot) multiLayerNotFitMessage(
	requiredLevelIdx int,
	count int32,
	constraints []kueue.PodsetSliceRequiredTopologyConstraint,
	stats *tasExclusionStats,
) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "topology %q doesn't allow to fit", s.topologyName)

	// Pick the domain with the highest sliceCount to report the best-case
	// fit counts. Tie-break on domain ID for deterministic messages, since
	// domainsPerLevel is map-backed and iteration order is random.
	var bestDomain *domain
	for _, d := range s.domainsPerLevel[requiredLevelIdx] {
		if bestDomain == nil || s.domainStateOf(d).sliceCount > s.domainStateOf(bestDomain).sliceCount ||
			(s.domainStateOf(d).sliceCount == s.domainStateOf(bestDomain).sliceCount && d.id < bestDomain.id) {
			bestDomain = d
		}
	}
	if bestDomain == nil {
		return builder.String()
	}

	for _, c := range constraints {
		targetLevelIdx, found := s.resolveLevelIdx(c.Topology)
		if !found {
			continue
		}
		neededSlices := count / c.Size
		fitSlices := s.countSlicesInSubtree(bestDomain, requiredLevelIdx, targetLevelIdx, c.Size)
		fmt.Fprintf(&builder, "; %d/%d slice(s) fit on level %s", fitSlices, neededSlices, c.Topology)
	}

	// Append exclusion stats if available.
	if stats.hasExclusions() {
		fmt.Fprintf(&builder, ". Total nodes: %d; excluded: %s", stats.TotalNodes, stats.formatReasons())
	}

	return builder.String()
}

// mergeTopologyAssignments merges two topology assignments keeping the lexicographical order of levelValues.
func (s *TASFlavorSnapshot) mergeTopologyAssignments(a, b *utiltas.TopologyAssignment) *utiltas.TopologyAssignment {
	levels := a.Levels
	sortedDomains := make([]utiltas.TopologyDomainAssignment, 0, len(a.Domains)+len(b.Domains))
	sortedDomains = append(sortedDomains, a.Domains...)
	sortedDomains = append(sortedDomains, b.Domains...)
	slices.SortFunc(sortedDomains, func(a, b utiltas.TopologyDomainAssignment) int {
		aDomain := s.domainForAssignmentValues(levels, a.Values)
		bDomain := s.domainForAssignmentValues(levels, b.Values)
		if aDomain == nil || bDomain == nil {
			// Defensive: staleness is verified before merging.
			return cmp.Compare(utiltas.DomainID(a.Values), utiltas.DomainID(b.Values))
		}
		return cmp.Compare(utiltas.DomainID(aDomain.levelValues), utiltas.DomainID(bDomain.levelValues))
	})
	mergedDomains := make([]utiltas.TopologyDomainAssignment, 0, len(sortedDomains))
	for _, domain := range sortedDomains {
		if canMergeDomains(mergedDomains, domain) {
			mergedDomains[len(mergedDomains)-1].Count += domain.Count
		} else {
			mergedDomains = append(mergedDomains, domain)
		}
	}
	return &utiltas.TopologyAssignment{
		Levels:  a.Levels,
		Domains: mergedDomains,
	}
}

func canMergeDomains(mergedDomains []utiltas.TopologyDomainAssignment, domain utiltas.TopologyDomainAssignment) bool {
	if len(mergedDomains) == 0 {
		return false
	}
	lastDomain := mergedDomains[len(mergedDomains)-1]
	return utiltas.DomainID(domain.Values) == utiltas.DomainID(lastDomain.Values)
}

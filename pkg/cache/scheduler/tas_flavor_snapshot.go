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
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-helpers/scheduling/corev1/nodeaffinity"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/podset"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
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

	podCountWithLeader   int32
	sliceCountWithLeader int32
	leaderCount          int32

	// affinityScore is the sum of weights of all preferred affinity terms that match the node.
	// For non-leaf domains, it is the sum of affinity scores of all children.
	affinityScore int64
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

	// matchingLeavesCache caches the set of qualified leaves for a PodSet
	// of a Workload to avoid recalculating selectors/taints during preemption simulations or
	// multiple worker PodSet placements within the same scheduling cycle snapshot.
	matchingLeavesCache map[podSetMatchKey]*matchingLeavesCacheEntry

	// simulatorSnapshot stores enough data to run a WAS scheduling simulation.
	simulatorSnapshot simulator.SimulatorSnapshot

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
}

// matchedLeaf is a leaf the checker accepted and the score it gave it. The score
// is a value because the state a candidate reads it from is cleared between runs.
// The leaf itself is shared by every snapshot and never written, so it is kept
// by pointer rather than looked up again on each hit.
type matchedLeaf struct {
	leaf  *leafDomain
	score int64
}

// matchingLeavesCacheEntry stores the cached list of matching leaves and accumulated
// exclusion stats for a specific podSetMatchKey.
type matchingLeavesCacheEntry struct {
	leaves []matchedLeaf
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
	topologyName kueue.TopologyReference,
	tree *topologyTree,
	tolerations []corev1.Toleration,
	simulatorSnapshot simulator.SimulatorSnapshot,
	opts ...tasFlavorSnapshotOption,
) *TASFlavorSnapshot {
	options := &tasFlavorSnapshotOptions{}
	for _, opt := range opts {
		if opt != nil {
			opt(options)
		}
	}

	snapshot := &TASFlavorSnapshot{
		log:               log,
		topologyName:      topologyName,
		topologyTree:      tree,
		domainStates:      make([]domainState, tree.domainCount),
		leafCapacities:    make([]leafCapacity, len(tree.leaves)),
		leafCandidates:    make([]leafCandidate, len(tree.leaves)),
		tolerations:       slices.Clone(tolerations),
		simulatorSnapshot: simulatorSnapshot,
		resourceFormatter: options.resourceFormatter,
	}
	for _, leaf := range tree.leaves {
		snapshot.leafCapacities[leaf.leafIdx].freeCapacity = leaf.capacity.Clone()
		snapshot.leafCandidates[leaf.leafIdx] = leafCandidate{leaf: leaf, s: snapshot}
	}
	return snapshot
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

// hasDomain reports whether the domain has a leaf in the snapshot, i.e. whether
// it holds a node the flavor selects.
func (s *TASFlavorSnapshot) hasDomain(domainID utiltas.TopologyDomainID) bool {
	return s.leaves[domainID] != nil
}

// addTASUsageForHeldDomains adds usage only for domains this snapshot has a leaf
// for. With TASHandleOverlappingFlavors, usages can cover far more domains than
// the flavor selects, so it walks whichever side is smaller.
func (s *TASFlavorSnapshot) addTASUsageForHeldDomains(usages map[utiltas.TopologyDomainID]resources.Requests) {
	if len(s.leaves) < len(usages) {
		for domainID := range s.leaves {
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

func (s *TASFlavorSnapshot) addTASUsage(domainID utiltas.TopologyDomainID, usage resources.Requests) {
	if s.leaves[domainID] == nil {
		// this can happen if there is an admitted workload for which the
		// backing node was deleted or is no longer Ready (so the addCapacity
		// function was not called).
		s.log.V(3).Info("skip accounting for TAS usage in domain", "domain", domainID, "usage", usage)
		return
	}
	leafCapacity := s.leafCapacityOf(s.leaves[domainID])
	if leafCapacity.tasUsage == nil {
		leafCapacity.tasUsage = resources.NewRequests()
	}
	leafCapacity.tasUsage.Add(usage)
	leafCapacity.cachedRemainingCapacity = resources.LazyRequests{}
}

func (s *TASFlavorSnapshot) removeTASUsage(domainID utiltas.TopologyDomainID, usage resources.Requests) {
	if s.leaves[domainID] == nil {
		// this can happen if there is an admitted workload for which the
		// backing node was deleted or is no longer Ready (so the addCapacity
		// function was not called).
		s.log.V(3).Info("skip removing TAS usage in domain", "domain", domainID, "usage", usage)
		return
	}
	leafCapacity := s.leafCapacityOf(s.leaves[domainID])
	if leafCapacity.tasUsage == nil {
		leafCapacity.tasUsage = resources.NewRequests()
	}
	leafCapacity.tasUsage.Sub(usage)
	leafCapacity.cachedRemainingCapacity = resources.LazyRequests{}
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
	Count             int32
	Flavor            kueue.ResourceFlavorReference
	Implied           bool
	PodSetGroupName   *string
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
		domainID := utiltas.DomainID(domainUsage.Values)
		leaf, found := s.leaves[domainID]
		if !found {
			return false
		}
		remainingCapacity := s.remainingCapacityForLeaf(leaf, false, cachingEnabled)
		if domainUsage.SinglePodRequests.CountIn(remainingCapacity.Get()) < domainUsage.Count {
			return false
		}
	}
	return true
}

type findTopologyAssignmentsOption struct {
	simulateEmpty          bool
	workload               *kueue.Workload
	aggregatedDomainUsages map[utiltas.TopologyDomainID]resources.Requests
}

type tasExclusionStats struct {
	simulator.NodeExclusionStats
	TopologyDomain int
	Resources      map[corev1.ResourceName]int
}

type topologyAssignmentPodRequirements struct {
	podRequirements           simulator.PodRequirements
	requests                  resources.Requests
	leaderRequests            resources.Requests
	assumedUsage              map[utiltas.TopologyDomainID]resources.Requests
	requiredReplacementDomain utiltas.TopologyDomainID
	simulateEmpty             bool
	matchKey                  *podSetMatchKey
}

// topologyAssignmentParameters stores placement-specific inputs that remain
// relevant after domain capacities are computed.
type topologyAssignmentParameters struct {
	sliceSizeAtLevel      map[int]int32
	sliceSize             int32
	count                 int32
	leaderCount           int32
	requestedLevelIdx     int
	sliceLevelIdx         int
	required              bool
	unconstrained         bool
	multiLayerConstraints []kueue.PodsetSliceRequiredTopologyConstraint
}

// findTopologyAssignmentState stores the derived state for a single run of the
// TAS placement algorithm.
type findTopologyAssignmentState struct {
	topologyAssignmentParameters
	stats *tasExclusionStats
}

func newTASExclusionStats() *tasExclusionStats {
	return &tasExclusionStats{}
}

func (s *tasExclusionStats) hasExclusions() bool {
	return s.NodeSelector > 0 || s.Affinity > 0 || len(s.Taints) > 0 || s.TopologyDomain > 0 || len(s.Resources) > 0
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

func WithWorkload(wl *kueue.Workload) FindTopologyAssignmentsOption {
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

// FindTopologyAssignmentsForFlavor returns TAS assignment, if possible, for all
// the TAS requests in the flavor handled by the snapshot.
func (s *TASFlavorSnapshot) FindTopologyAssignmentsForFlavor(ctx context.Context, flavorTASRequests FlavorTASRequests, options ...FindTopologyAssignmentsOption) TASAssignmentsResult {
	log := log.FromContext(ctx)
	opts := &findTopologyAssignmentsOption{}
	for _, option := range options {
		option(opts)
	}

	result := make(map[kueue.PodSetReference]tasPodSetAssignmentResult)
	assumedUsage := make(map[utiltas.TopologyDomainID]resources.Requests)
	if features.Enabled(features.TASHandleOverlappingFlavors) && opts.aggregatedDomainUsages != nil {
		assumedUsage = opts.aggregatedDomainUsages
	}

	groupedTASRequests := make(map[string]FlavorTASRequests)
	groupsOrder := make([]string, 0)

	for idx, tr := range flavorTASRequests {
		groupKey := strconv.Itoa(idx)
		if tr.PodSetGroupName != nil {
			groupKey = *tr.PodSetGroupName
		}

		if !slices.Contains(groupsOrder, groupKey) {
			groupsOrder = append(groupsOrder, groupKey)
		}
		groupedTASRequests[groupKey] = append(groupedTASRequests[groupKey], tr)
	}

	for _, groupKey := range groupsOrder {
		trs := groupedTASRequests[groupKey]
		if workload.HasUnhealthyNodes(opts.workload) {
			for _, tr := range trs {
				// In case of looking for Node replacement, TopologyRequest has only
				// PodSets with the Node to replace, so we match PodSetAssignment
				psa := findPSA(opts.workload, tr.PodSet.Name)
				if psa == nil || psa.TopologyAssignment == nil {
					continue
				}
				if features.Enabled(features.SkipReassignmentForPodOwnedWorkloads) && workload.OwnedBySinglePod(opts.workload) {
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
			assignments, reason := s.findTopologyAssignment(ctx, workers, leader, assumedUsage, opts.simulateEmpty, "", opts.workload)
			for _, tr := range trs {
				podSetName := tr.PodSet.Name
				result[podSetName] = tasPodSetAssignmentResult{TopologyAssignment: assignments[podSetName], FailureReason: reason}
			}

			if reason != "" {
				return result
			}
			for _, tr := range trs {
				addAssumedUsage(assumedUsage, assignments[tr.PodSet.Name], &tr)
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
// reason if finding fails
func (s *TASFlavorSnapshot) findReplacementAssignment(
	ctx context.Context,
	tr *TASPodSetRequests,
	existingAssignment *utiltas.TopologyAssignment,
	wl *kueue.Workload,
	assumedUsage map[utiltas.TopologyDomainID]resources.Requests,
) (*utiltas.TopologyAssignment, *utiltas.TopologyAssignment, string) {
	tr.Count = deleteDomain(existingAssignment, wl.Status.UnhealthyNodes[0].Name)
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
	replacementAssignment, reason := s.findTopologyAssignment(ctx, trCopy, nil, assumedUsage, false, requiredReplacementDomain, wl)
	if reason != "" {
		return nil, nil, reason
	}
	if replacementAssignment == nil || len(replacementAssignment[tr.PodSet.Name].Domains) == 0 {
		return nil, nil, fmt.Sprintf("cannot find replacement assignment for unhealthy node: %v", wl.Status.UnhealthyNodes[0].Name)
	}
	newAssignment := s.mergeTopologyAssignments(replacementAssignment[tr.PodSet.Name], existingAssignment)
	return newAssignment, replacementAssignment[tr.PodSet.Name], ""
}

func addAssumedUsage(assumedUsage map[utiltas.TopologyDomainID]resources.Requests, ta *utiltas.TopologyAssignment, tr *TASPodSetRequests) {
	addUsagePerDomain(assumedUsage, utiltas.ComputeUsagePerDomain(ta, tr.SinglePodRequests))
}

func addUsagePerDomain(assumedUsage map[utiltas.TopologyDomainID]resources.Requests, usagePerDomain map[utiltas.TopologyDomainID]resources.Requests) {
	for domainID, usage := range usagePerDomain {
		if assumedUsage[domainID] == nil {
			assumedUsage[domainID] = resources.NewRequests()
		}
		assumedUsage[domainID].Add(usage)
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
					return s.findIncompleteSliceDomain(tr, ta, tr.Count, v.Size, v.Topology)
				}
			}
		}
		return s.findIncompleteSliceDomain(tr, ta, tr.Count, sliceSize, s.sliceLevelKeyWithDefault(tr.PodSet.TopologyRequest, s.lowestLevel()))
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

// IsTopologyAssignmentStale indicates whether the topologyAssignment have Nodes
// that don't exists in the snapshot. It may be cause e.g. by Node deletion, or change
// in Node's NodeReady condition
func (s *TASFlavorSnapshot) IsTopologyAssignmentStale(ta *utiltas.TopologyAssignment) (bool, string) {
	for _, domain := range ta.Domains {
		if _, found := s.leaves[utiltas.DomainID(domain.Values)]; !found {
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

func (s *TASFlavorSnapshot) findIncompleteSliceDomain(tr *TASPodSetRequests, ta *utiltas.TopologyAssignment, missingCount int32, sliceSize int32, topologyKey string) utiltas.TopologyDomainID {
	// this function assumes that all assignments are at the hostname level
	sliceLevelIdx, found := s.resolveLevelIdx(topologyKey)
	if !found {
		return ""
	}

	// domainToUsage maps a domain at sliceLevel to the number of pods in it
	domainToUsage := make(map[utiltas.TopologyDomainID]int32)
	nodeLevel := len(s.levelKeys) - 1

	for _, domainFromAssignment := range ta.Domains {
		domain, ok := s.domainsPerLevel[nodeLevel][utiltas.DomainID(domainFromAssignment.Values)]
		if !ok {
			continue
		}

		for i := nodeLevel; i > sliceLevelIdx; i-- {
			domain = domain.parent
		}
		domainToUsage[domain.id] += domainFromAssignment.Count
	}

	for domainID, count := range domainToUsage {
		if (count+missingCount)%sliceSize == 0 {
			return domainID
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
	assumedUsage map[utiltas.TopologyDomainID]resources.Requests,
	simulateEmpty bool, requiredReplacementDomain utiltas.TopologyDomainID, wl *kueue.Workload) (map[kueue.PodSetReference]*utiltas.TopologyAssignment, string) {
	requirements := &topologyAssignmentPodRequirements{
		assumedUsage:              assumedUsage,
		requiredReplacementDomain: requiredReplacementDomain,
		simulateEmpty:             simulateEmpty,
	}
	state := &findTopologyAssignmentState{
		topologyAssignmentParameters: topologyAssignmentParameters{
			count: workersTasPodSetRequests.Count,
		},
		stats: &tasExclusionStats{},
	}
	requirements.requests = workersTasPodSetRequests.SinglePodRequests.Clone()
	requirements.requests.Add(resources.OnePodRequest)

	if leaderTasPodSetRequests != nil {
		requirements.leaderRequests = leaderTasPodSetRequests.SinglePodRequests.Clone()
		requirements.leaderRequests.Add(resources.OnePodRequest)
		// PodSet grouping validation requires the leader PodSet to have one replica.
		state.leaderCount = 1
	}

	info := podset.FromPodSet(workersTasPodSetRequests.PodSet)
	for _, podSetUpdate := range workersTasPodSetRequests.PodSetUpdates {
		if err := info.Merge(podset.FromUpdate(podSetUpdate)); err != nil {
			return nil, fmt.Sprintf("invalid podSetUpdate for PodSet %s, error: %s", workersTasPodSetRequests.PodSet.Name, err.Error())
		}
	}

	// If slice topology is not requested then we can assume that slice is a single pod
	sliceSize, reason := getSliceSizeWithSinglePodAsDefault(workersTasPodSetRequests.PodSet.TopologyRequest)
	if len(reason) > 0 {
		return nil, reason
	}
	state.sliceSize = sliceSize

	state.required = isRequired(workersTasPodSetRequests.PodSet.TopologyRequest)
	state.unconstrained = isUnconstrained(workersTasPodSetRequests.PodSet.TopologyRequest, &workersTasPodSetRequests)

	topologyKey := s.levelKeyWithImpliedFallback(&workersTasPodSetRequests)
	if topologyKey == nil {
		return nil, "topology level not specified"
	}
	requestedLevelIdx, found := s.resolveLevelIdx(*topologyKey)
	if !found {
		return nil, fmt.Sprintf("no requested topology level: %s", *topologyKey)
	}
	state.requestedLevelIdx = requestedLevelIdx

	sliceTopologyKey := s.sliceLevelKeyWithDefault(workersTasPodSetRequests.PodSet.TopologyRequest, s.lowestLevel())
	sliceLevelIdx, found := s.resolveLevelIdx(sliceTopologyKey)
	if !found {
		return nil, fmt.Sprintf("no requested topology level for slices: %s", sliceTopologyKey)
	}
	state.sliceLevelIdx = sliceLevelIdx

	if state.requestedLevelIdx > state.sliceLevelIdx {
		return nil, fmt.Sprintf("podset slice topology %s is above the podset topology %s", sliceTopologyKey, *topologyKey)
	}

	sliceSizeAtLevel, reason := s.buildSliceSizeAtLevel(workersTasPodSetRequests, state.sliceSize, state.sliceLevelIdx)
	if len(reason) > 0 {
		return nil, reason
	}
	state.sliceSizeAtLevel = sliceSizeAtLevel

	if len(sliceSizeAtLevel) > 0 {
		state.multiLayerConstraints = utiltas.PodSetSliceRequiredTopologyConstraints(workersTasPodSetRequests.PodSet.TopologyRequest)
	}

	requirements.podRequirements.Tolerations = append(info.Tolerations, s.tolerations...)

	if s.isLowestLevelNode {
		sel, err := labels.ValidatedSelectorFromSet(info.NodeSelector)
		if err != nil {
			return nil, fmt.Sprintf("invalid node selectors: %s, reason: %s", info.NodeSelector, err)
		}
		requirements.podRequirements.Selector = sel
		if features.Enabled(features.TASCacheNodeMatchResults) && wl != nil && wl.UID != "" {
			requirements.matchKey = &podSetMatchKey{
				WorkloadUID: wl.UID,
				PodSetName:  string(workersTasPodSetRequests.PodSet.Name),
			}
		}
	} else {
		requirements.podRequirements.Selector = labels.Everything()
	}

	if info.Affinity != nil && info.Affinity.NodeAffinity != nil {
		if requiredAffinity := info.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution; requiredAffinity != nil {
			affinitySelector, err := nodeaffinity.NewNodeSelector(requiredAffinity)
			if err != nil {
				return nil, fmt.Sprintf("invalid affinity node selectors: %s, reason: %s", requiredAffinity, err)
			}
			requirements.podRequirements.AffinitySelector = affinitySelector
		}
		if features.Enabled(features.TASRespectNodeAffinityPreferred) {
			preferredAffinity := info.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution
			if len(preferredAffinity) > 0 {
				prefTerms, err := nodeaffinity.NewPreferredSchedulingTerms(preferredAffinity)
				if err != nil {
					return nil, fmt.Sprintf("invalid preferred node affinity terms: %v, reason: %s", preferredAffinity, err)
				}
				requirements.podRequirements.PreferredSchedulingTerms = prefTerms
			}
		}
	}

	requirements.podRequirements.PodTemplate = workersTasPodSetRequests.PodSet.Template.DeepCopy()

	// phase 1 - determine the number of pods and slices which can fit in each topology domain
	err := s.fillInCounts(ctx, requirements, state)
	if err != nil {
		return nil, fmt.Sprintf("unable to calculate domain capacities for PodSet %s, error: %s", info.Name, err.Error())
	}

	// phase 2a: determine the level at which the assignment is done along with
	// the domains which can accommodate all pods/slices
	var currFitDomain []*domain
	var fitLevelIdx int
	var useBalancedPlacement bool
	if features.Enabled(features.TASBalancedPlacement) && !state.required && !state.unconstrained {
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
			return nil, reason
		}
	}
	// phase 2b: traverse the tree down level-by-level optimizing the number of
	// topology domains at each level
	// if unconstrained is set, we'll only do it once
	currFitDomain = s.updateCountsToMinimumGeneric(currFitDomain, state.count, state.leaderCount, state.sliceSize, state.unconstrained, true)
	currentLevelIdx := fitLevelIdx
	for ; currentLevelIdx < min(len(s.domainsPerLevel)-1, state.sliceLevelIdx) && !useBalancedPlacement; currentLevelIdx++ {
		// If we are "above" the requested slice topology level and we don't run the balanced placement algorithm,
		// we're greedily assigning pods/slices to all domains without checking what we've assigned to parent domains.
		sortedLowerDomains := s.sortedDomains(s.lowerLevelDomains(currFitDomain), state.unconstrained)
		currFitDomain = s.updateCountsToMinimumGeneric(sortedLowerDomains, state.count, state.leaderCount, state.sliceSize, state.unconstrained, true)
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
			sortedLowerDomains := s.sortedDomains(domain.children, state.unconstrained)

			if sliceSizeOnLevel > 1 {
				// For inner slice layers, recompute sliceCount on the
				// child domains based on the current inner slice size.
				// The pre-populated sliceCount was computed for the
				// outermost slice level and is not valid here.
				for _, d := range sortedLowerDomains {
					domainState := s.domainStateOf(d)
					domainState.sliceCount = domainState.podCount / sliceSizeOnLevel
					domainState.sliceCountWithLeader = domainState.podCountWithLeader / sliceSizeOnLevel
				}
			}

			domainState := s.domainStateOf(domain)
			addCurrFitDomain := s.updateCountsToMinimumGeneric(sortedLowerDomains, domainState.podCount, domainState.leaderCount, sliceSizeOnLevel, state.unconstrained, sliceSizeOnLevel > 1)
			newCurrFitDomain = append(newCurrFitDomain, addCurrFitDomain...)
		}
		currFitDomain = newCurrFitDomain
	}

	assignments := make(map[kueue.PodSetReference]*utiltas.TopologyAssignment)

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

		assignments[leaderTasPodSetRequests.PodSet.Name] = s.buildAssignment(leaderFitDomains)
		currFitDomain = workerFitDomains
	}

	assignments[workersTasPodSetRequests.PodSet.Name] = s.buildAssignment(currFitDomain)

	return assignments, ""
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

	sliceKey := s.sliceLevelKeyWithDefault(r, s.lowestLevel())

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
		return new(s.lowestLevel())
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
		return new(s.lowestLevel())
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
		if s.domainStateOf(domain).leaderCount < leaderCount {
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
func (s *TASFlavorSnapshot) findLevelWithFitDomains(
	searchLevelIdx int,
	state *findTopologyAssignmentState,
) (int, []*domain, string) {
	domains := s.domainsPerLevel[searchLevelIdx]
	if len(domains) == 0 {
		return 0, nil, fmt.Sprintf("no topology domains at level: %s", s.levelKeys[searchLevelIdx])
	}
	levelDomains := slices.Collect(maps.Values(domains))
	sortedDomain := s.sortedDomainsWithLeader(levelDomains, state.unconstrained)
	topDomain := sortedDomain[0]

	sliceCount := state.count / state.sliceSize
	if useBestFitAlgorithm(state.unconstrained) && s.domainStateOf(topDomain).sliceCountWithLeader >= sliceCount && s.domainStateOf(topDomain).leaderCount >= state.leaderCount {
		// optimize the potentially last domain
		topDomain = s.findBestFitDomainForSlices(sortedDomain, sliceCount, state.leaderCount)
	}
	notFitReason := func(slicesFitCount, totalRequestsSlicesCount int32) string {
		if len(state.multiLayerConstraints) > 0 {
			return s.multiLayerNotFitMessage(searchLevelIdx, state.count, state.multiLayerConstraints, state.stats)
		}
		return s.notFitMessage(slicesFitCount, totalRequestsSlicesCount, state.sliceSize, state.stats)
	}

	if useLeastFreeCapacityAlgorithm(state.unconstrained) {
		for _, candidateDomain := range sortedDomain {
			candidateDomainState := s.domainStateOf(candidateDomain)
			candidateCapacity := candidateDomainState.sliceCount
			if state.leaderCount > 0 {
				if candidateDomainState.leaderCount < state.leaderCount {
					continue
				}
				candidateCapacity = candidateDomainState.sliceCountWithLeader
			}
			if candidateCapacity >= sliceCount {
				return searchLevelIdx, []*domain{candidateDomain}, ""
			}
		}
		if state.required {
			maxCapacityFound := s.domainStateOf(sortedDomain[len(sortedDomain)-1]).podCount
			return 0, nil, notFitReason(maxCapacityFound, sliceCount)
		}
	}
	if s.domainStateOf(topDomain).sliceCountWithLeader < sliceCount || s.domainStateOf(topDomain).leaderCount < state.leaderCount {
		if state.required {
			// Scan remaining domains to support preferred affinity before failing
			if features.Enabled(features.TASRespectNodeAffinityPreferred) {
				for i := 1; i < len(sortedDomain); i++ {
					d := sortedDomain[i]
					if s.domainStateOf(d).sliceCountWithLeader >= sliceCount && s.domainStateOf(d).leaderCount >= state.leaderCount {
						return searchLevelIdx, []*domain{s.findBestFitDomainForSlices(sortedDomain[i:], sliceCount, state.leaderCount)}, ""
					}
				}
			}
			return 0, nil, notFitReason(s.domainStateOf(topDomain).sliceCount, sliceCount)
		}
		if searchLevelIdx > 0 && !state.unconstrained {
			return s.findLevelWithFitDomains(searchLevelIdx-1, state)
		}
		results := []*domain{}
		remainingSliceCount := sliceCount
		remainingLeaderCount := state.leaderCount
		// Prioritize before selecting the fitting set, since later descent cannot
		// recover a feasible leader domain omitted here. updateCountsToMinimumGeneric
		// repeats this for each newly produced domain set during descent.
		sortedDomain = s.prioritizeLeaderDomain(sortedDomain, state.count, state.leaderCount, state.sliceSize, true)

		// Assign leaders first from a domain that preserves total worker capacity.
		// After assigning all leaders, sort the remaining domains by worker capacity
		// and assign the remaining workers.
		idx := 0
		for ; remainingLeaderCount > 0 && idx < len(sortedDomain) && s.domainStateOf(sortedDomain[idx]).leaderCount > 0; idx++ {
			domain := sortedDomain[idx]
			if useBestFitAlgorithm(state.unconstrained) && s.domainStateOf(sortedDomain[idx]).sliceCountWithLeader >= remainingSliceCount {
				// optimize the last domain
				domain = s.findBestFitDomainForSlices(sortedDomain[idx:], remainingSliceCount, remainingLeaderCount)
			}
			results = append(results, domain)

			remainingLeaderCount -= s.domainStateOf(domain).leaderCount
			remainingSliceCount -= s.domainStateOf(domain).sliceCountWithLeader
		}
		if remainingLeaderCount > 0 {
			return 0, nil, notFitReason(state.leaderCount-remainingLeaderCount, sliceCount)
		}

		// At this point we have assigned all leaders, so we sort remaining domains based on worker capacity
		// and assign remaining workers.
		sortedDomain = s.sortedDomains(sortedDomain[idx:], state.unconstrained)
		for idx := 0; remainingSliceCount > 0 && idx < len(sortedDomain); idx++ {
			domain := sortedDomain[idx]
			if useBestFitAlgorithm(state.unconstrained) && s.domainStateOf(sortedDomain[idx]).sliceCount >= remainingSliceCount {
				// optimize the last domain
				domain = s.findBestFitDomainForSlices(sortedDomain[idx:], remainingSliceCount, 0)
			}
			results = append(results, domain)

			remainingSliceCount -= s.domainStateOf(domain).sliceCount
		}
		if remainingSliceCount > 0 {
			return 0, nil, notFitReason(sliceCount-remainingSliceCount, sliceCount)
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
func (s *TASFlavorSnapshot) prioritizeLeaderDomain(domains []*domain, count, leaderCount, sliceSize int32, slicesEnabled bool) []*domain {
	if leaderCount == 0 || len(domains) < 2 {
		return domains
	}

	requiredCapacity := count
	availableCapacity := int32(0)
	if slicesEnabled {
		requiredCapacity /= sliceSize
		for _, domain := range domains {
			availableCapacity += s.domainStateOf(domain).sliceCount
		}
	} else {
		for _, domain := range domains {
			availableCapacity += s.domainStateOf(domain).podCount
		}
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

func (s *TASFlavorSnapshot) updateCountsToMinimumGeneric(domains []*domain, count int32, leaderCount int32, sliceSize int32, unconstrained bool, slices bool) []*domain {
	domains = s.prioritizeLeaderDomain(domains, count, leaderCount, sliceSize, slices)
	result := make([]*domain, 0)
	remainingPrimary := count
	if slices {
		remainingPrimary = count / sliceSize
	}
	remainingLeaderCount := leaderCount

	for i, dom := range domains {
		if remainingLeaderCount > 0 {
			var d *domain
			var completed bool
			if slices {
				d, completed = s.consumeWithLeadersGeneric(
					dom,
					domains[i:],
					&remainingPrimary,
					&remainingLeaderCount,
					unconstrained,
					&s.domainStateOf(dom).sliceCountWithLeader,
					&s.domainStateOf(dom).sliceCount,
					sliceSize,
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
			if completed {
				return result
			}
			continue
		}

		// No leaders remaining: handle tail without leaders
		if slices {
			if useBestFitAlgorithm(unconstrained) && s.domainStateOf(dom).sliceCount >= remainingPrimary {
				// optimize the last domain
				dom = s.findBestFitDomainForSlices(domains[i:], remainingPrimary, 0)
			}
			domainState := s.domainStateOf(dom)
			domainState.leaderCount = 0
			if domainState.sliceCount >= remainingPrimary {
				domainState.podCount = remainingPrimary * sliceSize
				domainState.sliceCount = remainingPrimary
				result = append(result, dom)
				return result
			}
			domainState.podCount = domainState.sliceCount * sliceSize
			remainingPrimary -= domainState.sliceCount
			result = append(result, dom)
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
		"sliceSize", sliceSize,
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

// buildTopologyAssignmentForLevels build TopologyAssignment for levels starting from levelIdx
func (s *TASFlavorSnapshot) buildTopologyAssignmentForLevels(domains []*domain, levelIdx int) *utiltas.TopologyAssignment {
	assignment := &utiltas.TopologyAssignment{
		Domains: make([]utiltas.TopologyDomainAssignment, 0),
	}
	assignment.Levels = s.levelKeys[levelIdx:]
	for _, domain := range domains {
		if s.domainStateOf(domain).podCount == 0 {
			// It may happen when PodSet count is 0 or when using LeastFreeCapacity algorithm.
			continue
		}
		assignment.Domains = append(assignment.Domains, utiltas.TopologyDomainAssignment{
			Values: domain.levelValues[levelIdx:],
			Count:  s.domainStateOf(domain).podCount,
		})
	}
	return assignment
}

func (s *TASFlavorSnapshot) buildAssignment(domains []*domain) *utiltas.TopologyAssignment {
	// lex sort domains by their levelValues instead of IDs, as leaves' IDs can only contain the hostname
	slices.SortFunc(domains, s.compareDomainLevelValues)
	levelIdx := 0
	// assign only hostname values if topology defines it
	if s.isLowestLevelNode {
		levelIdx = len(s.levelKeys) - 1
	}
	return s.buildTopologyAssignmentForLevels(domains, levelIdx)
}

func (s *TASFlavorSnapshot) lowerLevelDomains(domains []*domain) []*domain {
	result := make([]*domain, 0, len(domains))
	for _, domain := range domains {
		result = append(result, domain.children...)
	}
	return result
}

func (s *TASFlavorSnapshot) compareDomainLevelValues(a, b *domain) int {
	if s.isLowestLevelNode && a.parent == b.parent {
		return strings.Compare(a.levelValues[len(a.levelValues)-1], b.levelValues[len(b.levelValues)-1])
	}
	return compareDomainLevelValues(a, b)
}

func compareDomainLevelValues(a, b *domain) int {
	return slices.CompareFunc(a.levelValues, b.levelValues, strings.Compare)
}

func (s *TASFlavorSnapshot) sortedDomainsWithLeader(domains []*domain, unconstrained bool) []*domain {
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
	return result
}

// This function sorts domains based on a specified algorithm: BestFit or LeastFreeCapacity.
//
// The sorting criteria are:
// - **BestFit**: `sliceCount` (descending), `podCount` (ascending), `levelValues` (ascending)
// - **LeastFreeCapacity**: `sliceCount` (ascending), `podCount` (ascending), `levelValues` (ascending)
//
// `podCount` is always sorted ascending. This prioritizes domains that can accommodate slices with minimal leftover pod capacity.
func (s *TASFlavorSnapshot) sortedDomains(domains []*domain, unconstrained bool) []*domain {
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
	return result
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
	if features.Enabled(features.TASCacheNodeMatchResults) {
		matchingLeaves, stats, err := s.getMatchingLeaves(ctx, requirements)
		if err != nil {
			return err
		}
		state.stats.add(stats)
		for _, ml := range matchingLeaves {
			s.domainStateOf(&ml.leaf.domain).affinityScore = ml.score
			s.fillLeafCounts(ml.leaf, requirements, state, cachingRemainingResourcesEnabled)
		}
	} else {
		if s.isLowestLevelNode {
			feasibleLeaves, err := s.simulatorSnapshot.FindFeasibleNodes(ctx, simulator.AsCandidates(s.candidates()), &requirements.podRequirements, &state.stats.NodeExclusionStats)

			if err != nil {
				return err
			}

			for _, ml := range feasibleLeaves {
				leaf := s.leaves[ml.GetID()]
				s.domainStateOf(&leaf.domain).affinityScore = ml.GetAffinityScore()
				s.fillLeafCounts(leaf, requirements, state, cachingRemainingResourcesEnabled)
			}
		} else {
			state.stats.TotalNodes += len(s.leaves)
			for candidate := range s.candidates() {
				s.fillLeafCounts(candidate.leaf, requirements, state, cachingRemainingResourcesEnabled)
			}
		}
	}

	for _, root := range s.roots {
		s.fillInCountsHelper(root, state.sliceSize, state.sliceLevelIdx, 0, state.sliceSizeAtLevel, state.leaderCount > 0)
	}
	return nil
}

func (s *TASFlavorSnapshot) getMatchingLeaves(ctx context.Context, requirements *topologyAssignmentPodRequirements) ([]matchedLeaf, *tasExclusionStats, error) {
	if !s.isLowestLevelNode {
		stats := newTASExclusionStats()
		stats.TotalNodes += len(s.leaves)
		result := make([]matchedLeaf, 0, len(s.leaves))
		for candidate := range s.candidates() {
			result = append(result, matchedLeaf{leaf: s.leaves[candidate.GetID()], score: candidate.GetAffinityScore()})
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
	feasibleLeaves, err := s.simulatorSnapshot.FindFeasibleNodes(ctx, simulator.AsCandidates(s.candidates()), &requirements.podRequirements, &leafStats.NodeExclusionStats)
	if err != nil {
		return nil, nil, err
	}
	matched := make([]matchedLeaf, 0, len(feasibleLeaves))
	for _, candidate := range feasibleLeaves {
		matched = append(matched, matchedLeaf{leaf: s.leaves[candidate.GetID()], score: candidate.GetAffinityScore()})
	}
	entry := &matchingLeavesCacheEntry{
		leaves: matched,
		stats:  leafStats,
	}

	if requirements.matchKey != nil {
		if s.matchingLeavesCache == nil {
			s.matchingLeavesCache = make(map[podSetMatchKey]*matchingLeavesCacheEntry)
		}
		s.matchingLeavesCache[*requirements.matchKey] = entry
	}

	return entry.leaves, entry.stats, nil
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
	remainingCapacity := s.remainingCapacityForLeaf(leaf, requirements.simulateEmpty, cachingRemainingResourcesEnabled)

	if leafAssumedUsage, found := requirements.assumedUsage[leaf.id]; found {
		remainingCapacity.Sub(leafAssumedUsage)
	}
	var limitingRes corev1.ResourceName
	leafDomainState := s.domainStateOf(&leaf.domain)
	leafDomainState.podCount, limitingRes = requirements.requests.CountInWithLimitingResource(remainingCapacity.Get())

	// Track resource exclusions: if this node can't fit even one pod,
	// identify which resource is the bottleneck.
	if leafDomainState.podCount == 0 && limitingRes != "" {
		state.stats.recordResourceExclusion(limitingRes)
	}

	leafDomainState.leaderCount = 0
	if requirements.leaderRequests != nil && requirements.leaderRequests.CountIn(remainingCapacity.Get()) > 0 {
		leafDomainState.leaderCount = 1
		remainingCapacity.Sub(requirements.leaderRequests)
	}

	leafDomainState.podCountWithLeader = requirements.requests.CountIn(remainingCapacity.Get())
}

func (s *TASFlavorSnapshot) fillInCountsHelper(domain *domain, sliceSize int32, sliceLevelIdx int, level int, sliceSizeAtLevel map[int]int32, leaderRequired bool) {
	domainState := s.domainStateOf(domain)
	// logic for a leaf
	if len(domain.children) == 0 {
		if level == sliceLevelIdx {
			// initialize the sliceCount if leaf is the request slice level
			domainState.sliceCount = domainState.podCount / sliceSize
			domainState.sliceCountWithLeader = domainState.podCountWithLeader / sliceSize
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

	// When multi-layer constraints exist, children at a constrained level
	// can only contribute pods in multiples of the inner slice size.
	// Round down each child's effective contribution so that the parent's
	// capacity accurately reflects what can actually be grouped.
	childLevel := level + 1
	innerSize, hasInnerConstraint := sliceSizeAtLevel[childLevel]

	for _, child := range domain.children {
		s.fillInCountsHelper(child, sliceSize, sliceLevelIdx, childLevel, sliceSizeAtLevel, leaderRequired)

		childDomainState := s.domainStateOf(child)
		childPodCount := childDomainState.podCount
		childPodCountWithLeader := childDomainState.podCountWithLeader
		if hasInnerConstraint {
			childPodCount = (childDomainState.podCount / innerSize) * innerSize
			childPodCountWithLeader = (childDomainState.podCountWithLeader / innerSize) * innerSize
		}

		childrenCapacity += childPodCount
		sliceCapacity += childDomainState.sliceCount
		if !leaderRequired || childDomainState.leaderCount > 0 {
			hasWithLeaderCapacityContributor = true
			minPodCountWithLeaderDifference = min(childPodCount-childPodCountWithLeader, minPodCountWithLeaderDifference)
			minSliceCountWithLeaderDifference = min(childDomainState.sliceCount-childDomainState.sliceCountWithLeader, minSliceCountWithLeaderDifference)
		}
		leaderCount = max(childDomainState.leaderCount, leaderCount)
		affinityScore += childDomainState.affinityScore
	}
	domainState.podCount = childrenCapacity
	sliceCountWithLeader := int32(0)
	if hasWithLeaderCapacityContributor {
		domainState.podCountWithLeader = childrenCapacity - minPodCountWithLeaderDifference
		sliceCountWithLeader = sliceCapacity - minSliceCountWithLeaderDifference
	} else {
		domainState.podCountWithLeader = 0
	}
	domainState.leaderCount = leaderCount
	domainState.affinityScore = affinityScore
	if level == sliceLevelIdx {
		// initialize the sliceCount for the requested slice level.
		sliceCapacity = domainState.podCount / sliceSize
		sliceCountWithLeader = domainState.podCountWithLeader / sliceSize
	}
	domainState.sliceCount = sliceCapacity
	domainState.sliceCountWithLeader = sliceCountWithLeader
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
	nodeLevel := len(s.levelKeys) - 1
	sortedDomains := make([]utiltas.TopologyDomainAssignment, 0, len(a.Domains)+len(b.Domains))
	sortedDomains = append(sortedDomains, a.Domains...)
	sortedDomains = append(sortedDomains, b.Domains...)
	slices.SortFunc(sortedDomains, func(a, b utiltas.TopologyDomainAssignment) int {
		aDomain := s.domainsPerLevel[nodeLevel][utiltas.DomainID(a.Values)]
		bDomain := s.domainsPerLevel[nodeLevel][utiltas.DomainID(b.Values)]
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

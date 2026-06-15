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
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	corev1helpers "k8s.io/component-helpers/scheduling/corev1"
	"k8s.io/component-helpers/scheduling/corev1/nodeaffinity"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/podset"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	"sigs.k8s.io/kueue/pkg/workload"
)

var (
	errCodeAssumptionsViolated = errors.New("code assumptions violated")
)

// domain holds the static information about placement of a topology
// domain in the hierarchy of topology domains.
type domain struct {
	// id is the globally unique id of the domain
	id utiltas.TopologyDomainID

	// parent points to domain which is a parent in topology structure
	parent *domain

	// children points to domains which are children in topology structure
	children []*domain

	// state is a temporary state of the topology domains during the
	// assignment algorithm.
	//
	// In the first phase of the algorithm (traversal to the top the topology to
	// determine the level to fit the workload) it denotes the number of pods
	// which can fit in a given domain.
	//
	// In the second phase of the algorithm (traversal to the bottom to
	// determine the actual assignments) it denotes the number of pods actually
	// assigned to the given domain.
	state int32

	// sliceState is a temporary state of the topology domains during the
	// assignment algorithm that denotes the number of slices that can fit within
	// that domain.
	//
	// For domains that are below the requested topology level the algorithm
	// assigns 0 to that field as this field makes no sense for lower level
	// domains.
	sliceState int32

	stateWithLeader      int32
	sliceStateWithLeader int32
	leaderState          int32

	// levelValues stores the mapping from domain ID back to the
	// ordered list of values
	levelValues []string

	// affinityScore is the sum of weights of all preferred affinity terms that match the node.
	// For non-leaf domains, it is the sum of affinity scores of all children.
	affinityScore int64
}

// leafDomain extends the domain with information for the lowest-level domain.
type leafDomain struct {
	domain
	// freeCapacity represents the total node capacity minus the non-TAS usage,
	// coming from Pods which are not managed by workloads admitted by TAS
	// (typically static Pods, DaemonSets, or Deployments).
	freeCapacity resources.Requests

	// tasUsage represents the usage associated with TAS workloads.
	tasUsage resources.Requests

	// node at the leaf, if the lowest level is a node
	node *nodeInfo
}

type domainByID map[utiltas.TopologyDomainID]*domain
type leafDomainByID map[utiltas.TopologyDomainID]*leafDomain

type TASFlavorSnapshot struct {
	log logr.Logger

	// topologyName indicates the name of the topology specified in the
	// ResourceFlavor spec.topologyName field.
	topologyName kueue.TopologyReference

	// levelKeys denotes the ordered list of topology keys set as label keys
	// on the Topology object
	levelKeys []string

	// leaves maps domainID to domains that are at the lowest level of topology structure
	leaves leafDomainByID

	// roots maps domainID to domains that are at the highest level of topology structure
	roots domainByID

	// domains maps domainID to every domain available in the topology structure
	domains domainByID

	// domainsPerLevel stores the static tree information
	domainsPerLevel []domainByID

	// tolerations represents the list of tolerations defined for the resource flavor
	tolerations []corev1.Toleration

	// isLowestLevelNode indicates if kubernetes.io/hostname is the lowest topology level
	isLowestLevelNode bool
}

func newTASFlavorSnapshot(log logr.Logger, topologyName kueue.TopologyReference,
	levels []string, tolerations []corev1.Toleration) *TASFlavorSnapshot {
	domainsPerLevel := make([]domainByID, len(levels))
	for level := range levels {
		domainsPerLevel[level] = make(domainByID)
	}

	snapshot := &TASFlavorSnapshot{
		log:               log,
		topologyName:      topologyName,
		levelKeys:         slices.Clone(levels),
		leaves:            make(leafDomainByID),
		tolerations:       slices.Clone(tolerations),
		domains:           make(domainByID),
		roots:             make(domainByID),
		domainsPerLevel:   domainsPerLevel,
		isLowestLevelNode: len(levels) > 0 && levels[len(levels)-1] == corev1.LabelHostname,
	}
	return snapshot
}

func (s *TASFlavorSnapshot) addNode(node *nodeInfo) utiltas.TopologyDomainID {
	var levelValues []string
	var domainID utiltas.TopologyDomainID
	var leafFound bool

	if s.isLowestLevelNode {
		// When the lowest level is kubernetes.io/hostname, directly extract hostname.
		hostname := node.Labels[corev1.LabelHostname]
		domainID = utiltas.TopologyDomainID(hostname)
		_, leafFound = s.leaves[domainID]
		// Only compute levelValues when we actually need to create a new leafDomain.
		if !leafFound {
			levelValues = utiltas.LevelValues(s.levelKeys, node.Labels)
		}
	} else {
		// Compute full level values and domain ID.
		levelValues = utiltas.LevelValues(s.levelKeys, node.Labels)
		domainID = utiltas.DomainID(levelValues)
		_, leafFound = s.leaves[domainID]
	}
	if !leafFound {
		leafDomain := leafDomain{
			domain: domain{
				id:          domainID,
				levelValues: levelValues,
			},
		}
		if s.isLowestLevelNode {
			leafDomain.node = node
		}
		s.leaves[domainID] = &leafDomain
	}
	capacity := resources.NewRequests(node.Allocatable)
	s.addCapacity(domainID, capacity)
	return domainID
}

func (s *TASFlavorSnapshot) lowestLevel() string {
	return s.levelKeys[len(s.levelKeys)-1]
}

func (s *TASFlavorSnapshot) highestLevel() string {
	return s.levelKeys[0]
}

// initialize prepares the topology tree structure. This structure holds
// for a given the list of topology domains with additional static and dynamic
// information. This function initializes the static information which
// represents the edges to the parent and child domains. This structure is
// reused for multiple workloads during a single scheduling cycle.
func (s *TASFlavorSnapshot) initialize() {
	for _, leafDomain := range s.leaves {
		domain := &leafDomain.domain
		s.domains[domain.id] = domain
		s.domainsPerLevel[len(domain.levelValues)-1][domain.id] = domain
		s.initializeHelper(domain)
	}
}

// initializeHelper is a recursive helper for initialize() method
func (s *TASFlavorSnapshot) initializeHelper(dom *domain) {
	if len(dom.levelValues) == 1 {
		s.roots[dom.id] = dom
		return
	}
	parentValues := dom.levelValues[:len(dom.levelValues)-1]
	parentID := utiltas.DomainID(parentValues)
	parent, parentFound := s.domains[parentID]
	if !parentFound {
		// create parent
		parent = &domain{
			id:          parentID,
			levelValues: parentValues,
		}
		s.domainsPerLevel[len(parentValues)-1][parentID] = parent
		s.domains[parentID] = parent
		s.initializeHelper(parent)
	}
	// connect parent and child
	dom.parent = parent
	parent.children = append(parent.children, dom)
}

func (s *TASFlavorSnapshot) addCapacity(domainID utiltas.TopologyDomainID, capacity resources.Requests) {
	if s.leaves[domainID].freeCapacity == nil {
		s.leaves[domainID].freeCapacity = resources.Requests{}
	}
	s.leaves[domainID].freeCapacity.Add(capacity)
}

func (s *TASFlavorSnapshot) addNonTASUsage(domainID utiltas.TopologyDomainID, usage resources.Requests) {
	// The usage for non-TAS pods is only accounted for "TAS" nodes  - with at
	// least one TAS pod, and so the addCapacity function to initialize
	// freeCapacity is already called.
	s.leaves[domainID].freeCapacity.Sub(usage)
}

func (s *TASFlavorSnapshot) updateTASUsage(domainID utiltas.TopologyDomainID, usage resources.Requests, op usageOp, count int32) {
	u := usage.Clone()
	u.Add(resources.Requests{corev1.ResourcePods: int64(count)})
	if op == add {
		s.addTASUsage(domainID, u)
	} else {
		s.removeTASUsage(domainID, u)
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
	if s.leaves[domainID].tasUsage == nil {
		s.leaves[domainID].tasUsage = resources.Requests{}
	}
	s.leaves[domainID].tasUsage.Add(usage)
}

func (s *TASFlavorSnapshot) removeTASUsage(domainID utiltas.TopologyDomainID, usage resources.Requests) {
	if s.leaves[domainID] == nil {
		// this can happen if there is an admitted workload for which the
		// backing node was deleted or is no longer Ready (so the addCapacity
		// function was not called).
		s.log.V(3).Info("skip removing TAS usage in domain", "domain", domainID, "usage", usage)
		return
	}
	if s.leaves[domainID].tasUsage == nil {
		s.leaves[domainID].tasUsage = resources.Requests{}
	}
	s.leaves[domainID].tasUsage.Sub(usage)
}

func (s *TASFlavorSnapshot) freeCapacityPerDomain() map[utiltas.TopologyDomainID]resources.Requests {
	freeCapacityPerDomain := make(map[utiltas.TopologyDomainID]resources.Requests, len(s.leaves))

	for domainID, leaf := range s.leaves {
		freeCapacityPerDomain[domainID] = leaf.freeCapacity.Clone()
	}

	return freeCapacityPerDomain
}

func (s *TASFlavorSnapshot) tasUsagePerDomain() map[utiltas.TopologyDomainID]resources.Requests {
	tasUsagePerDomain := make(map[utiltas.TopologyDomainID]resources.Requests, len(s.leaves))

	for domainID, leaf := range s.leaves {
		tasUsagePerDomain[domainID] = leaf.tasUsage.Clone()
	}

	return tasUsagePerDomain
}

type domainCapacityDetails struct {
	FreeCapacity map[corev1.ResourceName]string `json:"freeCapacity"`
	TasUsage     map[corev1.ResourceName]string `json:"tasUsage"`
}

func (s *TASFlavorSnapshot) SerializeFreeCapacityPerDomain() (string, error) {
	freeCapacityPerDomain := s.freeCapacityPerDomain()
	tasUsagePerDomain := s.tasUsagePerDomain()

	details := make(map[utiltas.TopologyDomainID]domainCapacityDetails, len(s.leaves))

	for _, domain := range slices.Sorted(maps.Keys(freeCapacityPerDomain)) {
		freeCapacity := freeCapacityPerDomain[domain]
		tasUsage := tasUsagePerDomain[domain]

		freeCapacityDetails := make(map[corev1.ResourceName]string, len(freeCapacity))
		for _, resourceName := range slices.Sorted(maps.Keys(freeCapacity)) {
			value := freeCapacity[resourceName]
			freeCapacityDetails[resourceName] = resources.ResourceQuantityString(resourceName, value)
		}

		tasUsageDetails := make(map[corev1.ResourceName]string, len(freeCapacity))
		for _, resourceName := range slices.Sorted(maps.Keys(tasUsage)) {
			value := tasUsage[resourceName]
			tasUsageDetails[resourceName] = resources.ResourceQuantityString(resourceName, value)
		}

		details[domain] = domainCapacityDetails{
			FreeCapacity: freeCapacityDetails,
			TasUsage:     tasUsageDetails,
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
}

type TASAssignmentsResult map[kueue.PodSetReference]tasPodSetAssignmentResult

func (r TASAssignmentsResult) Failure() *FailureInfo {
	for psName, psAssignment := range r {
		if psAssignment.FailureReason != "" {
			return &FailureInfo{PodSetName: psName, Reason: psAssignment.FailureReason}
		}
	}
	return nil
}

type tasPodSetAssignmentResult struct {
	TopologyAssignment *utiltas.TopologyAssignment
	FailureReason      string
}

type FlavorTASRequests []TASPodSetRequests

// Fits checks if the snapshot has enough capacity to accommodate the workload
func (s *TASFlavorSnapshot) Fits(flavorUsage workload.TASFlavorUsage) bool {
	for _, domainUsage := range flavorUsage {
		domainID := utiltas.DomainID(domainUsage.Values)
		leaf, found := s.leaves[domainID]
		if !found {
			return false
		}
		remainingCapacity := leaf.freeCapacity.Clone()
		remainingCapacity.Sub(leaf.tasUsage)
		if domainUsage.SinglePodRequests.CountIn(remainingCapacity) < domainUsage.Count {
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

// ExclusionStats tracks why nodes were excluded during TAS scheduling.
type ExclusionStats struct {
	Taints         map[string]int
	NodeSelector   int
	Affinity       int
	TopologyDomain int
	Resources      map[corev1.ResourceName]int
	TotalNodes     int
}

// topologyAssignmentPodRequirements stores pod-driven scheduling filters and
// resource inputs that are only needed while filling per-domain counts.
type topologyAssignmentPodRequirements struct {
	requests                  resources.Requests
	leaderRequests            *resources.Requests
	assumedUsage              map[utiltas.TopologyDomainID]resources.Requests
	tolerations               []corev1.Toleration
	selector                  labels.Selector
	affinitySelector          *nodeaffinity.NodeSelector
	preferredSchedulingTerms  *nodeaffinity.PreferredSchedulingTerms
	requiredReplacementDomain utiltas.TopologyDomainID
	simulateEmpty             bool
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
	stats *ExclusionStats
}

func newExclusionStats() *ExclusionStats {
	return &ExclusionStats{
		Taints:    make(map[string]int),
		Resources: make(map[corev1.ResourceName]int),
	}
}

// hasExclusions returns true if any exclusion reasons were recorded.
func (s *ExclusionStats) hasExclusions() bool {
	return s.NodeSelector > 0 || s.Affinity > 0 || s.TopologyDomain > 0 ||
		len(s.Taints) > 0 || len(s.Resources) > 0
}

// formatReasons returns a sorted, comma-separated string of exclusion reasons.
func (s *ExclusionStats) formatReasons() string {
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
	for _, taint := range slices.Sorted(maps.Keys(s.Taints)) {
		reasons = append(reasons, fmt.Sprintf("taint %q: %d", taint, s.Taints[taint]))
	}
	for _, resource := range slices.Sorted(maps.Keys(s.Resources)) {
		reasons = append(reasons, fmt.Sprintf("resource %q: %d", resource, s.Resources[resource]))
	}
	slices.Sort(reasons)
	return strings.Join(reasons, ", ")
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
func (s *TASFlavorSnapshot) FindTopologyAssignmentsForFlavor(flavorTASRequests FlavorTASRequests, options ...FindTopologyAssignmentsOption) TASAssignmentsResult {
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
				// We deepCopy the existing TopologyAssignment, so if we delete unwanted domain,
				// And there is no fit, we have the original newAssignment to retry with
				newAssignment, replacementAssignment, reason := s.findReplacementAssignment(&tr, utiltas.InternalFrom(psa.TopologyAssignment), opts.workload, assumedUsage)
				result[tr.PodSet.Name] = tasPodSetAssignmentResult{TopologyAssignment: newAssignment, FailureReason: reason}
				if reason != "" {
					return result
				}
				addAssumedUsage(assumedUsage, replacementAssignment, &tr)
			}
		} else {
			// Generalized PodSet group path: when the feature gate is enabled
			// and the group requests required topology, place the whole group
			// within a single co-located domain using the generalized
			// bin-packer. Everything else (gate disabled, or preferred
			// topology) uses the legacy leader-worker path below. See the
			// "Generalizing PodSet Groups beyond leader-worker" section in
			// keps/2724-topology-aware-scheduling.
			if features.Enabled(features.TASPodSetGroupGeneralization) && isRequiredPodSetGroup(trs) {
				groupAssignments, reason := s.findGeneralizedGroupAssignment(trs, assumedUsage, opts.simulateEmpty)
				for _, tr := range trs {
					result[tr.PodSet.Name] = tasPodSetAssignmentResult{TopologyAssignment: groupAssignments[tr.PodSet.Name], FailureReason: reason}
				}
				if reason != "" {
					return result
				}
				for _, tr := range trs {
					addAssumedUsage(assumedUsage, groupAssignments[tr.PodSet.Name], &tr)
				}
				continue
			}

			leader, workers := findLeaderAndWorkers(trs)

			// Handle elastic workloads with delta-only placement
			if features.Enabled(features.ElasticJobsViaWorkloadSlicesWithTAS) {
				elasticResult := s.handleElasticWorkload(workers, leader, assumedUsage, opts)
				if elasticResult.applied {
					maps.Copy(result, elasticResult.assignments)
					if elasticResult.assignments[workers.PodSet.Name].FailureReason != "" {
						return result
					}
					continue
				}
			}

			// Normal path: no previous assignment or stale assignment
			assignments, reason := s.findTopologyAssignment(workers, leader, assumedUsage, opts.simulateEmpty, "")
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

// isRequiredPodSetGroup reports whether a PodSet group should be handled by the
// generalized N-PodSet placement path. With the TASPodSetGroupGeneralization
// feature gate enabled, the new path handles required-topology groups EXCEPT the
// legacy leader-worker shape (exactly two PodSets, one with a single replica),
// which stays on the legacy path to preserve its merged leader/worker rank
// layout (its pods are not interchangeable). Preferred groups also use the
// legacy path.
func isRequiredPodSetGroup(trs FlavorTASRequests) bool {
	if len(trs) < 2 || !isRequired(trs[0].PodSet.TopologyRequest) {
		return false
	}
	return !isLegacyLeaderWorkerShape(len(trs), func(i int) int32 { return trs[i].Count })
}

// isLegacyLeaderWorkerShape reports whether a group is the legacy leader-worker
// shape: exactly two PodSets where at least one has a single replica.
func isLegacyLeaderWorkerShape(numPodSets int, count func(i int) int32) bool {
	return numPodSets == 2 && (count(0) == 1 || count(1) == 1)
}

// findGeneralizedGroupAssignment co-locates all PodSets sharing a
// podset-group-name within a single required-level topology domain and returns
// a per-PodSet TopologyAssignment.
//
// Required topology only. Each PodSet keeps its real per-pod footprint. The
// algorithm:
//
//  1. Compute the group's real aggregate demand and, as a fast pre-filter,
//     restrict to required-level domains whose aggregate free capacity can hold
//     it (a domain that fails this can never pack the group).
//  2. Among the surviving domains (tightest fit first), run a single greedy
//     best-fit pass that places all the group's pods (each with its own
//     footprint, on nodes its PodSet is allowed to run on) onto the domain's
//     leaves. This is near-optimal but NOT exact: a fragmented-but-packable
//     group may be reported as not fitting (a conservative false-negative).
//  3. Build one TopologyAssignment per PodSet from the placement.
func (s *TASFlavorSnapshot) findGeneralizedGroupAssignment(
	trs FlavorTASRequests,
	assumedUsage map[utiltas.TopologyDomainID]resources.Requests,
	simulateEmpty bool,
) (map[kueue.PodSetReference]*utiltas.TopologyAssignment, string) {
	// Compute the group's real aggregate demand: sum of count_i * footprint_i
	// across all PodSets, plus one Pods resource per pod.
	groupDemand := resources.Requests{}
	var totalCount int32
	for i := range trs {
		groupDemand.Add(trs[i].SinglePodRequests.ScaledUp(int64(trs[i].Count)))
		totalCount += trs[i].Count
	}
	groupDemand.Add(resources.Requests{corev1.ResourcePods: int64(totalCount)})

	// Build each PodSet's node-scheduling constraints (tolerations, selector,
	// required affinity, merged with PodSetUpdates and flavor tolerations) so
	// the packer only places a PodSet's pods on nodes that PodSet can run on.
	constraints := make([]podSetNodeConstraints, len(trs))
	for i := range trs {
		c, reason := s.podSetNodeRequirements(&trs[i])
		if reason != "" {
			return nil, reason
		}
		constraints[i] = c
	}

	// Resolve the required level shared by all PodSets in the group (validation
	// guarantees they request the same required topology).
	topologyKey := s.levelKeyWithImpliedFallback(&trs[0])
	if topologyKey == nil {
		return nil, "topology level not specified"
	}
	requiredLevelIdx, found := s.resolveLevelIdx(*topologyKey)
	if !found {
		return nil, fmt.Sprintf("no requested topology level: %s", *topologyKey)
	}

	// Try required-level domains, choosing the best-fit one (consistent with
	// the single-PodSet BestFit algorithm: among domains that fit, prefer the
	// one with the least remaining free capacity, keeping larger domains free).
	// Required groups are never unconstrained, so BestFit always applies.
	//
	// We first cheaply pre-filter domains by aggregate free capacity (a
	// necessary, not sufficient, condition) and rank the survivors tightest-fit
	// first. Then we run the greedy packer in that order and return the first
	// domain that admits a placement -- so we do the minimum work and naturally
	// fall back to a looser domain if a tighter one cannot be packed.
	type candidate struct {
		domainID   utiltas.TopologyDomainID
		leaves     []*leafDomain
		capacities []resources.Requests
		slack      int32
	}
	domainsByID := s.domainsPerLevel[requiredLevelIdx]
	candidates := make([]candidate, 0, len(domainsByID))
	for _, domainID := range slices.Sorted(maps.Keys(domainsByID)) {
		leaves := s.leavesUnder(domainsByID[domainID])
		capacities := make([]resources.Requests, len(leaves))
		aggregate := resources.Requests{}
		for i, leaf := range leaves {
			capacity := leaf.freeCapacity.Clone()
			if !simulateEmpty {
				capacity.Sub(leaf.tasUsage)
			}
			if used, ok := assumedUsage[leaf.id]; ok {
				capacity.Sub(used)
			}
			capacities[i] = capacity
			aggregate.Add(capacity)
		}
		slack := groupDemand.CountIn(aggregate)
		if slack < 1 {
			// Domain cannot even aggregate-fit the group; skip it.
			continue
		}
		candidates = append(candidates, candidate{
			domainID:   domainID,
			leaves:     leaves,
			capacities: capacities,
			slack:      slack,
		})
	}
	// Tightest fit first; ties broken by domain ID for deterministic placement.
	slices.SortFunc(candidates, func(a, b candidate) int {
		if a.slack != b.slack {
			return int(a.slack - b.slack)
		}
		return cmp.Compare(a.domainID, b.domainID)
	})
	for _, c := range candidates {
		if result := s.packGroupOntoLeaves(trs, constraints, c.leaves, c.capacities); result != nil {
			return result, ""
		}
	}
	return nil, fmt.Sprintf("no topology domain at level %s can fit the pod set group", *topologyKey)
}

// packGroupOntoLeaves attempts to place every pod of every PodSet in trs onto
// the given leaves (with the given remaining capacities) such that no leaf is
// oversubscribed and every pod lands on a node its PodSet is allowed to run on
// (per-PodSet node constraints). It returns one TopologyAssignment per PodSet,
// or nil if the greedy pass could not place all pods.
//
// This is a single greedy best-fit First-Fit-Decreasing pass: pods are placed
// largest-first, each on the eligible leaf that leaves the least
// scarcity-weighted residual capacity. It is O(pods * leaves * resources) and
// near-optimal, but not exact -- a fragmented-but-packable group may be reported
// as not fitting (a conservative false-negative; pods are never mis-placed).
func (s *TASFlavorSnapshot) packGroupOntoLeaves(
	trs FlavorTASRequests,
	constraints []podSetNodeConstraints,
	leaves []*leafDomain,
	capacities []resources.Requests,
) map[kueue.PodSetReference]*utiltas.TopologyAssignment {
	// Precompute, per PodSet, which leaves it may use (taints / selector /
	// required affinity). Only meaningful when nodes are the lowest level.
	eligible := make([][]bool, len(trs))
	for i := range trs {
		eligible[i] = make([]bool, len(leaves))
		for j, leaf := range leaves {
			if !s.isLowestLevelNode {
				eligible[i][j] = true
				continue
			}
			ok, _, _ := s.leafNodeEligibility(leaf, constraints[i].tolerations, constraints[i].selector, constraints[i].affinitySelector)
			eligible[i][j] = ok
		}
	}

	// Build the flat pod list: each entry records its PodSet and its footprint
	// (real per-pod requests plus one Pod slot).
	type pod struct {
		psIdx int
		need  resources.Requests
	}
	pods := make([]pod, 0)
	for i := range trs {
		need := trs[i].SinglePodRequests.Clone()
		need.Add(resources.Requests{corev1.ResourcePods: 1})
		for range int(trs[i].Count) {
			pods = append(pods, pod{psIdx: i, need: need})
		}
	}
	// First-Fit-Decreasing: place larger pods first (dominant-resource order).
	slices.SortStableFunc(pods, func(a, b pod) int {
		return compareFootprintDesc(a.need, b.need)
	})

	// Scarcity weights for the best-fit score: weigh each resource by the
	// inverse of its total capacity across the domain, so leftover capacity is
	// measured relative to how constrained that resource is (Panigrahy et al.,
	// "Heuristics for Vector Bin Packing"; Tetris, SIGCOMM 2014).
	totalCapacity := resources.Requests{}
	for _, c := range capacities {
		totalCapacity.Add(c)
	}

	remaining := make([]resources.Requests, len(capacities))
	for i := range capacities {
		remaining[i] = capacities[i].Clone()
	}
	placement := make([]int, len(pods))
	for podIdx := range pods {
		bestLeaf := -1
		var bestScore float64
		for leafIdx := range leaves {
			if !eligible[pods[podIdx].psIdx][leafIdx] {
				continue
			}
			if pods[podIdx].need.CountIn(remaining[leafIdx]) < 1 {
				continue
			}
			score := bestFitScore(pods[podIdx].need, remaining[leafIdx], totalCapacity)
			if bestLeaf == -1 || score < bestScore {
				bestLeaf = leafIdx
				bestScore = score
			}
		}
		if bestLeaf == -1 {
			return nil
		}
		remaining[bestLeaf].Sub(pods[podIdx].need)
		placement[podIdx] = bestLeaf
	}

	// Build per-PodSet assignments: for each PodSet, count pods per leaf.
	levelIdx := 0
	if s.isLowestLevelNode {
		levelIdx = len(s.levelKeys) - 1
	}
	// perPodSetLeafCounts[psIdx][leafIdx] = number of pods of that PodSet on the leaf.
	perPodSetLeafCounts := make([]map[int]int32, len(trs))
	for i := range perPodSetLeafCounts {
		perPodSetLeafCounts[i] = make(map[int]int32)
	}
	for podIdx := range pods {
		perPodSetLeafCounts[pods[podIdx].psIdx][placement[podIdx]]++
	}

	result := make(map[kueue.PodSetReference]*utiltas.TopologyAssignment, len(trs))
	for i := range trs {
		assignment := &utiltas.TopologyAssignment{
			Levels:  s.levelKeys[levelIdx:],
			Domains: make([]utiltas.TopologyDomainAssignment, 0, len(perPodSetLeafCounts[i])),
		}
		// Deterministic leaf ordering by levelValues.
		leafIdxs := slices.Collect(maps.Keys(perPodSetLeafCounts[i]))
		slices.SortFunc(leafIdxs, func(a, b int) int {
			return slices.Compare(leaves[a].levelValues, leaves[b].levelValues)
		})
		for _, leafIdx := range leafIdxs {
			assignment.Domains = append(assignment.Domains, utiltas.TopologyDomainAssignment{
				Values: leaves[leafIdx].levelValues[levelIdx:],
				Count:  perPodSetLeafCounts[i][leafIdx],
			})
		}
		result[trs[i].PodSet.Name] = assignment
	}
	return result
}

// bestFitScore returns the scarcity-weighted leftover capacity a leaf would
// have AFTER placing a pod of the given demand on it. A lower score is a tighter
// fit, so picking the minimum-score feasible leaf keeps roomier leaves available
// for larger pods and reduces fragmentation (best-fit). Each resource's leftover
// is weighted by its scarcity (inverse of total capacity) so a scarce resource
// such as GPUs dominates the decision over abundant ones such as CPU. Resources
// are iterated in sorted order so the float sum is reproducible (float addition
// is not associative; map iteration order is randomized).
func bestFitScore(need, residual, totalCapacity resources.Requests) float64 {
	var score float64
	for _, name := range slices.Sorted(maps.Keys(residual)) {
		total := totalCapacity[name]
		if total <= 0 {
			continue
		}
		leftover := max(residual[name]-need[name], 0)
		score += float64(leftover) / float64(total)
	}
	return score
}

// compareFootprintDesc orders footprints from largest to smallest using a
// dominant-resource style comparison (largest single resource value first, then
// remaining resources for determinism).
func compareFootprintDesc(a, b resources.Requests) int {
	names := make(map[corev1.ResourceName]struct{}, len(a)+len(b))
	for n := range a {
		names[n] = struct{}{}
	}
	for n := range b {
		names[n] = struct{}{}
	}
	sorted := slices.Sorted(maps.Keys(names))
	// Compare by the largest resource value first.
	var maxA, maxB int64
	for _, n := range sorted {
		maxA = max(maxA, a[n])
		maxB = max(maxB, b[n])
	}
	if maxA != maxB {
		return int(maxB - maxA)
	}
	// Tie-break deterministically by per-resource values.
	for _, n := range sorted {
		if a[n] != b[n] {
			return int(b[n] - a[n])
		}
	}
	return 0
}

// leavesUnder returns all leaf domains under the given domain (the domain
// itself if it is already a leaf).
func (s *TASFlavorSnapshot) leavesUnder(d *domain) []*leafDomain {
	if leaf, ok := s.leaves[d.id]; ok && len(d.children) == 0 {
		return []*leafDomain{leaf}
	}
	var result []*leafDomain
	stack := []*domain{d}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if len(cur.children) == 0 {
			if leaf, ok := s.leaves[cur.id]; ok {
				result = append(result, leaf)
			}
			continue
		}
		stack = append(stack, cur.children...)
	}
	// Sort by levelValues for deterministic placement: the domain tree is built
	// from map iteration, so child/leaf order is otherwise unstable across
	// scheduling cycles. A stable order keeps best-fit tie-breaking (and thus
	// the per-leaf pod distribution) reproducible.
	slices.SortFunc(result, func(a, b *leafDomain) int {
		return slices.Compare(a.levelValues, b.levelValues)
	})
	return result
}

// findReplacementAssignment finds the topology assignment for the replacement node
// it return new corrected topologyAssignment, a replacement topologyAssignment used to patched the old, faulty one, and
// reason if finding fails
func (s *TASFlavorSnapshot) findReplacementAssignment(
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
	sliceSize, _ := getSliceSizeWithSinglePodAsDefault(tr.PodSet.TopologyRequest)
	if slicesRequested(tr.PodSet.TopologyRequest) && requiredReplacementDomain != "" && (tr.Count%sliceSize != 0) {
		trCopy.PodSet = tr.PodSet.DeepCopy()
		// Find the innermost constraint whose size divides the number of replacement
		// pods to preserve leaf-level grouping
		effectiveSliceSize := int32(1)
		var effectiveSliceTopology *string
		constraints := tr.PodSet.TopologyRequest.PodsetSliceRequiredTopologyConstraints
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
	replacementAssignment, reason := s.findTopologyAssignment(trCopy, nil, assumedUsage, false, requiredReplacementDomain)
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
			assumedUsage[domainID] = resources.Requests{}
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

	sliceSize, _ := getSliceSizeWithSinglePodAsDefault(tr.PodSet.TopologyRequest)
	if slicesRequested(tr.PodSet.TopologyRequest) && (tr.Count%sliceSize != 0) {
		// For multi-layer constraints, find the innermost broken constraint's domain.
		// This ensures the replacement is confined to the tightest topology level
		// that needs repair, preserving intermediate grouping invariants.
		constraints := tr.PodSet.TopologyRequest.PodsetSliceRequiredTopologyConstraints
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
		if _, found := s.domains[utiltas.DomainID(domain.Values)]; !found {
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
	workersTasPodSetRequests TASPodSetRequests,
	leaderTasPodSetRequests *TASPodSetRequests,
	assumedUsage map[utiltas.TopologyDomainID]resources.Requests,
	simulateEmpty bool, requiredReplacementDomain utiltas.TopologyDomainID) (map[kueue.PodSetReference]*utiltas.TopologyAssignment, string) {
	requirements := &topologyAssignmentPodRequirements{
		assumedUsage:              assumedUsage,
		requiredReplacementDomain: requiredReplacementDomain,
		simulateEmpty:             simulateEmpty,
	}
	state := &findTopologyAssignmentState{
		topologyAssignmentParameters: topologyAssignmentParameters{
			count: workersTasPodSetRequests.Count,
		},
		stats: newExclusionStats(),
	}
	requirements.requests = workersTasPodSetRequests.SinglePodRequests.Clone()
	requirements.requests.Add(resources.Requests{corev1.ResourcePods: 1})

	if leaderTasPodSetRequests != nil {
		requirements.leaderRequests = new(leaderTasPodSetRequests.SinglePodRequests.Clone())
		requirements.leaderRequests.Add(resources.Requests{corev1.ResourcePods: 1})
		state.leaderCount = 1
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

	if features.Enabled(features.TASMultiLayerTopology) && len(sliceSizeAtLevel) > 0 {
		state.multiLayerConstraints = workersTasPodSetRequests.PodSet.TopologyRequest.PodsetSliceRequiredTopologyConstraints
	}

	// Build the per-PodSet node-scheduling constraints (tolerations, selector,
	// required/preferred affinity, merged with PodSetUpdates and flavor
	// tolerations). Shared with the generalized group packer.
	nodeReqs, reason := s.podSetNodeRequirements(&workersTasPodSetRequests)
	if reason != "" {
		return nil, reason
	}
	requirements.tolerations = nodeReqs.tolerations
	requirements.selector = nodeReqs.selector
	requirements.affinitySelector = nodeReqs.affinitySelector
	requirements.preferredSchedulingTerms = nodeReqs.preferredSchedulingTerms

	// phase 1 - determine the number of pods and slices which can fit in each topology domain
	s.fillInCounts(requirements, state)

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
		// that's why we're iterating through each parent domain and assigning `domain.state` amount of pods
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
				// For inner slice layers, recompute sliceState on the
				// child domains based on the current inner slice size.
				// The pre-populated sliceState was computed for the
				// outermost slice level and is not valid here.
				for _, d := range sortedLowerDomains {
					d.sliceState = d.state / sliceSizeOnLevel
					d.sliceStateWithLeader = d.stateWithLeader / sliceSizeOnLevel
				}
			}

			addCurrFitDomain := s.updateCountsToMinimumGeneric(sortedLowerDomains, domain.state, domain.leaderState, sliceSizeOnLevel, state.unconstrained, sliceSizeOnLevel > 1)
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
			if domain.leaderState > 0 {
				copiedDomain := *domain
				copiedDomain.state = copiedDomain.leaderState
				leaderFitDomains = append(leaderFitDomains, &copiedDomain)
			}

			// select domains with workers
			if domain.state > 0 {
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
//
// TODO: once TASMultiLayerTopology graduates to beta, use this function to unify logic for both
// 1-layer (kueue.x-k8s.io/podset-slice-size) and multi-layer topology constraints.
func (s *TASFlavorSnapshot) buildSliceSizeAtLevel(
	workersTasPodSetRequests TASPodSetRequests,
	sliceSize int32,
	sliceLevelIdx int,
) (map[int]int32, string) {
	sliceSizeAtLevel := make(map[int]int32)
	if !features.Enabled(features.TASMultiLayerTopology) || workersTasPodSetRequests.PodSet.TopologyRequest == nil {
		return sliceSizeAtLevel, ""
	}

	prevSize := sliceSize
	prevLevelIdx := sliceLevelIdx

	// Skip the first (outermost) constraint layer — it is already represented
	// by sliceSize / sliceLevelIdx which the caller resolved from the annotation.
	// Process only the inner layers that introduce additional grouping.
	innerLayers := workersTasPodSetRequests.PodSet.TopologyRequest.PodsetSliceRequiredTopologyConstraints
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
	if features.Enabled(features.TASMultiLayerTopology) && r != nil {
		for _, layer := range r.PodsetSliceRequiredTopologyConstraints {
			if _, found := s.resolveLevelIdx(layer.Topology); !found {
				return false
			}
		}
	}

	return true
}

func (s *TASFlavorSnapshot) sliceLevelKeyWithDefault(topologyRequest *kueue.PodSetTopologyRequest, defaultSliceLevelKey string) string {
	if topologyRequest != nil {
		if topologyRequest.PodSetSliceRequiredTopology != nil {
			return *topologyRequest.PodSetSliceRequiredTopology
		}
		if len(topologyRequest.PodsetSliceRequiredTopologyConstraints) > 0 {
			return topologyRequest.PodsetSliceRequiredTopologyConstraints[0].Topology
		}
	}
	return defaultSliceLevelKey
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
	return tr.PodSetSliceRequiredTopology != nil || len(tr.PodsetSliceRequiredTopologyConstraints) > 0
}

func slicesRequested(tr *kueue.PodSetTopologyRequest) bool {
	if tr == nil {
		return false
	}
	return (tr.PodSetSliceRequiredTopology != nil && tr.PodSetSliceSize != nil) || len(tr.PodsetSliceRequiredTopologyConstraints) > 0
}

func getSliceSizeWithSinglePodAsDefault(podSetTopologyRequest *kueue.PodSetTopologyRequest) (int32, string) {
	if podSetTopologyRequest == nil {
		return 1, ""
	}

	if len(podSetTopologyRequest.PodsetSliceRequiredTopologyConstraints) > 0 {
		return podSetTopologyRequest.PodsetSliceRequiredTopologyConstraints[0].Size, ""
	}

	if podSetTopologyRequest.PodSetSliceRequiredTopology == nil {
		return 1, ""
	}

	if podSetTopologyRequest.PodSetSliceSize == nil {
		return 0, "slice topology requested, but slice size not provided"
	}

	return *podSetTopologyRequest.PodSetSliceSize, ""
}

// findBestFitDomain finds an index of the first domain with the lowest
// value of state, higher or equal than count.
// If such a domain doesn't exist, it returns first domain as it's the domain with the
// most available resources
func findBestFitDomain(domains []*domain, count int32, leaderCount int32) *domain {
	getState := func(d *domain) int32 {
		return d.state
	}
	if leaderCount > 0 {
		getState = func(d *domain) int32 {
			return d.stateWithLeader
		}
	}
	return findBestFitDomainBy(domains, count, getState)
}

// findBestFitDomainForSlices finds an index of the first domain with the lowest
// value of sliceState, higher or equal than sliceCount.
// If such a domain doesn't exist, it returns first domain as it's the domain with the
// most available resources
func findBestFitDomainForSlices(domains []*domain, sliceCount int32, leaderCount int32) *domain {
	getState := func(d *domain) int32 {
		return d.sliceState
	}
	if leaderCount > 0 {
		getState = func(d *domain) int32 {
			return d.sliceStateWithLeader
		}
	}
	return findBestFitDomainBy(domains, sliceCount, getState)
}

type domainState func(d *domain) int32

func findBestFitDomainBy(domains []*domain, needed int32, state domainState) *domain {
	candidates := topAffinityTierDomains(domains)
	bestDomain := candidates[0]
	bestDomainState := state(bestDomain)

	for _, domain := range candidates {
		domainState := state(domain)

		if domainState >= needed && domainState < bestDomainState {
			// choose the first occurrence of fitting domains
			// to make it consecutive with other podSet's
			bestDomain = domain
			bestDomainState = state(bestDomain)
		}
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
	if useBestFitAlgorithm(state.unconstrained) && topDomain.sliceStateWithLeader >= sliceCount && topDomain.leaderState >= state.leaderCount {
		// optimize the potentially last domain
		topDomain = findBestFitDomainForSlices(sortedDomain, sliceCount, state.leaderCount)
	}
	notFitReason := func(slicesFitCount, totalRequestsSlicesCount int32) string {
		if len(state.multiLayerConstraints) > 0 {
			return s.multiLayerNotFitMessage(searchLevelIdx, state.count, state.multiLayerConstraints, state.stats)
		}
		return s.notFitMessage(slicesFitCount, totalRequestsSlicesCount, state.sliceSize, state.stats)
	}

	if useLeastFreeCapacityAlgorithm(state.unconstrained) {
		for _, candidateDomain := range sortedDomain {
			if candidateDomain.sliceState >= sliceCount {
				return searchLevelIdx, []*domain{candidateDomain}, ""
			}
		}
		if state.required {
			maxCapacityFound := sortedDomain[len(sortedDomain)-1].state
			return 0, nil, notFitReason(maxCapacityFound, sliceCount)
		}
	}
	if topDomain.sliceStateWithLeader < sliceCount || topDomain.leaderState < state.leaderCount {
		if state.required {
			// Scan remaining domains to support preferred affinity before failing
			if features.Enabled(features.TASRespectNodeAffinityPreferred) {
				for i := 1; i < len(sortedDomain); i++ {
					d := sortedDomain[i]
					if d.sliceStateWithLeader >= sliceCount && d.leaderState >= state.leaderCount {
						return searchLevelIdx, []*domain{findBestFitDomainForSlices(sortedDomain[i:], sliceCount, state.leaderCount)}, ""
					}
				}
			}
			return 0, nil, notFitReason(topDomain.sliceState, sliceCount)
		}
		if searchLevelIdx > 0 && !state.unconstrained {
			return s.findLevelWithFitDomains(searchLevelIdx-1, state)
		}
		results := []*domain{}
		remainingSliceCount := sliceCount
		remainingLeaderCount := state.leaderCount

		// Domains are sorted in a way that prioritizes domains with higher leader capacity.
		// We want to assign leaders first. After we are assign all leaders, we sort the remaining
		// "unused" domains based on worker capacity (sliceState, state and then levelValues) and
		// try to assign remaining workers.
		idx := 0
		for ; remainingLeaderCount > 0 && idx < len(sortedDomain) && sortedDomain[idx].leaderState > 0; idx++ {
			domain := sortedDomain[idx]
			if useBestFitAlgorithm(state.unconstrained) && sortedDomain[idx].sliceStateWithLeader >= remainingSliceCount {
				// optimize the last domain
				domain = findBestFitDomainForSlices(sortedDomain[idx:], remainingSliceCount, remainingLeaderCount)
			}
			results = append(results, domain)

			remainingLeaderCount -= domain.leaderState
			remainingSliceCount -= domain.sliceStateWithLeader
		}
		if remainingLeaderCount > 0 {
			return 0, nil, notFitReason(state.leaderCount-remainingLeaderCount, sliceCount)
		}

		// At this point we have assigned all leaders, so we sort remaining domains based on worker capacity
		// and assign remaining workers.
		sortedDomain = s.sortedDomains(sortedDomain[idx:], state.unconstrained)
		for idx := 0; remainingSliceCount > 0 && idx < len(sortedDomain); idx++ {
			domain := sortedDomain[idx]
			if useBestFitAlgorithm(state.unconstrained) && sortedDomain[idx].sliceState >= remainingSliceCount {
				// optimize the last domain
				domain = findBestFitDomainForSlices(sortedDomain[idx:], remainingSliceCount, 0)
			}
			results = append(results, domain)

			remainingSliceCount -= domain.sliceState
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
func topAffinityTierDomains(candidates []*domain) []*domain {
	if !features.Enabled(features.TASRespectNodeAffinityPreferred) || len(candidates) == 0 {
		return candidates
	}
	score := candidates[0].affinityScore
	for i, c := range candidates {
		if c.affinityScore != score {
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
//   - withLeader: pointer to the domain field that represents capacity with a leader present
//     (use &domain.stateWithLeader for pods, &domain.sliceStateWithLeader for slices)
//   - primary: pointer to the domain field that represents the primary unit being distributed
//     (use &domain.state for pods, &domain.sliceState for slices)
//   - sliceSize: factor to set domain.state when finalizing or partially consuming
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
	if useBestFitAlgorithm(unconstrained) && *withLeader >= *remainingPrimary && domain.leaderState >= *remainingLeaderCount {
		// optimize the last domain
		if slices {
			domain = findBestFitDomainForSlices(remainingDomains, *remainingPrimary, *remainingLeaderCount)
			withLeader = &domain.sliceStateWithLeader
			primary = &domain.sliceState
		} else {
			domain = findBestFitDomain(remainingDomains, *remainingPrimary, *remainingLeaderCount)
			withLeader = &domain.stateWithLeader
			primary = &domain.state
		}
	}

	if *withLeader >= *remainingPrimary && domain.leaderState >= *remainingLeaderCount {
		*primary = *remainingPrimary
		domain.leaderState = *remainingLeaderCount
		domain.state = *remainingPrimary * sliceSize
		return domain, true
	}

	if slices {
		// Clamp to remaining before consuming and compute state from slice count
		if *withLeader > *remainingPrimary {
			*withLeader = *remainingPrimary
		}
		if domain.leaderState > *remainingLeaderCount {
			domain.leaderState = *remainingLeaderCount
		}
		domain.state = *withLeader * sliceSize
		*remainingLeaderCount -= domain.leaderState
		*remainingPrimary -= *withLeader
		return domain, false
	}

	// Pods: subtract first, then clamp fields
	*remainingPrimary -= *withLeader
	*remainingLeaderCount -= domain.leaderState
	if *withLeader > *remainingPrimary {
		*withLeader = *remainingPrimary
	}
	if domain.leaderState > *remainingLeaderCount {
		domain.leaderState = *remainingLeaderCount
	}
	return domain, false
}

func (s *TASFlavorSnapshot) updateCountsToMinimumGeneric(domains []*domain, count int32, leaderCount int32, sliceSize int32, unconstrained bool, slices bool) []*domain {
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
				d, completed = s.consumeWithLeadersGeneric(dom, domains[i:], &remainingPrimary, &remainingLeaderCount, unconstrained, &dom.sliceStateWithLeader, &dom.sliceState, sliceSize, true)
			} else {
				d, completed = s.consumeWithLeadersGeneric(dom, domains[i:], &remainingPrimary, &remainingLeaderCount, unconstrained, &dom.stateWithLeader, &dom.state, 1, false)
			}
			result = append(result, d)
			if completed {
				return result
			}
			continue
		}

		// No leaders remaining: handle tail without leaders
		if slices {
			if useBestFitAlgorithm(unconstrained) && dom.sliceState >= remainingPrimary {
				// optimize the last domain
				dom = findBestFitDomainForSlices(domains[i:], remainingPrimary, 0)
			}
			dom.leaderState = 0
			if dom.sliceState >= remainingPrimary {
				dom.state = remainingPrimary * sliceSize
				dom.sliceState = remainingPrimary
				result = append(result, dom)
				return result
			}
			dom.state = dom.sliceState * sliceSize
			remainingPrimary -= dom.sliceState
			result = append(result, dom)
			continue
		}

		// pods (slices=false)
		if useBestFitAlgorithm(unconstrained) && dom.state >= remainingPrimary {
			// optimize the last domain
			dom = findBestFitDomain(domains[i:], remainingPrimary, 0)
		}
		dom.leaderState = 0
		if dom.state >= remainingPrimary {
			dom.state = remainingPrimary
			result = append(result, dom)
			return result
		}
		remainingPrimary -= dom.state
		result = append(result, dom)
	}
	s.log.Error(errCodeAssumptionsViolated, "unexpected remainingCount",
		"remainingCount", remainingPrimary,
		"remainingLeaderCount", remainingLeaderCount,
		"count", count,
		"sliceSize", sliceSize,
		"leaves", s.leaves)
	return nil
}

// buildTopologyAssignmentForLevels build TopologyAssignment for levels starting from levelIdx
func (s *TASFlavorSnapshot) buildTopologyAssignmentForLevels(domains []*domain, levelIdx int) *utiltas.TopologyAssignment {
	assignment := &utiltas.TopologyAssignment{
		Domains: make([]utiltas.TopologyDomainAssignment, 0),
	}
	assignment.Levels = s.levelKeys[levelIdx:]
	for _, domain := range domains {
		if domain.state == 0 {
			// It may happen when PodSet count is 0 or when using LeastFreeCapacity algorithm.
			continue
		}
		assignment.Domains = append(assignment.Domains, utiltas.TopologyDomainAssignment{
			Values: domain.levelValues[levelIdx:],
			Count:  domain.state,
		})
	}
	return assignment
}

func (s *TASFlavorSnapshot) buildAssignment(domains []*domain) *utiltas.TopologyAssignment {
	// lex sort domains by their levelValues instead of IDs, as leaves' IDs can only contain the hostname
	slices.SortFunc(domains, func(a, b *domain) int {
		return slices.Compare(a.levelValues, b.levelValues)
	})
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

func (s *TASFlavorSnapshot) sortedDomainsWithLeader(domains []*domain, unconstrained bool) []*domain {
	isLeastFreeCapacity := useLeastFreeCapacityAlgorithm(unconstrained)
	result := slices.Clone(domains)
	slices.SortFunc(result, func(a, b *domain) int {
		if a.leaderState != b.leaderState {
			return cmp.Compare(b.leaderState, a.leaderState)
		}

		if features.Enabled(features.TASRespectNodeAffinityPreferred) && a.affinityScore != b.affinityScore {
			return cmp.Compare(b.affinityScore, a.affinityScore)
		}

		if a.sliceStateWithLeader != b.sliceStateWithLeader {
			if isLeastFreeCapacity {
				// Start from the domain with the least amount of free resources.
				// Ascending order.
				return cmp.Compare(a.sliceStateWithLeader, b.sliceStateWithLeader)
			}
			return cmp.Compare(b.sliceStateWithLeader, a.sliceStateWithLeader)
		}

		if a.stateWithLeader != b.stateWithLeader {
			return cmp.Compare(a.stateWithLeader, b.stateWithLeader)
		}

		return slices.Compare(a.levelValues, b.levelValues)
	})
	return result
}

// This function sorts domains based on a specified algorithm: BestFit or LeastFreeCapacity.
//
// The sorting criteria are:
// - **BestFit**: `sliceState` (descending), `state` (ascending), `levelValues` (ascending)
// - **LeastFreeCapacity**: `sliceState` (ascending), `state` (ascending), `levelValues` (ascending)
//
// `state` is always sorted ascending. This prioritizes domains that can accommodate slices with minimal leftover pod capacity.
func (s *TASFlavorSnapshot) sortedDomains(domains []*domain, unconstrained bool) []*domain {
	isLeastFreeCapacity := useLeastFreeCapacityAlgorithm(unconstrained)
	result := slices.Clone(domains)
	slices.SortFunc(result, func(a, b *domain) int {
		if features.Enabled(features.TASRespectNodeAffinityPreferred) && a.affinityScore != b.affinityScore {
			return cmp.Compare(b.affinityScore, a.affinityScore)
		}

		if a.sliceState != b.sliceState {
			if isLeastFreeCapacity {
				// Start from the domain with the least amount of free resources.
				// Ascending order.
				return cmp.Compare(a.sliceState, b.sliceState)
			}
			return cmp.Compare(b.sliceState, a.sliceState)
		}

		if a.state != b.state {
			return cmp.Compare(a.state, b.state)
		}

		return slices.Compare(a.levelValues, b.levelValues)
	})
	return result
}

// podSetNodeConstraints holds a single PodSet's node-scheduling constraints,
// derived from its PodSet (merged with PodSetUpdates) and the flavor tolerations.
type podSetNodeConstraints struct {
	tolerations              []corev1.Toleration
	selector                 labels.Selector
	affinitySelector         *nodeaffinity.NodeSelector
	preferredSchedulingTerms *nodeaffinity.PreferredSchedulingTerms
}

// podSetNodeRequirements builds the node-scheduling constraints for a single
// PodSet: tolerations (PodSet + flavor), the node-label selector, required node
// affinity, and (when TASRespectNodeAffinityPreferred is enabled) the preferred
// scheduling terms. It is the single source of truth for per-PodSet node
// requirements, used by both findTopologyAssignment (legacy path) and the
// generalized group packer.
func (s *TASFlavorSnapshot) podSetNodeRequirements(tr *TASPodSetRequests) (podSetNodeConstraints, string) {
	var c podSetNodeConstraints
	info := podset.FromPodSet(tr.PodSet)
	for _, podSetUpdate := range tr.PodSetUpdates {
		if err := info.Merge(podset.FromUpdate(podSetUpdate)); err != nil {
			return c, fmt.Sprintf("invalid podSetUpdate for PodSet %s, error: %s", tr.PodSet.Name, err.Error())
		}
	}

	c.tolerations = append(info.Tolerations, s.tolerations...)

	if s.isLowestLevelNode {
		sel, err := labels.ValidatedSelectorFromSet(info.NodeSelector)
		if err != nil {
			return c, fmt.Sprintf("invalid node selectors: %s, reason: %s", info.NodeSelector, err)
		}
		c.selector = sel
	} else {
		c.selector = labels.Everything()
	}

	if info.Affinity != nil && info.Affinity.NodeAffinity != nil {
		if requiredAffinity := info.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution; requiredAffinity != nil {
			affinitySelector, err := nodeaffinity.NewNodeSelector(requiredAffinity)
			if err != nil {
				return c, fmt.Sprintf("invalid affinity node selectors: %s, reason: %s", requiredAffinity, err)
			}
			c.affinitySelector = affinitySelector
		}
		if features.Enabled(features.TASRespectNodeAffinityPreferred) {
			preferredAffinity := info.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution
			if len(preferredAffinity) > 0 {
				prefTerms, err := nodeaffinity.NewPreferredSchedulingTerms(preferredAffinity)
				if err != nil {
					return c, fmt.Sprintf("invalid preferred node affinity terms: %v, reason: %s", preferredAffinity, err)
				}
				c.preferredSchedulingTerms = prefTerms
			}
		}
	}
	return c, ""
}

// nodeIneligibleReason explains why a leaf node was excluded for a PodSet.
type nodeIneligibleReason int

const (
	nodeEligible nodeIneligibleReason = iota
	nodeIneligibleTaint
	nodeIneligibleSelector
	nodeIneligibleAffinity
)

// leafNodeEligibility reports whether a leaf node satisfies a PodSet's
// scheduling constraints: tolerations vs. node taints, node labels vs. selector,
// and required node affinity. On exclusion it returns the reason and (for the
// taint case) the offending taint string. This is the shared predicate used by
// both the single-PodSet path (fillInCounts) and the generalized group packer,
// so that generalized groups respect the same node constraints.
func (s *TASFlavorSnapshot) leafNodeEligibility(
	leaf *leafDomain,
	tolerations []corev1.Toleration,
	selector labels.Selector,
	affinitySelector *nodeaffinity.NodeSelector,
) (bool, nodeIneligibleReason, string) {
	// 1. Tolerations vs node taints.
	taint, untolerated := corev1helpers.FindMatchingUntoleratedTaint(s.log, leaf.node.Taints, tolerations, func(t *corev1.Taint) bool {
		return t.Effect == corev1.TaintEffectNoSchedule || t.Effect == corev1.TaintEffectNoExecute
	}, true)
	if untolerated {
		return false, nodeIneligibleTaint, taint.ToString()
	}

	// 2. Node labels vs compiled selector.
	var nodeLabelSet labels.Set
	if nodeLabels := leaf.node.Labels; nodeLabels != nil {
		nodeLabelSet = nodeLabels
	}
	if selector != nil && !selector.Matches(nodeLabelSet) {
		return false, nodeIneligibleSelector, ""
	}

	// 3. Required node affinity.
	if affinitySelector != nil && !affinitySelector.Match(leaf.node.toNode()) {
		return false, nodeIneligibleAffinity, ""
	}
	return true, nodeEligible, ""
}

// fillInCounts computes per-domain pod, slice, and leader capacities from the
// pod requirements, then rolls those capacities up the topology tree.
func (s *TASFlavorSnapshot) fillInCounts(requirements *topologyAssignmentPodRequirements, state *findTopologyAssignmentState) {
	for _, domain := range s.domains {
		// cleanup the state in case some remaining values are present from computing
		// assignments for previous PodSets.
		domain.state = 0
		domain.stateWithLeader = 0
		domain.sliceState = 0
		domain.sliceStateWithLeader = 0
		domain.leaderState = 0
		domain.affinityScore = 0
	}

	for _, leaf := range s.leaves {
		state.stats.TotalNodes++
		// Gather node level information only when the node is the lowest level of the topology
		if s.isLowestLevelNode {
			eligible, reason, taint := s.leafNodeEligibility(leaf, requirements.tolerations, requirements.selector, requirements.affinitySelector)
			if !eligible {
				switch reason {
				case nodeIneligibleTaint:
					s.log.V(5).Info("excluding node with untolerated taint", "domainID", leaf.id, "taint", taint)
					state.stats.Taints[taint]++
				case nodeIneligibleSelector:
					s.log.V(5).Info("excluding node that doesn't match nodeSelectors", "domainID", leaf.id, "nodeLabels", leaf.node.Labels)
					state.stats.NodeSelector++
				case nodeIneligibleAffinity:
					s.log.V(5).Info("excluding node that doesn't match requiredDuringSchedulingIgnoredDuringExecution affinity", "domainID", leaf.id)
					state.stats.Affinity++
				}
				continue
			}

			// Calculate Affinity Score
			if features.Enabled(features.TASRespectNodeAffinityPreferred) && requirements.preferredSchedulingTerms != nil {
				leaf.affinityScore += requirements.preferredSchedulingTerms.Score(leaf.node.toNode())
			}
		}

		// 5. While correcting the topologyAssignment with a failed node
		// check if the leaf belongs to the required domain
		if !belongsToRequiredDomain(leaf, requirements.requiredReplacementDomain) {
			state.stats.TopologyDomain++
			continue
		}

		remainingCapacity := leaf.freeCapacity.Clone()
		if !requirements.simulateEmpty {
			remainingCapacity.Sub(leaf.tasUsage)
		}
		if leafAssumedUsage, found := requirements.assumedUsage[leaf.id]; found {
			remainingCapacity.Sub(leafAssumedUsage)
		}
		var limitingRes corev1.ResourceName
		leaf.state, limitingRes = requirements.requests.CountInWithLimitingResource(remainingCapacity)

		// Track resource exclusions: if this node can't fit even one pod,
		// identify which resource is the bottleneck.
		if leaf.state == 0 && limitingRes != "" {
			state.stats.Resources[limitingRes]++
		}

		leaf.leaderState = 0
		if requirements.leaderRequests != nil && requirements.leaderRequests.CountIn(remainingCapacity) > 0 {
			leaf.leaderState = 1
			remainingCapacity.Sub(*requirements.leaderRequests)
		}

		leaf.stateWithLeader = requirements.requests.CountIn(remainingCapacity)
	}
	for _, root := range s.roots {
		s.fillInCountsHelper(root, state.sliceSize, state.sliceLevelIdx, 0, state.sliceSizeAtLevel, state.leaderCount > 0)
	}
}

func belongsToRequiredDomain(leaf *leafDomain, requiredReplacementDomain utiltas.TopologyDomainID) bool {
	if requiredReplacementDomain == "" {
		return true
	}
	// Uses levelValues instead of leaf.id since for topologies with hostname as lowest level it points directly to the hostname
	// TODO(#5322): Use util function that compare two DomainIDs
	return strings.HasPrefix(string(utiltas.DomainID(leaf.levelValues)), string(requiredReplacementDomain))
}

func (s *TASFlavorSnapshot) fillInCountsHelper(domain *domain, sliceSize int32, sliceLevelIdx int, level int, sliceSizeAtLevel map[int]int32, leaderRequired bool) {
	// logic for a leaf
	if len(domain.children) == 0 {
		if level == sliceLevelIdx {
			// initialize the sliceState if leaf is the request slice level
			domain.sliceState = domain.state / sliceSize
			domain.sliceStateWithLeader = domain.stateWithLeader / sliceSize
		}
		return
	}
	// logic for a parent
	childrenCapacity := int32(0)
	sliceCapacity := int32(0)
	hasWithLeaderCapacityContributor := false
	minStateWithLeaderDifference := int32(math.MaxInt32)
	minSliceStateWithLeaderDifference := int32(math.MaxInt32)
	leaderState := int32(0)
	affinityScore := int64(0)

	// When multi-layer constraints exist, children at a constrained level
	// can only contribute pods in multiples of the inner slice size.
	// Round down each child's effective contribution so that the parent's
	// capacity accurately reflects what can actually be grouped.
	childLevel := level + 1
	innerSize, hasInnerConstraint := sliceSizeAtLevel[childLevel]

	for _, child := range domain.children {
		s.fillInCountsHelper(child, sliceSize, sliceLevelIdx, childLevel, sliceSizeAtLevel, leaderRequired)

		childState := child.state
		childStateWithLeader := child.stateWithLeader
		if hasInnerConstraint {
			childState = (child.state / innerSize) * innerSize
			childStateWithLeader = (child.stateWithLeader / innerSize) * innerSize
		}

		childrenCapacity += childState
		sliceCapacity += child.sliceState
		if !leaderRequired || child.leaderState > 0 {
			hasWithLeaderCapacityContributor = true
			minStateWithLeaderDifference = min(childState-childStateWithLeader, minStateWithLeaderDifference)
			minSliceStateWithLeaderDifference = min(child.sliceState-child.sliceStateWithLeader, minSliceStateWithLeaderDifference)
		}
		leaderState = max(child.leaderState, leaderState)
		affinityScore += child.affinityScore
	}
	domain.state = childrenCapacity
	sliceStateWithLeader := int32(0)
	if hasWithLeaderCapacityContributor {
		domain.stateWithLeader = childrenCapacity - minStateWithLeaderDifference
		sliceStateWithLeader = sliceCapacity - minSliceStateWithLeaderDifference
	} else {
		domain.stateWithLeader = 0
	}
	domain.leaderState = leaderState
	domain.affinityScore = affinityScore
	if level == sliceLevelIdx {
		// initialize the sliceState for the requested slice level.
		sliceCapacity = domain.state / sliceSize
		sliceStateWithLeader = domain.stateWithLeader / sliceSize
	}
	domain.sliceState = sliceCapacity
	domain.sliceStateWithLeader = sliceStateWithLeader
}

func (s *TASFlavorSnapshot) notFitMessage(slicesFitCount, totalRequestsSlicesCount, sliceSize int32, stats *ExclusionStats) string {
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

func countSlicesInSubtree(d *domain, currentLevel, targetLevel int, sliceSize int32) int32 {
	if currentLevel == targetLevel {
		return d.state / sliceSize
	}
	var total int32
	for _, child := range d.children {
		total += countSlicesInSubtree(child, currentLevel+1, targetLevel, sliceSize)
	}
	return total
}

func (s *TASFlavorSnapshot) multiLayerNotFitMessage(
	requiredLevelIdx int,
	count int32,
	constraints []kueue.PodsetSliceRequiredTopologyConstraint,
	stats *ExclusionStats,
) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "topology %q doesn't allow to fit", s.topologyName)

	// Pick the domain with the highest sliceState to report the best-case
	// fit counts. Tie-break on domain ID for deterministic messages, since
	// domainsPerLevel is map-backed and iteration order is random.
	var bestDomain *domain
	for _, d := range s.domainsPerLevel[requiredLevelIdx] {
		if bestDomain == nil || d.sliceState > bestDomain.sliceState ||
			(d.sliceState == bestDomain.sliceState && d.id < bestDomain.id) {
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
		fitSlices := countSlicesInSubtree(bestDomain, requiredLevelIdx, targetLevelIdx, c.Size)
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

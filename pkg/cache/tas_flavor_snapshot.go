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

package cache

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	corev1helpers "k8s.io/component-helpers/scheduling/corev1"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/features"
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

	// levelValues stores the mapping from domain ID back to the
	// ordered list of values
	levelValues []string
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

	// nodeTaints contains the list of taints for the node, only applies for
	// lowest level of topology, if the lowest level is node
	nodeTaints []corev1.Taint
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
}

func newTASFlavorSnapshot(log logr.Logger, topologyName kueue.TopologyReference,
	levels []string, tolerations []corev1.Toleration) *TASFlavorSnapshot {
	domainsPerLevel := make([]domainByID, len(levels))
	for level := range levels {
		domainsPerLevel[level] = make(domainByID)
	}

	snapshot := &TASFlavorSnapshot{
		log:             log,
		topologyName:    topologyName,
		levelKeys:       slices.Clone(levels),
		leaves:          make(leafDomainByID),
		tolerations:     slices.Clone(tolerations),
		domains:         make(domainByID),
		roots:           make(domainByID),
		domainsPerLevel: domainsPerLevel,
	}
	return snapshot
}

func (s *TASFlavorSnapshot) addNode(node corev1.Node) utiltas.TopologyDomainID {
	levelValues := utiltas.LevelValues(s.levelKeys, node.Labels)
	domainID := utiltas.DomainID(levelValues)
	if s.isLowestLevelNode() {
		domainID = utiltas.DomainID(levelValues[len(levelValues)-1:])
	}
	if _, found := s.leaves[domainID]; !found {
		leafDomain := leafDomain{
			domain: domain{
				id:          domainID,
				levelValues: levelValues,
			},
		}
		if s.isLowestLevelNode() {
			leafDomain.nodeTaints = slices.Clone(node.Spec.Taints)
		}
		s.leaves[domainID] = &leafDomain
	}
	capacity := resources.NewRequests(node.Status.Allocatable)
	s.addCapacity(domainID, capacity)
	return domainID
}

func (s *TASFlavorSnapshot) isLowestLevelNode() bool {
	return s.lowestLevel() == corev1.LabelHostname
}

func (s *TASFlavorSnapshot) lowestLevel() string {
	return s.levelKeys[len(s.levelKeys)-1]
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
	s.leaves[domainID].freeCapacity.Sub(resources.Requests{corev1.ResourcePods: 1})
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
		s.log.Info("skip accounting for TAS usage in domain", "domain", domainID, "usage", usage)
		return
	}
	if s.leaves[domainID].tasUsage == nil {
		s.leaves[domainID].tasUsage = resources.Requests{}
	}
	s.leaves[domainID].tasUsage.Add(usage)
}

func (s *TASFlavorSnapshot) removeTASUsage(domainID utiltas.TopologyDomainID, usage resources.Requests) {
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
	SinglePodRequests resources.Requests
	Count             int32
	Flavor            kueue.ResourceFlavorReference
	Implied           bool
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
	TopologyAssignment *kueue.TopologyAssignment
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

// FindTopologyAssignmentsForFlavor returns TAS assignment, if possible, for all
// the TAS requests in the flavor handled by the snapshot.
// The simulateEmpty parameter allows to look for the assignment under the
// assumption that all TAS workloads are preempted.
func (s *TASFlavorSnapshot) FindTopologyAssignmentsForFlavor(flavorTASRequests FlavorTASRequests, simulateEmpty bool) TASAssignmentsResult {
	result := make(map[kueue.PodSetReference]tasPodSetAssignmentResult)
	assumedUsage := make(map[utiltas.TopologyDomainID]resources.Requests)
	for _, tr := range flavorTASRequests {
		assignment, reason := s.findTopologyAssignment(tr, assumedUsage, simulateEmpty)
		result[tr.PodSet.Name] = tasPodSetAssignmentResult{TopologyAssignment: assignment, FailureReason: reason}
		if reason != "" {
			return result
		}
		for _, domain := range assignment.Domains {
			domainID := utiltas.DomainID(domain.Values)
			if assumedUsage[domainID] == nil {
				assumedUsage[domainID] = resources.Requests{}
			}
			assumedUsage[domainID].Add(tr.TotalRequests())
		}
	}
	return result
}

// Algorithm overview:
// Phase 1:
//
//	determine pod counts for each topology domain. Start at the lowest level
//	and bubble up the numbers to the top level
//
// Phase 2:
//
//	a) select the domain at requested level with count >= requestedCount
//	b) traverse the structure down level-by-level optimizing the number of used
//	  domains at each level
//	c) build the assignment for the lowest level in the hierarchy
func (s *TASFlavorSnapshot) findTopologyAssignment(
	tasPodSetRequests TASPodSetRequests,
	assumedUsage map[utiltas.TopologyDomainID]resources.Requests,
	simulateEmpty bool) (*kueue.TopologyAssignment, string) {
	requests := tasPodSetRequests.SinglePodRequests.Clone()
	requests.Add(resources.Requests{corev1.ResourcePods: 1})
	podSetTolerations := tasPodSetRequests.PodSet.Template.Spec.Tolerations
	count := tasPodSetRequests.Count
	required := isRequired(tasPodSetRequests.PodSet.TopologyRequest)
	key := s.levelKeyWithImpliedFallback(&tasPodSetRequests)
	unconstrained := isUnconstrained(tasPodSetRequests.PodSet.TopologyRequest, &tasPodSetRequests)
	if key == nil {
		return nil, "topology level not specified"
	}
	levelIdx, found := s.resolveLevelIdx(*key)
	if !found {
		return nil, fmt.Sprintf("no requested topology level: %s", *key)
	}
	// phase 1 - determine the number of pods which can fit in each topology domain
	s.fillInCounts(requests, assumedUsage, simulateEmpty, append(podSetTolerations, s.tolerations...))

	// phase 2a: determine the level at which the assignment is done along with
	// the domains which can accommodate all pods
	fitLevelIdx, currFitDomain, reason := s.findLevelWithFitDomains(levelIdx, required, count, unconstrained)
	if len(reason) > 0 {
		return nil, reason
	}

	// phase 2b: traverse the tree down level-by-level optimizing the number of
	// topology domains at each level
	// if unconstrained is set, we'll only do it once
	currFitDomain = s.updateCountsToMinimum(currFitDomain, count, unconstrained)
	for levelIdx := fitLevelIdx; levelIdx+1 < len(s.domainsPerLevel); levelIdx++ {
		lowerFitDomains := s.lowerLevelDomains(currFitDomain)
		sortedLowerDomains := s.sortedDomains(lowerFitDomains, unconstrained)
		currFitDomain = s.updateCountsToMinimum(sortedLowerDomains, count, unconstrained)
	}
	return s.buildAssignment(currFitDomain), ""
}

func (s *TASFlavorSnapshot) HasLevel(r *kueue.PodSetTopologyRequest) bool {
	key := s.levelKey(r)
	if key == nil {
		return false
	}
	_, found := s.resolveLevelIdx(*key)
	return found
}

func (s *TASFlavorSnapshot) resolveLevelIdx(levelKey string) (int, bool) {
	levelIdx := slices.Index(s.levelKeys, levelKey)
	if levelIdx == -1 {
		return levelIdx, false
	}
	return levelIdx, true
}

func isRequired(tr *kueue.PodSetTopologyRequest) bool {
	return tr != nil && tr.Required != nil
}

func (s *TASFlavorSnapshot) levelKeyWithImpliedFallback(tasRequests *TASPodSetRequests) *string {
	if key := s.levelKey(tasRequests.PodSet.TopologyRequest); key != nil {
		return key
	}
	if tasRequests.Implied {
		return ptr.To(s.lowestLevel())
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
	case ptr.Deref(topologyRequest.Unconstrained, false):
		return ptr.To(s.lowestLevel())
	default:
		return nil
	}
}

func isUnconstrained(tr *kueue.PodSetTopologyRequest, tasRequests *TASPodSetRequests) bool {
	return (tr != nil && tr.Unconstrained != nil && *tr.Unconstrained) || tasRequests.Implied
}

// findBestFitDomainIdx finds an index of the first domain with the lowest
// value of state, higher or equal than count.
// If such a domain doesn't exist, it returns 0 as it's an index of the domain with the
// most available resources
func findBestFitDomainIdx(domains []*domain, count int32) int {
	bestFitIdx := 0
	for i, domain := range domains {
		if domain.state >= count && domain.state != domains[bestFitIdx].state {
			// choose the first occurrence of fitting domains
			// to make it consecutive with other podSet's
			bestFitIdx = i
		}
	}
	return bestFitIdx
}

func (s *TASFlavorSnapshot) findLevelWithFitDomains(levelIdx int, required bool, count int32, unconstrained bool) (int, []*domain, string) {
	domains := s.domainsPerLevel[levelIdx]
	if len(domains) == 0 {
		return 0, nil, fmt.Sprintf("no topology domains at level: %s", s.levelKeys[levelIdx])
	}
	levelDomains := slices.Collect(maps.Values(domains))
	sortedDomain := s.sortedDomains(levelDomains, unconstrained)
	topDomain := sortedDomain[0]
	if useBestFitAlgorithm(unconstrained) && topDomain.state >= count {
		// optimize the potentially last domain
		topDomain = sortedDomain[findBestFitDomainIdx(sortedDomain, count)]
	}
	if topDomain.state < count {
		if required {
			return 0, nil, s.notFitMessage(topDomain.state, count)
		}
		if levelIdx > 0 && !unconstrained {
			return s.findLevelWithFitDomains(levelIdx-1, required, count, unconstrained)
		}
		results := []*domain{}
		remainingCount := count
		for idx := 0; remainingCount > 0 && idx < len(sortedDomain) && sortedDomain[idx].state > 0; idx++ {
			offset := 0
			if useBestFitAlgorithm(unconstrained) && sortedDomain[idx].state >= remainingCount {
				// optimize the last domain
				offset = findBestFitDomainIdx(sortedDomain[idx:], remainingCount)
			}
			results = append(results, sortedDomain[idx+offset])
			remainingCount -= sortedDomain[idx].state
		}
		if remainingCount > 0 {
			return 0, nil, s.notFitMessage(count-remainingCount, count)
		}
		return levelIdx, results, ""
	}
	return levelIdx, []*domain{topDomain}, ""
}

func useBestFitAlgorithm(unconstrained bool) bool {
	if features.Enabled(features.TASProfileMostFreeCapacity) ||
		features.Enabled(features.TASProfileLeastFreeCapacity) ||
		(unconstrained && features.Enabled(features.TASProfileMixed)) {
		// following the matrix from KEP#2724
		return false
	}
	return true
}

func useLeastFreeCapacityAlgorithm(unconstrained bool) bool {
	if features.Enabled(features.TASProfileLeastFreeCapacity) ||
		(unconstrained && features.Enabled(features.TASProfileMixed)) {
		// following the matrix from KEP#2724
		return true
	}
	return false
}

func (s *TASFlavorSnapshot) updateCountsToMinimum(domains []*domain, count int32, unconstrained bool) []*domain {
	result := make([]*domain, 0)
	remainingCount := count
	for i, domain := range domains {
		if useBestFitAlgorithm(unconstrained) && domain.state >= remainingCount {
			// optimize the last domain
			mostAllocatedIdx := findBestFitDomainIdx(domains[i:], remainingCount)
			domain = domains[i+mostAllocatedIdx]
		}

		if domain.state >= remainingCount {
			domain.state = remainingCount
			result = append(result, domain)
			return result
		}
		remainingCount -= domain.state
		result = append(result, domain)
	}
	s.log.Error(errCodeAssumptionsViolated, "unexpected remainingCount",
		"remainingCount", remainingCount,
		"count", count,
		"leaves", s.leaves)
	return nil
}

// buildTopologyAssignmentForLevels build TopologyAssignment for levels starting from levelIdx
func (s *TASFlavorSnapshot) buildTopologyAssignmentForLevels(domains []*domain, levelIdx int) *kueue.TopologyAssignment {
	assignment := &kueue.TopologyAssignment{
		Domains: make([]kueue.TopologyDomainAssignment, len(domains)),
	}
	assignment.Levels = s.levelKeys[levelIdx:]
	for i, domain := range domains {
		assignment.Domains[i] = kueue.TopologyDomainAssignment{
			Values: domain.levelValues[levelIdx:],
			Count:  domain.state,
		}
	}
	return assignment
}

func (s *TASFlavorSnapshot) buildAssignment(domains []*domain) *kueue.TopologyAssignment {
	// lex sort domains by their levelValues instead of IDs, as leaves' IDs can only contain the hostname
	slices.SortFunc(domains, func(a, b *domain) int {
		return slices.Compare(a.levelValues, b.levelValues)
	})
	levelIdx := 0
	// assign only hostname values if topology defines it
	if s.isLowestLevelNode() {
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

func (s *TASFlavorSnapshot) sortedDomains(domains []*domain, unconstrained bool) []*domain {
	result := slices.Clone(domains)
	slices.SortFunc(result, func(a, b *domain) int {
		if a.state == b.state {
			return slices.Compare(a.levelValues, b.levelValues)
		}
		// descending order.
		return cmp.Compare(b.state, a.state)
	})
	if useLeastFreeCapacityAlgorithm(unconstrained) {
		// start from the domain with the least amount of free resources
		slices.Reverse(result)
	}
	return result
}

func (s *TASFlavorSnapshot) fillInCounts(requests resources.Requests,
	assumedUsage map[utiltas.TopologyDomainID]resources.Requests,
	simulateEmpty bool,
	tolerations []corev1.Toleration) {
	for _, domain := range s.domains {
		// cleanup the state in case some remaining values are present from computing
		// assignments for previous PodSets.
		domain.state = 0
	}
	for _, leaf := range s.leaves {
		taint, untolerated := corev1helpers.FindMatchingUntoleratedTaint(leaf.nodeTaints, tolerations, func(t *corev1.Taint) bool {
			return t.Effect == corev1.TaintEffectNoSchedule || t.Effect == corev1.TaintEffectNoExecute
		})
		if untolerated {
			s.log.V(2).Info("excluding node with untolerated taint", "domainID", leaf.id, "taint", taint)
			continue
		}
		remainingCapacity := leaf.freeCapacity.Clone()
		if !simulateEmpty {
			remainingCapacity.Sub(leaf.tasUsage)
		}
		if leafAssumedUsage, found := assumedUsage[leaf.domain.id]; found {
			remainingCapacity.Sub(leafAssumedUsage)
		}
		leaf.state = requests.CountIn(remainingCapacity)
	}
	for _, root := range s.roots {
		root.state = s.fillInCountsHelper(root)
	}
}

func (s *TASFlavorSnapshot) fillInCountsHelper(domain *domain) int32 {
	// logic for a leaf
	if len(domain.children) == 0 {
		return domain.state
	}
	// logic for a parent
	childrenCapacity := int32(0)
	for _, child := range domain.children {
		childrenCapacity += s.fillInCountsHelper(child)
	}
	domain.state = childrenCapacity
	return childrenCapacity
}

func (s *TASFlavorSnapshot) notFitMessage(fitCount, totalCount int32) string {
	if fitCount == 0 {
		return fmt.Sprintf("topology %q doesn't allow to fit any of %v pod(s)", s.topologyName, totalCount)
	}
	return fmt.Sprintf("topology %q allows to fit only %v out of %v pod(s)", s.topologyName, fitCount, totalCount)
}

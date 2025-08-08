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
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	corev1helpers "k8s.io/component-helpers/scheduling/corev1"
	"k8s.io/utils/ptr"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
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

	// nodeLabels contains the list of labels on the node, only applies for
	// lowest level of topology, if the lowest level is node
	nodeLabels map[string]string
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
			leafDomain.nodeLabels = node.GetLabels()
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

type findTopologyAssignmentsOption struct {
	simulateEmpty bool
	workload      *kueue.Workload
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

// FindTopologyAssignmentsForFlavor returns TAS assignment, if possible, for all
// the TAS requests in the flavor handled by the snapshot.
func (s *TASFlavorSnapshot) FindTopologyAssignmentsForFlavor(flavorTASRequests FlavorTASRequests, options ...FindTopologyAssignmentsOption) TASAssignmentsResult {
	opts := &findTopologyAssignmentsOption{}
	for _, option := range options {
		option(opts)
	}

	result := make(map[kueue.PodSetReference]tasPodSetAssignmentResult)
	assumedUsage := make(map[utiltas.TopologyDomainID]resources.Requests)

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
		if workload.HasNodeToReplace(opts.workload) {
			for _, tr := range trs {
				// In case of looking for Node replacement, TopologyRequest has only
				// PodSets with the Node to replace, so we match PodSetAssignment
				psa := findPSA(opts.workload, tr.PodSet.Name)
				if psa == nil || psa.TopologyAssignment == nil {
					continue
				}
				// We deepCopy the existing TopologyAssignment, so if we delete unwanted domain,
				// And there is no fit, we have the original newAssignment to retry with
				newAssignment, replacementAssignment, reason := s.findReplacementAssignment(&tr, psa.TopologyAssignment.DeepCopy(), opts.workload, assumedUsage)
				result[tr.PodSet.Name] = tasPodSetAssignmentResult{TopologyAssignment: newAssignment, FailureReason: reason}
				if reason != "" {
					return result
				}
				addAssumedUsage(assumedUsage, replacementAssignment, &tr)
			}
		} else {
			leader, workers := findLeaderAndWorkers(trs)

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

// findReplacementAssignment finds the topology assignment for the replacement node
// it return new corrected topologyAssignment, a replacement topologyAssignment used to patched the old, faulty one, and
// reason if finding fails
func (s *TASFlavorSnapshot) findReplacementAssignment(tr *TASPodSetRequests, existingAssignment *kueue.TopologyAssignment, wl *kueue.Workload, assumedUsage map[utiltas.TopologyDomainID]resources.Requests) (*kueue.TopologyAssignment, *kueue.TopologyAssignment, string) {
	nodeToReplace := wl.Annotations[kueuealpha.NodeToReplaceAnnotation]
	tr.Count = deleteDomain(existingAssignment, nodeToReplace)
	if isStale, staleDomain := s.IsTopologyAssignmentStale(existingAssignment); isStale {
		return nil, nil, fmt.Sprintf("Cannot replace the node, because the existing topologyAssignment is invalid, as it contains the stale domain %v", staleDomain)
	}
	requiredReplacementDomain := s.requiredReplacementDomain(tr, existingAssignment)
	replacementAssignment, reason := s.findTopologyAssignment(*tr, nil, assumedUsage, false, requiredReplacementDomain)
	if reason != "" {
		return nil, nil, reason
	}
	newAssignment := s.mergeTopologyAssignments(replacementAssignment[tr.PodSet.Name], existingAssignment)
	return newAssignment, replacementAssignment[tr.PodSet.Name], ""
}

func addAssumedUsage(assumedUsage map[utiltas.TopologyDomainID]resources.Requests, ta *kueue.TopologyAssignment, tr *TASPodSetRequests) {
	for _, domain := range ta.Domains {
		domainID := utiltas.DomainID(domain.Values)
		if assumedUsage[domainID] == nil {
			assumedUsage[domainID] = resources.Requests{}
		}
		assumedUsage[domainID].Add(tr.SinglePodRequests.ScaledUp(int64(domain.Count)))
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

// requiredReplacementDomain returns required domain for the next pass of findingTopologyAssignment to be compliant with the existing one
func (s *TASFlavorSnapshot) requiredReplacementDomain(tr *TASPodSetRequests, ta *kueue.TopologyAssignment) utiltas.TopologyDomainID {
	key := s.levelKeyWithImpliedFallback(tr)
	if key == nil {
		return ""
	}
	levelIdx, found := s.resolveLevelIdx(*key)
	if !found {
		return ""
	}
	required := isRequired(tr.PodSet.TopologyRequest)
	if !required {
		return ""
	}
	// no domain to comply with so we don't require any domain at all
	// this happens when the faulty node was the only one in the assignment
	if len(ta.Domains) == 0 {
		return ""
	}

	nodeLevel := len(s.levelKeys) - 1
	// Since all Domains comply with the required policy, take a random one
	// in this case, the first one
	// We know at this point that values contains only hostname
	nodeDomain := ta.Domains[0].Values[0]
	domain := s.domainsPerLevel[nodeLevel][utiltas.TopologyDomainID(nodeDomain)]
	// Find a domain that complies with the required policy
	for i := nodeLevel; i > levelIdx; i-- {
		domain = domain.parent
	}
	return domain.id
}

// IsTopologyAssignmentStale indicates whether the topologyAssignment have Nodes
// that don't exists in the snapshot. It may be cause e.g. by Node deletion, or change
// in Node's NodeReady condition
func (s *TASFlavorSnapshot) IsTopologyAssignmentStale(ta *kueue.TopologyAssignment) (bool, string) {
	for _, domain := range ta.Domains {
		if _, found := s.domains[utiltas.DomainID(domain.Values)]; !found {
			return true, domain.Values[0]
		}
	}
	return false, ""
}

// deleteDomain deletes the domain the has faulty node and returns number of affected pods by the node
func deleteDomain(currentTopologyAssignment *kueue.TopologyAssignment, nodeToReplace string) int32 {
	var noAffectedPods int32 = 0
	updatedAssignment := make([]kueue.TopologyDomainAssignment, 0, len(currentTopologyAssignment.Domains))
	for _, domain := range currentTopologyAssignment.Domains {
		if domain.Values[len(domain.Values)-1] == nodeToReplace {
			noAffectedPods = domain.Count
		} else {
			updatedAssignment = append(updatedAssignment, domain)
		}
	}
	currentTopologyAssignment.Domains = updatedAssignment
	return noAffectedPods
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
	simulateEmpty bool, requiredReplacementDomain utiltas.TopologyDomainID) (map[kueue.PodSetReference]*kueue.TopologyAssignment, string) {
	requests := workersTasPodSetRequests.SinglePodRequests.Clone()
	requests.Add(resources.Requests{corev1.ResourcePods: 1})

	leaderCount := int32(0)

	var leaderRequests *resources.Requests
	if leaderTasPodSetRequests != nil {
		leaderRequests = ptr.To(leaderTasPodSetRequests.SinglePodRequests.Clone())
		leaderRequests.Add(resources.Requests{corev1.ResourcePods: 1})
		leaderCount = 1
	}

	info := podset.FromPodSet(workersTasPodSetRequests.PodSet)
	for _, podSetUpdate := range workersTasPodSetRequests.PodSetUpdates {
		if err := info.Merge(podset.FromUpdate(podSetUpdate)); err != nil {
			return nil, fmt.Sprintf("invalid podSetUpdate for PodSet %s, error: %s", workersTasPodSetRequests.PodSet.Name, err.Error())
		}
	}
	podSetTolerations := info.Tolerations
	podSetNodeSelectors := info.NodeSelector
	count := workersTasPodSetRequests.Count

	// If slice topology is not requested then we can assume that slice is a single pod
	sliceSize, reason := getSliceSizeWithSinglePodAsDefault(workersTasPodSetRequests.PodSet.TopologyRequest)
	if len(reason) > 0 {
		return nil, reason
	}

	required := isRequired(workersTasPodSetRequests.PodSet.TopologyRequest)
	topologyKey := s.levelKeyWithImpliedFallback(&workersTasPodSetRequests)
	sliceTopologyKey := s.sliceLevelKeyWithDefault(workersTasPodSetRequests.PodSet.TopologyRequest, s.lowestLevel())

	unconstrained := isUnconstrained(workersTasPodSetRequests.PodSet.TopologyRequest, &workersTasPodSetRequests)
	if topologyKey == nil {
		return nil, "topology level not specified"
	}
	levelIdx, found := s.resolveLevelIdx(*topologyKey)
	if !found {
		return nil, fmt.Sprintf("no requested topology level: %s", *topologyKey)
	}

	sliceLevelIdx, found := s.resolveLevelIdx(sliceTopologyKey)
	if !found {
		return nil, fmt.Sprintf("no requested topology level for slices: %s", sliceTopologyKey)
	}

	if levelIdx > sliceLevelIdx {
		return nil, fmt.Sprintf("podset slice topology %s is above the podset topology %s", sliceTopologyKey, *topologyKey)
	}

	var selector labels.Selector
	if s.isLowestLevelNode() {
		sel, err := labels.ValidatedSelectorFromSet(podSetNodeSelectors)
		if err != nil {
			return nil, fmt.Sprintf("invalid node selectors: %s, reason: %s", podSetNodeSelectors, err)
		}
		selector = sel
	} else {
		selector = labels.Everything()
	}
	// phase 1 - determine the number of pods and slices which can fit in each topology domain
	s.fillInCounts(
		requests,
		leaderRequests,
		assumedUsage,
		sliceSize,
		sliceLevelIdx,
		simulateEmpty,
		append(podSetTolerations, s.tolerations...),
		selector,
		requiredReplacementDomain,
	)

	// phase 2a: determine the level at which the assignment is done along with
	// the domains which can accommodate all pods/slices
	fitLevelIdx, currFitDomain, reason := s.findLevelWithFitDomains(levelIdx, required, count, leaderCount, sliceSize, unconstrained)
	if len(reason) > 0 {
		return nil, reason
	}

	// phase 2b: traverse the tree down level-by-level optimizing the number of
	// topology domains at each level
	// if unconstrained is set, we'll only do it once
	currFitDomain = s.updateSliceCountsToMinimum(currFitDomain, count, leaderCount, sliceSize, unconstrained)
	for levelIdx := fitLevelIdx; levelIdx+1 < len(s.domainsPerLevel); levelIdx++ {
		if levelIdx < sliceLevelIdx {
			// If we are "above" the requested slice topology level, we're greedily assigning pods/slices to
			// all domains without checking what we've assigned to parent domains.
			sortedLowerDomains := s.sortedDomains(s.lowerLevelDomains(currFitDomain), unconstrained)
			currFitDomain = s.updateSliceCountsToMinimum(sortedLowerDomains, count, leaderCount, sliceSize, unconstrained)
		} else {
			// If we are "at" or "below" the requested slice topology level, we have to carefully assign pods
			// to domains based on what we've assigned to parent domains, that's why we're iterating through
			// each parent domain and assigning `domain.state` amount of pods its child domains.
			newCurrFitDomain := make([]*domain, 0)
			for _, domain := range currFitDomain {
				sortedLowerDomains := s.sortedDomains(domain.children, unconstrained)

				addCurrFitDomain := s.updateCountsToMinimum(sortedLowerDomains, domain.state, domain.leaderState, unconstrained)
				newCurrFitDomain = append(newCurrFitDomain, addCurrFitDomain...)
			}
			currFitDomain = newCurrFitDomain
		}
	}

	assignments := make(map[kueue.PodSetReference]*kueue.TopologyAssignment)

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

func getSliceSizeWithSinglePodAsDefault(podSetTopologyRequest *kueue.PodSetTopologyRequest) (int32, string) {
	if podSetTopologyRequest == nil || podSetTopologyRequest.PodSetSliceRequiredTopology == nil {
		return 1, ""
	}

	if podSetTopologyRequest.PodSetSliceSize == nil {
		return 0, "slice topology requested, but slice size not provided"
	}

	return *podSetTopologyRequest.PodSetSliceSize, ""
}

// Merges two topology assignments keeping the lexicographical order of levelValues
func (s *TASFlavorSnapshot) mergeTopologyAssignments(a, b *kueue.TopologyAssignment) *kueue.TopologyAssignment {
	nodeLevel := len(s.levelKeys) - 1
	sortedDomains := make([]kueue.TopologyDomainAssignment, 0, len(a.Domains)+len(b.Domains))
	sortedDomains = append(sortedDomains, a.Domains...)
	sortedDomains = append(sortedDomains, b.Domains...)
	sort.Slice(sortedDomains, func(i, j int) bool {
		a, b := sortedDomains[i], sortedDomains[j]
		aDomain, bDomain := s.domainsPerLevel[nodeLevel][utiltas.DomainID(a.Values)], s.domainsPerLevel[nodeLevel][utiltas.DomainID(b.Values)]
		return utiltas.DomainID(aDomain.levelValues) < utiltas.DomainID(bDomain.levelValues)
	})
	mergedDomains := make([]kueue.TopologyDomainAssignment, 0, len(sortedDomains))
	for _, domain := range sortedDomains {
		if canMerge(mergedDomains, domain) {
			mergedDomains[len(mergedDomains)-1].Count += domain.Count
		} else {
			mergedDomains = append(mergedDomains, domain)
		}
	}
	return &kueue.TopologyAssignment{
		Levels:  a.Levels,
		Domains: mergedDomains,
	}
}

func canMerge(mergedDomains []kueue.TopologyDomainAssignment, domain kueue.TopologyDomainAssignment) bool {
	if len(mergedDomains) == 0 {
		return false
	}
	lastDomain := mergedDomains[len(mergedDomains)-1]
	return utiltas.DomainID(domain.Values) == utiltas.DomainID(lastDomain.Values)
}

func (s *TASFlavorSnapshot) HasLevel(r *kueue.PodSetTopologyRequest) bool {
	mainKey := s.levelKey(r)
	if mainKey == nil {
		return false
	}

	sliceKey := s.sliceLevelKeyWithDefault(r, s.lowestLevel())

	_, mainTopologyFound := s.resolveLevelIdx(*mainKey)
	_, sliceTopologyFound := s.resolveLevelIdx(sliceKey)

	return mainTopologyFound && sliceTopologyFound
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
	case isSliceTopologyOnlyRequest(topologyRequest):
		return ptr.To(s.highestLevel())
	case ptr.Deref(topologyRequest.Unconstrained, false):
		return ptr.To(s.lowestLevel())
	default:
		return nil
	}
}

func (s *TASFlavorSnapshot) sliceLevelKeyWithDefault(topologyRequest *kueue.PodSetTopologyRequest, defaultSliceLevelKey string) string {
	if topologyRequest != nil && topologyRequest.PodSetSliceRequiredTopology != nil {
		return *topologyRequest.PodSetSliceRequiredTopology
	}
	return defaultSliceLevelKey
}

func isUnconstrained(tr *kueue.PodSetTopologyRequest, tasRequests *TASPodSetRequests) bool {
	return (tr != nil && tr.Unconstrained != nil && *tr.Unconstrained) || tasRequests.Implied || isSliceTopologyOnlyRequest(tr)
}

func isSliceTopologyOnlyRequest(tr *kueue.PodSetTopologyRequest) bool {
	return tr != nil && tr.Required == nil && tr.Preferred == nil && tr.PodSetSliceRequiredTopology != nil
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
	bestDomain := domains[0]
	bestDomainState := state(bestDomain)

	for _, domain := range domains {
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

func (s *TASFlavorSnapshot) findLevelWithFitDomains(levelIdx int, required bool, podSetSize int32, leaderPodSetSize int32, sliceSize int32, unconstrained bool) (int, []*domain, string) {
	domains := s.domainsPerLevel[levelIdx]
	if len(domains) == 0 {
		return 0, nil, fmt.Sprintf("no topology domains at level: %s", s.levelKeys[levelIdx])
	}
	levelDomains := slices.Collect(maps.Values(domains))
	sortedDomain := s.sortedDomainsWithLeader(levelDomains, unconstrained)
	topDomain := sortedDomain[0]

	sliceCount := podSetSize / sliceSize
	if useBestFitAlgorithm(unconstrained) && topDomain.sliceStateWithLeader >= sliceCount && topDomain.leaderState >= leaderPodSetSize {
		// optimize the potentially last domain
		topDomain = findBestFitDomainForSlices(sortedDomain, sliceCount, leaderPodSetSize)
	}
	if useLeastFreeCapacityAlgorithm(unconstrained) {
		for _, candidateDomain := range sortedDomain {
			if candidateDomain.sliceState >= sliceCount {
				return levelIdx, []*domain{candidateDomain}, ""
			}
		}
		if required {
			maxCapacityFound := sortedDomain[len(sortedDomain)-1].state
			return 0, nil, s.notFitMessage(maxCapacityFound, sliceCount, sliceSize)
		}
	}
	if topDomain.sliceStateWithLeader < sliceCount || topDomain.leaderState < leaderPodSetSize {
		if required {
			return 0, nil, s.notFitMessage(topDomain.sliceState, sliceCount, sliceSize)
		}
		if levelIdx > 0 && !unconstrained {
			return s.findLevelWithFitDomains(levelIdx-1, required, podSetSize, leaderPodSetSize, sliceSize, unconstrained)
		}
		results := []*domain{}
		remainingSliceCount := sliceCount
		remainingLeaderCount := leaderPodSetSize

		// Domains are sorted in a way that prioritizes domains with higher leader capacity.
		// We want to assign leaders first. After we are assign all leaders, we sort the remaining
		// "unused" domains based on worker capacity (sliceState, state and then levelValues) and
		// try to assign remaining workers.
		idx := 0
		for ; remainingLeaderCount > 0 && idx < len(sortedDomain) && sortedDomain[idx].leaderState > 0; idx++ {
			domain := sortedDomain[idx]
			if useBestFitAlgorithm(unconstrained) && sortedDomain[idx].sliceStateWithLeader >= remainingSliceCount {
				// optimize the last domain
				domain = findBestFitDomainForSlices(sortedDomain[idx:], remainingSliceCount, remainingLeaderCount)
			}
			results = append(results, domain)

			remainingLeaderCount -= domain.leaderState
			remainingSliceCount -= domain.sliceStateWithLeader
		}
		if remainingLeaderCount > 0 {
			return 0, nil, s.notFitMessage(leaderPodSetSize-remainingLeaderCount, sliceCount, sliceSize)
		}

		// At this point we have assigned all leaders, so we sort remaining domains based on worker capacity
		// and assign remaining workers.
		sortedDomain = s.sortedDomains(sortedDomain[idx:], unconstrained)
		for idx := 0; remainingSliceCount > 0 && idx < len(sortedDomain) && sortedDomain[idx].sliceState > 0; idx++ {
			domain := sortedDomain[idx]
			if useBestFitAlgorithm(unconstrained) && sortedDomain[idx].sliceState >= remainingSliceCount {
				// optimize the last domain
				domain = findBestFitDomainForSlices(sortedDomain[idx:], remainingSliceCount, 0)
			}
			results = append(results, domain)

			remainingSliceCount -= domain.sliceState
		}
		if remainingSliceCount > 0 {
			return 0, nil, s.notFitMessage(sliceCount-remainingSliceCount, sliceCount, sliceSize)
		}
		return levelIdx, results, ""
	}
	return levelIdx, []*domain{topDomain}, ""
}

func useBestFitAlgorithm(unconstrained bool) bool {
	// following the matrix from KEP#2724
	return !useLeastFreeCapacityAlgorithm(unconstrained)
}

func useLeastFreeCapacityAlgorithm(unconstrained bool) bool {
	// following the matrix from KEP#2724
	return features.Enabled(features.TASProfileLeastFreeCapacity) ||
		(unconstrained && features.Enabled(features.TASProfileMixed))
}

func (s *TASFlavorSnapshot) updateSliceCountsToMinimum(domains []*domain, count int32, leaderCount int32, sliceSize int32, unconstrained bool) []*domain {
	result := make([]*domain, 0)
	remainingSlices := count / sliceSize
	remainingLeaderCount := leaderCount
	for i, domain := range domains {
		if remainingLeaderCount > 0 {
			if useBestFitAlgorithm(unconstrained) && domain.sliceStateWithLeader >= remainingSlices && domain.leaderState >= remainingLeaderCount {
				// optimize the last domain
				domain = findBestFitDomainForSlices(domains[i:], remainingSlices, remainingLeaderCount)
			}

			if domain.sliceStateWithLeader >= remainingSlices && domain.leaderState >= remainingLeaderCount {
				domain.state = remainingSlices * sliceSize
				domain.sliceState = remainingSlices
				domain.leaderState = remainingLeaderCount
				result = append(result, domain)
				return result
			}

			if domain.sliceStateWithLeader > remainingSlices {
				domain.sliceStateWithLeader = remainingSlices
			}

			if domain.leaderState > remainingLeaderCount {
				domain.leaderState = remainingLeaderCount
			}

			domain.state = domain.sliceStateWithLeader * sliceSize
			remainingLeaderCount -= domain.leaderState
			remainingSlices -= domain.sliceStateWithLeader

			result = append(result, domain)
		} else {
			if useBestFitAlgorithm(unconstrained) && domain.sliceState >= remainingSlices {
				// optimize the last domain
				domain = findBestFitDomainForSlices(domains[i:], remainingSlices, 0)
			}

			domain.leaderState = 0

			if domain.sliceState >= remainingSlices {
				domain.state = remainingSlices * sliceSize
				domain.sliceState = remainingSlices
				result = append(result, domain)
				return result
			}
			domain.state = domain.sliceState * sliceSize
			remainingSlices -= domain.sliceState
			result = append(result, domain)
		}
	}
	s.log.Error(errCodeAssumptionsViolated, "unexpected remainingCount",
		"remainingSlices", remainingSlices,
		"remainingLeaderCount", remainingLeaderCount,
		"count", count,
		"leaves", s.leaves)
	return nil
}

func (s *TASFlavorSnapshot) updateCountsToMinimum(domains []*domain, count int32, leaderCount int32, unconstrained bool) []*domain {
	result := make([]*domain, 0)
	remainingCount := count
	remainingLeaderCount := leaderCount

	for i, domain := range domains {
		if remainingLeaderCount > 0 {
			if useBestFitAlgorithm(unconstrained) && domain.stateWithLeader >= remainingCount && domain.leaderState >= remainingLeaderCount {
				// optimize the last domain
				domain = findBestFitDomain(domains[i:], remainingCount, remainingLeaderCount)
			}

			if domain.stateWithLeader >= remainingCount && domain.leaderState >= remainingLeaderCount {
				domain.state = remainingCount
				domain.leaderState = remainingLeaderCount
				result = append(result, domain)
				return result
			}
			remainingCount -= domain.stateWithLeader
			remainingLeaderCount -= domain.leaderState

			if domain.stateWithLeader > remainingCount {
				domain.stateWithLeader = remainingCount
			}
			if domain.leaderState > remainingLeaderCount {
				domain.leaderState = remainingLeaderCount
			}
			result = append(result, domain)
		} else {
			if useBestFitAlgorithm(unconstrained) && domain.state >= remainingCount {
				// optimize the last domain
				domain = findBestFitDomain(domains[i:], remainingCount, 0)
			}

			domain.leaderState = 0

			if domain.state >= remainingCount {
				domain.state = remainingCount
				result = append(result, domain)
				return result
			}
			remainingCount -= domain.state
			result = append(result, domain)
		}
	}
	s.log.Error(errCodeAssumptionsViolated, "unexpected remainingCount",
		"remainingCount", remainingCount,
		"remainingLeaderCount", remainingLeaderCount,
		"count", count,
		"leaves", s.leaves)
	return nil
}

// buildTopologyAssignmentForLevels build TopologyAssignment for levels starting from levelIdx
func (s *TASFlavorSnapshot) buildTopologyAssignmentForLevels(domains []*domain, levelIdx int) *kueue.TopologyAssignment {
	assignment := &kueue.TopologyAssignment{
		Domains: make([]kueue.TopologyDomainAssignment, 0),
	}
	assignment.Levels = s.levelKeys[levelIdx:]
	for _, domain := range domains {
		if domain.state == 0 {
			// It may happen when PodSet count is 0.
			continue
		}
		assignment.Domains = append(assignment.Domains, kueue.TopologyDomainAssignment{
			Values: domain.levelValues[levelIdx:],
			Count:  domain.state,
		})
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

func (s *TASFlavorSnapshot) sortedDomainsWithLeader(domains []*domain, unconstrained bool) []*domain {
	isLeastFreeCapacity := useLeastFreeCapacityAlgorithm(unconstrained)
	result := slices.Clone(domains)
	slices.SortFunc(result, func(a, b *domain) int {
		if a.leaderState != b.leaderState {
			return cmp.Compare(b.leaderState, a.leaderState)
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

func (s *TASFlavorSnapshot) fillInCounts(
	requests resources.Requests,
	leaderRequests *resources.Requests,
	assumedUsage map[utiltas.TopologyDomainID]resources.Requests,
	sliceSize int32,
	sliceLevelIdx int,
	simulateEmpty bool,
	tolerations []corev1.Toleration,
	selector labels.Selector,
	requiredReplacementDomain utiltas.TopologyDomainID) {
	for _, domain := range s.domains {
		// cleanup the state in case some remaining values are present from computing
		// assignments for previous PodSets.
		domain.state = 0
		domain.stateWithLeader = 0
		domain.sliceState = 0
		domain.sliceStateWithLeader = 0
		domain.leaderState = 0
	}
	for _, leaf := range s.leaves {
		// 1. Check Tolerations against Node Taints
		taint, untolerated := corev1helpers.FindMatchingUntoleratedTaint(leaf.nodeTaints, tolerations, func(t *corev1.Taint) bool {
			return t.Effect == corev1.TaintEffectNoSchedule || t.Effect == corev1.TaintEffectNoExecute
		})
		if untolerated {
			s.log.V(3).Info("excluding node with untolerated taint", "domainID", leaf.id, "taint", taint)
			continue
		}
		// 2. Check Node Labels against Compiled Selector
		var nodeLabelSet labels.Set
		if leaf.nodeLabels != nil {
			nodeLabelSet = leaf.nodeLabels
		}

		// 3. While correcting the topologyAssignment with a failed node
		// check if the leaf belongs to the required domain
		if !belongsToRequiredDomain(leaf, requiredReplacementDomain) {
			continue
		}

		// isLowestLevelNode() is necessary because we gather node level information only when
		// node is the lowest level of the topology
		if s.isLowestLevelNode() && !selector.Matches(nodeLabelSet) {
			s.log.V(3).Info("excluding node that doesn't match nodeSelectors", "domainID", leaf.id, "nodeLabels", nodeLabelSet)
			continue
		}
		remainingCapacity := leaf.freeCapacity.Clone()
		if !simulateEmpty {
			remainingCapacity.Sub(leaf.tasUsage)
		}
		if leafAssumedUsage, found := assumedUsage[leaf.id]; found {
			remainingCapacity.Sub(leafAssumedUsage)
		}
		leaf.state = requests.CountIn(remainingCapacity)

		leaf.leaderState = 0
		if leaderRequests != nil && leaderRequests.CountIn(remainingCapacity) > 0 {
			leaf.leaderState = 1
			remainingCapacity.Sub(*leaderRequests)
		}

		leaf.stateWithLeader = requests.CountIn(remainingCapacity)
	}
	for _, root := range s.roots {
		root.state, root.sliceState, root.stateWithLeader, root.sliceStateWithLeader, root.leaderState = s.fillInCountsHelper(root, sliceSize, sliceLevelIdx, 0)
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

func (s *TASFlavorSnapshot) fillInCountsHelper(domain *domain, sliceSize int32, sliceLevelIdx int, level int) (int32, int32, int32, int32, int32) {
	// logic for a leaf
	if len(domain.children) == 0 {
		if level == sliceLevelIdx {
			// initialize the sliceState if leaf is the request slice level
			domain.sliceState = domain.state / sliceSize
			domain.sliceStateWithLeader = domain.stateWithLeader / sliceSize
		}
		return domain.state, domain.sliceState, domain.stateWithLeader, domain.sliceStateWithLeader, domain.leaderState
	}
	// logic for a parent
	childrenCapacity := int32(0)
	sliceCapacity := int32(0)

	minStateWithLeaderDifference := int32(math.MaxInt32)
	minSliceStateWithLeaderDifference := int32(math.MaxInt32)
	leaderState := int32(0)

	for _, child := range domain.children {
		addChildrenCapacity, addChildrenSliceCapacity, addChildrenCapacityWithLeader, addChildrenSliceCapacityWithLeader, childLeaderState := s.fillInCountsHelper(child, sliceSize, sliceLevelIdx, level+1)
		childrenCapacity += addChildrenCapacity
		sliceCapacity += addChildrenSliceCapacity
		if addChildrenCapacity-addChildrenCapacityWithLeader < minStateWithLeaderDifference {
			minStateWithLeaderDifference = addChildrenCapacity - addChildrenCapacityWithLeader
		}
		if addChildrenSliceCapacity-addChildrenSliceCapacityWithLeader < minSliceStateWithLeaderDifference {
			minSliceStateWithLeaderDifference = addChildrenSliceCapacity - addChildrenSliceCapacityWithLeader
		}
		if childLeaderState > leaderState {
			leaderState = childLeaderState
		}
	}
	domain.state = childrenCapacity
	domain.stateWithLeader = childrenCapacity - minStateWithLeaderDifference
	domain.leaderState = leaderState
	sliceStateWithLeader := sliceCapacity - minSliceStateWithLeaderDifference

	if level == sliceLevelIdx {
		// initialize the sliceState for the requested slice level.
		sliceCapacity = domain.state / sliceSize
		sliceStateWithLeader = domain.stateWithLeader / sliceSize
	}
	domain.sliceState = sliceCapacity
	domain.sliceStateWithLeader = sliceStateWithLeader

	return domain.state, domain.sliceState, domain.stateWithLeader, domain.sliceStateWithLeader, domain.leaderState
}

func (s *TASFlavorSnapshot) notFitMessage(slicesFitCount, totalRequestsSlicesCount, sliceSize int32) string {
	if sliceSize == 1 {
		// each slice is a single pod, so let's refer to them as pods
		if slicesFitCount == 0 {
			return fmt.Sprintf("topology %q doesn't allow to fit any of %d pod(s)", s.topologyName, totalRequestsSlicesCount)
		}
		return fmt.Sprintf("topology %q allows to fit only %d out of %d pod(s)", s.topologyName, slicesFitCount, totalRequestsSlicesCount)
	}

	if slicesFitCount == 0 {
		return fmt.Sprintf("topology %q doesn't allow to fit any of %d slice(s)", s.topologyName, totalRequestsSlicesCount)
	}
	return fmt.Sprintf("topology %q allows to fit only %d out of %d slice(s)", s.topologyName, slicesFitCount, totalRequestsSlicesCount)
}

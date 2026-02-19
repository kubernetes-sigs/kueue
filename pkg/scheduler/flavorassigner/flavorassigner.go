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

package flavorassigner

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	corev1helpers "k8s.io/component-helpers/scheduling/corev1"
	"k8s.io/component-helpers/scheduling/corev1/nodeaffinity"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/classical"
	preemptioncommon "sigs.k8s.io/kueue/pkg/scheduler/preemption/common"
	utilmaps "sigs.k8s.io/kueue/pkg/util/maps"
	"sigs.k8s.io/kueue/pkg/util/orderedgroups"
	"sigs.k8s.io/kueue/pkg/util/tas"
	"sigs.k8s.io/kueue/pkg/workload"
)

type Assignment struct {
	PodSets []PodSetAssignment
	// Borrowing is the height of the smallest cohort tree that fits
	// the additional Usage. It equals to 0 if no borrowing is required.
	Borrowing int
	LastState workload.AssignmentClusterQueueState

	// Usage is the accumulated Usage of resources as pod sets get
	// flavors assigned. When workload slicing is enabled and replaceWorkloadSlice
	// is set, this represents only the delta usage (new - old) to avoid double-counting
	// resources already reserved in the replaced slice.
	Usage workload.Usage

	// representativeMode is the cached representative mode for this assignment.
	representativeMode *FlavorAssignmentMode

	// replaceWorkloadSlice identifies the workload slice that will be replaced by this workload.
	// It is needed to correctly compute TotalRequestsFor applying (subtracting) replaced
	// workload resources quantities.
	//
	// Note: This value may be nil in the following cases:
	//   - Workload slicing is not enabled (either globally or for this specific workload).
	//   - The current workload does not represent a scale-up slice.
	// In these scenarios, flavor assignment proceeds as in the original flow—i.e., as for regular,
	// non-sliced workloads.
	replaceWorkloadSlice *workload.Info
}

// UpdateForTASResult updates the Assignment with the TAS result
func (a *Assignment) UpdateForTASResult(result schdcache.TASAssignmentsResult) {
	for psName, psResult := range result {
		psAssignment := a.podSetAssignmentByName(psName)
		psAssignment.TopologyAssignment = psResult.TopologyAssignment
		if psResult.TopologyAssignment != nil && psAssignment.DelayedTopologyRequest != nil {
			psAssignment.DelayedTopologyRequest = ptr.To(kueue.DelayedTopologyRequestStateReady)
		}
	}
	a.Usage.TAS = a.ComputeTASNetUsage(nil)
}

// ComputeTASNetUsage computes the net TAS usage for the assignment
func (a *Assignment) ComputeTASNetUsage(prevAdmission *kueue.Admission) workload.TASUsage {
	result := make(workload.TASUsage)
	for i, psa := range a.PodSets {
		if psa.TopologyAssignment != nil {
			if prevAdmission != nil && prevAdmission.PodSetAssignments[i].TopologyAssignment != nil {
				continue
			}
			singlePodRequests := resources.NewRequests(psa.Requests).ScaledDown(int64(psa.Count))
			for _, flv := range psa.Flavors {
				if _, ok := result[flv.Name]; !ok {
					result[flv.Name] = make(workload.TASFlavorUsage, 0)
				}
				for _, domain := range psa.TopologyAssignment.Domains {
					result[flv.Name] = append(result[flv.Name], workload.TopologyDomainRequests{
						Values:            domain.Values,
						SinglePodRequests: singlePodRequests.Clone(),
						Count:             domain.Count,
					})
				}
			}
		}
	}
	return result
}

// Borrows return whether assignment requires borrowing.
func (a *Assignment) Borrows() int {
	return a.Borrowing
}

func (a *Assignment) podSetAssignmentByName(psName kueue.PodSetReference) *PodSetAssignment {
	if idx := slices.IndexFunc(a.PodSets, func(ps PodSetAssignment) bool { return ps.Name == psName }); idx != -1 {
		return &a.PodSets[idx]
	}
	return nil
}

// RepresentativeMode calculates the representative mode for the assignment as
// the worst assignment mode among all the pod sets.
func (a *Assignment) RepresentativeMode() FlavorAssignmentMode {
	if len(a.PodSets) == 0 {
		// No assignments calculated.
		return NoFit
	}
	if a.representativeMode != nil {
		return *a.representativeMode
	}
	mode := Fit
	for _, ps := range a.PodSets {
		psMode := ps.RepresentativeMode()
		if psMode < mode {
			mode = psMode
		}
	}
	a.representativeMode = &mode
	return mode
}

func (a *Assignment) Message() string {
	var builder strings.Builder
	for _, ps := range a.PodSets {
		if ps.Status.IsFit() {
			continue
		}
		if ps.Status.IsError() {
			return fmt.Sprintf("failed to assign flavors to pod set %s: %v", ps.Name, ps.Status.err)
		}
		if builder.Len() > 0 {
			builder.WriteString("; ")
		}
		builder.WriteString("couldn't assign flavors to pod set ")
		builder.WriteString(string(ps.Name))
		builder.WriteString(": ")
		builder.WriteString(ps.Status.Message())
	}
	return builder.String()
}

func (a *Assignment) ToAPI() []kueue.PodSetAssignment {
	psFlavors := make([]kueue.PodSetAssignment, len(a.PodSets))
	for i := range psFlavors {
		psFlavors[i] = a.PodSets[i].toAPI()
	}
	return psFlavors
}

// TotalRequestsFor - returns the total quota needs of the wl, taking into account the potential
// workload slice replacement, or scaling needed in case of partial admission.
//
// Note: ElasticJobsViaWorkloadSlices is mutually exclusive with PartialAdmission.
func (a *Assignment) TotalRequestsFor(wl *workload.Info) resources.FlavorResourceQuantities {
	usage := make(resources.FlavorResourceQuantities)
	for i, ps := range wl.TotalRequests {
		newCount := a.PodSets[i].Count
		if a.replaceWorkloadSlice != nil {
			newCount = ps.Count - a.replaceWorkloadSlice.TotalRequests[i].Count
		}
		ps = *ps.ScaledTo(newCount)

		for res, q := range ps.Requests {
			// zero-quantity request may have no flavor (#8079), and is irrelevant for
			// later calculations
			if q == 0 {
				continue
			}
			flv := a.PodSets[i].Flavors[res].Name
			usage[resources.FlavorResource{Flavor: flv, Resource: res}] += q
		}
	}
	return usage
}

type Status struct {
	reasons []string
	err     error
}

func NewStatus(reasons ...string) *Status {
	return &Status{
		reasons: reasons,
	}
}

func (s *Status) IsFit() bool {
	return s == nil || (s.err == nil && len(s.reasons) == 0)
}

func (s *Status) IsError() bool {
	return s != nil && s.err != nil
}

func (s *Status) appendf(format string, args ...any) *Status {
	s.reasons = append(s.reasons, fmt.Sprintf(format, args...))
	return s
}

func (s *Status) Message() string {
	if s == nil {
		return ""
	}
	if s.err != nil {
		return s.err.Error()
	}
	sort.Strings(s.reasons)
	return strings.Join(s.reasons, ", ")
}

// PodSetAssignment holds the assigned flavors and status messages for each of
// the resources that the pod set requests. Each assigned flavor is accompanied
// with an AssignmentMode.
// Empty .Flavors can be interpreted as NoFit mode for all the resources.
// Empty .Status can be interpreted as Fit mode for all the resources.
// .Flavors and .Status can't be empty at the same time, once PodSetAssignment
// is fully calculated.
type PodSetAssignment struct {
	Name     kueue.PodSetReference
	Flavors  ResourceAssignment
	Status   Status
	Requests corev1.ResourceList
	Count    int32

	TopologyAssignment     *tas.TopologyAssignment
	DelayedTopologyRequest *kueue.DelayedTopologyRequestState

	FlavorAssignmentAttempts []FlavorAssignmentAttempt
}

// RepresentativeMode calculates the representative mode for this assignment as
// the worst assignment mode among all assigned flavors.
func (psa *PodSetAssignment) RepresentativeMode() FlavorAssignmentMode {
	if psa.Status.IsFit() {
		return Fit
	}
	if psa.Status.IsError() {
		// e.g. onlyFlavor failed in WorkloadsTopologyRequests, or TAS request build failed
		return NoFit
	}
	if len(psa.Flavors) == 0 {
		return NoFit
	}
	mode := Fit
	for _, flvAssignment := range psa.Flavors {
		if flvAssignment.Mode < mode {
			mode = flvAssignment.Mode
		}
	}
	return mode
}

func (psa *PodSetAssignment) updateMode(newMode FlavorAssignmentMode) {
	for _, flvAssignment := range psa.Flavors {
		flvAssignment.Mode = newMode
	}
}

func (psa *PodSetAssignment) reason(reason string) {
	psa.Status.reasons = append(psa.Status.reasons, reason)
}

func (psa *PodSetAssignment) error(err error) {
	psa.Status.err = err
}

type ResourceAssignment map[corev1.ResourceName]*FlavorAssignment

func (psa *PodSetAssignment) toAPI() kueue.PodSetAssignment {
	flavors := make(map[corev1.ResourceName]kueue.ResourceFlavorReference, len(psa.Flavors))
	// Only include resources with assigned flavors (filters out zero-quantity requests for undefined resources).
	resourceUsage := make(corev1.ResourceList, len(psa.Flavors))
	for res, flvAssignment := range psa.Flavors {
		flavors[res] = flvAssignment.Name
		resourceUsage[res] = psa.Requests[res]
	}
	return kueue.PodSetAssignment{
		Name:                   psa.Name,
		Flavors:                flavors,
		ResourceUsage:          resourceUsage,
		Count:                  ptr.To(psa.Count),
		TopologyAssignment:     tas.V1Beta2From(psa.TopologyAssignment),
		DelayedTopologyRequest: psa.DelayedTopologyRequest,
	}
}

// FlavorAssignmentMode describes whether the flavor can be assigned immediately
// or what needs to happen, so it can be assigned.
type FlavorAssignmentMode int

// The flavor assignment modes below are ordered from lowest to highest
// preference.
const (
	// NoFit means that there is not enough quota to assign this flavor,
	// or we require preemption but we are already borrowing, and policy
	// does not allow this.
	NoFit FlavorAssignmentMode = iota
	// Preempt indicates that admission is possible given Quotas.
	// Preemption may be impossible due to policy/limits/priorities.
	Preempt
	// Fit means that there is enough unused quota to assign to this Flavor
	// without preeemption, potentially with borrowing.
	Fit
)

func (m FlavorAssignmentMode) String() string {
	switch m {
	case NoFit:
		return "NoFit"
	case Preempt:
		return "Preempt"
	case Fit:
		return "Fit"
	}
	return "Unknown"
}

// borrowingLevel represents how locally the quota can be sourced. 0
// indicates that quota is available within the ClusterQueue, while
// progressively higher numbers indicate capacity comes from a more
// distant cohort.  Please note that while this number is
// monotonically increasing, it is not necessarily sequential.
type borrowingLevel int

// betterThan indicates that Flavor represented by b has NominalQuota
// available more locally than the flavor represented by other.
func (b borrowingLevel) betterThan(other borrowingLevel) bool {
	return b < other
}

// optimal indicates that capacity is available at the ClusterQueue
// level, i.e. no borrowing.
func (b borrowingLevel) optimal() bool {
	return b == 0
}

// granularMode is the FlavorAssignmentMode internal to
// FlavorAssigner, which lets us distinguish priority based
// preemption, reclamation within Cohort and borrowing.
type granularMode struct {
	preemptionMode preemptionMode
	borrowingLevel borrowingLevel
}

func worstGranularMode() granularMode {
	return granularMode{preemptionMode: noFit, borrowingLevel: math.MaxInt}
}

func bestGranularMode() granularMode {
	return granularMode{preemptionMode: fit, borrowingLevel: 0}
}

type preemptionMode int

const (
	noFit preemptionMode = iota
	// noPreemptionCandidates indicates that admission is possible with
	// preemption, but simulation found no preemption targets.
	noPreemptionCandidates
	preempt
	reclaim
	fit
)

// isPreferred returns true if mode a is better than b according to the selected policy
func isPreferred(a, b granularMode, fungibilityConfig kueue.FlavorFungibility) bool {
	if a.preemptionMode == noFit {
		return false
	}
	if b.preemptionMode == noFit {
		return true
	}

	borrowingOverPreemption := func() bool {
		if a.preemptionMode != b.preemptionMode {
			return a.preemptionMode > b.preemptionMode
		}
		return a.borrowingLevel.betterThan(b.borrowingLevel)
	}
	preemptionOverBorrowing := func() bool {
		if a.borrowingLevel != b.borrowingLevel {
			return a.borrowingLevel.betterThan(b.borrowingLevel)
		}
		return a.preemptionMode > b.preemptionMode
	}

	if fungibilityConfig.Preference != nil {
		switch *fungibilityConfig.Preference {
		case kueue.BorrowingOverPreemption:
			return preemptionOverBorrowing()
		case kueue.PreemptionOverBorrowing:
			return borrowingOverPreemption()
		}
	}

	return borrowingOverPreemption()
}

func fromPreemptionPossibility(preemptionPossibility preemptioncommon.PreemptionPossibility) preemptionMode {
	switch preemptionPossibility {
	case preemptioncommon.NoCandidates:
		return noPreemptionCandidates
	case preemptioncommon.Preempt:
		return preempt
	case preemptioncommon.Reclaim:
		return reclaim
	}
	panic(fmt.Sprintf("illegal PreemptionPossibility: %d", preemptionPossibility))
}

func (mode preemptionMode) preemptionPossibility() *preemptioncommon.PreemptionPossibility {
	switch mode {
	case noPreemptionCandidates:
		return ptr.To(preemptioncommon.NoCandidates)
	case preempt:
		return ptr.To(preemptioncommon.Preempt)
	case reclaim:
		return ptr.To(preemptioncommon.Reclaim)
	case fit, noFit:
		return nil
	default:
		panic(fmt.Sprintf("illegal preemptionMode: %d", mode))
	}
}

func (mode preemptionMode) flavorAssignmentMode() FlavorAssignmentMode {
	switch mode {
	case noFit:
		return NoFit
	case noPreemptionCandidates:
		return Preempt
	case preempt:
		return Preempt
	case reclaim:
		return Preempt
	case fit:
		return Fit
	default:
		panic(fmt.Sprintf("illegal granularMode: %d", mode))
	}
}

// isPreemptMode indicates a mode where preemption targets were found.
func (mode granularMode) isPreemptMode() bool {
	return mode.preemptionMode == preempt || mode.preemptionMode == reclaim
}

type FlavorAssignment struct {
	Name           kueue.ResourceFlavorReference
	Mode           FlavorAssignmentMode
	TriedFlavorIdx int
	borrow         int
}

type preemptionOracle interface {
	SimulatePreemption(log logr.Logger, cq *schdcache.ClusterQueueSnapshot, wl workload.Info, fr resources.FlavorResource, quantity int64) (preemptioncommon.PreemptionPossibility, int)
}

type FlavorAssigner struct {
	wl                *workload.Info
	cq                *schdcache.ClusterQueueSnapshot
	resourceFlavors   map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor
	enableFairSharing bool
	oracle            preemptionOracle

	// replaceWorkloadSlice identifies the workload slice that will be replaced by this workload.
	// It must be considered during flavor computation and included in the preemption targets.
	//
	// Note: This value may be nil in the following cases:
	//   - Workload slicing is not enabled (either globally or for this specific workload).
	//   - The current workload does not represent a scale-up slice.
	// In these scenarios, flavor assignment proceeds as in the original flow—i.e., as for regular,
	// non-sliced workloads.
	replaceWorkloadSlice *workload.Info
}

func New(wl *workload.Info, cq *schdcache.ClusterQueueSnapshot, resourceFlavors map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor, enableFairSharing bool, oracle preemptionOracle, preemptWorkloadSlice *workload.Info) *FlavorAssigner {
	return &FlavorAssigner{
		wl:                   wl,
		cq:                   cq,
		resourceFlavors:      resourceFlavors,
		enableFairSharing:    enableFairSharing,
		oracle:               oracle,
		replaceWorkloadSlice: preemptWorkloadSlice,
	}
}

func lastAssignmentOutdated(wl *workload.Info, cq *schdcache.ClusterQueueSnapshot) bool {
	return cq.AllocatableResourceGeneration > wl.LastAssignment.ClusterQueueGeneration
}

// Assign assigns a flavor to each of the resources requested in each pod set.
// The result for each pod set is accompanied with reasons why the flavor can't
// be assigned immediately. Each assigned flavor is accompanied with a
// FlavorAssignmentMode.
func (a *FlavorAssigner) Assign(log logr.Logger, counts []int32) Assignment {
	if a.wl.LastAssignment != nil && lastAssignmentOutdated(a.wl, a.cq) {
		if logV := log.V(6); logV.Enabled() {
			keysValues := []any{
				"cq.AllocatableResourceGeneration", a.cq.AllocatableResourceGeneration,
				"wl.LastAssignment.ClusterQueueGeneration", a.wl.LastAssignment.ClusterQueueGeneration,
			}
			logV.Info("Clearing Workload's last assignment because it was outdated", keysValues...)
		}
		a.wl.LastAssignment = nil
	}
	return a.assignFlavors(log, counts)
}

type indexedPodSet struct {
	originalIndex    int
	podSet           *workload.PodSetResources
	podSetAssignment *PodSetAssignment
}

func (a *FlavorAssigner) assignFlavors(log logr.Logger, counts []int32) Assignment {
	var requests []workload.PodSetResources
	if len(counts) == 0 {
		requests = a.wl.TotalRequests
	} else {
		requests = make([]workload.PodSetResources, len(a.wl.TotalRequests))
		for i := range a.wl.TotalRequests {
			requests[i] = *a.wl.TotalRequests[i].ScaledTo(counts[i])
		}
	}
	assignment := Assignment{
		PodSets: make([]PodSetAssignment, 0, len(requests)),
		Usage: workload.Usage{
			Quota: make(resources.FlavorResourceQuantities),
		},
		LastState: workload.AssignmentClusterQueueState{
			LastTriedFlavorIdx:     make([]map[corev1.ResourceName]int, 0, len(requests)),
			ClusterQueueGeneration: a.cq.AllocatableResourceGeneration,
		},
		replaceWorkloadSlice: a.replaceWorkloadSlice,
	}

	groupedRequests := orderedgroups.NewOrderedGroups[string, indexedPodSet]()

	for i, podSet := range requests {
		if a.cq.RGByResource(corev1.ResourcePods) != nil {
			podSet.Requests[corev1.ResourcePods] = int64(podSet.Count)
		}

		psAssignment := PodSetAssignment{
			Name:     podSet.Name,
			Flavors:  make(ResourceAssignment, len(podSet.Requests)),
			Requests: podSet.Requests.ToResourceList(),
			Count:    podSet.Count,
		}

		if features.Enabled(features.TopologyAwareScheduling) {
			// Respect preexisting assignments. The PodSet assignments may be
			// already set if this is the second pass of scheduler.
			for resName, fName := range podSet.Flavors {
				psAssignment.Flavors[resName] = &FlavorAssignment{
					Name: fName,
					Mode: Fit,
				}
			}
			if podSet.DelayedTopologyRequest != nil {
				psAssignment.DelayedTopologyRequest = ptr.To(*podSet.DelayedTopologyRequest)
			}
			if podSet.TopologyRequest != nil {
				psAssignment.TopologyAssignment = tas.InternalFrom(a.wl.Obj.Status.Admission.PodSetAssignments[i].TopologyAssignment)
			}
		}

		groupKey := strconv.Itoa(i)
		if tr := a.wl.Obj.Spec.PodSets[i].TopologyRequest; tr != nil && tr.PodSetGroupName != nil {
			groupKey = *tr.PodSetGroupName
		}

		groupedRequests.Insert(groupKey, indexedPodSet{originalIndex: i, podSet: &podSet, podSetAssignment: &psAssignment})
	}

	for _, podSets := range groupedRequests.InOrder {
		requests := make(resources.Requests)
		psIDs := make([]int, len(podSets))
		for idx, podset := range podSets {
			psIDs[idx] = podset.originalIndex
			requests.Add(podset.podSet.Requests)
		}

		consideredFlavors := make(map[kueue.ResourceFlavorReference]FlavorAssignmentAttempt)

		groupFlavors := make(ResourceAssignment)
		for _, ips := range podSets {
			for resName := range ips.podSetAssignment.Flavors {
				groupFlavors[resName] = ips.podSetAssignment.Flavors[resName]
				break
			}
		}
		var groupStatus Status
		for resName, quantity := range requests {
			// Skip zero-quantity requests for resources not defined in the ClusterQueue (#8079).
			if quantity == 0 && a.cq.RGByResource(resName) == nil {
				continue
			}
			if _, found := groupFlavors[resName]; found {
				// This resource got assigned the same flavor as its resource group.
				// No need to compute again.
				continue
			}

			flavors, status, considered := a.findFlavorForPodSets(log, psIDs, requests, resName, assignment.Usage.Quota)
			mergeFlavorAttemptsForResource(consideredFlavors, considered, resName, a.cq)
			if status.IsError() || (len(flavors) == 0 && len(requests) > 0) {
				groupFlavors = nil
				groupStatus = *status
				break
			}
			maps.Copy(groupFlavors, flavors)
			if status != nil {
				groupStatus.reasons = append(groupStatus.reasons, status.reasons...)
			}
		}

		finalConsidered := finalizeFlavorAssignmentAttempts(consideredFlavors)
		atLeastOnePodsAssignmentFailed := false
		for _, podSet := range podSets {
			podSetFlavors := utilmaps.FilterKeys(groupFlavors, slices.Collect(maps.Keys(podSet.podSet.Requests)))

			podSet.podSetAssignment.Flavors = podSetFlavors
			podSet.podSetAssignment.Status = groupStatus
			podSet.podSetAssignment.FlavorAssignmentAttempts = finalConsidered

			assignment.append(podSet.podSet.Requests, podSet.podSetAssignment)
			if podSet.podSetAssignment.Status.IsError() || (len(podSet.podSet.Requests) > 0 && len(podSet.podSetAssignment.Flavors) == 0) {
				atLeastOnePodsAssignmentFailed = true
			}
		}
		if atLeastOnePodsAssignmentFailed {
			return assignment
		}
	}
	if assignment.RepresentativeMode() == NoFit {
		return assignment
	}

	if features.Enabled(features.TopologyAwareScheduling) {
		tasRequests := assignment.WorkloadsTopologyRequests(a.wl, a.cq)
		if assignment.RepresentativeMode() == Fit {
			result := a.cq.FindTopologyAssignmentsForWorkload(tasRequests, schdcache.WithWorkload(a.wl.Obj))
			if failure := result.Failure(); failure != nil {
				// There is at least one PodSet which does not fit
				psAssignment := assignment.podSetAssignmentByName(failure.PodSetName)
				psAssignment.reason(failure.Reason)
				// update the mode for all flavors and the representative mode
				psAssignment.updateMode(Preempt)
				assignment.representativeMode = ptr.To(Preempt)
			} else {
				// All PodSets fit, we just update the TopologyAssignments
				assignment.UpdateForTASResult(result)
			}
		}
		if assignment.RepresentativeMode() == Preempt && !workload.HasUnhealthyNodes(a.wl.Obj) {
			// Don't preempt other workloads if looking for a failed node replacement
			result := a.cq.FindTopologyAssignmentsForWorkload(tasRequests, schdcache.WithSimulateEmpty(true))
			if failure := result.Failure(); failure != nil {
				// There is at least one PodSet which does not fit even if
				// all workloads are preempted.
				psAssignment := assignment.podSetAssignmentByName(failure.PodSetName)
				// update the mode for all flavors and the representative mode
				psAssignment.updateMode(NoFit)
				assignment.representativeMode = ptr.To(NoFit)
			}
		}
	}
	return assignment
}

func (a *Assignment) append(requests resources.Requests, psAssignment *PodSetAssignment) {
	flavorIdx := make(map[corev1.ResourceName]int, len(psAssignment.Flavors))
	a.PodSets = append(a.PodSets, *psAssignment)
	for resource, flvAssignment := range psAssignment.Flavors {
		if flvAssignment.borrow > a.Borrowing {
			a.Borrowing = flvAssignment.borrow
		}
		fr := resources.FlavorResource{Flavor: flvAssignment.Name, Resource: resource}

		// For workload slicing, only add the delta (new - old) to avoid double-counting
		// podSets that already have quota reserved in the old slice.
		requestAmount := requests[resource]
		if features.Enabled(features.ElasticJobsViaWorkloadSlices) && a.replaceWorkloadSlice != nil {
			oldRequest := a.findOldPodSetRequest(psAssignment.Name, resource)
			requestAmount -= oldRequest
		}

		a.Usage.Quota[fr] += requestAmount
		flavorIdx[resource] = flvAssignment.TriedFlavorIdx
	}
	a.LastState.LastTriedFlavorIdx = append(a.LastState.LastTriedFlavorIdx, flavorIdx)
}

// findOldPodSetRequest returns the resource request from the old workload slice
// for the given podSet name and resource. Returns 0 if not found.
func (a *Assignment) findOldPodSetRequest(psName kueue.PodSetReference, resource corev1.ResourceName) int64 {
	if a.replaceWorkloadSlice == nil {
		return 0
	}

	for _, oldPS := range a.replaceWorkloadSlice.TotalRequests {
		if oldPS.Name == psName {
			return oldPS.Requests[resource]
		}
	}

	return 0
}

// findFlavorForPodSets finds the flavor which can satisfy all the PodSet requests
// for all resources in the same group as resName.
// Returns the chosen flavor, along with the information about resources that need to be borrowed
// and the list of flavors that were also considered.
// If the flavor cannot be immediately assigned, it returns a status with
// reasons or failure.
func (a *FlavorAssigner) findFlavorForPodSets(
	log logr.Logger,
	psIDs []int,
	requests resources.Requests,
	resName corev1.ResourceName,
	assignmentUsage resources.FlavorResourceQuantities,
) (ResourceAssignment, *Status, FlavorAssignmentAttempts) {
	resourceGroup := a.cq.RGByResource(resName)
	if resourceGroup == nil {
		return nil, NewStatus(fmt.Sprintf("resource %s unavailable in ClusterQueue", resName)), nil
	}

	status := NewStatus()
	requests = filterRequestedResources(requests, resourceGroup.CoveredResources)

	podSets := make([]*kueue.PodSet, len(psIDs))
	selectors := make([]nodeaffinity.RequiredNodeAffinity, len(psIDs))

	for idx, psID := range psIDs {
		ps := &a.wl.Obj.Spec.PodSets[psID]
		podSets[idx] = ps

		selectors[idx] = flavorSelector(&ps.Template.Spec, resourceGroup.LabelKeys)
	}

	var bestAssignment ResourceAssignment
	bestAssignmentMode := worstGranularMode()
	consideredFlavors := newFlavorAssignmentAttempts(len(resourceGroup.Flavors))

	// We will only check against the flavors' labels for the resource.
	attemptedFlavorIdx := -1
	idx := a.wl.LastAssignment.NextFlavorToTryForPodSetResource(psIDs[0], resName)
	for ; idx < len(resourceGroup.Flavors); idx++ {
		attemptedFlavorIdx = idx
		fName := resourceGroup.Flavors[idx]

		flavorStatus := NewStatus()

		if fit, err := a.checkFlavorForPodSets(log, fName, psIDs, podSets, selectors, flavorStatus); !fit {
			if flavorStatus != nil {
				status.reasons = append(status.reasons, flavorStatus.reasons...)
			}
			consideredFlavors.AddNoFitFlavorAttempt(fName, flavorStatus)
			if err != nil {
				status.err = err
				return nil, status, consideredFlavors
			}
			continue
		}

		assignments := make(ResourceAssignment, len(requests))
		// Calculate representativeMode for this assignment as the worst mode among all requests.
		representativeMode := bestGranularMode()
		maxBorrow := 0
		var flavorQuotaReasons []string

		for rName, val := range requests {
			// Ensure the same resource flavor is used for the workload slice as in the original admitted slice.
			if features.Enabled(features.ElasticJobsViaWorkloadSlices) && a.replaceWorkloadSlice != nil {
				for _, psID := range psIDs {
					preemptWorkloadRequests := a.replaceWorkloadSlice.TotalRequests[psID]

					// Enforce consistent resource flavor assignment between slices.
					if originalFlavor := preemptWorkloadRequests.Flavors[rName]; originalFlavor != fName {
						// Flavor mismatch. Skip further checks for this resource.
						representativeMode = worstGranularMode()
						msg := fmt.Sprintf("could not assign %s flavor since the original workload is assigned: %s", fName, originalFlavor)
						status.reasons = append(status.reasons, msg)
						flavorQuotaReasons = append(flavorQuotaReasons, msg)
						break
					}

					// Subtract the resource usage of the preempted slice to request only the delta needed.
					val -= preemptWorkloadRequests.Requests[rName]
				}
			}

			resQuota := a.cq.QuotaFor(resources.FlavorResource{Flavor: fName, Resource: rName})
			// Check considering the flavor usage by previous pod sets.
			fr := resources.FlavorResource{Flavor: fName, Resource: rName}

			preemptionMode, borrow, s := a.fitsResourceQuota(log, fr, assignmentUsage[fr], val, resQuota)
			if s != nil {
				flavorQuotaReasons = append(flavorQuotaReasons, s.reasons...)
				status.reasons = append(status.reasons, s.reasons...)
			}
			maxBorrow = max(maxBorrow, borrow)
			mode := granularMode{preemptionMode, borrowingLevel(borrow)}
			if isPreferred(representativeMode, mode, a.cq.FlavorFungibility) {
				representativeMode = mode
			}
			if representativeMode.preemptionMode == noFit {
				// The flavor doesn't fit, no need to check other resources.
				break
			}

			assignments[rName] = &FlavorAssignment{
				Name:   fName,
				Mode:   preemptionMode.flavorAssignmentMode(),
				borrow: borrow,
			}
		}

		consideredFlavors.AddRepresentativeModeFlavorAttempt(fName, representativeMode.preemptionMode, maxBorrow, flavorQuotaReasons)

		if features.Enabled(features.FlavorFungibility) {
			if !shouldTryNextFlavor(representativeMode, a.cq.FlavorFungibility) {
				bestAssignment = assignments
				bestAssignmentMode = representativeMode
				break
			}
			if isPreferred(representativeMode, bestAssignmentMode, a.cq.FlavorFungibility) {
				bestAssignment = assignments
				bestAssignmentMode = representativeMode
			}
		} else if representativeMode.preemptionMode > bestAssignmentMode.preemptionMode {
			bestAssignment = assignments
			bestAssignmentMode = representativeMode
			if bestAssignmentMode.preemptionMode == fit {
				// All the resources fit in the cohort, no need to check more flavors.
				return bestAssignment, nil, consideredFlavors
			}
		}
	}

	if features.Enabled(features.FlavorFungibility) {
		for _, assignment := range bestAssignment {
			if attemptedFlavorIdx == len(resourceGroup.Flavors)-1 {
				// we have reach the last flavor, try from the first flavor next time
				assignment.TriedFlavorIdx = -1
			} else {
				assignment.TriedFlavorIdx = attemptedFlavorIdx
			}
		}
		if bestAssignmentMode.preemptionMode == fit {
			return bestAssignment, nil, consideredFlavors
		}
	}
	return bestAssignment, status, consideredFlavors
}

func (a *FlavorAssigner) checkFlavorForPodSets(
	log logr.Logger,
	flavorName kueue.ResourceFlavorReference,
	psIDs []int,
	podSets []*kueue.PodSet,
	selectors []nodeaffinity.RequiredNodeAffinity,
	status *Status,
) (bool, error) {
	flavor, exist := a.resourceFlavors[flavorName]
	if !exist {
		log.Error(nil, "Flavor not found", "Flavor", flavorName)
		status.appendf("flavor %s not found", flavorName)
		return false, nil
	}

	for psIdx, psID := range psIDs {
		if features.Enabled(features.TopologyAwareScheduling) {
			ps := &a.wl.Obj.Spec.PodSets[psID]
			if message := checkPodSetAndFlavorMatchForTAS(a.cq, ps, flavor); message != nil {
				log.Error(nil, *message)
				status.appendf("%s", *message)
				return false, nil
			}
		}
		podSpec := podSets[psIdx].Template.Spec
		taint, untolerated := corev1helpers.FindMatchingUntoleratedTaint(log, flavor.Spec.NodeTaints, append(podSpec.Tolerations, flavor.Spec.Tolerations...), func(t *corev1.Taint) bool {
			return t.Effect == corev1.TaintEffectNoSchedule || t.Effect == corev1.TaintEffectNoExecute
		}, true)
		if untolerated {
			status.appendf("untolerated taint %s in flavor %s", taint, flavorName)
			return false, nil
		}
		selector := selectors[psIdx]
		if match, err := selector.Match(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Labels: flavor.Spec.NodeLabels}}); !match || err != nil {
			if err != nil {
				status.err = err
				return false, err
			}
			status.appendf("flavor %s doesn't match node affinity", flavorName)
			return false, nil
		}
	}
	return true, nil
}

func shouldTryNextFlavor(representativeMode granularMode, flavorFungibility kueue.FlavorFungibility) bool {
	policyPreempt := flavorFungibility.WhenCanPreempt
	policyBorrow := flavorFungibility.WhenCanBorrow

	if representativeMode.preemptionMode == noFit || representativeMode.preemptionMode == noPreemptionCandidates {
		return true
	}

	if representativeMode.isPreemptMode() && policyPreempt == kueue.TryNextFlavor {
		return true
	}

	if !representativeMode.borrowingLevel.optimal() && policyBorrow == kueue.TryNextFlavor {
		return true
	}

	return false
}

func flavorSelector(spec *corev1.PodSpec, allowedKeys sets.Set[string]) nodeaffinity.RequiredNodeAffinity {
	// This function generally replicates the implementation of kube-scheduler's NodeAffinity
	// Filter plugin as of v1.24.
	var specCopy corev1.PodSpec

	// Remove affinity constraints with irrelevant keys.
	if len(spec.NodeSelector) != 0 {
		specCopy.NodeSelector = map[string]string{}
		for k, v := range spec.NodeSelector {
			if allowedKeys.Has(k) {
				specCopy.NodeSelector[k] = v
			}
		}
	}

	affinity := spec.Affinity
	if affinity != nil && affinity.NodeAffinity != nil && affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution != nil {
		var termsCopy []corev1.NodeSelectorTerm
		for _, t := range affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
			var expCopy []corev1.NodeSelectorRequirement
			for _, e := range t.MatchExpressions {
				if allowedKeys.Has(e.Key) {
					expCopy = append(expCopy, e)
				}
			}
			// If a term becomes empty, it means node affinity matches any flavor since those terms are ORed,
			// and so matching gets reduced to spec.NodeSelector
			if len(expCopy) == 0 {
				termsCopy = nil
				break
			}
			termsCopy = append(termsCopy, corev1.NodeSelectorTerm{MatchExpressions: expCopy})
		}
		if len(termsCopy) != 0 {
			specCopy.Affinity = &corev1.Affinity{
				NodeAffinity: &corev1.NodeAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
						NodeSelectorTerms: termsCopy,
					},
				},
			}
		}
	}
	return nodeaffinity.GetRequiredNodeAffinity(&corev1.Pod{Spec: specCopy})
}

// fitsResourceQuota returns how this flavor could be assigned to the resource,
// according to the remaining quota in the ClusterQueue and cohort.
// If it fits, also returns if borrowing required. Similarly, it returns information
// if borrowing is required when preempting.
// If the flavor doesn't satisfy limits immediately (when waiting or preemption
// could help), it returns a Status with reasons.
func (a *FlavorAssigner) fitsResourceQuota(log logr.Logger, fr resources.FlavorResource, assumedUsage int64, requestUsage int64, rQuota schdcache.ResourceQuota) (preemptionMode, int, *Status) {
	var status Status

	available := a.cq.Available(fr)
	maxCapacity := a.cq.PotentialAvailable(fr)
	val := assumedUsage + requestUsage

	// No Fit
	if val > maxCapacity {
		status.appendf("insufficient quota for %s in flavor %s, previously considered podsets requests (%s) + current podset request (%s) > maximum capacity (%s)",
			fr.Resource, fr.Flavor, resources.ResourceQuantityString(fr.Resource, assumedUsage), resources.ResourceQuantityString(fr.Resource, requestUsage), resources.ResourceQuantityString(fr.Resource, maxCapacity))
		return noFit, 0, &status
	}

	borrow, mayReclaimInHierarchy := classical.FindHeightOfLowestSubtreeThatFits(a.cq, fr, val)
	// Fit
	if val <= available {
		return fit, borrow, nil
	}

	// Preempt
	status.appendf("insufficient unused quota for %s in flavor %s, %s more needed",
		fr.Resource, fr.Flavor, resources.ResourceQuantityString(fr.Resource, val-available))

	if val <= rQuota.Nominal || mayReclaimInHierarchy || a.canPreemptWhileBorrowing() {
		preemptionPossiblity, borrowAfterPreemptions := a.oracle.SimulatePreemption(log, a.cq, *a.wl, fr, val)
		mode := fromPreemptionPossibility(preemptionPossiblity)
		return mode, borrowAfterPreemptions, &status
	}
	return noFit, borrow, &status
}

func (a *FlavorAssigner) canPreemptWhileBorrowing() bool {
	return (a.cq.Preemption.BorrowWithinCohort != nil && a.cq.Preemption.BorrowWithinCohort.Policy != kueue.BorrowWithinCohortPolicyNever) ||
		(a.enableFairSharing && a.cq.Preemption.ReclaimWithinCohort != kueue.PreemptionPolicyNever)
}

func filterRequestedResources(req resources.Requests, allowList sets.Set[corev1.ResourceName]) resources.Requests {
	filtered := make(resources.Requests)
	for n, v := range req {
		if allowList.Has(n) {
			filtered[n] = v
		}
	}
	return filtered
}

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

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/classical"
	preemptioncommon "sigs.k8s.io/kueue/pkg/scheduler/preemption/common"
	utilmaps "sigs.k8s.io/kueue/pkg/util/maps"
	"sigs.k8s.io/kueue/pkg/workload"
)

type Assignment struct {
	PodSets []PodSetAssignment
	// Borrowing is the height of the smallest cohort tree that fits
	// the additional Usage. It equals to 0 if no borrowing is required.
	Borrowing int
	LastState workload.AssignmentClusterQueueState

	// Usage is the accumulated Usage of resources as pod sets get
	// flavors assigned.
	Usage workload.Usage

	// representativeMode is the cached representative mode for this assignment.
	representativeMode *FlavorAssignmentMode
}

// UpdateForTASResult updates the Assignment with the TAS result
func (a *Assignment) UpdateForTASResult(result cache.TASAssignmentsResult) {
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
// scaling needed in case of partial admission.
func (a *Assignment) TotalRequestsFor(wl *workload.Info) resources.FlavorResourceQuantities {
	usage := make(resources.FlavorResourceQuantities)
	for i, ps := range wl.TotalRequests {
		// in case of partial admission scale down the quantity
		aps := a.PodSets[i]
		if aps.Count != ps.Count {
			ps = *ps.ScaledTo(aps.Count)
		}
		for res, q := range ps.Requests {
			flv := aps.Flavors[res].Name
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

	TopologyAssignment     *kueue.TopologyAssignment
	DelayedTopologyRequest *kueue.DelayedTopologyRequestState
}

// RepresentativeMode calculates the representative mode for this assignment as
// the worst assignment mode among all assigned flavors.
func (psa *PodSetAssignment) RepresentativeMode() FlavorAssignmentMode {
	if psa.Status.IsFit() {
		return Fit
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
	for res, flvAssignment := range psa.Flavors {
		flavors[res] = flvAssignment.Name
	}
	return kueue.PodSetAssignment{
		Name:                   psa.Name,
		Flavors:                flavors,
		ResourceUsage:          psa.Requests,
		Count:                  ptr.To(psa.Count),
		TopologyAssignment:     psa.TopologyAssignment.DeepCopy(),
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

// granularMode is the FlavorAssignmentMode internal to
// FlavorAssigner, which lets us distinguish priority based preemption,
// reclamation within Cohort and borrowing.
type granularMode struct {
	preemptionMode preemptionMode
	needsBorrowing bool
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

	if !features.Enabled(features.FlavorFungibilityImplicitPreferenceDefault) {
		if a.preemptionMode != b.preemptionMode {
			return a.preemptionMode > b.preemptionMode
		} else {
			return !a.needsBorrowing && b.needsBorrowing
		}
	}

	if fungibilityConfig.WhenCanBorrow == kueue.TryNextFlavor {
		if a.needsBorrowing != b.needsBorrowing {
			return !a.needsBorrowing
		}
		return a.preemptionMode > b.preemptionMode
	} else {
		if a.preemptionMode != b.preemptionMode {
			return a.preemptionMode > b.preemptionMode
		}
		return !a.needsBorrowing && b.needsBorrowing
	}
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
	SimulatePreemption(log logr.Logger, cq *cache.ClusterQueueSnapshot, wl workload.Info, fr resources.FlavorResource, quantity int64) (preemptioncommon.PreemptionPossibility, int)
}

type FlavorAssigner struct {
	wl                *workload.Info
	cq                *cache.ClusterQueueSnapshot
	resourceFlavors   map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor
	enableFairSharing bool
	oracle            preemptionOracle

	// preemptWorkloadSlice identifies the workload slice that will be mandatorily preempted
	// by this workload. It must be considered during flavor computation and included in the preemption targets.
	//
	// Note: This value may be nil in the following cases:
	//   - Workload slicing is not enabled (either globally or for this specific workload).
	//   - The current workload does not represent a scale-up slice.
	// In these scenarios, flavor assignment proceeds as in the original flow—i.e., as for regular,
	// non-sliced workloads.
	preemptWorkloadSlice *workload.Info
}

func New(wl *workload.Info, cq *cache.ClusterQueueSnapshot, resourceFlavors map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor, enableFairSharing bool, oracle preemptionOracle, preemptWorkloadSlice *workload.Info) *FlavorAssigner {
	return &FlavorAssigner{
		wl:                   wl,
		cq:                   cq,
		resourceFlavors:      resourceFlavors,
		enableFairSharing:    enableFairSharing,
		oracle:               oracle,
		preemptWorkloadSlice: preemptWorkloadSlice,
	}
}

func lastAssignmentOutdated(wl *workload.Info, cq *cache.ClusterQueueSnapshot) bool {
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
	}

	groupedRequests := newPodSetGroups()

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
				psAssignment.TopologyAssignment = a.wl.Obj.Status.Admission.PodSetAssignments[i].TopologyAssignment
			}
		}

		groupKey := strconv.Itoa(i)
		if tr := a.wl.Obj.Spec.PodSets[i].TopologyRequest; tr != nil && tr.PodSetGroupName != nil {
			groupKey = *tr.PodSetGroupName
		}

		groupedRequests.insert(groupKey, indexedPodSet{originalIndex: i, podSet: &podSet, podSetAssignment: &psAssignment})
	}

	for _, podSets := range groupedRequests.orderedPodSetGroups() {
		requests := make(resources.Requests)
		psIDs := make([]int, len(podSets))
		for idx, podset := range podSets {
			psIDs[idx] = podset.originalIndex
			requests.Add(podset.podSet.Requests)
		}

		groupFlavors := make(ResourceAssignment)
		for _, ips := range podSets {
			for resName := range ips.podSetAssignment.Flavors {
				groupFlavors[resName] = ips.podSetAssignment.Flavors[resName]
				break
			}
		}
		var groupStatus Status
		for resName := range requests {
			if _, found := groupFlavors[resName]; found {
				// This resource got assigned the same flavor as its resource group.
				// No need to compute again.
				continue
			}
			flavors, status := a.findFlavorForPodSetResource(log, psIDs, requests, resName, assignment.Usage.Quota)
			if status.IsError() || len(flavors) == 0 {
				groupFlavors = nil
				groupStatus = *status
				break
			}
			maps.Copy(groupFlavors, flavors)
			if status != nil {
				groupStatus.reasons = append(groupStatus.reasons, status.reasons...)
			}
		}
		atLeastOnePodsAssignmentFailed := false
		for _, podSet := range podSets {
			podSetFlavors := utilmaps.FilterKeys(groupFlavors, slices.Collect(maps.Keys(podSet.podSet.Requests)))

			podSet.podSetAssignment.Flavors = podSetFlavors
			podSet.podSetAssignment.Status = groupStatus

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
			result := a.cq.FindTopologyAssignmentsForWorkload(tasRequests, cache.WithWorkload(a.wl.Obj))
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
		if assignment.RepresentativeMode() == Preempt && !workload.HasNodeToReplace(a.wl.Obj) {
			// Don't preempt other workloads if looking for a failed node replacement
			result := a.cq.FindTopologyAssignmentsForWorkload(tasRequests, cache.WithSimulateEmpty(true))
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
		a.Usage.Quota[fr] += requests[resource]
		flavorIdx[resource] = flvAssignment.TriedFlavorIdx
	}
	a.LastState.LastTriedFlavorIdx = append(a.LastState.LastTriedFlavorIdx, flavorIdx)
}

// findFlavorForPodSetResource finds the flavor which can satisfy the podSet request
// for all resources in the same group as resName.
// Returns the chosen flavor, along with the information about resources that need to be borrowed.
// If the flavor cannot be immediately assigned, it returns a status with
// reasons or failure.
func (a *FlavorAssigner) findFlavorForPodSetResource(
	log logr.Logger,
	psIDs []int,
	requests resources.Requests,
	resName corev1.ResourceName,
	assignmentUsage resources.FlavorResourceQuantities,
) (ResourceAssignment, *Status) {
	resourceGroup := a.cq.RGByResource(resName)
	if resourceGroup == nil {
		return nil, NewStatus(fmt.Sprintf("resource %s unavailable in ClusterQueue", resName))
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
	bestAssignmentMode := granularMode{preemptionMode: noFit, needsBorrowing: true}

	// We will only check against the flavors' labels for the resource.
	attemptedFlavorIdx := -1
	idx := a.wl.LastAssignment.NextFlavorToTryForPodSetResource(psIDs[0], resName)
	for ; idx < len(resourceGroup.Flavors); idx++ {
		attemptedFlavorIdx = idx
		fName := resourceGroup.Flavors[idx]
		flavor, exist := a.resourceFlavors[fName]
		if !exist {
			log.Error(nil, "Flavor not found", "Flavor", fName)
			status.appendf("flavor %s not found", fName)
			continue
		}

		var flavorUnmatchMessage *string
		for psIdx, psID := range psIDs {
			if features.Enabled(features.TopologyAwareScheduling) {
				ps := &a.wl.Obj.Spec.PodSets[psID]
				flavorUnmatchMessage = checkPodSetAndFlavorMatchForTAS(a.cq, ps, flavor)
				if flavorUnmatchMessage != nil {
					log.Error(nil, *flavorUnmatchMessage)
					break
				}
			}
			podSpec := podSets[psIdx].Template.Spec
			taint, untolerated := corev1helpers.FindMatchingUntoleratedTaint(flavor.Spec.NodeTaints, append(podSpec.Tolerations, flavor.Spec.Tolerations...), func(t *corev1.Taint) bool {
				return t.Effect == corev1.TaintEffectNoSchedule || t.Effect == corev1.TaintEffectNoExecute
			})
			if untolerated {
				flavorUnmatchMessage = ptr.To(fmt.Sprintf("untolerated taint %s in flavor %s", taint, fName))
				break
			}
			selector := selectors[psIdx]
			if match, err := selector.Match(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Labels: flavor.Spec.NodeLabels}}); !match || err != nil {
				if err != nil {
					status.err = err
					return nil, status
				}
				flavorUnmatchMessage = ptr.To(fmt.Sprintf("flavor %s doesn't match node affinity", fName))
				break
			}
		}
		if flavorUnmatchMessage != nil {
			status.reasons = append(status.reasons, *flavorUnmatchMessage)
			continue
		}
		assignments := make(ResourceAssignment, len(requests))
		// Calculate representativeMode for this assignment as the worst mode among all requests.
		representativeMode := granularMode{preemptionMode: fit, needsBorrowing: false}
		for rName, val := range requests {
			// Ensure the same resource flavor is used for the workload slice as in the original admitted slice.
			if features.Enabled(features.ElasticJobsViaWorkloadSlices) && a.preemptWorkloadSlice != nil {
				for _, psID := range psIDs {
					preemptWorkloadRequests := a.preemptWorkloadSlice.TotalRequests[psID]

					// Enforce consistent resource flavor assignment between slices.
					if originalFlavor := preemptWorkloadRequests.Flavors[rName]; originalFlavor != fName {
						// Flavor mismatch. Skip further checks for this resource.
						representativeMode = granularMode{preemptionMode: noFit, needsBorrowing: true}
						status.reasons = append(status.reasons, fmt.Sprintf("could not assign %s flavor since the original workload is assigned: %s", fName, originalFlavor))
						break
					}

					// Subtract the resource usage of the preempted slice to request only the delta needed.
					val -= preemptWorkloadRequests.Requests[rName]
				}
			}

			resQuota := a.cq.QuotaFor(resources.FlavorResource{Flavor: fName, Resource: rName})
			// Check considering the flavor usage by previous pod sets.
			fr := resources.FlavorResource{Flavor: fName, Resource: rName}
			preemptionMode, borrow, s := a.fitsResourceQuota(log, fr, val+assignmentUsage[fr], resQuota)
			if s != nil {
				status.reasons = append(status.reasons, s.reasons...)
			}
			mode := granularMode{preemptionMode, borrow > 0}
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
				return bestAssignment, nil
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
			return bestAssignment, nil
		}
	}
	return bestAssignment, status
}

func shouldTryNextFlavor(representativeMode granularMode, flavorFungibility kueue.FlavorFungibility) bool {
	policyPreempt := flavorFungibility.WhenCanPreempt
	policyBorrow := flavorFungibility.WhenCanBorrow
	if representativeMode.isPreemptMode() && policyPreempt == kueue.Preempt {
		if !representativeMode.needsBorrowing || policyBorrow == kueue.Borrow {
			return false
		}
	}

	if representativeMode.preemptionMode == fit && representativeMode.needsBorrowing && policyBorrow == kueue.Borrow {
		return false
	}

	if representativeMode.preemptionMode == fit && !representativeMode.needsBorrowing {
		return false
	}

	return true
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
func (a *FlavorAssigner) fitsResourceQuota(log logr.Logger, fr resources.FlavorResource, val int64, rQuota cache.ResourceQuota) (preemptionMode, int, *Status) {
	var status Status

	available := a.cq.Available(fr)
	maxCapacity := a.cq.PotentialAvailable(fr)

	// No Fit
	if val > maxCapacity {
		status.appendf("insufficient quota for %s in flavor %s, request > maximum capacity (%s > %s)",
			fr.Resource, fr.Flavor, resources.ResourceQuantityString(fr.Resource, val), resources.ResourceQuantityString(fr.Resource, maxCapacity))
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

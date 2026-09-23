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
	"context"
	"fmt"
	"maps"
	"math"
	"slices"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	corev1helpers "k8s.io/component-helpers/scheduling/corev1"
	"k8s.io/component-helpers/scheduling/corev1/nodeaffinity"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/classical"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/policy"
	utilmaps "sigs.k8s.io/kueue/pkg/util/maps"
	"sigs.k8s.io/kueue/pkg/util/orderedgroups"
	"sigs.k8s.io/kueue/pkg/util/podset"
	"sigs.k8s.io/kueue/pkg/util/resourcegroups"
	"sigs.k8s.io/kueue/pkg/util/tas"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workload/concurrentadmission"
)

func IgnoreUndeclaredResources(quotaCheckStrategy configapi.QuotaCheckStrategy) bool {
	return features.Enabled(features.QuotaCheckStrategy) && quotaCheckStrategy == configapi.QuotaCheckIgnoreUndeclared
}

func mostSevereReason(a, b string) string {
	if reasonSeverity(a) >= reasonSeverity(b) {
		return a
	}
	return b
}

const (
	reasonSeverityNone int = iota
	reasonSeverityTopologyPlacementFailed
	reasonSeverityWaitingForQuota
	reasonSeverityExceedsMaxQuota
	reasonSeverityNoMatchingFlavor
)

func reasonSeverity(reason string) int {
	switch reason {
	case kueue.WorkloadQuotaReservedReasonNoMatchingFlavor:
		return reasonSeverityNoMatchingFlavor
	case kueue.WorkloadQuotaReservedReasonExceedsMaxQuota:
		return reasonSeverityExceedsMaxQuota
	case kueue.WorkloadQuotaReservedReasonWaitingForQuota:
		return reasonSeverityWaitingForQuota
	case kueue.WorkloadQuotaReservedReasonTopologyPlacementFailed:
		return reasonSeverityTopologyPlacementFailed
	default:
		return reasonSeverityNone
	}
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

	// A flavor without preemption candidates cannot be admitted in this
	// scheduling attempt. Rank it below viable modes before applying the
	// configured fungibility preference, while retaining noFit as the worst mode.
	aHasNoCandidates := a.preemptionMode == noPreemptionCandidates
	bHasNoCandidates := b.preemptionMode == noPreemptionCandidates
	if aHasNoCandidates != bHasNoCandidates {
		return !aHasNoCandidates
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
			return borrowingOverPreemption()
		case kueue.PreemptionOverBorrowing:
			return preemptionOverBorrowing()
		}
	}

	return borrowingOverPreemption()
}

func fromPreemptionPossibility(preemptionPossibility policy.PreemptionPossibility) preemptionMode {
	switch preemptionPossibility {
	case policy.NoCandidates:
		return noPreemptionCandidates
	case policy.Preempt:
		return preempt
	case policy.Reclaim:
		return reclaim
	}
	panic(fmt.Sprintf("illegal PreemptionPossibility: %d", preemptionPossibility))
}

func (mode preemptionMode) preemptionPossibility() *policy.PreemptionPossibility {
	switch mode {
	case noPreemptionCandidates:
		return new(policy.NoCandidates)
	case preempt:
		return new(policy.Preempt)
	case reclaim:
		return new(policy.Reclaim)
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

type preemptionOracle interface {
	SimulatePreemption(
		ctx context.Context,
		cq *schdcache.ClusterQueueSnapshot,
		wl workload.Info,
		fr resources.FlavorResource,
		quantity resources.Amount,
	) (policy.PreemptionPossibility, int)
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
	quotaCheckStrategy   configapi.QuotaCheckStrategy
	resourceFormatter    *resources.ResourceFormatter

	// schedulingCycle is the cycle this assignment is being computed in. It is recorded
	// on the assignment so that a later cycle can tell how old the assignment is.
	schedulingCycle int64
}

func New(
	wl *workload.Info,
	cq *schdcache.ClusterQueueSnapshot,
	resourceFlavors map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor,
	enableFairSharing bool,
	oracle preemptionOracle,
	preemptWorkloadSlice *workload.Info,
	quotaCheckStrategy configapi.QuotaCheckStrategy,
	resourceFormatter *resources.ResourceFormatter,
	schedulingCycle int64,
) *FlavorAssigner {
	return &FlavorAssigner{
		wl:                   wl,
		cq:                   cq,
		resourceFlavors:      resourceFlavors,
		enableFairSharing:    enableFairSharing,
		oracle:               oracle,
		replaceWorkloadSlice: preemptWorkloadSlice,
		quotaCheckStrategy:   quotaCheckStrategy,
		resourceFormatter:    resourceFormatter,
		schedulingCycle:      schedulingCycle,
	}
}

type indexedPodSet struct {
	originalIndex    int
	podSet           *workload.PodSetResources
	podSetAssignment *PodSetAssignment
}

func (a *FlavorAssigner) AssignFlavors(
	ctx context.Context,
	log logr.Logger,
	counts []int32,
) Assignment {
	// Second-pass requests follow admission order, while counts and flavor scan state
	// follow spec order. Use the request's spec index for the lookups below.
	specIndexes := podset.SpecIndexes(a.wl.Obj.Spec.PodSets, a.wl.TotalRequests,
		func(ps *workload.PodSetResources) kueue.PodSetReference { return ps.Name })
	for i := range specIndexes {
		if specIndexes[i] == -1 {
			specIndexes[i] = i
		}
	}
	requests := make([]workload.PodSetResources, len(a.wl.TotalRequests))
	if len(counts) == 0 {
		for i, ps := range a.wl.TotalRequests {
			requests[i] = ps
			if ps.Requests != nil {
				requests[i].Requests = ps.Requests.Clone()
			}
		}
	} else {
		for i := range a.wl.TotalRequests {
			requests[i] = *a.wl.TotalRequests[i].ScaledTo(counts[specIndexes[i]])
		}
	}
	assignment := Assignment{
		PodSets:            make([]PodSetAssignment, 0, len(requests)),
		quotaCheckStrategy: a.quotaCheckStrategy,
		Usage: workload.Usage{
			Quota: workload.ResourceUsage{
				Assigned:   make(resources.FlavorResourceQuantities),
				Unassigned: make(resources.MapRequests),
			},
		},
		FlavorScanState: workload.FlavorScanState{
			LastTriedFlavorIndexes:        make([]map[corev1.ResourceName]int, 0, len(requests)),
			AllocatableResourceGeneration: a.cq.AllocatableResourceGeneration,
			SchedulingCycle:               a.schedulingCycle,
			SchedulingHash:                a.wl.SchedulingHash,
		},
		replaceWorkloadSlice: a.replaceWorkloadSlice,
	}

	groupedRequests := orderedgroups.NewOrderedGroups[tas.PodSetGroupKey, indexedPodSet]()

	for i, podSet := range requests {
		if a.cq.RGByResource(corev1.ResourcePods) != nil {
			if podSet.Requests != nil {
				podSet.Requests.Set(corev1.ResourcePods, resources.NewAmount(int64(podSet.Count)))
			} else {
				podSet.Requests = resources.NewRequestsFromMap(map[corev1.ResourceName]int64{corev1.ResourcePods: int64(podSet.Count)})
			}
		}

		flavorsLen := 0
		var resList corev1.ResourceList
		if podSet.Requests != nil {
			flavorsLen = podSet.Requests.Len()
			resList = podSet.Requests.ToResourceList(a.resourceFormatter)
		}

		psAssignment := PodSetAssignment{
			Name:     podSet.Name,
			Flavors:  make(ResourceAssignment, flavorsLen),
			Requests: resList,
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
				psAssignment.DelayedTopologyRequest = new(*podSet.DelayedTopologyRequest)
			}
			if podSet.TopologyRequest != nil {
				psAssignment.TopologyAssignment = tas.InternalFrom(a.wl.Obj.Status.Admission.PodSetAssignments[i].TopologyAssignment)
			}
		}

		psIdx := specIndexes[i]
		groupKey := tas.GroupKeyForPodSet(&a.wl.Obj.Spec.PodSets[psIdx])

		groupedRequests.Insert(groupKey, indexedPodSet{originalIndex: psIdx, podSet: &podSet, podSetAssignment: &psAssignment})
	}

	// The probe needs the earlier PodSets' full requests. Quota usage may only
	// contain replacement deltas, including negative values for shrinking PodSets.
	assignedRequests := make(resources.FlavorResourceQuantities)
	for _, podSets := range groupedRequests.InOrder {
		requests := resources.NewRequests()
		psIDs := make([]int, len(podSets))
		for idx, podset := range podSets {
			psIDs[idx] = podset.originalIndex
			requests.Add(podset.podSet.Requests)
		}
		probeRequests := a.probeRequestsFor(podSets)

		consideredFlavors := make(map[kueue.ResourceFlavorReference]FlavorAssignmentAttempt)

		groupFlavors := make(ResourceAssignment)
		for _, ips := range podSets {
			// Seed the dedup memo with every prior-pass assignment, not just one.
			maps.Copy(groupFlavors, ips.podSetAssignment.Flavors)
		}
		var groupStatus Status
		for resName, quantity := range requests.Iter() {
			// Skip zero-quantity requests for resources not defined in the ClusterQueue (#8079) or
			// If quotaCheckStrategy is IgnoreUndeclared, skip resources not declared in the ClusterQueue.
			if a.cq.RGByResource(resName) == nil {
				if quantity.Sign() == 0 {
					continue
				}
				if IgnoreUndeclaredResources(a.quotaCheckStrategy) {
					log.V(3).Info("Skipping resource not declared in the ClusterQueue", "res", resName)
					continue
				}
			}

			if _, found := groupFlavors[resName]; found {
				// This resource got assigned the same flavor as its resource group.
				// No need to compute again.
				continue
			}

			flavors, status, considered := a.findFlavorForPodSets(ctx, log, psIDs, requests, probeRequests, resName, assignment.Usage.Quota.Assigned, assignedRequests)
			if probeRequests != nil && len(flavors) == 0 && !status.IsError() {
				// The probe is a preference, not an admission barrier for zero-count PodSets.
				probeReason := status.Message()
				flavors, status, considered = a.findFlavorForPodSets(ctx, log, psIDs, requests, nil, resName, assignment.Usage.Quota.Assigned, assignedRequests)
				if len(flavors) > 0 && !status.IsError() {
					if assignment.ZeroCountFlavorFallback != "" {
						assignment.ZeroCountFlavorFallback += " "
					}
					assignment.ZeroCountFlavorFallback += a.zeroCountFallbackMessage(podSets, flavors, resName, probeReason)
				}
			}
			mergeFlavorAttemptsForResource(consideredFlavors, considered, resName, a.cq)
			if status.IsError() || (len(flavors) == 0 && requests.Len() > 0) {
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
			podSet.podSetAssignment.Flavors = a.resolvePodSetFlavors(log, podSet, groupFlavors)
			podSet.podSetAssignment.Status = groupStatus
			podSet.podSetAssignment.FlavorAssignmentAttempts = finalConsidered

			assignment.append(podSet.originalIndex, podSet.podSet.Requests, podSet.podSetAssignment)
			if podSet.podSet.Requests != nil {
				for resName, flavor := range podSet.podSetAssignment.Flavors {
					fr := resources.FlavorResource{Flavor: flavor.Name, Resource: resName}
					assignedRequests[fr] = assignedRequests[fr].Add(podSet.podSet.Requests.ResourceValue(resName))
				}
			}
			if podSet.podSetAssignment.Status.IsError() || (podSet.podSet.Requests != nil && podSet.podSet.Requests.Len() > 0 && len(podSet.podSetAssignment.Flavors) == 0) {
				atLeastOnePodsAssignmentFailed = true
			}
		}
		if atLeastOnePodsAssignmentFailed {
			if features.Enabled(features.UnadmittedWorkloadsObservability) {
				assignment.ResolveNoFitReason(a.cq)
			}
			return assignment
		}
	}
	if assignment.RepresentativeMode() == NoFit {
		if features.Enabled(features.UnadmittedWorkloadsObservability) {
			assignment.ResolveNoFitReason(a.cq)
		}
		return assignment
	}
	return assignment
}

// AssignTopology updates the assignment based on topology requirements.
func (a *FlavorAssigner) AssignTopology(ctx context.Context, log logr.Logger, assignment *Assignment) {
	if !features.Enabled(features.TopologyAwareScheduling) {
		return
	}
	if features.Enabled(features.ElasticJobsViaWorkloadSlicesWithTAS) && a.replaceWorkloadSlice != nil {
		// Elastic placement accounts for the previous assignment itself.
		// Remove its cached usage during the search to avoid counting it twice.
		restore := a.cq.SimulateUsageRemoval(workload.Usage{TAS: a.replaceWorkloadSlice.TASUsage()})
		defer restore()
	}
	tasRequests := assignment.WorkloadsTopologyRequests(log, a.wl, a.cq)
	if assignment.RepresentativeMode() == Fit {
		result := a.cq.FindTopologyAssignmentsForWorkload(ctx, tasRequests, schdcache.WithWorkloadInfo(a.wl))
		if failure := result.Failure(); failure != nil {
			// There is at least one PodSet which does not fit
			psAssignment := assignment.podSetAssignmentByName(failure.PodSetName)
			psAssignment.reason(failure.Reason)
			// update the mode for all flavors and the representative mode
			assignment.updateMode(failure.PodSetName, Preempt)
		} else {
			// All PodSets fit, we just update the TopologyAssignments
			assignment.UpdateForTASResult(log, a.cq, a.wl, result)
		}
	}
	if assignment.RepresentativeMode() == Preempt && !workload.HasUnhealthyNodes(a.wl.Obj) {
		// Don't preempt other workloads if looking for a failed node replacement
		result := a.cq.FindTopologyAssignmentsForWorkload(
			ctx,
			tasRequests,
			schdcache.WithSimulateEmpty(true),
			schdcache.WithWorkloadInfo(a.wl),
		)
		if failure := result.Failure(); failure != nil {
			// There is at least one PodSet which does not fit even if
			// all workloads are preempted.
			psAssignment := assignment.podSetAssignmentByName(failure.PodSetName)
			if features.Enabled(features.UnadmittedWorkloadsObservability) {
				psAssignment.markFlavorAttempt(failure.Flavor, NoFit, kueue.WorkloadQuotaReservedReasonTopologyPlacementFailed)
			}
			// update the mode for all flavors and the representative mode
			assignment.updateMode(failure.PodSetName, NoFit)
		} else {
			// Update TAS-related assignments to Preempt because preemptions might be needed
			// in resources in which total unused quota is sufficient (Fit), but the
			// quota is fragmented.
			assignment.updateModeForTASRequests(tasRequests, Preempt)
		}
	}
}

// resolvePodSetFlavors returns the flavors podSet should be assigned, given the flavors
// already resolved for its whole PodSet group (groupFlavors). Normally this is just
// groupFlavors filtered down to the resources podSet itself requests. A PodSet requesting
// none of the group's managed resources (e.g. an LWS leader) would otherwise end up with no
// flavor and be rejected from TAS, so such a PodSet instead falls back to the group's TAS
// flavor(s) if it belongs to a topology group. A ClusterQueue-wide fallback can be revisited
// later if users request it.
func (a *FlavorAssigner) resolvePodSetFlavors(log logr.Logger, idxPodSet indexedPodSet, groupFlavors ResourceAssignment) ResourceAssignment {
	// For PodSets with requests, keep only flavors for resources this PodSet requests.
	if idxPodSet.podSet.Requests != nil && idxPodSet.podSet.Requests.Len() != 0 {
		var reqKeys []corev1.ResourceName
		idxPodSet.podSet.Requests.ForEach(func(name corev1.ResourceName, _ resources.Amount) {
			reqKeys = append(reqKeys, name)
		})
		podSetFlavors := utilmaps.FilterKeys(groupFlavors, reqKeys)
		log.V(5).Info("Resolved PodSet flavors from group flavors",
			"podSet", idxPodSet.podSet.Name,
			"requestedResources", idxPodSet.podSet.Requests.Len(),
			"resolvedFlavors", len(podSetFlavors))
		return podSetFlavors
	}

	// For PodSets without requests, reuse TAS flavors from the topology group when available.
	if groupName := podSetGroupName(&a.wl.Obj.Spec.PodSets[idxPodSet.originalIndex]); groupName != nil {
		// A PodSet with no resource requests in a topology group (e.g. an LWS leader) still needs a
		// resolved TAS flavor so it can be placed; keep the group's TAS flavor(s) instead
		// of filtering the group's resolution down to nothing.
		podSetFlavors := tasFlavorsOnly(groupFlavors, a.cq.TASFlavors)
		if len(podSetFlavors) > 0 {
			log.V(5).Info("Using TAS flavors from topology group for PodSet with no resource requests", "podSet", idxPodSet.podSet.Name, "flavors", podSetFlavors)
			return podSetFlavors
		}
	}

	return nil
}

// tasFlavorsOnly returns the subset of resourceAssignment whose flavor is a TAS flavor.
func tasFlavorsOnly(resourceAssignment ResourceAssignment, tasFlavors map[kueue.ResourceFlavorReference]*schdcache.TASFlavorSnapshot) ResourceAssignment {
	result := make(ResourceAssignment, len(resourceAssignment))
	for resName, flavorAssignment := range resourceAssignment {
		if _, isTAS := tasFlavors[flavorAssignment.Name]; isTAS {
			result[resName] = flavorAssignment
		}
	}
	return result
}

func findRGIndicesByFlavor(cq *schdcache.ClusterQueueSnapshot, flavor kueue.ResourceFlavorReference) []int {
	var indices []int
	for i, rg := range cq.ResourceGroups {
		if slices.Contains(rg.Flavors, flavor) {
			indices = append(indices, i)
		}
	}
	return indices
}

func podSetResourcesByName(podSets []workload.PodSetResources, name kueue.PodSetReference) *workload.PodSetResources {
	if idx := slices.IndexFunc(podSets, func(ps workload.PodSetResources) bool { return ps.Name == name }); idx != -1 {
		return &podSets[idx]
	}
	return nil
}

// probeRequestsFor checks one pod per PodSet only when the entire group is empty.
// Otherwise, actual requests drive flavor selection: zero-count PodSets may
// represent completed, reclaimed pods that will not run again.
func (a *FlavorAssigner) probeRequestsFor(podSets []indexedPodSet) resources.Requests {
	if slices.ContainsFunc(podSets, func(ps indexedPodSet) bool { return ps.podSet.Count != 0 }) {
		return nil
	}
	probeRequests := resources.NewRequests()
	for _, podSet := range podSets {
		requests := podSet.podSet.PerPodRequests
		if requests != nil {
			probeRequests.Add(requests)
		}
	}
	if a.cq.RGByResource(corev1.ResourcePods) != nil {
		probeRequests.Set(corev1.ResourcePods, resources.NewAmount(int64(len(podSets))))
	}
	return probeRequests
}

func (a *FlavorAssigner) zeroCountFallbackMessage(podSets []indexedPodSet, flavors ResourceAssignment, resName corev1.ResourceName, probeReason string) string {
	podSetNames := make([]kueue.PodSetReference, 0, len(podSets))
	for _, ps := range podSets {
		if ps.podSet.Count == 0 {
			podSetNames = append(podSetNames, ps.podSet.Name)
		}
	}
	return fmt.Sprintf("Assigned flavor %s to zero-count PodSets %v for resources %v in ClusterQueue %s. "+
		"No considered flavor could satisfy one pod per PodSet: %s. "+
		"Review capacity and flavor constraints before scaling up.",
		flavors[resName].Name, podSetNames, slices.Sorted(maps.Keys(flavors)), a.cq.Name, probeReason)
}

// findFlavorForPodSets finds the flavor which can satisfy all the PodSet requests
// for all resources in the same group as resName.
// Returns the chosen flavor, along with the information about resources that need to be borrowed
// and the list of flavors that were also considered.
// If the flavor cannot be immediately assigned, it returns a status with
// reasons or failure.
func (a *FlavorAssigner) findFlavorForPodSets(
	ctx context.Context,
	log logr.Logger,
	psIDs []int,
	requests resources.Requests,
	probeRequests resources.Requests,
	resName corev1.ResourceName,
	assignmentUsage resources.FlavorResourceQuantities,
	assignedRequests resources.FlavorResourceQuantities,
) (ResourceAssignment, *Status, FlavorAssignmentAttempts) {
	resourceGroup := a.cq.RGByResource(resName)
	if resourceGroup == nil {
		status := NewStatus(fmt.Sprintf("resource %s unavailable in ClusterQueue", resName))
		status.noFitReason = kueue.WorkloadQuotaReservedReasonNoMatchingFlavor
		return nil, status, nil
	}

	status := NewStatus()
	requests = filterRequestedResources(requests, resourceGroup.CoveredResources)
	if probeRequests != nil {
		probeRequests = filterRequestedResources(probeRequests, resourceGroup.CoveredResources)
	}

	podSets := make([]*kueue.PodSet, len(psIDs))
	for idx, psID := range psIDs {
		podSets[idx] = &a.wl.Obj.Spec.PodSets[psID]
	}

	var bestAssignment ResourceAssignment
	bestAssignmentMode := worstGranularMode()
	consideredFlavors := newFlavorAssignmentAttempts(len(resourceGroup.Flavors))

	// We will only check against the flavors' labels for the resource.
	attemptedFlavorIdx := -1
	idx := a.wl.FlavorScanState.NextFlavorToTryForPodSetResource(psIDs[0], resName)
	for ; idx < len(resourceGroup.Flavors); idx++ {
		attemptedFlavorIdx = idx
		fName := resourceGroup.Flavors[idx]
		if a.shouldRespectNominationMapping() && a.shouldSkipBasedOnNominationMapping(log, fName, psIDs, resName) {
			status.appendf("skipping flavor %s as it is not found in the nomination mapping for resource %s", fName, resName)
			continue
		}
		if features.Enabled(features.ConcurrentAdmission) && !concurrentadmission.IsFlavorAllowedForVariant(a.wl.Obj, fName) {
			status.appendf("skipping flavor %s due to WorkloadAllowedResourceFlavorAnnotation annotation", fName)
			continue
		}

		if flavorStatus := a.checkFlavorForPodSets(log, fName, psIDs, podSets, resourceGroup); !flavorStatus.IsFit() {
			flavorStatus.noFitReason = kueue.WorkloadQuotaReservedReasonNoMatchingFlavor
			status.reasons = append(status.reasons, flavorStatus.reasons...)
			consideredFlavors.AddNoFitFlavorAttempt(fName, flavorStatus)
			if flavorStatus.err != nil {
				status.err = flavorStatus.err
				return nil, status, consideredFlavors
			}
			continue
		}

		if probeRequests != nil {
			probeStatus := NewStatus()
			probeRequests.ForEach(func(rName corev1.ResourceName, val resources.Amount) {
				fr := resources.FlavorResource{Flavor: fName, Resource: rName}
				if s := a.fitsMaxCapacity(fr, assignedRequests[fr], val); s != nil {
					probeStatus.reasons = append(probeStatus.reasons, s.reasons...)
					probeStatus.noFitReason = s.noFitReason
				}
			})
			if !probeStatus.IsFit() {
				status.reasons = append(status.reasons, probeStatus.reasons...)
				consideredFlavors.AddNoFitFlavorAttempt(fName, probeStatus)
				continue
			}
		}

		assignments := make(ResourceAssignment, requests.Len())
		// Calculate representativeMode for this assignment as the worst mode among all requests.
		representativeMode := bestGranularMode()
		maxBorrow := 0
		var flavorQuotaReasons []string
		var flavorNoFitReason string

		requests.ForEach(func(rName corev1.ResourceName, val resources.Amount) {
			// Ensure the same resource flavor is used for the workload slice as in the original admitted slice.
			if features.Enabled(features.ElasticJobsViaWorkloadSlices) && a.replaceWorkloadSlice != nil {
				for _, psID := range psIDs {
					// The replaced slice's requests come from its admission, which is in group order.
					preemptWorkloadRequests := podSetResourcesByName(a.replaceWorkloadSlice.TotalRequests, a.wl.Obj.Spec.PodSets[psID].Name)
					if preemptWorkloadRequests == nil {
						log.V(1).Info("PodSet not found in the replaced workload slice", "podSet", a.wl.Obj.Spec.PodSets[psID].Name)
						continue
					}

					// Enforce consistent resource flavor assignment between slices, but,
					// when the feature gate is enabled, only while the replaced slice still
					// has pods in this PodSet. A PodSet scaled to zero has nothing running
					// on the old flavor, so the new slice may pick any flavor (E.g. fall
					// through to a flex flavor when the reserved one is full) without
					// splitting one PodSet across flavors. The old slice's requests are
					// zero in that case, so the usage delta below stays correct.
					if features.Enabled(features.ElasticJobsViaWorkloadSlicesFlavorChangeFromZero) && preemptWorkloadRequests.Count == 0 {
						continue
					}
					if originalFlavor := preemptWorkloadRequests.Flavors[rName]; originalFlavor != fName {
						// Flavor mismatch. Skip further checks for this resource.
						representativeMode = worstGranularMode()
						msg := fmt.Sprintf("could not assign %s flavor since the original workload is assigned: %s", fName, originalFlavor)
						status.reasons = append(status.reasons, msg)
						flavorQuotaReasons = append(flavorQuotaReasons, msg)
						flavorNoFitReason = mostSevereReason(flavorNoFitReason, kueue.WorkloadQuotaReservedReasonNoMatchingFlavor)
						break
					}

					// Subtract the resource usage of the preempted slice to request only the delta needed.
					if preemptWorkloadRequests.Requests != nil {
						val = val.Sub(preemptWorkloadRequests.Requests.ResourceValue(rName))
					}
				}
			}

			resQuota := a.cq.QuotaFor(resources.FlavorResource{Flavor: fName, Resource: rName})
			// Check considering the flavor usage by previous pod sets.
			fr := resources.FlavorResource{Flavor: fName, Resource: rName}

			preemptionMode, borrow, s := a.fitsResourceQuota(ctx, fr, assignmentUsage[fr], val, resQuota)
			if s != nil {
				flavorQuotaReasons = append(flavorQuotaReasons, s.reasons...)
				status.reasons = append(status.reasons, s.reasons...)
				flavorNoFitReason = mostSevereReason(flavorNoFitReason, s.noFitReason)
			}
			maxBorrow = max(maxBorrow, borrow)
			mode := granularMode{preemptionMode, borrowingLevel(borrow)}
			if isPreferred(representativeMode, mode, a.cq.FlavorFungibility) {
				representativeMode = mode
			}
			if representativeMode.preemptionMode == noFit {
				// The flavor doesn't fit, no need to check other resources.
				return
			}

			assignments[rName] = &FlavorAssignment{
				Name:   fName,
				Mode:   preemptionMode.flavorAssignmentMode(),
				borrow: borrow,
			}
		})

		consideredFlavors.AddRepresentativeModeFlavorAttempt(fName, representativeMode.preemptionMode, maxBorrow, flavorQuotaReasons, flavorNoFitReason)

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
	rg *resourcegroups.ResourceGroup,
) *Status {
	status := NewStatus()

	flavor, exist := a.resourceFlavors[flavorName]
	if !exist {
		log.Error(nil, "Flavor not found", "Flavor", flavorName)
		status.appendf("flavor %s not found", flavorName)
		return status
	}

	// Use only this flavor's own label keys (not the union across all flavors in
	// the resource group) so that affinity terms referencing keys from other
	// flavors are correctly ignored when evaluating this flavor.
	flavorLabelKeys := sets.KeySet(flavor.Spec.NodeLabels)

	for psIdx, psID := range psIDs {
		if features.Enabled(features.TopologyAwareScheduling) {
			ps := &a.wl.Obj.Spec.PodSets[psID]
			if message := checkPodSetAndFlavorMatchForTAS(a.cq, a.wl.TopologySpreading, ps, a.wl.PodSpec(psID), flavor, rg); message != nil {
				log.V(3).Info("Flavor does not match TAS requirements", "reason", *message)
				status.appendf("%s", *message)
				return status
			}
		}
		podSpec := podSets[psIdx].Template.Spec
		taint, untolerated := corev1helpers.FindMatchingUntoleratedTaint(log, flavor.Spec.NodeTaints, append(podSpec.Tolerations, flavor.Spec.Tolerations...), func(t *corev1.Taint) bool {
			return t.Effect == corev1.TaintEffectNoSchedule || t.Effect == corev1.TaintEffectNoExecute
		}, true)
		if untolerated {
			status.appendf("untolerated taint %s in flavor %s", taint, flavorName)
			return status
		}
		selector := flavorSelector(&podSpec, flavorLabelKeys)
		if match, err := selector.Match(&corev1.Node{Labels: flavor.Spec.NodeLabels}); !match || err != nil {
			if err != nil {
				status.err = err
				return status
			}
			status.appendf("flavor %s doesn't match node affinity", flavorName)
			return status
		}
	}
	return status
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

// fitsMaxCapacity checks potential capacity without considering current usage
// or whether preemption is possible.
func (a *FlavorAssigner) fitsMaxCapacity(fr resources.FlavorResource, assumedUsage resources.Amount, requestUsage resources.Amount) *Status {
	maxCapacity := a.cq.PotentialAvailable(fr)
	if assumedUsage.Add(requestUsage).Cmp(maxCapacity) <= 0 {
		return nil
	}
	status := NewStatus()
	status.noFitReason = kueue.WorkloadQuotaReservedReasonExceedsMaxQuota
	status.appendf(
		"insufficient quota for %s in flavor %s, previously considered podsets requests (%s) + current podset request (%s) > maximum capacity (%s)",
		fr.Resource,
		fr.Flavor,
		a.resourceFormatter.ExactAmountString(fr.Resource, assumedUsage),
		a.resourceFormatter.ExactAmountString(fr.Resource, requestUsage),
		a.resourceFormatter.ExactAmountString(fr.Resource, maxCapacity),
	)
	return status
}

// fitsResourceQuota returns how this flavor could be assigned to the resource,
// according to the remaining quota in the ClusterQueue and cohort.
// If it fits, also returns if borrowing required. Similarly, it returns information
// if borrowing is required when preempting.
// If the flavor doesn't satisfy limits immediately (when waiting or preemption
// could help), it returns a Status with reasons.
func (a *FlavorAssigner) fitsResourceQuota(
	ctx context.Context,
	fr resources.FlavorResource,
	assumedUsage resources.Amount,
	requestUsage resources.Amount,
	rQuota schdcache.ResourceQuota,
) (preemptionMode, int, *Status) {
	if status := a.fitsMaxCapacity(fr, assumedUsage, requestUsage); status != nil {
		return noFit, 0, status
	}
	status := Status{
		noFitReason: kueue.WorkloadQuotaReservedReasonWaitingForQuota,
	}

	available := a.cq.Available(fr)
	val := assumedUsage.Add(requestUsage)

	borrow, mayReclaimInHierarchy := classical.FindHeightOfLowestSubtreeThatFits(a.cq, fr, val)
	// Fit
	if val.Cmp(available) <= 0 {
		return fit, borrow, nil
	}

	// Preempt
	status.appendf("insufficient unused quota for %s in flavor %s, %s more needed",
		fr.Resource, fr.Flavor, a.resourceFormatter.ExactAmountString(fr.Resource, val.Sub(available)))

	if rQuota.Nominal.Cmp(val) >= 0 || mayReclaimInHierarchy || a.canPreemptWithinClusterQueue() || a.canPreemptWhileBorrowing() {
		preemptionPossiblity, borrowAfterPreemptions := a.oracle.SimulatePreemption(ctx, a.cq, *a.wl, fr, val)
		mode := fromPreemptionPossibility(preemptionPossiblity)
		if mode != noFit {
			status.noFitReason = ""
		}
		return mode, borrowAfterPreemptions, &status
	}
	return noFit, borrow, &status
}

func (a *FlavorAssigner) canPreemptWithinClusterQueue() bool {
	return a.cq.Preemption.WithinClusterQueue != "" && a.cq.Preemption.WithinClusterQueue != kueue.PreemptionPolicyNever
}

func (a *FlavorAssigner) canPreemptWhileBorrowing() bool {
	return (a.cq.Preemption.BorrowWithinCohort != nil && a.cq.Preemption.BorrowWithinCohort.Policy != kueue.BorrowWithinCohortPolicyNever) ||
		(a.enableFairSharing && a.cq.Preemption.ReclaimWithinCohort != kueue.PreemptionPolicyNever) ||
		a.usesConfigurablePreemption()
}

// usesConfigurablePreemption returns true if the ClusterQueue references a
// PreemptionConfig. The rules of a PreemptionConfig may select candidates
// independently of the quota-based restrictions, so preemption might be
// possible even if the ClusterQueue would borrow afterwards, and the classical
// preemption policies don't allow it. Whether any rule is actually triggered is
// determined by the preemption algorithm itself.
// TODO(#15893): stop widening canPreemptWhileBorrowing, leaving the borrowing
// relaxation to the ConfigurablePreemption rules alone, once ConfigurablePreemption
// covers the classical and Fair Sharing preemption and the three become mutually
// exclusive.
func (a *FlavorAssigner) usesConfigurablePreemption() bool {
	return features.Enabled(features.ConfigurablePreemptions) && a.cq.PreemptionConfigName != nil
}

func filterRequestedResources(req resources.Requests, allowList sets.Set[corev1.ResourceName]) resources.Requests {
	filtered := resources.NewRequests()
	req.ForEach(func(resName corev1.ResourceName, quantity resources.Amount) {
		if allowList.Has(resName) {
			filtered.Set(resName, quantity)
		}
	})
	return filtered
}

// NominationMapping pins the initial flavors during TAS and preemption-target-overlap
// recomputation. Keep that pin scoped to whichever recomputation is enabled.
func (a *FlavorAssigner) shouldRespectNominationMapping() bool {
	return len(a.wl.NominationMapping) > 0 &&
		(features.Enabled(features.RecomputeAssignmentUponPreemptionTargetsOverlap) ||
			(features.Enabled(features.TopologyAwareScheduling) &&
				features.Enabled(features.TASRecomputeAssignmentWithinSchedulingCycle)))
}

// shouldSkipBasedOnNominationMapping returns true if the flavor should be skipped to enforce stickiness.
// We stick to nominated flavors from the initial attempt to avoid flavor switching during recomputation.
//
// Note: Assumes NominationMapping is complete. If a resource is not requested by a pod set in the group,
// it returns "" which won't match fName. We rely on the upstream guarantee that at least one pod set
// in the group requests the resource and has a nominated flavor, otherwise it would incorrectly skip.
func (a *FlavorAssigner) shouldSkipBasedOnNominationMapping(log logr.Logger,
	fName kueue.ResourceFlavorReference,
	psIDs []int,
	resName corev1.ResourceName,
) bool {
	for _, psID := range psIDs {
		psName := a.wl.Obj.Spec.PodSets[psID].Name
		if fName == a.wl.NominationMapping[psName][resName] {
			log.V(5).Info("Found flavor in the nomination mapping - cannot skip", "psName", psName, "resName", resName, "flavorName", fName)
			return false
		}
	}
	log.V(5).Info("Didn't find the flavor in the nomination mapping - skipping", "resName", resName, "flavorName", fName)
	return true
}

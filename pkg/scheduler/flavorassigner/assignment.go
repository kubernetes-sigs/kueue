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
	"slices"
	"strings"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/policy"
	"sigs.k8s.io/kueue/pkg/util/podset"
	"sigs.k8s.io/kueue/pkg/util/tas"
	"sigs.k8s.io/kueue/pkg/workload"
)

type Assignment struct {
	PodSets []PodSetAssignment
	// Borrowing is the height of the smallest cohort tree that fits
	// the additional Usage. It equals to 0 if no borrowing is required.
	Borrowing int

	// FlavorScanState records flavor scan progress from this assignment attempt
	// for reuse in subsequent scheduling attempts.
	FlavorScanState workload.FlavorScanState

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

	// quotaCheckStrategy is the strategy to use for quota check.
	quotaCheckStrategy configapi.QuotaCheckStrategy

	// NoFitReason contains the reason why the overall assignment failed with NoFit.
	NoFitReason string

	// ZeroCountFlavorFallback records why zero-count PodSets needed a flavor
	// assignment without the capacity probe, for a warning after quota reservation.
	ZeroCountFlavorFallback string
}

// UpdateForTASResult updates the Assignment with the TAS result
func (a *Assignment) UpdateForTASResult(log logr.Logger, cq *schdcache.ClusterQueueSnapshot, wl *workload.Info, result schdcache.TASAssignmentsResult) {
	for psName, psResult := range result {
		psAssignment := a.podSetAssignmentByName(psName)
		psAssignment.TopologyAssignment = psResult.TopologyAssignment
		if psResult.TopologyAssignment != nil && psAssignment.DelayedTopologyRequest != nil {
			psAssignment.DelayedTopologyRequest = new(kueue.DelayedTopologyRequestStateReady)
		}
	}
	a.Usage.TAS = a.ComputeTASNetUsage(log, cq, wl, nil)
}

// ResolvePodSetFailure updates the status of the given PodSet and adjusts
// the RepresentativeMode of the PodSet and the Assignment at large.
func (a *Assignment) ResolvePodSetFailure(psRef kueue.PodSetReference, targetMode FlavorAssignmentMode, failStatus Status) {
	psAssignment := a.podSetAssignmentByName(psRef)
	psAssignment.Status = failStatus
	// update the mode for all flavors and the representative mode
	a.demoteTo(targetMode)
	psAssignment.demoteTo(targetMode)
}

// SetRepresentativeMode updates the representative mode
// and flavor assignment modes across all registered PodSets.
func (a *Assignment) SetRepresentativeMode(mode FlavorAssignmentMode) {
	a.representativeMode = &mode
	for i := range a.PodSets {
		a.PodSets[i].updateMode(mode)
	}
}

// demoteTo demotes representative mode of asssignment to the targeted mode.
// Can only lower the mode (e.g. Fit to Preempt).
func (a *Assignment) demoteTo(targetMode FlavorAssignmentMode) {
	if targetMode < *a.representativeMode {
		a.representativeMode = new(targetMode)
	}
}

// ComputeTASNetUsage computes the net TAS usage for the assignment
func (a *Assignment) ComputeTASNetUsage(log logr.Logger, cq *schdcache.ClusterQueueSnapshot, wl *workload.Info, prevAdmission *kueue.Admission) workload.TASUsage {
	result := make(workload.TASUsage)
	for _, psa := range a.PodSets {
		if psa.TopologyAssignment == nil {
			continue
		}
		// Pods the current admission already places on a domain are accounted for
		// in the snapshot through the cache, so only the additional pods count
		// towards the net usage. Comparing per domain rather than skipping the
		// whole PodSet matters when the assignment changed: a second pass
		// replacing an unhealthy node moves pods onto a domain nothing has
		// accounted for yet, and that claim has to be checked and recorded like
		// any other.
		accounted := admittedDomainCounts(prevAdmission, psa.Name)
		podSet := podset.FindPodSetByName(wl.Obj.Spec.PodSets, psa.Name)
		if podSet == nil {
			log.Error(nil, "PodSet not found while computing TAS net usage", "podSet", psa.Name)
			continue
		}
		tasFlavor, err := onlyTASFlavor(psa.Flavors, cq.TASFlavors)
		if err != nil {
			log.Error(err, "Failed to find TAS flavor while computing TAS net usage", "podSet", psa.Name)
			continue
		}
		singlePodRequests := resources.NewRequestsFromPodSpec(wl.PodSpecByName(psa.Name))
		draDelegation := delegateDRABackedExtendedResources(wl.PodSpecByName(psa.Name), cq.DRABackedResources(), singlePodRequests)
		tasFlavorSnapshot := cq.TASFlavors[*tasFlavor]
		for _, domain := range psa.TopologyAssignment.Domains {
			count := domain.Count - accounted[tas.DomainID(domain.Values)]
			if count <= 0 {
				// Unchanged, or the domain now holds fewer pods than the
				// admission already accounts for. Releasing the surplus is not
				// expressible here, since a Usage value is applied with a single
				// add or subtract, so the snapshot keeps counting it until the
				// next one is built.
				continue
			}
			if _, ok := result[*tasFlavor]; !ok {
				result[*tasFlavor] = make(workload.TASFlavorUsage, 0)
			}
			result[*tasFlavor] = append(result[*tasFlavor], workload.TopologyDomainRequests{
				Values:            domain.Values,
				SinglePodRequests: tasFlavorSnapshot.RequestsForDomain(tas.DomainID(domain.Values), singlePodRequests, draDelegation).Clone(),
				Count:             count,
			})
		}
	}
	return result
}

// admittedDomainCounts returns the number of pods per topology domain that the
// workload's current admission already contributes to the snapshot, keyed by
// domain. It returns nil when the PodSet has no admitted topology assignment.
func admittedDomainCounts(prevAdmission *kueue.Admission, psName kueue.PodSetReference) map[tas.TopologyDomainID]int32 {
	if prevAdmission == nil {
		return nil
	}
	idx := slices.IndexFunc(prevAdmission.PodSetAssignments, func(psa kueue.PodSetAssignment) bool {
		return psa.Name == psName
	})
	if idx == -1 || prevAdmission.PodSetAssignments[idx].TopologyAssignment == nil {
		return nil
	}
	counts := make(map[tas.TopologyDomainID]int32)
	for _, domain := range tas.InternalFrom(prevAdmission.PodSetAssignments[idx].TopologyAssignment).Domains {
		counts[tas.DomainID(domain.Values)] += domain.Count
	}
	return counts
}

// Borrows returns the borrowing level of the assignment.
// It equals 0 if no borrowing is required.
func (a *Assignment) Borrows() int {
	return a.Borrowing
}

// RequiresBorrowing returns whether the assignment requires borrowing
// at any level.
func (a *Assignment) RequiresBorrowing() bool {
	return a.Borrowing > 0
}

func (a *Assignment) podSetAssignmentByName(psName kueue.PodSetReference) *PodSetAssignment {
	if idx := slices.IndexFunc(a.PodSets, func(ps PodSetAssignment) bool { return ps.Name == psName }); idx != -1 {
		return &a.PodSets[idx]
	}
	return nil
}

func (a *Assignment) updateMode(psName kueue.PodSetReference, mode FlavorAssignmentMode) {
	if psAssignment := a.podSetAssignmentByName(psName); psAssignment != nil {
		psAssignment.updateMode(mode)
		a.representativeMode = new(mode)
	}
}

func (a *Assignment) updateModeForTASRequests(tasRequests schdcache.WorkloadTASRequests, mode FlavorAssignmentMode) {
	for _, reqs := range tasRequests {
		for _, req := range reqs {
			a.updateMode(req.PodSet.Name, mode)
		}
	}
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

func (a *Assignment) ToAPI(log logr.Logger) []kueue.PodSetAssignment {
	psFlavors := make([]kueue.PodSetAssignment, len(a.PodSets))
	for i := range psFlavors {
		psFlavors[i] = a.PodSets[i].toAPI(log)
	}
	return psFlavors
}

// TotalRequestsFor returns the quota request used to size the workload for
// preemption, based on the assigned PodSet counts. For a replacement, it only
// includes the usage needed on top of the replaced slice.
func (a *Assignment) TotalRequestsFor(log logr.Logger, wl *workload.Info) resources.FlavorResourceQuantities {
	usage := make(resources.FlavorResourceQuantities)
	for _, ps := range wl.TotalRequests {
		// The assignment lists PodSets in group order, which can differ from wl.TotalRequests.
		psAssignment := a.podSetAssignmentByName(ps.Name)
		if psAssignment == nil {
			log.V(1).Info("PodSet not found in the assignment while computing preemption requests", "podSet", ps.Name)
			continue
		}
		newCount := psAssignment.Count
		if a.replaceWorkloadSlice != nil {
			if old := podSetResourcesByName(a.replaceWorkloadSlice.TotalRequests, ps.Name); old != nil {
				newCount -= old.Count
			}
		}
		ps = *ps.ScaledTo(newCount)

		podsFlavor := psAssignment.Flavors[corev1.ResourcePods]
		if podsFlavor != nil && newCount != 0 {
			fr := resources.FlavorResource{Flavor: podsFlavor.Name, Resource: corev1.ResourcePods}
			usage[fr] = usage[fr].AddInt64(int64(newCount))
		}

		if ps.Requests == nil {
			continue
		}
		ps.Requests.ForEach(func(res corev1.ResourceName, q resources.Amount) {
			// Requests taken from an admission already count Pods.
			if res == corev1.ResourcePods && podsFlavor != nil {
				return
			}
			// zero-quantity request may have no flavor (#8079), and is irrelevant for
			// later calculations
			if q.Sign() == 0 {
				return
			}
			if IgnoreUndeclaredResources(a.quotaCheckStrategy) && psAssignment.Flavors[res] == nil {
				log.V(3).Info("Skipping usage count for resource with undefined flavor", "res", res)
				return
			}
			flv := psAssignment.Flavors[res].Name
			usage[resources.FlavorResource{Flavor: flv, Resource: res}] = usage[resources.FlavorResource{Flavor: flv, Resource: res}].Add(q)
		})
	}
	return usage
}

func (a *Assignment) psError(psAssignment *PodSetAssignment, err error) {
	psAssignment.error(err)
	a.representativeMode = nil
}

func (a *Assignment) append(psIdx int, requests resources.Requests, psAssignment *PodSetAssignment) {
	triedFlavors := make(map[corev1.ResourceName]sets.Set[kueue.ResourceFlavorReference], len(psAssignment.Flavors))
	a.PodSets = append(a.PodSets, *psAssignment)
	for resource, flvAssignment := range psAssignment.Flavors {
		if flvAssignment.borrow > a.Borrowing {
			a.Borrowing = flvAssignment.borrow
		}
		fr := resources.FlavorResource{Flavor: flvAssignment.Name, Resource: resource}

		// For workload slicing, only add the delta (new - old) to avoid double-counting
		// podSets that already have quota reserved in the old slice.
		var requestAmount resources.Amount
		if requests != nil {
			requestAmount = requests.ResourceValue(resource)
		}
		if features.Enabled(features.ElasticJobsViaWorkloadSlices) && a.replaceWorkloadSlice != nil {
			oldRequest := a.findOldPodSetRequest(psAssignment.Name, resource)
			requestAmount = requestAmount.Sub(oldRequest)
		}

		a.Usage.Quota.Assigned[fr] = a.Usage.Quota.Assigned[fr].Add(requestAmount)
		triedFlavors[resource] = flvAssignment.TriedFlavors
	}
	// The next attempt resumes each PodSet by its position in the Workload, not by the
	// order in which the groups were assigned.
	if missing := psIdx + 1 - len(a.FlavorScanState.TriedFlavors); missing > 0 {
		a.FlavorScanState.TriedFlavors = append(a.FlavorScanState.TriedFlavors, make([]map[corev1.ResourceName]sets.Set[kueue.ResourceFlavorReference], missing)...)
	}
	a.FlavorScanState.TriedFlavors[psIdx] = triedFlavors
}

// findOldPodSetRequest returns the resource request from the old workload slice
// for the given podSet name and resource. Returns 0 if not found.
func (a *Assignment) findOldPodSetRequest(psName kueue.PodSetReference, resource corev1.ResourceName) resources.Amount {
	if a.replaceWorkloadSlice == nil {
		return resources.Amount{}
	}

	if oldPS := podSetResourcesByName(a.replaceWorkloadSlice.TotalRequests, psName); oldPS != nil && oldPS.Requests != nil {
		return oldPS.Requests.ResourceValue(resource)
	}
	return resources.Amount{}
}

func (a *Assignment) ResolveNoFitReason(cq *schdcache.ClusterQueueSnapshot) {
	if a.RepresentativeMode() != NoFit {
		return
	}

	var overallReason string

	for _, ps := range a.PodSets {
		if ps.RepresentativeMode() != NoFit {
			continue
		}
		if len(ps.FlavorAssignmentAttempts) == 0 {
			overallReason = mostSevereReason(overallReason, kueue.WorkloadQuotaReservedReasonNoMatchingFlavor)
			continue
		}

		// Map from resource group index to the minimum severity blocker (alternative flavors) for that group.
		rgMinReason := make(map[int]string)
		podSetReason := ps.Status.noFitReason

		for i, att := range ps.FlavorAssignmentAttempts {
			if att.Mode != NoFit {
				continue
			}
			r := att.NoFitReason
			rgIndices := findRGIndicesByFlavor(cq, att.Flavor)
			if len(rgIndices) == 0 {
				// Special case: flavor not found in any group (e.g. deleted).
				// Treat it as a unique independent group using a negative index.
				rgIndices = []int{-1 - i}
			}

			for _, rgIdx := range rgIndices {
				if existing, ok := rgMinReason[rgIdx]; !ok || reasonSeverity(r) < reasonSeverity(existing) {
					rgMinReason[rgIdx] = r
				}
			}
		}

		// Across groups, we take the maximum severity (co-requisites).
		for _, reason := range rgMinReason {
			podSetReason = mostSevereReason(podSetReason, reason)
		}

		overallReason = mostSevereReason(overallReason, podSetReason)
	}
	a.NoFitReason = overallReason
}

type Status struct {
	reasons     []string
	err         error
	noFitReason string
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
	slices.Sort(s.reasons)
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
		// e.g. onlyTASFlavor failed in WorkloadsTopologyRequests, or TAS request build failed
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

func (psa *PodSetAssignment) demoteTo(targetMode FlavorAssignmentMode) {
	for _, flvAssignment := range psa.Flavors {
		flvAssignment.Mode = min(flvAssignment.Mode, targetMode)
	}
}

func (psa *PodSetAssignment) markFlavorAttempt(flavor kueue.ResourceFlavorReference, mode FlavorAssignmentMode, reason string) {
	for i := range psa.FlavorAssignmentAttempts {
		if psa.FlavorAssignmentAttempts[i].Flavor == flavor {
			psa.FlavorAssignmentAttempts[i].Mode = mode
			psa.FlavorAssignmentAttempts[i].NoFitReason = reason
			break
		}
	}
}

func (psa *PodSetAssignment) error(err error) {
	psa.Status.err = err
}

type ResourceAssignment map[corev1.ResourceName]*FlavorAssignment

func (psa *PodSetAssignment) toAPI(log logr.Logger) kueue.PodSetAssignment {
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
		Count:                  new(psa.Count),
		TopologyAssignment:     tas.V1Beta2From(psa.TopologyAssignment, tas.WithLogger(log)),
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
	// DeferredFit indicates that the workload fits, but we cannot
	// admit it yet in this scheduling cycle as we are waiting
	// e.g. for some preemptions to finish.
	DeferredFit
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
	case DeferredFit:
		return "DeferredFit"
	case Fit:
		return "Fit"
	}
	return "Unknown"
}

type FlavorAssignment struct {
	Name         kueue.ResourceFlavorReference
	Mode         FlavorAssignmentMode
	TriedFlavors sets.Set[kueue.ResourceFlavorReference]
	borrow       int
}

// FlavorAssignmentAttempt captures one attempted flavor and its worst-case outcome
// across the requested resources.
type FlavorAssignmentAttempt struct {
	Flavor                kueue.ResourceFlavorReference
	Mode                  FlavorAssignmentMode
	Borrow                int
	PreemptionPossibility *policy.PreemptionPossibility
	Reasons               []string
	NoFitReason           string
}

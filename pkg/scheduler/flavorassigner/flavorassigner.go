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
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	corev1helpers "k8s.io/component-helpers/scheduling/corev1"
	"k8s.io/component-helpers/scheduling/corev1/nodeaffinity"
	"sigs.k8s.io/controller-runtime/pkg/log"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/classical"
	preemptioncommon "sigs.k8s.io/kueue/pkg/scheduler/preemption/common"
	utilmaps "sigs.k8s.io/kueue/pkg/util/maps"
	"sigs.k8s.io/kueue/pkg/util/orderedgroups"
	"sigs.k8s.io/kueue/pkg/util/podset"
	"sigs.k8s.io/kueue/pkg/util/resourcegroups"
	"sigs.k8s.io/kueue/pkg/util/tas"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workload/concurrentadmission"
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

	// quotaCheckStrategy is the strategy to use for quota check.
	quotaCheckStrategy configapi.QuotaCheckStrategy

	// NoFitReason contains the reason why the overall assignment failed with NoFit.
	NoFitReason string
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

func (a *Assignment) SetRepresentativeMode(mode FlavorAssignmentMode) {
	a.representativeMode = &mode
	for i := range a.PodSets {
		a.PodSets[i].updateMode(mode)
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
		singlePodRequests := resources.NewRequestsFromPodSpec(&podSet.Template.Spec)
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
				SinglePodRequests: singlePodRequests.Clone(),
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

// TotalRequestsFor - returns the total quota needs of the wl, taking into account the potential
// workload slice replacement, or scaling needed in case of partial admission.
//
// Note: ElasticJobsViaWorkloadSlices is mutually exclusive with PartialAdmission.
func (a *Assignment) TotalRequestsFor(log logr.Logger, wl *workload.Info) resources.FlavorResourceQuantities {
	usage := make(resources.FlavorResourceQuantities)
	for i, ps := range wl.TotalRequests {
		newCount := a.PodSets[i].Count
		if a.replaceWorkloadSlice != nil {
			newCount = ps.Count - a.replaceWorkloadSlice.TotalRequests[i].Count
		}
		ps = *ps.ScaledTo(newCount)

		if ps.Requests == nil {
			continue
		}
		ps.Requests.ForEach(func(res corev1.ResourceName, q int64) {
			// zero-quantity request may have no flavor (#8079), and is irrelevant for
			// later calculations
			if q == 0 {
				return
			}
			if IgnoreUndeclaredResources(a.quotaCheckStrategy) && a.PodSets[i].Flavors[res] == nil {
				log.V(3).Info("Skipping usage count for resource with undefined flavor", "res", res)
				return
			}
			flv := a.PodSets[i].Flavors[res].Name
			usage[resources.FlavorResource{Flavor: flv, Resource: res}] = usage[resources.FlavorResource{Flavor: flv, Resource: res}].AddInt64(q)
		})
	}
	return usage
}

func (a *Assignment) psError(psAssignment *PodSetAssignment, err error) {
	psAssignment.error(err)
	a.representativeMode = nil
}

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

func (psa *PodSetAssignment) reason(reason string) {
	psa.Status.reasons = append(psa.Status.reasons, reason)
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
		return new(preemptioncommon.NoCandidates)
	case preempt:
		return new(preemptioncommon.Preempt)
	case reclaim:
		return new(preemptioncommon.Reclaim)
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
	SimulatePreemption(
		ctx context.Context,
		cq *schdcache.ClusterQueueSnapshot,
		wl workload.Info,
		fr resources.FlavorResource,
		quantity resources.Amount,
	) (preemptioncommon.PreemptionPossibility, int)
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

// Assign assigns a flavor to each of the resources requested in each pod set.
// The result for each pod set is accompanied with reasons why the flavor can't
// be assigned immediately. Each assigned flavor is accompanied with a
// FlavorAssignmentMode.
func (a *FlavorAssigner) Assign(ctx context.Context, counts []int32) Assignment {
	log := log.FromContext(ctx)

	tasFlavorModes := make(tasFlavorModes)
	for {
		assignment := a.assignFlavors(ctx, log, counts, tasFlavorModes)
		if !features.Enabled(features.TopologyAwareScheduling) || assignment.RepresentativeMode() == NoFit {
			return assignment
		}
		if !a.assignTopology(ctx, log, &assignment, tasFlavorModes) {
			if features.Enabled(features.UnadmittedWorkloadsObservability) {
				assignment.resolveNoFitReason(a.cq)
			}
			return assignment
		}
	}
}

type indexedPodSet struct {
	originalIndex    int
	podSet           *workload.PodSetResources
	podSetAssignment *PodSetAssignment
}

type tasFlavorModeKey struct {
	podSetName kueue.PodSetReference
	flavor     kueue.ResourceFlavorReference
}

type tasFlavorMode struct {
	preemptionMode preemptionMode
	reasons        []string
}

type tasFlavorModes map[tasFlavorModeKey]tasFlavorMode

func (m tasFlavorModes) modeForPodSets(
	podSetIDs []int,
	wl *workload.Info,
	flavor kueue.ResourceFlavorReference,
) (tasFlavorMode, bool) {
	result := tasFlavorMode{preemptionMode: fit}
	found := false
	for _, podSetID := range podSetIDs {
		mode, exists := m[tasFlavorModeKey{
			podSetName: wl.Obj.Spec.PodSets[podSetID].Name,
			flavor:     flavor,
		}]
		if !exists {
			continue
		}
		found = true
		if mode.preemptionMode < result.preemptionMode {
			result.preemptionMode = mode.preemptionMode
		}
		result.reasons = mergeUnique(result.reasons, mode.reasons)
	}
	return result, found
}

func (a *FlavorAssigner) downgradeTASFlavorMode(
	modes tasFlavorModes,
	failure *schdcache.FailureInfo,
	mode preemptionMode,
) bool {
	failedPodSetIndex := slices.IndexFunc(a.wl.Obj.Spec.PodSets, func(podSet kueue.PodSet) bool {
		return podSet.Name == failure.PodSetName
	})
	if failedPodSetIndex < 0 {
		return false
	}

	failedGroupName := podSetGroupName(&a.wl.Obj.Spec.PodSets[failedPodSetIndex])
	changed := false
	for i := range a.wl.Obj.Spec.PodSets {
		podSet := &a.wl.Obj.Spec.PodSets[i]
		belongsToFailedGroup := podSet.Name == failure.PodSetName
		if failedGroupName != nil {
			groupName := podSetGroupName(podSet)
			belongsToFailedGroup = groupName != nil && *groupName == *failedGroupName
		}
		if !belongsToFailedGroup {
			continue
		}

		key := tasFlavorModeKey{podSetName: podSet.Name, flavor: failure.Flavor}
		current, found := modes[key]
		if !found || mode < current.preemptionMode {
			current.preemptionMode = mode
			changed = true
		}
		current.reasons = mergeUnique(current.reasons, []string{failure.Reason})
		modes[key] = current
	}
	return changed
}

func (a *FlavorAssigner) assignFlavors(ctx context.Context, log logr.Logger, counts []int32, tasFlavorModes tasFlavorModes) Assignment {
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
			requests[i] = *a.wl.TotalRequests[i].ScaledTo(counts[i])
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
		LastState: workload.AssignmentClusterQueueState{
			LastTriedFlavorIdx:     make([]map[corev1.ResourceName]int, 0, len(requests)),
			ClusterQueueGeneration: a.cq.AllocatableResourceGeneration,
			SchedulingCycle:        a.schedulingCycle,
			SchedulingHash:         a.wl.SchedulingHash,
		},
		replaceWorkloadSlice: a.replaceWorkloadSlice,
	}

	groupedRequests := orderedgroups.NewOrderedGroups[string, indexedPodSet]()

	for i, podSet := range requests {
		if a.cq.RGByResource(corev1.ResourcePods) != nil {
			if podSet.Requests != nil {
				podSet.Requests.Set(corev1.ResourcePods, int64(podSet.Count))
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

		groupKey := strconv.Itoa(i)
		if tr := a.wl.Obj.Spec.PodSets[i].TopologyRequest; tr != nil && tr.PodSetGroupName != nil {
			groupKey = *tr.PodSetGroupName
		}

		groupedRequests.Insert(groupKey, indexedPodSet{originalIndex: i, podSet: &podSet, podSetAssignment: &psAssignment})
	}

	for _, podSets := range groupedRequests.InOrder {
		requests := resources.NewRequests()
		psIDs := make([]int, len(podSets))
		for idx, podset := range podSets {
			psIDs[idx] = podset.originalIndex
			requests.Add(podset.podSet.Requests)
		}

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
				if quantity == 0 {
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

			flavors, status, considered := a.findFlavorForPodSets(ctx, log, psIDs, requests, resName, assignment.Usage.Quota.Assigned, tasFlavorModes)
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

			assignment.append(podSet.podSet.Requests, podSet.podSetAssignment)
			if podSet.podSetAssignment.Status.IsError() || (podSet.podSet.Requests != nil && podSet.podSet.Requests.Len() > 0 && len(podSet.podSetAssignment.Flavors) == 0) {
				atLeastOnePodsAssignmentFailed = true
			}
		}
		if atLeastOnePodsAssignmentFailed {
			if features.Enabled(features.UnadmittedWorkloadsObservability) {
				assignment.resolveNoFitReason(a.cq)
			}
			return assignment
		}
	}
	if assignment.RepresentativeMode() == NoFit {
		if features.Enabled(features.UnadmittedWorkloadsObservability) {
			assignment.resolveNoFitReason(a.cq)
		}
		return assignment
	}

	return assignment
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
		idxPodSet.podSet.Requests.ForEach(func(name corev1.ResourceName, _ int64) {
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

func (a *FlavorAssigner) assignTopology(
	ctx context.Context,
	log logr.Logger,
	assignment *Assignment,
	tasFlavorModes tasFlavorModes,
) bool {
	tasRequests := assignment.WorkloadsTopologyRequests(log, a.wl, a.cq)
	if assignment.RepresentativeMode() == Fit {
		result := a.cq.FindTopologyAssignmentsForWorkload(ctx, tasRequests, schdcache.WithWorkload(a.wl.Obj))
		failure := result.Failure()
		if failure == nil {
			assignment.UpdateForTASResult(log, a.cq, a.wl, result)
			return false
		}

		if a.shouldRetryFlavorAssignmentAfterTASFailure() &&
			a.downgradeTASFlavorMode(tasFlavorModes, failure, preempt) {
			return true
		}

		psAssignment := assignment.podSetAssignmentByName(failure.PodSetName)
		psAssignment.reason(failure.Reason)
		assignment.updateMode(failure.PodSetName, Preempt)
	}

	if assignment.RepresentativeMode() != Preempt || workload.HasUnhealthyNodes(a.wl.Obj) {
		return false
	}

	// Don't preempt other workloads if looking for a failed node replacement.
	tasRequests = assignment.WorkloadsTopologyRequests(log, a.wl, a.cq)
	result := a.cq.FindTopologyAssignmentsForWorkload(
		ctx,
		tasRequests,
		schdcache.WithSimulateEmpty(true),
		schdcache.WithWorkload(a.wl.Obj),
	)
	failure := result.Failure()
	if failure == nil {
		// Update TAS-related assignments to Preempt because preemptions might be needed
		// in resources in which total unused quota is sufficient (Fit), but the
		// quota is fragmented.
		assignment.updateModeForTASRequests(tasRequests, Preempt)
		return false
	}

	if a.shouldRetryFlavorAssignmentAfterTASFailure() &&
		a.downgradeTASFlavorMode(
			tasFlavorModes,
			failure,
			noFit,
		) {
		return true
	}

	psAssignment := assignment.podSetAssignmentByName(failure.PodSetName)
	if features.Enabled(features.UnadmittedWorkloadsObservability) {
		psAssignment.markFlavorAttempt(failure.Flavor, NoFit, kueue.WorkloadQuotaReservedReasonTopologyPlacementFailed)
	}
	psAssignment.Status.reasons = mergeUnique(psAssignment.Status.reasons, []string{failure.Reason})
	assignment.updateMode(failure.PodSetName, NoFit)
	return false
}

func (a *FlavorAssigner) shouldRetryFlavorAssignmentAfterTASFailure() bool {
	return features.Enabled(features.FlavorFungibility) &&
		features.Enabled(features.FlavorFungibilityPreserveScanProgress) &&
		!a.shouldRespectNominationMapping() &&
		!workload.HasUnhealthyNodes(a.wl.Obj)
}

func (a *Assignment) resolveNoFitReason(cq *schdcache.ClusterQueueSnapshot) {
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
		var podSetReason string
		for _, reason := range rgMinReason {
			podSetReason = mostSevereReason(podSetReason, reason)
		}

		overallReason = mostSevereReason(overallReason, podSetReason)
	}
	a.NoFitReason = overallReason
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
		var requestAmount int64
		if requests != nil {
			requestAmount = requests.ResourceValue(resource)
		}
		if features.Enabled(features.ElasticJobsViaWorkloadSlices) && a.replaceWorkloadSlice != nil {
			oldRequest := a.findOldPodSetRequest(psAssignment.Name, resource)
			requestAmount -= oldRequest
		}

		a.Usage.Quota.Assigned[fr] = a.Usage.Quota.Assigned[fr].AddInt64(requestAmount)
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
		if oldPS.Name == psName && oldPS.Requests != nil {
			return oldPS.Requests.ResourceValue(resource)
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
	ctx context.Context,
	log logr.Logger,
	psIDs []int,
	requests resources.Requests,
	resName corev1.ResourceName,
	assignmentUsage resources.FlavorResourceQuantities,
	tasFlavorModes tasFlavorModes,
) (ResourceAssignment, *Status, FlavorAssignmentAttempts) {
	resourceGroup := a.cq.RGByResource(resName)
	if resourceGroup == nil {
		return nil, NewStatus(fmt.Sprintf("resource %s unavailable in ClusterQueue", resName)), nil
	}

	status := NewStatus()
	requests = filterRequestedResources(requests, resourceGroup.CoveredResources)

	podSets := make([]*kueue.PodSet, len(psIDs))
	for idx, psID := range psIDs {
		podSets[idx] = &a.wl.Obj.Spec.PodSets[psID]
	}

	var bestAssignment ResourceAssignment
	bestAssignmentMode := worstGranularMode()
	consideredFlavors := newFlavorAssignmentAttempts(len(resourceGroup.Flavors))
	var bestStatus *Status
	bestHasTASFlavorMode := false

	// We will only check against the flavors' labels for the resource.
	attemptedFlavorIdx := -1
	idx := a.wl.LastAssignment.NextFlavorToTryForPodSetResource(psIDs[0], resName)
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

		assignments := make(ResourceAssignment, requests.Len())
		// Calculate representativeMode for this assignment as the worst mode among all requests.
		representativeMode := bestGranularMode()
		maxBorrow := 0
		var flavorQuotaReasons []string
		var flavorNoFitReason string

		requests.ForEach(func(rName corev1.ResourceName, val int64) {
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
						flavorNoFitReason = mostSevereReason(flavorNoFitReason, kueue.WorkloadQuotaReservedReasonNoMatchingFlavor)
						break
					}

					// Subtract the resource usage of the preempted slice to request only the delta needed.
					if preemptWorkloadRequests.Requests != nil {
						val -= preemptWorkloadRequests.Requests.ResourceValue(rName)
					}
				}
			}

			resQuota := a.cq.QuotaFor(resources.FlavorResource{Flavor: fName, Resource: rName})
			// Check considering the flavor usage by previous pod sets.
			fr := resources.FlavorResource{Flavor: fName, Resource: rName}

			preemptionMode, borrow, s := a.fitsResourceQuota(ctx, log, fr, assignmentUsage[fr], val, resQuota)
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

		tasMode, flavorHasTASFlavorMode := tasFlavorModes.modeForPodSets(psIDs, a.wl, fName)
		if flavorHasTASFlavorMode {
			if tasMode.preemptionMode < representativeMode.preemptionMode {
				representativeMode.preemptionMode = tasMode.preemptionMode
				for _, assignment := range assignments {
					assignment.Mode = tasMode.preemptionMode.flavorAssignmentMode()
				}
			}
			flavorQuotaReasons = mergeUnique(flavorQuotaReasons, tasMode.reasons)
			status.reasons = mergeUnique(status.reasons, tasMode.reasons)
			if tasMode.preemptionMode == noFit {
				flavorNoFitReason = mostSevereReason(flavorNoFitReason, kueue.WorkloadQuotaReservedReasonTopologyPlacementFailed)
			}
		}

		consideredFlavors.AddRepresentativeModeFlavorAttempt(fName, representativeMode.preemptionMode, maxBorrow, flavorQuotaReasons, flavorNoFitReason)
		flavorStatus := &Status{reasons: slices.Clone(flavorQuotaReasons), noFitReason: flavorNoFitReason}

		if features.Enabled(features.FlavorFungibility) {
			if !shouldTryNextFlavor(representativeMode, a.cq.FlavorFungibility) {
				bestAssignment = assignments
				bestAssignmentMode = representativeMode
				bestStatus = flavorStatus
				bestHasTASFlavorMode = flavorHasTASFlavorMode
				break
			}
			if isPreferred(representativeMode, bestAssignmentMode, a.cq.FlavorFungibility) {
				bestAssignment = assignments
				bestAssignmentMode = representativeMode
				bestStatus = flavorStatus
				bestHasTASFlavorMode = flavorHasTASFlavorMode
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
		if bestHasTASFlavorMode && bestStatus != nil {
			return bestAssignment, bestStatus, consideredFlavors
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
			if message := checkPodSetAndFlavorMatchForTAS(a.cq, ps, flavor, rg); message != nil {
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
		if match, err := selector.Match(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Labels: flavor.Spec.NodeLabels}}); !match || err != nil {
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

// fitsResourceQuota returns how this flavor could be assigned to the resource,
// according to the remaining quota in the ClusterQueue and cohort.
// If it fits, also returns if borrowing required. Similarly, it returns information
// if borrowing is required when preempting.
// If the flavor doesn't satisfy limits immediately (when waiting or preemption
// could help), it returns a Status with reasons.
func (a *FlavorAssigner) fitsResourceQuota(
	ctx context.Context,
	log logr.Logger,
	fr resources.FlavorResource,
	assumedUsage resources.Amount,
	requestUsage int64,
	rQuota schdcache.ResourceQuota,
) (preemptionMode, int, *Status) {
	status := Status{
		noFitReason: kueue.WorkloadQuotaReservedReasonWaitingForQuota,
	}

	available := a.cq.Available(fr)
	maxCapacity := a.cq.PotentialAvailable(fr)

	val := assumedUsage.AddInt64(requestUsage)

	// No Fit
	if val.Cmp(maxCapacity) > 0 {
		status.appendf(
			"insufficient quota for %s in flavor %s, previously considered podsets requests (%s) + current podset request (%s) > maximum capacity (%s)",
			fr.Resource,
			fr.Flavor,
			a.resourceFormatter.AmountQuantityString(fr.Resource, assumedUsage),
			a.resourceFormatter.ResourceQuantityString(fr.Resource, requestUsage),
			a.resourceFormatter.AmountQuantityString(fr.Resource, maxCapacity),
		)
		status.noFitReason = kueue.WorkloadQuotaReservedReasonExceedsMaxQuota
		return noFit, 0, &status
	}

	borrow, mayReclaimInHierarchy := classical.FindHeightOfLowestSubtreeThatFits(a.cq, fr, val)
	// Fit
	if val.Cmp(available) <= 0 {
		return fit, borrow, nil
	}

	// Preempt
	status.appendf("insufficient unused quota for %s in flavor %s, %s more needed",
		fr.Resource, fr.Flavor, a.resourceFormatter.AmountQuantityString(fr.Resource, val.Sub(available)))

	if rQuota.Nominal.Cmp(val) >= 0 || mayReclaimInHierarchy || a.canPreemptWhileBorrowing() {
		preemptionPossiblity, borrowAfterPreemptions := a.oracle.SimulatePreemption(ctx, a.cq, *a.wl, fr, val)
		mode := fromPreemptionPossibility(preemptionPossiblity)
		if mode != noFit {
			status.noFitReason = ""
		}
		return mode, borrowAfterPreemptions, &status
	}
	return noFit, borrow, &status
}

func (a *FlavorAssigner) canPreemptWhileBorrowing() bool {
	return (a.cq.Preemption.BorrowWithinCohort != nil && a.cq.Preemption.BorrowWithinCohort.Policy != kueue.BorrowWithinCohortPolicyNever) ||
		(a.enableFairSharing && a.cq.Preemption.ReclaimWithinCohort != kueue.PreemptionPolicyNever)
}

func filterRequestedResources(req resources.Requests, allowList sets.Set[corev1.ResourceName]) resources.Requests {
	filtered := resources.NewRequests()
	req.ForEach(func(resName corev1.ResourceName, quantity int64) {
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

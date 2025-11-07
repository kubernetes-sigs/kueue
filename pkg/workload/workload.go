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

package workload

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
	resourcehelpers "k8s.io/component-helpers/resource"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/util/api"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
	utilmaps "sigs.k8s.io/kueue/pkg/util/maps"
	utilptr "sigs.k8s.io/kueue/pkg/util/ptr"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
	utilslices "sigs.k8s.io/kueue/pkg/util/slices"
	"sigs.k8s.io/kueue/pkg/util/wait"
)

const (
	StatusPending       = "pending"
	StatusQuotaReserved = "quotaReserved"
	StatusAdmitted      = "admitted"
	StatusFinished      = "finished"
)

var (
	admissionManagedConditions = []string{
		kueue.WorkloadQuotaReserved,
		kueue.WorkloadEvicted,
		kueue.WorkloadAdmitted,
		kueue.WorkloadPreempted,
		kueue.WorkloadRequeued,
		kueue.WorkloadDeactivationTarget,
	}
)

// Reference is the full reference to Workload formed as <namespace>/< kueue.WorkloadName >.
type Reference string

func NewReference(namespace, name string) Reference {
	return Reference(namespace + "/" + name)
}

func Status(w *kueue.Workload) string {
	if apimeta.IsStatusConditionTrue(w.Status.Conditions, kueue.WorkloadFinished) {
		return StatusFinished
	}
	if IsAdmitted(w) {
		return StatusAdmitted
	}
	if HasQuotaReservation(w) {
		return StatusQuotaReserved
	}
	return StatusPending
}

type AssignmentClusterQueueState struct {
	LastTriedFlavorIdx     []map[corev1.ResourceName]int
	ClusterQueueGeneration int64
}

// dra holds DRA-specific configuration for workload.Info construction.
type dra struct {
	preprocessedDRAResources map[kueue.PodSetReference]corev1.ResourceList
}

type InfoOptions struct {
	excludedResourcePrefixes []string
	resourceTransformations  map[corev1.ResourceName]*config.ResourceTransformation
	dra
}

type InfoOption func(*InfoOptions)

var defaultOptions = InfoOptions{}

// WithExcludedResourcePrefixes adds the prefixes
func WithExcludedResourcePrefixes(n []string) InfoOption {
	return func(o *InfoOptions) {
		o.excludedResourcePrefixes = n
	}
}

// WithResourceTransformations sets the resource transformations.
func WithResourceTransformations(transforms []config.ResourceTransformation) InfoOption {
	return func(o *InfoOptions) {
		o.resourceTransformations = utilslices.ToRefMap(transforms, func(e *config.ResourceTransformation) corev1.ResourceName { return e.Input })
	}
}

// WithPreprocessedDRAResources creates an InfoOption that provides preprocessed DRA resources.
func WithPreprocessedDRAResources(draResources map[kueue.PodSetReference]corev1.ResourceList) InfoOption {
	return func(o *InfoOptions) {
		o.dra = dra{
			preprocessedDRAResources: draResources,
		}
	}
}

func (s *AssignmentClusterQueueState) Clone() *AssignmentClusterQueueState {
	c := AssignmentClusterQueueState{
		LastTriedFlavorIdx:     make([]map[corev1.ResourceName]int, len(s.LastTriedFlavorIdx)),
		ClusterQueueGeneration: s.ClusterQueueGeneration,
	}
	for ps, flavorIdx := range s.LastTriedFlavorIdx {
		c.LastTriedFlavorIdx[ps] = maps.Clone(flavorIdx)
	}
	return &c
}

// PendingFlavors returns whether there are pending flavors to try
// after the last attempt.
func (s *AssignmentClusterQueueState) PendingFlavors() bool {
	if s == nil {
		// This is only reached in unit tests.
		return false
	}
	for _, podSetIdxs := range s.LastTriedFlavorIdx {
		for _, idx := range podSetIdxs {
			if idx != -1 {
				return true
			}
		}
	}
	return false
}

func (s *AssignmentClusterQueueState) NextFlavorToTryForPodSetResource(ps int, res corev1.ResourceName) int {
	if !features.Enabled(features.FlavorFungibility) {
		return 0
	}
	if s == nil || ps >= len(s.LastTriedFlavorIdx) {
		return 0
	}
	idx, ok := s.LastTriedFlavorIdx[ps][res]
	if !ok {
		return 0
	}
	return idx + 1
}

// Info holds a Workload object and some pre-processing.
type Info struct {
	Obj *kueue.Workload
	// list of total resources requested by the podsets.
	TotalRequests []PodSetResources
	// Populated from the queue during admission or from the admission field if
	// already admitted.
	ClusterQueue   kueue.ClusterQueueReference
	LastAssignment *AssignmentClusterQueueState

	// LocalQueueFSUsage indicates the historical usage of resource in the LocalQueue, needed for the
	// AdmissionFairSharing feature, it is only populated for Infos in cache.Snapshot (not in queue manager).
	LocalQueueFSUsage *float64

	// SecondPassIteration indicates the current iteration of the second pass scheduling.
	SecondPassIteration int
}

type PodSetResources struct {
	// Name is the name of the PodSet.
	Name kueue.PodSetReference
	// Requests incorporates the requests from all pods in the podset.
	Requests resources.Requests
	// Count indicates how many pods are in the podset.
	Count int32

	// TopologyRequest specifies the requests for TAS
	TopologyRequest *TopologyRequest

	// DelayedTopologyRequest indicates the state of the delayed TopologyRequest
	DelayedTopologyRequest *kueue.DelayedTopologyRequestState

	// Flavors are populated when the Workload is assigned.
	Flavors map[corev1.ResourceName]kueue.ResourceFlavorReference
}

func (p *PodSetResources) SinglePodRequests() resources.Requests {
	return p.Requests.ScaledDown(int64(p.Count))
}

type TopologyRequest struct {
	Levels         []string
	DomainRequests []TopologyDomainRequests
}

type TopologyDomainRequests struct {
	Values            []string
	SinglePodRequests resources.Requests
	// Count indicates how many pods are requested in this TopologyDomain.
	Count int32
}

func (t *TopologyDomainRequests) TotalRequests() resources.Requests {
	return t.SinglePodRequests.ScaledUp(int64(t.Count))
}

func (p *PodSetResources) ScaledTo(newCount int32) *PodSetResources {
	if p.TopologyRequest != nil {
		return p
	}
	ret := &PodSetResources{
		Name:     p.Name,
		Requests: maps.Clone(p.Requests),
		Count:    p.Count,
		Flavors:  maps.Clone(p.Flavors),
	}

	if p.Count != 0 && p.Count != newCount {
		ret.Requests.Divide(int64(ret.Count))
		ret.Requests.Mul(int64(newCount))
		ret.Count = newCount
	}
	return ret
}

func NewInfo(w *kueue.Workload, opts ...InfoOption) *Info {
	options := defaultOptions
	for _, opt := range opts {
		opt(&options)
	}
	info := &Info{
		Obj: w,
	}
	if w.Status.Admission != nil {
		info.ClusterQueue = w.Status.Admission.ClusterQueue
		info.TotalRequests = totalRequestsFromAdmission(w)
	} else {
		info.TotalRequests = totalRequestsFromPodSets(w, &options)
	}
	return info
}

func (i *Info) Update(wl *kueue.Workload) {
	i.Obj = wl
}

func (i *Info) CanBePartiallyAdmitted() bool {
	return CanBePartiallyAdmitted(i.Obj)
}

// Usage returns the total resource usage for the workload, including regular
// quota and TAS usage.
func (i *Info) Usage() Usage {
	return Usage{
		Quota: i.FlavorResourceUsage(),
		TAS:   i.TASUsage(),
	}
}

// FlavorResourceUsage returns the total resource usage for the workload,
// per flavor (if assigned, otherwise flavor shows as empty string), per resource.
func (i *Info) FlavorResourceUsage() resources.FlavorResourceQuantities {
	total := make(resources.FlavorResourceQuantities)
	if i == nil {
		return total
	}
	for _, psReqs := range i.TotalRequests {
		for res, q := range psReqs.Requests {
			flv := psReqs.Flavors[res]
			total[resources.FlavorResource{Flavor: flv, Resource: res}] += q
		}
	}
	return total
}

func dropExcludedResources(input corev1.ResourceList, excludedPrefixes []string) corev1.ResourceList {
	res := corev1.ResourceList{}
	for inputName, inputQuantity := range input {
		exclude := false
		for _, excludedPrefix := range excludedPrefixes {
			if strings.HasPrefix(string(inputName), excludedPrefix) {
				exclude = true
				break
			}
		}
		if !exclude {
			res[inputName] = inputQuantity
		}
	}
	return res
}

func (i *Info) CalcLocalQueueFSUsage(ctx context.Context, c client.Client, resWeights map[corev1.ResourceName]float64, afsEntryPenalties *utilmaps.SyncMap[utilqueue.LocalQueueReference, corev1.ResourceList]) (float64, error) {
	var lq kueue.LocalQueue
	lqKey := client.ObjectKey{Namespace: i.Obj.Namespace, Name: string(i.Obj.Spec.QueueName)}
	if err := c.Get(ctx, lqKey, &lq); err != nil {
		return 0, err
	}
	var usage float64
	if lq.Status.FairSharing == nil || lq.Status.FairSharing.AdmissionFairSharingStatus == nil {
		// If FairSharing is not enabled or initialized, return 0 usage.
		return 0, nil
	}
	for resName, resVal := range lq.Status.FairSharing.AdmissionFairSharingStatus.ConsumedResources {
		weight, found := resWeights[resName]
		if !found {
			weight = 1
		}
		usage += weight * resVal.AsApproximateFloat64()
	}
	penalty := corev1.ResourceList{}
	if afsEntryPenalties != nil {
		penalty, _ = afsEntryPenalties.Get(utilqueue.Key(&lq))
	}
	for resName, penaltyVal := range penalty {
		weight, found := resWeights[resName]
		if !found {
			weight = 1
		}
		usage += weight * penaltyVal.AsApproximateFloat64()
	}

	if lq.Spec.FairSharing != nil && lq.Spec.FairSharing.Weight != nil {
		// if no weight for lq was defined, use default weight of 1
		usage /= lq.Spec.FairSharing.Weight.AsApproximateFloat64()
	}
	return usage, nil
}

// IsUsingTAS returns information if the workload is using TAS
func (i *Info) IsUsingTAS() bool {
	return slices.ContainsFunc(i.TotalRequests,
		func(ps PodSetResources) bool {
			return ps.TopologyRequest != nil
		})
}

// IsExplicitlyRequestingTAS returns information if the workload is requesting TAS
func IsExplicitlyRequestingTAS(podSets ...kueue.PodSet) bool {
	return slices.ContainsFunc(podSets,
		func(ps kueue.PodSet) bool {
			tr := ps.TopologyRequest
			return tr != nil && (tr.Unconstrained != nil || tr.Required != nil || tr.Preferred != nil || tr.PodSetSliceRequiredTopology != nil || tr.PodSetSliceSize != nil)
		})
}

// TASUsage returns topology usage requested by the Workload
func (i *Info) TASUsage() TASUsage {
	if !features.Enabled(features.TopologyAwareScheduling) || !i.IsUsingTAS() {
		return nil
	}
	result := make(TASUsage, 0)
	for _, ps := range i.TotalRequests {
		if ps.TopologyRequest != nil {
			psFlavors := sets.New[kueue.ResourceFlavorReference]()
			for _, psFlavor := range ps.Flavors {
				psFlavors.Insert(psFlavor)
			}
			for psFlavor := range psFlavors {
				result[psFlavor] = append(result[psFlavor], ps.TopologyRequest.DomainRequests...)
			}
		}
	}
	return result
}

func (i *Info) SumTotalRequests() corev1.ResourceList {
	reqs := make(resources.Requests)
	for _, psReqs := range i.TotalRequests {
		reqs.Add(psReqs.Requests)
	}
	return reqs.ToResourceList()
}

func applyResourceTransformations(input corev1.ResourceList, transforms map[corev1.ResourceName]*config.ResourceTransformation) corev1.ResourceList {
	match := false
	for resourceName := range input {
		if _, ok := transforms[resourceName]; ok {
			match = true
			break
		}
	}
	if !match {
		return input
	}
	output := make(corev1.ResourceList)
	for inputName, inputQuantity := range input {
		if mapping, ok := transforms[inputName]; ok {
			for outputName, baseFactor := range mapping.Outputs {
				outputQuantity := baseFactor.DeepCopy()
				outputQuantity.Mul(inputQuantity.Value())
				if accumulated, ok := output[outputName]; ok {
					outputQuantity.Add(accumulated)
				}
				output[outputName] = outputQuantity
			}
			if ptr.Deref(mapping.Strategy, config.Retain) == config.Retain {
				output[inputName] = inputQuantity
			}
		} else {
			output[inputName] = inputQuantity
		}
	}
	return output
}

func CanBePartiallyAdmitted(wl *kueue.Workload) bool {
	ps := wl.Spec.PodSets
	for psi := range ps {
		if ps[psi].Count > ptr.Deref(ps[psi].MinCount, ps[psi].Count) {
			return true
		}
	}
	return false
}

func Key(w *kueue.Workload) Reference {
	return NewReference(w.Namespace, w.Name)
}

func reclaimableCounts(wl *kueue.Workload) map[kueue.PodSetReference]int32 {
	return utilslices.ToMap(wl.Status.ReclaimablePods, func(i int) (kueue.PodSetReference, int32) {
		return wl.Status.ReclaimablePods[i].Name, wl.Status.ReclaimablePods[i].Count
	})
}

func podSetsCounts(wl *kueue.Workload) map[kueue.PodSetReference]int32 {
	return utilslices.ToMap(wl.Spec.PodSets, func(i int) (kueue.PodSetReference, int32) {
		return wl.Spec.PodSets[i].Name, wl.Spec.PodSets[i].Count
	})
}

func podSetsCountsAfterReclaim(wl *kueue.Workload) map[kueue.PodSetReference]int32 {
	totalCounts := podSetsCounts(wl)
	if !features.Enabled(features.ReclaimablePods) {
		return totalCounts
	}
	reclaimCounts := reclaimableCounts(wl)
	for podSetName := range totalCounts {
		if rc, found := reclaimCounts[podSetName]; found {
			totalCounts[podSetName] -= rc
		}
	}
	return totalCounts
}

func PodSetNameToTopologyRequest(wl *kueue.Workload) map[kueue.PodSetReference]*kueue.PodSetTopologyRequest {
	return utilslices.ToMap(wl.Spec.PodSets, func(i int) (kueue.PodSetReference, *kueue.PodSetTopologyRequest) {
		return wl.Spec.PodSets[i].Name, wl.Spec.PodSets[i].TopologyRequest
	})
}

func totalRequestsFromPodSets(wl *kueue.Workload, info *InfoOptions) []PodSetResources {
	if len(wl.Spec.PodSets) == 0 {
		return nil
	}
	res := make([]PodSetResources, 0, len(wl.Spec.PodSets))
	currentCounts := podSetsCountsAfterReclaim(wl)
	for _, ps := range wl.Spec.PodSets {
		count := currentCounts[ps.Name]
		setRes := PodSetResources{
			Name:  ps.Name,
			Count: count,
		}
		specRequests := resourcehelpers.PodRequests(&corev1.Pod{Spec: ps.Template.Spec}, resourcehelpers.PodResourcesOptions{})
		effectiveRequests := dropExcludedResources(specRequests, info.excludedResourcePrefixes)
		if features.Enabled(features.ConfigurableResourceTransformations) {
			effectiveRequests = applyResourceTransformations(effectiveRequests, info.resourceTransformations)
		}
		setRes.Requests = resources.NewRequests(effectiveRequests)
		if features.Enabled(features.DynamicResourceAllocation) && info.preprocessedDRAResources != nil {
			if draRes, exists := info.preprocessedDRAResources[ps.Name]; exists {
				for resName, quantity := range draRes {
					if setRes.Requests == nil {
						setRes.Requests = make(resources.Requests)
					}
					setRes.Requests[resName] += resources.ResourceValue(resName, quantity)
				}
			}
		}
		setRes.Requests.Mul(int64(count))
		res = append(res, setRes)
	}

	return res
}

func totalRequestsFromAdmission(wl *kueue.Workload) []PodSetResources {
	if wl.Status.Admission == nil {
		return nil
	}
	res := make([]PodSetResources, 0, len(wl.Spec.PodSets))
	currentCounts := podSetsCountsAfterReclaim(wl)
	totalCounts := podSetsCounts(wl)
	for _, psa := range wl.Status.Admission.PodSetAssignments {
		setRes := PodSetResources{
			Name:     psa.Name,
			Flavors:  psa.Flavors,
			Count:    ptr.Deref(psa.Count, totalCounts[psa.Name]),
			Requests: resources.NewRequests(psa.ResourceUsage),
		}
		if features.Enabled(features.TopologyAwareScheduling) && psa.TopologyAssignment != nil {
			setRes.TopologyRequest = &TopologyRequest{
				Levels: psa.TopologyAssignment.Levels,
			}
			for _, domain := range psa.TopologyAssignment.Domains {
				setRes.TopologyRequest.DomainRequests = append(setRes.TopologyRequest.DomainRequests, TopologyDomainRequests{
					Values:            domain.Values,
					SinglePodRequests: setRes.SinglePodRequests(),
					Count:             domain.Count,
				})
			}
		}
		if features.Enabled(features.TopologyAwareScheduling) && psa.DelayedTopologyRequest != nil {
			setRes.DelayedTopologyRequest = ptr.To(*psa.DelayedTopologyRequest)
		}

		// If countAfterReclaim is lower then the admission count indicates that
		// additional pods are marked as reclaimable, and the consumption should be scaled down.
		if countAfterReclaim := currentCounts[psa.Name]; countAfterReclaim < setRes.Count {
			setRes.Requests.Divide(int64(setRes.Count))
			setRes.Requests.Mul(int64(countAfterReclaim))
			setRes.Count = countAfterReclaim
		}
		// Otherwise if countAfterReclaim is higher it means that the podSet was partially admitted
		// and the count should be preserved.
		res = append(res, setRes)
	}
	return res
}

// UpdateStatus updates the condition of a workload with ssa,
// fieldManager being set to managerPrefix + "-" + conditionType
func UpdateStatus(ctx context.Context,
	c client.Client,
	wl *kueue.Workload,
	conditionType string,
	conditionStatus metav1.ConditionStatus,
	reason, message string,
	managerPrefix string,
	clock clock.Clock) error {
	now := metav1.NewTime(clock.Now())
	condition := metav1.Condition{
		Type:               conditionType,
		Status:             conditionStatus,
		LastTransitionTime: now,
		Reason:             reason,
		Message:            api.TruncateConditionMessage(message),
		ObservedGeneration: wl.Generation,
	}

	newWl := BaseSSAWorkload(wl, false)
	newWl.Status.Conditions = []metav1.Condition{condition}
	return c.Status().Patch(ctx, newWl, client.Apply, client.FieldOwner(managerPrefix+"-"+condition.Type))
}

// UnsetQuotaReservationWithCondition sets the QuotaReserved condition to false, clears
// the admission and set the WorkloadRequeued status.
// Returns whether any change was done.
func UnsetQuotaReservationWithCondition(wl *kueue.Workload, reason, message string, now time.Time) bool {
	condition := metav1.Condition{
		Type:               kueue.WorkloadQuotaReserved,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            api.TruncateConditionMessage(message),
		LastTransitionTime: metav1.NewTime(now),
		ObservedGeneration: wl.Generation,
	}
	changed := apimeta.SetStatusCondition(&wl.Status.Conditions, condition)
	if wl.Status.Admission != nil {
		wl.Status.Admission = nil
		changed = true
	}

	// Reset the admitted condition if necessary.
	if SyncAdmittedCondition(wl, now) {
		changed = true
	}
	return changed
}

// UpdateRequeueState calculate requeueAt time and update requeuingCount
func UpdateRequeueState(wl *kueue.Workload, backoffBaseSeconds int32, backoffMaxSeconds int32, clock clock.Clock) {
	if wl.Status.RequeueState == nil {
		wl.Status.RequeueState = &kueue.RequeueState{}
	}
	requeuingCount := ptr.Deref(wl.Status.RequeueState.Count, 0) + 1

	// Every backoff duration is about "60s*2^(n-1)+Rand" where:
	// - "n" represents the "requeuingCount",
	// - "Rand" represents the random jitter.
	// During this time, the workload is taken as an inadmissible and other
	// workloads will have a chance to be admitted.
	backoff := wait.NewBackoff(time.Duration(backoffBaseSeconds)*time.Second, time.Duration(backoffMaxSeconds)*time.Second, 2, 0.0001)
	waitDuration := backoff.WaitTime(int(requeuingCount))

	wl.Status.RequeueState.RequeueAt = ptr.To(metav1.NewTime(clock.Now().Add(waitDuration)))
	wl.Status.RequeueState.Count = &requeuingCount
}

// SetRequeuedCondition sets the WorkloadRequeued condition to true
func SetRequeuedCondition(wl *kueue.Workload, reason, message string, status bool) bool {
	condition := metav1.Condition{
		Type:               kueue.WorkloadRequeued,
		Reason:             reason,
		Message:            api.TruncateConditionMessage(message),
		ObservedGeneration: wl.Generation,
	}
	if status {
		condition.Status = metav1.ConditionTrue
	} else {
		condition.Status = metav1.ConditionFalse
	}
	return apimeta.SetStatusCondition(&wl.Status.Conditions, condition)
}

func QueuedWaitTime(wl *kueue.Workload, clock clock.Clock) time.Duration {
	queuedTime := wl.CreationTimestamp.Time
	if c := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadRequeued); c != nil {
		queuedTime = c.LastTransitionTime.Time
	}
	return clock.Since(queuedTime)
}

// workloadsWithPodsReadyToEvictedTime is the amount of time it takes a workload's pods running to getting evicted.
// This measures runtime of workloads that do not run to completion (ie are evicted).
func workloadsWithPodsReadyToEvictedTime(wl *kueue.Workload) *time.Duration {
	var podsReady *time.Time
	if c := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadPodsReady); c != nil && c.Status == metav1.ConditionTrue {
		podsReady = &c.LastTransitionTime.Time
	} else {
		return nil
	}

	var evicted *time.Time
	if c := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadEvicted); c != nil && c.Status == metav1.ConditionTrue {
		evicted = &c.LastTransitionTime.Time
	} else {
		return nil
	}

	return ptr.To(evicted.Sub(*podsReady))
}

// BaseSSAWorkload creates a new object based on the input workload that
// only contains the fields necessary to identify the original object.
// The object can be used in as a base for Server-Side-Apply.
func BaseSSAWorkload(w *kueue.Workload, strict bool) *kueue.Workload {
	wlCopy := &kueue.Workload{
		ObjectMeta: metav1.ObjectMeta{
			UID:         w.UID,
			Name:        w.Name,
			Namespace:   w.Namespace,
			Generation:  w.Generation, // Produce a conflict if there was a change in the spec.
			Annotations: maps.Clone(w.Annotations),
			Labels:      maps.Clone(w.Labels),
		},
		TypeMeta: w.TypeMeta,
	}
	if wlCopy.APIVersion == "" {
		wlCopy.APIVersion = kueue.GroupVersion.String()
	}
	if wlCopy.Kind == "" {
		wlCopy.Kind = "Workload"
	}
	if strict {
		wlCopy.ResourceVersion = w.ResourceVersion
	}
	return wlCopy
}

// SetQuotaReservation records that quota has been reserved for the given Workload
// in the specified ClusterQueue and updates the Workload status accordingly.
//
// Effects:
//   - Sets w.Status.Admission to the provided admission.
//   - Adds or updates a Condition of type kueue.WorkloadQuotaReserved with
//     Status=True, Reason="QuotaReserved", Message="Quota reserved in ClusterQueue <name>",
//     and ObservedGeneration set to w.Generation. The message is truncated via
//     api.TruncateConditionMessage.
//   - Resets any active "evicted" and "preempted" conditions by invoking
//     resetActiveCondition for kueue.WorkloadEvicted and kueue.WorkloadPreempted.
func SetQuotaReservation(w *kueue.Workload, admission *kueue.Admission, clock clock.Clock) bool {
	w.Status.Admission = admission

	reason := "QuotaReserved"

	changed := apimeta.SetStatusCondition(&w.Status.Conditions, metav1.Condition{
		Type:               kueue.WorkloadQuotaReserved,
		Status:             metav1.ConditionTrue,
		Reason:             reason,
		Message:            api.TruncateConditionMessage(fmt.Sprintf("Quota reserved in ClusterQueue %s", admission.ClusterQueue)),
		ObservedGeneration: w.Generation,
		LastTransitionTime: metav1.NewTime(clock.Now()),
	})

	if resetActiveCondition(&w.Status.Conditions, w.Generation, kueue.WorkloadEvicted, reason, clock) {
		changed = true
	}

	if resetActiveCondition(&w.Status.Conditions, w.Generation, kueue.WorkloadPreempted, reason, clock) {
		changed = true
	}

	return changed
}

func resetActiveCondition(conds *[]metav1.Condition, gen int64, condType, reason string, clock clock.Clock) bool {
	prev := apimeta.FindStatusCondition(*conds, condType)
	// Ignore not found or inactive condition.
	if prev == nil || prev.Status != metav1.ConditionTrue {
		return false
	}
	return apimeta.SetStatusCondition(conds, metav1.Condition{
		Type:               condType,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            api.TruncateConditionMessage("Previously: " + prev.Message),
		ObservedGeneration: gen,
		LastTransitionTime: metav1.NewTime(clock.Now()),
	})
}

// NeedsSecondPass checks if the second pass of scheduling is needed for the
// workload.
func NeedsSecondPass(w *kueue.Workload) bool {
	if IsFinished(w) || IsEvicted(w) || !HasQuotaReservation(w) {
		return false
	}
	return needsSecondPassForDelayedAssignment(w) || needsSecondPassAfterNodeFailure(w)
}

func needsSecondPassForDelayedAssignment(w *kueue.Workload) bool {
	return len(w.Status.AdmissionChecks) > 0 &&
		HasAllChecksReady(w) &&
		HasTopologyAssignmentsPending(w) &&
		!IsAdmitted(w)
}

func needsSecondPassAfterNodeFailure(w *kueue.Workload) bool {
	return HasTopologyAssignmentWithUnhealthyNode(w)
}

// HasTopologyAssignmentsPending checks if the workload contains any
// PodSetAssignment with the DelayedTopologyRequest=Pending.
func HasTopologyAssignmentsPending(w *kueue.Workload) bool {
	if w.Status.Admission == nil {
		return false
	}
	for _, psa := range w.Status.Admission.PodSetAssignments {
		if psa.TopologyAssignment == nil &&
			utilptr.ValEquals(psa.DelayedTopologyRequest, kueue.DelayedTopologyRequestStatePending) {
			return true
		}
	}
	return false
}

func SetPreemptedCondition(w *kueue.Workload, now time.Time, reason string, message string) {
	condition := metav1.Condition{
		Type:               kueue.WorkloadPreempted,
		Status:             metav1.ConditionTrue,
		LastTransitionTime: metav1.NewTime(now),
		Reason:             reason,
		Message:            api.TruncateConditionMessage(message),
	}
	apimeta.SetStatusCondition(&w.Status.Conditions, condition)
}

func SetDeactivationTarget(w *kueue.Workload, reason string, message string) bool {
	condition := metav1.Condition{
		Type:               kueue.WorkloadDeactivationTarget,
		Status:             metav1.ConditionTrue,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: w.Generation,
	}
	return apimeta.SetStatusCondition(&w.Status.Conditions, condition)
}

func SetEvictedCondition(w *kueue.Workload, now time.Time, reason string, message string) bool {
	condition := metav1.Condition{
		Type:               kueue.WorkloadEvicted,
		Status:             metav1.ConditionTrue,
		LastTransitionTime: metav1.NewTime(now),
		Reason:             reason,
		Message:            api.TruncateConditionMessage(message),
		ObservedGeneration: w.Generation,
	}
	return apimeta.SetStatusCondition(&w.Status.Conditions, condition)
}

// PropagateResourceRequests synchronizes w.Status.ResourceRequests to
// with info.TotalRequests if the feature gate is enabled and returns true if w was updated
func PropagateResourceRequests(w *kueue.Workload, info *Info) bool {
	if len(w.Status.ResourceRequests) == len(info.TotalRequests) {
		match := true
		for idx := range w.Status.ResourceRequests {
			if w.Status.ResourceRequests[idx].Name != info.TotalRequests[idx].Name ||
				!equality.Semantic.DeepEqual(w.Status.ResourceRequests[idx].Resources, info.TotalRequests[idx].Requests.ToResourceList()) {
				match = false
				break
			}
		}
		if match {
			return false
		}
	}

	res := make([]kueue.PodSetRequest, len(info.TotalRequests))
	for idx := range info.TotalRequests {
		res[idx].Name = info.TotalRequests[idx].Name
		res[idx].Resources = info.TotalRequests[idx].Requests.ToResourceList()
	}
	w.Status.ResourceRequests = res
	return true
}

// admissionStatusPatch creates a new object based on the input workload that contains
// the admission and related conditions. The object can be used in Server-Side-Apply.
// If strict is true, resourceVersion will be part of the patch.
func admissionStatusPatch(w *kueue.Workload, wlCopy *kueue.Workload) {
	wlCopy.Status.Admission = w.Status.Admission.DeepCopy()
	wlCopy.Status.RequeueState = w.Status.RequeueState.DeepCopy()
	if wlCopy.Status.Admission != nil {
		// Clear ResourceRequests; Assignment.PodSetAssignment[].ResourceUsage supercedes it
		wlCopy.Status.ResourceRequests = []kueue.PodSetRequest{}
	} else {
		for _, rr := range w.Status.ResourceRequests {
			wlCopy.Status.ResourceRequests = append(wlCopy.Status.ResourceRequests, *rr.DeepCopy())
		}
	}
	for _, conditionName := range admissionManagedConditions {
		if existing := apimeta.FindStatusCondition(w.Status.Conditions, conditionName); existing != nil {
			wlCopy.Status.Conditions = append(wlCopy.Status.Conditions, *existing.DeepCopy())
		}
	}
	wlCopy.Status.AccumulatedPastExecutionTimeSeconds = w.Status.AccumulatedPastExecutionTimeSeconds
	if w.Status.SchedulingStats != nil {
		if wlCopy.Status.SchedulingStats == nil {
			wlCopy.Status.SchedulingStats = &kueue.SchedulingStats{}
		}
		wlCopy.Status.SchedulingStats.Evictions = append(wlCopy.Status.SchedulingStats.Evictions, w.Status.SchedulingStats.Evictions...)
	}
	wlCopy.Status.ClusterName = w.Status.ClusterName
	wlCopy.Status.NominatedClusterNames = w.Status.NominatedClusterNames
	wlCopy.Status.UnhealthyNodes = w.Status.UnhealthyNodes
}

func admissionChecksStatusPatch(w *kueue.Workload, wlCopy *kueue.Workload, c clock.Clock) {
	if wlCopy.Status.AdmissionChecks == nil && w.Status.AdmissionChecks != nil {
		wlCopy.Status.AdmissionChecks = make([]kueue.AdmissionCheckState, 0)
	}
	for _, ac := range w.Status.AdmissionChecks {
		SetAdmissionCheckState(&wlCopy.Status.AdmissionChecks, ac, c)
	}
}

// ApplyAdmissionStatus updated all the admission related status fields of a workload with SSA.
// If strict is true, resourceVersion will be part of the patch, make this call fail if Workload
// was changed.
func ApplyAdmissionStatus(ctx context.Context, c client.Client, w *kueue.Workload, strict bool, clk clock.Clock) error {
	wlCopy := PrepareWorkloadPatch(w, strict, clk)
	return ApplyAdmissionStatusPatch(ctx, c, wlCopy)
}

func PrepareWorkloadPatch(w *kueue.Workload, strict bool, clk clock.Clock) *kueue.Workload {
	wlCopy := BaseSSAWorkload(w, strict)
	admissionStatusPatch(w, wlCopy)
	admissionChecksStatusPatch(w, wlCopy, clk)
	return wlCopy
}

// ApplyAdmissionStatusPatch applies the patch of admission related status fields of a workload with SSA.
func ApplyAdmissionStatusPatch(ctx context.Context, c client.Client, patch *kueue.Workload) error {
	return c.Status().Patch(ctx, patch, client.Apply, client.FieldOwner(constants.AdmissionName), client.ForceOwnership)
}

// PatchAdmissionStatusOption defines a functional option for customizing PatchAdmissionStatusOptions.
// It follows the functional options pattern, allowing callers to configure
// patch behavior at call sites without directly manipulating PatchAdmissionStatusOptions.
type PatchAdmissionStatusOption func(*PatchAdmissionStatusOptions)

// PatchAdmissionStatusOptions contains configuration parameters that control how patches
// are generated and applied.
//
// Fields:
//   - StrictPatch: Controls whether ResourceVersion should always be cleared
//     from the "original" object to ensure its inclusion in the generated
//     patch. Defaults to true. Setting StrictPatch=false preserves the current
//     ResourceVersion.
//   - StrictApply: When using Patch Apply, controls whether ResourceVersion should always be cleared
//     from the "original" object to ensure its inclusion in the generated
//     patch. Defaults to true. Setting StrictPatch=false preserves the current
//     ResourceVersion.
//
// Typically, PatchAdmissionStatusOptions are constructed via DefaultPatchAdmissionStatusOptions and
// modified using PatchAdmissionStatusOption functions (e.g., WithLoose).
type PatchAdmissionStatusOptions struct {
	StrictPatch bool
	StrictApply bool
}

// DefaultPatchAdmissionStatusOptions returns a new PatchAdmissionStatusOptions instance configured with
// default settings.
//
// By default, StrictPatch and StrictApply is set to true, meaning ResourceVersion is cleared
// from the original object so it will always be included in the generated
// patch. This ensures stricter version handling during patch application.
func DefaultPatchAdmissionStatusOptions() *PatchAdmissionStatusOptions {
	return &PatchAdmissionStatusOptions{
		StrictPatch: true, // default is strict
		StrictApply: true, // default is strict
	}
}

// WithLoose returns a PatchAdmissionStatusOption that resets both the StrictPatch and StrictApply
// fields on PatchAdmissionStatusOptions.
//
// By default, StrictPatch and StrictApply are true. In strict mode, generated patches enforce stricter
// behavior by clearing the ResourceVersion field from the "original" object.
// This ensures that the ResourceVersion is always included in the generated patch
// and taken into account during patch application.
//
// Example:
//
//	patch := clientutil.Patch(ctx, c, w, clk, func() (bool, error) {
//	    return updateFn(obj), nil
//	}, WithLoose()) // disables strict mode
func WithLoose() PatchAdmissionStatusOption {
	return func(o *PatchAdmissionStatusOptions) {
		o.StrictPatch = false
		o.StrictApply = false
	}
}

// WithLooseOnApply returns a PatchAdmissionStatusOption that resets the StrictApply field on PatchAdmissionStatusOptions.
//
// When using Patch Apply, setting StrictApply to false enforces looser
// version handling only for Patch Apply.
// This is useful when the update function already handles version conflicts
// and we want to avoid additional conflicts during Patch Apply.
//
// Example:
//	patch := clientutil.Patch(ctx, c, w, clk, func() (bool, error) {
//	    return updateFn(obj), nil
//	}, WithLooseOnApply()) // disables strict mode for Patch Apply

func WithLooseOnApply() PatchAdmissionStatusOption {
	return func(o *PatchAdmissionStatusOptions) {
		o.StrictApply = false
	}
}

// PatchAdmissionStatus updates the admission status of a workload.
// If the WorkloadRequestUseMergePatch feature is enabled, it uses a Merge Patch with update function.
// Otherwise, it runs the update function and, if updated, applies the SSA Patch status.
func PatchAdmissionStatus(ctx context.Context, c client.Client, w *kueue.Workload, clk clock.Clock, update func() (*kueue.Workload, bool, error), options ...PatchAdmissionStatusOption) error {
	opts := DefaultPatchAdmissionStatusOptions()
	for _, opt := range options {
		opt(opts)
	}

	if features.Enabled(features.WorkloadRequestUseMergePatch) {
		var patchOptions []clientutil.PatchOption
		if !opts.StrictPatch {
			patchOptions = append(patchOptions, clientutil.WithLoose())
		}
		return clientutil.PatchStatus(ctx, c, w, func() (client.Object, bool, error) {
			return update()
		}, patchOptions...)
	}
	wPatched, updated, err := update()
	if err != nil || !updated {
		return err
	}

	return ApplyAdmissionStatus(ctx, c, wPatched, opts.StrictApply, clk)
}

type Ordering struct {
	PodsReadyRequeuingTimestamp config.RequeuingTimestamp
}

// GetQueueOrderTimestamp return the timestamp to be used by the scheduler. It could
// be the workload creation time or the last time a PodsReady timeout has occurred.
func (o Ordering) GetQueueOrderTimestamp(w *kueue.Workload) *metav1.Time {
	if o.PodsReadyRequeuingTimestamp == config.EvictionTimestamp {
		if evictedCond, evictedByTimeout := IsEvictedByPodsReadyTimeout(w); evictedByTimeout {
			return &evictedCond.LastTransitionTime
		}
	}
	if evictedCond, evictedByCheck := IsEvictedByAdmissionCheck(w); evictedByCheck {
		return &evictedCond.LastTransitionTime
	}
	if !features.Enabled(features.PrioritySortingWithinCohort) {
		if preemptedCond := apimeta.FindStatusCondition(w.Status.Conditions, kueue.WorkloadPreempted); preemptedCond != nil &&
			preemptedCond.Status == metav1.ConditionTrue &&
			preemptedCond.Reason == kueue.InCohortReclaimWhileBorrowingReason {
			// We add an epsilon to make sure the timestamp of the preempted
			// workload is strictly greater that the preemptor's
			return &metav1.Time{Time: preemptedCond.LastTransitionTime.Add(time.Millisecond)}
		}
	}
	return &w.CreationTimestamp
}

// HasQuotaReservation checks if workload is admitted based on conditions
func HasQuotaReservation(w *kueue.Workload) bool {
	return apimeta.IsStatusConditionTrue(w.Status.Conditions, kueue.WorkloadQuotaReserved)
}

// UpdateReclaimablePods updates the ReclaimablePods list for the workload with SSA.
func UpdateReclaimablePods(ctx context.Context, c client.Client, w *kueue.Workload, reclaimablePods []kueue.ReclaimablePod) error {
	patch := BaseSSAWorkload(w, false)
	patch.Status.ReclaimablePods = reclaimablePods
	return c.Status().Patch(ctx, patch, client.Apply, client.FieldOwner(constants.ReclaimablePodsMgr))
}

// ReclaimablePodsAreEqual checks if two Reclaimable pods are semantically equal
// having the same length and all keys have the same value.
func ReclaimablePodsAreEqual(a, b []kueue.ReclaimablePod) bool {
	if len(a) != len(b) {
		return false
	}
	ma := utilslices.ToMap(a, func(i int) (kueue.PodSetReference, int32) { return a[i].Name, a[i].Count })
	mb := utilslices.ToMap(b, func(i int) (kueue.PodSetReference, int32) { return b[i].Name, b[i].Count })
	return maps.Equal(ma, mb)
}

// IsAdmitted returns true if the workload is admitted.
func IsAdmitted(w *kueue.Workload) bool {
	return apimeta.IsStatusConditionTrue(w.Status.Conditions, kueue.WorkloadAdmitted)
}

// IsFinished returns true if the workload is finished.
func IsFinished(w *kueue.Workload) bool {
	return apimeta.IsStatusConditionTrue(w.Status.Conditions, kueue.WorkloadFinished)
}

// IsActive returns true if the workload is active.
func IsActive(w *kueue.Workload) bool {
	return ptr.Deref(w.Spec.Active, true)
}

// HasDRA returns true if the workload has DRA resources (ResourceClaims or ResourceClaimTemplates).
func HasDRA(w *kueue.Workload) bool {
	return HasResourceClaim(w) || HasResourceClaimTemplates(w)
}

// HasResourceClaimTemplates returns true if the workload has ResourceClaimTemplates.
func HasResourceClaimTemplates(w *kueue.Workload) bool {
	for _, ps := range w.Spec.PodSets {
		for _, prc := range ps.Template.Spec.ResourceClaims {
			if prc.ResourceClaimTemplateName != nil {
				return true
			}
		}
	}
	return false
}

// HasResourceClaim returns true if the workload has ResourceClaims.
func HasResourceClaim(w *kueue.Workload) bool {
	for _, ps := range w.Spec.PodSets {
		for _, prc := range ps.Template.Spec.ResourceClaims {
			if prc.ResourceClaimName != nil {
				return true
			}
		}
	}
	return false
}

// IsEvictedByDeactivation returns true if the workload is evicted by deactivation.
func IsEvictedByDeactivation(w *kueue.Workload) bool {
	cond := apimeta.FindStatusCondition(w.Status.Conditions, kueue.WorkloadEvicted)
	return cond != nil && cond.Status == metav1.ConditionTrue && strings.HasPrefix(cond.Reason, kueue.WorkloadDeactivated)
}

// IsEvictedDueToDeactivationByKueue returns true if the workload is evicted by deactivation by kueue.
func IsEvictedDueToDeactivationByKueue(w *kueue.Workload) bool {
	cond := apimeta.FindStatusCondition(w.Status.Conditions, kueue.WorkloadEvicted)
	return cond != nil && cond.Status == metav1.ConditionTrue &&
		strings.HasPrefix(cond.Reason, ReasonWithCause(kueue.WorkloadDeactivated, ""))
}

func IsEvictedByPodsReadyTimeout(w *kueue.Workload) (*metav1.Condition, bool) {
	cond := apimeta.FindStatusCondition(w.Status.Conditions, kueue.WorkloadEvicted)
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != kueue.WorkloadEvictedByPodsReadyTimeout {
		return nil, false
	}
	return cond, true
}

func IsEvictedByAdmissionCheck(w *kueue.Workload) (*metav1.Condition, bool) {
	cond := apimeta.FindStatusCondition(w.Status.Conditions, kueue.WorkloadEvicted)
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != kueue.WorkloadEvictedByAdmissionCheck {
		return nil, false
	}
	return cond, true
}

func IsEvicted(w *kueue.Workload) bool {
	return apimeta.IsStatusConditionPresentAndEqual(w.Status.Conditions, kueue.WorkloadEvicted, metav1.ConditionTrue)
}

// HasConditionWithTypeAndReason checks if there is a condition in Workload's status
// with exactly the same Type, Status and Reason
func HasConditionWithTypeAndReason(w *kueue.Workload, cond *metav1.Condition) bool {
	for _, statusCond := range w.Status.Conditions {
		if statusCond.Type == cond.Type && statusCond.Reason == cond.Reason &&
			statusCond.Status == cond.Status {
			return true
		}
	}
	return false
}

func HasUnhealthyNodes(w *kueue.Workload) bool {
	return w != nil && len(w.Status.UnhealthyNodes) > 0
}

func HasUnhealthyNode(w *kueue.Workload, nodeName string) bool {
	return slices.ContainsFunc(w.Status.UnhealthyNodes, func(node kueue.UnhealthyNode) bool {
		return node.Name == nodeName
	})
}

func UnhealthyNodeNames(w *kueue.Workload) []string {
	return utilslices.Map(w.Status.UnhealthyNodes, func(unhealthyNode *kueue.UnhealthyNode) string {
		return unhealthyNode.Name
	})
}

func HasTopologyAssignmentWithUnhealthyNode(w *kueue.Workload) bool {
	if !HasUnhealthyNodes(w) || !IsAdmitted(w) {
		return false
	}
	for _, psa := range w.Status.Admission.PodSetAssignments {
		if psa.TopologyAssignment == nil {
			continue
		}
		for _, domain := range psa.TopologyAssignment.Domains {
			if HasUnhealthyNode(w, domain.Values[len(domain.Values)-1]) {
				return true
			}
		}
	}
	return false
}

func CreatePodsReadyCondition(status metav1.ConditionStatus, reason, message string, clock clock.Clock) metav1.Condition {
	return metav1.Condition{
		Type:               kueue.WorkloadPodsReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.NewTime(clock.Now()),
		// ObservedGeneration is added via workload.UpdateStatus
	}
}

func RemoveFinalizer(ctx context.Context, c client.Client, wl *kueue.Workload) error {
	if controllerutil.RemoveFinalizer(wl, kueue.ResourceInUseFinalizerName) {
		return c.Update(ctx, wl)
	}
	return nil
}

// AdmissionChecksForWorkload returns AdmissionChecks that should be assigned to a specific Workload based on
// ClusterQueue configuration and ResourceFlavors
func AdmissionChecksForWorkload(log logr.Logger, wl *kueue.Workload, admissionChecks map[kueue.AdmissionCheckReference]sets.Set[kueue.ResourceFlavorReference], allFlavors sets.Set[kueue.ResourceFlavorReference]) sets.Set[kueue.AdmissionCheckReference] {
	// If all admissionChecks should be run for all flavors we don't need to wait for Workload's Admission to be set.
	// This is also the case if admissionChecks are specified with ClusterQueue.Spec.AdmissionChecks instead of
	// ClusterQueue.Spec.AdmissionCheckStrategy
	hasAllFlavors := true
	for _, flavors := range admissionChecks {
		if !flavors.Equal(allFlavors) {
			hasAllFlavors = false
		}
	}
	if hasAllFlavors {
		return sets.New(slices.Collect(maps.Keys(admissionChecks))...)
	}

	// Kueue sets AdmissionChecks first based on ClusterQueue configuration and at this point Workload has no
	// ResourceFlavors assigned, so we cannot match AdmissionChecks to ResourceFlavor.
	// After Quota is reserved, another reconciliation happens and we can match AdmissionChecks to ResourceFlavors
	if wl.Status.Admission == nil {
		log.V(2).Info("Workload has no Admission", "Workload", klog.KObj(wl))
		return nil
	}

	var assignedFlavors []kueue.ResourceFlavorReference
	for _, podSet := range wl.Status.Admission.PodSetAssignments {
		for _, flavor := range podSet.Flavors {
			assignedFlavors = append(assignedFlavors, flavor)
		}
	}

	acNames := sets.New[kueue.AdmissionCheckReference]()
	for acName, flavors := range admissionChecks {
		for _, fName := range assignedFlavors {
			if flavors.Has(fName) {
				acNames.Insert(acName)
			}
		}
	}
	return acNames
}

type EvictOption func(*EvictOptions)

type EvictOptions struct {
	CustomPrepare func() (*kueue.Workload, error)
}

func DefaultEvictOptions() *EvictOptions {
	return &EvictOptions{
		CustomPrepare: nil,
	}
}

func WithCustomPrepare(customPrepare func() (*kueue.Workload, error)) EvictOption {
	return func(o *EvictOptions) {
		if customPrepare != nil {
			o.CustomPrepare = customPrepare
		}
	}
}

func Evict(ctx context.Context, c client.Client, recorder record.EventRecorder, wl *kueue.Workload, reason, msg string, underlyingCause kueue.EvictionUnderlyingCause, clock clock.Clock, options ...EvictOption) error {
	opts := DefaultEvictOptions()
	for _, opt := range options {
		opt(opts)
	}

	// if there is no customPrepare, wl and wlOrig are equal
	wlOrig := wl.DeepCopy()
	if opts.CustomPrepare != nil {
		var err error
		wl, err = opts.CustomPrepare()
		if err != nil || wl == nil {
			return err
		}
	}

	evictionReason := reason
	if reason == kueue.WorkloadDeactivated && underlyingCause != "" {
		evictionReason = ReasonWithCause(evictionReason, string(underlyingCause))
	}
	prepareForEviction(wl, clock.Now(), evictionReason, msg)
	reportWorkloadEvictedOnce := workloadEvictionStateInc(wl, reason, underlyingCause)
	if err := PatchAdmissionStatus(ctx, c, wlOrig, clock, func() (*kueue.Workload, bool, error) {
		return wl, true, nil
	}); err != nil {
		return err
	}
	if wlOrig.Status.Admission == nil {
		// This is an extra safeguard for access to `wl.Status.Admission`.
		// This function is expected to be called only for workload which have
		// Admission.
		log := log.FromContext(ctx)
		log.V(3).Info("WARNING: unexpected eviction of workload without status.Admission", "workload", klog.KObj(wl))
		return nil
	}
	reportEvictedWorkload(recorder, wl, wl.Status.Admission.ClusterQueue, reason, msg, underlyingCause)
	if reportWorkloadEvictedOnce {
		metrics.ReportEvictedWorkloadsOnce(wl.Status.Admission.ClusterQueue, reason, string(underlyingCause), PriorityClassName(wl))
	}
	return nil
}

func PriorityClassName(wl *kueue.Workload) string {
	if wl.Spec.PriorityClassRef != nil {
		return wl.Spec.PriorityClassRef.Name
	}
	return ""
}

func IsWorkloadPriorityClass(wl *kueue.Workload) bool {
	return wl.Spec.PriorityClassRef != nil &&
		wl.Spec.PriorityClassRef.Kind == kueue.WorkloadPriorityClassKind &&
		wl.Spec.PriorityClassRef.Group == kueue.WorkloadPriorityClassGroup
}

func prepareForEviction(w *kueue.Workload, now time.Time, reason, message string) {
	SetEvictedCondition(w, now, reason, message)
	resetClusterNomination(w)
	resetChecksOnEviction(w, now)
	resetUnhealthyNodes(w)
}

func resetClusterNomination(w *kueue.Workload) {
	w.Status.ClusterName = nil
	w.Status.NominatedClusterNames = nil
}

func resetUnhealthyNodes(w *kueue.Workload) {
	w.Status.UnhealthyNodes = nil
}

func reportEvictedWorkload(recorder record.EventRecorder, wl *kueue.Workload, cqName kueue.ClusterQueueReference, reason, message string, underlyingCause kueue.EvictionUnderlyingCause) {
	priorityClassName := PriorityClassName(wl)
	metrics.ReportEvictedWorkloads(cqName, reason, string(underlyingCause), priorityClassName)
	if podsReadyToEvictionTime := workloadsWithPodsReadyToEvictedTime(wl); podsReadyToEvictionTime != nil {
		metrics.PodsReadyToEvictedTimeSeconds.WithLabelValues(string(cqName), reason, string(underlyingCause)).Observe(podsReadyToEvictionTime.Seconds())
	}
	if features.Enabled(features.LocalQueueMetrics) {
		metrics.ReportLocalQueueEvictedWorkloads(
			metrics.LQRefFromWorkload(wl),
			reason,
			string(underlyingCause),
			priorityClassName,
		)
	}
	eventReason := ReasonWithCause(kueue.WorkloadEvicted, reason)
	if reason == kueue.WorkloadDeactivated && underlyingCause != "" {
		eventReason = ReasonWithCause(eventReason, string(underlyingCause))
	}
	recorder.Event(wl, corev1.EventTypeNormal, eventReason, message)
}

func ReportPreemption(preemptingCqName kueue.ClusterQueueReference, preemptingReason string, targetCqName kueue.ClusterQueueReference) {
	metrics.ReportPreemption(preemptingCqName, preemptingReason, targetCqName)
}

func References(wls []*Info) []klog.ObjectRef {
	if len(wls) == 0 {
		return nil
	}
	keys := make([]klog.ObjectRef, len(wls))
	for i, wl := range wls {
		keys[i] = klog.KObj(wl.Obj)
	}
	return keys
}

func workloadEvictionStateInc(wl *kueue.Workload, reason string, underlyingCause kueue.EvictionUnderlyingCause) bool {
	evictionState := findSchedulingStatsEvictionByReason(wl, reason, underlyingCause)
	if evictionState == nil {
		evictionState = &kueue.WorkloadSchedulingStatsEviction{
			Reason:          reason,
			UnderlyingCause: underlyingCause,
		}
	}
	report := evictionState.Count == 0
	evictionState.Count++
	setSchedulingStatsEviction(wl, *evictionState)
	return report
}

func findSchedulingStatsEvictionByReason(wl *kueue.Workload, reason string, underlyingCause kueue.EvictionUnderlyingCause) *kueue.WorkloadSchedulingStatsEviction {
	if wl.Status.SchedulingStats != nil {
		for i := range wl.Status.SchedulingStats.Evictions {
			if wl.Status.SchedulingStats.Evictions[i].Reason == reason && wl.Status.SchedulingStats.Evictions[i].UnderlyingCause == underlyingCause {
				return &wl.Status.SchedulingStats.Evictions[i]
			}
		}
	}
	return nil
}

func setSchedulingStatsEviction(wl *kueue.Workload, newEvictionState kueue.WorkloadSchedulingStatsEviction) bool {
	if wl.Status.SchedulingStats == nil {
		wl.Status.SchedulingStats = &kueue.SchedulingStats{}
	}
	evictionState := findSchedulingStatsEvictionByReason(wl, newEvictionState.Reason, newEvictionState.UnderlyingCause)
	if evictionState == nil {
		wl.Status.SchedulingStats.Evictions = append(wl.Status.SchedulingStats.Evictions, newEvictionState)
		return true
	}
	if evictionState.Count != newEvictionState.Count {
		evictionState.Count = newEvictionState.Count
		return true
	}
	return false
}

func ReasonWithCause(reason, underlyingCause string) string {
	return fmt.Sprintf("%sDueTo%s", reason, underlyingCause)
}

// ClusterName returns the name of the remote cluster where the original workload
// was scheduled in a multikueue context. If the corresponding annotation is not set,
// it returns an empty string.
func ClusterName(wl *kueue.Workload) string {
	return ptr.Deref(wl.Status.ClusterName, "")
}

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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/record"
	resourcehelpers "k8s.io/component-helpers/resource"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
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

type InfoOptions struct {
	excludedResourcePrefixes []string
	resourceTransformations  map[corev1.ResourceName]*config.ResourceTransformation
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

// IsRequestingTAS returns information if the workload is requesting TAS
func (i *Info) IsRequestingTAS() bool {
	return slices.ContainsFunc(i.Obj.Spec.PodSets,
		func(ps kueue.PodSet) bool {
			return ps.TopologyRequest != nil
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
	backoff := &wait.Backoff{
		Duration: time.Duration(backoffBaseSeconds) * time.Second,
		Factor:   2,
		Jitter:   0.0001,
		Steps:    int(requeuingCount),
	}
	var waitDuration time.Duration
	for backoff.Steps > 0 {
		waitDuration = min(backoff.Step(), time.Duration(backoffMaxSeconds)*time.Second)
	}

	wl.Status.RequeueState.RequeueAt = ptr.To(metav1.NewTime(clock.Now().Add(waitDuration)))
	wl.Status.RequeueState.Count = &requeuingCount
}

// SetRequeuedCondition sets the WorkloadRequeued condition to true
func SetRequeuedCondition(wl *kueue.Workload, reason, message string, status bool) {
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
	apimeta.SetStatusCondition(&wl.Status.Conditions, condition)
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

// SetQuotaReservation applies the provided admission to the workload.
// The WorkloadAdmitted and WorkloadEvicted are added or updated if necessary.
func SetQuotaReservation(w *kueue.Workload, admission *kueue.Admission, clock clock.Clock) {
	w.Status.Admission = admission
	message := fmt.Sprintf("Quota reserved in ClusterQueue %s", w.Status.Admission.ClusterQueue)
	admittedCond := metav1.Condition{
		Type:               kueue.WorkloadQuotaReserved,
		Status:             metav1.ConditionTrue,
		Reason:             "QuotaReserved",
		Message:            api.TruncateConditionMessage(message),
		ObservedGeneration: w.Generation,
	}
	apimeta.SetStatusCondition(&w.Status.Conditions, admittedCond)

	// reset Evicted condition if present.
	if evictedCond := apimeta.FindStatusCondition(w.Status.Conditions, kueue.WorkloadEvicted); evictedCond != nil {
		evictedCond.Status = metav1.ConditionFalse
		evictedCond.Reason = "QuotaReserved"
		evictedCond.Message = api.TruncateConditionMessage("Previously: " + evictedCond.Message)
		evictedCond.LastTransitionTime = metav1.NewTime(clock.Now())
	}
	// reset Preempted condition if present.
	if preemptedCond := apimeta.FindStatusCondition(w.Status.Conditions, kueue.WorkloadPreempted); preemptedCond != nil {
		preemptedCond.Status = metav1.ConditionFalse
		preemptedCond.Reason = "QuotaReserved"
		preemptedCond.Message = api.TruncateConditionMessage("Previously: " + preemptedCond.Message)
		preemptedCond.LastTransitionTime = metav1.NewTime(clock.Now())
	}
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
	return HasTopologyAssignmentWithNodeToReplace(w)
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

func SetPreemptedCondition(w *kueue.Workload, reason string, message string) {
	condition := metav1.Condition{
		Type:    kueue.WorkloadPreempted,
		Status:  metav1.ConditionTrue,
		Reason:  reason,
		Message: api.TruncateConditionMessage(message),
	}
	apimeta.SetStatusCondition(&w.Status.Conditions, condition)
}

func SetDeactivationTarget(w *kueue.Workload, reason string, message string) {
	condition := metav1.Condition{
		Type:               kueue.WorkloadDeactivationTarget,
		Status:             metav1.ConditionTrue,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: w.Generation,
	}
	apimeta.SetStatusCondition(&w.Status.Conditions, condition)
}

func SetEvictedCondition(w *kueue.Workload, reason string, message string) {
	condition := metav1.Condition{
		Type:               kueue.WorkloadEvicted,
		Status:             metav1.ConditionTrue,
		Reason:             reason,
		Message:            api.TruncateConditionMessage(message),
		ObservedGeneration: w.Generation,
	}
	apimeta.SetStatusCondition(&w.Status.Conditions, condition)
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
	wlCopy.Status.AccumulatedPastExexcutionTimeSeconds = w.Status.AccumulatedPastExexcutionTimeSeconds
	if w.Status.SchedulingStats != nil {
		if wlCopy.Status.SchedulingStats == nil {
			wlCopy.Status.SchedulingStats = &kueue.SchedulingStats{}
		}
		wlCopy.Status.SchedulingStats.Evictions = append(wlCopy.Status.SchedulingStats.Evictions, w.Status.SchedulingStats.Evictions...)
	}
	wlCopy.Status.ClusterName = w.Status.ClusterName
	wlCopy.Status.NominatedClusterNames = w.Status.NominatedClusterNames
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

// IsEvictedByDeactivation returns true if the workload is evicted by deactivation.
func IsEvictedByDeactivation(w *kueue.Workload) bool {
	cond := apimeta.FindStatusCondition(w.Status.Conditions, kueue.WorkloadEvicted)
	return cond != nil && cond.Status == metav1.ConditionTrue && strings.HasPrefix(cond.Reason, kueue.WorkloadDeactivated)
}

// IsEvictedDueToDeactivationByKueue returns true if the workload is evicted by deactivation by kueue.
func IsEvictedDueToDeactivationByKueue(w *kueue.Workload) bool {
	cond := apimeta.FindStatusCondition(w.Status.Conditions, kueue.WorkloadEvicted)
	return cond != nil && cond.Status == metav1.ConditionTrue &&
		strings.HasPrefix(cond.Reason, fmt.Sprintf("%sDueTo", kueue.WorkloadDeactivated))
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

func HasNodeToReplace(w *kueue.Workload) bool {
	if w == nil {
		return false
	}
	annotations := w.GetAnnotations()
	_, found := annotations[kueuealpha.NodeToReplaceAnnotation]
	return found
}

func NodeToReplace(w *kueue.Workload) string {
	if !HasNodeToReplace(w) {
		return ""
	}
	annotations := w.GetAnnotations()
	return annotations[kueuealpha.NodeToReplaceAnnotation]
}

func HasTopologyAssignmentWithNodeToReplace(w *kueue.Workload) bool {
	if !HasNodeToReplace(w) || !IsAdmitted(w) {
		return false
	}
	annotations := w.GetAnnotations()
	failedNode := annotations[kueuealpha.NodeToReplaceAnnotation]
	for _, psa := range w.Status.Admission.PodSetAssignments {
		if psa.TopologyAssignment == nil {
			continue
		}
		for _, domain := range psa.TopologyAssignment.Domains {
			if domain.Values[len(domain.Values)-1] == failedNode {
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

func RemoveAnnotation(ctx context.Context, cl client.Client, wl *kueue.Workload, annotation string) error {
	wlKey := types.NamespacedName{Name: wl.Name, Namespace: wl.Namespace}
	var wlToPatch kueue.Workload
	if err := cl.Get(ctx, wlKey, &wlToPatch); err != nil {
		return err
	}
	return clientutil.Patch(ctx, cl, &wlToPatch, false, func() (bool, error) {
		annotations := wlToPatch.GetAnnotations()
		delete(annotations, annotation)
		wlToPatch.SetAnnotations(annotations)
		return true, nil
	})
}

// AdmissionChecksForWorkload returns AdmissionChecks that should be assigned to a specific Workload based on
// ClusterQueue configuration and ResourceFlavors
func AdmissionChecksForWorkload(log logr.Logger, wl *kueue.Workload, admissionChecks map[kueue.AdmissionCheckReference]sets.Set[kueue.ResourceFlavorReference]) sets.Set[kueue.AdmissionCheckReference] {
	// If all admissionChecks should be run for all flavors we don't need to wait for Workload's Admission to be set.
	// This is also the case if admissionChecks are specified with ClusterQueue.Spec.AdmissionChecks instead of
	// ClusterQueue.Spec.AdmissionCheckStrategy
	allFlavors := true
	for _, flavors := range admissionChecks {
		if len(flavors) != 0 {
			allFlavors = false
		}
	}
	if allFlavors {
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
		if len(flavors) == 0 {
			acNames.Insert(acName)
			continue
		}
		for _, fName := range assignedFlavors {
			if flavors.Has(fName) {
				acNames.Insert(acName)
			}
		}
	}
	return acNames
}

func Evict(ctx context.Context, c client.Client, recorder record.EventRecorder, wl *kueue.Workload, reason, underlyingCause, msg string, clock clock.Clock) error {
	prepareForEviction(wl, clock.Now(), reason, msg)
	reportWorkloadEvictedOnce := workloadEvictionStateInc(wl, reason, underlyingCause)
	if err := ApplyAdmissionStatus(ctx, c, wl, true, clock); err != nil {
		return err
	}
	if wl.Status.Admission == nil {
		// This is an extra safeguard for access to `wl.Status.Admission`.
		// This function is expected to be called only for workload which have
		// Admission.
		log := log.FromContext(ctx)
		log.V(3).Info("WARNING: unexpected eviction of workload without status.Admission", "workload", klog.KObj(wl))
		return nil
	}
	reportEvictedWorkload(recorder, wl, wl.Status.Admission.ClusterQueue, reason, msg)
	if reportWorkloadEvictedOnce {
		metrics.ReportEvictedWorkloadsOnce(wl.Status.Admission.ClusterQueue, reason, underlyingCause)
	}
	return nil
}

func prepareForEviction(w *kueue.Workload, now time.Time, reason, message string) {
	SetEvictedCondition(w, reason, message)
	resetClusterNomination(w)
	resetChecksOnEviction(w, now)
}

func resetClusterNomination(w *kueue.Workload) {
	w.Status.ClusterName = nil
	w.Status.NominatedClusterNames = nil
}

func reportEvictedWorkload(recorder record.EventRecorder, wl *kueue.Workload, cqName kueue.ClusterQueueReference, reason, message string) {
	metrics.ReportEvictedWorkloads(cqName, reason)
	if podsReadyToEvictionTime := workloadsWithPodsReadyToEvictedTime(wl); podsReadyToEvictionTime != nil {
		metrics.PodsReadyToEvictedTimeSeconds.WithLabelValues(string(cqName), reason).Observe(podsReadyToEvictionTime.Seconds())
	}
	if features.Enabled(features.LocalQueueMetrics) {
		metrics.ReportLocalQueueEvictedWorkloads(metrics.LQRefFromWorkload(wl), reason)
	}
	recorder.Event(wl, corev1.EventTypeNormal, fmt.Sprintf("%sDueTo%s", kueue.WorkloadEvicted, reason), message)
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

func workloadEvictionStateInc(wl *kueue.Workload, reason, underlyingCause string) bool {
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

func findSchedulingStatsEvictionByReason(wl *kueue.Workload, reason, underlyingCause string) *kueue.WorkloadSchedulingStatsEviction {
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

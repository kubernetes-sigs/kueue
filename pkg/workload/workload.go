/*
Copyright 2022 The Kubernetes Authors.

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
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/util/api"
	"sigs.k8s.io/kueue/pkg/util/limitrange"
	utilmaps "sigs.k8s.io/kueue/pkg/util/maps"
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
	ClusterQueue   string
	LastAssignment *AssignmentClusterQueueState
}

type PodSetResources struct {
	Name string
	// Requests incorporates the requests from all pods in the podset.
	Requests resources.Requests
	// Count indicates how many pods are in the podset.
	Count int32

	// TopologyRequest specifies the requests for TAS
	TopologyRequest *TopologyRequest

	// Flavors are populated when the Workload is assigned.
	Flavors map[corev1.ResourceName]kueue.ResourceFlavorReference
}

type TopologyRequest struct {
	Levels         []string
	DomainRequests []TopologyDomainRequests
}

type TopologyDomainRequests struct {
	Values   []string
	Requests resources.Requests
}

func (psr *PodSetResources) ScaledTo(newCount int32) *PodSetResources {
	if psr.TopologyRequest != nil {
		return psr
	}
	ret := &PodSetResources{
		Name:     psr.Name,
		Requests: maps.Clone(psr.Requests),
		Count:    psr.Count,
		Flavors:  maps.Clone(psr.Flavors),
	}

	if psr.Count != 0 && psr.Count != newCount {
		scaleDown(ret.Requests, int64(ret.Count))
		scaleUp(ret.Requests, int64(newCount))
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
		info.ClusterQueue = string(w.Status.Admission.ClusterQueue)
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

// TASUsage returns topology usage requested by the Workload
func (i *Info) TASUsage() []TopologyDomainRequests {
	if !features.Enabled(features.TopologyAwareScheduling) {
		return nil
	}
	result := make([]TopologyDomainRequests, 0)
	for _, ps := range i.TotalRequests {
		if ps.TopologyRequest != nil {
			result = append(result, ps.TopologyRequest.DomainRequests...)
		}
	}
	return result
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

func Key(w *kueue.Workload) string {
	return fmt.Sprintf("%s/%s", w.Namespace, w.Name)
}

func QueueKey(w *kueue.Workload) string {
	return fmt.Sprintf("%s/%s", w.Namespace, w.Spec.QueueName)
}

func reclaimableCounts(wl *kueue.Workload) map[string]int32 {
	return utilslices.ToMap(wl.Status.ReclaimablePods, func(i int) (string, int32) {
		return wl.Status.ReclaimablePods[i].Name, wl.Status.ReclaimablePods[i].Count
	})
}

func podSetsCounts(wl *kueue.Workload) map[string]int32 {
	return utilslices.ToMap(wl.Spec.PodSets, func(i int) (string, int32) {
		return wl.Spec.PodSets[i].Name, wl.Spec.PodSets[i].Count
	})
}

func podSetsCountsAfterReclaim(wl *kueue.Workload) map[string]int32 {
	totalCounts := podSetsCounts(wl)
	reclaimCounts := reclaimableCounts(wl)
	for podSetName := range totalCounts {
		if rc, found := reclaimCounts[podSetName]; found {
			totalCounts[podSetName] -= rc
		}
	}
	return totalCounts
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
		specRequests := limitrange.TotalRequests(&ps.Template.Spec)
		effectiveRequests := dropExcludedResources(specRequests, info.excludedResourcePrefixes)
		if features.Enabled(features.ConfigurableResourceTransformations) {
			effectiveRequests = applyResourceTransformations(effectiveRequests, info.resourceTransformations)
		}
		setRes.Requests = resources.NewRequests(effectiveRequests)
		scaleUp(setRes.Requests, int64(count))
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
				domainRequests := setRes.Requests.Clone()
				scaleDown(domainRequests, int64(setRes.Count))
				scaleUp(domainRequests, int64(domain.Count))
				setRes.TopologyRequest.DomainRequests = append(setRes.TopologyRequest.DomainRequests, TopologyDomainRequests{
					Values:   domain.Values,
					Requests: domainRequests,
				})
			}
		}

		// If countAfterReclaim is lower then the admission count indicates that
		// additional pods are marked as reclaimable, and the consumption should be scaled down.
		if countAfterReclaim := currentCounts[psa.Name]; countAfterReclaim < setRes.Count {
			scaleDown(setRes.Requests, int64(setRes.Count))
			scaleUp(setRes.Requests, int64(countAfterReclaim))
			setRes.Count = countAfterReclaim
		}
		// Otherwise if countAfterReclaim is higher it means that the podSet was partially admitted
		// and the count should be preserved.
		res = append(res, setRes)
	}
	return res
}

func scaleUp(r resources.Requests, f int64) {
	for name := range r {
		r[name] *= f
	}
}

func scaleDown(r resources.Requests, f int64) {
	for name := range r {
		r[name] /= f
	}
}

// UpdateStatus updates the condition of a workload with ssa,
// fieldManager being set to managerPrefix + "-" + conditionType
func UpdateStatus(ctx context.Context,
	c client.Client,
	wl *kueue.Workload,
	conditionType string,
	conditionStatus metav1.ConditionStatus,
	reason, message string,
	managerPrefix string) error {
	now := metav1.Now()
	condition := metav1.Condition{
		Type:               conditionType,
		Status:             conditionStatus,
		LastTransitionTime: now,
		Reason:             reason,
		Message:            api.TruncateConditionMessage(message),
		ObservedGeneration: wl.Generation,
	}

	newWl := BaseSSAWorkload(wl)
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

func QueuedWaitTime(wl *kueue.Workload) time.Duration {
	queuedTime := wl.CreationTimestamp.Time
	if c := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadRequeued); c != nil {
		queuedTime = c.LastTransitionTime.Time
	}
	return time.Since(queuedTime)
}

// BaseSSAWorkload creates a new object based on the input workload that
// only contains the fields necessary to identify the original object.
// The object can be used in as a base for Server-Side-Apply.
func BaseSSAWorkload(w *kueue.Workload) *kueue.Workload {
	wlCopy := &kueue.Workload{
		ObjectMeta: metav1.ObjectMeta{
			UID:        w.UID,
			Name:       w.Name,
			Namespace:  w.Namespace,
			Generation: w.Generation, // Produce a conflict if there was a change in the spec.
		},
		TypeMeta: w.TypeMeta,
	}
	if wlCopy.APIVersion == "" {
		wlCopy.APIVersion = kueue.GroupVersion.String()
	}
	if wlCopy.Kind == "" {
		wlCopy.Kind = "Workload"
	}
	return wlCopy
}

// SetQuotaReservation applies the provided admission to the workload.
// The WorkloadAdmitted and WorkloadEvicted are added or updated if necessary.
func SetQuotaReservation(w *kueue.Workload, admission *kueue.Admission) {
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
		evictedCond.LastTransitionTime = metav1.Now()
	}
	// reset Preempted condition if present.
	if preemptedCond := apimeta.FindStatusCondition(w.Status.Conditions, kueue.WorkloadPreempted); preemptedCond != nil {
		preemptedCond.Status = metav1.ConditionFalse
		preemptedCond.Reason = "QuotaReserved"
		preemptedCond.Message = api.TruncateConditionMessage("Previously: " + preemptedCond.Message)
		preemptedCond.LastTransitionTime = metav1.Now()
	}
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
	if !features.Enabled(features.WorkloadResourceRequestsSummary) {
		return false
	}
	if len(w.Status.ResourceRequests) == len(info.TotalRequests) {
		match := true
		for idx := range w.Status.ResourceRequests {
			if w.Status.ResourceRequests[idx].Name != info.TotalRequests[idx].Name ||
				!maps.Equal(w.Status.ResourceRequests[idx].Resources, info.TotalRequests[idx].Requests.ToResourceList()) {
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

// AdmissionStatusPatch creates a new object based on the input workload that contains
// the admission and related conditions. The object can be used in Server-Side-Apply.
// If strict is true, resourceVersion will be part of the patch.
func AdmissionStatusPatch(w *kueue.Workload, wlCopy *kueue.Workload, strict bool) {
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
	if strict {
		wlCopy.ResourceVersion = w.ResourceVersion
	}
	wlCopy.Status.AccumulatedPastExexcutionTimeSeconds = w.Status.AccumulatedPastExexcutionTimeSeconds
}

func AdmissionChecksStatusPatch(w *kueue.Workload, wlCopy *kueue.Workload) {
	if wlCopy.Status.AdmissionChecks == nil && w.Status.AdmissionChecks != nil {
		wlCopy.Status.AdmissionChecks = make([]kueue.AdmissionCheckState, 0)
	}
	for _, ac := range w.Status.AdmissionChecks {
		SetAdmissionCheckState(&wlCopy.Status.AdmissionChecks, ac)
	}
}

// ApplyAdmissionStatus updated all the admission related status fields of a workload with SSA.
// If strict is true, resourceVersion will be part of the patch, make this call fail if Workload
// was changed.
func ApplyAdmissionStatus(ctx context.Context, c client.Client, w *kueue.Workload, strict bool) error {
	wlCopy := BaseSSAWorkload(w)
	AdmissionStatusPatch(w, wlCopy, strict)
	AdmissionChecksStatusPatch(w, wlCopy)
	return ApplyAdmissionStatusPatch(ctx, c, wlCopy)
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
	patch := BaseSSAWorkload(w)
	patch.Status.ReclaimablePods = reclaimablePods
	return c.Status().Patch(ctx, patch, client.Apply, client.FieldOwner(constants.ReclaimablePodsMgr))
}

// ReclaimablePodsAreEqual checks if two Reclaimable pods are semantically equal
// having the same length and all keys have the same value.
func ReclaimablePodsAreEqual(a, b []kueue.ReclaimablePod) bool {
	if len(a) != len(b) {
		return false
	}
	ma := utilslices.ToMap(a, func(i int) (string, int32) { return a[i].Name, a[i].Count })
	mb := utilslices.ToMap(b, func(i int) (string, int32) { return b[i].Name, b[i].Count })
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
	return cond != nil && cond.Status == metav1.ConditionTrue && cond.Reason == kueue.WorkloadEvictedByDeactivation
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

func RemoveFinalizer(ctx context.Context, c client.Client, wl *kueue.Workload) error {
	if controllerutil.RemoveFinalizer(wl, kueue.ResourceInUseFinalizerName) {
		return c.Update(ctx, wl)
	}
	return nil
}

// AdmissionChecksForWorkload returns AdmissionChecks that should be assigned to a specific Workload based on
// ClusterQueue configuration and ResourceFlavors
func AdmissionChecksForWorkload(log logr.Logger, wl *kueue.Workload, admissionChecks map[string]sets.Set[kueue.ResourceFlavorReference]) sets.Set[string] {
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
		return sets.New(utilmaps.Keys(admissionChecks)...)
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

	acNames := sets.New[string]()
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

func ReportEvictedWorkload(recorder record.EventRecorder, wl *kueue.Workload, cqName, reason, message string) {
	metrics.ReportEvictedWorkloads(cqName, reason)
	recorder.Event(wl, corev1.EventTypeNormal, fmt.Sprintf("%sDueTo%s", kueue.WorkloadEvicted, reason), message)
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

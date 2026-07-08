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
	"crypto/sha256"
	"encoding/json"
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
	resourcehelpers "k8s.io/component-helpers/resource"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	queueafs "sigs.k8s.io/kueue/pkg/cache/queue/afs"
	"sigs.k8s.io/kueue/pkg/constants"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	afs "sigs.k8s.io/kueue/pkg/util/admissionfairsharing"
	"sigs.k8s.io/kueue/pkg/util/api"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	"sigs.k8s.io/kueue/pkg/util/podset"
	"sigs.k8s.io/kueue/pkg/util/priority"
	utilptr "sigs.k8s.io/kueue/pkg/util/ptr"
	"sigs.k8s.io/kueue/pkg/util/queue"
	utilslices "sigs.k8s.io/kueue/pkg/util/slices"
	"sigs.k8s.io/kueue/pkg/util/tas"
	"sigs.k8s.io/kueue/pkg/util/wait"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
	workloadfinish "sigs.k8s.io/kueue/pkg/workload/finish"
	workloadpatching "sigs.k8s.io/kueue/pkg/workload/patching"
)

const (
	StatusPending       = "pending"
	StatusQuotaReserved = "quotaReserved"
	StatusAdmitted      = "admitted"
	StatusFinished      = "finished"

	// SchedulingHashUnknown indicates the scheduling hash could not be computed.
	SchedulingHashUnknown EquivalenceHash = "unknown"
)

// Reference is the full reference to Workload formed as <namespace>/< kueue.WorkloadName >.
type Reference string

// EquivalenceHash represents a scheduling equivalence class key used to track
// EquivalenceHash is a hash of a workload's scheduling-relevant shape.
// Workloads with the same hash have identical scheduling properties
// and will receive the same FlavorAssigner result given the same cluster state.
type EquivalenceHash string

func NewReference(namespace, name string) Reference {
	return Reference(namespace + "/" + name)
}

func Status(w *kueue.Workload) string {
	if workloadfinish.IsFinished(w) {
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

// FromQuotaReservedOrAdmittedToPending reports a transition from quota-reserved or admitted to pending.
func FromQuotaReservedOrAdmittedToPending(prevStatus, newStatus string) bool {
	return (prevStatus == StatusQuotaReserved || prevStatus == StatusAdmitted) && newStatus == StatusPending
}

type AssignmentClusterQueueState struct {
	LastTriedFlavorIdx     []map[corev1.ResourceName]int
	ClusterQueueGeneration int64
}

type dra struct {
	preprocessedDRAResources  map[kueue.PodSetReference]corev1.ResourceList
	replacedExtendedResources map[kueue.PodSetReference]sets.Set[corev1.ResourceName]
}

type InfoOptions struct {
	excludedResourcePrefixes []string
	resourceTransformations  map[corev1.ResourceName]*config.ResourceTransformation
	preserveTotalRequests    bool
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

// WithPreserveTotalRequests prevents Update from rebuilding TotalRequests.
// Used when requeuing DRA-backed workloads whose TotalRequests were
// preprocessed by the workload controller and must survive the requeue.
func WithPreserveTotalRequests() InfoOption {
	return func(o *InfoOptions) {
		o.preserveTotalRequests = true
	}
}

// WithPreprocessedDRAResources provides DRA resources to add and extended resources to remove.
func WithPreprocessedDRAResources(
	draResources map[kueue.PodSetReference]corev1.ResourceList,
	replacedExtendedResources map[kueue.PodSetReference]sets.Set[corev1.ResourceName],
) InfoOption {
	return func(o *InfoOptions) {
		o.dra = dra{
			preprocessedDRAResources:  draResources,
			replacedExtendedResources: replacedExtendedResources,
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

type ResourceToFlavor map[corev1.ResourceName]kueue.ResourceFlavorReference

type PodSetResourcesToFlavors map[kueue.PodSetReference]ResourceToFlavor

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

	// LastEvaluatedGeneration stores the Obj.Generation at the time the scheduler
	// popped this workload for evaluation. Used by PushOrUpdate to detect spec
	// changes masked by RequeueWorkload's info.Update.
	LastEvaluatedGeneration int64

	// SchedulingHash identifies the workload's scheduling equivalence class.
	// Workloads with the same hash have identical scheduling-relevant shape
	// and will receive the same FlavorAssigner result given the same cluster state.
	SchedulingHash EquivalenceHash

	// NominationMapping is the mapping of PodSets resources and their flavors
	// based on the nomination phase.
	NominationMapping PodSetResourcesToFlavors
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
	info := &Info{}
	info.Update(klog.Background(), w, opts...)
	return info
}

// UpdateSchedulingHash computes and sets the scheduling hash using the
// provided contextual logger. Called internally by Update.
func (i *Info) UpdateSchedulingHash(log logr.Logger) {
	i.SchedulingHash = computeSchedulingHash(log, i.Obj, i.TotalRequests)
}

// Update refreshes the object reference, rebuilds TotalRequests, and
// recomputes the scheduling hash. Pass WithPreserveTotalRequests to skip
// the TotalRequests rebuild (e.g., to retain DRA preprocessing on requeue).
func (i *Info) Update(log logr.Logger, wl *kueue.Workload, opts ...InfoOption) {
	i.Obj = wl
	i.rebuildTotalRequests(opts...)
	i.UpdateSchedulingHash(log)
}

// rebuildTotalRequests refreshes ClusterQueue and recomputes TotalRequests
// from the current workload state. When WithPreserveTotalRequests is set,
// only TotalRequests recomputation is skipped.
func (i *Info) rebuildTotalRequests(opts ...InfoOption) {
	options := defaultOptions
	for _, opt := range opts {
		opt(&options)
	}
	admitted := i.Obj.Status.Admission != nil
	if admitted {
		i.ClusterQueue = i.Obj.Status.Admission.ClusterQueue
	} else {
		i.ClusterQueue = ""
	}
	if !options.preserveTotalRequests {
		if admitted {
			i.TotalRequests = totalRequestsFromAdmission(i.Obj)
		} else {
			i.TotalRequests = totalRequestsFromPodSets(i.Obj, &options)
		}
	}
}

// computeSchedulingHash returns a deterministic hash of the workload's
// scheduling-relevant shape: effective workload priority, pod spec (via
// SpecShape), effective count, minCount, and topologyRequest per PodSet.
func computeSchedulingHash(log logr.Logger, wl *kueue.Workload, totalRequests []PodSetResources) EquivalenceHash {
	if !features.Enabled(features.SchedulingEquivalenceHashing) {
		return SchedulingHashUnknown
	}
	effectivePriority := priority.EffectivePriority(log, wl)
	podSetShapes := make([]map[string]any, 0, len(wl.Spec.PodSets))
	for i, ps := range wl.Spec.PodSets {
		effectiveCount := ps.Count
		var effectiveRequests resources.Requests
		if i < len(totalRequests) {
			effectiveCount = totalRequests[i].Count
			effectiveRequests = totalRequests[i].Requests
		}
		podSetShapes = append(podSetShapes, map[string]any{
			"name":            ps.Name,
			"spec":            utilpod.SpecShape(&ps.Template.Spec),
			"count":           effectiveCount,
			"requests":        effectiveRequests,
			"minCount":        ps.MinCount,
			"topologyRequest": ps.TopologyRequest,
		})
	}
	shape := map[string]any{
		"podSets":  podSetShapes,
		"priority": effectivePriority,
	}
	if features.Enabled(features.ConcurrentAdmission) {
		if val, ok := wl.GetAnnotations()[controllerconstants.WorkloadAllowedResourceFlavorAnnotation]; ok {
			shape["allowedFlavors"] = val
		}
	}
	shapeJSON, err := json.Marshal(shape)
	if err != nil {
		log.Error(err, "Failed to compute scheduling hash", "workload", klog.KObj(wl))
		return SchedulingHashUnknown
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(shapeJSON))[:16]
	if logV := log.V(5); logV.Enabled() {
		logV.Info("Computed scheduling hash", "workload", klog.KObj(wl), "hash", hash, "shapeJSON", string(shapeJSON))
	}
	return EquivalenceHash(hash)
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
			total[resources.FlavorResource{Flavor: flv, Resource: res}] = total[resources.FlavorResource{Flavor: flv, Resource: res}].AddInt64(q)
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

func (i *Info) CalcLocalQueueFSUsage(
	ctx context.Context,
	c client.Client,
	resWeights map[corev1.ResourceName]float64,
	afsEntryPenalties *queueafs.AfsEntryPenalties,
	afsConsumedResources *queueafs.AfsConsumedResources,
) (float64, error) {
	lqKey := queue.KeyFromWorkload(i.Obj)

	consumed := corev1.ResourceList{}
	if afsConsumedResources != nil {
		entry, found := afsConsumedResources.Get(lqKey)
		if found {
			consumed = entry.Resources
		}
	}

	penalty := corev1.ResourceList{}
	if afsEntryPenalties != nil {
		penalty = afsEntryPenalties.Peek(lqKey)
	}

	var lq kueue.LocalQueue
	lqObjKey := client.ObjectKey{Namespace: i.Obj.Namespace, Name: string(i.Obj.Spec.QueueName)}
	if err := c.Get(ctx, lqObjKey, &lq); err != nil {
		return 0, err
	}
	lqWeight := afs.LQWeightAsFloat64(&lq)
	return afs.CalculateUsage(consumed, penalty, lqWeight, resWeights), nil
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
			return tr != nil &&
				(tr.Unconstrained != nil || tr.Required != nil || tr.Preferred != nil || tr.PodSetSliceRequiredTopology != nil || tr.PodSetSliceSize != nil || len(tr.PodsetSliceRequiredTopologyConstraints) > 0)
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
			// If MultiplyBy is specified, multiply the input quantity by
			// the value of the resource specified in MultiplyBy.
			if mapping.MultiplyBy != "" {
				if q, ok := input[mapping.MultiplyBy]; ok {
					inputQuantity.Mul(q.Value())
				}
			}

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
		effectiveRequests = applyResourceTransformations(effectiveRequests, info.resourceTransformations)
		setRes.Requests = resources.NewRequests(effectiveRequests)
		if features.Enabled(features.KueueDRAIntegration) && info.preprocessedDRAResources != nil {
			// First, remove extended resources that were converted to DRA logical resources
			if replacedRes, exists := info.replacedExtendedResources[ps.Name]; exists {
				for extRes := range replacedRes {
					delete(setRes.Requests, extRes)
				}
			}
			// Then, add the DRA logical resources
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
			singlePodRequests := setRes.SinglePodRequests()
			if ps := podset.FindPodSetByName(wl.Spec.PodSets, psa.Name); ps != nil {
				singlePodRequests = resources.NewRequestsFromPodSpec(&ps.Template.Spec)
			}
			for req := range tas.InternalSeqFrom(psa.TopologyAssignment) {
				setRes.TopologyRequest.DomainRequests = append(setRes.TopologyRequest.DomainRequests, TopologyDomainRequests{
					Values:            req.Values,
					SinglePodRequests: singlePodRequests,
					Count:             req.Count,
				})
			}
		}
		if features.Enabled(features.TopologyAwareScheduling) && psa.DelayedTopologyRequest != nil {
			setRes.DelayedTopologyRequest = new(*psa.DelayedTopologyRequest)
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

// SetConditionAndUpdate sets (or replaces) a single condition in a Workload's status.
//
// Behaviour depends on the feature gate WorkloadRequestUseMergePatch:
//
//   - Enabled → uses merge-patch via PatchStatus (preserves other conditions,
//     safe for concurrent controllers).
//
//   - Disabled → uses server-side apply with field manager
//     "<managerPrefix>-<conditionType>" (legacy path; only the written condition
//     is managed by this controller).
//
// The condition gets:
//   - ObservedGeneration = wl.Generation
//   - LastTransitionTime = clock.Now()
//   - Message truncated to the allowed length
func SetConditionAndUpdate(ctx context.Context,
	c client.Client,
	wl *kueue.Workload,
	conditionType string,
	conditionStatus metav1.ConditionStatus,
	reason, message string,
	managerPrefix string,
	clock clock.Clock,
) error {
	condition := metav1.Condition{
		Type:               conditionType,
		Status:             conditionStatus,
		ObservedGeneration: wl.Generation,
		LastTransitionTime: metav1.NewTime(clock.Now()),
		Reason:             reason,
		Message:            api.TruncateConditionMessage(message),
	}
	return workloadpatching.PatchStatus(ctx, c, wl, client.FieldOwner(managerPrefix+"-"+condition.Type), func(wl *kueue.Workload) (bool, error) {
		return apimeta.SetStatusCondition(&wl.Status.Conditions, condition), nil
	})
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

	_ = SetRequeueState(wl, metav1.NewTime(clock.Now().Add(waitDuration)), true)
}

// SetRequeueState sets the status.requeueState field with the given timeout
// if it's greater than the existing value.
// It will return true if the workload was mutated.
func SetRequeueState(wl *kueue.Workload, waitUntil metav1.Time, incrementCount bool) bool {
	if wl.Status.RequeueState == nil {
		wl.Status.RequeueState = &kueue.RequeueState{}
	}

	// The requeue state is shared between multiple components,
	// so we have to ensure that we don't overwrite a future requeue.
	var updated bool
	currentRequeueAt := ptr.Deref(wl.Status.RequeueState.RequeueAt, metav1.NewTime(time.Time{}))
	if currentRequeueAt.Before(&waitUntil) && !currentRequeueAt.Equal(&waitUntil) {
		wl.Status.RequeueState.RequeueAt = &waitUntil
		updated = true
	}
	if incrementCount {
		requeuingCount := ptr.Deref(wl.Status.RequeueState.Count, 0) + 1
		wl.Status.RequeueState.Count = &requeuingCount
		updated = true
	}
	return updated
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

	if resetActiveCondition(&w.Status.Conditions, w.Generation, kueue.WorkloadBlockedOnPreemptionGates, reason, clock) {
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
	if workloadfinish.IsFinished(w) || workloadevict.IsEvicted(w) || !HasQuotaReservation(w) || IsOnHold(w) {
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

func SetPreemptedCondition(w *kueue.Workload, now time.Time, reason string, message string) bool {
	condition := metav1.Condition{
		Type:               kueue.WorkloadPreempted,
		Status:             metav1.ConditionTrue,
		LastTransitionTime: metav1.NewTime(now),
		Reason:             reason,
		Message:            api.TruncateConditionMessage(message),
	}
	return apimeta.SetStatusCondition(&w.Status.Conditions, condition)
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

func SetAdmittedCondition(w *kueue.Workload, now time.Time, reason string, message string) bool {
	condition := metav1.Condition{
		Type:               kueue.WorkloadAdmitted,
		Status:             metav1.ConditionTrue,
		LastTransitionTime: metav1.NewTime(now),
		Reason:             reason,
		Message:            api.TruncateConditionMessage(message),
		ObservedGeneration: w.Generation,
	}
	return apimeta.SetStatusCondition(&w.Status.Conditions, condition)
}

func SetBlockedOnPreemptionGatesCondition(w *kueue.Workload, now time.Time, reason string, message string) bool {
	condition := metav1.Condition{
		Type:               kueue.WorkloadBlockedOnPreemptionGates,
		Status:             metav1.ConditionTrue,
		LastTransitionTime: metav1.NewTime(now),
		Reason:             reason,
		Message:            api.TruncateConditionMessage(message),
		ObservedGeneration: w.Generation,
	}
	return apimeta.SetStatusCondition(&w.Status.Conditions, condition)
}

// HasClosedPreemptionGate checks if the workload contains any PreemptionGate
// that is considered closed, preventing it from triggering preemptions.
func HasClosedPreemptionGate(w *kueue.Workload) bool {
	gatePositions := make(map[string]kueue.PreemptionGatePosition)
	for _, gateState := range w.Status.PreemptionGates {
		gatePositions[gateState.Name] = gateState.Position
	}

	return slices.ContainsFunc(w.Spec.PreemptionGates, func(pg kueue.PreemptionGate) bool {
		position, hasPosition := gatePositions[pg.Name]
		// Preemption gates that are present only in `.spec` are considered closed.
		// This ensures that the workload can be created gated atomically.
		if !hasPosition {
			return true
		}
		return position == kueue.PreemptionGatePositionClosed
	})
}

// SetPreemptionGatePosition sets the position of a preemption gate with the given name.
func SetPreemptionGatePosition(w *kueue.Workload, gateName string, gatePosition kueue.PreemptionGatePosition, transitionTime metav1.Time) bool {
	gateIdx := slices.IndexFunc(w.Spec.PreemptionGates, func(gate kueue.PreemptionGate) bool {
		return gate.Name == gateName
	})
	if gateIdx == -1 {
		return false
	}

	gateStateIdx := slices.IndexFunc(w.Status.PreemptionGates, func(gate kueue.PreemptionGateState) bool {
		return gate.Name == gateName
	})

	if gateStateIdx == -1 {
		w.Status.PreemptionGates = append(w.Status.PreemptionGates, kueue.PreemptionGateState{
			Name:               gateName,
			Position:           gatePosition,
			LastTransitionTime: transitionTime,
		})
	} else {
		w.Status.PreemptionGates[gateStateIdx].Position = gatePosition
		w.Status.PreemptionGates[gateStateIdx].LastTransitionTime = transitionTime
	}

	return true
}

// Finds preemption gate with gateName, and returns pointer to that preemption gate state object
func FindPreemptionGate(w *kueue.Workload, gateName string) *kueue.PreemptionGateState {
	idx := slices.IndexFunc(w.Status.PreemptionGates, func(gate kueue.PreemptionGateState) bool {
		return gate.Name == gateName
	})
	if idx == -1 {
		return nil
	}
	return &w.Status.PreemptionGates[idx]
}

// HasOpenPreemptionGate reports whether the named preemption gate is open.
func HasOpenPreemptionGate(w *kueue.Workload, gateName string) bool {
	gate := FindPreemptionGate(w, gateName)
	return gate != nil && gate.Position == kueue.PreemptionGatePositionOpen
}

// OpenPreemptionGate opens the named preemption gate, recording transitionTime
// as the moment it opened. Returns true if opened gate and false if no change
func OpenPreemptionGate(w *kueue.Workload, gateName string, transitionTime metav1.Time) bool {
	return SetPreemptionGatePosition(w, gateName, kueue.PreemptionGatePositionOpen, transitionTime)
}

// EnsurePreemptionGateOnSpec appends the named preemption gate to the workload's
// spec if it is not already present, and returns whether it was added.
func EnsurePreemptionGateOnSpec(w *kueue.Workload, gateName string) bool {
	if slices.ContainsFunc(w.Spec.PreemptionGates, func(g kueue.PreemptionGate) bool {
		return g.Name == gateName
	}) {
		return false
	}
	w.Spec.PreemptionGates = append(w.Spec.PreemptionGates, kueue.PreemptionGate{Name: gateName})
	return true
}

// BlockedOnPreemptionGatesCondition returns kueue.WorkloadBlockedOnPreemptionGates condition type
// if condition status equals metav1.ConditionTrue returns condition otherwise returns nil
func BlockedOnPreemptionGatesCondition(w *kueue.Workload) *metav1.Condition {
	cond := apimeta.FindStatusCondition(w.Status.Conditions, kueue.WorkloadBlockedOnPreemptionGates)
	if cond != nil && cond.Status == metav1.ConditionTrue {
		return cond
	}
	return nil
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

type Ordering struct {
	PodsReadyRequeuingTimestamp config.RequeuingTimestamp
}

// GetQueueOrderTimestamp return the timestamp to be used by the scheduler. It could
// be the workload creation time or the last time a PodsReady timeout has occurred.
func (o Ordering) GetQueueOrderTimestamp(w *kueue.Workload) *metav1.Time {
	if o.PodsReadyRequeuingTimestamp == config.EvictionTimestamp {
		if evictedCond, evictedByTimeout := workloadevict.IsEvictedByPodsReadyTimeout(w); evictedByTimeout {
			return &evictedCond.LastTransitionTime
		}
	}
	if evictedCond, evictedByCheck := workloadevict.IsEvictedByAdmissionCheck(w); evictedByCheck {
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

// EvictionPendingLatency returns cluster queue, eviction reason, and latency from the WorkloadEvicted
// condition's last transition until now when oldWl→newWl is the quota-release eviction→pending
// transition that should record workload_eviction_latency_seconds. Otherwise ok is false and other
// return values are zero.
func EvictionPendingLatency(oldWl, newWl *kueue.Workload, now time.Time) (kueue.ClusterQueueReference, string, time.Duration, bool) {
	if oldWl == nil || newWl == nil {
		return "", "", 0, false
	}
	prevStatus := Status(oldWl)
	newStatus := Status(newWl)
	if !FromQuotaReservedOrAdmittedToPending(prevStatus, newStatus) {
		return "", "", 0, false
	}
	c := apimeta.FindStatusCondition(newWl.Status.Conditions, kueue.WorkloadEvicted)
	if c == nil || c.Status != metav1.ConditionTrue {
		return "", "", 0, false
	}
	if oldWl.Status.Admission == nil {
		return "", "", 0, false
	}
	cq := oldWl.Status.Admission.ClusterQueue
	if cq == "" {
		return "", "", 0, false
	}
	return cq, c.Reason, now.Sub(c.LastTransitionTime.Time), true
}

// UpdateReclaimablePods updates the ReclaimablePods list for the workload with SSA.
func UpdateReclaimablePods(ctx context.Context, c client.Client, wl *kueue.Workload, reclaimablePods []kueue.ReclaimablePod) error {
	return workloadpatching.PatchStatus(ctx, c, wl, constants.ReclaimablePodsMgr, func(wl *kueue.Workload) (bool, error) {
		wl.Status.ReclaimablePods = reclaimablePods
		return true, nil
	})
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

// IsActive returns true if the workload is active.
func IsActive(w *kueue.Workload) bool {
	return ptr.Deref(w.Spec.Active, true)
}

// IsAdmissible returns true if the workload can be added to the queue.
func IsAdmissible(w *kueue.Workload) bool {
	return !HasAdmissionGate(w) && !workloadfinish.IsFinished(w) && IsActive(w) && !HasQuotaReservation(w) && !IsOnHold(w) &&
		!apimeta.IsStatusConditionTrue(w.Status.Conditions, kueue.WorkloadWaitingForReplacementPods)
}

// HasAdmissionGate returns true if the workload has an admission gate annotation and the AdmissionGatedBy feature is on
func HasAdmissionGate(w *kueue.Workload) bool {
	if !features.Enabled(features.AdmissionGatedBy) || w.Annotations == nil {
		return false
	}

	if val, exists := w.Annotations[constants.AdmissionGatedByAnnotation]; exists {
		return val != ""
	}

	return false
}

// HasActiveQuotaReservation returns true if the workload has an active quota
// reservation that should be tracked for ClusterQueue usage. This requires the
// workload to be active, not finished, and holding a quota reservation.
func HasActiveQuotaReservation(w *kueue.Workload) bool {
	return HasQuotaReservation(w) && !workloadfinish.IsFinished(w) && IsActive(w)
}

// IsOnHold returns true when the workload's quota reservation is intentionally
// released and the workload should not be requeued. This is indicated by the
// QuotaReserved condition being False with reason "OnHold".
// Any job integration can put the workload on hold to prevent requeuing
// (e.g., StatefulSet on scale-to-zero).
func IsOnHold(w *kueue.Workload) bool {
	cond := apimeta.FindStatusCondition(w.Status.Conditions, kueue.WorkloadQuotaReserved)
	return cond != nil && cond.Status == metav1.ConditionFalse && cond.Reason == kueue.WorkloadOnHold
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
		for value := range tas.LowestLevelValues(psa.TopologyAssignment) {
			if HasUnhealthyNode(w, value) {
				return true
			}
		}
	}
	return false
}

// IsAdmittedByTAS checks if a workload is admitted by TAS.
func IsAdmittedByTAS(w *kueue.Workload) bool {
	return w.Status.Admission != nil && IsAdmitted(w) &&
		slices.ContainsFunc(w.Status.Admission.PodSetAssignments,
			func(psa kueue.PodSetAssignment) bool {
				return psa.TopologyAssignment != nil
			})
}

// PodSetsOnNode returns the PodSets of a workload that are assigned to a specific node.
func PodSetsOnNode(w *kueue.Workload, nodeName string) []kueue.PodSet {
	if w.Status.Admission == nil {
		return nil
	}
	var result []kueue.PodSet
	for _, psa := range w.Status.Admission.PodSetAssignments {
		if psa.TopologyAssignment == nil || !tas.IsLowestLevelHostname(psa.TopologyAssignment.Levels) {
			continue
		}
		assigned := false
		for val := range tas.LowestLevelValues(psa.TopologyAssignment) {
			if val == nodeName {
				assigned = true
				break
			}
		}
		if assigned {
			if ps := podset.FindPodSetByName(w.Spec.PodSets, psa.Name); ps != nil {
				result = append(result, *ps)
			}
		}
	}
	return result
}

func CreatePodsReadyCondition(status metav1.ConditionStatus, reason, message string, clock clock.Clock) metav1.Condition {
	return metav1.Condition{
		Type:               kueue.WorkloadPodsReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.NewTime(clock.Now()),
		// ObservedGeneration is added via workload.SetConditionAndUpdate
	}
}

func FinalizeOrphanedWorkload(ctx context.Context, c client.Client, clk clock.Clock, wl *kueue.Workload, canFinish bool) error {
	log := ctrl.LoggerFrom(ctx)

	// Only Finish workloads that are not currently being deleted.
	if features.Enabled(features.FinishOrphanedWorkloads) && wl.DeletionTimestamp.IsZero() && canFinish {
		log.V(2).Info("Workload is orphaned; finishing to release quota")
		if err := workloadfinish.Finish(ctx, c, wl, kueue.WorkloadFinishedReasonOwnerNotFound,
			"The workload's owner no longer exists", clk); err != nil {
			if client.IgnoreNotFound(err) != nil {
				log.Error(err, "Failed to finish Workload")
				return err
			}
			return nil
		}
	}

	if err := RemoveFinalizer(ctx, c, wl); err != nil {
		if client.IgnoreNotFound(err) != nil {
			log.Error(err, "Failed to remove finalizer")
			return err
		}
	}

	return nil
}

func RemoveFinalizer(ctx context.Context, c client.Client, wl *kueue.Workload) error {
	if controllerutil.RemoveFinalizer(wl, kueue.ResourceInUseFinalizerName) {
		return c.Update(ctx, wl)
	}
	return nil
}

// Delete removes Kueue's resource-in-use finalizer before deleting the Workload.
// If deletion is already in progress, finalizer removal is enough for Kubernetes
// to continue object cleanup, so Delete returns false without issuing another
// delete request. The returned bool reports whether this call successfully
// requested deletion.
func Delete(ctx context.Context, c client.Client, wl *kueue.Workload) (bool, error) {
	if err := RemoveFinalizer(ctx, c, wl); err != nil {
		return false, err
	}
	if !wl.DeletionTimestamp.IsZero() {
		log.FromContext(ctx).V(3).Info("Skipping workload delete because deletion is already in progress", "workload", klog.KObj(wl))
		return false, nil
	}
	if err := c.Delete(ctx, wl); err != nil {
		return false, err
	}
	return true, nil
}

type flavorSet = sets.Set[kueue.ResourceFlavorReference]

// AdmissionChecksForWorkload returns AdmissionChecks that should be assigned to a specific Workload based on
// ClusterQueue configuration
func AdmissionChecksForWorkload(log logr.Logger, wl *kueue.Workload, cq *kueue.ClusterQueue) sets.Set[kueue.AdmissionCheckReference] {
	allChecks := admissioncheck.NewAdmissionChecks(cq)

	if wl.Status.Admission != nil {
		// If we have an admission we can provide all relevant checks right away.
		// Checks that are defined with an empty list of flavors are considered
		// to apply to all flavors declared for the ClusterQueue.
		// These checks are considered valid by this logic, as intended,
		// because checks with empty OnFlavor lists have their lists populated
		// with all flavors in the CQ when initially processed by Kueue.
		return admissionChecksForAdmission(log, allChecks, *wl.Status.Admission)
	}

	// If no admission is present yet we can only list
	// the checks which apply to all flavors supported by the ClusterQueue
	allFlavors := queue.AllFlavors(cq.Spec.ResourceGroups)
	checksForAllFlavors := filterChecks(allChecks, func(acFlavors flavorSet) bool {
		return acFlavors.IsSuperset(allFlavors)
	})
	log.V(3).Info(
		"Workload has no Admission: assigning only checks that apply to all workloads in the Cluster Queue regardless of flavor",
		"Assigned AdmissionChecks",
		checksForAllFlavors,
	)
	return checksForAllFlavors
}

func admissionChecksForAdmission(_ logr.Logger, acs AdmissionChecks, admission kueue.Admission) sets.Set[kueue.AdmissionCheckReference] {
	admissionFlavors := findAdmissionFlavors(admission)
	return filterChecks(acs, func(acFlavors flavorSet) bool {
		return admissionFlavors.Intersection(acFlavors).Len() > 0
	})
}

func filterChecks(acs AdmissionChecks, fsPredicate func(flavorSet) bool) sets.Set[kueue.AdmissionCheckReference] {
	acNames := sets.New[kueue.AdmissionCheckReference]()
	for acName, acFlavors := range acs {
		if fsPredicate(acFlavors) {
			acNames.Insert(acName)
		}
	}
	return acNames
}

func findAdmissionFlavors(admission kueue.Admission) sets.Set[kueue.ResourceFlavorReference] {
	assignedFlavors := sets.New[kueue.ResourceFlavorReference]()
	for _, podSet := range admission.PodSetAssignments {
		for _, flavor := range podSet.Flavors {
			assignedFlavors.Insert(flavor)
		}
	}
	return assignedFlavors
}

func IsWorkloadPriorityClass(wl *kueue.Workload) bool {
	return wl.Spec.PriorityClassRef != nil &&
		wl.Spec.PriorityClassRef.Kind == kueue.WorkloadPriorityClassKind &&
		wl.Spec.PriorityClassRef.Group == kueue.WorkloadPriorityClassGroup
}

func IsPodPriorityClass(wl *kueue.Workload) bool {
	return wl.Spec.PriorityClassRef != nil &&
		wl.Spec.PriorityClassRef.Kind == kueue.PodPriorityClassKind &&
		wl.Spec.PriorityClassRef.Group == kueue.PodPriorityClassGroup
}

func HasNoPriority(wl *kueue.Workload) bool {
	return wl.Spec.PriorityClassRef == nil
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

// ClusterName returns the name of the remote cluster where the original workload
// was scheduled in a multikueue context. If the corresponding annotation is not set,
// it returns an empty string.
func ClusterName(wl *kueue.Workload) string {
	return ptr.Deref(wl.Status.ClusterName, "")
}

// ShouldSkipClusterNomination returns true if cluster nomination should be
// skipped. This covers the case when eviction is ongoing (ClusterName is still
// assigned while the admission check transitions through Retry), as well as
// any other state where the admission check is not Pending.
// Elastic workloads are exempt from the stale-ClusterName check because they
// intentionally set ClusterName on new slices to target the same worker cluster.
func ShouldSkipClusterNomination(acs *kueue.AdmissionCheckState, wl *kueue.Workload, isElastic bool) bool {
	if acs == nil || acs.State != kueue.CheckStatePending {
		return true
	}
	if isElastic {
		return false
	}
	return wl.Status.ClusterName != nil
}

func PriorityChanged(log logr.Logger, old, new *kueue.Workload) bool {
	// Updates to Pod Priority are not supported.
	if IsPodPriorityClass(old) || !IsWorkloadPriorityClass(new) {
		return false
	}
	// Check if priority class reference changed.
	if workloadpatching.PriorityClassName(new) != "" &&
		workloadpatching.PriorityClassName(old) != workloadpatching.PriorityClassName(new) {
		return true
	}
	// Check if effective priority changed (for WorkloadPriorityClass value updates or priority-boost annotation).
	return priority.EffectivePriority(log, old) != priority.EffectivePriority(log, new)
}

// TASAssignedNodeNames extracts the unique set of node names that a Workload is assigned to.
func TASAssignedNodeNames(wl *kueue.Workload) []string {
	if !IsAdmittedByTAS(wl) {
		return nil
	}

	nodesSet := sets.New[string]()
	for _, psa := range wl.Status.Admission.PodSetAssignments {
		if psa.TopologyAssignment == nil || !tas.IsLowestLevelHostname(psa.TopologyAssignment.Levels) {
			continue
		}
		for domain := range tas.InternalSeqFrom(psa.TopologyAssignment) {
			if len(domain.Values) > 0 && domain.Count > 0 {
				nodesSet.Insert(domain.Values[len(domain.Values)-1])
			}
		}
	}
	return nodesSet.UnsortedList()
}

// IsElasticWorkload returns true if ElasticJobsViaWorkloadSlices feature gate is enabled
// and the given Workload is marked as elastic.
func IsElasticWorkload(wl *kueue.Workload) bool {
	if wl == nil {
		return false
	}
	return features.Enabled(features.ElasticJobsViaWorkloadSlices) && wl.GetAnnotations()[constants.ElasticJobAnnotation] == "true"
}

// UnadmittedWorkloadReasonWithFallback returns the granularReason if the UnadmittedWorkloadsObservability
// feature gate is enabled, otherwise it returns the fallback.
func UnadmittedWorkloadReasonWithFallback(granularReason, fallback string) string {
	if features.Enabled(features.UnadmittedWorkloadsObservability) {
		return granularReason
	}
	return fallback
}

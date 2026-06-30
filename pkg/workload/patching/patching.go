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

package patching

import (
	"context"
	"fmt"
	"maps"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	kueueac "sigs.k8s.io/kueue/client-go/applyconfiguration/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
)

var (
	admissionManagedConditions = []string{
		kueue.WorkloadQuotaReserved,
		kueue.WorkloadBlockedOnPreemptionGates,
		kueue.WorkloadEvicted,
		kueue.WorkloadAdmitted,
		kueue.WorkloadPreempted,
		kueue.WorkloadRequeued,
		kueue.WorkloadDeactivationTarget,
		kueue.WorkloadFinished,
		kueue.WorkloadPodsReady,
	}
)

// baseSSAWorkload creates a new object based on the input workload that
// only contains the fields necessary to identify the original object.
// The object can be used in as a base for Server-Side-Apply.
func baseSSAWorkload(w *kueue.Workload, strict bool) *kueue.Workload {
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

// SetAdmissionCheckState - adds or updates newCheck in the provided checks list.
func SetAdmissionCheckState(checks *[]kueue.AdmissionCheckState, newCheck kueue.AdmissionCheckState, clock clock.Clock) bool {
	if checks == nil {
		return false
	}
	existingCondition := admissioncheck.FindAdmissionCheck(*checks, newCheck.Name)
	if existingCondition == nil {
		if newCheck.LastTransitionTime.IsZero() {
			newCheck.LastTransitionTime = metav1.NewTime(clock.Now())
		}
		*checks = append(*checks, newCheck)
		return true
	}

	if existingCondition.State != newCheck.State {
		existingCondition.State = newCheck.State
		if !newCheck.LastTransitionTime.IsZero() {
			existingCondition.LastTransitionTime = newCheck.LastTransitionTime
		} else {
			existingCondition.LastTransitionTime = metav1.NewTime(clock.Now())
		}
	}
	existingCondition.Message = newCheck.Message
	existingCondition.PodSetUpdates = newCheck.PodSetUpdates
	existingCondition.RequeueAfterSeconds = newCheck.RequeueAfterSeconds
	return true
}

// admissionStatusPatch creates a new object based on the input workload that contains
// the admission and related conditions. The object can be used in Server-Side-Apply.
// If strict is true, resourceVersion will be part of the patch.
func admissionStatusPatch(w *kueue.Workload, wlCopy *kueue.Workload) {
	wlCopy.Status.Admission = w.Status.Admission.DeepCopy()
	// Only include RequeueState in the patch if it has meaningful content.
	if w.Status.RequeueState != nil && (w.Status.RequeueState.Count != nil || w.Status.RequeueState.RequeueAt != nil) {
		wlCopy.Status.RequeueState = w.Status.RequeueState.DeepCopy()
	}
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
	wlCopy.Status.PreemptionGates = w.Status.PreemptionGates
}

func admissionChecksStatusPatch(w *kueue.Workload, wlCopy *kueue.Workload, c clock.Clock) {
	if wlCopy.Status.AdmissionChecks == nil && w.Status.AdmissionChecks != nil {
		wlCopy.Status.AdmissionChecks = make([]kueue.AdmissionCheckState, 0)
	}
	for _, ac := range w.Status.AdmissionChecks {
		SetAdmissionCheckState(&wlCopy.Status.AdmissionChecks, ac, c)
	}
}

func prepareWorkloadPatch(w *kueue.Workload, strict bool, clk clock.Clock) *kueue.Workload {
	wlCopy := baseSSAWorkload(w, strict)
	admissionStatusPatch(w, wlCopy)
	admissionChecksStatusPatch(w, wlCopy, clk)
	return wlCopy
}

type UpdateFunc func(*kueue.Workload) (bool, error)

// PatchStatusOption defines a functional option for customizing PatchStatusOptions.
// It follows the functional options pattern, allowing callers to configure
// patch behavior at call sites without directly manipulating PatchStatusOptions.
type PatchStatusOption func(*patchStatusOptions)

// patchStatusOptions contains configuration parameters that control how patches
// are generated and applied.
//
// Fields:
//   - StrictPatch: Controls whether ResourceVersion should always be cleared
//     from the "original" object to ensure its inclusion in the generated
//     patch. Defaults to true. Setting StrictPatch=false preserves the current
//     ResourceVersion.
//   - StrictApply: When using Patch Apply, controls whether ResourceVersion should always be cleared
//     from the "original" object to ensure its inclusion in the generated
//     patch. Defaults to true. Setting StrictApply=false preserves the current
//     ResourceVersion.
//
// Typically, patchStatusOptions are constructed via default options and
// modified using PatchStatusOption functions (e.g., WithLooseOnApply).
type patchStatusOptions struct {
	StrictPatch             bool
	StrictApply             bool
	RetryOnConflictForPatch bool
	ForceApply              bool
}

// defaultPatchStatusOptions returns a new patchStatusOptions instance configured with default settings.
//
// By default, StrictPatch and StrictApply is set to true, meaning ResourceVersion is cleared
// from the original object so it will always be included in the generated
// patch. This ensures stricter version handling during patch application.
func defaultPatchStatusOptions() *patchStatusOptions {
	return &patchStatusOptions{
		StrictPatch: true, // default is strict
		StrictApply: true, // default is strict
	}
}

// WithLooseOnApply returns a PatchStatusOption that resets the StrictApply field on patchStatusOptions.
//
// When using Patch Apply, setting StrictApply to false enforces looser
// version handling only for Patch Apply.
// This is useful when the update function already handles version conflicts
// and we want to avoid additional conflicts during Patch Apply.
//
// Example:
//
//	err := PatchAdmissionStatus(ctx, c, w, clk, func(wl *kueue.Workload) (bool, error) {
//	    return updateFn(wl), nil
//	}, WithLooseOnApply()) // disables strict mode for Patch Apply
func WithLooseOnApply() PatchStatusOption {
	return func(o *patchStatusOptions) {
		o.StrictApply = false
	}
}

// WithRetryOnConflict configures patchStatusOptions to enable retry logic on conflicts.
// Note: This only works with merge patches.
func WithRetryOnConflict() PatchStatusOption {
	return func(o *patchStatusOptions) {
		o.RetryOnConflictForPatch = true
	}
}

// WithForceApply is a PatchStatusOption that forces the use of the apply patch.
func WithForceApply() PatchStatusOption {
	return func(o *patchStatusOptions) {
		o.ForceApply = true
	}
}

func convertPatchStatusOptions(options []PatchStatusOption) *patchStatusOptions {
	opts := defaultPatchStatusOptions()
	for _, opt := range options {
		opt(opts)
	}
	return opts
}

// baseWorkloadApplyConfiguration creates a new object based on the input workload that
// only contains the fields necessary to identify the original object.
// The object can be used as a base for Server-Side-Apply.
func baseWorkloadApplyConfiguration(w *kueue.Workload, strict bool) *kueueac.WorkloadApplyConfiguration {
	applyConfig := kueueac.Workload(w.Name, w.Namespace).WithUID(w.UID).WithGeneration(w.Generation)

	if strict {
		applyConfig.WithResourceVersion(w.ResourceVersion)
	}

	if len(w.Labels) > 0 {
		applyConfig.WithLabels(w.Labels)
	}
	if len(w.Annotations) > 0 {
		applyConfig.WithAnnotations(w.Annotations)
	}

	return applyConfig
}

func toWorkloadStatusApplyConfiguration(status *kueue.WorkloadStatus) *kueueac.WorkloadStatusApplyConfiguration {
	statusConfig := kueueac.WorkloadStatus()

	if condConfigs := toConditionConfigs(status.Conditions); len(condConfigs) > 0 {
		statusConfig.WithConditions(condConfigs...)
	}
	if status.Admission != nil {
		statusConfig.WithAdmission(toAdmissionConfig(status.Admission))
	}
	if status.RequeueState != nil {
		statusConfig.WithRequeueState(toRequeueStateConfig(status.RequeueState))
	}
	if rpConfigs := toReclaimablePodConfigs(status.ReclaimablePods); len(rpConfigs) > 0 {
		statusConfig.WithReclaimablePods(rpConfigs...)
	}
	if acConfigs := toAdmissionCheckConfigs(status.AdmissionChecks); len(acConfigs) > 0 {
		statusConfig.WithAdmissionChecks(acConfigs...)
	}
	if rrConfigs := toResourceRequestConfigs(status.ResourceRequests); len(rrConfigs) > 0 {
		statusConfig.WithResourceRequests(rrConfigs...)
	}
	if status.AccumulatedPastExecutionTimeSeconds != nil {
		statusConfig.WithAccumulatedPastExecutionTimeSeconds(*status.AccumulatedPastExecutionTimeSeconds)
	}
	if status.SchedulingStats != nil {
		statusConfig.WithSchedulingStats(toSchedulingStatsConfig(status.SchedulingStats))
	}
	if len(status.NominatedClusterNames) > 0 {
		statusConfig.WithNominatedClusterNames(status.NominatedClusterNames...)
	}
	if status.ClusterName != nil {
		statusConfig.WithClusterName(*status.ClusterName)
	}
	if unConfigs := toUnhealthyNodeConfigs(status.UnhealthyNodes); len(unConfigs) > 0 {
		statusConfig.WithUnhealthyNodes(unConfigs...)
	}
	if pgConfigs := toPreemptionGateConfigs(status.PreemptionGates); len(pgConfigs) > 0 {
		statusConfig.WithPreemptionGates(pgConfigs...)
	}

	return statusConfig
}

// toConditionConfigs converts status conditions (listMapKey=type).
func toConditionConfigs(conditions []metav1.Condition) []*metav1ac.ConditionApplyConfiguration {
	var condConfigs []*metav1ac.ConditionApplyConfiguration
	for _, c := range conditions {
		if c.Type == "" {
			continue
		}
		condConfig := metav1ac.Condition().
			WithType(c.Type).
			WithStatus(c.Status).
			WithReason(c.Reason).
			WithMessage(c.Message).
			WithObservedGeneration(c.ObservedGeneration)

		if !c.LastTransitionTime.IsZero() {
			condConfig.WithLastTransitionTime(c.LastTransitionTime)
		}
		condConfigs = append(condConfigs, condConfig)
	}
	return condConfigs
}

func toAdmissionConfig(admission *kueue.Admission) *kueueac.AdmissionApplyConfiguration {
	admConfig := kueueac.Admission()
	if admission.ClusterQueue != "" {
		admConfig.WithClusterQueue(admission.ClusterQueue)
	}
	if psaConfigs := toPodSetAssignmentConfigs(admission.PodSetAssignments); len(psaConfigs) > 0 {
		admConfig.WithPodSetAssignments(psaConfigs...)
	}
	return admConfig
}

// toPodSetAssignmentConfigs converts pod set assignments (listMapKey=name).
func toPodSetAssignmentConfigs(podSetAssignments []kueue.PodSetAssignment) []*kueueac.PodSetAssignmentApplyConfiguration {
	var psaConfigs []*kueueac.PodSetAssignmentApplyConfiguration
	for _, psa := range podSetAssignments {
		if psa.Name == "" {
			continue
		}
		psaConfig := kueueac.PodSetAssignment().WithName(psa.Name)
		if len(psa.Flavors) > 0 {
			psaConfig.WithFlavors(psa.Flavors)
		}
		if len(psa.ResourceUsage) > 0 {
			psaConfig.WithResourceUsage(psa.ResourceUsage)
		}
		if psa.Count != nil {
			psaConfig.WithCount(*psa.Count)
		}
		if psa.TopologyAssignment != nil {
			psaConfig.WithTopologyAssignment(toTopologyAssignmentConfig(psa.TopologyAssignment))
		}
		if psa.DelayedTopologyRequest != nil {
			psaConfig.WithDelayedTopologyRequest(*psa.DelayedTopologyRequest)
		}
		psaConfigs = append(psaConfigs, psaConfig)
	}
	return psaConfigs
}

func toTopologyAssignmentConfig(ta *kueue.TopologyAssignment) *kueueac.TopologyAssignmentApplyConfiguration {
	taConfig := kueueac.TopologyAssignment()
	if len(ta.Levels) > 0 {
		taConfig.WithLevels(ta.Levels...)
	}
	if len(ta.Slices) > 0 {
		taConfig.WithSlices(toTopologyAssignmentSliceConfigs(ta.Slices)...)
	}
	return taConfig
}

func toTopologyAssignmentSliceConfigs(slices []kueue.TopologyAssignmentSlice) []*kueueac.TopologyAssignmentSliceApplyConfiguration {
	var sliceConfigs []*kueueac.TopologyAssignmentSliceApplyConfiguration
	for _, slice := range slices {
		sliceConfig := kueueac.TopologyAssignmentSlice().WithDomainCount(slice.DomainCount)
		if len(slice.ValuesPerLevel) > 0 {
			sliceConfig.WithValuesPerLevel(toSliceLevelValuesConfigs(slice.ValuesPerLevel)...)
		}
		sliceConfig.WithPodCounts(toSlicePodCountsConfig(slice.PodCounts))
		sliceConfigs = append(sliceConfigs, sliceConfig)
	}
	return sliceConfigs
}

func toSliceLevelValuesConfigs(valuesPerLevel []kueue.TopologyAssignmentSliceLevelValues) []*kueueac.TopologyAssignmentSliceLevelValuesApplyConfiguration {
	var vplConfigs []*kueueac.TopologyAssignmentSliceLevelValuesApplyConfiguration
	for _, vpl := range valuesPerLevel {
		vplConfig := kueueac.TopologyAssignmentSliceLevelValues()
		if vpl.Universal != nil {
			vplConfig.WithUniversal(*vpl.Universal)
		}
		if vpl.Individual != nil {
			vplConfig.WithIndividual(toIndividualValuesConfig(vpl.Individual))
		}
		vplConfigs = append(vplConfigs, vplConfig)
	}
	return vplConfigs
}

func toIndividualValuesConfig(individual *kueue.TopologyAssignmentSliceLevelIndividualValues) *kueueac.TopologyAssignmentSliceLevelIndividualValuesApplyConfiguration {
	indConfig := kueueac.TopologyAssignmentSliceLevelIndividualValues()
	if individual.Prefix != nil {
		indConfig.WithPrefix(*individual.Prefix)
	}
	if individual.Suffix != nil {
		indConfig.WithSuffix(*individual.Suffix)
	}
	if len(individual.Roots) > 0 {
		indConfig.WithRoots(individual.Roots...)
	}
	return indConfig
}

func toSlicePodCountsConfig(podCounts kueue.TopologyAssignmentSlicePodCounts) *kueueac.TopologyAssignmentSlicePodCountsApplyConfiguration {
	podCountsConfig := kueueac.TopologyAssignmentSlicePodCounts()
	if podCounts.Universal != nil {
		podCountsConfig.WithUniversal(*podCounts.Universal)
	}
	if len(podCounts.Individual) > 0 {
		podCountsConfig.WithIndividual(podCounts.Individual...)
	}
	return podCountsConfig
}

func toRequeueStateConfig(requeueState *kueue.RequeueState) *kueueac.RequeueStateApplyConfiguration {
	reqConfig := kueueac.RequeueState()
	if requeueState.Count != nil {
		reqConfig.WithCount(*requeueState.Count)
	}
	if requeueState.RequeueAt != nil && !requeueState.RequeueAt.IsZero() {
		reqConfig.WithRequeueAt(*requeueState.RequeueAt)
	}
	return reqConfig
}

// toReclaimablePodConfigs converts reclaimable pods (listMapKey=name).
func toReclaimablePodConfigs(reclaimablePods []kueue.ReclaimablePod) []*kueueac.ReclaimablePodApplyConfiguration {
	var rpConfigs []*kueueac.ReclaimablePodApplyConfiguration
	for _, rp := range reclaimablePods {
		if rp.Name == "" {
			continue
		}
		rpConfigs = append(rpConfigs, kueueac.ReclaimablePod().WithName(rp.Name).WithCount(rp.Count))
	}
	return rpConfigs
}

// toAdmissionCheckConfigs converts admission check states (listMapKey=name).
func toAdmissionCheckConfigs(admissionChecks []kueue.AdmissionCheckState) []*kueueac.AdmissionCheckStateApplyConfiguration {
	var acConfigs []*kueueac.AdmissionCheckStateApplyConfiguration
	for _, ac := range admissionChecks {
		if ac.Name == "" {
			continue
		}
		acConfig := kueueac.AdmissionCheckState().
			WithName(ac.Name).
			WithState(ac.State).
			WithMessage(ac.Message).
			WithLastTransitionTime(ac.LastTransitionTime) // required, always set

		if ac.RequeueAfterSeconds != nil {
			acConfig.WithRequeueAfterSeconds(*ac.RequeueAfterSeconds)
		}
		if ac.RetryCount != nil {
			acConfig.WithRetryCount(*ac.RetryCount)
		}
		if psuConfigs := toPodSetUpdateConfigs(ac.PodSetUpdates); len(psuConfigs) > 0 {
			acConfig.WithPodSetUpdates(psuConfigs...)
		}
		acConfigs = append(acConfigs, acConfig)
	}
	return acConfigs
}

func toPodSetUpdateConfigs(podSetUpdates []kueue.PodSetUpdate) []*kueueac.PodSetUpdateApplyConfiguration {
	var psuConfigs []*kueueac.PodSetUpdateApplyConfiguration
	for _, psu := range podSetUpdates {
		if psu.Name == "" {
			continue
		}
		psuConfig := kueueac.PodSetUpdate().WithName(psu.Name)
		if len(psu.Annotations) > 0 {
			psuConfig.WithAnnotations(psu.Annotations)
		}
		if len(psu.Labels) > 0 {
			psuConfig.WithLabels(psu.Labels)
		}
		if len(psu.NodeSelector) > 0 {
			psuConfig.WithNodeSelector(psu.NodeSelector)
		}
		if tolConfigs := toTolerationConfigs(psu.Tolerations); len(tolConfigs) > 0 {
			psuConfig.WithTolerations(tolConfigs...)
		}
		psuConfigs = append(psuConfigs, psuConfig)
	}
	return psuConfigs
}

func toTolerationConfigs(tolerations []corev1.Toleration) []*corev1ac.TolerationApplyConfiguration {
	var tolConfigs []*corev1ac.TolerationApplyConfiguration
	for _, tol := range tolerations {
		tolConfig := corev1ac.Toleration()
		if tol.Key != "" {
			tolConfig.WithKey(tol.Key)
		}
		if tol.Operator != "" {
			tolConfig.WithOperator(tol.Operator)
		}
		if tol.Value != "" {
			tolConfig.WithValue(tol.Value)
		}
		if tol.Effect != "" {
			tolConfig.WithEffect(tol.Effect)
		}
		if tol.TolerationSeconds != nil {
			tolConfig.WithTolerationSeconds(*tol.TolerationSeconds)
		}
		tolConfigs = append(tolConfigs, tolConfig)
	}
	return tolConfigs
}

// toResourceRequestConfigs converts resource requests (listMapKey=name).
func toResourceRequestConfigs(resourceRequests []kueue.PodSetRequest) []*kueueac.PodSetRequestApplyConfiguration {
	var rrConfigs []*kueueac.PodSetRequestApplyConfiguration
	for _, rr := range resourceRequests {
		if rr.Name == "" {
			continue
		}
		rrConfig := kueueac.PodSetRequest().WithName(rr.Name)
		if len(rr.Resources) > 0 {
			rrConfig.WithResources(rr.Resources)
		}
		rrConfigs = append(rrConfigs, rrConfig)
	}
	return rrConfigs
}

func toSchedulingStatsConfig(schedulingStats *kueue.SchedulingStats) *kueueac.SchedulingStatsApplyConfiguration {
	statsConfig := kueueac.SchedulingStats()
	if len(schedulingStats.Evictions) > 0 {
		var evConfigs []*kueueac.WorkloadSchedulingStatsEvictionApplyConfiguration
		for _, ev := range schedulingStats.Evictions {
			evConfigs = append(evConfigs, kueueac.WorkloadSchedulingStatsEviction().
				WithReason(ev.Reason).
				WithUnderlyingCause(ev.UnderlyingCause).
				WithCount(ev.Count))
		}
		statsConfig.WithEvictions(evConfigs...)
	}
	return statsConfig
}

// toUnhealthyNodeConfigs converts unhealthy nodes (listMapKey=name).
func toUnhealthyNodeConfigs(unhealthyNodes []kueue.UnhealthyNode) []*kueueac.UnhealthyNodeApplyConfiguration {
	var unConfigs []*kueueac.UnhealthyNodeApplyConfiguration
	for _, un := range unhealthyNodes {
		if un.Name == "" {
			continue
		}
		unConfigs = append(unConfigs, kueueac.UnhealthyNode().WithName(un.Name))
	}
	return unConfigs
}

// toPreemptionGateConfigs converts preemption gate states (listMapKey=name).
func toPreemptionGateConfigs(preemptionGates []kueue.PreemptionGateState) []*kueueac.PreemptionGateStateApplyConfiguration {
	var pgConfigs []*kueueac.PreemptionGateStateApplyConfiguration
	for _, pg := range preemptionGates {
		if pg.Name == "" {
			continue
		}
		pgConfig := kueueac.PreemptionGateState().
			WithName(pg.Name).
			WithPosition(pg.Position).
			WithLastTransitionTime(pg.LastTransitionTime) // required, always set
		pgConfigs = append(pgConfigs, pgConfig)
	}
	return pgConfigs
}

// patchStatus updates the status of a workload.
// If the WorkloadRequestUseMergePatch feature is enabled, it uses a Merge Patch with update function.
// Otherwise, it runs the update function and, if updated, applies the SSA Patch status.
func patchStatus(ctx context.Context, c client.Client, apiReader client.Reader, wl *kueue.Workload, owner client.FieldOwner, update UpdateFunc, opts *patchStatusOptions) error {
	wlCopy := wl.DeepCopy()
	if !opts.ForceApply && features.Enabled(features.WorkloadRequestUseMergePatch) {
		patchOptions := make([]clientutil.PatchOption, 0, 2)
		if !opts.StrictPatch {
			patchOptions = append(patchOptions, clientutil.WithLoose())
		}
		if opts.RetryOnConflictForPatch {
			patchOptions = append(patchOptions, clientutil.WithRetryOnConflict())
		}
		err := clientutil.PatchStatus(ctx, c, wlCopy, func() (bool, error) {
			return update(wlCopy)
		}, patchOptions...)
		if err != nil {
			return err
		}
		wlCopy.DeepCopyInto(wl)
		return nil
	}

	if updated, err := update(wlCopy); err != nil || !updated {
		return err
	}
	applyConfig := baseWorkloadApplyConfiguration(wlCopy, opts.StrictApply)
	applyConfig.WithStatus(toWorkloadStatusApplyConfiguration(&wlCopy.Status))
	if err := c.Status().Apply(ctx, applyConfig, owner, client.ForceOwnership); err != nil {
		return err
	}
	// Refresh wl from the authoritative store (uncached reader) so the caller sees
	// the post-apply object, including the merged status and resourceVersion.
	return apiReader.Get(ctx, client.ObjectKeyFromObject(wl), wl)
}

func PatchStatus(ctx context.Context, c client.Client, apiReader client.Reader, wl *kueue.Workload, owner client.FieldOwner, update UpdateFunc, options ...PatchStatusOption) error {
	opts := convertPatchStatusOptions(options)
	return patchStatus(ctx, c, apiReader, wl, owner, func(wl *kueue.Workload) (bool, error) {
		if opts.ForceApply || !features.Enabled(features.WorkloadRequestUseMergePatch) {
			wlPatch := baseSSAWorkload(wl, opts.StrictApply)
			wlPatch.DeepCopyInto(wl)
		}
		return update(wl)
	}, opts)
}

func PatchAdmissionStatus(ctx context.Context, c client.Client, apiReader client.Reader, wl *kueue.Workload, clk clock.Clock, update UpdateFunc, options ...PatchStatusOption) error {
	opts := convertPatchStatusOptions(options)
	return patchStatus(ctx, c, apiReader, wl, constants.AdmissionName, func(wl *kueue.Workload) (bool, error) {
		if updated, err := update(wl); err != nil || !updated {
			return updated, err
		}
		if opts.ForceApply || !features.Enabled(features.WorkloadRequestUseMergePatch) {
			wlPatch := prepareWorkloadPatch(wl, opts.StrictApply, clk)
			wlPatch.DeepCopyInto(wl)
		}
		return true, nil
	}, opts)
}

func PriorityClassName(wl *kueue.Workload) string {
	if wl.Spec.PriorityClassRef != nil {
		return wl.Spec.PriorityClassRef.Name
	}
	return ""
}

func ReasonWithCause(reason, underlyingCause string) string {
	return fmt.Sprintf("%sDueTo%s", reason, underlyingCause)
}

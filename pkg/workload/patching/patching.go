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

package patch

import (
	"context"
	"fmt"
	"maps"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
)

var (
	admissionManagedConditions = []string{
		kueue.WorkloadQuotaReserved,
		kueue.WorkloadEvicted,
		kueue.WorkloadAdmitted,
		kueue.WorkloadPreempted,
		kueue.WorkloadRequeued,
		kueue.WorkloadDeactivationTarget,
		kueue.WorkloadFinished,
	}
)

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
}

func admissionChecksStatusPatch(w *kueue.Workload, wlCopy *kueue.Workload, c clock.Clock) {
	if wlCopy.Status.AdmissionChecks == nil && w.Status.AdmissionChecks != nil {
		wlCopy.Status.AdmissionChecks = make([]kueue.AdmissionCheckState, 0)
	}
	for _, ac := range w.Status.AdmissionChecks {
		SetAdmissionCheckState(&wlCopy.Status.AdmissionChecks, ac, c)
	}
}

func PrepareWorkloadPatch(w *kueue.Workload, strict bool, clk clock.Clock) *kueue.Workload {
	wlCopy := BaseSSAWorkload(w, strict)
	admissionStatusPatch(w, wlCopy)
	admissionChecksStatusPatch(w, wlCopy, clk)
	return wlCopy
}

type UpdateFunc func(*kueue.Workload) (bool, error)

// PatchStatusOption defines a functional option for customizing PatchStatusOptions.
// It follows the functional options pattern, allowing callers to configure
// patch behavior at call sites without directly manipulating PatchStatusOptions.
type PatchStatusOption func(*PatchStatusOptions)

// PatchStatusOptions contains configuration parameters that control how patches
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
// Typically, PatchStatusOptions are constructed via DefaultPatchStatusOptions and
// modified using PatchStatusOption functions (e.g., WithLoose).
type PatchStatusOptions struct {
	StrictPatch             bool
	StrictApply             bool
	RetryOnConflictForPatch bool
	ForceApply              bool
}

// DefaultPatchStatusOptions returns a new PatchStatusOptions instance configured with
// default settings.
//
// By default, StrictPatch and StrictApply is set to true, meaning ResourceVersion is cleared
// from the original object so it will always be included in the generated
// patch. This ensures stricter version handling during patch application.
func DefaultPatchStatusOptions() *PatchStatusOptions {
	return &PatchStatusOptions{
		StrictPatch: true, // default is strict
		StrictApply: true, // default is strict
	}
}

// WithLooseOnApply returns a PatchStatusOption that resets the StrictApply field on PatchStatusOptions.
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

func WithLooseOnApply() PatchStatusOption {
	return func(o *PatchStatusOptions) {
		o.StrictApply = false
	}
}

// WithRetryOnConflict configures PatchStatusOptions to enable retry logic on conflicts.
// Note: This only works with merge patches.
func WithRetryOnConflict() PatchStatusOption {
	return func(o *PatchStatusOptions) {
		o.RetryOnConflictForPatch = true
	}
}

// WithForceApply is a PatchStatusOption that forces the use of the apply patch.
func WithForceApply() PatchStatusOption {
	return func(o *PatchStatusOptions) {
		o.ForceApply = true
	}
}

func patchStatusOptions(options []PatchStatusOption) *PatchStatusOptions {
	opts := DefaultPatchStatusOptions()
	for _, opt := range options {
		opt(opts)
	}
	return opts
}

// patchStatus updates the status of a workload.
// If the WorkloadRequestUseMergePatch feature is enabled, it uses a Merge Patch with update function.
// Otherwise, it runs the update function and, if updated, applies the SSA Patch status.
func patchStatus(ctx context.Context, c client.Client, wl *kueue.Workload, owner client.FieldOwner, update UpdateFunc, opts *PatchStatusOptions) error {
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
	} else {
		if updated, err := update(wlCopy); err != nil || !updated {
			return err
		}
		err := c.Status().Patch(ctx, wlCopy, client.Apply, owner, client.ForceOwnership) //nolint:staticcheck //SA1019: client.Apply is deprecated
		if err != nil {
			return err
		}
	}
	wlCopy.DeepCopyInto(wl)
	return nil
}

func PatchStatus(ctx context.Context, c client.Client, wl *kueue.Workload, owner client.FieldOwner, update UpdateFunc, options ...PatchStatusOption) error {
	opts := patchStatusOptions(options)
	return patchStatus(ctx, c, wl, owner, func(wl *kueue.Workload) (bool, error) {
		if opts.ForceApply || !features.Enabled(features.WorkloadRequestUseMergePatch) {
			wlPatch := BaseSSAWorkload(wl, opts.StrictApply)
			wlPatch.DeepCopyInto(wl)
		}
		return update(wl)
	}, opts)
}

func PatchAdmissionStatus(ctx context.Context, c client.Client, wl *kueue.Workload, clk clock.Clock, update UpdateFunc, options ...PatchStatusOption) error {
	opts := patchStatusOptions(options)
	return patchStatus(ctx, c, wl, constants.AdmissionName, func(wl *kueue.Workload) (bool, error) {
		if updated, err := update(wl); err != nil || !updated {
			return updated, err
		}
		if opts.ForceApply || !features.Enabled(features.WorkloadRequestUseMergePatch) {
			wlPatch := PrepareWorkloadPatch(wl, opts.StrictApply, clk)
			wlPatch.DeepCopyInto(wl)
		}
		return true, nil
	}, opts)
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

func PriorityClassName(wl *kueue.Workload) string {
	if wl.Spec.PriorityClassRef != nil {
		return wl.Spec.PriorityClassRef.Name
	}
	return ""
}

func ReasonWithCause(reason, underlyingCause string) string {
	return fmt.Sprintf("%sDueTo%s", reason, underlyingCause)
}

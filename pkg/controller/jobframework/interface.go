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

package jobframework

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/podset"
)

// GenericJob is a required interface that must be implemented by all jobs
// managed by Kueue's jobframework.
type GenericJob interface {
	// Object returns the job instance.
	Object() client.Object

	// IsSuspended returns whether the job is suspended.
	IsSuspended() bool

	// Suspend suspends the job.
	Suspend()

	// RunWithPodSetsInfo injects node affinity and pod set counts extracted
	// from the workload into the job and unsuspends it.
	RunWithPodSetsInfo(ctx context.Context, c client.Client, podSetsInfo []podset.PodSetInfo) error

	// RestorePodSetsInfo restores the original node affinity and pod set counts of the job.
	// It returns whether any change was made. On a pod set count mismatch it logs
	// and returns false without applying any change.
	RestorePodSetsInfo(ctx context.Context, podSetsInfo []podset.PodSetInfo) bool

	// Finished returns whether the job is completed or failed.
	// The message describes the condition, and success indicates completion status.
	// Observed generation of the workload is set by the jobframework.
	Finished(ctx context.Context) (message string, success, finished bool)

	// PodSets builds workload pod sets corresponding to the job.
	PodSets(ctx context.Context, c client.Client) ([]kueue.PodSet, error)

	// IsActive returns true if there are any running pods.
	IsActive(ctx context.Context) bool

	// PodsReady indicates whether all job-derived pods are ready.
	PodsReady(ctx context.Context, c client.Client) bool

	// GVK returns the GroupVersionKind for the job.
	GVK() schema.GroupVersionKind
}

// JobWithPodLabelSelector is an optional interface that should be implemented by generic jobs
// when pod label selector information is needed.
type JobWithPodLabelSelector interface {
	// PodLabelSelector returns the label selector used by pods for the job.
	PodLabelSelector() string
}

// JobWithReclaimablePods is an optional interface that should be implemented by generic jobs
// when reclaimable pod information is needed.
type JobWithReclaimablePods interface {
	// ReclaimablePods returns the list of reclaimable pods.
	//
	// Note: for Jobs with ordered Pods that support elastic scaling, implementations
	// must account for which ranks remain after scaling down to prevent quota leaks.
	// See the batch Job implementation and kueue#12958, kueue#13117.
	ReclaimablePods(ctx context.Context, c client.Client) ([]kueue.ReclaimablePod, error)
}

// JobWithCustomStop is an optional interface that should be implemented by generic jobs
// when a custom stop procedure is needed.
type JobWithCustomStop interface {
	// Stop implements a custom stop procedure.
	// The function should be idempotent and must not perform any API calls if the job is already stopped.
	// Returns whether the Job was stopped by this call or an error.
	Stop(ctx context.Context, c client.Client, podSetsInfo []podset.PodSetInfo, stopReason StopReason, eventMsg string) (bool, error)
}

// JobWithFinalize is an optional interface that should be implemented by generic jobs
// when custom finalization logic is needed for a job after it has finished.
type JobWithFinalize interface {
	Finalize(ctx context.Context, c client.Client) error
}

// JobWithSkip is an optional interface that should be implemented by generic jobs
// when reconciliation should be skipped depending on the job's state.
type JobWithSkip interface {
	Skip(ctx context.Context) bool
}

// JobWithPriorityClass is an optional interface that should be implemented by generic jobs
// when a custom priority class is needed.
type JobWithPriorityClass interface {
	// PriorityClass returns the name of the job's priority class.
	PriorityClass() string
}

// JobWithCustomValidation is an optional interface that allows custom webhook validation
// for jobs that use BaseWebhook.
type JobWithCustomValidation interface {
	// ValidateOnCreate returns a list of webhook create validation errors.
	ValidateOnCreate(ctx context.Context) (field.ErrorList, error)

	// ValidateOnUpdate returns a list of webhook update validation errors.
	ValidateOnUpdate(ctx context.Context, oldJob GenericJob) (field.ErrorList, error)
}

// ComposableJob is an optional interface that should be implemented by generic jobs
// composed of multiple API objects.
type ComposableJob interface {
	// Load loads all members of the composable job. If removeFinalizers is true,
	// workload and job finalizers should be removed.
	Load(ctx context.Context, c client.Client, key *types.NamespacedName) (removeFinalizers bool, err error)

	// Run unsuspends all members of the ComposableJob and injects node affinity
	// with pod set counts extracted from the workload into all members of the job.
	Run(ctx context.Context, c client.Client, wl *kueue.Workload, podSetsInfo []podset.PodSetInfo, r events.EventRecorder, msg string) error

	// ConstructComposableWorkload builds a new Workload from all members of the ComposableJob.
	ConstructComposableWorkload(ctx context.Context, c client.Client, r events.EventRecorder, labelKeysToCopy, annotationsToCopy sets.Set[string]) (*kueue.Workload, error)

	// ListChildWorkloads returns all workloads related to the composable job.
	ListChildWorkloads(ctx context.Context, c client.Client, parent types.NamespacedName) (*kueue.WorkloadList, error)

	// FindMatchingWorkloads returns related workloads: the matching ComposableJob workload
	// and any duplicates that should be deleted.
	FindMatchingWorkloads(ctx context.Context, c client.Client, r events.EventRecorder) (match *kueue.Workload, toDelete []*kueue.Workload, err error)

	// Stop implements the custom stop procedure for ComposableJob.
	Stop(ctx context.Context, c client.Client, podSetsInfo []podset.PodSetInfo, stopReason StopReason, eventMsg string) ([]client.Object, error)

	// ForEach calls f on each member of the ComposableJob.
	ForEach(f func(obj runtime.Object))

	// EnsureWorkloadOwnedByAllMembers ensures that the provided workload is owned by all specified members.
	// If not, it adds missing owner references and returns an error if any issue occurs.
	EnsureWorkloadOwnedByAllMembers(ctx context.Context, c client.Client, r events.EventRecorder, workload *kueue.Workload) error

	// EquivalentToWorkload checks whether the provided workload is equivalent to the target workload.
	// Returns true if they are equivalent and an error if any issues occur.
	EquivalentToWorkload(ctx context.Context, c client.Client, wl *kueue.Workload) (bool, error)
}

// JobWithCustomWorkloadConditions is an optional interface that should be implemented
// by generic jobs when custom workload conditions need to be updated after ensuring
// the workload exists.
type JobWithCustomWorkloadConditions interface {
	// CustomWorkloadConditions returns custom workload conditions and whether the status changed.
	CustomWorkloadConditions(wl *kueue.Workload) ([]metav1.Condition, bool)
}

// JobWithCustomWorkloadActivation is an optional interface that should be implemented
// by generic jobs when custom logic is needed to determine whether the workload is active.
type JobWithCustomWorkloadActivation interface {
	// IsWorkloadActive returns true if the workload is active.
	IsWorkloadActive() bool
}

// JobWithManagedBy is an optional interface that should be implemented
// by generic jobs that support the managedBy protocol for Multi-Kueue.
type JobWithManagedBy interface {
	// CanDefaultManagedBy returns true if ManagedBy() would return nil
	// or the default controller for the framework.
	CanDefaultManagedBy() bool

	// ManagedBy returns the name of the controller managing the Job.
	ManagedBy() *string

	// SetManagedBy sets the spec field containing the name
	// of the managing controller.
	SetManagedBy(*string)
}

// JobWithCustomAnnotations is an optional interface that should be implemented
// by generic jobs when custom annotations need to be updated in the API server
// after changes occur.
//
// For example, RayJob may have the "kueue.x-k8s.io/podset-replica-sizes"
// annotation, which reflects the current replica sizes of the underlying
// RayCluster. The job reconciler calls GetCustomAnnotations to update
// such annotations in the API server.
type JobWithCustomAnnotations interface {
	// GetCustomAnnotations returns additional annotations
	// that should be added to the job.
	GetCustomAnnotations(ctx context.Context, c client.Client, podSets []kueue.PodSet) (map[string]string, error)
}

// ElasticWorkloadNameProvider is an optional interface that provides additional
// information for building workload names for elastic jobs.
type ElasticWorkloadNameProvider interface {
	// GetWorkloadNameExtraPart returns additional information used
	// to build the workload name.
	GetWorkloadNameExtraPart() string
}

// TopLevelJob is an optional interface used to indicate
// that the Job owns or manages the Workload object, regardless of the Job's
// owner references.
type TopLevelJob interface {
	// IsTopLevel returns true if the Job owns or manages the Workload.
	IsTopLevel() bool
}

// JobWithCustomQueueNameChange is an optional interface that allows jobs
// to provide custom queue-name change logic.
type JobWithCustomQueueNameChange interface {
	// CustomQueueNameChange is called by the jobframework to allow the job
	// to customize the workload queue-name using custom logic.
	CustomQueueNameChange(ctx context.Context, c client.Client, wl *kueue.Workload) error
}

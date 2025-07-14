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
	"strconv"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/podset"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	"sigs.k8s.io/kueue/pkg/util/maps"
)

// GenericJob if the interface which needs to be implemented by all jobs
// managed by the kueue's jobframework.
type GenericJob interface {
	// Object returns the job instance.
	Object() client.Object
	// IsSuspended returns whether the job is suspended or not.
	IsSuspended() bool
	// Suspend will suspend the job.
	Suspend()
	// RunWithPodSetsInfo will inject the node affinity and podSet counts extracting from workload to job and unsuspend it.
	RunWithPodSetsInfo(podSetsInfo []podset.PodSetInfo) error
	// RestorePodSetsInfo will restore the original node affinity and podSet counts of the job.
	// Returns whether any change was done.
	RestorePodSetsInfo(podSetsInfo []podset.PodSetInfo) bool
	// Finished means whether the job is completed/failed or not,
	// condition represents the workload finished condition.
	// Observed generation of the workload is set by the jobframework.
	Finished() (message string, success, finished bool)
	// PodSets will build workload podSets corresponding to the job.
	PodSets() ([]kueue.PodSet, error)
	// IsActive returns true if there are any running pods.
	IsActive() bool
	// PodsReady instructs whether job derived pods are all ready now.
	PodsReady() bool
	// GVK returns GVK (Group Version Kind) for the job.
	GVK() schema.GroupVersionKind
}

// Optional interfaces, are meant to implemented by jobs to enable additional
// features of the jobframework reconciler.

type JobWithPodLabelSelector interface {
	// PodLabelSelector returns the label selector used by pods for the job.
	PodLabelSelector() string
}

type JobWithReclaimablePods interface {
	// ReclaimablePods returns the list of reclaimable pods.
	ReclaimablePods() ([]kueue.ReclaimablePod, error)
}

type StopReason string

const (
	StopReasonWorkloadDeleted    StopReason = "WorkloadDeleted"
	StopReasonWorkloadEvicted    StopReason = "WorkloadEvicted"
	StopReasonNoMatchingWorkload StopReason = "NoMatchingWorkload"
	StopReasonNotAdmitted        StopReason = "NotAdmitted"
)

type JobWithCustomStop interface {
	// Stop implements a custom stop procedure.
	// The function should be idempotent: not do any API calls if the job is already stopped.
	// Returns whether the Job stopped with this call or an error
	Stop(ctx context.Context, c client.Client, podSetsInfo []podset.PodSetInfo, stopReason StopReason, eventMsg string) (bool, error)
}

// JobWithFinalize interface should be implemented by generic jobs,
// when custom finalization logic is needed for a job, after it's finished.
type JobWithFinalize interface {
	Finalize(ctx context.Context, c client.Client) error
}

// JobWithSkip interface should be implemented by generic jobs,
// when reconciliation should be skipped depending on the job's state
type JobWithSkip interface {
	Skip() bool
}

type JobWithPriorityClass interface {
	// PriorityClass returns the job's priority class name.
	PriorityClass() string
}

// JobWithCustomValidation optional interface that allows custom webhook validation
// for Jobs that use BaseWebhook.
type JobWithCustomValidation interface {
	// ValidateOnCreate returns list of webhook create validation errors.
	ValidateOnCreate() (field.ErrorList, error)
	// ValidateOnUpdate returns list of webhook update validation errors.
	ValidateOnUpdate(oldJob GenericJob) (field.ErrorList, error)
}

// ComposableJob interface should be implemented by generic jobs that
// are composed out of multiple API objects.
type ComposableJob interface {
	// Load loads all members of the composable job. If removeFinalizers == true, workload and job finalizers should be removed.
	Load(ctx context.Context, c client.Client, key *types.NamespacedName) (removeFinalizers bool, err error)
	// Run unsuspends all members of the ComposableJob and injects the node affinity with podSet
	// counts extracting from workload to all members of the ComposableJob.
	Run(ctx context.Context, c client.Client, podSetsInfo []podset.PodSetInfo, r record.EventRecorder, msg string) error
	// ConstructComposableWorkload returns a new Workload that's assembled out of all members of the ComposableJob.
	ConstructComposableWorkload(ctx context.Context, c client.Client, r record.EventRecorder, labelKeysToCopy []string) (*kueue.Workload, error)
	// ListChildWorkloads returns all workloads related to the composable job.
	ListChildWorkloads(ctx context.Context, c client.Client, parent types.NamespacedName) (*kueue.WorkloadList, error)
	// FindMatchingWorkloads returns all related workloads, workload that matches the ComposableJob and duplicates that has to be deleted.
	FindMatchingWorkloads(ctx context.Context, c client.Client, r record.EventRecorder) (match *kueue.Workload, toDelete []*kueue.Workload, err error)
	// Stop implements the custom stop procedure for ComposableJob.
	Stop(ctx context.Context, c client.Client, podSetsInfo []podset.PodSetInfo, stopReason StopReason, eventMsg string) ([]client.Object, error)
	// ForEach calls f on each member of the ComposableJob.
	ForEach(f func(obj runtime.Object))
	// EnsureWorkloadOwnedByAllMembers ensures that the provided workload is owned by the specified owners.
	// If the workload is not owned by all the specified owners, it adds them to the owner references.
	// Returns true if the workload is updated, and an error if any issues occur.
	EnsureWorkloadOwnedByAllMembers(ctx context.Context, c client.Client, r record.EventRecorder, workload *kueue.Workload) error
	// EquivalentToWorkload checks if the provided workload is equivalent to the target workload.
	// Returns true if they are equivalent and an error if any issues occur.
	EquivalentToWorkload(ctx context.Context, c client.Client, wl *kueue.Workload) (bool, error)
}

// JobWithCustomWorkloadConditions interface should be implemented by generic jobs,
// when custom workload conditions should be updated after ensure that the workload exists.
type JobWithCustomWorkloadConditions interface {
	// CustomWorkloadConditions return custom workload conditions and status changed or not.
	CustomWorkloadConditions(wl *kueue.Workload) ([]metav1.Condition, bool)
}

// JobWithManagedBy interface should be implemented by generic jobs
// that implement the managedBy protocol for Multi-Kueue
type JobWithManagedBy interface {
	// CanDefaultManagedBy returns true of ManagedBy() would return nil or the default controller for the framework
	CanDefaultManagedBy() bool
	// ManagedBy returns the name of the controller that is managing the Job
	ManagedBy() *string
	// SetManagedBy sets the field in the spec that contains the name of the managing controller
	SetManagedBy(*string)
}

// TopLevelJob interface is an optional interface used to indicate
// that the Job owns/manages the Workload object, regardless of the Job
// owner references.
type TopLevelJob interface {
	// IsTopLevel returns true if the Job owns/manages the Workload.
	IsTopLevel() bool
}

func QueueName(job GenericJob) kueue.LocalQueueName {
	return QueueNameForObject(job.Object())
}

func QueueNameForObject(object client.Object) kueue.LocalQueueName {
	if queueLabel := object.GetLabels()[constants.QueueLabel]; queueLabel != "" {
		return kueue.LocalQueueName(queueLabel)
	}
	// fallback to the annotation (deprecated)
	return kueue.LocalQueueName(object.GetAnnotations()[constants.QueueAnnotation])
}

func MaximumExecutionTimeSeconds(job GenericJob) *int32 {
	return MaximumExecutionTimeSecondsForObject(job.Object())
}

func MaximumExecutionTimeSecondsForObject(object client.Object) *int32 {
	strVal, found := object.GetLabels()[constants.MaxExecTimeSecondsLabel]
	if !found {
		return nil
	}

	v, err := strconv.ParseInt(strVal, 10, 32)
	if err != nil || v <= 0 {
		return nil
	}

	return ptr.To(int32(v))
}

func WorkloadPriorityClassName(object client.Object) string {
	if workloadPriorityClassLabel := object.GetLabels()[constants.WorkloadPriorityClassLabel]; workloadPriorityClassLabel != "" {
		return workloadPriorityClassLabel
	}
	return ""
}

func PrebuiltWorkloadFor(job GenericJob) (string, bool) {
	name, found := job.Object().GetLabels()[constants.PrebuiltWorkloadLabel]
	return name, found
}

func NewWorkload(name string, obj client.Object, podSets []kueue.PodSet, labelKeysToCopy []string) *kueue.Workload {
	return &kueue.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   obj.GetNamespace(),
			Labels:      maps.FilterKeys(obj.GetLabels(), labelKeysToCopy),
			Finalizers:  []string{kueue.ResourceInUseFinalizerName},
			Annotations: admissioncheck.FilterProvReqAnnotations(obj.GetAnnotations()),
		},
		Spec: kueue.WorkloadSpec{
			QueueName:                   QueueNameForObject(obj),
			PodSets:                     podSets,
			MaximumExecutionTimeSeconds: MaximumExecutionTimeSecondsForObject(obj),
		},
	}
}

// MultiKueueAdapter interface needed for MultiKueue job delegation.
type MultiKueueAdapter interface {
	// SyncJob creates the Job object in the worker cluster using remote client, if not already created.
	// Copy the status from the remote job if already exists.
	SyncJob(ctx context.Context, localClient client.Client, remoteClient client.Client, key types.NamespacedName, workloadName, origin string) error
	// DeleteRemoteObject deletes the Job in the worker cluster.
	DeleteRemoteObject(ctx context.Context, remoteClient client.Client, key types.NamespacedName) error
	// IsJobManagedByKueue returns:
	// - a bool indicating if the job object identified by key is managed by kueue and can be delegated.
	// - a reason indicating why the job is not managed by Kueue
	// - any API error encountered during the check
	IsJobManagedByKueue(ctx context.Context, localClient client.Client, key types.NamespacedName) (bool, string, error)
	// KeepAdmissionCheckPending returns true if the state of the multikueue admission check should be
	// kept Pending while the job runs in a worker. This might be needed to keep the managers job
	// suspended and not start the execution locally.
	KeepAdmissionCheckPending() bool
	// GVK returns GVK (Group Version Kind) for the job.
	GVK() schema.GroupVersionKind
}

// MultiKueueWatcher optional interface that can be implemented by a MultiKueueAdapter
// to receive job related watch events from the worker cluster.
// If not implemented, MultiKueue will only receive events related to the job's workload.
type MultiKueueWatcher interface {
	// GetEmptyList returns an empty list of objects
	GetEmptyList() client.ObjectList
	// WorkloadKeyFor returns the key of the workload of interest
	// - the object name for workloads
	// - the prebuilt workload for job types
	WorkloadKeyFor(runtime.Object) (types.NamespacedName, error)
}

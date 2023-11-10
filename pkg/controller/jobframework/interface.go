/*
Copyright 2023 The Kubernetes Authors.
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
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/podset"
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
	Finished() (condition metav1.Condition, finished bool)
	// PodSets will build workload podSets corresponding to the job.
	PodSets() []kueue.PodSet
	// IsActive returns true if there are any running pods.
	IsActive() bool
	// PodsReady instructs whether job derived pods are all ready now.
	PodsReady() bool
	// GVK returns GVK (Group Version Kind) for the job.
	GVK() schema.GroupVersionKind
}

// Optional interfaces, are meant to implemented by jobs to enable additional
// features of the jobframework reconciler.

type JobWithReclaimablePods interface {
	// ReclaimablePods returns the list of reclaimable pods.
	ReclaimablePods() []kueue.ReclaimablePod
}

type JobWithCustomStop interface {
	// Stop implements a custom stop procedure.
	// The function should be idempotent: not do any API calls if the job is already stopped.
	// Returns whether the Job stopped with this call or an error
	Stop(ctx context.Context, c client.Client, podSetsInfo []podset.PodSetInfo, eventMsg string) (bool, error)
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

// ComposableJob interface should be implemented by generic jobs that
// are composed out of multiple API objects.
type ComposableJob interface {
	// Load loads all members of the composable job. If removeFinalizers == true, workload and job finalizers should be removed.
	Load(ctx context.Context, c client.Client, key types.NamespacedName) (removeFinalizers bool, err error)
	// ConstructComposableWorkload returns a new Workload that's assembled out of all members of the ComposableJob.
	ConstructComposableWorkload(ctx context.Context, c client.Client, r record.EventRecorder) (*kueue.Workload, error)
	// FindMatchingWorkloads returns all related workloads, workload that matches the ComposableJob and duplicates that has to be deleted.
	FindMatchingWorkloads(ctx context.Context, c client.Client) (match *kueue.Workload, toDelete []*kueue.Workload, err error)
}

func ParentWorkloadName(job GenericJob) string {
	return job.Object().GetAnnotations()[constants.ParentWorkloadAnnotation]
}

func QueueName(job GenericJob) string {
	return QueueNameForObject(job.Object())
}

func QueueNameForObject(object client.Object) string {
	if queueLabel := object.GetLabels()[constants.QueueLabel]; queueLabel != "" {
		return queueLabel
	}
	// fallback to the annotation (deprecated)
	return object.GetAnnotations()[constants.QueueAnnotation]
}

func workloadPriorityClassName(job GenericJob) string {
	object := job.Object()
	if workloadPriorityClassLabel := object.GetLabels()[constants.WorkloadPriorityClassLabel]; workloadPriorityClassLabel != "" {
		return workloadPriorityClassLabel
	}
	return ""
}

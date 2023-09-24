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
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
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
	RunWithPodSetsInfo(nodeSelectors []PodSetInfo) error
	// RestorePodSetsInfo will restore the original node affinity and podSet counts of the job.
	// Returns whether any change was done.
	RestorePodSetsInfo(nodeSelectors []PodSetInfo) bool
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
	Stop(ctx context.Context, c client.Client, podSetsInfo []PodSetInfo) (bool, error)
}

type JobWithPriorityClass interface {
	// PriorityClass returns the job's priority class name.
	PriorityClass() string
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

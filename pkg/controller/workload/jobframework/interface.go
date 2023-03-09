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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
)

type GenericJob interface {
	// Object returns the job instance.
	Object() client.Object
	// IsSuspend returns whether the job is suspended or not.
	IsSuspend() bool
	// Suspend will suspend the job.
	Suspend() error
	// UnSuspend will unsuspend the job.
	UnSuspend() error
	// ResetStatus will reset the job status to the original state.
	// If true, status is modified, if not, status is as it was.
	ResetStatus() bool
	// InjectNodeAffinity will inject the node affinity extracting from workload to job.
	InjectNodeAffinity(nodeSelectors []map[string]string) error
	// RestoreNodeAffinity will restore the original node affinity of job.
	RestoreNodeAffinity(podSets []kueue.PodSet) error
	// Finished means whether the job is completed/failed or not,
	// condition represents the workload finished condition.
	Finished() (condition metav1.Condition, finished bool)
	// PodSets will build workload podSets corresponding to the job.
	PodSets() []kueue.PodSet
	// EquivalentToWorkload validates whether the workload is semantically equal to the job.
	EquivalentToWorkload(wl kueue.Workload) bool
	// PriorityClass returns the job's priority class name.
	PriorityClass() string
	// QueueName returns the queue name the job enqueued.
	QueueName() string
	// ParentWorkloadName returns the parent workload name.
	ParentWorkloadName() string
	// IsActive returns true if there are any running pods.
	IsActive() bool
	// PodsReady instructs whether job derived pods are all ready now.
	PodsReady() bool
	// GetWorkloadName returns the name of the workload for the job.
	GetWorkloadName() string
	// GetOwnerKey returns a unique key that identifies Workloads owned by the job implementation.
	GetOwnerKey() string
}

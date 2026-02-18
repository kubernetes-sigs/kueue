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

package constants

import kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"

const (
	// QueueLabel is the label key in the workload that holds the queue name.
	QueueLabel = "kueue.x-k8s.io/queue-name"

	// DefaultLocalQueueName is the name for default LocalQueue that is applied
	// if QueueLabel is not specified.
	DefaultLocalQueueName kueue.LocalQueueName = "default"

	// PrebuiltWorkloadLabel is the label key of the job holding the name of the pre-built workload to use.
	PrebuiltWorkloadLabel = "kueue.x-k8s.io/prebuilt-workload-name"

	// JobUIDLabel is the label key in the workload resource, that holds the UID of
	// the owner job.
	JobUIDLabel = "kueue.x-k8s.io/job-uid"

	// WorkloadPriorityClassLabel is the label key in the workload that holds the
	// workloadPriorityClass name.
	// This label is always mutable because it might be useful for the preemption.
	WorkloadPriorityClassLabel = "kueue.x-k8s.io/priority-class"

	// ProvReqAnnotationPrefix is the prefix for annotations that should be pass to ProvisioningRequest as Parameters.
	ProvReqAnnotationPrefix = "provreq.kueue.x-k8s.io/"

	// MaxExecTimeSecondsLabel is the label key in the job that holds the maximum execution time.
	MaxExecTimeSecondsLabel = `kueue.x-k8s.io/max-exec-time-seconds`

	// SafeToForcefullyTerminateAnnotationKey is the annotation key that controls whether a pod opted in to FailureRecoveryPolicy.
	SafeToForcefullyTerminateAnnotationKey = "kueue.x-k8s.io/safe-to-forcefully-terminate"
	// SafeToForcefullyTerminateAnnotationValue is the value of that annotation that enables FailureRecoveryPolicy for that pod.
	SafeToForcefullyTerminateAnnotationValue = "true"

	// JobOwnerGVKAnnotation is the annotation key in the workload that holds the GVK of the owner job.
	JobOwnerGVKAnnotation = "kueue.x-k8s.io/job-owner-gvk"

	// JobOwnerNameAnnotation is the annotation key in the workload that holds the name of the owner job.
	JobOwnerNameAnnotation = "kueue.x-k8s.io/job-owner-name"

	// ComponentWorkloadIndexAnnotation stores the numeric index for component workloads
	// in multi-workload jobs (e.g., LeaderWorkerSet replicas).
	ComponentWorkloadIndexAnnotation = "kueue.x-k8s.io/component-workload-index"
)

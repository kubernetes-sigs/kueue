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

import (
	"time"
)

const (
	KueueName                    = "kueue"
	JobControllerName            = KueueName + "-job-controller"
	WorkloadControllerName       = KueueName + "-workload-controller"
	PodTerminationControllerName = KueueName + "-pod-termination-controller"
	AdmissionName                = KueueName + "-admission"
	ReclaimablePodsMgr           = KueueName + "-reclaimable-pods"

	// UpdatesBatchPeriod is the batch period to hold workload updates
	// before syncing a Queue and ClusterQueue objects.
	UpdatesBatchPeriod = time.Second

	// DefaultPriority is used to set priority of workloads
	// that do not specify any priority class, and there is no priority class
	// marked as default.
	DefaultPriority int32 = 0

	DefaultPendingWorkloadsLimit = 1000

	// ManagedByKueueLabelKey label that signalize that an object is managed by Kueue
	ManagedByKueueLabelKey   = "kueue.x-k8s.io/managed"
	ManagedByKueueLabelValue = "true"

	// PodSetLabel is a label set on the Job's PodTemplate to indicate the name
	// of the PodSet of the admitted Workload corresponding to the PodTemplate.
	// The label is set when starting the Job, and removed on stopping the Job.
	PodSetLabel = "kueue.x-k8s.io/podset"

	// ClusterQueueLabel is a pod label indicating the name of the ClusterQueue
	// which admitted the corresponding Kueue's workload.
	// It can be used, for example, to aggregate the actual resource usage (cpu/mem)
	// coming from workloads admitted to a given ClusterQueue.
	//
	// This label requires AssignQueueLabelsForPods feature, enabled by default.
	//
	// Warning: This label is added only if cluster queue name is a valid label value.
	// For limitations see Kubernetes [documentation](https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/#syntax-and-character-set)
	ClusterQueueLabel = "kueue.x-k8s.io/cluster-queue-name"

	// LocalQueueLabel is a pod label indicating the name of the LocalQueue
	// which admitted the corresponding Kueue's workload.
	// It can be used, for example, to aggregate the actual resource usage (cpu/mem)
	// coming from workloads admitted to a given LocalQueue.
	//
	// This label requires AssignQueueLabelsForPods feature, enabled by default.
	LocalQueueLabel = "kueue.x-k8s.io/local-queue-name"
)

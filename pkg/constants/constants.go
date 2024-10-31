/*
Copyright 2022 The Kubernetes Authors.

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
	KueueName              = "kueue"
	JobControllerName      = KueueName + "-job-controller"
	WorkloadControllerName = KueueName + "-workload-controller"
	AdmissionName          = KueueName + "-admission"
	ReclaimablePodsMgr     = KueueName + "-reclaimable-pods"

	// UpdatesBatchPeriod is the batch period to hold workload updates
	// before syncing a Queue and ClusterQueue objects.
	UpdatesBatchPeriod = time.Second

	// DefaultPriority is used to set priority of workloads
	// that do not specify any priority class and there is no priority class
	// marked as default.
	DefaultPriority = 0

	WorkloadPriorityClassSource = "kueue.x-k8s.io/workloadpriorityclass"
	PodPriorityClassSource      = "scheduling.k8s.io/priorityclass"

	DefaultPendingWorkloadsLimit = 1000

	// ManagedByKueueLabel label that signalize that an object is managed by Kueue
	ManagedByKueueLabel = "kueue.x-k8s.io/managed"
)

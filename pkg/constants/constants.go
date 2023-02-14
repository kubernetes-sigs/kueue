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

import "time"

const (
	// QueueAnnotation is the annotation in the workload that holds the queue name.
	//
	// TODO(#23): Use the kubernetes.io domain when graduating APIs to beta.
	QueueAnnotation = "kueue.x-k8s.io/queue-name"

	// ParentWorkloadAnnotation is the annotation used to mark a kubernetes Job
	// as a child of a Workload. The value is the name of the workload,
	// in the same namespace. It is used when the parent workload corresponds to
	// a custom job CRD composed of one or more kubernetes Jobs. When set, Kueue
	// ignores this Job from admission, and takes control of its suspension
	// status based on the admission status of the parent workload.
	ParentWorkloadAnnotation = "kueue.x-k8s.io/parent-workload"

	KueueName         = "kueue"
	JobControllerName = KueueName + "-job-controller"
	AdmissionName     = KueueName + "-admission"

	// UpdatesBatchPeriod is the batch period to hold workload updates
	// before syncing a Queue and ClusterQueue objects.
	UpdatesBatchPeriod = time.Second

	// DefaultPriority is used to set priority of workloads
	// that do not specify any priority class and there is no priority class
	// marked as default.
	DefaultPriority = 0
)

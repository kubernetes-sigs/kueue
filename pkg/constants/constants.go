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

	ManagerName       = "kueue-manager"
	JobControllerName = "kueue-job-controller"

	// UpdatesBatchPeriod is the batch period to hold queuedworkload updates
	// before syncing a Queue and CLusterQueue onbjects.
	UpdatesBatchPeriod = time.Second

	// DefaultPriority is used to set priority of workloads
	// that do not specify any priority class and there is no priority class
	// marked as default.
	DefaultPriority = 0
)

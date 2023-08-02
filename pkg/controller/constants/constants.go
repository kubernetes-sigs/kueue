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

package constants

const (
	// QueueLabel is the label key in the workload that holds the queue name.
	QueueLabel = "kueue.x-k8s.io/queue-name"

	// QueueAnnotation is the annotation key in the workload that holds the queue name.
	//
	// DEPRECATED: Use QueueLabel as a label key.
	QueueAnnotation = QueueLabel

	// ParentWorkloadAnnotation is the annotation used to mark a kubernetes Job
	// as a child of a Workload. The value is the name of the workload,
	// in the same namespace. It is used when the parent workload corresponds to
	// a custom job CRD composed of one or more kubernetes Jobs. When set, Kueue
	// ignores this Job from admission, and takes control of its suspension
	// status based on the admission status of the parent workload.
	ParentWorkloadAnnotation = "kueue.x-k8s.io/parent-workload"

	// ParentUIDLabel is the label key in the workload resource, that holds the UID of
	// a parent.
	ParentUIDLabel = "kueue.x-k8s.io/parent-uid"
)

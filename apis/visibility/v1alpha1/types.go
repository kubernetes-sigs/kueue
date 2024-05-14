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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +kubebuilder:object:root=true
// +k8s:openapi-gen=true
// +genclient:nonNamespaced
// +genclient:method=GetPendingWorkloadsSummary,verb=get,subresource=pendingworkloads,result=sigs.k8s.io/kueue/apis/visibility/v1alpha1.PendingWorkloadsSummary
// +genclient:method=GetRunningWorkloadsSummary,verb=get,subresource=runningWorkloads,result=sigs.k8s.io/kueue/apis/visibility/v1alpha1.RunningWorkloadsSummary
type ClusterQueue struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	PendingWorkloadsSummary PendingWorkloadsSummary `json:"pendingWorkloadsSummary"`
	RunningWorkloadsSummary RunningWorkloadsSummary `json:"runningWorkloadsSummary"`
}

// +kubebuilder:object:root=true
type ClusterQueueList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []ClusterQueue `json:"items"`
}

// +genclient
// +kubebuilder:object:root=true
// +k8s:openapi-gen=true
// +genclient:method=GetPendingWorkloadsSummary,verb=get,subresource=pendingworkloads,result=sigs.k8s.io/kueue/apis/visibility/v1alpha1.PendingWorkloadsSummary
type LocalQueue struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Summary PendingWorkloadsSummary `json:"pendingWorkloadsSummary"`
}

// +kubebuilder:object:root=true
type LocalQueueList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []LocalQueue `json:"items"`
}

// PendingWorkload is a user-facing representation of a pending workload that summarizes the relevant information for
// position in the cluster queue.
type PendingWorkload struct {
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Priority indicates the workload's priority
	Priority int32 `json:"priority"`

	// LocalQueueName indicates the name of the LocalQueue the workload is submitted to
	LocalQueueName string `json:"localQueueName"`

	// PositionInClusterQueue indicates the workload's position in the ClusterQueue, starting from 0
	PositionInClusterQueue int32 `json:"positionInClusterQueue"`

	// PositionInLocalQueue indicates the workload's position in the LocalQueue, starting from 0
	PositionInLocalQueue int32 `json:"positionInLocalQueue"`
}

// RunningWorkload is a user-facing representation of a running workload that summarizes the relevant information for
// assumed resources in the cluster queue.
type RunningWorkload struct {
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Priority indicates the workload's priority
	Priority int32 `json:"priority"`
	// AdmissionTime indecates the time workloads admitted
	AdmissionTime metav1.Time `json:"admissionTime"`
}

// +k8s:openapi-gen=true
// +kubebuilder:object:root=true

// RunningWorkloadsSummary contains a list of running workloads in the context
// of the query (within LocalQueue or ClusterQueue).
type RunningWorkloadsSummary struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Items []RunningWorkload `json:"items"`
}

// +kubebuilder:object:root=true
type RunningWorkloadsSummaryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []RunningWorkloadsSummary `json:"items"`
}

// +k8s:openapi-gen=true
// +kubebuilder:object:root=true

// PendingWorkloadsSummary contains a list of pending workloads in the context
// of the query (within LocalQueue or ClusterQueue).
type PendingWorkloadsSummary struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Items []PendingWorkload `json:"items"`
}

// +kubebuilder:object:root=true
type PendingWorkloadsSummaryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []PendingWorkloadsSummary `json:"items"`
}

// +kubebuilder:object:root=true
// +k8s:openapi-gen=true
// +k8s:conversion-gen:explicit-from=net/url.Values
// +k8s:defaulter-gen=true

// RunningWorkloadOptions are query params used in the visibility queries
type RunningWorkloadOptions struct {
	metav1.TypeMeta `json:",inline"`

	// Offset indicates position of the first pending workload that should be fetched, starting from 0. 0 by default
	Offset int64 `json:"offset"`

	// Limit indicates max number of pending workloads that should be fetched. 1000 by default
	Limit int64 `json:"limit,omitempty"`
}

// +kubebuilder:object:root=true
// +k8s:openapi-gen=true
// +k8s:conversion-gen:explicit-from=net/url.Values
// +k8s:defaulter-gen=true

// PendingWorkloadOptions are query params used in the visibility queries
type PendingWorkloadOptions struct {
	metav1.TypeMeta `json:",inline"`

	// Offset indicates position of the first pending workload that should be fetched, starting from 0. 0 by default
	Offset int64 `json:"offset"`

	// Limit indicates max number of pending workloads that should be fetched. 1000 by default
	Limit int64 `json:"limit,omitempty"`
}

func init() {
	SchemeBuilder.Register(
		&PendingWorkloadsSummary{},
		&PendingWorkloadOptions{},
		&RunningWorkloadsSummary{},
		&RunningWorkloadOptions{},
	)
}

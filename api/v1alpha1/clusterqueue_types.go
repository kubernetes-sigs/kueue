/*
Copyright 2021 The Kubernetes Authors.

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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ClusterQueueSpec defines the desired state of ClusterQueue
type ClusterQueueSpec struct {
	// requestableResources represent the total pod requests of workloads dispatched
	// via this clusterQueue. This doesn’t guarantee the actual availability of
	// resources, although an integration with a resource provisioner like Cluster
	// Autoscaler is possible to achieve that. Example:
	//
	// - name: cpu
	//   flavors:
	//   - quota:
	//       min: 100
	// - name: memory
	//   flavors:
	//   - quota:
	//       min: 100Gi
	//
	// +listType=map
	// +listMapKey=name
	RequestableResources []Resource `json:"requestableResources,omitempty"`

	// cohort that this ClusterQueue belongs to. QCs that belong to the
	// same cohort can borrow unused resources from each other.
	//
	// A QC can be a member of a single borrowing cohort. A workload submitted
	// to a queue referencing this QC can borrow resources from any QC in the
	// cohort. Only resources listed in the QC can be borrowed (see example).
	//
	// In the example below, the following applies:
	// 1. tenantB can run a workload consuming up to 20 k80 GPUs, meaning a resource
	//    can be allocated from more than one clusterQueue in a cohort.
	// 2. tenantB can not consume any p100 GPUs or spot because its QC has no quota
	//    defined for them, and so the max is implicitly 0.
	// 3. If both tenantA and tenantB are running jobs such that current usage for
	//    tenantA is lower than its min quota (e.g., 5 k80 GPUS) while
	//    tenantB’s usage is higher than its min quota (e.g., 12 k80 GPUs),
	//    and both tenants have pending jobs requesting the remaining clusterQueue of
	//    the cohort (the 3 k80 GPUs), then tenantA jobs will get this remaining
	//    clusterQueue since tenantA is below its min limit.
	// 4. If a tenantA workload doesn’t tolerate spot, then the workload will only
	//    be eligible to consume on-demand cores (the next in the list of cpu flavors).
	// 5. Before considering on-demand, the workload will get assigned spot if
	//    the quota can be borrowed from the cohort.
	//
	// metadata:
	//  name: tenantA
	// spec:
	//  cohort: borrowing-cohort
	//  requestableResources:
	// - name: cpu
	//   - name: spot
	//     quota:
	//       min: 1000
	//   - name: on-demand
	//     quota:
	//       min: 100
	// - name: nvidia.com/gpus
	//   - name: k80
	//     quota:
	//       min: 10
	//       max: 20
	//     labels:
	//     - cloud.provider.com/accelerator: nvidia-tesla-k80
	//   - name: p100
	//     quota:
	//       min: 10
	//       max: 20
	//     labels:
	//     - cloud.provider.com/accelerator: nvidia-tesla-p100
	//
	// metadata:
	//  name: tenantB
	// spec:
	//  cohort: borrowing-cohort
	//  requestableResources:
	// - name: cpu
	//   - name: on-demand
	//     quota:
	//       min: 100
	// - name: nvidia.com/gpus
	//   - name: k80
	//     quota:
	//       min: 10
	//       max: 20
	//     labels:
	//     - cloud.provider.com/accelerator: nvidia-tesla-k80
	//
	// If empty, this ClusterQueue cannot borrow from any other ClusterQueue and vice versa.
	//
	// The name style is similar to label keys. These are just names to link QCs
	// together, and they are meaningless otherwise.
	Cohort string `json:"cohort,omitempty"`

	// QueueingStrategy indicates the queueing strategy of the workloads
	// across the queues in this ClusterQueue. This field is immutable.
	// Current Supported Strategies:
	//
	// - StrictFIFO: workloads are ordered strictly by creation time.
	// Older workloads that can't be admitted will block admitting newer
	// workloads even if they fit available quota.
	// - BestEffortFIFO：workloads are ordered by creation time,
	// however older workloads that can't be admitted will not block
	// admitting newer workloads that fit existing quota.
	//
	// +kubebuilder:default=BestEffortFIFO
	// +kubebuilder:validation:Enum=StrictFIFO;BestEffortFIFO
	QueueingStrategy QueueingStrategy `json:"queueingStrategy,omitempty"`

	// namespaceSelector defines which namespaces are allowed to submit workloads to
	// this clusterQueue. Beyond this basic support for policy, an policy agent like
	// Gatekeeper should be used to enforce more advanced policies.
	// Defaults to null which is a nothing selector (no namespaces eligible).
	// If set to an empty selector `{}`, then all namespaces are eligible.
	NamespaceSelector *metav1.LabelSelector `json:"namespaceSelector,omitempty"`
}

type QueueingStrategy string

const (
	// StrictFIFO means that workloads are ordered strictly by creation time.
	// Older workloads that can't be admitted will block admitting newer
	// workloads even if they fit available quota.
	StrictFIFO QueueingStrategy = "StrictFIFO"

	// BestEffortFIFO means that workloads are ordered by creation time,
	// however older workloads that can't be admitted will not block
	// admitting newer workloads that fit existing quota.
	BestEffortFIFO QueueingStrategy = "BestEffortFIFO"
)

type Resource struct {
	// name of the resource. For example, cpu, memory or nvidia.com/gpu.
	Name corev1.ResourceName `json:"name"`

	// flavors is the list of different flavors of this resource and their limits.
	// Typically two different “flavors” of the same resource represent
	// different hardware models (e.g., gpu models, cpu architectures) or
	// pricing (on-demand vs spot cpus). The flavors are distinguished via labels and
	// taints.
	//
	// For example, if the resource is nvidia.com/gpu, and we want to define
	// different limits for different gpu models, then each model is mapped to a
	// flavor and must set different values of a shared key. For example:
	//
	// spec:
	//  requestableResources:
	// - name: nvidia.com/gpus
	//   - name: k80
	//     quota:
	//       min: 10
	//   - name: p100
	//     quota:
	//       min: 10
	//
	// The flavors are evaluated in order, selecting the first to satisfy a
	// workload’s requirements. Also the quantities are additive, in the example
	// above the GPU quota in total is 20 (10 k80 + 10 p100).
	// A workload is limited to the selected type by converting the labels to a node
	// selector that gets injected into the workload. This list can’t be empty, at
	// least one flavor must exist.
	//
	// +listType=map
	// +listMapKey=resourceFlavor
	Flavors []Flavor `json:"flavors,omitempty"`
}

type Flavor struct {
	// resourceFlavor is a reference to the resourceFlavor that defines this flavor.
	// +kubebuilder:default=default
	ResourceFlavor ResourceFlavorReference `json:"resourceFlavor"`

	// quota is the limit of resource usage at a point in time.
	Quota Quota `json:"quota"`
}

// ResourceFlavorReference is the name of the ResourceFlavor.
type ResourceFlavorReference string

type Quota struct {
	// min amount of resource requests that are available to be used by workloads
	// admitted by this ClusterQueue at a point in time.
	// The sum of min quotas for a flavor in a cohort defines the maximum amount
	// of resources that can be allocated by a ClusterQueue in the cohort.
	Min resource.Quantity `json:"min,omitempty"`

	// max is the upper limit on the amount of resource requests that
	// can be used by workloads admitted by this ClusterQueue at a point in time.
	// Resources can be borrowed from unused min quota of other
	// ClusterQueues in the same cohort.
	// If not null, it must be greater than or equal to min.
	// If null, there is no upper limit for borrowing.
	Max *resource.Quantity `json:"max,omitempty"`
}

// ClusterQueueStatus defines the observed state of ClusterQueue
type ClusterQueueStatus struct {
	// usedResources are the resources (by flavor) currently in use by the
	// workloads assigned to this clusterQueue.
	// +optional
	UsedResources UsedResources `json:"usedResources"`

	// PendingWorkloads is the number of workloads currently waiting to be
	// admitted to this clusterQueue.
	// +optional
	PendingWorkloads int32 `json:"pendingWorkloads"`

	// AdmittedWorkloads is the number of workloads currently admitted to this
	// clusterQueue and haven't finished yet.
	// +optional
	AdmittedWorkloads int32 `json:"admittedWorkloads"`
}

type UsedResources map[corev1.ResourceName]map[string]Usage

type Usage struct {
	// Total is the total quantity of the resource used, including resources
	// borrowed from the cohort.
	Total *resource.Quantity `json:"total,omitempty"`

	// Borrowed is the used quantity past the min quota, borrowed from the cohort.
	Borrowed *resource.Quantity `json:"borrowing,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:resource:scope=Cluster
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Cohort",JSONPath=".spec.cohort",type=string,description="Cohort that this ClusterQueue belongs to"
//+kubebuilder:printcolumn:name="Strategy",JSONPath=".spec.queueingStrategy",type=string,description="The queueing strategy used to prioritize workloads",priority=1
//+kubebuilder:printcolumn:name="Pending Workloads",JSONPath=".status.pendingWorkloads",type=integer,description="Number of pending workloads"
//+kubebuilder:printcolumn:name="Admitted Workloads",JSONPath=".status.admittedWorkloads",type=integer,description="Number of admitted workloads that haven't finished yet",priority=1

// ClusterQueue is the Schema for the clusterQueue API.
type ClusterQueue struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClusterQueueSpec   `json:"spec,omitempty"`
	Status ClusterQueueStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// ClusterQueueList contains a list of ClusterQueue
type ClusterQueueList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterQueue `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClusterQueue{}, &ClusterQueueList{})
}

// Copyright 2024 The Kubeflow Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// JAXJobDefaultPortName is name of the port used to communicate between Coordinator and Workers.
	JAXJobDefaultPortName = "jaxjob-port"
	// JAXJobDefaultContainerName is the name of the JAXJob container.
	JAXJobDefaultContainerName = "jax"
	// JAXJobDefaultPort is default value of the port.
	JAXJobDefaultPort = 6666
	// JAXJobDefaultRestartPolicy is default RestartPolicy for JAXReplicaSpecs.
	JAXJobDefaultRestartPolicy = RestartPolicyNever
	// JAXJobKind is the kind name.
	JAXJobKind = "JAXJob"
	// JAXJobPlural is the JAXJobPlural for JAXJob.
	JAXJobPlural = "jaxjobs"
	// JAXJobSingular is the singular for JAXJob.
	JAXJobSingular = "jaxjob"
	// JAXJobFrameworkName is the name of the ML Framework
	JAXJobFrameworkName = "jax"
	// JAXJobReplicaTypeWorker is the type for workers of distributed JAX.
	JAXJobReplicaTypeWorker ReplicaType = "Worker"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +resource:path=jaxjob
//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="State",type=string,JSONPath=`.status.conditions[-1:].type`
//+kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:subresource:scale:specpath=.spec.jaxReplicaSpecs.Worker.replicas,statuspath=.status.replicaStatuses.Worker.active,selectorpath=.status.replicaStatuses.Worker.selector

// JAXJob Represents a JAXJob resource.
type JAXJob struct {
	// Standard Kubernetes type metadata.
	metav1.TypeMeta `json:",inline"`

	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Specification of the desired state of the JAXJob.
	Spec JAXJobSpec `json:"spec,omitempty"`

	// Most recently observed status of the JAXJob.
	// Read-only (modified by the system).
	Status JobStatus `json:"status,omitempty"`
}

// JAXJobSpec is a desired state description of the JAXJob.
type JAXJobSpec struct {
	// RunPolicy encapsulates various runtime policies of the distributed training
	// job, for example how to clean up resources and how long the job can stay
	// active.
	//+kubebuilder:validation:Optional
	RunPolicy RunPolicy `json:"runPolicy"`

	// A map of JAXReplicaType (type) to ReplicaSpec (value). Specifies the JAX cluster configuration.
	// For example,
	//   {
	//     "Worker": JAXReplicaSpec,
	//   }
	JAXReplicaSpecs map[ReplicaType]*ReplicaSpec `json:"jaxReplicaSpecs"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +resource:path=jaxjobs
//+kubebuilder:object:root=true

// JAXJobList is a list of JAXJobs.
type JAXJobList struct {
	// Standard type metadata.
	metav1.TypeMeta `json:",inline"`

	// Standard list metadata.
	metav1.ListMeta `json:"metadata,omitempty"`

	// List of JAXJobs.
	Items []JAXJob `json:"items"`
}

func init() {
	SchemeBuilder.Register(&JAXJob{}, &JAXJobList{})
	SchemeBuilder.SchemeBuilder.Register(addJAXDefaultingFuncs)
}

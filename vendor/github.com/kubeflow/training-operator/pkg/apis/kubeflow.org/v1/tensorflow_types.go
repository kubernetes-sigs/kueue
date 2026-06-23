// Copyright 2020 The Kubeflow Authors
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
	// TFJobDefaultPortName is name of the port used to communicate between PS and
	// workers.
	TFJobDefaultPortName = "tfjob-port"
	// TFJobDefaultContainerName is the name of the TFJob container.
	TFJobDefaultContainerName = "tensorflow"
	// TFJobDefaultPort is default value of the port.
	TFJobDefaultPort = 2222
	// TFJobDefaultRestartPolicy is default RestartPolicy for TFReplicaSpec.
	TFJobDefaultRestartPolicy = RestartPolicyNever
	// TFJobKind is the kind name.
	TFJobKind = "TFJob"
	// TFJobPlural is the TensorflowPlural for TFJob.
	TFJobPlural = "tfjobs"
	// TFJobSingular is the singular for TFJob.
	TFJobSingular = "tfjob"
	// TFJobFrameworkName is the name of the ML Framework
	TFJobFrameworkName = "tensorflow"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +resource:path=tfjob
//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="State",type=string,JSONPath=`.status.conditions[-1:].type`
//+kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// TFJob represents a TFJob resource.
type TFJob struct {
	// Standard Kubernetes type metadata.
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Specification of the desired state of the TFJob.
	// +optional
	Spec TFJobSpec `json:"spec,omitempty"`

	// Most recently observed status of the TFJob.
	// Populated by the system.
	// Read-only.
	// +optional
	Status JobStatus `json:"status,omitempty"`
}

// TFJobSpec is a desired state description of the TFJob.
type TFJobSpec struct {
	// RunPolicy encapsulates various runtime policies of the distributed training
	// job, for example how to clean up resources and how long the job can stay
	// active.
	//+kubebuilder:validation:Optional
	RunPolicy RunPolicy `json:"runPolicy"`

	// SuccessPolicy defines the policy to mark the TFJob as succeeded.
	// Default to "", using the default rules.
	// +optional
	SuccessPolicy *SuccessPolicy `json:"successPolicy,omitempty"`

	// A map of TFReplicaType (type) to ReplicaSpec (value). Specifies the TF cluster configuration.
	// For example,
	//   {
	//     "PS": ReplicaSpec,
	//     "Worker": ReplicaSpec,
	//   }
	TFReplicaSpecs map[ReplicaType]*ReplicaSpec `json:"tfReplicaSpecs"`

	// A switch to enable dynamic worker
	EnableDynamicWorker bool `json:"enableDynamicWorker,omitempty"`
}

// SuccessPolicy is the success policy.
type SuccessPolicy string

const (
	SuccessPolicyDefault    SuccessPolicy = ""
	SuccessPolicyAllWorkers SuccessPolicy = "AllWorkers"
)

// TFReplicaType is the type for TFReplica. Can be one of: "Chief"/"Master" (semantically equivalent),
// "Worker", "PS", or "Evaluator".

const (
	// TFJobReplicaTypePS is the type for parameter servers of distributed TensorFlow.
	TFJobReplicaTypePS ReplicaType = "PS"

	// TFJobReplicaTypeWorker is the type for workers of distributed TensorFlow.
	// This is also used for non-distributed TensorFlow.
	TFJobReplicaTypeWorker ReplicaType = "Worker"

	// TFJobReplicaTypeChief is the type for chief worker of distributed TensorFlow.
	// If there is "chief" replica type, it's the "chief worker".
	// Else, worker:0 is the chief worker.
	TFJobReplicaTypeChief ReplicaType = "Chief"

	// TFJobReplicaTypeMaster is the type for master worker of distributed TensorFlow.
	// This is similar to chief, and kept just for backwards compatibility.
	TFJobReplicaTypeMaster ReplicaType = "Master"

	// TFJobReplicaTypeEval is the type for evaluation replica in TensorFlow.
	TFJobReplicaTypeEval ReplicaType = "Evaluator"
)

// IsChiefOrMaster returns true if the type is Master or Chief.
func IsChiefOrMaster(typ ReplicaType) bool {
	return typ == TFJobReplicaTypeChief || typ == TFJobReplicaTypeMaster
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +resource:path=tfjobs
// +kubebuilder:object:root=true

// TFJobList is a list of TFJobs.
type TFJobList struct {
	// Standard type metadata.
	metav1.TypeMeta `json:",inline"`

	// Standard list metadata.
	// +optional
	metav1.ListMeta `json:"metadata,omitempty"`

	// List of TFJobs.
	Items []TFJob `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TFJob{}, &TFJobList{})
	SchemeBuilder.SchemeBuilder.Register(addTensorflowDefaultingFuncs)
}

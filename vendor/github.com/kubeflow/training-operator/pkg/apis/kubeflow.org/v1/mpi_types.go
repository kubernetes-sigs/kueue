// Copyright 2019 The Kubeflow Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
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
	// MPIJobDefaultPortName is name of the port used to communicate between Master and Workers.
	MPIJobDefaultPortName = "mpi-port"
	// MPIJobDefaultPort is default value of the port.
	MPIJobDefaultPort = 9999
	// MPIJobDefaultContainerName is the name of the MPIJob container.
	MPIJobDefaultContainerName = "mpi"
	// MPIJobDefaultRestartPolicy is default RestartPolicy for ReplicaSpec.
	MPIJobDefaultRestartPolicy = RestartPolicyNever
	MPIJobKind                 = "MPIJob"
	// MPIJobPlural is the MPIJobPlural for TFJob.
	MPIJobPlural = "mpijobs"
	// MPIJobSingular is the singular for TFJob.
	MPIJobSingular = "mpijob"
	// MPIJobFrameworkName is the name of the ML Framework
	MPIJobFrameworkName = "mpi"
	// MPIJobReplicaTypeLauncher is the type for launcher replica.
	MPIJobReplicaTypeLauncher ReplicaType = "Launcher"
	// MPIJobReplicaTypeWorker is the type for worker replicas.
	MPIJobReplicaTypeWorker ReplicaType = "Worker"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +resource:path=mpijob
// +kubebuilder:object:root=true
// +kubebuilder:printcolumn:JSONPath=`.metadata.creationTimestamp`,name="Age",type=date
// +kubebuilder:printcolumn:JSONPath=`.status.conditions[-1:].type`,name="State",type=string
// +kubebuilder:subresource:status

type MPIJob struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              MPIJobSpec `json:"spec,omitempty"`
	Status            JobStatus  `json:"status,omitempty"`
}

type MPIJobSpec struct {

	// Specifies the number of slots per worker used in hostfile.
	// Defaults to 1.
	// +optional
	SlotsPerWorker *int32 `json:"slotsPerWorker,omitempty"`

	// CleanPodPolicy defines the policy that whether to kill pods after the job completes.
	// Defaults to None.
	CleanPodPolicy *CleanPodPolicy `json:"cleanPodPolicy,omitempty"`

	// `MPIReplicaSpecs` contains maps from `MPIReplicaType` to `ReplicaSpec` that
	// specify the MPI replicas to run.
	MPIReplicaSpecs map[ReplicaType]*ReplicaSpec `json:"mpiReplicaSpecs"`

	// MainContainer specifies name of the main container which
	// executes the MPI code.
	MainContainer string `json:"mainContainer,omitempty"`

	// `RunPolicy` encapsulates various runtime policies of the distributed training
	// job, for example how to clean up resources and how long the job can stay
	// active.
	RunPolicy RunPolicy `json:"runPolicy,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +resource:path=mpijobs
// +kubebuilder:object:root=true

type MPIJobList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MPIJob `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MPIJob{}, &MPIJobList{})
	SchemeBuilder.SchemeBuilder.Register(addMPIJobDefaultingFuncs)
}

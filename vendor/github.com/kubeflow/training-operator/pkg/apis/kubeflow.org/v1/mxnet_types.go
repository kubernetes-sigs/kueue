// Copyright 2021 The Kubeflow Authors
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
	// MXJobDefaultPortName is name of the port used to communicate between scheduler and
	// servers & workers.
	MXJobDefaultPortName = "mxjob-port"
	// MXJobDefaultContainerName is the name of the MXJob container.
	MXJobDefaultContainerName = "mxnet"
	// MXJobDefaultPort is default value of the port.
	MXJobDefaultPort = 9091
	// MXJobDefaultRestartPolicy is default RestartPolicy for MXReplicaSpec.
	MXJobDefaultRestartPolicy = RestartPolicyNever
	// MXJobKind is the kind name.
	MXJobKind = "MXJob"
	// MXJobPlural is the MXNetPlural for mxJob.
	MXJobPlural = "mxjobs"
	// MXJobSingular is the singular for mxJob.
	MXJobSingular = "mxjob"
	// MXJobFrameworkName is the name of the ML Framework
	MXJobFrameworkName = "mxnet"
	// MXJobReplicaTypeScheduler is the type for scheduler replica in MXNet.
	MXJobReplicaTypeScheduler ReplicaType = "Scheduler"

	// MXJobReplicaTypeServer is the type for parameter servers of distributed MXNet.
	MXJobReplicaTypeServer ReplicaType = "Server"

	// MXJobReplicaTypeWorker is the type for workers of distributed MXNet.
	// This is also used for non-distributed MXNet.
	MXJobReplicaTypeWorker ReplicaType = "Worker"

	// MXJobReplicaTypeTunerTracker
	// This the auto-tuning tracker e.g. autotvm tracker, it will dispatch tuning task to TunerServer
	MXJobReplicaTypeTunerTracker ReplicaType = "TunerTracker"

	// MXJobReplicaTypeTunerServer
	MXJobReplicaTypeTunerServer ReplicaType = "TunerServer"

	// MXJobReplicaTypeTuner is the type for auto-tuning of distributed MXNet.
	// This is also used for non-distributed MXNet.
	MXJobReplicaTypeTuner ReplicaType = "Tuner"
)

// MXJobSpec defines the desired state of MXJob
type MXJobSpec struct {
	// RunPolicy encapsulates various runtime policies of the distributed training
	// job, for example how to clean up resources and how long the job can stay
	// active.
	//+kubebuilder:validation:Optional
	RunPolicy RunPolicy `json:"runPolicy"`

	// JobMode specify the kind of MXjob to do. Different mode may have
	// different MXReplicaSpecs request
	JobMode JobModeType `json:"jobMode"`

	// MXReplicaSpecs is map of ReplicaType and ReplicaSpec
	// specifies the MX replicas to run.
	// For example,
	//   {
	//     "Scheduler": ReplicaSpec,
	//     "Server": ReplicaSpec,
	//     "Worker": ReplicaSpec,
	//   }
	MXReplicaSpecs map[ReplicaType]*ReplicaSpec `json:"mxReplicaSpecs"`
}

// JobModeType id the type for JobMode
type JobModeType string

const (
	// Train Mode, in this mode requested MXReplicaSpecs need
	// has Server, Scheduler, Worker
	MXTrain JobModeType = "MXTrain"

	// Tune Mode, in this mode requested MXReplicaSpecs need
	// has Tuner
	MXTune JobModeType = "MXTune"
)

// MXJobStatus defines the observed state of MXJob
type MXJobStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
}

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +resource:path=mxjob
//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="State",type=string,JSONPath=`.status.conditions[-1:].type`
//+kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// MXJob is the Schema for the mxjobs API
type MXJob struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MXJobSpec `json:"spec,omitempty"`
	Status JobStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// MXJobList contains a list of MXJob
type MXJobList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MXJob `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MXJob{}, &MXJobList{})
	SchemeBuilder.SchemeBuilder.Register(addMXNetDefaultingFuncs)
}

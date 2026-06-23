// Copyright 2022 The Kubeflow Authors
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
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// PaddleJobDefaultPortName is name of the port used to communicate between Master and
	// workers.
	PaddleJobDefaultPortName = "master"
	// PaddleJobDefaultContainerName is the name of the PaddleJob container.
	PaddleJobDefaultContainerName = "paddle"
	// PaddleJobDefaultPort is default value of the port.
	PaddleJobDefaultPort = 36543
	// PaddleJobDefaultRestartPolicy is default RestartPolicy for PaddleReplicaSpec.
	PaddleJobDefaultRestartPolicy = RestartPolicyOnFailure
	// PaddleJobKind is the kind name.
	PaddleJobKind = "PaddleJob"
	// PaddleJobPlural is the PaddlePlural for paddleJob.
	PaddleJobPlural = "paddlejobs"
	// PaddleJobSingular is the singular for paddleJob.
	PaddleJobSingular = "paddlejob"
	// PaddleJobFrameworkName is the name of the ML Framework
	PaddleJobFrameworkName = "paddle"
	// PaddleJobReplicaTypeMaster is the type of Master of distributed Paddle
	PaddleJobReplicaTypeMaster ReplicaType = "Master"
	// PaddleJobReplicaTypeWorker is the type for workers of distributed Paddle.
	PaddleJobReplicaTypeWorker ReplicaType = "Worker"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +resource:path=paddlejob
//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="State",type=string,JSONPath=`.status.conditions[-1:].type`
//+kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:subresource:scale:specpath=.spec.paddleReplicaSpecs.Worker.replicas,statuspath=.status.replicaStatuses.Worker.active,selectorpath=.status.replicaStatuses.Worker.selector

// PaddleJob Represents a PaddleJob resource.
type PaddleJob struct {
	// Standard Kubernetes type metadata.
	metav1.TypeMeta `json:",inline"`

	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Specification of the desired state of the PaddleJob.
	Spec PaddleJobSpec `json:"spec,omitempty"`

	// Most recently observed status of the PaddleJob.
	// Read-only (modified by the system).
	Status JobStatus `json:"status,omitempty"`
}

// PaddleJobSpec is a desired state description of the PaddleJob.
type PaddleJobSpec struct {
	// RunPolicy encapsulates various runtime policies of the distributed training
	// job, for example how to clean up resources and how long the job can stay
	// active.
	//+kubebuilder:validation:Optional
	RunPolicy RunPolicy `json:"runPolicy"`

	// ElasticPolicy holds the elastic policy for paddle job.
	ElasticPolicy *PaddleElasticPolicy `json:"elasticPolicy,omitempty"`

	// A map of PaddleReplicaType (type) to ReplicaSpec (value). Specifies the Paddle cluster configuration.
	// For example,
	//   {
	//     "Master": PaddleReplicaSpec,
	//     "Worker": PaddleReplicaSpec,
	//   }
	PaddleReplicaSpecs map[ReplicaType]*ReplicaSpec `json:"paddleReplicaSpecs"`
}

type PaddleElasticPolicy struct {
	// minReplicas is the lower limit for the number of replicas to which the training job
	// can scale down.  It defaults to null.
	// +optional
	MinReplicas *int32 `json:"minReplicas,omitempty"`
	// upper limit for the number of pods that can be set by the autoscaler; cannot be smaller than MinReplicas, defaults to null.
	// +optional
	MaxReplicas *int32 `json:"maxReplicas,omitempty"`

	// MaxRestarts is the limit for restart times of pods in elastic mode.
	// +optional
	MaxRestarts *int32 `json:"maxRestarts,omitempty"`

	// Metrics contains the specifications which are used to calculate the
	// desired replica count (the maximum replica count across all metrics will
	// be used).  The desired replica count is calculated with multiplying the
	// ratio between the target value and the current value by the current
	// number of pods. Ergo, metrics used must decrease as the pod count is
	// increased, and vice-versa.  See the individual metric source types for
	// more information about how each type of metric must respond.
	// If not set, the HPA will not be created.
	// +optional
	Metrics []autoscalingv2.MetricSpec `json:"metrics,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +resource:path=paddlejobs
//+kubebuilder:object:root=true

// PaddleJobList is a list of PaddleJobs.
type PaddleJobList struct {
	// Standard type metadata.
	metav1.TypeMeta `json:",inline"`

	// Standard list metadata.
	metav1.ListMeta `json:"metadata,omitempty"`

	// List of PaddleJobs.
	Items []PaddleJob `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PaddleJob{}, &PaddleJobList{})
	SchemeBuilder.SchemeBuilder.Register(addPaddleDefaultingFuncs)
}

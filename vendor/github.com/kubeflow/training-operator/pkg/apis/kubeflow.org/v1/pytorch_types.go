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
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// PyTorchJobDefaultPortName is name of the port used to communicate between Master and
	// workers.
	PyTorchJobDefaultPortName = "pytorchjob-port"
	// PyTorchJobDefaultContainerName is the name of the PyTorchJob container.
	PyTorchJobDefaultContainerName = "pytorch"
	// PyTorchJobDefaultPort is default value of the port.
	PyTorchJobDefaultPort = 23456
	// PyTorchJobDefaultRestartPolicy is default RestartPolicy for PyTorchReplicaSpec.
	PyTorchJobDefaultRestartPolicy = RestartPolicyOnFailure
	// PyTorchJobKind is the kind name.
	PyTorchJobKind = "PyTorchJob"
	// PyTorchJobPlural is the PyTorchPlural for pytorchJob.
	PyTorchJobPlural = "pytorchjobs"
	// PyTorchJobSingular is the singular for pytorchJob.
	PyTorchJobSingular = "pytorchjob"
	// PyTorchJobFrameworkName is the name of the ML Framework
	PyTorchJobFrameworkName = "pytorch"
	// PyTorchJobReplicaTypeMaster is the type of Master of distributed PyTorch
	PyTorchJobReplicaTypeMaster ReplicaType = "Master"
	// PyTorchJobReplicaTypeWorker is the type for workers of distributed PyTorch.
	PyTorchJobReplicaTypeWorker ReplicaType = "Worker"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +resource:path=pytorchjob
//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="State",type=string,JSONPath=`.status.conditions[-1:].type`
//+kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:subresource:scale:specpath=.spec.pytorchReplicaSpecs.Worker.replicas,statuspath=.status.replicaStatuses.Worker.active,selectorpath=.status.replicaStatuses.Worker.selector

// PyTorchJob Represents a PyTorchJob resource.
type PyTorchJob struct {
	// Standard Kubernetes type metadata.
	metav1.TypeMeta `json:",inline"`

	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Specification of the desired state of the PyTorchJob.
	Spec PyTorchJobSpec `json:"spec,omitempty"`

	// Most recently observed status of the PyTorchJob.
	// Read-only (modified by the system).
	Status JobStatus `json:"status,omitempty"`
}

// For PyTorch launch/run related spec declaration, please see the following doc for more detail:
// https://pytorch.org/docs/stable/elastic/run.html
// Or run command `torchrun --help` for a brief description.

// PyTorchJobSpec is a desired state description of the PyTorchJob.
type PyTorchJobSpec struct {
	// RunPolicy encapsulates various runtime policies of the distributed training
	// job, for example how to clean up resources and how long the job can stay
	// active.
	//+kubebuilder:validation:Optional
	RunPolicy RunPolicy `json:"runPolicy"`

	ElasticPolicy *ElasticPolicy `json:"elasticPolicy,omitempty"`

	// A map of PyTorchReplicaType (type) to ReplicaSpec (value). Specifies the PyTorch cluster configuration.
	// For example,
	//   {
	//     "Master": PyTorchReplicaSpec,
	//     "Worker": PyTorchReplicaSpec,
	//   }
	PyTorchReplicaSpecs map[ReplicaType]*ReplicaSpec `json:"pytorchReplicaSpecs"`

	// Number of workers per node; supported values: [auto, cpu, gpu, int].
	// For more, https://github.com/pytorch/pytorch/blob/26f7f470df64d90e092081e39507e4ac751f55d6/torch/distributed/run.py#L629-L658.
	// Defaults to auto.
	NprocPerNode *string `json:"nprocPerNode,omitempty"`
}

type ElasticPolicy struct {
	// minReplicas is the lower limit for the number of replicas to which the training job
	// can scale down.  It defaults to null.
	// +optional
	MinReplicas *int32 `json:"minReplicas,omitempty"`
	// upper limit for the number of pods that can be set by the autoscaler; cannot be smaller than MinReplicas, defaults to null.
	// +optional
	MaxReplicas *int32 `json:"maxReplicas,omitempty"`

	RDZVBackend *RDZVBackend `json:"rdzvBackend,omitempty"`
	RDZVPort    *int32       `json:"rdzvPort,omitempty"`
	RDZVHost    *string      `json:"rdzvHost,omitempty"`
	RDZVID      *string      `json:"rdzvId,omitempty"`
	// RDZVConf contains additional rendezvous configuration (<key1>=<value1>,<key2>=<value2>,...).
	RDZVConf []RDZVConf `json:"rdzvConf,omitempty"`
	// Start a local standalone rendezvous backend that is represented by a C10d TCP store
	// on port 29400. Useful when launching single-node, multi-worker job. If specified
	// --rdzv_backend, --rdzv_endpoint, --rdzv_id are auto-assigned; any explicitly set values
	// are ignored.
	Standalone *bool `json:"standalone,omitempty"`
	// Number of workers per node; supported values: [auto, cpu, gpu, int].
	// Deprecated: This API is deprecated in v1.7+
	// Use .spec.nprocPerNode instead.
	NProcPerNode *int32 `json:"nProcPerNode,omitempty"`

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

type RDZVConf struct {
	Key   string `json:"key,omitempty"`
	Value string `json:"value,omitempty"`
}

type RDZVBackend string

const (
	// BackendC10D is the rendezvous backend type for C10d.
	BackendC10D RDZVBackend = "c10d"
	// BackendETCD is the rendezvous backend type for ETCD.
	BackendETCD RDZVBackend = "etcd"
	// BackendETCDV2 is the rendezvous backend type for ETCD v2.
	BackendETCDV2 RDZVBackend = "etcd-v2"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +resource:path=pytorchjobs
//+kubebuilder:object:root=true

// PyTorchJobList is a list of PyTorchJobs.
type PyTorchJobList struct {
	// Standard type metadata.
	metav1.TypeMeta `json:",inline"`

	// Standard list metadata.
	metav1.ListMeta `json:"metadata,omitempty"`

	// List of PyTorchJobs.
	Items []PyTorchJob `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PyTorchJob{}, &PyTorchJobList{})
	SchemeBuilder.SchemeBuilder.Register(addPyTorchDefaultingFuncs)
}

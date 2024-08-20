/*
Copyright 2024 The Kubernetes Authors.

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

// VolumeBundleReference is the name of the VolumeBundle.
//
// +kubebuilder:validation:MaxLength=253
// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
type VolumeBundleReference string

type ApplicationProfileMode string

const (
	InteractiveMode ApplicationProfileMode = "Interactive"
	JobMode         ApplicationProfileMode = "Job"
	RayJobMode      ApplicationProfileMode = "RayJob"
	RayClusterMode  ApplicationProfileMode = "RayCluster"
	SlurmMode       ApplicationProfileMode = "Slurm"
)

// +kubebuilder:validation:Enum=cmd;parallelism;completions;replicas;min-replicas;max-replicas;request;localqueue;raycluster;array;cpus-per-task;stderr;gpus-per-task;input;job-name;mem-per-cpu;mem-per-gpu;mem-per-task;nodes;ntasks;stdout;partition
type Flag string

const (
	CmdFlag         Flag = "cmd"
	ParallelismFlag Flag = "parallelism"
	CompletionsFlag Flag = "completions"
	ReplicasFlag    Flag = "replicas"
	MinReplicasFlag Flag = "min-replicas"
	MaxReplicasFlag Flag = "max-replicas"
	RequestFlag     Flag = "request"
	LocalQueueFlag  Flag = "localqueue"
	RayClusterFlag  Flag = "raycluster"
	ArrayFlag       Flag = "array"
	CpusPerTaskFlag Flag = "cpus-per-task"
	ErrorFlag       Flag = "stderr"
	GpusPerTaskFlag Flag = "gpus-per-task"
	InputFlag       Flag = "input"
	JobNameFlag     Flag = "job-name"
	MemPerCPUFlag   Flag = "mem-per-cpu"
	MemPerGPUFlag   Flag = "mem-per-gpu"
	MemPerTaskFlag  Flag = "mem-per-task"
	NodesFlag       Flag = "nodes"
	NTasksFlag      Flag = "ntasks"
	OutputFlag      Flag = "stdout"
	PartitionFlag   Flag = "partition"
)

// TemplateReference is the name of the template.
//
// +kubebuilder:validation:MaxLength=253
// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
type TemplateReference string

// TODO: Fix validations...
// +kubebuilder:validation:XValidation:rule="!has(self.requiredFlags) || self.name == 'RayJob' || self.name == 'RayCluster' || (self.name == 'Interactive' || self.name == 'Job' || self.name == 'Slurm') && !('replicas' in self.requiredFlags)", message="replicas flag can be used only on RayJob and RayCluster modes"
// +kubebuilder:validation:XValidation:rule="!has(self.requiredFlags) || self.name == 'RayJob' || self.name == 'RayCluster' || (self.name == 'Interactive' || self.name == 'Job' || self.name == 'Slurm') && !('min-replicas' in self.requiredFlags)", message="min-replicas flag can be used only on RayJob and RayCluster modes"
// +kubebuilder:validation:XValidation:rule="!has(self.requiredFlags) || self.name == 'RayJob' || self.name == 'RayCluster' || (self.name == 'Interactive' || self.name == 'Job' || self.name == 'Slurm') && !('max-replicas' in self.requiredFlags)", message="max-replicas flag can be used only on RayJob and RayCluster modes"
// +kubebuilder:validation:XValidation:rule="!has(self.requiredFlags) || self.name == 'Interactive' || self.name == 'Job' || (self.name == 'RayJob' || self.name == 'RayCluster' || self.name == 'Slurm') && !('request' in self.requiredFlags)", message="request flag can be used only on Job and Interactive modes"
// +kubebuilder:validation:XValidation:rule="!has(self.requiredFlags) || self.name == 'Interactive' || self.name == 'Job' || self.name == 'RayJob' || (self.name == 'RayCluster' || self.name == 'Slurm') && !('cmd' in self.requiredFlags) ", message="cmd flag can be used only on Job, Interactive and RayJob modes"
// +kubebuilder:validation:XValidation:rule="!has(self.requiredFlags) || self.name == 'RayJob' || (self.name == 'Interactive' || self.name == 'Job' || self.name == 'RayCluster' || self.name == 'Slurm') && !('raycluster' in self.requiredFlags)", message="raycluster flag can be used only on RayJob mode"
// +kubebuilder:validation:XValidation:rule="!has(self.requiredFlags) || !('raycluster' in self.requiredFlags) || (!('localqueue' in self.requiredFlags) && !('replicas' in self.requiredFlags)  && !('min-replicas' in self.requiredFlags)  && !('max-replicas' in self.requiredFlags) )", message="if raycluster flag are set none of localqueue, replicas, min-replicas and max-replicas can be"
// +kubebuilder:validation:XValidation:rule="!has(self.requiredFlags) || self.name == 'Slurm' || self.name != 'Slurm' && !('array' in self.requiredFlags)", message="array flag can be used only on Slurm mode"
// +kubebuilder:validation:XValidation:rule="!has(self.requiredFlags) || self.name == 'Slurm' || self.name != 'Slurm' && !('cpus-per-task' in self.requiredFlags)", message="cpus-per-task flag can be used only on Slurm mode"
// +kubebuilder:validation:XValidation:rule="!has(self.requiredFlags) || self.name == 'Slurm' || self.name != 'Slurm' && !('stderr' in self.requiredFlags)", message="stderr flag can be used only on Slurm mode"
// +kubebuilder:validation:XValidation:rule="!has(self.requiredFlags) || self.name == 'Slurm' || self.name != 'Slurm' && !('gpus-per-task' in self.requiredFlags)", message="gpus-per-task flag can be used only on Slurm mode"
// +kubebuilder:validation:XValidation:rule="!has(self.requiredFlags) || self.name == 'Slurm' || self.name != 'Slurm' && !('input' in self.requiredFlags)", message="input flag can be used only on Slurm mode"
// +kubebuilder:validation:XValidation:rule="!has(self.requiredFlags) || self.name == 'Slurm' || self.name != 'Slurm' && !('job-name' in self.requiredFlags)", message="job-name flag can be used only on Slurm mode"
// +kubebuilder:validation:XValidation:rule="!has(self.requiredFlags) || self.name == 'Slurm' || self.name != 'Slurm' && !('mem-per-cpu' in self.requiredFlags)", message="mem-per-cpu flag can be used only on Slurm mode"
// +kubebuilder:validation:XValidation:rule="!has(self.requiredFlags) || self.name == 'Slurm' || self.name != 'Slurm' && !('mem-per-gpu' in self.requiredFlags)", message="mem-per-gpu flag can be used only on Slurm mode"
// +kubebuilder:validation:XValidation:rule="!has(self.requiredFlags) || self.name == 'Slurm' || self.name != 'Slurm' && !('mem-per-task' in self.requiredFlags)", message="mem-per-task flag can be used only on Slurm mode"
// +kubebuilder:validation:XValidation:rule="!has(self.requiredFlags) || self.name == 'Slurm' || self.name != 'Slurm' && !('nodes' in self.requiredFlags)", message="nodes flag can be used only on Slurm mode"
// +kubebuilder:validation:XValidation:rule="!has(self.requiredFlags) || self.name == 'Slurm' || self.name != 'Slurm' && !('ntasks' in self.requiredFlags)", message="ntasks flag can be used only on Slurm mode"
// +kubebuilder:validation:XValidation:rule="!has(self.requiredFlags) || self.name == 'Slurm' || self.name != 'Slurm' && !('stdout' in self.requiredFlags)", message="stdout flag can be used only on Slurm mode"
// +kubebuilder:validation:XValidation:rule="!has(self.requiredFlags) || self.name == 'Slurm' || self.name != 'Slurm' && !('partition' in self.requiredFlags)", message="partition flag can be used only on Slurm mode"
type SupportedMode struct {
	// name determines which template will be used and which object will eventually be created.
	// Possible values are Interactive, Job, RayJob, RayCluster and Slurm.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=Interactive;Job;RayJob;RayCluster;Slurm
	Name ApplicationProfileMode `json:"name"`

	// template is the name of the template.
	// Template type depends on ApplicationProfileMode:
	//   - on Interactive mode it must be v1/PodTemplate
	//   - on Job mode it must be kjobctl.x-k8s.io/v1alpha1/JobTemplate
	//   - on RayJob mode it must be kjobctl.x-k8s.io/v1alpha1/RayJobTemplate
	//   - on RayCluster mode it must be kjobctl.x-k8s.io/v1alpha1/RayClusterTemplate
	//   - on Slurm mode it must be kjobctl.x-k8s.io/v1alpha1/JobTemplate
	//
	// +kubebuilder:validation:Required
	Template TemplateReference `json:"template"`

	// requiredFlags point which cli flags are required to be passed in order to fill the gaps in the templates.
	// Possible values are cmd, parallelism, completions, replicas, min-replicas, max-replicas, request, localqueue, and raycluster.
	// replicas, min-replicas, and max-replicas flags used only for RayJob and RayCluster mode.
	// The raycluster flag used only for the RayJob mode.
	// The request flag used only for Interactive and Job modes.
	// The cmd flag used only for Interactive, Job, and RayJob.
	// If the raycluster flag are set, none of localqueue, replicas, min-replicas, or max-replicas can be set.
	// For the Slurm mode, the possible values are: array, cpus-per-task, stderr, gpus-per-task, input, job-name, mem-per-cpu,
	// mem-per-gpu, mem-per-task, nodes, ntasks, stdout, partition.
	//
	// cmd and requests values are going to be added only to the first primary container.
	//
	// +optional
	// +listType=set
	// +kubebuilder:validation:MaxItems=8
	RequiredFlags []Flag `json:"requiredFlags,omitempty"`
}

// ApplicationProfileSpec defines the desired state of ApplicationProfile
type ApplicationProfileSpec struct {
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:Required
	SupportedModes []SupportedMode `json:"supportedModes"`

	// +optional
	// +listType=set
	VolumeBundles []VolumeBundleReference `json:"volumeBundles,omitempty"`
}

// +genclient
// +kubebuilder:object:root=true

// ApplicationProfile is the Schema for the applicationprofiles API
type ApplicationProfile struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ApplicationProfileSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// ApplicationProfileList contains a list of ApplicationProfile
type ApplicationProfileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []ApplicationProfile `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ApplicationProfile{}, &ApplicationProfileList{})
}

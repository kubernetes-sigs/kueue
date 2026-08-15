/*
Copyright The Kubeflow Authors.

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

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// +kubebuilder:validation:Enum=Maximize;Minimize
type ObjectiveDirection string

const (
	ObjectiveDirectionMaximize ObjectiveDirection = "Maximize"
	ObjectiveDirectionMinimize ObjectiveDirection = "Minimize"
)

// OptimizationJob is the Schema for the optimizationjobs API.
// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// OptimizationJob is the Schema for the optimizationjobs API.
type OptimizationJob struct {
	// typeMeta is the type meta for the optimization job.
	metav1.TypeMeta `json:",inline"`

	// metadata is the object meta for the optimization job.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec is the spec for the optimization job.
	// +required
	Spec OptimizationJobSpec `json:"spec,omitempty,omitzero"`

	// status is the status for the optimization job.
	// +optional
	Status *OptimizationJobStatus `json:"status,omitempty,omitzero"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// OptimizationJobList contains a list of OptimizationJob.
type OptimizationJobList struct {
	// typeMeta is the type meta for the optimization job list.
	metav1.TypeMeta `json:",inline"`

	// listMeta is the list meta for the optimization job list.
	metav1.ListMeta `json:"metadata,omitempty"`

	// items is the list of optimization jobs.
	Items []OptimizationJob `json:"items"`
}

// OptimizationJobSpec defines the desired state of OptimizationJob.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="OptimizationJobSpec is immutable and cannot be updated after creation"
// +kubebuilder:validation:XValidation:rule="self.parallelTrials <= self.numTrials",message="parallelTrials cannot exceed numTrials"
// +kubebuilder:validation:XValidation:rule="!has(self.searchAlgorithm.grid) || self.parameters.all(p, has(p.searchSpace.categorical))",message="Grid search requires all parameters to be Categorical; Uniform and LogUniform are not supported."
type OptimizationJobSpec struct {
	// objectives is the list of objectives to optimize.
	// +listType=map
	// +listMapKey=metric
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=1
	// +required
	Objectives []Objective `json:"objectives,omitempty"`

	// searchAlgorithm is the algorithm to use for searching over the hyperparameters.
	// +kubebuilder:default={random: {}}
	// +optional
	SearchAlgorithm *SearchAlgorithm `json:"searchAlgorithm,omitempty"`

	// parameters is the list of hyperparameters to search over.
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=100
	// +required
	Parameters []Parameter `json:"parameters,omitempty"`

	// numTrials is the total number of trials to run.
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	// +optional
	NumTrials int32 `json:"numTrials,omitempty"`

	// parallelTrials is the number of trials to run in parallel. Defaults to 1.
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	// +optional
	ParallelTrials int32 `json:"parallelTrials,omitempty"`

	// trainJobTemplate is the template for the train job to run.
	// +required
	TrainJobTemplate TrainJobTemplateSpec `json:"trainJobTemplate,omitzero"`
}

type Objective struct {
	// metric specifies the name of the objective metric to track.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	// +required
	Metric string `json:"metric,omitempty"`

	// direction specifies the optimization goal. Defaults to "Minimize".
	// +kubebuilder:default=Minimize
	// +optional
	Direction ObjectiveDirection `json:"direction,omitempty"`
}

// +kubebuilder:validation:ExactlyOneOf=random;grid
type SearchAlgorithm struct {
	// random is the random search algorithm.
	// +optional
	Random *RandomAlgorithm `json:"random,omitempty"`

	// grid is the grid search algorithm.
	// +optional
	Grid *GridAlgorithm `json:"grid,omitempty"`
}

// RandomAlgorithm is the random search algorithm.
type RandomAlgorithm struct {
	// seed is the seed for the random search algorithm.
	// +optional
	Seed *int64 `json:"seed,omitempty"`
}

// GridAlgorithm is the grid search algorithm.
type GridAlgorithm struct{}

// SearchSpace acts as a Discriminated Union (OneOf) supporting flexible statistical distributions.
// +kubebuilder:validation:ExactlyOneOf=uniform;logUniform;categorical
type SearchSpace struct {
	// uniform is the uniform search space.
	// +optional
	Uniform UniformSpace `json:"uniform,omitempty,omitzero"`

	// logUniform is the log-uniform search space.
	// +optional
	LogUniform LogUniformSpace `json:"logUniform,omitempty,omitzero"`

	// categorical is the categorical search space.
	// +optional
	Categorical CategoricalSpace `json:"categorical,omitempty,omitzero"`
}

// +kubebuilder:validation:Pattern="^-?(0|[1-9][0-9]*)(\\.[0-9]+)?([eE][+-]?[0-9]+)?$"
// +kubebuilder:validation:MaxLength=64
type Double string

// UniformSpace defines a continuous uniform distribution over [Min, Max].
// +kubebuilder:validation:XValidation:rule="double(self.min) < double(self.max)",message="min must be strictly less than max"
type UniformSpace struct {
	// min is the minimum value of the uniform search space.
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:MinLength=1
	// +required
	Min Double `json:"min,omitempty"`

	// max is the maximum value of the uniform search space.
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:MinLength=1
	// +required
	Max Double `json:"max,omitempty"`

	// type specifies the underlying data type. Defaults to "Float".
	// +kubebuilder:default=Float
	// +optional
	Type ParameterType `json:"type,omitempty"`
}

// LogUniformSpace defines a continuous log-uniform distribution over [Min, Max].
// +kubebuilder:validation:XValidation:rule="double(self.min) > 0.0",message="min must be strictly greater than 0"
// +kubebuilder:validation:XValidation:rule="double(self.min) < double(self.max)",message="min must be strictly less than max"
type LogUniformSpace struct {
	// min is the minimum value of the log-uniform search space.
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:MinLength=1
	// +required
	Min Double `json:"min,omitempty"`

	// max is the maximum value of the log-uniform search space.
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:MinLength=1
	// +required
	Max Double `json:"max,omitempty"`

	// type specifies the underlying data type. Defaults to "Float".
	// +kubebuilder:default=Float
	// +optional
	Type ParameterType `json:"type,omitempty"`
}

// ParameterType is the type of the parameter.
// +kubebuilder:validation:Enum=Int;Float
type ParameterType string

const (
	ParameterTypeInt   ParameterType = "Int"
	ParameterTypeFloat ParameterType = "Float"
)

// CategoricalSpace defines a search space over a discrete set of unordered strings.
type CategoricalSpace struct {
	// choices is the set of strings to sample from.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=100
	// +kubebuilder:validation:items:MaxLength=64
	// +listType=set
	// +required
	Choices []string `json:"choices,omitempty"`
}

type Parameter struct {
	// name is the name of the hyperparameter.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	// +required
	Name string `json:"name,omitempty"`

	// searchSpace is the search space for the hyperparameter.
	// +required
	SearchSpace *SearchSpace `json:"searchSpace,omitempty"`
}

// ParameterAssignment represents a single hyperparameter and its assigned value.
type ParameterAssignment struct {
	// name is the name of the hyperparameter.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	// +required
	Name string `json:"name,omitempty"`

	// value is the value of the hyperparameter.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	// +required
	Value string `json:"value,omitempty"`
}

// TrainJobTemplateSpec is the template for the train job to run.
type TrainJobTemplateSpec struct {
	// metadata is the metadata for the train job.
	// +optional
	// +kubebuilder:validation:XValidation:rule="!has(self.name) && !has(self.namespace)", message="name and namespace cannot be set in a template."
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec is the spec for the train job.
	// +required
	Spec TrainJobSpec `json:"spec,omitzero"`
}

// OptimizationJobStatus is the status of the optimization job.
type OptimizationJobStatus struct {
	// conditions is the list of conditions for the optimization job.
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MaxItems=100
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// result is the result of the optimization job.
	// +optional
	Result Result `json:"result,omitempty,omitzero"`
}

// Result tracks the parameters of the highest performing trial.
type Result struct {
	// trainJobName is the name of the underlying TrainJob that achieved this result.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +required
	TrainJobName string `json:"trainJobName,omitempty"`

	// parameters is the list of parameters for the result.
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=100
	// +optional
	Parameters []ParameterAssignment `json:"parameters,omitempty"`
}

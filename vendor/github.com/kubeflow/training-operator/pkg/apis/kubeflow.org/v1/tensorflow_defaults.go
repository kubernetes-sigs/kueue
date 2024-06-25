// Copyright 2018 The Kubeflow Authors
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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// addTensorflowDefaultingFuncs is used to register default funcs
func addTensorflowDefaultingFuncs(scheme *runtime.Scheme) error {
	return RegisterDefaults(scheme)
}

// setTensorflowDefaultPort sets the default ports for tensorflow container.
func setTensorflowDefaultPort(spec *corev1.PodSpec) {
	index := getDefaultContainerIndex(spec, TFJobDefaultContainerName)
	if ok := hasDefaultPort(spec, index, TFJobDefaultPortName); !ok {
		setDefaultPort(spec, TFJobDefaultPortName, TFJobDefaultPort, index)
	}
}

// setTensorflowTypeNamesToCamelCase sets the name of all replica types from any case to correct case.
func setTensorflowTypeNamesToCamelCase(tfJob *TFJob) {
	replicaTypes := []ReplicaType{
		TFJobReplicaTypePS,
		TFJobReplicaTypeWorker,
		TFJobReplicaTypeChief,
		TFJobReplicaTypeMaster,
		TFJobReplicaTypeEval,
	}
	for _, replicaType := range replicaTypes {
		setTypeNameToCamelCase(tfJob.Spec.TFReplicaSpecs, replicaType)
	}
}

// SetDefaults_TFJob sets any unspecified values to defaults.
func SetDefaults_TFJob(tfJob *TFJob) {
	// Set default cleanpod policy to None.
	if tfJob.Spec.RunPolicy.CleanPodPolicy == nil {
		tfJob.Spec.RunPolicy.CleanPodPolicy = CleanPodPolicyPointer(CleanPodPolicyNone)
	}
	// Set default success policy to "".
	if tfJob.Spec.SuccessPolicy == nil {
		defaultPolicy := SuccessPolicyDefault
		tfJob.Spec.SuccessPolicy = &defaultPolicy
	}

	// Update the key of TFReplicaSpecs to camel case.
	setTensorflowTypeNamesToCamelCase(tfJob)

	for _, spec := range tfJob.Spec.TFReplicaSpecs {
		// Set default replicas to 1.
		setDefaultReplicas(spec, 1)
		// Set default restartPolicy
		setDefaultRestartPolicy(spec, TFJobDefaultRestartPolicy)
		// Set default port to tensorFlow container.
		setTensorflowDefaultPort(&spec.Template.Spec)
	}
}

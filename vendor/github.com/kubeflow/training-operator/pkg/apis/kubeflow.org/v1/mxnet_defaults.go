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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func addMXNetDefaultingFuncs(scheme *runtime.Scheme) error {
	return RegisterDefaults(scheme)
}

// setMXNetDefaultPort sets the default ports for mxnet container.
func setMXNetDefaultPort(spec *corev1.PodSpec) {
	index := getDefaultContainerIndex(spec, MXJobDefaultContainerName)
	if ok := hasDefaultPort(spec, index, MXJobDefaultPortName); !ok {
		setDefaultPort(spec, MXJobDefaultPortName, MXJobDefaultPort, index)
	}
}

// setMXNetTypeNamesToCamelCase sets the name of all replica types from any case to correct case.
func setMXNetTypeNamesToCamelCase(mxJob *MXJob) {
	replicaTypes := []ReplicaType{
		MXJobReplicaTypeScheduler,
		MXJobReplicaTypeServer,
		MXJobReplicaTypeWorker,
	}
	for _, replicaType := range replicaTypes {
		setTypeNameToCamelCase(mxJob.Spec.MXReplicaSpecs, replicaType)
	}
}

// SetDefaults_MXJob sets any unspecified values to defaults.
func SetDefaults_MXJob(mxjob *MXJob) {
	// Set default cleanpod policy to None.
	if mxjob.Spec.RunPolicy.CleanPodPolicy == nil {
		mxjob.Spec.RunPolicy.CleanPodPolicy = CleanPodPolicyPointer(CleanPodPolicyNone)
	}

	// Update the key of MXReplicaSpecs to camel case.
	setMXNetTypeNamesToCamelCase(mxjob)

	for _, spec := range mxjob.Spec.MXReplicaSpecs {
		// Set default replicas to 1
		setDefaultReplicas(spec, 1)
		// Set default default restartPolicy
		setDefaultRestartPolicy(spec, MXJobDefaultRestartPolicy)
		// Set default port to mxnet container.
		setMXNetDefaultPort(&spec.Template.Spec)
	}
}

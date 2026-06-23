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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func addJAXDefaultingFuncs(scheme *runtime.Scheme) error {
	return RegisterDefaults(scheme)
}

// setJAXDefaultPort sets the default ports for jax container.
func setJAXDefaultPort(spec *corev1.PodSpec) {
	index := getDefaultContainerIndex(spec, JAXJobDefaultContainerName)
	if ok := hasDefaultPort(spec, index, JAXJobDefaultPortName); !ok {
		setDefaultPort(spec, JAXJobDefaultPortName, JAXJobDefaultPort, index)
	}
}

// setJAXTypeNamesToCamelCase sets the name of all replica types from any case to correct case.
func setJAXTypeNamesToCamelCase(jaxJob *JAXJob) {
	replicaTypes := []ReplicaType{
		JAXJobReplicaTypeWorker,
	}
	for _, replicaType := range replicaTypes {
		setTypeNameToCamelCase(jaxJob.Spec.JAXReplicaSpecs, replicaType)
	}
}

// SetDefaults_JAXJob sets any unspecified values to defaults.
func SetDefaults_JAXJob(job *JAXJob) {
	// Set default cleanpod policy to None.
	if job.Spec.RunPolicy.CleanPodPolicy == nil {
		job.Spec.RunPolicy.CleanPodPolicy = CleanPodPolicyPointer(CleanPodPolicyNone)
	}

	// Update the key of JAXReplicaSpecs to camel case.
	setJAXTypeNamesToCamelCase(job)

	for _, spec := range job.Spec.JAXReplicaSpecs {
		// Set default replicas to 1.
		setDefaultReplicas(spec, 1)
		// Set default restartPolicy
		setDefaultRestartPolicy(spec, JAXJobDefaultRestartPolicy)
		// Set default port to jax container.
		setJAXDefaultPort(&spec.Template.Spec)
	}
}

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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func addPaddleDefaultingFuncs(scheme *runtime.Scheme) error {
	return RegisterDefaults(scheme)
}

// setPaddleDefaultPort sets the default ports for paddle container.
func setPaddleDefaultPort(spec *corev1.PodSpec) {
	index := getDefaultContainerIndex(spec, PaddleJobDefaultContainerName)
	if ok := hasDefaultPort(spec, index, PaddleJobDefaultPortName); !ok {
		setDefaultPort(spec, PaddleJobDefaultPortName, PaddleJobDefaultPort, index)
	}
}

func setPaddleElasticPolicy(paddleJob *PaddleJob) {
	if paddleJob.Spec.ElasticPolicy != nil {
		if paddleJob.Spec.ElasticPolicy.MaxReplicas != nil &&
			paddleJob.Spec.ElasticPolicy.MinReplicas != nil {
			return
		} else if paddleJob.Spec.ElasticPolicy.MaxReplicas != nil {
			// Set MinRepliacs to elasticPolicy.MaxReplicas.
			paddleJob.Spec.ElasticPolicy.MinReplicas = paddleJob.Spec.ElasticPolicy.MaxReplicas
		} else if paddleJob.Spec.ElasticPolicy.MinReplicas != nil {
			paddleJob.Spec.ElasticPolicy.MaxReplicas = paddleJob.Spec.ElasticPolicy.MinReplicas
		} else {
			workerReplicas := paddleJob.Spec.PaddleReplicaSpecs[PaddleJobReplicaTypeWorker].Replicas
			// Set Min and Max to worker.spec.Replicas.
			paddleJob.Spec.ElasticPolicy.MaxReplicas = workerReplicas
			paddleJob.Spec.ElasticPolicy.MinReplicas = workerReplicas
		}
	}
}

// setPaddleTypeNamesToCamelCase sets the name of all replica types from any case to correct case.
func setPaddleTypeNamesToCamelCase(paddleJob *PaddleJob) {
	replicaTypes := []ReplicaType{
		PaddleJobReplicaTypeMaster,
		PaddleJobReplicaTypeWorker,
	}
	for _, replicaType := range replicaTypes {
		setTypeNameToCamelCase(paddleJob.Spec.PaddleReplicaSpecs, replicaType)
	}
}

// SetDefaults_PaddleJob sets any unspecified values to defaults.
func SetDefaults_PaddleJob(job *PaddleJob) {
	// Set default cleanpod policy to None.
	if job.Spec.RunPolicy.CleanPodPolicy == nil {
		job.Spec.RunPolicy.CleanPodPolicy = CleanPodPolicyPointer(CleanPodPolicyNone)
	}

	// Update the key of PaddleReplicaSpecs to camel case.
	setPaddleTypeNamesToCamelCase(job)

	for _, spec := range job.Spec.PaddleReplicaSpecs {
		setDefaultReplicas(spec, 1)
		setDefaultRestartPolicy(spec, PaddleJobDefaultRestartPolicy)
		setPaddleDefaultPort(&spec.Template.Spec)
	}
	// Set default elastic policy.
	setPaddleElasticPolicy(job)
}

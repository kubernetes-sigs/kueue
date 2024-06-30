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

func addXGBoostJobDefaultingFuncs(scheme *runtime.Scheme) error {
	return RegisterDefaults(scheme)
}

// setXGBoostJobDefaultPort sets the default ports for mxnet container.
func setXGBoostJobDefaultPort(spec *corev1.PodSpec) {
	index := getDefaultContainerIndex(spec, XGBoostJobDefaultContainerName)
	if ok := hasDefaultPort(spec, index, XGBoostJobDefaultPortName); !ok {
		setDefaultPort(spec, XGBoostJobDefaultPortName, XGBoostJobDefaultPort, index)
	}
}

// setXGBoostJobTypeNamesToCamelCase sets the name of all replica types from any case to correct case.
func setXGBoostJobTypeNamesToCamelCase(xgboostJob *XGBoostJob) {
	replicaTypes := []ReplicaType{
		XGBoostJobReplicaTypeMaster,
		XGBoostJobReplicaTypeWorker,
	}
	for _, replicaType := range replicaTypes {
		setTypeNameToCamelCase(xgboostJob.Spec.XGBReplicaSpecs, replicaType)
	}
}

// SetDefaults_XGBoostJob sets any unspecified values to defaults.
func SetDefaults_XGBoostJob(xgboostJob *XGBoostJob) {
	// Set default cleanpod policy to None.
	if xgboostJob.Spec.RunPolicy.CleanPodPolicy == nil {
		xgboostJob.Spec.RunPolicy.CleanPodPolicy = CleanPodPolicyPointer(CleanPodPolicyNone)
	}

	// Update the key of MXReplicaSpecs to camel case.
	setXGBoostJobTypeNamesToCamelCase(xgboostJob)

	for _, spec := range xgboostJob.Spec.XGBReplicaSpecs {
		// Set default replicas to 1.
		setDefaultReplicas(spec, 1)
		// Set default restartPolicy
		setDefaultRestartPolicy(spec, XGBoostJobDefaultRestartPolicy)
		// Set default port to mxnet container.
		setXGBoostJobDefaultPort(&spec.Template.Spec)
	}
}

/*
Copyright 2024 The Kubeflow Authors.

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

package coscheduling

import (
	"sigs.k8s.io/controller-runtime/pkg/client"

	trainer "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
)

var (
	TrainingRuntimeContainerRuntimeClassKey        = ".trainingRuntimeSpec.jobSetTemplateSpec.replicatedJobs.podTemplateSpec.runtimeClassName"
	ClusterTrainingRuntimeContainerRuntimeClassKey = ".clusterTrainingRuntimeSpec.jobSetTemplateSpec.replicatedJobs.podTemplateSpec.runtimeClassName"
)

func IndexTrainingRuntimeContainerRuntimeClass(obj client.Object) []string {
	runtime, ok := obj.(*trainer.TrainingRuntime)
	if !ok {
		return nil
	}
	var runtimeClasses []string
	for _, rJob := range runtime.Spec.Template.Spec.ReplicatedJobs {
		if rJob.Template.Spec.Template.Spec.RuntimeClassName != nil {
			runtimeClasses = append(runtimeClasses, *rJob.Template.Spec.Template.Spec.RuntimeClassName)
		}
	}
	return runtimeClasses
}

func IndexClusterTrainingRuntimeContainerRuntimeClass(obj client.Object) []string {
	clRuntime, ok := obj.(*trainer.ClusterTrainingRuntime)
	if !ok {
		return nil
	}
	var runtimeClasses []string
	for _, rJob := range clRuntime.Spec.Template.Spec.ReplicatedJobs {
		if rJob.Template.Spec.Template.Spec.RuntimeClassName != nil {
			runtimeClasses = append(runtimeClasses, *rJob.Template.Spec.Template.Spec.RuntimeClassName)
		}
	}
	return runtimeClasses
}

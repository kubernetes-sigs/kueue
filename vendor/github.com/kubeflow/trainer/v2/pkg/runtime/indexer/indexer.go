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

package indexer

import (
	"errors"

	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	trainer "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
	"github.com/kubeflow/trainer/v2/pkg/util/trainjob"
)

const (
	TrainJobRuntimeRefKey        = ".spec.runtimeRef.kind=trainingRuntime"
	TrainJobClusterRuntimeRefKey = ".spec.runtimeRef.kind=clusterTrainingRuntime"
)

var (
	TrainingRuntimeContainerRuntimeClassKey                   = ".trainingRuntimeSpec.jobSetTemplateSpec.replicatedJobs.podTemplateSpec.runtimeClassName"
	ClusterTrainingRuntimeContainerRuntimeClassKey            = ".clusterTrainingRuntimeSpec.jobSetTemplateSpec.replicatedJobs.podTemplateSpec.runtimeClassName"
	ErrorCanNotSetupTrainingRuntimeRuntimeClassIndexer        = errors.New("setting index on runtimeClass for TrainingRuntime")
	ErrorCanNotSetupClusterTrainingRuntimeRuntimeClassIndexer = errors.New("setting index on runtimeClass for ClusterTrainingRuntime")
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

func IndexTrainJobTrainingRuntime(obj client.Object) []string {
	trainJob, ok := obj.(*trainer.TrainJob)
	if !ok {
		return nil
	}
	if trainjob.RuntimeRefIsTrainingRuntime(trainJob.Spec.RuntimeRef) {
		return []string{trainJob.Spec.RuntimeRef.Name}
	}
	return nil
}

func IndexTrainJobClusterTrainingRuntime(obj client.Object) []string {
	trainJob, ok := obj.(*trainer.TrainJob)
	if !ok {
		return nil
	}
	if ptr.Deref(trainJob.Spec.RuntimeRef.APIGroup, "") == trainer.GroupVersion.Group &&
		ptr.Deref(trainJob.Spec.RuntimeRef.Kind, "") == trainer.ClusterTrainingRuntimeKind {
		return []string{trainJob.Spec.RuntimeRef.Name}
	}
	return nil
}

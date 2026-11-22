/*
Copyright 2025 The Kubeflow Authors.

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

package trainjob

import (
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"

	trainer "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
)

const (
	// TrainJobControllerName is the reserved value for the TrainJob managedBy field,
	// indicating that the TrainJob is reconciled by the built-in TrainJob controller.
	TrainJobControllerName string = "trainer.kubeflow.org/trainjob-controller"
)

func IsTrainJobFinished(trainJob *trainer.TrainJob) bool {
	return meta.IsStatusConditionTrue(trainJob.Status.Conditions, trainer.TrainJobComplete) ||
		meta.IsStatusConditionTrue(trainJob.Status.Conditions, trainer.TrainJobFailed)
}

// IsManagedByExternalController returns true when the TrainJob is managed by an external
// controller, i.e. it is not reconciled by the built-in TrainJob controller.
func IsManagedByExternalController(trainJob *trainer.TrainJob) bool {
	return ptr.Deref(trainJob.Spec.ManagedBy, TrainJobControllerName) != TrainJobControllerName
}

func RuntimeRefIsTrainingRuntime(ref trainer.RuntimeRef) bool {
	return ptr.Equal(ref.APIGroup, &trainer.GroupVersion.Group) &&
		ptr.Equal(ref.Kind, ptr.To(trainer.TrainingRuntimeKind))
}

func RuntimeRefIsClusterTrainingRuntime(ref trainer.RuntimeRef) bool {
	return ptr.Equal(ref.APIGroup, &trainer.GroupVersion.Group) &&
		ptr.Equal(ref.Kind, ptr.To(trainer.ClusterTrainingRuntimeKind))
}

// ResourceClaimsPaths returns the paths of the DRA resource claims which the TrainJob declares:
// spec.trainer.resourceClaimsPerNode and the Pod resourceClaims of spec.runtimePatches.
func ResourceClaimsPaths(trainJob *trainer.TrainJob) []*field.Path {
	var paths []*field.Path
	if trainJob.Spec.Trainer != nil && len(trainJob.Spec.Trainer.ResourceClaimsPerNode) > 0 {
		paths = append(paths, field.NewPath("spec", "trainer", "resourceClaimsPerNode"))
	}
	ForEachRuntimePatchPodSpec(trainJob, func(podSpecPath *field.Path, _, _ string, podSpec *trainer.PodSpecPatch) {
		if len(podSpec.ResourceClaims) > 0 {
			paths = append(paths, podSpecPath.Child("resourceClaims"))
		}
	})
	return paths
}

// ContainerResourceClaimsPaths returns the paths of the container and initContainer
// resources.claims which the TrainJob sets through spec.runtimePatches.
func ContainerResourceClaimsPaths(trainJob *trainer.TrainJob) []*field.Path {
	var paths []*field.Path
	ForEachRuntimePatchPodSpec(trainJob, func(podSpecPath *field.Path, _, _ string, podSpec *trainer.PodSpecPatch) {
		for k, c := range podSpec.Containers {
			if c.Resources != nil && len(c.Resources.Claims) > 0 {
				paths = append(paths, podSpecPath.Child("containers").Index(k).Child("resources", "claims"))
			}
		}
		for k, c := range podSpec.InitContainers {
			if c.Resources != nil && len(c.Resources.Claims) > 0 {
				paths = append(paths, podSpecPath.Child("initContainers").Index(k).Child("resources", "claims"))
			}
		}
	})
	return paths
}

// ForEachRuntimePatchPodSpec calls fn for every ReplicatedJob Pod spec patched by spec.runtimePatches,
// along with the manager of the patch and the name of the patched ReplicatedJob.
func ForEachRuntimePatchPodSpec(trainJob *trainer.TrainJob, fn func(podSpecPath *field.Path, manager, rJobName string, podSpec *trainer.PodSpecPatch)) {
	for i, patch := range trainJob.Spec.RuntimePatches {
		if patch.TrainingRuntimeSpec == nil || patch.TrainingRuntimeSpec.Template == nil ||
			patch.TrainingRuntimeSpec.Template.Spec == nil {
			continue
		}
		for j, rJob := range patch.TrainingRuntimeSpec.Template.Spec.ReplicatedJobs {
			if rJob.Template == nil || rJob.Template.Spec == nil ||
				rJob.Template.Spec.Template == nil || rJob.Template.Spec.Template.Spec == nil {
				continue
			}
			podSpecPath := field.NewPath("spec", "runtimePatches").Index(i).
				Child("trainingRuntimeSpec", "template", "spec", "replicatedJobs").Index(j).
				Child("template", "spec", "template", "spec")
			fn(podSpecPath, patch.Manager, rJob.Name, rJob.Template.Spec.Template.Spec)
		}
	}
}

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

package torch

import (
	"context"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation/field"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	trainer "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
	"github.com/kubeflow/trainer/v2/pkg/apply"
	"github.com/kubeflow/trainer/v2/pkg/constants"
	"github.com/kubeflow/trainer/v2/pkg/runtime"
	"github.com/kubeflow/trainer/v2/pkg/runtime/framework"
)

type Torch struct{}

var _ framework.EnforceMLPolicyPlugin = (*Torch)(nil)
var _ framework.CustomValidationPlugin = (*Torch)(nil)

const Name = "Torch"

func New(context.Context, client.Client, client.FieldIndexer) (framework.Plugin, error) {
	return &Torch{}, nil
}

func (t *Torch) Name() string {
	return Name
}

func (t *Torch) Validate(_ context.Context, runtimeInfo *runtime.Info, _, newObj *trainer.TrainJob) (admission.Warnings, field.ErrorList) {
	var allErrs field.ErrorList
	if runtimeInfo == nil || runtimeInfo.RuntimePolicy.MLPolicySource == nil || runtimeInfo.RuntimePolicy.MLPolicySource.Torch == nil || newObj.Spec.Trainer == nil || newObj.Spec.Trainer.NumProcPerNode == nil {
		return nil, allErrs
	}

	specPath := field.NewPath("spec")

	if newObj.Spec.Trainer != nil {
		numProcPerNodePath := specPath.Child("trainer").Child("numProcPerNode")
		numProcPerNode := *newObj.Spec.Trainer.NumProcPerNode
		if numProcPerNode.Type == intstr.String {
			allowed := sets.New("auto", "cpu", "gpu")
			if !allowed.Has(numProcPerNode.StrVal) {
				allErrs = append(allErrs, field.Invalid(numProcPerNodePath, numProcPerNode, fmt.Sprintf("must have an int value or %v", sets.List(allowed))))
			}
		}

		// Check reserved envs.
		torchEnvs := sets.New[string]()
		for _, env := range newObj.Spec.Trainer.Env {
			if constants.TorchRunReservedEnvNames.Has(env.Name) {
				torchEnvs.Insert(env.Name)
			}
		}

		if torchEnvs.Len() > 0 {
			trainerEnvsPath := specPath.Child("trainer").Child("env")
			allErrs = append(allErrs, field.Invalid(trainerEnvsPath, newObj.Spec.Trainer.Env, fmt.Sprintf("must not have reserved envs, invalid envs configured: %v", sets.List(torchEnvs))))
		}

		// Check supported pretrained models for torchtune.
		// TODO(Electronic-Waste): Add more validation for torchtune when we support more arguments.
		if slices.Equal(newObj.Spec.Trainer.Command, constants.TorchTuneEntrypoint) {
			_, torchTuneErrs := validateTorchTune(runtimeInfo, newObj)
			allErrs = append(allErrs, torchTuneErrs...)
		}
	}
	return nil, allErrs
}

// TODO (andreyvelich): Add support for PyTorch elastic when JobSet supports Elastic Jobs.
func (t *Torch) EnforceMLPolicy(info *runtime.Info, trainJob *trainer.TrainJob) error {
	if info == nil || info.RuntimePolicy.MLPolicySource == nil || info.RuntimePolicy.MLPolicySource.Torch == nil {
		return nil
	}

	// TrainJob contains the actual information for the Trainer.
	trainerPS := info.FindPodSetByAncestor(constants.AncestorTrainer)
	if trainerPS != nil && trainerPS.Count != nil && trainJob.Spec.Trainer != nil && trainJob.Spec.Trainer.NumNodes != nil {
		*trainerPS.Count = *trainJob.Spec.Trainer.NumNodes
	}

	numProcPerNode := ptr.Deref(info.RuntimePolicy.MLPolicySource.Torch.NumProcPerNode, intstr.FromString("auto"))
	if trainJob.Spec.Trainer != nil && trainJob.Spec.Trainer.NumProcPerNode != nil {
		numProcPerNode = ptr.Deref(trainJob.Spec.Trainer.NumProcPerNode, intstr.FromString("auto"))
	}

	// Determine numProcPerNode based on the resourcesPerNode.
	resourcesPerNode := ptr.Deref(runtime.ExtractResourcePerNodeFromRuntime(info), corev1.ResourceRequirements{})
	if jobTrainer := trainJob.Spec.Trainer; jobTrainer != nil && jobTrainer.ResourcesPerNode != nil {
		resourcesPerNode = ptr.Deref(jobTrainer.ResourcesPerNode, corev1.ResourceRequirements{})
	}
	gpuQ := runtime.GetNumGPUPerNode(&resourcesPerNode)
	// If numProcPerNode is "cpu" or no GPU is set in resource, we calculate numProcPerNode based on CPU.
	if numProcPerNode.String() == "cpu" || numProcPerNode.String() == "auto" && gpuQ == 0 {
		numProcPerNode = intstr.FromInt(max(1, getNumCPUPerNode(&resourcesPerNode)))
	}

	// Update envs for Info object.
	var trainerContainer *runtime.Container
	if trainJob.Spec.Trainer != nil {
		if trainerContainer = info.FindContainerByPodSetAncestorContainerName(constants.AncestorTrainer, constants.Node); trainerContainer != nil {
			apply.UpsertEnvVars(&trainerContainer.Env, apply.EnvVars(trainJob.Spec.Trainer.Env...)...)
		}
	}
	if trainerContainer != nil {
		// Add PyTorch distributed "PET_" values for torchrun and torchtune.
		// TODO (andreyvelich): We should validate that envs from different plugins don't conflict with each other.
		// Ref: https://github.com/kubeflow/trainer/pull/2308#discussion_r1823229940
		apply.UpsertEnvVars(&trainerContainer.Env,
			*corev1ac.EnvVar().
				WithName(constants.TorchEnvNumNodes).
				WithValue(fmt.Sprintf("%d", ptr.Deref(ptr.Deref(trainerPS, runtime.PodSet{}).Count, 1))),
			*corev1ac.EnvVar().
				WithName(constants.TorchEnvNumProcPerNode).
				WithValue(numProcPerNode.String()),
			*corev1ac.EnvVar().
				WithName(constants.TorchEnvNodeRank).
				WithValueFrom(corev1ac.EnvVarSource().
					WithFieldRef(corev1ac.ObjectFieldSelector().
						WithFieldPath(constants.JobCompletionIndexFieldPath))),
		)

		if !slices.Equal(trainJob.Spec.Trainer.Command, constants.TorchTuneEntrypoint) {
			// Add PET_MASTER_ADDR and PET_MASTER_PORT envs for torchrun.
			apply.UpsertEnvVars(&trainerContainer.Env,
				*corev1ac.EnvVar().
					WithName(constants.TorchEnvMasterAddr).
					WithValue(fmt.Sprintf("%s-%s-0-0.%s", trainJob.Name, constants.Node, trainJob.Name)),
				*corev1ac.EnvVar().
					WithName(constants.TorchEnvMasterPort).
					WithValue(fmt.Sprintf("%d", constants.ContainerTrainerPort)),
			)
		} else {
			// Mutate trainer command for torchtune.
			// Ref: https://github.com/kubeflow/trainer/tree/master/docs/proposals/2401-llm-trainer-v2#complement-torch-plugin
			// 1. Add rendezvous backend arg for torchtune.
			// Rendezvous backend is only enabled for multi-nodes or multi-devices training.
			var newCommand []string
			numNodes := ptr.Deref(ptr.Deref(trainerPS, runtime.PodSet{}).Count, 1)
			if numNodes > 1 || numProcPerNode.Type == intstr.Int && numProcPerNode.IntVal > 1 || numProcPerNode.Type == intstr.String && gpuQ > 1 {
				newCommand = append(newCommand,
					fmt.Sprintf("%s=%s-%s-0-0.%s:%d",
						constants.TorchTuneArgRdzvEndpoint,
						trainJob.Name, constants.Node, trainJob.Name, constants.ContainerTrainerPort,
					),
				)
			}

			// 2. Get the recipe and config from old args and append them to newCommand.
			recipe, config := getRecipeAndConfig(numNodes, numProcPerNode, gpuQ, trainJob)
			newCommand = append(newCommand, recipe, constants.TorchTuneArgConfig, config)

			// 3. Extract output directory, tokenizer path and model mount path from (Cluster)TrainingRuntime.
			newCommand = append(newCommand, extractOverridesFromRuntime(info)...)

			trainJob.Spec.Trainer.Command = append(trainJob.Spec.Trainer.Command, newCommand...)
		}
		// Add container port for the headless service.
		apply.UpsertPort(&trainerContainer.Ports, *corev1ac.ContainerPort().WithContainerPort(constants.ContainerTrainerPort))
	}

	return nil
}

// getNumCPUPerNode calculates the number of CPU processes per node based on the provided resources.
func getNumCPUPerNode(res *corev1.ResourceRequirements) int {
	if res == nil {
		return 0
	}
	limitCpuQ, requestCpuQ := res.Limits.Cpu(), res.Requests.Cpu()
	if requestCpuQ == nil || requestCpuQ.IsZero() {
		if limitCpuQ != nil {
			return int(limitCpuQ.Value())
		}
		return 0
	}
	return int(requestCpuQ.Value())
}

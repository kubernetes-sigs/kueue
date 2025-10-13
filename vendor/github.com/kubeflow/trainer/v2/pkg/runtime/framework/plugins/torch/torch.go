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
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation/field"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	jobsetv1alpha2ac "sigs.k8s.io/jobset/client-go/applyconfiguration/jobset/v1alpha2"

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
			runtimeRefNamePath := specPath.Child("runtimeRef").Child("name")
			model := getModelFromRuntimeRef(newObj.Spec.RuntimeRef.Name)

			if !constants.TorchTuneSupportedPretrainedModels.Has(model) {
				allErrs = append(allErrs, field.Invalid(runtimeRefNamePath, newObj.Spec.RuntimeRef.Name, fmt.Sprintf("must have a supported pretrained model, invalid model configured: %v", model)))
			}

			numNodesRefPath := specPath.Child("trainer").Child("numNodes")
			numNodes := *newObj.Spec.Trainer.NumNodes
			if numNodes > 1 && model != constants.TORCHTUNE_MODEL_LLAMA3_3_70B {
				allErrs = append(allErrs, field.Invalid(numNodesRefPath, numNodes, fmt.Sprintf("must be 1 for %v model", model)))
			}
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

	if jobTrainer := trainJob.Spec.Trainer; jobTrainer != nil && jobTrainer.ResourcesPerNode != nil {
		var (
			shouldUseCPU           func(resources corev1.ResourceList) bool
			fallbackNumProcPerNode intstr.IntOrString
		)
		switch numProcPerNode.String() {
		case "auto":
			shouldUseCPU = func(resources corev1.ResourceList) bool {
				for resName := range resources {
					if strings.Contains(strings.ToLower(resName.String()), "gpu") {
						return false
					}
				}
				return true
			}
			fallbackNumProcPerNode = intstr.FromString("auto")
		case "cpu":
			shouldUseCPU = func(resources corev1.ResourceList) bool {
				_, ok := resources[corev1.ResourceCPU]
				return ok
			}
			fallbackNumProcPerNode = intstr.FromInt32(1)
		default:
			shouldUseCPU = func(corev1.ResourceList) bool { return false }
			fallbackNumProcPerNode = numProcPerNode
		}
		nppNode, usedCPU := calculateNumProcPerNode(fallbackNumProcPerNode, jobTrainer.ResourcesPerNode.Limits, shouldUseCPU)
		if !usedCPU {
			nppNode, _ = calculateNumProcPerNode(fallbackNumProcPerNode, jobTrainer.ResourcesPerNode.Requests, shouldUseCPU)
		}
		numProcPerNode = nppNode
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
			if numNodes > 1 || !(numProcPerNode.Type == intstr.Int && numProcPerNode.IntVal == 1) {
				newCommand = append(newCommand,
					fmt.Sprintf("%s=%s-%s-0-0.%s:%d",
						constants.TorchTuneArgRdzvEndpoint,
						trainJob.Name, constants.Node, trainJob.Name, constants.ContainerTrainerPort,
					),
				)
			}

			// 2. Get the recipe and config from old args and append them to newCommand.
			recipe, config := getRecipeAndConfig(
				numNodes,
				numProcPerNode,
				getModelFromRuntimeRef(trainJob.Spec.RuntimeRef.Name),
				trainJob.Spec.Trainer.Args,
			)
			newCommand = append(newCommand, recipe, constants.TorchTuneArgConfig, config)

			// 3. Extract output directory, tokenizer path and model mount path from (Cluster)TrainingRuntime.
			newCommand = append(newCommand, extractOverridesFromRuntime(info)...)

			trainJob.Spec.Trainer.Command = append(trainJob.Spec.Trainer.Command, newCommand...)
		}
		// Add container port for the headless service.
		apply.UpsertPort(&trainerContainer.Ports, *corev1ac.ContainerPort().WithContainerPort(constants.ContainerTrainerPort))
	}
	info.SyncPodSetsToTemplateSpec()
	return nil
}

// calculateNumProcPerNode calculates the number of processes per node based on the provided resources.
// It returns the calculated number of processes per node and a boolean indicating whether CPU resources were used.
func calculateNumProcPerNode(
	fallbackNumProcPerNode intstr.IntOrString, resources corev1.ResourceList, shouldUseCPU func(resources corev1.ResourceList) bool,
) (intstr.IntOrString, bool) {
	var defaultCPU int32 = 1
	if resources != nil {
		if shouldUseCPU(resources) {
			cpuQ := resources[corev1.ResourceCPU]
			return intstr.FromInt32(max(defaultCPU, int32(cpuQ.Value()))), true
		}
		return fallbackNumProcPerNode, false
	}
	return intstr.FromInt32(defaultCPU), false
}

// getRecipeAndConfig returns the recipe and config file name based on the number of nodes,
// number of processes per node, model name, and command line arguments.
func getRecipeAndConfig(numNodes int32, numProcPerNode intstr.IntOrString, model string, _ []string) (string, string) {
	recipe := constants.TorchTuneFullFinetuneDistributed
	suffix := constants.TorchTuneFullFinetuneMultiDevicesConfigSuffix
	if numNodes == 1 && numProcPerNode.Type == intstr.Int && numProcPerNode.IntVal == 1 {
		recipe = constants.TorchTuneFullFinetuneSingleDevice
		suffix = constants.TorchTuneFullFinetuneSingleDeviceConfigSuffix
	} else if numNodes > 1 {
		suffix = constants.TorchTuneFullFinetuneMultiNodesConfigSuffix
	}

	return recipe, fmt.Sprintf("%s%s", model, suffix)
}

// extractOverridesFromRuntime extracts overrides from the TorchTune Trainer Node.
func extractOverridesFromRuntime(info *runtime.Info) []string {
	overrides := []string{}
	jobSetSpec, ok := runtime.TemplateSpecApply[jobsetv1alpha2ac.JobSetSpecApplyConfiguration](info)
	if !ok {
		return overrides
	}

	for _, rJob := range jobSetSpec.ReplicatedJobs {
		jobMetadata := rJob.Template.ObjectMetaApplyConfiguration
		if jobMetadata == nil || jobMetadata.Labels == nil {
			continue
		}
		if ancestor, ok := jobMetadata.Labels[constants.LabelTrainJobAncestor]; ok && ancestor == constants.AncestorTrainer {
			for _, container := range rJob.Template.Spec.Template.Spec.Containers {
				if container.Name != nil && *container.Name == constants.Node {
					for _, command := range container.Command {
						if constants.TorchTuneImmutableConfigs.Has(strings.Split(command, "=")[0]) {
							overrides = append(overrides, command)
						}
					}
				}
			}
		}
	}

	return overrides
}

func getModelFromRuntimeRef(runtimeRefName string) string {
	fields := strings.Split(runtimeRefName, "-")
	if len(fields) != 3 {
		return ""
	}

	return fmt.Sprintf("%s/%s", strings.ReplaceAll(fields[1], ".", "_"), strings.ToUpper(fields[2]))
}

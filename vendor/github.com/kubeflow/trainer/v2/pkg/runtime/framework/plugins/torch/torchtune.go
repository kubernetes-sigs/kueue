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

package torch

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	jobsetv1alpha2ac "sigs.k8s.io/jobset/client-go/applyconfiguration/jobset/v1alpha2"

	trainer "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
	"github.com/kubeflow/trainer/v2/pkg/constants"
	"github.com/kubeflow/trainer/v2/pkg/runtime"
)

func validateTorchTune(runtimeInfo *runtime.Info, newObj *trainer.TrainJob) (admission.Warnings, field.ErrorList) {
	var allErrs field.ErrorList
	specPath := field.NewPath("spec")

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

	numProcPerNodeRefPath := specPath.Child("trainer").Child("numProcPerNode")
	numProcPerNode := *newObj.Spec.Trainer.NumProcPerNode
	resourcesPerNode := ptr.Deref(runtime.ExtractResourcePerNodeFromRuntime(runtimeInfo), corev1.ResourceRequirements{})
	if jobTrainer := newObj.Spec.Trainer; jobTrainer != nil && jobTrainer.ResourcesPerNode != nil {
		resourcesPerNode = ptr.Deref(jobTrainer.ResourcesPerNode, corev1.ResourceRequirements{})
	}
	_, config := getRecipeAndConfig(numNodes, numProcPerNode, runtime.GetNumGPUPerNode(&resourcesPerNode), newObj)
	if strings.Contains(config, constants.TorchTuneQLoRAFinetuneDistributedConfigSuffix) {
		if model == constants.TORCHTUNE_MODEL_QWEN2_5_1_5B {
			allErrs = append(allErrs, field.Invalid(runtimeRefNamePath, newObj.Spec.RuntimeRef.Name, fmt.Sprintf("QLoRA is not supported for %v model", model)))
		}
		resourcePerNodeRefPath := specPath.Child("trainer").Child("resourcesPerNode")
		if !strings.Contains(config, constants.TorchTuneQLoRAFinetuneSingleDeviceConfigSuffix) &&
			(model == constants.TORCHTUNE_MODEL_LLAMA3_2_1B || model == constants.TORCHTUNE_MODEL_LLAMA3_2_3B) {
			allErrs = append(
				allErrs,
				field.Invalid(numProcPerNodeRefPath, numProcPerNode, fmt.Sprintf("must be auto or 1 for %v model when using QLoRA", model)),
				field.Invalid(resourcePerNodeRefPath, newObj.Spec.Trainer.ResourcesPerNode, fmt.Sprintf("must have gpu resource with value 1 for %v model when using QLoRA", model)),
			)
		}
	}
	return nil, allErrs
}

// getRecipeAndConfig returns the recipe and config file name based on the number of nodes,
// number of processes per node, gpu count, resource per node, model name, and command line arguments.
func getRecipeAndConfig(numNodes int32, numProcPerNode intstr.IntOrString, gpuQ int, trainJob *trainer.TrainJob) (string, string) {
	recipe := constants.TorchTuneFullFinetuneDistributed
	suffix := constants.TorchTuneFullFinetuneMultiDevicesConfigSuffix
	if numNodes == 1 && (numProcPerNode.Type == intstr.Int && numProcPerNode.IntVal == 1 || gpuQ == 1) {
		if isUseQLoraFinetune(trainJob.Spec.Trainer.Args) {
			recipe = constants.TorchTuneLoRAFinetuneSingleDevice
			suffix = constants.TorchTuneQLoRAFinetuneSingleDeviceConfigSuffix
		} else if isLoraConfigEnabled(trainJob.Spec.Trainer.Args) {
			recipe = constants.TorchTuneLoRAFinetuneSingleDevice
			suffix = constants.TorchTuneLoRAFinetuneSingleDeviceConfigSuffix
		} else {
			recipe = constants.TorchTuneFullFinetuneSingleDevice
			suffix = constants.TorchTuneFullFinetuneSingleDeviceConfigSuffix
		}
	} else if numNodes == 1 && isUseQLoraFinetune(trainJob.Spec.Trainer.Args) {
		recipe = constants.TorchTuneLoRAFinetuneDistributed
		suffix = constants.TorchTuneQLoRAFinetuneDistributedConfigSuffix
	} else if numNodes == 1 && isLoraConfigEnabled(trainJob.Spec.Trainer.Args) {
		recipe = constants.TorchTuneLoRAFinetuneDistributed
		suffix = constants.TorchTuneLoRAFinetuneDistributedConfigSuffix
	} else if numNodes > 1 {
		suffix = constants.TorchTuneFullFinetuneMultiNodesConfigSuffix
	}

	return recipe, fmt.Sprintf("%s%s", getModelFromRuntimeRef(trainJob.Spec.RuntimeRef.Name), suffix)
}

// isLoraConfigEnabled checks if we enables LoraConfig.
func isLoraConfigEnabled(args []string) bool {
	for _, arg := range args {
		if strings.Contains(arg, constants.TorchTuneLoraAttnModules) {
			return true
		}
	}
	return false
}

// isUseQLoraFinetune checks if QLoRA fine-tuning should be used.
func isUseQLoraFinetune(args []string) bool {
	hasQuantizeBase := false
	for _, arg := range args {
		switch {
		case strings.Contains(arg, constants.TorchTuneUseDora):
			// If Dora is enabled, no need to continue
			return false
		case strings.Contains(arg, constants.TorchTuneQuantizeBase):
			hasQuantizeBase = true
		}
	}
	return hasQuantizeBase
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

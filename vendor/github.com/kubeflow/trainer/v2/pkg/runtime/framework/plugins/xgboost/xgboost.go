/*
Copyright 2026 The Kubeflow Authors.

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

package xgboost

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	configapi "github.com/kubeflow/trainer/v2/pkg/apis/config/v1alpha1"
	trainer "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
	"github.com/kubeflow/trainer/v2/pkg/apply"
	"github.com/kubeflow/trainer/v2/pkg/constants"
	"github.com/kubeflow/trainer/v2/pkg/runtime"
	"github.com/kubeflow/trainer/v2/pkg/runtime/framework"
)

type XGBoost struct{}

var _ framework.EnforceMLPolicyPlugin = (*XGBoost)(nil)
var _ framework.CustomValidationPlugin = (*XGBoost)(nil)

const Name = "XGBoost"

func New(context.Context, client.Client, client.FieldIndexer, *configapi.Configuration) (framework.Plugin, error) {
	return &XGBoost{}, nil
}

func (x *XGBoost) Name() string {
	return Name
}

func (x *XGBoost) Validate(_ context.Context, runtimeInfo *runtime.Info, _, newObj *trainer.TrainJob) (admission.Warnings, field.ErrorList) {
	var allErrs field.ErrorList
	if runtimeInfo == nil || runtimeInfo.RuntimePolicy.MLPolicySource == nil ||
		runtimeInfo.RuntimePolicy.MLPolicySource.XGBoost == nil {
		return nil, allErrs
	}
	if newObj.Spec.Trainer != nil {
		specPath := field.NewPath("spec", "trainer", "env")
		for i, env := range newObj.Spec.Trainer.Env {
			if constants.XGBoostReservedEnvNames.Has(env.Name) {
				allErrs = append(allErrs, field.Forbidden(
					specPath.Index(i),
					fmt.Sprintf("%s is reserved for the XGBoost runtime", env.Name),
				))
			}
		}
	}
	return nil, allErrs
}

func (x *XGBoost) EnforceMLPolicy(info *runtime.Info, trainJob *trainer.TrainJob) error {
	// Guard: Return early if XGBoost policy not configured.
	if info == nil || info.RuntimePolicy.MLPolicySource == nil ||
		info.RuntimePolicy.MLPolicySource.XGBoost == nil {
		return nil
	}

	// Find the trainer PodSet.
	trainerPS := info.FindPodSetByAncestor(constants.AncestorTrainer)
	if trainerPS == nil {
		return nil
	}

	// Set the number of nodes from TrainJob if specified.
	if trainerPS.Count != nil &&
		trainJob.Spec.Trainer != nil && trainJob.Spec.Trainer.NumNodes != nil {
		*trainerPS.Count = *trainJob.Spec.Trainer.NumNodes
	}

	// Find the trainer container and inject environment variables.
	var trainerContainer *runtime.Container
	if trainJob.Spec.Trainer != nil {
		if trainerContainer = info.FindContainerByPodSetAncestorContainerName(
			constants.AncestorTrainer, constants.Node,
		); trainerContainer != nil {
			numNodes := ptr.Deref(ptr.Deref(trainerPS, runtime.PodSet{}).Count, 1)

			// Auto-derive numWorkersPerNode from GPU resources.
			// GPU training: 1 worker per GPU | CPU training: 1 worker per node.
			numWorkersPerNode := int32(1)
			// Step 1: Get resources from Runtime (ClusterTrainingRuntime template).
			resourcesPerNode := ptr.Deref(runtime.ExtractResourcePerNodeFromRuntime(info), corev1.ResourceRequirements{})
			// Step 2: Override with TrainJob resources if specified.
			if jobTrainer := trainJob.Spec.Trainer; jobTrainer != nil && jobTrainer.ResourcesPerNode != nil {
				resourcesPerNode = ptr.Deref(jobTrainer.ResourcesPerNode, corev1.ResourceRequirements{})
			}
			// Step 3: Derive GPU count from the final resolved resources.
			if gpuCount := runtime.GetNumGPUPerNode(&resourcesPerNode); gpuCount > 0 {
				numWorkersPerNode = int32(gpuCount)
			}
			totalWorkers := numNodes * numWorkersPerNode

			// Build tracker URI: <trainjob-name>-node-0-0.<trainjob-name>
			trackerURI := fmt.Sprintf("%s-%s-0-0.%s",
				trainJob.Name, constants.Node, trainJob.Name)

			// Inject DMLC_* environment variables.
			apply.UpsertEnvVars(&trainerContainer.Env,
				// DMLC_TRACKER_URI - DNS name for rank-0 worker running tracker.
				*corev1ac.EnvVar().
					WithName(constants.XGBoostEnvTrackerURI).
					WithValue(trackerURI),
				// DMLC_TRACKER_PORT - Default tracker port.
				*corev1ac.EnvVar().
					WithName(constants.XGBoostEnvTrackerPort).
					WithValue(fmt.Sprintf("%d", constants.ContainerTrainerPort)),
				// DMLC_TASK_ID - Worker rank from Job completion index.
				*corev1ac.EnvVar().
					WithName(constants.XGBoostEnvTaskID).
					WithValueFrom(corev1ac.EnvVarSource().
						WithFieldRef(corev1ac.ObjectFieldSelector().
							WithFieldPath(constants.JobCompletionIndexFieldPath))),
				// DMLC_NUM_WORKER - Total number of workers.
				*corev1ac.EnvVar().
					WithName(constants.XGBoostEnvNumWorker).
					WithValue(fmt.Sprintf("%d", totalWorkers)),
			)

			// Add container port for tracker communication.
			apply.UpsertPort(&trainerContainer.Ports,
				*corev1ac.ContainerPort().
					WithContainerPort(constants.ContainerTrainerPort))
		}
	}

	return nil
}

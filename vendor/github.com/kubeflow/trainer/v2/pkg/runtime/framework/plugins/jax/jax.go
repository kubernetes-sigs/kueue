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

package jax

import (
	"context"
	"fmt"

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

type Jax struct{}

var _ framework.EnforceMLPolicyPlugin = (*Jax)(nil)
var _ framework.CustomValidationPlugin = (*Jax)(nil)

const Name = "JAX"

func New(context.Context, client.Client, client.FieldIndexer, *configapi.Configuration) (framework.Plugin, error) {
	return &Jax{}, nil
}

func (j *Jax) Name() string {
	return Name
}

func (j *Jax) Validate(_ context.Context, runtimeInfo *runtime.Info, _, newObj *trainer.TrainJob) (admission.Warnings, field.ErrorList) {
	return nil, nil
}

func (j *Jax) EnforceMLPolicy(info *runtime.Info, trainJob *trainer.TrainJob) error {
	// Check if JAX MLPolicy is enabled
	if info == nil || info.RuntimePolicy.MLPolicySource == nil || info.RuntimePolicy.MLPolicySource.JAX == nil {
		return nil
	}

	// Find the trainer PodSet
	trainerPS := info.FindPodSetByAncestor(constants.AncestorTrainer)
	// Set the number of nodes (JAX processes/hosts) from TrainJob
	if trainerPS.Count != nil && trainJob.Spec.Trainer != nil && trainJob.Spec.Trainer.NumNodes != nil {
		*trainerPS.Count = *trainJob.Spec.Trainer.NumNodes
	}

	var trainerContainer *runtime.Container
	if trainJob.Spec.Trainer != nil {
		if trainerContainer = info.FindContainerByPodSetAncestorContainerName(constants.AncestorTrainer, constants.Node); trainerContainer != nil {
			// Get the number of nodes for distributed setup
			numNodes := ptr.Deref(ptr.Deref(trainerPS, runtime.PodSet{}).Count, 1)

			// Set JAX distributed environment variables
			apply.UpsertEnvVars(&trainerContainer.Env,
				// Total number of JAX processes (one per node/host)
				*corev1ac.EnvVar().
					WithName("JAX_NUM_PROCESSES").
					WithValue(fmt.Sprintf("%d", numNodes)),

				// Process ID - derived from job completion index
				*corev1ac.EnvVar().
					WithName("JAX_PROCESS_ID").
					WithValueFrom(corev1ac.EnvVarSource().
						WithFieldRef(corev1ac.ObjectFieldSelector().
							WithFieldPath(constants.JobCompletionIndexFieldPath))),

				// Coordinator address - first pod in the headless service
				*corev1ac.EnvVar().
					WithName("JAX_COORDINATOR_ADDRESS").
					WithValue(fmt.Sprintf("%s-%s-0-0.%s:%d",
						trainJob.Name,
						constants.Node,
						trainJob.Name,
						constants.ContainerTrainerPort)),
			)

			// Add container port for the headless service (needed for pod-to-pod communication)
			apply.UpsertPort(&trainerContainer.Ports, *corev1ac.ContainerPort().WithContainerPort(constants.ContainerTrainerPort))
		}
	}

	return nil
}

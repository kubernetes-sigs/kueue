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

package plainml

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	trainer "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
	"github.com/kubeflow/trainer/v2/pkg/apply"
	"github.com/kubeflow/trainer/v2/pkg/constants"
	"github.com/kubeflow/trainer/v2/pkg/runtime"
	"github.com/kubeflow/trainer/v2/pkg/runtime/framework"
)

var _ framework.EnforceMLPolicyPlugin = (*PlainML)(nil)

type PlainML struct{}

const Name = "PlainML"

func New(context.Context, client.Client, client.FieldIndexer) (framework.Plugin, error) {
	return &PlainML{}, nil
}

func (p *PlainML) Name() string {
	return Name
}

func (p *PlainML) EnforceMLPolicy(info *runtime.Info, trainJob *trainer.TrainJob) error {
	if info == nil ||
		(info.RuntimePolicy.MLPolicySource != nil && (info.RuntimePolicy.MLPolicySource.Torch != nil || info.RuntimePolicy.MLPolicySource.MPI != nil)) {
		return nil
	}

	// TrainJob contains the actual information for the number of nodes.
	if trainJob.Spec.Trainer != nil && trainJob.Spec.Trainer.NumNodes != nil {
		if trainerPS := info.FindPodSetByAncestor(constants.AncestorTrainer); trainerPS != nil && trainerPS.Count != nil {
			*trainerPS.Count = *trainJob.Spec.Trainer.NumNodes
		}
	}

	// Add envs from the TrainJob.
	var trainerContainer *runtime.Container
	if trainJob.Spec.Trainer != nil {
		if trainerContainer = info.FindContainerByPodSetAncestorContainerName(constants.AncestorTrainer, constants.Node); trainerContainer != nil {
			apply.UpsertEnvVars(&trainerContainer.Env, apply.EnvVars(trainJob.Spec.Trainer.Env...)...)
		}
	}
	info.SyncPodSetsToTemplateSpec()
	return nil
}

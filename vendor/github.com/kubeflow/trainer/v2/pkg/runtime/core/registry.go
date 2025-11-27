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

package core

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kubeflow/trainer/v2/pkg/runtime"
)

type Registry map[string]RuntimeRegistrar
type RuntimeRegistrar struct {
	factory      func(ctx context.Context, client client.Client, indexer client.FieldIndexer) (runtime.Runtime, error)
	dependencies []string
}

func NewRuntimeRegistry() Registry {
	return Registry{
		TrainingRuntimeGroupKind: RuntimeRegistrar{
			factory: NewTrainingRuntime,
		},
		ClusterTrainingRuntimeGroupKind: RuntimeRegistrar{
			factory:      NewClusterTrainingRuntime,
			dependencies: []string{TrainingRuntimeGroupKind},
		},
	}
}

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

package plugins

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kubeflow/trainer/v2/pkg/runtime/framework"
	"github.com/kubeflow/trainer/v2/pkg/runtime/framework/plugins/coscheduling"
	"github.com/kubeflow/trainer/v2/pkg/runtime/framework/plugins/jobset"
	"github.com/kubeflow/trainer/v2/pkg/runtime/framework/plugins/mpi"
	"github.com/kubeflow/trainer/v2/pkg/runtime/framework/plugins/plainml"
	"github.com/kubeflow/trainer/v2/pkg/runtime/framework/plugins/torch"
	"github.com/kubeflow/trainer/v2/pkg/runtime/framework/plugins/volcano"
)

type Registry map[string]func(ctx context.Context, client client.Client, indexer client.FieldIndexer) (framework.Plugin, error)

func NewRegistry() Registry {
	return Registry{
		coscheduling.Name: coscheduling.New,
		volcano.Name:      volcano.New,
		mpi.Name:          mpi.New,
		plainml.Name:      plainml.New,
		torch.Name:        torch.New,
		jobset.Name:       jobset.New,
	}
}

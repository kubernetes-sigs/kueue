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

package runtime

import (
	"context"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	trainer "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
)

type ReconcilerBuilder func(*builder.Builder, client.Client, cache.Cache) *builder.Builder

type Runtime interface {
	NewObjects(ctx context.Context, trainJob *trainer.TrainJob) ([]runtime.ApplyConfiguration, error)
	RuntimeInfo(trainJob *trainer.TrainJob, runtimeTemplateSpec any, mlPolicy *trainer.MLPolicy, podGroupPolicy *trainer.PodGroupPolicy) (*Info, error)
	TrainJobStatus(ctx context.Context, trainJob *trainer.TrainJob) (*trainer.TrainJobStatus, error)
	EventHandlerRegistrars() []ReconcilerBuilder
	ValidateObjects(ctx context.Context, old, new *trainer.TrainJob) (admission.Warnings, field.ErrorList)
}

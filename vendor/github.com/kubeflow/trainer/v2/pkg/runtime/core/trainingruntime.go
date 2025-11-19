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
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	jobsetv1alpha2 "sigs.k8s.io/jobset/api/jobset/v1alpha2"
	jobsetv1alpha2ac "sigs.k8s.io/jobset/client-go/applyconfiguration/jobset/v1alpha2"

	trainer "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
	"github.com/kubeflow/trainer/v2/pkg/apply"
	"github.com/kubeflow/trainer/v2/pkg/constants"
	"github.com/kubeflow/trainer/v2/pkg/runtime"
	fwkcore "github.com/kubeflow/trainer/v2/pkg/runtime/framework/core"
	fwkplugins "github.com/kubeflow/trainer/v2/pkg/runtime/framework/plugins"
	idxer "github.com/kubeflow/trainer/v2/pkg/runtime/indexer"
)

var (
	errorNotFoundSpecifiedTrainingRuntime = errors.New("TrainingRuntime specified in TrainJob is not found")
)

type TrainingRuntime struct {
	framework *fwkcore.Framework
	client    client.Client
}

var TrainingRuntimeGroupKind = schema.GroupKind{
	Group: trainer.GroupVersion.Group,
	Kind:  trainer.TrainingRuntimeKind,
}.String()

var _ runtime.Runtime = (*TrainingRuntime)(nil)

var trainingRuntimeFactory *TrainingRuntime

func NewTrainingRuntime(ctx context.Context, c client.Client, indexer client.FieldIndexer) (runtime.Runtime, error) {
	if err := indexer.IndexField(ctx, &trainer.TrainJob{}, idxer.TrainJobRuntimeRefKey, idxer.IndexTrainJobTrainingRuntime); err != nil {
		return nil, fmt.Errorf("setting index on TrainingRuntime for TrainJob: %w", err)
	}
	if err := indexer.IndexField(ctx, &trainer.TrainJob{}, idxer.TrainJobClusterRuntimeRefKey, idxer.IndexTrainJobClusterTrainingRuntime); err != nil {
		return nil, fmt.Errorf("setting index on ClusterTrainingRuntime for TrainJob: %w", err)
	}
	fwk, err := fwkcore.New(ctx, c, fwkplugins.NewRegistry(), indexer)
	if err != nil {
		return nil, err
	}
	trainingRuntimeFactory = &TrainingRuntime{
		framework: fwk,
		client:    c,
	}
	return trainingRuntimeFactory, nil
}

func (r *TrainingRuntime) NewObjects(ctx context.Context, trainJob *trainer.TrainJob) ([]apiruntime.ApplyConfiguration, error) {
	var trainingRuntime trainer.TrainingRuntime
	err := r.client.Get(ctx, client.ObjectKey{Namespace: trainJob.Namespace, Name: trainJob.Spec.RuntimeRef.Name}, &trainingRuntime)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errorNotFoundSpecifiedTrainingRuntime, err)
	}
	info, err := r.RuntimeInfo(trainJob, trainingRuntime.Spec.Template, trainingRuntime.Spec.MLPolicy, trainingRuntime.Spec.PodGroupPolicy)
	if err != nil {
		return nil, err
	}
	return r.framework.RunComponentBuilderPlugins(ctx, info, trainJob)
}

func (r *TrainingRuntime) RuntimeInfo(
	trainJob *trainer.TrainJob, runtimeTemplateSpec any, mlPolicy *trainer.MLPolicy, podGroupPolicy *trainer.PodGroupPolicy,
) (*runtime.Info, error) {

	jobSetTemplateSpec, ok := runtimeTemplateSpec.(trainer.JobSetTemplateSpec)
	if !ok {
		return nil, fmt.Errorf("unsupported runtimeTemplateSpec")
	}
	info, err := r.newRuntimeInfo(trainJob, jobSetTemplateSpec, mlPolicy, podGroupPolicy)
	if err != nil {
		return nil, err
	}
	if err = r.framework.RunEnforceMLPolicyPlugins(info, trainJob); err != nil {
		return nil, err
	}

	if err = r.framework.RunEnforcePodGroupPolicyPlugins(info, trainJob); err != nil {
		return nil, err
	}

	if err = r.framework.RunPodNetworkPlugins(info, trainJob); err != nil {
		return nil, err
	}

	return info, nil
}

func (r *TrainingRuntime) newRuntimeInfo(
	trainJob *trainer.TrainJob, jobSetTemplateSpec trainer.JobSetTemplateSpec, mlPolicy *trainer.MLPolicy, podGroupPolicy *trainer.PodGroupPolicy,
) (*runtime.Info, error) {
	propagationLabels := jobSetTemplateSpec.Labels
	if propagationLabels == nil && trainJob.Spec.Labels != nil {
		propagationLabels = make(map[string]string, len(trainJob.Spec.Labels))
	}
	for k, v := range trainJob.Spec.Labels {
		// The JobSetTemplateSpec labels are overridden by the TrainJob Labels (.spec.labels).
		propagationLabels[k] = v
	}
	propagationAnnotations := jobSetTemplateSpec.Annotations
	if propagationAnnotations == nil && trainJob.Spec.Annotations != nil {
		propagationAnnotations = make(map[string]string, len(trainJob.Spec.Annotations))
	}
	for k, v := range trainJob.Spec.Annotations {
		// The JobSetTemplateSpec annotations are overridden by the TrainJob Annotations (.spec.annotations).
		propagationAnnotations[k] = v
	}
	err := r.mergePodTemplateOverrides(trainJob, &jobSetTemplateSpec)
	if err != nil {
		return nil, err
	}
	jobSetSpecApply, err := apply.FromTypedObjWithFields[jobsetv1alpha2ac.JobSetSpecApplyConfiguration](&jobsetv1alpha2.JobSet{
		TypeMeta: metav1.TypeMeta{
			APIVersion: jobsetv1alpha2.GroupVersion.String(),
			Kind:       "JobSet",
		},
		Spec: jobSetTemplateSpec.Spec,
	}, "spec")
	if err != nil {
		return nil, err
	}

	opts := []runtime.InfoOption{
		runtime.WithLabels(propagationLabels),
		runtime.WithAnnotations(propagationAnnotations),
		runtime.WithMLPolicySource(mlPolicy),
		runtime.WithPodGroupPolicy(podGroupPolicy),
		runtime.WithTemplateSpecObjApply(jobSetSpecApply),
	}

	for i, rJob := range jobSetSpecApply.ReplicatedJobs {
		// TODO: Support multiple replicas ('.template.spec.replicatedJobs[*].replicas') for replicated Jobs.
		// REF: https://github.com/kubeflow/trainer/issues/2318
		count := ptr.Deref(rJob.Template.Spec.Parallelism, 1)
		var ancestor *string
		if metadata := rJob.Template.ObjectMetaApplyConfiguration; metadata != nil && metadata.Labels != nil {
			if labelAncestor, ok := metadata.Labels[constants.LabelTrainJobAncestor]; ok {
				if labelAncestor == constants.AncestorTrainer && mlPolicy != nil {
					count = ptr.Deref(mlPolicy.NumNodes, 1)
				}
				ancestor = &labelAncestor
			}
		}
		opts = append(opts, runtime.WithPodSet(
			*rJob.Name,
			ancestor,
			count,
			*jobSetTemplateSpec.Spec.ReplicatedJobs[i].Template.Spec.Template.Spec.DeepCopy(),
			rJob.Template.Spec.Template.Spec),
		)
	}

	return runtime.NewInfo(opts...), nil
}

func (r *TrainingRuntime) mergePodTemplateOverrides(trainJob *trainer.TrainJob, jobSetTemplateSpec *trainer.JobSetTemplateSpec) error {
	for _, podTemplateOverride := range trainJob.Spec.PodTemplateOverrides {
		for i, job := range jobSetTemplateSpec.Spec.ReplicatedJobs {
			if !slices.ContainsFunc(podTemplateOverride.TargetJobs, func(targetJob trainer.PodTemplateOverrideTargetJob) bool {
				return targetJob.Name == job.Name
			}) {
				continue
			}

			podTemplatePatch := map[string]any{}
			if podTemplateOverride.Metadata != nil {
				metadata := map[string]any{}
				if podTemplateOverride.Metadata.Labels != nil {
					metadata["labels"] = podTemplateOverride.Metadata.Labels
				}
				if podTemplateOverride.Metadata.Annotations != nil {
					metadata["annotations"] = podTemplateOverride.Metadata.Annotations
				}
				if len(metadata) > 0 {
					podTemplatePatch["metadata"] = metadata
				}
			}

			if podTemplateOverride.Spec != nil {
				podTemplatePatch["spec"] = podTemplateOverride.Spec
			}

			// Apply a strategic merge patch against the full PodTemplateSpec
			source, err := json.Marshal(job.Template.Spec.Template)
			if err != nil {
				return err
			}
			patch, err := json.Marshal(podTemplatePatch)
			if err != nil {
				return err
			}
			merged, err := strategicpatch.StrategicMergePatch(source, patch, corev1.PodTemplateSpec{})
			if err != nil {
				return err
			}
			mergedTemplate := corev1.PodTemplateSpec{}
			if err := json.Unmarshal(merged, &mergedTemplate); err != nil {
				return err
			}
			jobSetTemplateSpec.Spec.ReplicatedJobs[i].Template.Spec.Template = mergedTemplate
		}
	}
	return nil
}

func (r *TrainingRuntime) TrainJobStatus(ctx context.Context, trainJob *trainer.TrainJob) (*trainer.TrainJobStatus, error) {
	return r.framework.RunTrainJobStatusPlugin(ctx, trainJob)
}

func (r *TrainingRuntime) EventHandlerRegistrars() []runtime.ReconcilerBuilder {
	var builders []runtime.ReconcilerBuilder
	for _, ex := range r.framework.WatchExtensionPlugins() {
		builders = append(builders, ex.ReconcilerBuilders()...)
	}
	return builders
}

func (r *TrainingRuntime) ValidateObjects(ctx context.Context, old, new *trainer.TrainJob) (admission.Warnings, field.ErrorList) {
	trainingRuntime := &trainer.TrainingRuntime{}
	if err := r.client.Get(ctx, client.ObjectKey{
		Namespace: new.Namespace,
		Name:      new.Spec.RuntimeRef.Name,
	}, trainingRuntime); err != nil {
		return nil, field.ErrorList{
			field.Invalid(field.NewPath("spec", "runtimeRef"), new.Spec.RuntimeRef,
				fmt.Sprintf("%v: specified trainingRuntime must be created before the TrainJob is created", err)),
		}
	}
	info, _ := r.newRuntimeInfo(new, trainingRuntime.Spec.Template, trainingRuntime.Spec.MLPolicy, trainingRuntime.Spec.PodGroupPolicy) // ignoring the error here as the runtime configured should be valid
	return r.framework.RunCustomValidationPlugins(ctx, info, old, new)
}

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

package jobset

import (
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/utils/ptr"
	jobsetv1alpha2ac "sigs.k8s.io/jobset/client-go/applyconfiguration/jobset/v1alpha2"

	trainer "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
	"github.com/kubeflow/trainer/v2/pkg/apply"
	"github.com/kubeflow/trainer/v2/pkg/constants"
	"github.com/kubeflow/trainer/v2/pkg/runtime"
	jobsetplgconsts "github.com/kubeflow/trainer/v2/pkg/runtime/framework/plugins/jobset/constants"
)

type Builder struct {
	*jobsetv1alpha2ac.JobSetApplyConfiguration
}

func NewBuilder(jobSet *jobsetv1alpha2ac.JobSetApplyConfiguration) *Builder {
	return &Builder{
		JobSetApplyConfiguration: jobSet,
	}
}

// Initializer updates JobSet values for the initializer Job.
func (b *Builder) Initializer(trainJob *trainer.TrainJob) *Builder {
	for i, rJob := range b.Spec.ReplicatedJobs {
		jobMetadata := rJob.Template.ObjectMetaApplyConfiguration
		if jobMetadata == nil || jobMetadata.Labels == nil {
			continue
		}
		// Update values for the Dataset Initializer Job.
		if ancestor, ok := jobMetadata.Labels[constants.LabelTrainJobAncestor]; ok && ancestor == constants.DatasetInitializer {
			// TODO: Support multiple replicas ('.template.spec.replicatedJobs[*].replicas') for replicated Jobs.
			// REF: https://github.com/kubeflow/trainer/issues/2318
			b.Spec.ReplicatedJobs[i].Replicas = ptr.To[int32](1)
			for j, container := range rJob.Template.Spec.Template.Spec.Containers {
				// Update values for the dataset initializer container.
				if *container.Name == constants.DatasetInitializer && trainJob.Spec.Initializer != nil && trainJob.Spec.Initializer.Dataset != nil {
					env := &b.Spec.ReplicatedJobs[i].Template.Spec.Template.Spec.Containers[j].Env
					// Update the dataset initializer envs.
					if storageUri := trainJob.Spec.Initializer.Dataset.StorageUri; storageUri != nil {
						apply.UpsertEnvVars(env, *corev1ac.EnvVar().
							WithName(jobsetplgconsts.InitializerEnvStorageUri).
							WithValue(*storageUri))
					}
					apply.UpsertEnvVars(env, apply.EnvVars(trainJob.Spec.Initializer.Dataset.Env...)...)
					// Update the dataset initializer secret reference.
					if trainJob.Spec.Initializer.Dataset.SecretRef != nil {
						b.Spec.ReplicatedJobs[i].Template.Spec.Template.Spec.Containers[j].
							WithEnvFrom(corev1ac.EnvFromSource().
								WithSecretRef(corev1ac.SecretEnvSource().
									WithName(trainJob.Spec.Initializer.Dataset.SecretRef.Name)))
					}
				}
			}
		}
		// Update values for the Model Initializer Job.
		if ancestor, ok := jobMetadata.Labels[constants.LabelTrainJobAncestor]; ok && ancestor == constants.ModelInitializer {
			// TODO: Support multiple replicas ('.template.spec.replicatedJobs[*].replicas') for replicated Jobs.
			// REF: https://github.com/kubeflow/trainer/issues/2318
			b.Spec.ReplicatedJobs[i].Replicas = ptr.To[int32](1)
			for j, container := range rJob.Template.Spec.Template.Spec.Containers {
				// Update values for the model initializer container.
				if *container.Name == constants.ModelInitializer && trainJob.Spec.Initializer != nil && trainJob.Spec.Initializer.Model != nil {
					env := &b.Spec.ReplicatedJobs[i].Template.Spec.Template.Spec.Containers[j].Env
					// Update the model initializer envs.
					if storageUri := trainJob.Spec.Initializer.Model.StorageUri; storageUri != nil {
						apply.UpsertEnvVars(env, *corev1ac.EnvVar().
							WithName(jobsetplgconsts.InitializerEnvStorageUri).
							WithValue(*storageUri))
					}
					apply.UpsertEnvVars(env, apply.EnvVars(trainJob.Spec.Initializer.Model.Env...)...)
					// Update the model initializer secret reference.
					if trainJob.Spec.Initializer.Model.SecretRef != nil {
						b.Spec.ReplicatedJobs[i].Template.Spec.Template.Spec.Containers[j].
							WithEnvFrom(corev1ac.EnvFromSource().
								WithSecretRef(corev1ac.SecretEnvSource().
									WithName(trainJob.Spec.Initializer.Model.SecretRef.Name)))
					}
				}
			}
		}
	}
	return b
}

// isRunLauncherAsNode returns true if runLauncherAsNode is set to true in the MPI policy.
func (b *Builder) isRunLauncherAsNode(info *runtime.Info) bool {
	return info.RuntimePolicy.MLPolicySource != nil &&
		info.RuntimePolicy.MLPolicySource.MPI != nil &&
		info.RuntimePolicy.MLPolicySource.MPI.RunLauncherAsNode != nil &&
		*info.RuntimePolicy.MLPolicySource.MPI.RunLauncherAsNode
}

// Trainer updates JobSet values for the trainer Job.
func (b *Builder) Trainer(info *runtime.Info, trainJob *trainer.TrainJob) *Builder {
	for i, rJob := range b.Spec.ReplicatedJobs {
		ancestor := ""
		jobMetadata := rJob.Template.ObjectMetaApplyConfiguration
		if jobMetadata != nil && jobMetadata.Labels != nil {
			ancestor = jobMetadata.Labels[constants.LabelTrainJobAncestor]
		}
		if ancestor == constants.AncestorTrainer {
			// TODO: Support multiple replicas ('.template.spec.replicatedJobs[*].replicas') for replicated Jobs.
			// REF: https://github.com/kubeflow/trainer/issues/2318
			b.Spec.ReplicatedJobs[i].Replicas = ptr.To[int32](1)
			// Update values for the Trainer container.
			for j, container := range rJob.Template.Spec.Template.Spec.Containers {
				if *container.Name == constants.Node {
					// Update values from the TrainJob trainer.
					if jobTrainer := trainJob.Spec.Trainer; jobTrainer != nil {
						if image := jobTrainer.Image; image != nil {
							b.Spec.ReplicatedJobs[i].Template.Spec.Template.Spec.Containers[j].Image = image
						}
						if command := jobTrainer.Command; command != nil {
							b.Spec.ReplicatedJobs[i].Template.Spec.Template.Spec.Containers[j].Command = command
						}
						if args := jobTrainer.Args; args != nil {
							b.Spec.ReplicatedJobs[i].Template.Spec.Template.Spec.Containers[j].Args = args
						}
					}
				}
			}
		}
		if ancestor == constants.AncestorTrainer || b.isRunLauncherAsNode(info) && *rJob.Name == constants.Node {
			// TODO (andreyvelich): For MPI we should apply container resources to the Node ReplicatedJob also.
			// Eventually, we should find better way to propagate resources from TrainJob to JobSet.
			for j, container := range rJob.Template.Spec.Template.Spec.Containers {
				if *container.Name == constants.Node {
					if jobTrainer := trainJob.Spec.Trainer; jobTrainer != nil {
						if resourcesPerNode := jobTrainer.ResourcesPerNode; resourcesPerNode != nil &&
							(resourcesPerNode.Limits != nil || resourcesPerNode.Requests != nil) {
							requirements := corev1ac.ResourceRequirements()
							if limits := resourcesPerNode.Limits; limits != nil {
								requirements.WithLimits(limits)
							}
							if requests := resourcesPerNode.Requests; requests != nil {
								requirements.WithRequests(requests)
							}
							b.Spec.ReplicatedJobs[i].Template.Spec.Template.Spec.Containers[j].
								WithResources(requirements)
						}
						apply.UpsertEnvVars(
							&b.Spec.ReplicatedJobs[i].Template.Spec.Template.Spec.Containers[j].Env,
							apply.EnvVars(jobTrainer.Env...)...,
						)
					}
				}
			}
		}
	}
	return b
}

// TODO: Supporting merge labels would be great.

func (b *Builder) PodLabels(labels map[string]string) *Builder {
	for i := range b.Spec.ReplicatedJobs {
		b.Spec.ReplicatedJobs[i].Template.Spec.Template.WithLabels(labels)
	}
	return b
}

func (b *Builder) PodAnnotations(annotations map[string]string) *Builder {
	for i := range b.Spec.ReplicatedJobs {
		b.Spec.ReplicatedJobs[i].Template.Spec.Template.WithAnnotations(annotations)
	}
	return b
}

func (b *Builder) Suspend(suspend *bool) *Builder {
	b.Spec.Suspend = suspend
	return b
}

func (b *Builder) Build() *jobsetv1alpha2ac.JobSetApplyConfiguration {
	return b.JobSetApplyConfiguration
}

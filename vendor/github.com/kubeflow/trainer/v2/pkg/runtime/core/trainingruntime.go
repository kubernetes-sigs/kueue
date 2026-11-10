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
	"maps"
	"slices"
	"sort"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/apimachinery/pkg/util/validation/field"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	jobsetv1alpha2 "sigs.k8s.io/jobset/api/jobset/v1alpha2"
	jobsetv1alpha2ac "sigs.k8s.io/jobset/client-go/applyconfiguration/jobset/v1alpha2"

	configapi "github.com/kubeflow/trainer/v2/pkg/apis/config/v1alpha1"
	trainer "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
	"github.com/kubeflow/trainer/v2/pkg/apply"
	"github.com/kubeflow/trainer/v2/pkg/constants"
	"github.com/kubeflow/trainer/v2/pkg/features"
	"github.com/kubeflow/trainer/v2/pkg/runtime"
	fwkcore "github.com/kubeflow/trainer/v2/pkg/runtime/framework/core"
	fwkplugins "github.com/kubeflow/trainer/v2/pkg/runtime/framework/plugins"
	idxer "github.com/kubeflow/trainer/v2/pkg/runtime/indexer"
	trainingruntimeutil "github.com/kubeflow/trainer/v2/pkg/util/trainingruntime"
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

func NewTrainingRuntime(ctx context.Context, c client.Client, indexer client.FieldIndexer, cfg *configapi.Configuration) (runtime.Runtime, error) {
	if err := indexer.IndexField(ctx, &trainer.TrainJob{}, idxer.TrainJobRuntimeRefKey, idxer.IndexTrainJobTrainingRuntime); err != nil {
		return nil, fmt.Errorf("setting index on TrainingRuntime for TrainJob: %w", err)
	}
	if err := indexer.IndexField(ctx, &trainer.TrainJob{}, idxer.TrainJobClusterRuntimeRefKey, idxer.IndexTrainJobClusterTrainingRuntime); err != nil {
		return nil, fmt.Errorf("setting index on ClusterTrainingRuntime for TrainJob: %w", err)
	}
	fwk, err := fwkcore.New(ctx, c, fwkplugins.NewRegistry(), indexer, cfg)
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
	// Try to get runtime from snapshot first
	if err := getRuntimeSnapshot(ctx, r.client, trainJob, &trainingRuntime); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("unable to get runtime snapshot: %w", err)
		}

		// Snapshot doesn't exist, load runtime from API server
		err := r.client.Get(ctx, client.ObjectKey{Namespace: trainJob.Namespace, Name: trainJob.Spec.RuntimeRef.Name}, &trainingRuntime)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errorNotFoundSpecifiedTrainingRuntime, err)
		}

		// Create snapshot for future reconciliations
		if err := createRuntimeSnapshot(ctx, r.client, trainJob, &trainingRuntime); err != nil {
			return nil, fmt.Errorf("creating runtime snapshot: %w", err)
		}
	}

	info, err := r.RuntimeInfo(trainJob, trainingRuntime.Spec.Template, trainingRuntime.Spec.MLPolicy, trainingRuntime.Spec.PodGroupPolicy, trainingRuntime.Spec.Scheduling)
	if err != nil {
		return nil, err
	}
	return r.framework.RunComponentBuilderPlugins(ctx, info, trainJob)
}

// DeleteObjects returns the previously materialized objects that should now be removed
// for the TrainJob. It runs on every TrainJob reconcile and does not read the runtime:
// each ComponentDeleterPlugin decides from the TrainJob state whether anything is returned.
func (r *TrainingRuntime) DeleteObjects(ctx context.Context, trainJob *trainer.TrainJob) ([]client.Object, error) {
	return r.framework.RunComponentDeleterPlugins(ctx, trainJob)
}

// RuntimeInfo builds the Info object for a TrainJob and consolidates it through the
// Build Phase extension points, in this order:
//  1. EnforceMLPolicy, then EnforcePodGroupPolicy, then EnforceScheduling, for the parameters
//     declared in the runtime `.spec.mlPolicy`, `.spec.podGroupPolicy`, and `.spec.scheduling`.
//  2. EnforcePodSpec, for the PodSet concerns enabled outside of MLPolicy and PodGroupPolicy APIs.
//  3. PreBuildSync, which consolidates the Info object with the concrete runtime template.
func (r *TrainingRuntime) RuntimeInfo(
	trainJob *trainer.TrainJob, runtimeTemplateSpec any, mlPolicy *trainer.MLPolicy, podGroupPolicy *trainer.PodGroupPolicy,
	scheduling *trainer.TrainingRuntimeScheduling,
) (*runtime.Info, error) {

	jobSetTemplateSpec, ok := runtimeTemplateSpec.(trainer.JobSetTemplateSpec)
	if !ok {
		return nil, fmt.Errorf("unsupported runtimeTemplateSpec")
	}
	info, err := r.newRuntimeInfo(trainJob, jobSetTemplateSpec, mlPolicy, podGroupPolicy, scheduling)
	if err != nil {
		return nil, err
	}
	if err = r.framework.RunEnforceMLPolicyPlugins(info, trainJob); err != nil {
		return nil, err
	}
	if err = r.framework.RunEnforcePodGroupPolicyPlugins(info, trainJob); err != nil {
		return nil, err
	}
	if err = r.framework.RunEnforceSchedulingPlugins(info, trainJob); err != nil {
		return nil, err
	}
	if err = r.framework.RunEnforcePodSpecPlugins(info, trainJob); err != nil {
		return nil, err
	}
	if err = r.framework.RunPreComponentBuilderPlugins(info, trainJob); err != nil {
		return nil, err
	}

	return info, nil
}

func (r *TrainingRuntime) newRuntimeInfo(
	trainJob *trainer.TrainJob, jobSetTemplateSpec trainer.JobSetTemplateSpec, mlPolicy *trainer.MLPolicy, podGroupPolicy *trainer.PodGroupPolicy,
	scheduling *trainer.TrainingRuntimeScheduling,
) (*runtime.Info, error) {
	propagationLabels := maps.Clone(jobSetTemplateSpec.Labels)
	propagationAnnotations := maps.Clone(jobSetTemplateSpec.Annotations)
	for _, patch := range trainJob.Spec.RuntimePatches {
		if patch.TrainingRuntimeSpec != nil && patch.TrainingRuntimeSpec.Template != nil && patch.TrainingRuntimeSpec.Template.Metadata != nil {
			if propagationLabels == nil && len(patch.TrainingRuntimeSpec.Template.Metadata.Labels) > 0 {
				propagationLabels = make(map[string]string)
			}
			for k, v := range patch.TrainingRuntimeSpec.Template.Metadata.Labels {
				propagationLabels[k] = v
			}
			if propagationAnnotations == nil && len(patch.TrainingRuntimeSpec.Template.Metadata.Annotations) > 0 {
				propagationAnnotations = make(map[string]string)
			}
			for k, v := range patch.TrainingRuntimeSpec.Template.Metadata.Annotations {
				propagationAnnotations[k] = v
			}
		}
	}
	err := r.mergeRuntimePatches(trainJob, &jobSetTemplateSpec)
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
		runtime.WithScheduling(scheduling),
		runtime.WithTemplateSpecObjApply(jobSetSpecApply),
	}

	for i, rJob := range jobSetSpecApply.ReplicatedJobs {
		// The PodSet count is the total number of Pods across all replicas of the replicatedJob.
		// The JobSet plugin divides it by replicas to compute per-replica Parallelism/Completions.
		// REF: https://github.com/kubeflow/trainer/issues/2318
		count := ptr.Deref(rJob.Template.Spec.Parallelism, 1) * ptr.Deref(rJob.Replicas, constants.DefaultJobReplicas)
		var ancestor *string
		if metadata := rJob.Template.ObjectMetaApplyConfiguration; metadata != nil && metadata.Labels != nil {
			if labelAncestor, ok := metadata.Labels[constants.LabelTrainJobAncestor]; ok {
				if labelAncestor == constants.AncestorTrainer && mlPolicy != nil {
					// numNodes is the total number of nodes across all trainer replicas (e.g. TPU slices).
					count = ptr.Deref(mlPolicy.NumNodes, 1)
				}
				ancestor = &labelAncestor
			}
		}
		isTrainerAncestor := ancestor != nil && *ancestor == constants.AncestorTrainer && mlPolicy != nil
		// For MPI, the processes run on the node replicatedJob, and on the launcher, which is the
		// trainer ancestor, only when it runs as a node.
		isMPI := mlPolicy != nil && mlPolicy.MPI != nil
		isTrainerNode := isTrainerAncestor && (!isMPI || ptr.Deref(mlPolicy.MPI.RunLauncherAsNode, false))
		isMPINode := isMPI && ptr.Deref(rJob.Name, "") == constants.Node
		if isTrainerNode || isMPINode {
			if applyPodSpec := jobSetSpecApply.ReplicatedJobs[i].Template.Spec.Template.Spec; applyPodSpec != nil {
				if jobTrainer := trainJob.Spec.Trainer; jobTrainer != nil {
					applyTrainerPodResourceClaims(applyPodSpec, jobTrainer.ResourceClaimsPerNode)
					hasNodeResources := jobTrainer.ResourcesPerNode != nil ||
						(features.Enabled(features.TrainJobDynamicResourceAllocation) && len(jobTrainer.ResourceClaimsPerNode) > 0)
					for k := range applyPodSpec.Containers {
						if ptr.Deref(applyPodSpec.Containers[k].Name, "") != constants.Node || !hasNodeResources {
							continue
						}
						mergedRes, mergeErr := mergeTrainerNodeResources(&applyPodSpec.Containers[k], jobTrainer.ResourcesPerNode, jobTrainer.ResourceClaimsPerNode)
						if mergeErr != nil {
							return nil, mergeErr
						}
						// Keep the typed template in sync since it feeds PodSet.SinglePodRequests.
						jobSetTemplateSpec.Spec.ReplicatedJobs[i].Template.Spec.Template.Spec.Containers[k].Resources = mergedRes
						applyPodSpec.Containers[k].Resources = toApplyConfig(mergedRes)
						break
					}
				}
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

func (r *TrainingRuntime) mergeRuntimePatches(trainJob *trainer.TrainJob, jobSetTemplateSpec *trainer.JobSetTemplateSpec) error {
	// Capture the original ReplicatedJobs ordering since SMP may reorder the list.
	order := make(map[string]int, len(jobSetTemplateSpec.Spec.ReplicatedJobs))
	for i, rJob := range jobSetTemplateSpec.Spec.ReplicatedJobs {
		order[rJob.Name] = i
	}

	for _, runtimePatch := range trainJob.Spec.RuntimePatches {
		if runtimePatch.TrainingRuntimeSpec == nil ||
			runtimePatch.TrainingRuntimeSpec.Template == nil ||
			runtimePatch.TrainingRuntimeSpec.Template.Spec == nil {
			continue
		}
		source, err := json.Marshal(jobSetTemplateSpec.Spec)
		if err != nil {
			return err
		}
		patch, err := json.Marshal(runtimePatch.TrainingRuntimeSpec.Template.Spec)
		if err != nil {
			return err
		}
		merged, err := strategicpatch.StrategicMergePatch(source, patch, jobsetv1alpha2.JobSetSpec{})
		if err != nil {
			return err
		}
		mergedSpec := jobsetv1alpha2.JobSetSpec{}
		if err := json.Unmarshal(merged, &mergedSpec); err != nil {
			return err
		}
		jobSetTemplateSpec.Spec = mergedSpec
	}

	// Restore the ReplicatedJobs order defined in the runtime template.
	sort.SliceStable(jobSetTemplateSpec.Spec.ReplicatedJobs, func(i, j int) bool {
		return order[jobSetTemplateSpec.Spec.ReplicatedJobs[i].Name] <
			order[jobSetTemplateSpec.Spec.ReplicatedJobs[j].Name]
	})

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
	// Validate against the snapshot the TrainJob was built from, falling back to the live
	// runtime when no snapshot exists yet, as NewObjects does.
	trainingRuntime := &trainer.TrainingRuntime{}
	if err := getRuntimeSnapshot(ctx, r.client, new, trainingRuntime); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, field.ErrorList{
				field.InternalError(field.NewPath("spec", "runtimeRef"), fmt.Errorf("unable to get runtime snapshot: %w", err)),
			}
		}
		trainingRuntime = &trainer.TrainingRuntime{}
		if err := r.client.Get(ctx, client.ObjectKey{
			Namespace: new.Namespace,
			Name:      new.Spec.RuntimeRef.Name,
		}, trainingRuntime); err != nil {
			if !apierrors.IsNotFound(err) {
				return nil, field.ErrorList{
					field.InternalError(field.NewPath("spec", "runtimeRef"), fmt.Errorf("unable to get trainingRuntime: %w", err)),
				}
			}
			return nil, field.ErrorList{
				field.Invalid(field.NewPath("spec", "runtimeRef"), new.Spec.RuntimeRef,
					fmt.Sprintf("%v: specified trainingRuntime must be created before the TrainJob is created", err)),
			}
		}
	}
	var warnings admission.Warnings
	if trainingruntimeutil.IsSupportDeprecated(trainingRuntime.Labels) {
		warnings = append(warnings, fmt.Sprintf(
			"Referenced TrainingRuntime \"%s\" is deprecated and will be removed in a future release of Kubeflow Trainer. See runtime deprecation policy: %s",
			trainingRuntime.Name,
			constants.RuntimeDeprecationPolicyURL,
		))
	}
	info, _ := r.newRuntimeInfo(new, trainingRuntime.Spec.Template, trainingRuntime.Spec.MLPolicy, trainingRuntime.Spec.PodGroupPolicy, trainingRuntime.Spec.Scheduling) // ignoring the error here as the runtime configured should be valid
	fwWarnings, errs := r.framework.RunCustomValidationPlugins(ctx, info, old, new)
	if len(fwWarnings) != 0 {
		warnings = append(warnings, fwWarnings...)
	}
	return warnings, errs
}

// applyTrainerPodResourceClaims upserts the TrainJob's resourceClaimsPerNode into the Pod's
// resourceClaims by name. It is a no-op while the TrainJobDynamicResourceAllocation gate is disabled.
func applyTrainerPodResourceClaims(podSpec *corev1ac.PodSpecApplyConfiguration, claims []trainer.TrainerResourceClaim) {
	if !features.Enabled(features.TrainJobDynamicResourceAllocation) {
		return
	}
	for _, claim := range claims {
		podClaim := corev1ac.PodResourceClaim().
			WithName(claim.Name).
			WithResourceClaimTemplateName(claim.ResourceClaimTemplateName)
		if idx := slices.IndexFunc(podSpec.ResourceClaims, func(c corev1ac.PodResourceClaimApplyConfiguration) bool {
			return c.Name != nil && *c.Name == claim.Name
		}); idx != -1 {
			podSpec.ResourceClaims[idx] = *podClaim
			continue
		}
		podSpec.ResourceClaims = append(podSpec.ResourceClaims, *podClaim)
	}
}

// mergeTrainerNodeResources merges the TrainJob's resourcesPerNode and resourceClaimsPerNode over the
// runtime node container's resources with strategic merge patch: requests and limits merge per key
// and claims merge by name. A runtime claim overridden by resourceClaimsPerNode is dropped, including its request.
// Claims are ignored while the TrainJobDynamicResourceAllocation gate is disabled.
func mergeTrainerNodeResources(
	runtimeNode *corev1ac.ContainerApplyConfiguration, resourcesPerNode *corev1.ResourceRequirements, resourceClaimsPerNode []trainer.TrainerResourceClaim,
) (corev1.ResourceRequirements, error) {
	var nodeRes corev1.ResourceRequirements
	if resourcesPerNode != nil {
		nodeRes.Limits = resourcesPerNode.Limits
		nodeRes.Requests = resourcesPerNode.Requests
	}
	if features.Enabled(features.TrainJobDynamicResourceAllocation) {
		// The node container consumes every claim declared in resourceClaimsPerNode.
		for _, claim := range resourceClaimsPerNode {
			nodeRes.Claims = append(nodeRes.Claims, corev1.ResourceClaim{Name: claim.Name})
		}
	}

	var runtimeRes corev1.ResourceRequirements
	if runtimeNode != nil && runtimeNode.Resources != nil {
		if runtimeNode.Resources.Limits != nil {
			runtimeRes.Limits = *runtimeNode.Resources.Limits
		}
		if runtimeNode.Resources.Requests != nil {
			runtimeRes.Requests = *runtimeNode.Resources.Requests
		}
		for _, claim := range runtimeNode.Resources.Claims {
			name := ptr.Deref(claim.Name, "")
			// resourceClaimsPerNode replaces the Pod-level claim, so a request scoped to the
			// previous claim source no longer applies.
			if slices.ContainsFunc(nodeRes.Claims, func(c corev1.ResourceClaim) bool { return c.Name == name }) {
				continue
			}
			runtimeRes.Claims = append(runtimeRes.Claims, corev1.ResourceClaim{
				Name:    name,
				Request: ptr.Deref(claim.Request, ""),
			})
		}
	}

	return trainingruntimeutil.MergeResourceRequirements(runtimeRes, nodeRes)
}

// toApplyConfig converts typed ResourceRequirements to the apply configuration used in the JobSet template.
func toApplyConfig(res corev1.ResourceRequirements) *corev1ac.ResourceRequirementsApplyConfiguration {
	out := corev1ac.ResourceRequirements()
	if res.Limits != nil {
		out.WithLimits(maps.Clone(res.Limits))
	}
	if res.Requests != nil {
		out.WithRequests(maps.Clone(res.Requests))
	}
	for _, claim := range res.Claims {
		c := corev1ac.ResourceClaim().WithName(claim.Name)
		if claim.Request != "" {
			c.WithRequest(claim.Request)
		}
		out.WithClaims(c)
	}
	return out
}

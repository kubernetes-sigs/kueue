/*
Copyright 2023 The Kubernetes Authors.

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

package raycluster

import (
	"context"
	"fmt"

	rayclusterapi "github.com/ray-project/kuberay/ray-operator/apis/ray/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"sigs.k8s.io/kueue/pkg/controller/jobframework"
)

type RayClusterWebhook struct {
	manageJobsWithoutQueueName bool
}

// SetupRayClusterWebhook configures the webhook for rayclusterapi RayCluster.
func SetupRayClusterWebhook(mgr ctrl.Manager, opts ...jobframework.Option) error {
	options := jobframework.DefaultOptions
	for _, opt := range opts {
		opt(&options)
	}
	wh := &RayClusterWebhook{
		manageJobsWithoutQueueName: options.ManageJobsWithoutQueueName,
	}
	return ctrl.NewWebhookManagedBy(mgr).
		For(&rayclusterapi.RayCluster{}).
		WithDefaulter(wh).
		WithValidator(wh).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-ray-io-v1alpha1-raycluster,mutating=true,failurePolicy=fail,sideEffects=None,groups=ray.io,resources=rayclusters,verbs=create,versions=v1alpha1,name=mraycluster.kb.io,admissionReviewVersions=v1

var _ webhook.CustomDefaulter = &RayClusterWebhook{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type
func (w *RayClusterWebhook) Default(ctx context.Context, obj runtime.Object) error {
	job := obj.(*rayclusterapi.RayCluster)
	log := ctrl.LoggerFrom(ctx).WithName("raycluster-webhook")
	log.V(5).Info("Applying defaults", "job", klog.KObj(job))
	jobframework.ApplyDefaultForSuspend((*RayCluster)(job), w.manageJobsWithoutQueueName)
	return nil
}

// +kubebuilder:webhook:path=/validate-ray-io-v1alpha1-raycluster,mutating=false,failurePolicy=fail,sideEffects=None,groups=ray.io,resources=rayclusters,verbs=create;update,versions=v1alpha1,name=vraycluster.kb.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &RayClusterWebhook{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *RayClusterWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	job := obj.(*rayclusterapi.RayCluster)
	log := ctrl.LoggerFrom(ctx).WithName("raycluster-webhook")
	log.Info("Validating create", "job", klog.KObj(job))
	return nil, w.validateCreate(job).ToAggregate()
}

func (w *RayClusterWebhook) validateCreate(job *rayclusterapi.RayCluster) field.ErrorList {
	var allErrors field.ErrorList
	kueueJob := (*RayCluster)(job)

	if w.manageJobsWithoutQueueName || jobframework.QueueName(kueueJob) != "" {
		spec := &job.Spec
		specPath := field.NewPath("spec")

		// Should always delete the cluster after the sob has ended, otherwise it will continue to the queue's resources.
		//if !spec.ShutdownAfterJobFinishes {
		//	allErrors = append(allErrors, field.Invalid(specPath.Child("shutdownAfterJobFinishes"), spec.ShutdownAfterJobFinishes, "a kueue managed job should delete the cluster after finishing"))
		//}

		// Should not want existing cluster. Keuue (workload) should be able to control the admission of the actual work, not only the trigger.
		//if len(spec.ClusterSelector) > 0 {
		//	allErrors = append(allErrors, field.Invalid(specPath.Child("HeadGroupSpec.Replicas"), spec.HeadGroupSpec.Replicas, "a kueue managed job should not use an existing cluster"))
		//}
		clusterSpec := spec
		clusterSpecPath := specPath.Child("rayClusterSpec")

		// Should not use auto scaler. Once the resources are reserved by queue the cluster should do it's best to use them.
		if ptr.Deref(clusterSpec.EnableInTreeAutoscaling, false) {
			allErrors = append(allErrors, field.Invalid(clusterSpecPath.Child("enableInTreeAutoscaling"), clusterSpec.EnableInTreeAutoscaling, "a kueue managed job should not use autoscaling"))
		}

		// Should limit the worker count to 8 - 1 (max podSets num - cluster head)
		if len(clusterSpec.WorkerGroupSpecs) > 7 {
			allErrors = append(allErrors, field.TooMany(clusterSpecPath.Child("workerGroupSpecs"), len(clusterSpec.WorkerGroupSpecs), 7))
		}

		// None of the workerGroups should be named "head"
		for i := range clusterSpec.WorkerGroupSpecs {
			if clusterSpec.WorkerGroupSpecs[i].GroupName == headGroupPodSetName {
				allErrors = append(allErrors, field.Forbidden(clusterSpecPath.Child("workerGroupSpecs").Index(i).Child("groupName"), fmt.Sprintf("%q is reserved for the head group", headGroupPodSetName)))
			}
		}
	}

	allErrors = append(allErrors, jobframework.ValidateCreateForQueueName(kueueJob)...)
	return allErrors
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *RayClusterWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldJob := oldObj.(*rayclusterapi.RayCluster)
	newJob := newObj.(*rayclusterapi.RayCluster)
	log := ctrl.LoggerFrom(ctx).WithName("raycluster-webhook")
	if w.manageJobsWithoutQueueName || jobframework.QueueName((*RayCluster)(newJob)) != "" {
		log.Info("Validating update", "job", klog.KObj(newJob))
		allErrors := jobframework.ValidateUpdateForQueueName((*RayCluster)(oldJob), (*RayCluster)(newJob))
		allErrors = append(allErrors, w.validateCreate(newJob)...)
		allErrors = append(allErrors, jobframework.ValidateUpdateForWorkloadPriorityClassName((*RayCluster)(oldJob), (*RayCluster)(newJob))...)
		return nil, allErrors.ToAggregate()
	}
	return nil, nil
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type
func (w *RayClusterWebhook) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

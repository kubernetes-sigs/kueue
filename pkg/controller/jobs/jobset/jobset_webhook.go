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

package jobset

import (
	"context"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	jobsetapi "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	"sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/queue"
)

type JobSetWebhook struct {
	manageJobsWithoutQueueName bool
	queues                     *queue.Manager
	cache                      *cache.Cache
}

// SetupJobSetWebhook configures the webhook for kubeflow JobSet.
func SetupJobSetWebhook(mgr ctrl.Manager, opts ...jobframework.Option) error {
	options := jobframework.ProcessOptions(opts...)
	wh := &JobSetWebhook{
		manageJobsWithoutQueueName: options.ManageJobsWithoutQueueName,
		queues:                     options.Queues,
		cache:                      options.Cache,
	}
	return ctrl.NewWebhookManagedBy(mgr).
		For(&jobsetapi.JobSet{}).
		WithDefaulter(wh).
		WithValidator(wh).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-jobset-x-k8s-io-v1alpha2-jobset,mutating=true,failurePolicy=fail,sideEffects=None,groups=jobset.x-k8s.io,resources=jobsets,verbs=create,versions=v1alpha2,name=mjobset.kb.io,admissionReviewVersions=v1

var _ webhook.CustomDefaulter = &JobSetWebhook{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type
func (w *JobSetWebhook) Default(ctx context.Context, obj runtime.Object) error {
	jobSet := fromObject(obj)
	log := ctrl.LoggerFrom(ctx).WithName("jobset-webhook")
	log.V(5).Info("Applying defaults", "jobset", klog.KObj(jobSet))

	jobframework.ApplyDefaultForSuspend(jobSet, w.manageJobsWithoutQueueName)

	if canDefaultManagedBy(jobSet.Spec.ManagedBy) {
		localQueueName, found := jobSet.Labels[constants.QueueLabel]
		if !found {
			return nil
		}
		clusterQueueName, err := w.queues.ClusterQueueFromLocalQueue(queue.QueueKey(jobSet.ObjectMeta.Namespace, localQueueName))
		if err != nil {
			log.V(5).Info("Failed to get cluster queue name", "jobset", klog.KObj(jobSet), "error", err)
			return nil
		}
		for _, admissionCheck := range w.cache.AdmissionChecksForClusterQueue(clusterQueueName) {
			if admissionCheck.Controller == v1alpha1.MultiKueueControllerName {
				log.V(5).Info("Defaulting ManagedBy", "jobset", klog.KObj(jobSet), "oldManagedBy", jobSet.Spec.ManagedBy, "managedBy", v1alpha1.MultiKueueControllerName)
				jobSet.Spec.ManagedBy = ptr.To(v1alpha1.MultiKueueControllerName)
				return nil
			}
		}
	}

	return nil
}

func canDefaultManagedBy(jobSetSpecManagedBy *string) bool {
	return features.Enabled(features.MultiKueue) &&
		(jobSetSpecManagedBy == nil || *jobSetSpecManagedBy == jobsetapi.JobSetControllerName)
}

// +kubebuilder:webhook:path=/validate-jobset-x-k8s-io-v1alpha2-jobset,mutating=false,failurePolicy=fail,sideEffects=None,groups=jobset.x-k8s.io,resources=jobsets,verbs=create;update,versions=v1alpha2,name=vjobset.kb.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &JobSetWebhook{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *JobSetWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	jobSet := fromObject(obj)
	log := ctrl.LoggerFrom(ctx).WithName("jobset-webhook")
	log.Info("Validating create", "jobset", klog.KObj(jobSet))
	return nil, jobframework.ValidateJobOnCreate(jobSet).ToAggregate()
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *JobSetWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldJobSet := fromObject(oldObj)
	newJobSet := fromObject(newObj)
	log := ctrl.LoggerFrom(ctx).WithName("jobset-webhook")
	log.Info("Validating update", "jobset", klog.KObj(newJobSet))
	allErrs := jobframework.ValidateJobOnUpdate(oldJobSet, newJobSet)
	allErrs = append(allErrs, jobframework.ValidateJobOnCreate(newJobSet)...)
	return nil, allErrs.ToAggregate()
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type
func (w *JobSetWebhook) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

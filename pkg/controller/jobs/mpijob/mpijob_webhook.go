/*
Copyright The Kubernetes Authors.

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

package mpijob

import (
	"context"
	"sort"

	"github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobframework/webhook"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/queue"
	"sigs.k8s.io/kueue/pkg/util/kubeversion"
	"sigs.k8s.io/kueue/pkg/util/podset"
)

var (
	mpiReplicaSpecsPath = field.NewPath("spec", "mpiReplicaSpecs")
)

type MpiJobWebhook struct {
	client                       client.Client
	manageJobsWithoutQueueName   bool
	managedJobsNamespaceSelector labels.Selector
	kubeServerVersion            *kubeversion.ServerVersionFetcher
	queues                       *queue.Manager
	cache                        *cache.Cache
}

// SetupMPIJobWebhook configures the webhook for MPIJob.
func SetupMPIJobWebhook(mgr ctrl.Manager, opts ...jobframework.Option) error {
	options := jobframework.ProcessOptions(opts...)
	wh := &MpiJobWebhook{
		client:                       mgr.GetClient(),
		manageJobsWithoutQueueName:   options.ManageJobsWithoutQueueName,
		managedJobsNamespaceSelector: options.ManagedJobsNamespaceSelector,
		kubeServerVersion:            options.KubeServerVersion,
		queues:                       options.Queues,
		cache:                        options.Cache,
	}
	obj := &v2beta1.MPIJob{}
	return webhook.WebhookManagedBy(mgr).
		For(obj).
		WithMutationHandler(admission.WithCustomDefaulter(mgr.GetScheme(), obj, wh)).
		WithValidator(wh).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-kubeflow-org-v2beta1-mpijob,mutating=true,failurePolicy=fail,sideEffects=None,groups=kubeflow.org,resources=mpijobs,verbs=create,versions=v2beta1,name=mmpijob.kb.io,admissionReviewVersions=v1

var _ admission.CustomDefaulter = &MpiJobWebhook{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type
func (w *MpiJobWebhook) Default(ctx context.Context, obj runtime.Object) error {
	mpiJob := fromObject(obj)
	log := ctrl.LoggerFrom(ctx).WithName("mpijob-webhook")
	log.V(5).Info("Applying defaults")

	jobframework.ApplyDefaultLocalQueue(mpiJob.Object(), w.queues.DefaultLocalQueueExist)
	if err := jobframework.ApplyDefaultForSuspend(ctx, mpiJob, w.client, w.manageJobsWithoutQueueName, w.managedJobsNamespaceSelector); err != nil {
		return err
	}

	jobframework.ApplyDefaultForManagedBy(mpiJob, w.queues, w.cache, log)

	return nil
}

// +kubebuilder:webhook:path=/validate-kubeflow-org-v2beta1-mpijob,mutating=false,failurePolicy=fail,sideEffects=None,groups=kubeflow.org,resources=mpijobs,verbs=create;update,versions=v2beta1,name=vmpijob.kb.io,admissionReviewVersions=v1

var _ admission.CustomValidator = &MpiJobWebhook{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *MpiJobWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	mpiJob := fromObject(obj)
	log := ctrl.LoggerFrom(ctx).WithName("mpijob-webhook")
	log.Info("Validating create")
	validationErrs, err := w.validateCommon(mpiJob)
	if err != nil {
		return nil, err
	}
	sort.Slice(validationErrs, func(i, j int) bool {
		return validationErrs[i].Field < validationErrs[j].Field
	})
	return nil, validationErrs.ToAggregate()
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *MpiJobWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldMpiJob := fromObject(oldObj)
	newMpiJob := fromObject(newObj)
	log := ctrl.LoggerFrom(ctx).WithName("mpijob-webhook")
	log.Info("Validating update")
	allErrs := jobframework.ValidateJobOnUpdate(oldMpiJob, newMpiJob, w.queues.DefaultLocalQueueExist)
	validationErrs, err := w.validateCommon(newMpiJob)
	if err != nil {
		return nil, err
	}
	allErrs = append(allErrs, validationErrs...)
	sort.Slice(validationErrs, func(i, j int) bool {
		return validationErrs[i].Field < validationErrs[j].Field
	})
	return nil, allErrs.ToAggregate()
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type
func (w *MpiJobWebhook) ValidateDelete(context.Context, runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func (w *MpiJobWebhook) validateCommon(mpiJob *MPIJob) (field.ErrorList, error) {
	var allErrs field.ErrorList
	allErrs = jobframework.ValidateJobOnCreate(mpiJob)
	if features.Enabled(features.TopologyAwareScheduling) {
		validationErrs, err := w.validateTopologyRequest(mpiJob)
		if err != nil {
			return nil, err
		}
		allErrs = append(allErrs, validationErrs...)
	}
	return allErrs, nil
}

func (w *MpiJobWebhook) validateTopologyRequest(mpiJob *MPIJob) (field.ErrorList, error) {
	var allErrs field.ErrorList

	podSets, podSetsErr := mpiJob.PodSets()

	for replicaType, replicaSpec := range mpiJob.Spec.MPIReplicaSpecs {
		replicaMetaPath := mpiReplicaSpecsPath.Key(string(replicaType)).Child("template", "metadata")
		allErrs = append(allErrs, jobframework.ValidateTASPodSetRequest(replicaMetaPath, &replicaSpec.Template.ObjectMeta)...)

		if podSetsErr != nil {
			continue
		}

		podSet := podset.FindPodSetByName(podSets, v1beta1.NewPodSetReference(string(replicaType)))
		allErrs = append(allErrs, jobframework.ValidateSliceSizeAnnotationUpperBound(replicaMetaPath, &replicaSpec.Template.ObjectMeta, podSet)...)
	}

	if len(allErrs) > 0 {
		return allErrs, nil
	}

	return nil, podSetsErr
}

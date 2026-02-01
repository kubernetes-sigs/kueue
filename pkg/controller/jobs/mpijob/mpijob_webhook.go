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
	"cmp"
	"context"
	"slices"

	"github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/kubeversion"
	"sigs.k8s.io/kueue/pkg/util/podset"
	"sigs.k8s.io/kueue/pkg/util/webhook"
)

var (
	mpiReplicaSpecsPath         = field.NewPath("spec", "mpiReplicaSpecs")
	launcherAnnotationsPath     = mpiReplicaSpecsPath.Key(string(v2beta1.MPIReplicaTypeLauncher)).Child("template", "metadata", "annotations")
	workerAnnotationsPath       = mpiReplicaSpecsPath.Key(string(v2beta1.MPIReplicaTypeWorker)).Child("template", "metadata", "annotations")
	podSetAnnotationsPathByName = map[kueue.PodSetReference]*field.Path{
		kueue.NewPodSetReference(string(v2beta1.MPIReplicaTypeLauncher)): launcherAnnotationsPath,
		kueue.NewPodSetReference(string(v2beta1.MPIReplicaTypeWorker)):   workerAnnotationsPath,
	}
)

type MpiJobWebhook struct {
	client                       client.Client
	manageJobsWithoutQueueName   bool
	managedJobsNamespaceSelector labels.Selector
	kubeServerVersion            *kubeversion.ServerVersionFetcher
	queues                       *qcache.Manager
	cache                        *schdcache.Cache
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
	if options.NoopWebhook {
		return webhook.SetupNoopWebhook(mgr, obj)
	}
	return ctrl.NewWebhookManagedBy(mgr, obj).
		WithDefaulter(wh).
		WithValidator(wh).
		WithLogConstructor(jobframework.WebhookLogConstructor(fromObject(obj).GVK(), options.RoleTracker)).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-kubeflow-org-v2beta1-mpijob,mutating=true,failurePolicy=fail,sideEffects=None,groups=kubeflow.org,resources=mpijobs,verbs=create,versions=v2beta1,name=mmpijob.kb.io,admissionReviewVersions=v1

var _ admission.Defaulter[*v2beta1.MPIJob] = &MpiJobWebhook{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type
func (w *MpiJobWebhook) Default(ctx context.Context, obj *v2beta1.MPIJob) error {
	mpiJob := fromObject(obj)
	log := ctrl.LoggerFrom(ctx).WithName("mpijob-webhook")
	log.V(5).Info("Applying defaults")

	jobframework.ApplyDefaultLocalQueue(mpiJob.Object(), w.queues.DefaultLocalQueueExist)
	if err := jobframework.ApplyDefaultForSuspend(ctx, mpiJob, w.client, w.manageJobsWithoutQueueName, w.managedJobsNamespaceSelector); err != nil {
		return err
	}

	jobframework.ApplyDefaultForManagedBy(mpiJob, w.queues, w.cache, log)

	if features.Enabled(features.TopologyAwareScheduling) {
		if replicaSpecs := mpiJob.Spec.MPIReplicaSpecs; ptr.Deref(mpiJob.Spec.RunLauncherAsWorker, false) &&
			len(replicaSpecs) == 2 && replicaSpecs[v2beta1.MPIReplicaTypeWorker] != nil {
			// The offset is handled as PodSet group scheduling mechanism separately in topology-unGater
			// when the MPIJob constructs PodSet group across Launcher and Worker.
			if _, isPodSetGroup := replicaSpecs[v2beta1.MPIReplicaTypeLauncher].Template.Annotations[kueue.PodSetGroupName]; isPodSetGroup {
				return nil
			}

			if mpiJob.Spec.MPIReplicaSpecs[v2beta1.MPIReplicaTypeWorker].Template.Annotations == nil {
				mpiJob.Spec.MPIReplicaSpecs[v2beta1.MPIReplicaTypeWorker].Template.Annotations = make(map[string]string)
			}
			mpiJob.Spec.MPIReplicaSpecs[v2beta1.MPIReplicaTypeWorker].Template.Annotations[kueue.PodIndexOffsetAnnotation] = "1"
		}
	}

	return nil
}

// +kubebuilder:webhook:path=/validate-kubeflow-org-v2beta1-mpijob,mutating=false,failurePolicy=fail,sideEffects=None,groups=kubeflow.org,resources=mpijobs,verbs=create;update,versions=v2beta1,name=vmpijob.kb.io,admissionReviewVersions=v1

var _ admission.Validator[*v2beta1.MPIJob] = &MpiJobWebhook{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (w *MpiJobWebhook) ValidateCreate(ctx context.Context, obj *v2beta1.MPIJob) (admission.Warnings, error) {
	mpiJob := fromObject(obj)
	log := ctrl.LoggerFrom(ctx).WithName("mpijob-webhook")
	log.Info("Validating create")
	validationErrs, err := w.validateCommon(ctx, mpiJob)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(validationErrs, func(a, b *field.Error) int {
		return cmp.Compare(a.Field, b.Field)
	})
	return nil, validationErrs.ToAggregate()
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *MpiJobWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj *v2beta1.MPIJob) (admission.Warnings, error) {
	oldMpiJob := fromObject(oldObj)
	newMpiJob := fromObject(newObj)
	log := ctrl.LoggerFrom(ctx).WithName("mpijob-webhook")
	log.Info("Validating update")
	allErrs := jobframework.ValidateJobOnUpdate(oldMpiJob, newMpiJob, w.queues.DefaultLocalQueueExist)
	validationErrs, err := w.validateCommon(ctx, newMpiJob)
	if err != nil {
		return nil, err
	}
	allErrs = append(allErrs, validationErrs...)
	slices.SortFunc(validationErrs, func(a, b *field.Error) int {
		return cmp.Compare(a.Field, b.Field)
	})
	return nil, allErrs.ToAggregate()
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (w *MpiJobWebhook) ValidateDelete(context.Context, *v2beta1.MPIJob) (admission.Warnings, error) {
	return nil, nil
}

func (w *MpiJobWebhook) validateCommon(ctx context.Context, mpiJob *MPIJob) (field.ErrorList, error) {
	var allErrs field.ErrorList
	allErrs = jobframework.ValidateJobOnCreate(mpiJob)
	if features.Enabled(features.TopologyAwareScheduling) {
		validationErrs, err := w.validateTopologyRequest(ctx, mpiJob)
		if err != nil {
			return nil, err
		}
		allErrs = append(allErrs, validationErrs...)
	}
	return allErrs, nil
}

func (w *MpiJobWebhook) validateTopologyRequest(ctx context.Context, mpiJob *MPIJob) (field.ErrorList, error) {
	var allErrs field.ErrorList

	podSets, podSetsErr := jobframework.JobPodSets(ctx, mpiJob)

	if podSetsErr == nil {
		allErrs = append(allErrs, jobframework.ValidatePodSetGroupingTopology(podSets, podSetAnnotationsPathByName)...)
	}

	for replicaType, replicaSpec := range mpiJob.Spec.MPIReplicaSpecs {
		replicaMetaPath := mpiReplicaSpecsPath.Key(string(replicaType)).Child("template", "metadata")
		allErrs = append(allErrs, jobframework.ValidateTASPodSetRequest(replicaMetaPath, &replicaSpec.Template.ObjectMeta)...)

		if podSetsErr != nil {
			continue
		}

		podSet := podset.FindPodSetByName(podSets, kueue.NewPodSetReference(string(replicaType)))
		allErrs = append(allErrs, jobframework.ValidateSliceSizeAnnotationUpperBound(replicaMetaPath, &replicaSpec.Template.ObjectMeta, podSet)...)
	}

	if len(allErrs) > 0 {
		return allErrs, nil
	}

	return nil, podSetsErr
}

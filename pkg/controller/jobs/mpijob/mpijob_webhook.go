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
	"fmt"
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
	"sigs.k8s.io/kueue/pkg/util/webhook"
)

var (
	mpiReplicaSpecsPath      = field.NewPath("spec", "mpiReplicaSpecs")
	launcherMetadataPath     = mpiReplicaSpecsPath.Key(string(v2beta1.MPIReplicaTypeLauncher)).Child("template", "metadata")
	workerMetadataPath       = mpiReplicaSpecsPath.Key(string(v2beta1.MPIReplicaTypeWorker)).Child("template", "metadata")
	podSetMetadataPathByName = map[kueue.PodSetReference]*field.Path{
		kueue.NewPodSetReference(string(v2beta1.MPIReplicaTypeLauncher)): launcherMetadataPath,
		kueue.NewPodSetReference(string(v2beta1.MPIReplicaTypeWorker)):   workerMetadataPath,
	}
	podSetAnnotationsPathByName = map[kueue.PodSetReference]*field.Path{
		kueue.NewPodSetReference(string(v2beta1.MPIReplicaTypeLauncher)): launcherMetadataPath.Child("annotations"),
		kueue.NewPodSetReference(string(v2beta1.MPIReplicaTypeWorker)):   workerMetadataPath.Child("annotations"),
	}
	workerOffsetAnnotationPath = workerMetadataPath.Child("annotations").Key(kueue.PodIndexOffsetAnnotation)
)

type MpiJobWebhook struct {
	integrationManager           *jobframework.IntegrationManager
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
		integrationManager:           options.IntegrationManager,
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

	if err := w.integrationManager.ApplyDefaultLocalQueue(ctx, w.client, mpiJob.Object(), w.queues.DefaultLocalQueueExist, w.managedJobsNamespaceSelector); err != nil {
		return err
	}
	w.integrationManager.ApplyDefaultWorkloadPriorityClass(ctx, w.client, mpiJob.Object())
	if err := w.integrationManager.ApplyDefaultForSuspend(ctx, mpiJob, w.client, w.manageJobsWithoutQueueName, w.managedJobsNamespaceSelector); err != nil {
		return err
	}

	jobframework.ApplyDefaultForManagedBy(mpiJob, w.queues, w.cache, log)

	if features.Enabled(features.TopologyAwareScheduling) {
		if expected, managed := expectedWorkerPodIndexOffset(mpiJob); managed && expected != "" {
			workerSpec := mpiJob.Spec.MPIReplicaSpecs[v2beta1.MPIReplicaTypeWorker]
			if workerSpec.Template.Annotations == nil {
				workerSpec.Template.Annotations = make(map[string]string)
			}
			workerSpec.Template.Annotations[kueue.PodIndexOffsetAnnotation] = expected
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

	if features.Enabled(features.TopologyAwareScheduling) {
		got := workerPodIndexOffset(newMpiJob)
		if expected, managed := expectedWorkerPodIndexOffset(newMpiJob); managed {
			if got != expected {
				allErrs = append(allErrs, field.Invalid(workerOffsetAnnotationPath, got,
					fmt.Sprintf("must be %q, the value the defaulting webhook would set", expected)))
			}
		}
	}
	slices.SortFunc(allErrs, func(a, b *field.Error) int {
		return cmp.Compare(a.Field, b.Field)
	})
	return nil, allErrs.ToAggregate()
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (w *MpiJobWebhook) ValidateDelete(context.Context, *v2beta1.MPIJob) (admission.Warnings, error) {
	return nil, nil
}

func workerPodIndexOffset(mpiJob *MPIJob) string {
	worker := mpiJob.Spec.MPIReplicaSpecs[v2beta1.MPIReplicaTypeWorker]
	if worker == nil {
		return ""
	}
	return worker.Template.Annotations[kueue.PodIndexOffsetAnnotation]
}

func expectedWorkerPodIndexOffset(mpiJob *MPIJob) (expected string, managed bool) {
	if !ptr.Deref(mpiJob.Spec.RunLauncherAsWorker, false) {
		return "", false
	}
	replicaSpecs := mpiJob.Spec.MPIReplicaSpecs
	launcherSpec, workerSpec := replicaSpecs[v2beta1.MPIReplicaTypeLauncher], replicaSpecs[v2beta1.MPIReplicaTypeWorker]
	if launcherSpec == nil || workerSpec == nil {
		return "", false
	}
	// A PodSet group manages its own offset; see topology-ungater.
	if _, isPodSetGroup := launcherSpec.Template.Annotations[kueue.PodSetGroupName]; isPodSetGroup {
		return "", true
	}
	return "1", true
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

	podSets, podSetsErr := jobframework.JobPodSets(ctx, mpiJob, nil)
	if podSetsErr == nil {
		for _, p := range podSets {
			replicaMetaPath := podSetMetadataPathByName[p.Name]
			allErrs = append(allErrs, jobframework.ValidateTASPodSetRequest(replicaMetaPath, &p.Template.ObjectMeta)...)
			allErrs = append(allErrs, jobframework.ValidateSliceSizeAnnotationUpperBound(replicaMetaPath, &p.Template.ObjectMeta, &p)...)
		}
		allErrs = append(allErrs, jobframework.ValidatePodSetGroupingTopology(podSets, podSetAnnotationsPathByName)...)
	}

	if len(allErrs) > 0 {
		return allErrs, nil
	}

	return nil, podSetsErr
}

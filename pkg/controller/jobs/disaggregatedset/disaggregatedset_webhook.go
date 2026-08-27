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

package disaggregatedset

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	disaggregatedsetv1 "sigs.k8s.io/lws/api/disaggregatedset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/podset"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	"sigs.k8s.io/kueue/pkg/util/webhook"
)

type Webhook struct {
	integrationManager           *jobframework.IntegrationManager
	client                       client.Client
	manageJobsWithoutQueueName   bool
	managedJobsNamespaceSelector labels.Selector
	queues                       *qcache.Manager
}

func SetupWebhook(mgr ctrl.Manager, opts ...jobframework.Option) error {
	options := jobframework.ProcessOptions(opts...)
	wh := &Webhook{
		integrationManager:           options.IntegrationManager,
		client:                       mgr.GetClient(),
		manageJobsWithoutQueueName:   options.ManageJobsWithoutQueueName,
		managedJobsNamespaceSelector: options.ManagedJobsNamespaceSelector,
		queues:                       options.Queues,
	}
	obj := &disaggregatedsetv1.DisaggregatedSet{}
	if options.NoopWebhook {
		return webhook.SetupNoopWebhook(mgr, obj)
	}
	return ctrl.NewWebhookManagedBy(mgr, obj).
		WithDefaulter(wh).
		WithValidator(wh).
		WithLogConstructor(roletracker.WebhookLogConstructor(options.RoleTracker)).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-disaggregatedset-x-k8s-io-v1-disaggregatedset,mutating=true,failurePolicy=fail,sideEffects=None,groups="disaggregatedset.x-k8s.io",resources=disaggregatedsets,verbs=create;update,versions=v1,name=mdisaggregatedset.kb.io,admissionReviewVersions=v1

var _ admission.Defaulter[*disaggregatedsetv1.DisaggregatedSet] = &Webhook{}

func (wh *Webhook) Default(ctx context.Context, obj *disaggregatedsetv1.DisaggregatedSet) error {
	ds := fromObject(obj)
	log := ctrl.LoggerFrom(ctx).WithName("disaggregatedset-webhook")
	log.V(5).Info("Applying defaults")

	if err := wh.integrationManager.ApplyDefaultLocalQueue(ctx, wh.client, obj, wh.queues.DefaultLocalQueueExist, wh.managedJobsNamespaceSelector); err != nil {
		return err
	}
	wh.integrationManager.ApplyDefaultWorkloadPriorityClass(ctx, wh.client, obj)
	suspend, err := wh.integrationManager.WorkloadShouldBeSuspended(
		ctx,
		ds.Object(),
		wh.client,
		wh.manageJobsWithoutQueueName,
		wh.managedJobsNamespaceSelector,
		jobframework.WithDeletingObjectTolerance(true),
	)
	if err != nil {
		return err
	}
	if suspend {
		for i := range obj.Spec.Roles {
			role := &obj.Spec.Roles[i]
			hasLeader := role.Spec.LeaderWorkerTemplate.LeaderTemplate != nil
			if hasLeader {
				wh.podTemplateSpecDefault(ds, role.Spec.LeaderWorkerTemplate.LeaderTemplate, role.Name, leaderPodSetSuffix, hasLeader)
			}
			wh.podTemplateSpecDefault(ds, &role.Spec.LeaderWorkerTemplate.WorkerTemplate, role.Name, workerPodSetSuffix, hasLeader)
		}
	}

	return nil
}

func (wh *Webhook) podTemplateSpecDefault(
	ds *DisaggregatedSet, podTemplateSpec *corev1.PodTemplateSpec, roleName, podSetSuffix string, hasLeaderTemplate bool,
) {
	if podTemplateSpec.Labels == nil {
		podTemplateSpec.Labels = make(map[string]string, 1)
	}
	if queueName := jobframework.QueueNameForObject(ds.Object()); queueName != "" {
		podTemplateSpec.Labels[constants.QueueLabel] = string(queueName)
	}
	if priorityClass := jobframework.WorkloadPriorityClassName(ds.Object()); priorityClass != "" {
		podTemplateSpec.Labels[constants.WorkloadPriorityClassLabel] = priorityClass
	}

	if podTemplateSpec.Annotations == nil {
		podTemplateSpec.Annotations = make(map[string]string, 2)
	}
	podTemplateSpec.Annotations[podconstants.SuspendedByParentAnnotation] = FrameworkName
	podTemplateSpec.Annotations[podconstants.GroupServingAnnotationKey] = podconstants.GroupServingAnnotationValue

	if features.Enabled(features.TopologyAwareScheduling) && podSetSuffix == workerPodSetSuffix && hasLeaderTemplate {
		if _, isPodSetGroup := podTemplateSpec.Annotations[kueue.PodSetGroupName]; !isPodSetGroup {
			podTemplateSpec.Annotations[kueue.PodIndexOffsetAnnotation] = "1"
		}
	}
}

// +kubebuilder:webhook:path=/validate-disaggregatedset-x-k8s-io-v1-disaggregatedset,mutating=false,failurePolicy=fail,sideEffects=None,groups="disaggregatedset.x-k8s.io",resources=disaggregatedsets,verbs=create;update,versions=v1,name=vdisaggregatedset.kb.io,admissionReviewVersions=v1

var _ admission.Validator[*disaggregatedsetv1.DisaggregatedSet] = &Webhook{}

var (
	labelsPath         = field.NewPath("metadata", "labels")
	queueNameLabelPath = labelsPath.Key(constants.QueueLabel)
	specPath           = field.NewPath("spec")
	rolesPath          = specPath.Child("roles")
)

func (wh *Webhook) ValidateCreate(ctx context.Context, obj *disaggregatedsetv1.DisaggregatedSet) (warnings admission.Warnings, err error) {
	ds := fromObject(obj)

	log := ctrl.LoggerFrom(ctx).WithName("disaggregatedset-webhook")
	log.V(5).Info("Validating create")

	allErrs, err := validateCreate(ds)
	if err != nil {
		return nil, err
	}

	return nil, allErrs.ToAggregate()
}

func (wh *Webhook) ValidateUpdate(ctx context.Context, oldObj, newObj *disaggregatedsetv1.DisaggregatedSet) (warnings admission.Warnings, err error) {
	oldDS := fromObject(oldObj)
	newDS := fromObject(newObj)

	log := ctrl.LoggerFrom(ctx).WithName("disaggregatedset-webhook")
	log.V(5).Info("Validating update")

	allErrs, err := validateCreate(newDS)
	if err != nil {
		return nil, err
	}

	oldQueueName := jobframework.QueueNameForObject(oldDS.Object())
	newQueueName := jobframework.QueueNameForObject(newDS.Object())

	isSuspended := isTotallySuspended(oldDS)

	if !isSuspended || newQueueName == "" {
		allErrs = append(allErrs, apivalidation.ValidateImmutableField(newQueueName, oldQueueName, queueNameLabelPath)...)
	}

	allErrs = append(allErrs, jobframework.ValidateUpdateForWorkloadPriorityClassName(
		isSuspended,
		oldObj,
		newObj,
	)...)

	if features.Enabled(features.AdmissionGatedBy) {
		allErrs = append(allErrs, webhook.ValidateAdmissionGatedByAnnotationOnUpdate(oldDS.Object(), newDS.Object())...)
	}

	suspend, err := wh.integrationManager.WorkloadShouldBeSuspended(
		ctx,
		newDS.Object(),
		wh.client,
		wh.manageJobsWithoutQueueName,
		wh.managedJobsNamespaceSelector,
		jobframework.WithDeletingObjectTolerance(true),
	)
	if err != nil {
		return nil, err
	}
	if suspend {
		allErrs = append(allErrs, validateImmutableRoles(newObj, oldObj)...)
	}

	return warnings, allErrs.ToAggregate()
}

func (wh *Webhook) ValidateDelete(_ context.Context, _ *disaggregatedsetv1.DisaggregatedSet) (warnings admission.Warnings, err error) {
	return nil, nil
}

func GetWorkloadName(uid types.UID, name string) string {
	return jobframework.GetWorkloadNameForOwnerWithGVK(name, uid, gvk)
}

func isTotallySuspended(ds *DisaggregatedSet) bool {
	for _, rs := range ds.Status.RoleStatuses {
		if rs.ReadyReplicas > 0 {
			return false
		}
	}
	return true
}

func validateCreate(ds *DisaggregatedSet) (field.ErrorList, error) {
	var allErrs field.ErrorList
	allErrs = append(allErrs, jobframework.ValidateQueueName(ds.Object())...)

	if features.Enabled(features.AdmissionGatedBy) {
		allErrs = append(allErrs, webhook.ValidateAdmissionGatedByAnnotationOnCreate(ds.Object())...)
	}

	if features.Enabled(features.TopologyAwareScheduling) {
		validationErrs, err := validateTopologyRequests(ds)
		if err != nil {
			return nil, err
		}
		allErrs = append(allErrs, validationErrs...)
	}

	return allErrs, nil
}

func validateTopologyRequests(ds *DisaggregatedSet) (field.ErrorList, error) {
	var allErrs field.ErrorList

	dsv1 := disaggregatedsetv1.DisaggregatedSet(*ds)
	ps, psErr := podSets(&dsv1)

	for i, role := range ds.Spec.Roles {
		rolePath := rolesPath.Index(i)
		lwTemplatePath := rolePath.Child("spec", "leaderWorkerTemplate")

		if role.Spec.LeaderWorkerTemplate.LeaderTemplate != nil {
			leaderMetaPath := lwTemplatePath.Child("leaderTemplate", "metadata")
			allErrs = append(allErrs, jobframework.ValidateTASPodSetRequest(leaderMetaPath, &role.Spec.LeaderWorkerTemplate.LeaderTemplate.ObjectMeta)...)

			if psErr == nil {
				leaderPSName := kueue.PodSetReference(fmt.Sprintf("%s-%s", role.Name, leaderPodSetSuffix))
				leaderPS := podset.FindPodSetByName(ps, leaderPSName)
				allErrs = append(allErrs, jobframework.ValidateSliceSizeAnnotationUpperBound(leaderMetaPath,
					&role.Spec.LeaderWorkerTemplate.LeaderTemplate.ObjectMeta, leaderPS)...)
			}
		}

		workerMetaPath := lwTemplatePath.Child("workerTemplate", "metadata")
		allErrs = append(allErrs, jobframework.ValidateTASPodSetRequest(workerMetaPath, &role.Spec.LeaderWorkerTemplate.WorkerTemplate.ObjectMeta)...)

		if psErr == nil {
			var workerPSName kueue.PodSetReference
			if role.Spec.LeaderWorkerTemplate.LeaderTemplate != nil {
				workerPSName = kueue.PodSetReference(fmt.Sprintf("%s-%s", role.Name, workerPodSetSuffix))
			} else {
				workerPSName = kueue.PodSetReference(fmt.Sprintf("%s-%s", role.Name, mainPodSetSuffix))
			}
			workerPS := podset.FindPodSetByName(ps, workerPSName)
			allErrs = append(allErrs, jobframework.ValidateSliceSizeAnnotationUpperBound(workerMetaPath,
				&role.Spec.LeaderWorkerTemplate.WorkerTemplate.ObjectMeta, workerPS)...)
		}
	}

	if psErr == nil {
		podSetAnnotations := make(map[kueue.PodSetReference]*field.Path, len(ps))
		for i, role := range ds.Spec.Roles {
			rolePath := rolesPath.Index(i)
			lwTemplatePath := rolePath.Child("spec", "leaderWorkerTemplate")
			workerAnnotPath := lwTemplatePath.Child("workerTemplate", "metadata", "annotations")

			if role.Spec.LeaderWorkerTemplate.LeaderTemplate != nil {
				leaderAnnotPath := lwTemplatePath.Child("leaderTemplate", "metadata", "annotations")
				podSetAnnotations[kueue.PodSetReference(fmt.Sprintf("%s-%s", role.Name, leaderPodSetSuffix))] = leaderAnnotPath
				podSetAnnotations[kueue.PodSetReference(fmt.Sprintf("%s-%s", role.Name, workerPodSetSuffix))] = workerAnnotPath
			} else {
				podSetAnnotations[kueue.PodSetReference(fmt.Sprintf("%s-%s", role.Name, mainPodSetSuffix))] = workerAnnotPath
			}
		}
		allErrs = append(allErrs, jobframework.ValidatePodSetGroupingTopology(ps, podSetAnnotations)...)
	}

	if len(allErrs) > 0 {
		return allErrs, nil
	}

	return nil, psErr
}

func validateImmutableRoles(newDS, oldDS *disaggregatedsetv1.DisaggregatedSet) field.ErrorList {
	var allErrs field.ErrorList

	oldRoleMap := make(map[string]*disaggregatedsetv1.DisaggregatedRoleSpec, len(oldDS.Spec.Roles))
	for i := range oldDS.Spec.Roles {
		oldRoleMap[oldDS.Spec.Roles[i].Name] = &oldDS.Spec.Roles[i]
	}

	for i, newRole := range newDS.Spec.Roles {
		oldRole, ok := oldRoleMap[newRole.Name]
		if !ok {
			continue
		}
		rolePath := rolesPath.Index(i)

		if newRole.Spec.LeaderWorkerTemplate.LeaderTemplate != nil && oldRole.Spec.LeaderWorkerTemplate.LeaderTemplate != nil {
			allErrs = append(allErrs, validateImmutablePodTemplateSpec(
				newRole.Spec.LeaderWorkerTemplate.LeaderTemplate,
				oldRole.Spec.LeaderWorkerTemplate.LeaderTemplate,
				rolePath.Child("spec", "leaderWorkerTemplate", "leaderTemplate"),
			)...)
		}
		allErrs = append(allErrs, validateImmutablePodTemplateSpec(
			&newRole.Spec.LeaderWorkerTemplate.WorkerTemplate,
			&oldRole.Spec.LeaderWorkerTemplate.WorkerTemplate,
			rolePath.Child("spec", "leaderWorkerTemplate", "workerTemplate"),
		)...)
	}

	return allErrs
}

func validateImmutablePodTemplateSpec(newPTS, oldPTS *corev1.PodTemplateSpec, fieldPath *field.Path) field.ErrorList {
	var allErrors field.ErrorList
	if newPTS == nil || oldPTS == nil {
		allErrors = append(allErrors, apivalidation.ValidateImmutableField(newPTS, oldPTS, fieldPath)...)
	} else {
		allErrors = append(allErrors, jobframework.ValidateImmutablePodGroupPodSpec(&newPTS.Spec, &oldPTS.Spec, fieldPath.Child("spec"))...)
	}
	return allErrors
}

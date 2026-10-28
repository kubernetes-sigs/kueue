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

package leaderworkerset

import (
	"context"
	"fmt"
	"maps"
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/util/podset"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utiltolerations "sigs.k8s.io/kueue/pkg/util/tolerations"
	"sigs.k8s.io/kueue/pkg/util/webhook"
	"sigs.k8s.io/kueue/pkg/workload"
)

type Webhook struct {
	integrationManager           *jobframework.IntegrationManager
	client                       client.Client
	manageJobsWithoutQueueName   bool
	managedJobsNamespaceSelector labels.Selector
	queues                       *qcache.Manager
	maxTimeoutOnWorkload         *metav1.Duration
}

func SetupWebhook(mgr ctrl.Manager, opts ...jobframework.Option) error {
	options := jobframework.ProcessOptions(opts...)
	wh := &Webhook{
		integrationManager:           options.IntegrationManager,
		client:                       mgr.GetClient(),
		manageJobsWithoutQueueName:   options.ManageJobsWithoutQueueName,
		managedJobsNamespaceSelector: options.ManagedJobsNamespaceSelector,
		queues:                       options.Queues,
		maxTimeoutOnWorkload:         options.MaxTimeoutOnWorkload,
	}
	obj := &leaderworkersetv1.LeaderWorkerSet{}
	if options.NoopWebhook {
		return webhook.SetupNoopWebhook(mgr, obj)
	}
	return ctrl.NewWebhookManagedBy(mgr, obj).
		WithDefaulter(wh).
		WithValidator(wh).
		WithLogConstructor(roletracker.WebhookLogConstructor(options.RoleTracker)).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-leaderworkerset-x-k8s-io-v1-leaderworkerset,mutating=true,failurePolicy=fail,sideEffects=None,groups="leaderworkerset.x-k8s.io",resources=leaderworkersets,verbs=create;update,versions=v1,name=mleaderworkerset.kb.io,admissionReviewVersions=v1

var _ admission.Defaulter[*leaderworkersetv1.LeaderWorkerSet] = &Webhook{}

// canApplyDefaultLocalQueue reports whether Default can set the default LocalQueue
// on an update of oldLWS. ValidateUpdate rejects groupIdentity Hash when an update
// brings a LeaderWorkerSet under Kueue management, so the default LocalQueue is not
// applied to a Hash LeaderWorkerSet without a queue name. A Hash LeaderWorkerSet
// that already has a queue name is handled like any other, so a dropped "default"
// label is restored.
func canApplyDefaultLocalQueue(oldLWS *leaderworkersetv1.LeaderWorkerSet) bool {
	if oldLWS.Spec.GroupIdentity == leaderworkersetv1.GroupIdentityHash && jobframework.QueueNameForObject(oldLWS) == "" {
		return false
	}
	return jobframework.CanApplyDefaultLocalQueue(oldLWS.Status.ReadyReplicas == 0, oldLWS)
}

func (wh *Webhook) Default(ctx context.Context, obj *leaderworkersetv1.LeaderWorkerSet) error {
	lws := fromObject(obj)
	log := ctrl.LoggerFrom(ctx).WithName("leaderworkerset-webhook")
	log.V(5).Info("Applying defaults")

	oldLWSObj, err := webhook.OldObjectFromContext[leaderworkersetv1.LeaderWorkerSet](ctx)
	if err != nil {
		return err
	}
	// ValidateUpdate rejects adding the queue-name and priority class labels
	// to a running LeaderWorkerSet that did not have them.
	if oldLWSObj == nil || canApplyDefaultLocalQueue(oldLWSObj) {
		if err := wh.integrationManager.ApplyDefaultLocalQueue(ctx, wh.client, obj, wh.queues.DefaultLocalQueueExist, wh.managedJobsNamespaceSelector); err != nil {
			return err
		}
	}
	if oldLWSObj == nil || jobframework.CanSetWorkloadPriorityClassName(oldLWSObj.Status.ReadyReplicas == 0, oldLWSObj) {
		if err := wh.integrationManager.ApplyDefaultWorkloadPriorityClass(ctx, wh.client, obj, wh.managedJobsNamespaceSelector); err != nil {
			return err
		}
	}
	suspend, err := wh.integrationManager.WorkloadShouldBeSuspended(
		ctx,
		lws.Object(),
		wh.client,
		wh.manageJobsWithoutQueueName,
		wh.managedJobsNamespaceSelector,
		jobframework.WithDeletingObjectTolerance(true),
	)
	if err != nil {
		return err
	}
	if suspend {
		if lws.Spec.LeaderWorkerTemplate.LeaderTemplate != nil {
			wh.podTemplateSpecDefault(lws, lws.Spec.LeaderWorkerTemplate.LeaderTemplate, leaderPodSetName)
		}
		wh.podTemplateSpecDefault(lws, &lws.Spec.LeaderWorkerTemplate.WorkerTemplate, workerPodSetName)
	}

	return nil
}

func (wh *Webhook) podTemplateSpecDefault(
	lws *LeaderWorkerSet, podTemplateSpec *corev1.PodTemplateSpec, psName string,
) {
	if podTemplateSpec.Labels == nil {
		podTemplateSpec.Labels = make(map[string]string, 1)
	}
	if queueName := jobframework.QueueNameForObject(lws.Object()); queueName != "" {
		podTemplateSpec.Labels[constants.QueueLabel] = string(queueName)
	}
	if priorityClass := jobframework.WorkloadPriorityClassName(lws.Object()); priorityClass != "" {
		podTemplateSpec.Labels[constants.WorkloadPriorityClassLabel] = priorityClass
	}

	if podTemplateSpec.Annotations == nil {
		podTemplateSpec.Annotations = make(map[string]string, 2)
	}
	podTemplateSpec.Annotations[podconstants.SuspendedByParentAnnotation] = FrameworkName
	podTemplateSpec.Annotations[podconstants.GroupServingAnnotationKey] = podconstants.GroupServingAnnotationValue

	if features.Enabled(features.TopologyAwareScheduling) && psName == workerPodSetName && lws.Spec.LeaderWorkerTemplate.LeaderTemplate != nil {
		// The offset is handled as PodSet group scheduling mechanism separately in topology-unGater
		// when the LeaderWorkerSet constructs PodSet group across Leaders and Workers.
		if _, isPodSetGroup := podTemplateSpec.Annotations[kueue.PodSetGroupName]; !isPodSetGroup {
			podTemplateSpec.Annotations[kueue.PodIndexOffsetAnnotation] = "1"
		}
	}
}

// +kubebuilder:webhook:path=/validate-leaderworkerset-x-k8s-io-v1-leaderworkerset,mutating=false,failurePolicy=fail,sideEffects=None,groups="leaderworkerset.x-k8s.io",resources=leaderworkersets,verbs=create;update,versions=v1,name=vleaderworkerset.kb.io,admissionReviewVersions=v1

var _ admission.Validator[*leaderworkersetv1.LeaderWorkerSet] = &Webhook{}

var (
	labelsPath                    = field.NewPath("metadata", "labels")
	queueNameLabelPath            = labelsPath.Key(constants.QueueLabel)
	specPath                      = field.NewPath("spec")
	replicasPath                  = specPath.Child("replicas")
	groupIdentityPath             = specPath.Child("groupIdentity")
	leaderWorkerTemplatePath      = specPath.Child("leaderWorkerTemplate")
	leaderWorkerTemplateSizePath  = leaderWorkerTemplatePath.Child("size")
	leaderTemplatePath            = leaderWorkerTemplatePath.Child("leaderTemplate")
	leaderTemplateMetaPath        = leaderTemplatePath.Child("metadata")
	workerTemplatePath            = leaderWorkerTemplatePath.Child("workerTemplate")
	workerTemplateMetaPath        = workerTemplatePath.Child("metadata")
	workerTemplateAnnotationsPath = workerTemplateMetaPath.Child("annotations")
	podSetAnnotationsPathByName   = map[kueue.PodSetReference]*field.Path{
		"leader": leaderTemplateMetaPath.Child("annotations"),
		"worker": workerTemplateAnnotationsPath,
		"main":   workerTemplateAnnotationsPath,
	}
)

func (wh *Webhook) ValidateCreate(ctx context.Context, obj *leaderworkersetv1.LeaderWorkerSet) (warnings admission.Warnings, err error) {
	lws := fromObject(obj)

	log := ctrl.LoggerFrom(ctx).WithName("leaderworkerset-webhook")
	log.V(5).Info("Validating create")

	validationErrs, err := validateCreate(lws)
	if err != nil {
		return nil, err
	}
	validationErrs = append(validationErrs, jobframework.ValidateWaitForPodsReadyAnnotation(lws.Object(), wh.maxTimeoutOnWorkload)...)

	groupIdentityErrs, err := wh.validateGroupIdentityOnCreate(ctx, lws)
	if err != nil {
		return nil, err
	}
	validationErrs = append(validationErrs, groupIdentityErrs...)

	return nil, validationErrs.ToAggregate()
}

func (wh *Webhook) ValidateUpdate(ctx context.Context, oldObj, newObj *leaderworkersetv1.LeaderWorkerSet) (warnings admission.Warnings, err error) {
	oldLeaderWorkerSet := fromObject(oldObj)
	newLeaderWorkerSet := fromObject(newObj)

	log := ctrl.LoggerFrom(ctx).WithName("leaderworkerset-webhook")
	log.V(5).Info("Validating update")

	allErrs, err := validateCreate(newLeaderWorkerSet)
	if err != nil {
		return nil, err
	}
	allErrs = append(allErrs, jobframework.ValidateWaitForPodsReadyAnnotationOnUpdate(oldLeaderWorkerSet.Object(), newLeaderWorkerSet.Object(), wh.maxTimeoutOnWorkload)...)

	oldQueueName := jobframework.QueueNameForObject(oldLeaderWorkerSet.Object())
	newQueueName := jobframework.QueueNameForObject(newLeaderWorkerSet.Object())

	isSuspended := oldLeaderWorkerSet.Status.ReadyReplicas == 0

	queueErrs, err := jobframework.ValidateQueueNameUpdate(oldQueueName, newQueueName, isSuspended, func() (bool, error) {
		return hasWorkloadWithQuotaReservation(ctx, wh.client, oldObj)
	})
	if err != nil {
		return nil, err
	}
	allErrs = append(allErrs, queueErrs...)

	allErrs = append(allErrs, jobframework.ValidateUpdateForWorkloadPriorityClassName(
		isSuspended,
		oldObj,
		newObj,
	)...)

	if features.Enabled(features.AdmissionGatedBy) {
		allErrs = append(allErrs, webhook.ValidateAdmissionGatedByAnnotationOnUpdate(oldLeaderWorkerSet.Object(), newLeaderWorkerSet.Object())...)
	}

	suspend, err := wh.integrationManager.WorkloadShouldBeSuspended(
		ctx,
		newLeaderWorkerSet.Object(),
		wh.client,
		wh.manageJobsWithoutQueueName,
		wh.managedJobsNamespaceSelector,
		jobframework.WithDeletingObjectTolerance(true),
	)
	if err != nil {
		return nil, err
	}
	if suspend {
		groupIdentityWarns, groupIdentityErrs, err := wh.validateGroupIdentityOnUpdate(ctx, oldLeaderWorkerSet, newLeaderWorkerSet)
		if err != nil {
			return nil, err
		}
		warnings = append(warnings, groupIdentityWarns...)
		allErrs = append(allErrs, groupIdentityErrs...)
		partition := rollingUpdatePartition(newLeaderWorkerSet)
		var in workload.AdjustmentInputs
		if features.Enabled(features.LWSAllowChangingInitContainers) &&
			initContainersChanged(newLeaderWorkerSet, oldLeaderWorkerSet) {
			// The quota of the Workloads includes the defaults of the LimitRanges,
			// so the effective requests of the pods have to include them too.
			if in.LimitRangeSummary, err = workload.ResolveLimitRangeSummary(ctx, wh.client, newLeaderWorkerSet.Namespace); err != nil {
				return nil, err
			}
		}
		allErrs = append(allErrs, validateImmutablePodTemplateSpec(
			newLeaderWorkerSet.Spec.LeaderWorkerTemplate.LeaderTemplate,
			oldLeaderWorkerSet.Spec.LeaderWorkerTemplate.LeaderTemplate,
			partition,
			in,
			leaderTemplatePath,
		)...)
		allErrs = append(allErrs, validateImmutablePodTemplateSpec(
			&newLeaderWorkerSet.Spec.LeaderWorkerTemplate.WorkerTemplate,
			&oldLeaderWorkerSet.Spec.LeaderWorkerTemplate.WorkerTemplate,
			partition,
			in,
			workerTemplatePath,
		)...)
		if features.Enabled(features.LWSImmutableGroupSize) {
			// Immutable while managed: Workload updates never recompute PodSets,
			// so growing Size would ungate extra pods without accounting for quota.
			allErrs = append(allErrs, apivalidation.ValidateImmutableField(
				ptr.Deref(newLeaderWorkerSet.Spec.LeaderWorkerTemplate.Size, defaultLeaderWorkerSetSize),
				ptr.Deref(oldLeaderWorkerSet.Spec.LeaderWorkerTemplate.Size, defaultLeaderWorkerSetSize),
				leaderWorkerTemplateSizePath,
			)...)
		}
	}

	return warnings, allErrs.ToAggregate()
}

func (wh *Webhook) ValidateDelete(_ context.Context, _ *leaderworkersetv1.LeaderWorkerSet) (warnings admission.Warnings, err error) {
	return nil, nil
}

func hasWorkloadWithQuotaReservation(ctx context.Context, c client.Client, lws *leaderworkersetv1.LeaderWorkerSet) (bool, error) {
	wlList := &kueue.WorkloadList{}
	if err := c.List(ctx, wlList, client.InNamespace(lws.Namespace),
		client.MatchingFields{indexer.OwnerReferenceUID: string(lws.UID)},
	); err != nil {
		return false, err
	}
	return slices.ContainsFunc(wlList.Items, func(wl kueue.Workload) bool {
		return workload.HasQuotaReservation(&wl)
	}), nil
}

func GetWorkloadName(uid types.UID, name string, groupIndex string) string {
	// Workload name should be unique by group index.
	return jobframework.GetWorkloadNameForOwnerWithGVK(fmt.Sprintf("%s-%s", name, groupIndex), uid, gvk)
}

func validateCreate(lws *LeaderWorkerSet) (field.ErrorList, error) {
	var allErrs field.ErrorList
	allErrs = append(allErrs, jobframework.ValidateQueueName(lws.Object())...)
	allErrs = append(allErrs, jobframework.ValidateElasticJobAnnotation(lws.Object(), lws.GVK())...)
	allErrs = append(allErrs, validateReplicasField(lws)...)

	if features.Enabled(features.AdmissionGatedBy) {
		allErrs = append(allErrs, webhook.ValidateAdmissionGatedByAnnotationOnCreate(lws.Object())...)
	}

	if features.Enabled(features.TopologyAwareScheduling) {
		validationErrs, err := validateTopologyRequest(lws)
		if err != nil {
			return nil, err
		}
		allErrs = append(allErrs, validationErrs...)
	}
	return allErrs, nil
}

// Kueue does not support groupIdentity Hash. The controller of a Hash leader Pod
// is a ReplicaSet rather than a StatefulSet, so the reconciler ungates it without
// admission. A Hash group index is not an ordinal, so the worker Pods reference a
// Workload that is never created and stay gated.
const (
	groupIdentityHashNotSupportedMsg = "groupIdentity Hash is not supported for LeaderWorkerSets managed by Kueue"
	groupIdentityHashWarning         = groupIdentityHashNotSupportedMsg +
		": leader Pods run without admission, worker Pods stay gated, and the Workloads can reserve quota with no Pods;" +
		" delete the LeaderWorkerSet and create it again with groupIdentity Ordinal"
)

// validateGroupIdentityOnCreate rejects groupIdentity Hash on a LeaderWorkerSet
// created under Kueue management. groupIdentity is checked first, so that other
// LeaderWorkerSets need no lookup of their owner or Namespace.
func (wh *Webhook) validateGroupIdentityOnCreate(ctx context.Context, lws *LeaderWorkerSet) (field.ErrorList, error) {
	if lws.Spec.GroupIdentity != leaderworkersetv1.GroupIdentityHash {
		return nil, nil
	}
	managed, err := wh.integrationManager.WorkloadShouldBeSuspended(
		ctx,
		lws.Object(),
		wh.client,
		wh.manageJobsWithoutQueueName,
		wh.managedJobsNamespaceSelector,
	)
	if err != nil || !managed {
		return nil, err
	}
	return field.ErrorList{field.Forbidden(groupIdentityPath, groupIdentityHashNotSupportedMsg)}, nil
}

// validateGroupIdentityOnUpdate rejects groupIdentity Hash unless oldLWS is already
// managed by Kueue. The caller has checked that newLWS is managed, so this rejects
// only an update that brings the LeaderWorkerSet under Kueue management, e.g. adding
// the queue-name label. groupIdentity is immutable, so rejecting an already managed
// LeaderWorkerSet would block all its updates, e.g. scaling or finalizer removal.
// Such a LeaderWorkerSet gets a warning instead, because its ordinal Workloads can
// reserve quota that no Pod uses.
func (wh *Webhook) validateGroupIdentityOnUpdate(ctx context.Context, oldLWS, newLWS *LeaderWorkerSet) (admission.Warnings, field.ErrorList, error) {
	if newLWS.Spec.GroupIdentity != leaderworkersetv1.GroupIdentityHash {
		return nil, nil, nil
	}
	wasManaged, err := wh.integrationManager.WorkloadShouldBeSuspended(
		ctx,
		oldLWS.Object(),
		wh.client,
		wh.manageJobsWithoutQueueName,
		wh.managedJobsNamespaceSelector,
	)
	if err != nil {
		return nil, nil, err
	}
	if wasManaged {
		return admission.Warnings{groupIdentityHashWarning}, nil, nil
	}
	return nil, field.ErrorList{field.Forbidden(groupIdentityPath, groupIdentityHashNotSupportedMsg)}, nil
}

func validateReplicasField(lws *LeaderWorkerSet) field.ErrorList {
	replicas := ptr.Deref(lws.Spec.Replicas, defaultLeaderWorkerSetReplicas)
	if err := validateLeaderWorkerSetReplicaCount(replicas); err != nil {
		return field.ErrorList{field.Invalid(replicasPath, replicas, err.Error())}
	}
	return nil
}

func validateTopologyRequest(lws *LeaderWorkerSet) (field.ErrorList, error) {
	var allErrs field.ErrorList

	podSets, podSetsErr := podSets(new(leaderworkersetv1.LeaderWorkerSet(*lws)))

	defaultPodSetName := kueue.DefaultPodSetName

	if lws.Spec.LeaderWorkerTemplate.LeaderTemplate != nil {
		defaultPodSetName = workerPodSetName

		allErrs = append(allErrs, jobframework.ValidateTASPodSetRequest(leaderTemplateMetaPath, &lws.Spec.LeaderWorkerTemplate.LeaderTemplate.ObjectMeta)...)

		if podSetsErr == nil {
			leaderPodSet := podset.FindPodSetByName(podSets, leaderPodSetName)
			allErrs = append(allErrs, jobframework.ValidateSliceSizeAnnotationUpperBound(leaderTemplateMetaPath,
				&lws.Spec.LeaderWorkerTemplate.LeaderTemplate.ObjectMeta, leaderPodSet)...)
		}
	}

	allErrs = append(allErrs, jobframework.ValidateTASPodSetRequest(workerTemplateMetaPath, &lws.Spec.LeaderWorkerTemplate.WorkerTemplate.ObjectMeta)...)

	if podSetsErr == nil {
		workerPodSet := podset.FindPodSetByName(podSets, defaultPodSetName)
		allErrs = append(allErrs, jobframework.ValidateSliceSizeAnnotationUpperBound(workerTemplateMetaPath,
			&lws.Spec.LeaderWorkerTemplate.WorkerTemplate.ObjectMeta, workerPodSet)...)
		allErrs = append(allErrs, jobframework.ValidatePodSetGroupingTopology(podSets, podSetAnnotationsPathByName)...)
	}

	if len(allErrs) > 0 {
		return allErrs, nil
	}

	return nil, podSetsErr
}

func validateImmutablePodTemplateSpec(
	newPodTemplateSpec *corev1.PodTemplateSpec,
	oldPodTemplateSpec *corev1.PodTemplateSpec,
	partition int32,
	in workload.AdjustmentInputs,
	fieldPath *field.Path,
) field.ErrorList {
	var allErrors field.ErrorList
	if newPodTemplateSpec == nil || oldPodTemplateSpec == nil {
		allErrors = append(allErrors, apivalidation.ValidateImmutableField(newPodTemplateSpec, oldPodTemplateSpec, fieldPath)...)
	} else {
		newPodSpec, oldPodSpec := newPodTemplateSpec.Spec, oldPodTemplateSpec.Spec
		if features.Enabled(features.AllowAddingPodTemplateTolerations) {
			// Admitted Workloads keep their admission, so only additions are allowed:
			// removing a toleration could stop the new pods from running on the nodes
			// that Kueue assigned.
			allErrors = append(allErrors, validateOnlyAddedTolerations(newPodSpec.Tolerations, oldPodSpec.Tolerations, partition, fieldPath.Child("spec", "tolerations"))...)
			newPodSpec.Tolerations, oldPodSpec.Tolerations = nil, nil
		}
		if features.Enabled(features.LWSAllowChangingInitContainers) {
			// The quota of the Workloads depends only on the effective requests of
			// the pods, so the init containers can change as long as these requests
			// and the ports stay the same.
			allErrors = append(allErrors, validateInitContainersUpdate(&newPodSpec, &oldPodSpec, in, fieldPath.Child("spec", "initContainers"))...)
			newPodSpec.InitContainers, oldPodSpec.InitContainers = nil, nil
		}
		allErrors = append(allErrors, jobframework.ValidateImmutablePodGroupPodSpec(&newPodSpec, &oldPodSpec, fieldPath.Child("spec"))...)
	}
	return allErrors
}

func validateOnlyAddedTolerations(newTolerations, oldTolerations []corev1.Toleration, partition int32, fieldPath *field.Path) field.ErrorList {
	if !utiltolerations.ContainsAll(newTolerations, oldTolerations) {
		return field.ErrorList{field.Forbidden(fieldPath, "existing tolerations cannot be removed or changed while the LeaderWorkerSet is managed by Kueue, only new tolerations can be added")}
	}
	// Kueue copies the added tolerations to the Workloads of all groups, but
	// LeaderWorkerSet keeps the Pods of the groups below the partition on the
	// previous revision. Topology Aware Scheduling would then treat the node
	// taints as tolerated by Pods that do not tolerate them.
	if partition > 0 && !utiltolerations.ContainsAll(oldTolerations, newTolerations) {
		return field.ErrorList{field.Forbidden(fieldPath, "tolerations cannot be added while rolloutStrategy.rollingUpdateConfiguration.partition is greater than 0")}
	}
	return nil
}

func rollingUpdatePartition(lws *LeaderWorkerSet) int32 {
	if lws.Spec.RolloutStrategy.RollingUpdateConfiguration == nil {
		return 0
	}
	return ptr.Deref(lws.Spec.RolloutStrategy.RollingUpdateConfiguration.Partition, 0)
}

// initContainersChanged reports whether the init containers of any pod template changed.
func initContainersChanged(newLWS, oldLWS *LeaderWorkerSet) bool {
	oldTemplates := podTemplates((*leaderworkersetv1.LeaderWorkerSet)(oldLWS))
	for name, newTemplate := range podTemplates((*leaderworkersetv1.LeaderWorkerSet)(newLWS)) {
		if oldTemplate, found := oldTemplates[name]; found && !equality.Semantic.DeepEqual(newTemplate.Spec.InitContainers, oldTemplate.Spec.InitContainers) {
			return true
		}
	}
	return false
}

func validateInitContainersUpdate(newPodSpec, oldPodSpec *corev1.PodSpec, in workload.AdjustmentInputs, fieldPath *field.Path) field.ErrorList {
	if equality.Semantic.DeepEqual(newPodSpec.InitContainers, oldPodSpec.InitContainers) {
		return nil
	}
	var allErrors field.ErrorList
	// Compare against the old app containers, so that only the init containers
	// can change the effective requests. They are computed as for the quota of
	// the Workloads: limits are used as missing requests, then the defaults of
	// the LimitRanges are applied.
	withNewInitContainers := *oldPodSpec
	withNewInitContainers.InitContainers = newPodSpec.InitContainers
	if !equality.Semantic.DeepEqual(effectiveRequests(&withNewInitContainers, in), effectiveRequests(oldPodSpec, in)) {
		allErrors = append(allErrors, field.Forbidden(fieldPath, "init containers cannot change the effective resource requests of the pods while the LeaderWorkerSet is managed by Kueue"))
	}
	// Kueue keeps the ports unchanged: the host ports of sidecar containers limit
	// the nodes that the pods fit on.
	if !maps.Equal(initContainerPorts(newPodSpec), initContainerPorts(oldPodSpec)) {
		allErrors = append(allErrors, field.Forbidden(fieldPath, "init container ports cannot be changed while the LeaderWorkerSet is managed by Kueue"))
	}
	return allErrors
}

// effectiveRequests returns the effective requests of the pods without the zero
// requests, which use no quota, so that adding one, for example `memory: 0`, is
// not a change.
func effectiveRequests(podSpec *corev1.PodSpec, in workload.AdjustmentInputs) corev1.ResourceList {
	requests := resources.PodRequests(new(workload.EffectivePodSpec(podSpec, in)))
	maps.DeleteFunc(requests, func(_ corev1.ResourceName, q resource.Quantity) bool {
		return q.IsZero()
	})
	return requests
}

// initContainerPort is a port of an init container. The scheduler reserves the
// host ports of sidecar containers for the whole lifetime of the pod, but not
// those of the other init containers, so moving a port between them matters.
type initContainerPort struct {
	corev1.ContainerPort
	sidecar bool
}

// initContainerPorts counts the ports of the init containers. Kubernetes checks
// host port conflicts only within each init container, so a sidecar container
// can repeat the host port of another one. Count each port, so that adding such
// a repeat counts as a change.
func initContainerPorts(podSpec *corev1.PodSpec) map[initContainerPort]int {
	ports := make(map[initContainerPort]int)
	for _, c := range podSpec.InitContainers {
		sidecar := ptr.Deref(c.RestartPolicy, "") == corev1.ContainerRestartPolicyAlways
		for _, p := range c.Ports {
			ports[initContainerPort{ContainerPort: p, sidecar: sidecar}]++
		}
	}
	return ports
}

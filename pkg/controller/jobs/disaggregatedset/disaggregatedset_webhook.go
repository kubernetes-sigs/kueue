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
	"maps"
	"slices"

	corev1 "k8s.io/api/core/v1"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	disaggregatedsetv1 "sigs.k8s.io/lws/api/disaggregatedset/v1"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	kueueconstants "sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
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

func (wh *Webhook) Default(ctx context.Context, ds *disaggregatedsetv1.DisaggregatedSet) error {
	log := ctrl.LoggerFrom(ctx).WithName("disaggregatedset-webhook")
	log.V(5).Info("Applying defaults")

	oldDS, err := webhook.OldObjectFromContext[disaggregatedsetv1.DisaggregatedSet](ctx)
	if err != nil {
		return err
	}
	// ValidateUpdate rejects adding the queue name or the priority class to a
	// running DisaggregatedSet, so the defaults apply only while nothing runs.
	if oldDS == nil || noReadyReplicas(oldDS) {
		if err := wh.integrationManager.ApplyDefaultLocalQueue(ctx, wh.client, ds, wh.queues.DefaultLocalQueueExist, wh.managedJobsNamespaceSelector); err != nil {
			return err
		}
	}
	if oldDS == nil || jobframework.CanSetWorkloadPriorityClassName(noReadyReplicas(oldDS), oldDS) {
		if err := wh.integrationManager.ApplyDefaultWorkloadPriorityClass(ctx, wh.client, ds, wh.managedJobsNamespaceSelector); err != nil {
			return err
		}
	}

	managed, err := wh.isManaged(ctx, ds)
	if err != nil {
		return err
	}
	if !managed {
		return nil
	}
	// Pod templates are part of the DisaggregatedSet revision hash, so only
	// constant values are stamped. Anything else would start a rollout when it
	// changes.
	for i := range ds.Spec.Roles {
		lwt := &ds.Spec.Roles[i].Spec.LeaderWorkerTemplate
		if lwt.LeaderTemplate != nil {
			stampPodTemplate(lwt.LeaderTemplate)
		}
		stampPodTemplate(&lwt.WorkerTemplate)
	}
	return nil
}

func stampPodTemplate(template *corev1.PodTemplateSpec) {
	if template.Annotations == nil {
		template.Annotations = make(map[string]string, 2)
	}
	template.Annotations[podconstants.SuspendedByParentAnnotation] = FrameworkName
	template.Annotations[podconstants.GroupServingAnnotationKey] = podconstants.GroupServingAnnotationValue
}

func (wh *Webhook) isManaged(ctx context.Context, ds *disaggregatedsetv1.DisaggregatedSet) (bool, error) {
	return wh.integrationManager.WorkloadShouldBeSuspended(
		ctx,
		ds,
		wh.client,
		wh.manageJobsWithoutQueueName,
		wh.managedJobsNamespaceSelector,
		jobframework.WithDeletingObjectTolerance(true),
	)
}

// noReadyReplicas reports whether no role of the DisaggregatedSet has Ready
// replicas, which is how the queue name and priority rules treat a job as
// suspended.
func noReadyReplicas(ds *disaggregatedsetv1.DisaggregatedSet) bool {
	for _, rs := range ds.Status.RoleStatuses {
		if rs.ReadyReplicas > 0 {
			return false
		}
	}
	return true
}

// +kubebuilder:webhook:path=/validate-disaggregatedset-x-k8s-io-v1-disaggregatedset,mutating=false,failurePolicy=fail,sideEffects=None,groups="disaggregatedset.x-k8s.io",resources=disaggregatedsets,verbs=create;update,versions=v1,name=vdisaggregatedset.kb.io,admissionReviewVersions=v1

var _ admission.Validator[*disaggregatedsetv1.DisaggregatedSet] = &Webhook{}

var (
	specPath                = field.NewPath("spec")
	rolesPath               = specPath.Child("roles")
	placementPolicyTypePath = specPath.Child("placementPolicy", "type")
)

// forbiddenRoleLabels are labels that have no effect on a managed
// DisaggregatedSet, but start a rollout when they change.
var forbiddenRoleLabels = []string{constants.QueueLabel, constants.WorkloadPriorityClassLabel}

// forbiddenRoleAnnotations are annotations that the DisaggregatedSet controller
// copies from role metadata to the child LeaderWorkerSet. The LeaderWorkerSet
// webhook validates them on every LeaderWorkerSet, so a value that it rejects
// would keep the DisaggregatedSet controller from creating or updating the child.
var forbiddenRoleAnnotations = []string{
	kueueconstants.ElasticJobAnnotation,
	kueueconstants.ElasticJobScaleUpStrategyAnnotationKey,
	kueueconstants.AdmissionGatedByAnnotation,
	constants.WaitForPodsReadyAnnotation,
}

func (wh *Webhook) ValidateCreate(ctx context.Context, ds *disaggregatedsetv1.DisaggregatedSet) (admission.Warnings, error) {
	log := ctrl.LoggerFrom(ctx).WithName("disaggregatedset-webhook")
	log.V(5).Info("Validating create")

	managed, err := wh.isManaged(ctx, ds)
	if err != nil || !managed {
		return nil, err
	}
	allErrs, err := validateManaged(ds)
	if err != nil {
		return nil, err
	}
	allErrs = append(allErrs, jobframework.ValidateWaitForPodsReadyAnnotation(ds, wh.maxTimeoutOnWorkload)...)
	return nil, allErrs.ToAggregate()
}

func (wh *Webhook) ValidateUpdate(ctx context.Context, oldDS, newDS *disaggregatedsetv1.DisaggregatedSet) (admission.Warnings, error) {
	log := ctrl.LoggerFrom(ctx).WithName("disaggregatedset-webhook")
	log.V(5).Info("Validating update")

	isSuspended := noReadyReplicas(oldDS)
	allErrs, err := jobframework.ValidateQueueNameUpdate(
		jobframework.QueueNameForObject(oldDS),
		jobframework.QueueNameForObject(newDS),
		isSuspended,
		func() (bool, error) {
			return hasWorkloadWithQuotaReservation(ctx, wh.client, oldDS)
		},
	)
	if err != nil {
		return nil, err
	}

	managed, err := wh.isManaged(ctx, newDS)
	if err != nil {
		return nil, err
	}
	if !managed {
		return nil, allErrs.ToAggregate()
	}

	managedErrs, err := validateManaged(newDS)
	if err != nil {
		return nil, err
	}
	allErrs = append(allErrs, managedErrs...)
	allErrs = append(allErrs, jobframework.ValidateWaitForPodsReadyAnnotationOnUpdate(oldDS, newDS, wh.maxTimeoutOnWorkload)...)
	allErrs = append(allErrs, jobframework.ValidateUpdateForWorkloadPriorityClassName(isSuspended, oldDS, newDS)...)
	if features.Enabled(features.AdmissionGatedBy) {
		allErrs = append(allErrs, webhook.ValidateAdmissionGatedByAnnotationOnUpdate(oldDS, newDS)...)
	}
	allErrs = append(allErrs, validateImmutableRoles(oldDS, newDS)...)
	return nil, allErrs.ToAggregate()
}

func (wh *Webhook) ValidateDelete(context.Context, *disaggregatedsetv1.DisaggregatedSet) (admission.Warnings, error) {
	return nil, nil
}

func hasWorkloadWithQuotaReservation(ctx context.Context, c client.Client, ds *disaggregatedsetv1.DisaggregatedSet) (bool, error) {
	wlList := &kueue.WorkloadList{}
	if err := c.List(ctx, wlList, client.InNamespace(ds.Namespace),
		client.MatchingFields{indexer.OwnerReferenceUID: string(ds.UID)},
	); err != nil {
		return false, err
	}
	return slices.ContainsFunc(wlList.Items, func(wl kueue.Workload) bool {
		return workload.HasQuotaReservation(&wl)
	}), nil
}

// validateManaged runs the checks that apply to a managed DisaggregatedSet on
// both create and update.
func validateManaged(ds *disaggregatedsetv1.DisaggregatedSet) (field.ErrorList, error) {
	var allErrs field.ErrorList
	allErrs = append(allErrs, jobframework.ValidateQueueName(ds)...)
	allErrs = append(allErrs, jobframework.ValidateElasticJobAnnotation(ds, gvk)...)
	if features.Enabled(features.AdmissionGatedBy) {
		allErrs = append(allErrs, webhook.ValidateAdmissionGatedByAnnotationOnCreate(ds)...)
	}
	for i := range ds.Spec.Roles {
		allErrs = append(allErrs, validateRole(&ds.Spec.Roles[i], rolesPath.Index(i))...)
	}

	rolePodSets := slicePodSets(ds)
	allErrs = append(allErrs, validatePodSetNames(rolePodSets)...)
	sizeErrs, err := validateSliceSize(ds, rolePodSets)
	if err != nil {
		return nil, err
	}
	allErrs = append(allErrs, sizeErrs...)

	if features.Enabled(features.TopologyAwareScheduling) {
		if hasTASAnnotation(rolePodSets) && ds.Spec.PlacementPolicy != nil &&
			ds.Spec.PlacementPolicy.Type != "" && ds.Spec.PlacementPolicy.Type != disaggregatedsetv1.PlacementNone {
			allErrs = append(allErrs, field.Forbidden(placementPolicyTypePath,
				fmt.Sprintf("must be %q when a role template has Topology Aware Scheduling annotations", disaggregatedsetv1.PlacementNone)))
		}
		// Building PodSets with too many Pods fails, and validateSliceSize
		// already reports it.
		if len(sizeErrs) == 0 {
			tasErrs, err := validateTopologyRequest(ds, rolePodSets)
			if err != nil {
				return nil, err
			}
			allErrs = append(allErrs, tasErrs...)
		}
	}
	return allErrs, nil
}

// validateRole rejects the DisaggregatedSet features that the integration does
// not support, the Kueue labels that would only start a rollout, and the Kueue
// annotations that the LeaderWorkerSet webhook could reject on a child.
func validateRole(role *disaggregatedsetv1.DisaggregatedRoleSpec, rolePath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	roleSpecPath := rolePath.Child("spec")
	lwtPath := roleSpecPath.Child("leaderWorkerTemplate")

	if role.Scaling != nil && role.Scaling.Mode == disaggregatedsetv1.RoleScalingExternal {
		allErrs = append(allErrs, field.NotSupported(rolePath.Child("scaling", "mode"), role.Scaling.Mode,
			[]disaggregatedsetv1.RoleScalingMode{disaggregatedsetv1.RoleScalingStatic}))
	}
	if len(role.SubRoles) > 0 {
		allErrs = append(allErrs, field.Forbidden(rolePath.Child("subRoles"), "is not supported for a DisaggregatedSet managed by Kueue"))
	}
	if role.Spec.Scheduling != nil {
		allErrs = append(allErrs, field.Forbidden(roleSpecPath.Child("scheduling"), "is not supported for a DisaggregatedSet managed by Kueue"))
	}
	if role.Spec.GroupIdentity == leaderworkersetv1.GroupIdentityHash {
		allErrs = append(allErrs, field.NotSupported(roleSpecPath.Child("groupIdentity"), role.Spec.GroupIdentity,
			[]leaderworkersetv1.GroupIdentityType{leaderworkersetv1.GroupIdentityOrdinal}))
	}
	// LWS counts the Pods that Kueue deletes on eviction toward the group
	// restart budget, so a preempted slice could come back with terminal groups.
	if role.Spec.LeaderWorkerTemplate.MaxGroupRestarts != nil {
		allErrs = append(allErrs, field.Forbidden(lwtPath.Child("maxGroupRestarts"), "is not supported for a DisaggregatedSet managed by Kueue"))
	}
	if role.Spec.LeaderWorkerTemplate.RestartBackoff != nil {
		allErrs = append(allErrs, field.Forbidden(lwtPath.Child("restartBackoff"), "is not supported for a DisaggregatedSet managed by Kueue"))
	}

	maxSurgePath := roleSpecPath.Child("rolloutStrategy", "rollingUpdateConfiguration", "maxSurge")
	if maxSurge, err := roleMaxSurge(role); err != nil {
		allErrs = append(allErrs, field.Invalid(maxSurgePath, role.Spec.RolloutStrategy.RollingUpdateConfiguration.MaxSurge.String(), err.Error()))
	} else if maxSurge > 0 {
		allErrs = append(allErrs, field.Invalid(maxSurgePath, role.Spec.RolloutStrategy.RollingUpdateConfiguration.MaxSurge.String(),
			"must resolve to 0 for a DisaggregatedSet managed by Kueue"))
	}

	allErrs = append(allErrs, validateForbiddenLabels(role.Labels, rolePath.Child("metadata", "labels"))...)
	allErrs = append(allErrs, validateForbiddenKeys(role.Annotations, forbiddenRoleAnnotations, rolePath.Child("metadata", "annotations"),
		"is not supported in role metadata of a DisaggregatedSet managed by Kueue")...)
	if leader := role.Spec.LeaderWorkerTemplate.LeaderTemplate; leader != nil {
		allErrs = append(allErrs, validateForbiddenLabels(leader.Labels, lwtPath.Child("leaderTemplate", "metadata", "labels"))...)
	}
	allErrs = append(allErrs, validateForbiddenLabels(role.Spec.LeaderWorkerTemplate.WorkerTemplate.Labels,
		lwtPath.Child("workerTemplate", "metadata", "labels"))...)
	return allErrs
}

func validateForbiddenLabels(roleLabels map[string]string, labelsPath *field.Path) field.ErrorList {
	return validateForbiddenKeys(roleLabels, forbiddenRoleLabels, labelsPath,
		"must be set on the DisaggregatedSet metadata instead of a role")
}

func validateForbiddenKeys(values map[string]string, forbiddenKeys []string, mapPath *field.Path, detail string) field.ErrorList {
	var allErrs field.ErrorList
	for _, key := range forbiddenKeys {
		if _, found := values[key]; found {
			allErrs = append(allErrs, field.Forbidden(mapPath.Key(key), detail))
		}
	}
	return allErrs
}

// validatePodSetNames checks that a slice Workload can hold the PodSets of
// every role and that their generated names are valid and unique.
func validatePodSetNames(podSets []rolePodSet) field.ErrorList {
	var allErrs field.ErrorList
	if len(podSets) > jobframework.MaxPodSets {
		allErrs = append(allErrs, field.TooMany(rolesPath, len(podSets), jobframework.MaxPodSets))
	}
	seen := make(map[kueue.PodSetReference]*field.Path, len(podSets))
	for _, ps := range podSets {
		if errs := apivalidation.NameIsDNSLabel(string(ps.name), false); len(errs) > 0 {
			allErrs = append(allErrs, field.Invalid(ps.metaPath, ps.name,
				fmt.Sprintf("generated PodSet name is invalid: %v", errs)))
		}
		if otherPath, found := seen[ps.name]; found {
			allErrs = append(allErrs, field.Invalid(ps.metaPath, ps.name,
				fmt.Sprintf("generated PodSet name is also generated by %s", otherPath)))
		} else {
			seen[ps.name] = ps.metaPath
		}
	}
	return allErrs
}

// validateSliceSize checks the limits that keep a slice Workload below the
// object size limit.
func validateSliceSize(ds *disaggregatedsetv1.DisaggregatedSet, podSets []rolePodSet) (field.ErrorList, error) {
	membership, err := sliceMaxMembership(ds)
	if err != nil {
		return field.ErrorList{field.Invalid(rolesPath, field.OmitValueType{}, err.Error())}, nil
	}
	if membership > maxSliceMembership {
		return field.ErrorList{field.Invalid(rolesPath, membership,
			fmt.Sprintf("a slice can have up to %d Pods during a rollout, must be no more than %d", membership, maxSliceMembership))}, nil
	}
	templates, err := templateBytes(podSets)
	if err != nil {
		return nil, err
	}
	if templates > maxTemplateBytes {
		return field.ErrorList{field.Invalid(rolesPath, templates,
			fmt.Sprintf("role templates use %d bytes, must be no more than %d", templates, maxTemplateBytes))}, nil
	}
	// A ClusterQueue with only TAS flavors assigns a topology also to Pods
	// without TAS annotations, and the slice can be admitted to any ClusterQueue.
	withTAS := features.Enabled(features.TopologyAwareScheduling)
	if projected := projectedWorkloadBytes(templates, membership, withTAS); projected > safeWorkloadBytes {
		return field.ErrorList{field.Invalid(rolesPath, projected,
			fmt.Sprintf("the slice Workload can reach %d bytes, must be no more than %d; reduce the Pods per slice or the role templates",
				projected, safeWorkloadBytes))}, nil
	}
	return nil, nil
}

// validateTopologyRequest validates the TAS annotations of every role template
// and the TAS groups of the PodSets that Kueue builds.
func validateTopologyRequest(ds *disaggregatedsetv1.DisaggregatedSet, rolePodSets []rolePodSet) (field.ErrorList, error) {
	var allErrs field.ErrorList
	for _, rps := range rolePodSets {
		allErrs = append(allErrs, jobframework.ValidateTASPodSetRequest(rps.metaPath, &rps.template.ObjectMeta)...)
	}
	for i := range ds.Spec.Roles {
		allErrs = append(allErrs, validatePodSetGroupName(&ds.Spec.Roles[i], rolesPath.Index(i))...)
	}
	if len(allErrs) > 0 {
		return allErrs, nil
	}
	// A DisaggregatedSet scaled to zero has no slice Workloads, and its PodSet
	// counts would fail the checks below.
	if allRolesScaledToZero(ds) {
		return nil, nil
	}
	podSets, err := podSets(ds)
	if err != nil {
		return nil, err
	}
	annotationsPaths := make(map[kueue.PodSetReference]*field.Path, len(rolePodSets))
	for i, rps := range rolePodSets {
		annotationsPaths[rps.name] = rps.metaPath.Child("annotations")
		allErrs = append(allErrs, jobframework.ValidateSliceSizeAnnotationUpperBound(rps.metaPath, &rps.template.ObjectMeta, &podSets[i])...)
	}
	allErrs = append(allErrs, jobframework.ValidatePodSetGroupingTopology(podSets, annotationsPaths)...)
	return allErrs, nil
}

// validatePodSetGroupName allows a TAS PodSet group only in the shape of one
// LeaderWorkerSet group: a leader and its workers. A slice Workload puts every
// group of a role in the same PodSets, so the leader PodSet has one Pod only
// when the role has one replica.
func validatePodSetGroupName(role *disaggregatedsetv1.DisaggregatedRoleSpec, rolePath *field.Path) field.ErrorList {
	lwt := &role.Spec.LeaderWorkerTemplate
	lwtPath := rolePath.Child("spec", "leaderWorkerTemplate")
	workerPath := lwtPath.Child("workerTemplate", "metadata", "annotations").Key(kueue.PodSetGroupName)
	workerGroup, workerFound := lwt.WorkerTemplate.Annotations[kueue.PodSetGroupName]
	if lwt.LeaderTemplate == nil {
		if workerFound {
			return field.ErrorList{field.Forbidden(workerPath, "requires a leader template")}
		}
		return nil
	}
	leaderPath := lwtPath.Child("leaderTemplate", "metadata", "annotations").Key(kueue.PodSetGroupName)
	leaderGroup, leaderFound := lwt.LeaderTemplate.Annotations[kueue.PodSetGroupName]
	switch {
	case !leaderFound && !workerFound:
		return nil
	case leaderFound != workerFound || leaderGroup != workerGroup:
		return field.ErrorList{field.Invalid(leaderPath, leaderGroup, fmt.Sprintf("must be equal to %s", workerPath))}
	case roleSize(role) < 2:
		return field.ErrorList{field.Forbidden(leaderPath, "requires spec.leaderWorkerTemplate.size greater than 1")}
	case roleReplicas(role) > 1:
		return field.ErrorList{field.Forbidden(leaderPath, "requires spec.replicas of at most 1")}
	}
	return nil
}

// validateImmutableRoles checks that an update keeps the shape of every slice
// Workload. Existing Workloads are not updated when the shape changes, so
// replacement Pods would no longer match them.
func validateImmutableRoles(oldDS, newDS *disaggregatedsetv1.DisaggregatedSet) field.ErrorList {
	oldNames := roleNames(oldDS)
	newNames := roleNames(newDS)
	if !slices.Equal(oldNames, newNames) {
		return field.ErrorList{field.Invalid(rolesPath, newNames,
			"roles cannot be added, removed, renamed or reordered while Kueue manages the DisaggregatedSet")}
	}

	var allErrs field.ErrorList
	replicasChanged := false
	for i := range newDS.Spec.Roles {
		oldRole, newRole := &oldDS.Spec.Roles[i], &newDS.Spec.Roles[i]
		roleSpecPath := rolesPath.Index(i).Child("spec")
		lwtPath := roleSpecPath.Child("leaderWorkerTemplate")
		oldLWT, newLWT := &oldRole.Spec.LeaderWorkerTemplate, &newRole.Spec.LeaderWorkerTemplate

		allErrs = append(allErrs, apivalidation.ValidateImmutableField(roleSize(newRole), roleSize(oldRole), lwtPath.Child("size"))...)
		allErrs = append(allErrs, validateImmutablePodTemplateSpec(newLWT.LeaderTemplate, oldLWT.LeaderTemplate, lwtPath.Child("leaderTemplate"))...)
		allErrs = append(allErrs, validateImmutablePodTemplateSpec(&newLWT.WorkerTemplate, &oldLWT.WorkerTemplate, lwtPath.Child("workerTemplate"))...)
		replicasChanged = replicasChanged || roleReplicas(newRole) != roleReplicas(oldRole)
	}
	if replicasChanged && !allRolesScaledToZero(oldDS) && !allRolesScaledToZero(newDS) {
		allErrs = append(allErrs, field.Forbidden(rolesPath,
			"role replicas can only change when every role is scaled to zero, or from zero"))
	}
	return allErrs
}

func roleNames(ds *disaggregatedsetv1.DisaggregatedSet) []string {
	names := make([]string, 0, len(ds.Spec.Roles))
	for i := range ds.Spec.Roles {
		names = append(names, ds.Spec.Roles[i].Name)
	}
	return names
}

// validateImmutablePodTemplateSpec checks that a template keeps its presence,
// its Pod shape and its TAS annotations. Existing slice Workloads never refresh
// their TopologyRequest, so a changed annotation would leave replacement Pods
// on the old placement.
func validateImmutablePodTemplateSpec(newTemplate, oldTemplate *corev1.PodTemplateSpec, templatePath *field.Path) field.ErrorList {
	if newTemplate == nil || oldTemplate == nil {
		return apivalidation.ValidateImmutableField(newTemplate != nil, oldTemplate != nil, templatePath)
	}
	allErrs := jobframework.ValidateImmutablePodGroupPodSpec(&newTemplate.Spec, &oldTemplate.Spec, templatePath.Child("spec"))
	allErrs = append(allErrs, apivalidation.ValidateImmutableField(
		tasAnnotationsOf(newTemplate), tasAnnotationsOf(oldTemplate), templatePath.Child("metadata", "annotations"))...)
	return allErrs
}

func tasAnnotationsOf(template *corev1.PodTemplateSpec) map[string]string {
	result := maps.Clone(template.Annotations)
	maps.DeleteFunc(result, func(k, _ string) bool {
		return !slices.Contains(tasAnnotations, k)
	})
	return result
}

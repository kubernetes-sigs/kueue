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

package pod

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	"sigs.k8s.io/kueue/pkg/constants"
	ctrlconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	"sigs.k8s.io/kueue/pkg/util/webhook"
)

var (
	metaPath                       = field.NewPath("metadata")
	labelsPath                     = metaPath.Child("labels")
	annotationsPath                = metaPath.Child("annotations")
	managedLabelPath               = labelsPath.Key(constants.ManagedByKueueLabelKey)
	groupNameLabelPath             = labelsPath.Key(podconstants.GroupNameLabel)
	groupNameAnnotationPath        = annotationsPath.Key(podconstants.GroupNameAnnotation)
	groupTotalCountAnnotationPath  = annotationsPath.Key(podconstants.GroupTotalCountAnnotation)
	retriableInGroupAnnotationPath = annotationsPath.Key(podconstants.RetriableInGroupAnnotationKey)
	roleHashAnnotationPath         = annotationsPath.Key(podconstants.RoleHashAnnotation)
)

type PodWebhook struct {
	client                       client.Client
	queues                       *qcache.Manager
	manageJobsWithoutQueueName   bool
	managedJobsNamespaceSelector labels.Selector
	namespaceSelector            *metav1.LabelSelector
	podSelector                  *metav1.LabelSelector
}

// SetupWebhook configures the webhook for pods.
func SetupWebhook(mgr ctrl.Manager, opts ...jobframework.Option) error {
	options := jobframework.ProcessOptions(opts...)
	wh := &PodWebhook{
		client:                       mgr.GetClient(),
		queues:                       options.Queues,
		manageJobsWithoutQueueName:   options.ManageJobsWithoutQueueName,
		managedJobsNamespaceSelector: options.ManagedJobsNamespaceSelector,
	}
	obj := &corev1.Pod{}
	if options.NoopWebhook {
		return webhook.SetupNoopWebhook(mgr, obj)
	}
	return ctrl.NewWebhookManagedBy(mgr, obj).
		WithDefaulter(wh).
		WithValidator(wh).
		WithLogConstructor(jobframework.WebhookLogConstructor(FromObject(obj).GVK(), options.RoleTracker)).
		Complete()
}

// +kubebuilder:webhook:path=/mutate--v1-pod,mutating=true,failurePolicy=fail,sideEffects=None,groups="",resources=pods,verbs=create,versions=v1,name=mpod.kb.io,admissionReviewVersions=v1
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch

var _ admission.Defaulter[*corev1.Pod] = &PodWebhook{}

// addRoleHash sets the role-hash annotation used to group pods into PodSets.
//
// When trustExisting is false — the default for plain pods and pod groups Kueue
// builds a Workload for — the hash is always recomputed from the pod spec, so a
// user-supplied value cannot dictate how pods are grouped and therefore the quota
// footprint of the workload. When trustExisting is true, an existing annotation
// is kept: this covers pods managed by a Kueue-aware parent
// (StatefulSet/LeaderWorkerSet/Deployment), whose trusted reconciler sets a
// PodSet-reference name rather than a spec hash, and pods adopting a prebuilt
// Workload, whose role-hash maps onto the PodSet names the user declared there.
func (p *Pod) addRoleHash(trustExisting bool) error {
	if p.pod.Annotations == nil {
		p.pod.Annotations = make(map[string]string)
	}

	var (
		hash string
		err  error
	)
	if trustExisting {
		hash, err = getRoleHash(p.pod)
	} else {
		hash, err = utilpod.GenerateRoleHash(&p.pod.Spec)
	}
	if err != nil {
		return err
	}

	p.pod.Annotations[podconstants.RoleHashAnnotation] = hash
	return nil
}

func (w *PodWebhook) Default(ctx context.Context, obj *corev1.Pod) error {
	pod := FromObject(obj)
	log := ctrl.LoggerFrom(ctx).WithName("pod-webhook")
	log.V(5).Info("Applying defaults")

	_, suspendByParent := pod.pod.GetAnnotations()[podconstants.SuspendedByParentAnnotation]

	suspend := suspendByParent
	if !suspend {
		// Namespace filtering
		ns := corev1.Namespace{}
		err := w.client.Get(ctx, client.ObjectKey{Name: pod.pod.GetNamespace()}, &ns)
		if err != nil {
			return fmt.Errorf("failed to get namespace: %w", err)
		}
		if w.managedJobsNamespaceSelector != nil && !w.managedJobsNamespaceSelector.Matches(labels.Set(ns.GetLabels())) {
			return nil
		}

		// Backwards compatibility: podOptions.podSelector
		if w.podSelector != nil {
			podSelector, err := metav1.LabelSelectorAsSelector(w.podSelector)
			if err != nil {
				return fmt.Errorf("failed to parse pod selector: %w", err)
			}
			if !podSelector.Matches(labels.Set(pod.pod.GetLabels())) {
				return nil
			}
		}

		// Backwards compatibility: podOptions.namespaceSelector
		if w.namespaceSelector != nil {
			nsSelector, err := metav1.LabelSelectorAsSelector(w.namespaceSelector)
			if err != nil {
				return fmt.Errorf("failed to parse namespace selector: %w", err)
			}
			if !nsSelector.Matches(labels.Set(ns.GetLabels())) {
				return nil
			}
		}

		// Do not suspend a Pod whose owner is already managed by Kueue
		ancestorJob, err := jobframework.FindAncestorJobManagedByKueue(ctx, w.client, pod.Object(), w.manageJobsWithoutQueueName)
		if err != nil || ancestorJob != nil {
			return err
		}

		// Local queue defaulting
		if jobframework.QueueNameForObject(pod.Object()) == "" &&
			w.queues.DefaultLocalQueueExist(pod.pod.GetNamespace()) {
			if pod.pod.Labels == nil {
				pod.pod.Labels = make(map[string]string)
			}
			pod.pod.Labels[ctrlconstants.QueueLabel] = string(ctrlconstants.DefaultLocalQueueName)
		}

		jobframework.ApplyDefaultWorkloadPriorityClass(ctx, w.client, pod.Object())

		suspend = jobframework.QueueNameForObject(pod.Object()) != "" || w.manageJobsWithoutQueueName
		if suspend {
			if pod.pod.Labels == nil {
				pod.pod.Labels = make(map[string]string)
			}
			pod.pod.Labels[constants.ManagedByKueueLabelKey] = constants.ManagedByKueueLabelValue
		}
	}

	if suspend {
		if !suspendByParent {
			controllerutil.AddFinalizer(pod.Object(), podconstants.PodFinalizer)
		}

		gate(&pod.pod)

		if features.Enabled(features.TopologyAwareScheduling) {
			if val, ok := pod.pod.Annotations[kueue.PodGroupPodIndexLabelAnnotation]; ok {
				if pod.pod.Labels == nil {
					pod.pod.Labels = make(map[string]string, 1)
				}
				pod.pod.Labels[kueue.PodGroupPodIndexLabel] = pod.pod.Labels[val]
			}
			utilpod.Gate(&pod.pod, kueue.TopologySchedulingGate)
		}
		// Preserve the role-hash for pods whose grouping is owned by a trusted
		// writer: a Kueue-aware parent (suspend-by-parent) or a user-declared
		// prebuilt Workload whose PodSet names the pod maps onto. Otherwise
		// recompute it from the spec so a tenant cannot dictate the grouping.
		trustRoleHash := suspendByParent || jobframework.PrebuiltWorkloadNameFor(&pod.pod) != ""
		if err := pod.addRoleHash(trustRoleHash); err != nil {
			return err
		}
		// copy back changes to the object
		pod.pod.DeepCopyInto(obj)
	}

	return nil
}

// +kubebuilder:webhook:path=/validate--v1-pod,mutating=false,failurePolicy=fail,sideEffects=None,groups="",resources=pods,verbs=create;update,versions=v1,name=vpod.kb.io,admissionReviewVersions=v1

var _ admission.Validator[*corev1.Pod] = &PodWebhook{}

func (w *PodWebhook) ValidateCreate(ctx context.Context, obj *corev1.Pod) (admission.Warnings, error) {
	var warnings admission.Warnings

	pod := FromObject(obj)
	log := ctrl.LoggerFrom(ctx).WithName("pod-webhook")
	log.V(5).Info("Validating create")

	allErrs := jobframework.ValidateJobOnCreate(pod)
	allErrs = append(allErrs, validateCommon(pod)...)
	allErrs = append(allErrs, validateRoleHashOnCreate(pod)...)

	if warn := warningForPodManagedLabel(pod); warn != "" {
		warnings = append(warnings, warn)
	}

	return warnings, allErrs.ToAggregate()
}

func (w *PodWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj *corev1.Pod) (admission.Warnings, error) {
	var warnings admission.Warnings

	oldPod := FromObject(oldObj)
	newPod := FromObject(newObj)
	log := ctrl.LoggerFrom(ctx).WithName("pod-webhook")
	log.V(5).Info("Validating update")

	allErrs := jobframework.ValidateJobOnUpdate(oldPod, newPod, w.queues.DefaultLocalQueueExist)
	allErrs = append(allErrs, validateCommon(newPod)...)
	allErrs = append(allErrs, validateUpdateForRetriableInGroupAnnotation(oldPod, newPod)...)

	if oldGroupName := utilpod.GetPodGroupName(&oldPod.pod); oldGroupName != "" {
		newGroupName := utilpod.GetPodGroupName(&newPod.pod)
		allErrs = append(allErrs, validation.ValidateImmutableField(newGroupName, oldGroupName, getGroupNamePath(&newPod.pod))...)
	}

	allErrs = append(allErrs, validateRoleHashOnUpdate(oldPod, newPod)...)

	if _, suspendByParent := newPod.pod.Annotations[podconstants.SuspendedByParentAnnotation]; !suspendByParent {
		if warn := warningForPodManagedLabel(newPod); warn != "" {
			warnings = append(warnings, warn)
		}
	}

	return warnings, allErrs.ToAggregate()
}

func (w *PodWebhook) ValidateDelete(context.Context, *corev1.Pod) (admission.Warnings, error) {
	return nil, nil
}

func validateCommon(pod *Pod) field.ErrorList {
	allErrs := validateManagedLabel(pod)
	allErrs = append(allErrs, validatePodGroupMetadata(pod)...)
	allErrs = append(allErrs, validateTopologyRequest(pod)...)
	allErrs = append(allErrs, validatePrebuiltWorkloadName(pod)...)
	return allErrs
}

// roleHashExempt reports whether the role-hash annotation checks should be
// skipped for this pod. Exempt pods are those whose grouping is not derived from
// the pod spec by Kueue:
//   - pods managed by a Kueue-aware parent (e.g. StatefulSet, LeaderWorkerSet)
//     carry a PodSet-reference role-hash set by a trusted reconciler;
//   - pods adopting a prebuilt Workload map their role-hash onto the PodSet names
//     the user declared on that Workload;
//   - pods Kueue does not manage are never grouped into PodSets by their role-hash.
func roleHashExempt(pod *Pod) bool {
	if _, suspendByParent := pod.pod.GetAnnotations()[podconstants.SuspendedByParentAnnotation]; suspendByParent {
		return true
	}
	if jobframework.PrebuiltWorkloadNameFor(&pod.pod) != "" {
		return true
	}
	return pod.pod.GetLabels()[constants.ManagedByKueueLabelKey] != constants.ManagedByKueueLabelValue
}

// validateRoleHashOnCreate rejects a role-hash annotation that does not match the
// hash derived from the pod spec. The mutating webhook recomputes the annotation
// on creation, so in the normal flow it already matches; this is a defense-in-depth
// guard against a value that reaches the API server without being recomputed.
func validateRoleHashOnCreate(pod *Pod) field.ErrorList {
	if roleHashExempt(pod) {
		return nil
	}
	roleHash, ok := pod.pod.GetAnnotations()[podconstants.RoleHashAnnotation]
	if !ok {
		return nil
	}
	wantRoleHash, err := utilpod.GenerateRoleHash(&pod.pod.Spec)
	if err != nil {
		return field.ErrorList{field.InternalError(roleHashAnnotationPath, err)}
	}
	if roleHash != wantRoleHash {
		return field.ErrorList{field.Invalid(roleHashAnnotationPath, roleHash,
			fmt.Sprintf("%s is managed by Kueue and must match the pod spec", podconstants.RoleHashAnnotation))}
	}
	return nil
}

// validateRoleHashOnUpdate keeps the Kueue-managed role-hash annotation immutable.
// It is set once by the mutating webhook on creation and determines how pods are
// grouped into PodSets, and therefore the quota footprint of the workload, so a
// tenant must not be able to change it afterwards. Immutability is enforced rather
// than re-deriving the hash from the spec because Kueue itself mutates the pod spec
// at admission (e.g. injecting flavor node selectors) without touching the
// annotation, which would make a spec-derived check reject Kueue's own updates.
func validateRoleHashOnUpdate(oldPod, newPod *Pod) field.ErrorList {
	if roleHashExempt(newPod) {
		return nil
	}
	oldRoleHash := oldPod.pod.GetAnnotations()[podconstants.RoleHashAnnotation]
	newRoleHash := newPod.pod.GetAnnotations()[podconstants.RoleHashAnnotation]
	return validation.ValidateImmutableField(newRoleHash, oldRoleHash, roleHashAnnotationPath)
}

func validateManagedLabel(pod *Pod) field.ErrorList {
	var allErrs field.ErrorList

	if managedLabel, ok := pod.pod.GetLabels()[constants.ManagedByKueueLabelKey]; ok && managedLabel != constants.ManagedByKueueLabelValue {
		return append(allErrs, field.Forbidden(managedLabelPath, fmt.Sprintf("managed label value can only be '%s'", constants.ManagedByKueueLabelValue)))
	}

	return allErrs
}

// warningForPodManagedLabel returns a warning message if the pod has a managed label, and it's parent is managed by kueue
func warningForPodManagedLabel(p *Pod) string {
	managedLabel := p.pod.GetLabels()[constants.ManagedByKueueLabelKey]
	if managedLabel == constants.ManagedByKueueLabelValue && jobframework.IsOwnerManagedByKueueForObject(p.Object()) {
		return fmt.Sprintf("pod owner is managed by kueue, label '%s=%s' might lead to unexpected behaviour",
			constants.ManagedByKueueLabelKey, constants.ManagedByKueueLabelValue)
	}

	return ""
}

func validatePodGroupMetadata(p *Pod) field.ErrorList {
	var allErrs field.ErrorList

	gtc, gtcExists := p.pod.GetAnnotations()[podconstants.GroupTotalCountAnnotation]

	errorDetail := fmt.Sprintf("both the '%s' annotation and the '%s' label should be set", podconstants.GroupTotalCountAnnotation, podconstants.GroupNameLabel)
	groupNameAnnotation := p.pod.Annotations[podconstants.GroupNameAnnotation]
	if features.Enabled(features.WorkloadIdentifierAnnotations) && groupNameAnnotation != "" {
		errorDetail = fmt.Sprintf("both the '%s' annotation and the '%s' annotation should be set", podconstants.GroupTotalCountAnnotation, podconstants.GroupNameAnnotation)
	}

	if groupName := utilpod.GetPodGroupName(&p.pod); groupName == "" {
		if gtcExists {
			return append(allErrs, field.Required(getGroupNamePath(&p.pod), errorDetail))
		}
	} else {
		allErrs = append(allErrs, validatePodGroupName(p.Object())...)

		if !gtcExists {
			return append(allErrs, field.Required(groupTotalCountAnnotationPath, errorDetail))
		}
	}

	if _, err := p.groupTotalCount(); gtcExists && err != nil {
		return append(allErrs, field.Invalid(groupTotalCountAnnotationPath, gtc, err.Error()))
	}

	return allErrs
}

func validateTopologyRequest(pod *Pod) field.ErrorList {
	return jobframework.ValidateTASPodSetRequest(metaPath, &pod.pod.ObjectMeta)
}

func validateUpdateForRetriableInGroupAnnotation(oldPod, newPod *Pod) field.ErrorList {
	if groupName := utilpod.GetPodGroupName(&newPod.pod); groupName != "" && isUnretriablePod(oldPod.pod) && !isUnretriablePod(newPod.pod) {
		return field.ErrorList{
			field.Forbidden(retriableInGroupAnnotationPath, "unretriable pod group can't be converted to retriable"),
		}
	}

	return nil
}

func validatePrebuiltWorkloadName(pod *Pod) field.ErrorList {
	var allErrs field.ErrorList

	groupName := utilpod.GetPodGroupName(&pod.pod)
	prebuiltWorkload := jobframework.PrebuiltWorkloadNameFor(&pod.pod)
	if groupName != "" && prebuiltWorkload != "" && prebuiltWorkload != groupName {
		allErrs = append(allErrs, field.Invalid(jobframework.GetPrebuiltWorkloadPath(&pod.pod), prebuiltWorkload, "prebuilt workload and pod group should be equal"))
	}
	return allErrs
}
func validatePodGroupName(obj client.Object) field.ErrorList {
	groupName := obj.GetAnnotations()[podconstants.GroupNameAnnotation]
	if features.Enabled(features.WorkloadIdentifierAnnotations) && groupName != "" {
		return jobframework.ValidateAnnotationAsCRDName(obj, podconstants.GroupNameAnnotation)
	}
	return jobframework.ValidateLabelAsCRDName(obj, podconstants.GroupNameLabel)
}

func getGroupNamePath(obj client.Object) *field.Path {
	groupNameAnnotation := obj.GetAnnotations()[podconstants.GroupNameAnnotation]
	if features.Enabled(features.WorkloadIdentifierAnnotations) && groupNameAnnotation != "" {
		return groupNameAnnotationPath
	}
	return groupNameLabelPath
}

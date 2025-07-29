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
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/constants"
	ctrlconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobframework/webhook"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/queue"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
)

var (
	metaPath                       = field.NewPath("metadata")
	labelsPath                     = metaPath.Child("labels")
	annotationsPath                = metaPath.Child("annotations")
	managedLabelPath               = labelsPath.Key(constants.ManagedByKueueLabelKey)
	groupNameLabelPath             = labelsPath.Key(podconstants.GroupNameLabel)
	prebuiltWorkloadLabelPath      = labelsPath.Key(ctrlconstants.PrebuiltWorkloadLabel)
	groupTotalCountAnnotationPath  = annotationsPath.Key(podconstants.GroupTotalCountAnnotation)
	retriableInGroupAnnotationPath = annotationsPath.Key(podconstants.RetriableInGroupAnnotationKey)

	errPodOptsTypeAssertion = errors.New("options are not of type PodIntegrationOptions")
)

type PodWebhook struct {
	client                       client.Client
	queues                       *queue.Manager
	manageJobsWithoutQueueName   bool
	managedJobsNamespaceSelector labels.Selector
	namespaceSelector            *metav1.LabelSelector
	podSelector                  *metav1.LabelSelector
}

// SetupWebhook configures the webhook for pods.
func SetupWebhook(mgr ctrl.Manager, opts ...jobframework.Option) error {
	options := jobframework.ProcessOptions(opts...)
	podOpts, err := getPodOptions(options.IntegrationOptions)
	if err != nil {
		return err
	}
	wh := &PodWebhook{
		client:                       mgr.GetClient(),
		queues:                       options.Queues,
		manageJobsWithoutQueueName:   options.ManageJobsWithoutQueueName,
		managedJobsNamespaceSelector: options.ManagedJobsNamespaceSelector,
	}
	if podOpts != nil {
		wh.namespaceSelector = podOpts.NamespaceSelector
		wh.podSelector = podOpts.PodSelector
	}
	obj := &corev1.Pod{}
	return webhook.WebhookManagedBy(mgr).
		For(obj).
		WithMutationHandler(admission.WithCustomDefaulter(mgr.GetScheme(), obj, wh)).
		WithValidator(wh).
		Complete()
}

func getPodOptions(integrationOpts map[string]any) (*configapi.PodIntegrationOptions, error) {
	opts, ok := integrationOpts[corev1.SchemeGroupVersion.WithKind("Pod").String()]
	if !ok {
		return nil, nil
	}
	podOpts, ok := opts.(*configapi.PodIntegrationOptions)
	if !ok {
		return nil, fmt.Errorf("%w, got %T", errPodOptsTypeAssertion, opts)
	}
	return podOpts, nil
}

// +kubebuilder:webhook:path=/mutate--v1-pod,mutating=true,failurePolicy=fail,sideEffects=None,groups="",resources=pods,verbs=create,versions=v1,name=mpod.kb.io,admissionReviewVersions=v1
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch

var _ admission.CustomDefaulter = &PodWebhook{}

// addRoleHash calculates the role hash and adds it to the pod's annotations
func (p *Pod) addRoleHash() error {
	if p.pod.Annotations == nil {
		p.pod.Annotations = make(map[string]string)
	}

	hash, err := getRoleHash(p.pod)
	if err != nil {
		return err
	}

	p.pod.Annotations[podconstants.RoleHashAnnotation] = hash
	return nil
}

func (w *PodWebhook) Default(ctx context.Context, obj runtime.Object) error {
	pod := FromObject(obj)
	log := ctrl.LoggerFrom(ctx).WithName("pod-webhook")
	log.V(5).Info("Applying defaults")

	_, suspend := pod.pod.GetAnnotations()[podconstants.SuspendedByParentAnnotation]
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
		if features.Enabled(features.LocalQueueDefaulting) &&
			jobframework.QueueNameForObject(pod.Object()) == "" &&
			w.queues.DefaultLocalQueueExist(pod.pod.GetNamespace()) {
			if pod.pod.Labels == nil {
				pod.pod.Labels = make(map[string]string)
			}
			pod.pod.Labels[ctrlconstants.QueueLabel] = string(ctrlconstants.DefaultLocalQueueName)
		}

		suspend = jobframework.QueueNameForObject(pod.Object()) != "" || w.manageJobsWithoutQueueName
		if suspend {
			if pod.pod.Labels == nil {
				pod.pod.Labels = make(map[string]string)
			}
			pod.pod.Labels[constants.ManagedByKueueLabelKey] = constants.ManagedByKueueLabelValue
		}
	}

	if suspend {
		controllerutil.AddFinalizer(pod.Object(), podconstants.PodFinalizer)
		gate(&pod.pod)

		if features.Enabled(features.TopologyAwareScheduling) {
			if val, ok := pod.pod.Annotations[kueuealpha.PodGroupPodIndexLabelAnnotation]; ok {
				if pod.pod.Labels == nil {
					pod.pod.Labels = make(map[string]string, 1)
				}
				pod.pod.Labels[kueuealpha.PodGroupPodIndexLabel] = pod.pod.Labels[val]
			}
			utilpod.Gate(&pod.pod, kueuealpha.TopologySchedulingGate)
		}
		if err := pod.addRoleHash(); err != nil {
			return err
		}
		// copy back changes to the object
		pod.pod.DeepCopyInto(obj.(*corev1.Pod))
	}

	return nil
}

// +kubebuilder:webhook:path=/validate--v1-pod,mutating=false,failurePolicy=fail,sideEffects=None,groups="",resources=pods,verbs=create;update,versions=v1,name=vpod.kb.io,admissionReviewVersions=v1

var _ admission.CustomValidator = &PodWebhook{}

func (w *PodWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	var warnings admission.Warnings

	pod := FromObject(obj)
	log := ctrl.LoggerFrom(ctx).WithName("pod-webhook")
	log.V(5).Info("Validating create")

	allErrs := jobframework.ValidateJobOnCreate(pod)
	allErrs = append(allErrs, validateCommon(pod)...)

	if warn := warningForPodManagedLabel(pod); warn != "" {
		warnings = append(warnings, warn)
	}

	return warnings, allErrs.ToAggregate()
}

func (w *PodWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	var warnings admission.Warnings

	oldPod := FromObject(oldObj)
	newPod := FromObject(newObj)
	log := ctrl.LoggerFrom(ctx).WithName("pod-webhook")
	log.V(5).Info("Validating update")

	allErrs := jobframework.ValidateJobOnUpdate(oldPod, newPod, w.queues.DefaultLocalQueueExist)
	allErrs = append(allErrs, validateCommon(newPod)...)
	allErrs = append(allErrs, validateUpdateForRetriableInGroupAnnotation(oldPod, newPod)...)

	if podGroupName(oldPod.pod) != "" {
		allErrs = append(allErrs, validation.ValidateImmutableField(podGroupName(newPod.pod), podGroupName(oldPod.pod), groupNameLabelPath)...)
	}

	if _, suspendByParent := newPod.pod.Annotations[podconstants.SuspendedByParentAnnotation]; !suspendByParent {
		if warn := warningForPodManagedLabel(newPod); warn != "" {
			warnings = append(warnings, warn)
		}
	}

	return warnings, allErrs.ToAggregate()
}

func (w *PodWebhook) ValidateDelete(context.Context, runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func validateCommon(pod *Pod) field.ErrorList {
	allErrs := validateManagedLabel(pod)
	allErrs = append(allErrs, validatePodGroupMetadata(pod)...)
	allErrs = append(allErrs, validateTopologyRequest(pod)...)
	allErrs = append(allErrs, validatePrebuiltWorkloadName(pod)...)
	return allErrs
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

	if podGroupName(p.pod) == "" {
		if gtcExists {
			return append(allErrs, field.Required(
				groupNameLabelPath,
				fmt.Sprintf("both the '%s' annotation and the '%s' label should be set", podconstants.GroupTotalCountAnnotation, podconstants.GroupNameLabel),
			))
		}
	} else {
		allErrs = append(allErrs, jobframework.ValidateLabelAsCRDName(p.Object(), podconstants.GroupNameLabel)...)

		if !gtcExists {
			return append(allErrs, field.Required(
				groupTotalCountAnnotationPath,
				fmt.Sprintf("both the '%s' annotation and the '%s' label should be set", podconstants.GroupTotalCountAnnotation, podconstants.GroupNameLabel),
			))
		}
	}

	if _, err := p.groupTotalCount(); gtcExists && err != nil {
		return append(allErrs, field.Invalid(
			groupTotalCountAnnotationPath,
			gtc,
			err.Error(),
		))
	}

	return allErrs
}

func validateTopologyRequest(pod *Pod) field.ErrorList {
	return jobframework.ValidateTASPodSetRequest(metaPath, &pod.pod.ObjectMeta)
}

func validateUpdateForRetriableInGroupAnnotation(oldPod, newPod *Pod) field.ErrorList {
	if podGroupName(newPod.pod) != "" && isUnretriablePod(oldPod.pod) && !isUnretriablePod(newPod.pod) {
		return field.ErrorList{
			field.Forbidden(retriableInGroupAnnotationPath, "unretriable pod group can't be converted to retriable"),
		}
	}

	return nil
}

func validatePrebuiltWorkloadName(pod *Pod) field.ErrorList {
	var allErrs field.ErrorList
	prebuiltWorkloadName, hasPrebuiltWorkload := jobframework.PrebuiltWorkloadFor(pod)
	groupName := podGroupName(pod.pod)
	if hasPrebuiltWorkload && groupName != "" && prebuiltWorkloadName != groupName {
		allErrs = append(allErrs, field.Invalid(prebuiltWorkloadLabelPath, prebuiltWorkloadLabelPath, "prebuilt workload and pod group should be equal"))
	}
	return allErrs
}

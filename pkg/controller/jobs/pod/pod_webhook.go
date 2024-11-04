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
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobframework/webhook"
	"sigs.k8s.io/kueue/pkg/features"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
)

const (
	ManagedLabelKey              = constants.ManagedByKueueLabel
	ManagedLabelValue            = "true"
	PodFinalizer                 = ManagedLabelKey
	GroupNameLabel               = "kueue.x-k8s.io/pod-group-name"
	GroupTotalCountAnnotation    = "kueue.x-k8s.io/pod-group-total-count"
	GroupFastAdmissionAnnotation = "kueue.x-k8s.io/pod-group-fast-admission"
	RoleHashAnnotation           = "kueue.x-k8s.io/role-hash"
	RetriableInGroupAnnotation   = "kueue.x-k8s.io/retriable-in-group"
)

var (
	metaPath                       = field.NewPath("metadata")
	labelsPath                     = metaPath.Child("labels")
	annotationsPath                = metaPath.Child("annotations")
	managedLabelPath               = labelsPath.Key(ManagedLabelKey)
	groupNameLabelPath             = labelsPath.Key(GroupNameLabel)
	groupTotalCountAnnotationPath  = annotationsPath.Key(GroupTotalCountAnnotation)
	retriableInGroupAnnotationPath = annotationsPath.Key(RetriableInGroupAnnotation)

	errPodOptsTypeAssertion = errors.New("options are not of type PodIntegrationOptions")
)

type PodWebhook struct {
	client                     client.Client
	manageJobsWithoutQueueName bool
	namespaceSelector          *metav1.LabelSelector
	podSelector                *metav1.LabelSelector
}

// SetupWebhook configures the webhook for pods.
func SetupWebhook(mgr ctrl.Manager, opts ...jobframework.Option) error {
	options := jobframework.ProcessOptions(opts...)
	podOpts, err := getPodOptions(options.IntegrationOptions)
	if err != nil {
		return err
	}
	wh := &PodWebhook{
		client:                     mgr.GetClient(),
		manageJobsWithoutQueueName: options.ManageJobsWithoutQueueName,
		namespaceSelector:          podOpts.NamespaceSelector,
		podSelector:                podOpts.PodSelector,
	}
	obj := &corev1.Pod{}
	return webhook.WebhookManagedBy(mgr).
		For(obj).
		WithMutationHandler(webhook.WithLosslessDefaulter(mgr.GetScheme(), obj, wh)).
		WithValidator(wh).
		Complete()
}

func getPodOptions(integrationOpts map[string]any) (*configapi.PodIntegrationOptions, error) {
	opts, ok := integrationOpts[corev1.SchemeGroupVersion.WithKind("Pod").String()]
	if !ok {
		return &configapi.PodIntegrationOptions{}, nil
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

func containersShape(containers []corev1.Container) (result []map[string]interface{}) {
	for _, c := range containers {
		result = append(result, map[string]interface{}{
			"resources": map[string]interface{}{
				"requests": c.Resources.Requests,
			},
			"ports": c.Ports,
		})
	}

	return result
}

// addRoleHash calculates the role hash and adds it to the pod's annotations
func (p *Pod) addRoleHash() error {
	if p.pod.Annotations == nil {
		p.pod.Annotations = make(map[string]string)
	}

	hash, err := getRoleHash(p.pod)
	if err != nil {
		return err
	}

	p.pod.Annotations[RoleHashAnnotation] = hash
	return nil
}

func (w *PodWebhook) Default(ctx context.Context, obj runtime.Object) error {
	pod := FromObject(obj)
	log := ctrl.LoggerFrom(ctx).WithName("pod-webhook").WithValues("pod", klog.KObj(&pod.pod))
	log.V(5).Info("Applying defaults")

	if IsPodOwnerManagedByKueue(pod) {
		log.V(5).Info("Pod owner is managed by kueue, skipping")
		return nil
	}

	// Check for pod label selector match
	podSelector, err := metav1.LabelSelectorAsSelector(w.podSelector)
	if err != nil {
		return fmt.Errorf("failed to parse pod selector: %w", err)
	}
	if !podSelector.Matches(labels.Set(pod.pod.GetLabels())) {
		return nil
	}

	// Get pod namespace and check for namespace label selector match
	ns := corev1.Namespace{}
	err = w.client.Get(ctx, client.ObjectKey{Name: pod.pod.GetNamespace()}, &ns)
	if err != nil {
		return fmt.Errorf("failed to run mutating webhook on pod %s, error while getting namespace: %w",
			pod.pod.GetName(),
			err,
		)
	}
	log.V(5).Info("Found pod namespace", "Namespace.Name", ns.GetName())
	nsSelector, err := metav1.LabelSelectorAsSelector(w.namespaceSelector)
	if err != nil {
		return fmt.Errorf("failed to parse namespace selector: %w", err)
	}
	if !nsSelector.Matches(labels.Set(ns.GetLabels())) {
		return nil
	}

	if jobframework.QueueName(pod) != "" || w.manageJobsWithoutQueueName {
		controllerutil.AddFinalizer(pod.Object(), PodFinalizer)

		if pod.pod.Labels == nil {
			pod.pod.Labels = make(map[string]string)
		}
		pod.pod.Labels[ManagedLabelKey] = ManagedLabelValue

		gate(&pod.pod)

		if features.Enabled(features.TopologyAwareScheduling) && jobframework.PodSetTopologyRequest(&pod.pod.ObjectMeta) != nil {
			pod.pod.Labels[kueuealpha.TASLabel] = "true"
			utilpod.Gate(&pod.pod, kueuealpha.TopologySchedulingGate)
		}

		if podGroupName(pod.pod) != "" {
			if err := pod.addRoleHash(); err != nil {
				return err
			}
		}
	}

	// copy back to the object
	pod.pod.DeepCopyInto(obj.(*corev1.Pod))
	return nil
}

// +kubebuilder:webhook:path=/validate--v1-pod,mutating=false,failurePolicy=fail,sideEffects=None,groups="",resources=pods,verbs=create;update,versions=v1,name=vpod.kb.io,admissionReviewVersions=v1

var _ admission.CustomValidator = &PodWebhook{}

func (w *PodWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	var warnings admission.Warnings

	pod := FromObject(obj)
	log := ctrl.LoggerFrom(ctx).WithName("pod-webhook").WithValues("pod", klog.KObj(&pod.pod))
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
	log := ctrl.LoggerFrom(ctx).WithName("pod-webhook").WithValues("pod", klog.KObj(&newPod.pod))
	log.V(5).Info("Validating update")

	allErrs := jobframework.ValidateJobOnUpdate(oldPod, newPod)
	allErrs = append(allErrs, validateCommon(newPod)...)

	allErrs = append(allErrs, validation.ValidateImmutableField(podGroupName(newPod.pod), podGroupName(oldPod.pod), groupNameLabelPath)...)
	allErrs = append(allErrs, validateUpdateForRetriableInGroupAnnotation(oldPod, newPod)...)

	if warn := warningForPodManagedLabel(newPod); warn != "" {
		warnings = append(warnings, warn)
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
	return allErrs
}

func validateManagedLabel(pod *Pod) field.ErrorList {
	var allErrs field.ErrorList

	if managedLabel, ok := pod.pod.GetLabels()[ManagedLabelKey]; ok && managedLabel != ManagedLabelValue {
		return append(allErrs, field.Forbidden(managedLabelPath, fmt.Sprintf("managed label value can only be '%s'", ManagedLabelValue)))
	}

	return allErrs
}

// warningForPodManagedLabel returns a warning message if the pod has a managed label, and it's parent is managed by kueue
func warningForPodManagedLabel(p *Pod) string {
	if managedLabel := p.pod.GetLabels()[ManagedLabelKey]; managedLabel == ManagedLabelValue && IsPodOwnerManagedByKueue(p) {
		return fmt.Sprintf("pod owner is managed by kueue, label '%s=%s' might lead to unexpected behaviour",
			ManagedLabelKey, ManagedLabelValue)
	}

	return ""
}

func validatePodGroupMetadata(p *Pod) field.ErrorList {
	var allErrs field.ErrorList

	gtc, gtcExists := p.pod.GetAnnotations()[GroupTotalCountAnnotation]

	if podGroupName(p.pod) == "" {
		if gtcExists {
			return append(allErrs, field.Required(
				groupNameLabelPath,
				fmt.Sprintf("both the '%s' annotation and the '%s' label should be set", GroupTotalCountAnnotation, GroupNameLabel),
			))
		}
	} else {
		allErrs = append(allErrs, jobframework.ValidateLabelAsCRDName(p, GroupNameLabel)...)

		if !gtcExists {
			return append(allErrs, field.Required(
				groupTotalCountAnnotationPath,
				fmt.Sprintf("both the '%s' annotation and the '%s' label should be set", GroupTotalCountAnnotation, GroupNameLabel),
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

	return field.ErrorList{}
}

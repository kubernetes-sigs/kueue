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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"sigs.k8s.io/kueue/pkg/controller/jobframework"
)

const (
	ManagedLabelKey   = "kueue.x-k8s.io/managed"
	ManagedLabelValue = "true"
	PodFinalizer      = ManagedLabelKey
)

var (
	labelsPath       = field.NewPath("metadata", "labels")
	managedLabelPath = labelsPath.Key(ManagedLabelKey)
)

type PodWebhook struct {
	client                     client.Client
	manageJobsWithoutQueueName bool
	namespaceSelector          *metav1.LabelSelector
	podSelector                *metav1.LabelSelector
}

// SetupWebhook configures the webhook for pods.
func SetupWebhook(mgr ctrl.Manager, opts ...jobframework.Option) error {
	options := jobframework.DefaultOptions
	for _, opt := range opts {
		opt(&options)
	}
	wh := &PodWebhook{
		client:                     mgr.GetClient(),
		manageJobsWithoutQueueName: options.ManageJobsWithoutQueueName,
		namespaceSelector:          options.PodNamespaceSelector,
		podSelector:                options.PodSelector,
	}
	return ctrl.NewWebhookManagedBy(mgr).
		For(&corev1.Pod{}).
		WithDefaulter(wh).
		WithValidator(wh).
		Complete()
}

// +kubebuilder:webhook:path=/mutate--v1-pod,mutating=true,failurePolicy=fail,sideEffects=None,groups="",resources=pods,verbs=create,versions=v1,name=mpod.kb.io,admissionReviewVersions=v1
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch

var _ webhook.CustomDefaulter = &PodWebhook{}

func (w *PodWebhook) Default(ctx context.Context, obj runtime.Object) error {
	pod := fromObject(obj)
	log := ctrl.LoggerFrom(ctx).WithName("pod-webhook").WithValues("pod", klog.KObj(pod))
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
	if !podSelector.Matches(labels.Set(pod.GetLabels())) {
		return nil
	}

	// Get pod namespace and check for namespace label selector match
	ns := corev1.Namespace{}
	err = w.client.Get(ctx, client.ObjectKey{Name: pod.GetNamespace()}, &ns)
	if err != nil {
		return fmt.Errorf("failed to run mutating webhook on pod %s, error while getting namespace: %w",
			pod.GetName(),
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

		if pod.Labels == nil {
			pod.Labels = make(map[string]string)
		}
		pod.Labels[ManagedLabelKey] = ManagedLabelValue

		if pod.gateIndex() == gateNotFound {
			log.V(5).Info("Adding gate")
			pod.Spec.SchedulingGates = append(pod.Spec.SchedulingGates, corev1.PodSchedulingGate{Name: SchedulingGateName})
		}
	}

	return nil
}

// +kubebuilder:webhook:path=/validate--v1-pod,mutating=false,failurePolicy=fail,sideEffects=None,groups="",resources=pods,verbs=create;update,versions=v1,name=vpod.kb.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &PodWebhook{}

func (w *PodWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	var warnings admission.Warnings

	pod := fromObject(obj)
	log := ctrl.LoggerFrom(ctx).WithName("pod-webhook").WithValues("pod", klog.KObj(pod))
	log.V(5).Info("Validating create")
	allErrs := jobframework.ValidateCreateForQueueName(pod)

	allErrs = append(allErrs, validateManagedLabel(pod)...)

	if warn := warningForPodManagedLabel(pod); warn != "" {
		warnings = append(warnings, warn)
	}

	return warnings, allErrs.ToAggregate()
}

func (w *PodWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	var warnings admission.Warnings

	oldPod := fromObject(oldObj)
	newPod := fromObject(newObj)
	log := ctrl.LoggerFrom(ctx).WithName("pod-webhook").WithValues("pod", klog.KObj(newPod))
	log.V(5).Info("Validating update")
	allErrs := jobframework.ValidateUpdateForQueueName(oldPod, newPod)

	allErrs = append(allErrs, validateManagedLabel(newPod)...)

	if warn := warningForPodManagedLabel(newPod); warn != "" {
		warnings = append(warnings, warn)
	}

	return warnings, allErrs.ToAggregate()
}

func (w *PodWebhook) ValidateDelete(context.Context, runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func validateManagedLabel(pod *Pod) field.ErrorList {
	var allErrs field.ErrorList

	if managedLabel, ok := pod.GetLabels()[ManagedLabelKey]; ok && managedLabel != ManagedLabelValue {
		return append(allErrs, field.Forbidden(managedLabelPath, fmt.Sprintf("managed label value can only be '%s'", ManagedLabelValue)))
	}

	return allErrs
}

// warningForPodManagedLabel returns a warning message if the pod has a managed label, and it's parent is managed by kueue
func warningForPodManagedLabel(p *Pod) string {
	if managedLabel := p.GetLabels()[ManagedLabelKey]; managedLabel == ManagedLabelValue && IsPodOwnerManagedByKueue(p) {
		return fmt.Sprintf("pod owner is managed by kueue, label '%s=%s' might lead to unexpected behaviour",
			ManagedLabelKey, ManagedLabelValue)
	}

	return ""
}

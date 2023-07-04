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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"sigs.k8s.io/kueue/pkg/controller/jobframework"
)

type PodWebhook struct{}

// SetupWebhook configures the webhook for pods.
func SetupWebhook(mgr ctrl.Manager, _ ...jobframework.Option) error {
	wh := &PodWebhook{}
	return ctrl.NewWebhookManagedBy(mgr).
		For(&corev1.Pod{}).
		WithDefaulter(wh).
		WithValidator(wh).
		Complete()
}

func fromObject(o runtime.Object) *Pod {
	return (*Pod)(o.(*corev1.Pod))
}

// +kubebuilder:webhook:path=/mutate--v1-pod,mutating=true,failurePolicy=ignore,sideEffects=None,groups="",resources=pods,verbs=create,versions=v1,name=mpod.kb.io,admissionReviewVersions=v1

//TODO: check if we can use namespace/object selectors to skip this webhook

var _ webhook.CustomDefaulter = &PodWebhook{}

func (w *PodWebhook) Default(ctx context.Context, obj runtime.Object) error {
	pod := fromObject(obj)
	log := ctrl.LoggerFrom(ctx).WithName("pod-webhook").WithValues("pod", klog.KObj(pod))
	log.V(5).Info("Applying defaults")

	if jobframework.QueueName(pod) != "" {
		if pod.gateIndex() == gateNotFound {
			log.V(5).Info("Adding gate")
			pod.Spec.SchedulingGates = append(pod.Spec.SchedulingGates, corev1.PodSchedulingGate{Name: SchedulingGateName})
		}
	}

	return nil
}

// +kubebuilder:webhook:path=/validate--v1-pod,mutating=false,failurePolicy=ignore,sideEffects=None,groups="",resources=pods,verbs=create;update,versions=v1,name=vpod.kb.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &PodWebhook{}

func (w *PodWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	pod := fromObject(obj)
	log := ctrl.LoggerFrom(ctx).WithName("pod-webhook").WithValues("pod", klog.KObj(pod))
	log.V(5).Info("Validating create")
	allErrs := jobframework.ValidateCreateForQueueName(pod)
	return nil, allErrs.ToAggregate()
}

func (w *PodWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldPod := fromObject(oldObj)
	newPod := fromObject(newObj)
	log := ctrl.LoggerFrom(ctx).WithName("pod-webhook").WithValues("pod", klog.KObj(newPod))
	log.V(5).Info("Validating update")
	allErrs := jobframework.ValidateUpdateForQueueName(oldPod, newPod)
	return nil, allErrs.ToAggregate()
}

func (w *PodWebhook) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

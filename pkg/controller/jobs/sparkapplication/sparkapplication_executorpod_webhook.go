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

package sparkapplication

import (
	"context"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobframework/webhook"

	sparkappv1beta2 "github.com/kubeflow/spark-operator/v2/api/v1beta2"
	sparkapputil "github.com/kubeflow/spark-operator/v2/pkg/util"
)

// SparkApplicationWebhook handles executor Pods managed under SparkApplication.
// This webhook is responsible for setting owner reference on executor pods under SparkApplication to the SparkApplication.
// It is because SparkApplication is not the direct owner of executor pods.
// In SparkApplication, owner reference hierarchy is as follows:
//
//	SparkApplication <--(owner)-- driver pod <--(owner)-- executor pods
//
// Thus, we set non-controller owner reference on executor pods to SparkApplication to make sure that kueue can find the SparkApplication from executor pods.
type SparkApplicationExecutorPodWebhook struct {
	client                       client.Client
	queues                       *qcache.Manager
	manageJobsWithoutQueueName   bool
	managedJobsNamespaceSelector labels.Selector
	cache                        *schdcache.Cache
}

// +kubebuilder:webhook:path=/mutate-sparkoperator-k8s-io-v1beta2-sparkapplication-executor-pod,mutating=true,failurePolicy=fail,sideEffects=None,reinvocationPolicy=IfNeeded,groups="",resources=pods,verbs=create,versions=v1,name=mpod.sparkapplication.kb.io,admissionReviewVersions=v1

var _ admission.CustomDefaulter = &SparkApplicationExecutorPodWebhook{}

func setupSparkApplicationLaunchedExecutorPodWebhook(mgr ctrl.Manager, opts ...jobframework.Option) error {
	options := jobframework.ProcessOptions(opts...)

	wh := &SparkApplicationExecutorPodWebhook{
		client:                       mgr.GetClient(),
		queues:                       options.Queues,
		manageJobsWithoutQueueName:   options.ManageJobsWithoutQueueName,
		managedJobsNamespaceSelector: options.ManagedJobsNamespaceSelector,
		cache:                        options.Cache,
	}
	obj := &corev1.Pod{}
	return webhook.WebhookManagedBy(mgr).
		For(obj).
		WithMutationHandler(admission.WithCustomDefaulter(mgr.GetScheme(), obj, wh)).
		WithMutatingWebhookPath("/mutate-sparkoperator-k8s-io-v1beta2-sparkapplication-executor-pod").
		Complete()
}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type
func (w *SparkApplicationExecutorPodWebhook) Default(ctx context.Context, obj runtime.Object) error {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return nil
	}
	log := ctrl.LoggerFrom(ctx).WithName("sparkapplication-executor-pod-webhook").WithValues("pod", klog.KObj(pod))
	log.V(5).Info("Applying defaults")

	if !w.isSparkApplicationManagedExecutorPod(ctx, pod) {
		log.V(5).Info("The pod is not a SparkApplication managed executor pod. Skip mutation")
		return nil
	}

	sparkApp, err := w.getSparkApplicationFromExecutorPod(ctx, pod)
	if err != nil {
		log.Error(err, "failed to resolve SparkApplication from executor Pod")
		return err
	}

	log = log.WithValues("sparkapplication", klog.KObj(sparkApp))
	if jobframework.QueueName(FromObject(sparkApp)) == "" {
		log.V(5).Info("SparkApplication is not managed by kueue. Skip mutation")
		return nil
	}

	// set owner reference(not controller reference) to the SparkApplication
	ownerReference := sparkapputil.GetOwnerReference(sparkApp)
	ownerReference.Controller = ptr.To(false)
	ownerReference.BlockOwnerDeletion = ptr.To(false)

	if slices.Contains(pod.OwnerReferences, ownerReference) {
		log.V(5).Info("The executor pod already has the owner reference to the SparkApplication. Skip mutation")
		return nil
	}

	log.V(5).Info("Owner reference to the SparkApplication is added to the executor pod", "ownerReference", ownerReference.String())
	pod.OwnerReferences = append(pod.OwnerReferences, ownerReference)

	return nil
}

func (w *SparkApplicationExecutorPodWebhook) isSparkApplicationManagedExecutorPod(ctx context.Context, pod *corev1.Pod) bool {
	return sparkapputil.IsLaunchedBySparkOperator(pod) && sparkapputil.IsExecutorPod(pod)
}

func (w *SparkApplicationExecutorPodWebhook) getSparkApplicationFromExecutorPod(ctx context.Context, pod *corev1.Pod) (*sparkappv1beta2.SparkApplication, error) {
	ownerRef := metav1.GetControllerOf(pod)
	if ownerRef == nil {
		return nil, fmt.Errorf("the owner reference is missing: %v", ownerRef)
	}
	if !(ownerRef.APIVersion == "v1" && ownerRef.Kind == "Pod") {
		return nil, fmt.Errorf("the owner reference is not a Pod: %v", ownerRef)
	}

	driverPod := &corev1.Pod{}
	err := w.client.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: ownerRef.Name}, driverPod)
	if err != nil {
		return nil, fmt.Errorf("failed to get driver pod: %v", err)
	}

	appName := sparkapputil.GetAppName(driverPod)
	if appName == "" {
		return nil, fmt.Errorf("the SparkApplication name label is missing in the driver Pod: %v", driverPod.Labels)
	}

	sparkApp := &sparkappv1beta2.SparkApplication{}
	err = w.client.Get(ctx, client.ObjectKey{Namespace: driverPod.Namespace, Name: appName}, sparkApp)
	if err != nil {
		return nil, fmt.Errorf("failed to get SparkApplication: %v", err)
	}

	return sparkApp, nil
}

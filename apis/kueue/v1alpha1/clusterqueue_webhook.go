/*
Copyright 2021 The Kubernetes Authors.

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

package v1alpha1

import (
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

// log is for logging in this package.
var clusterQueueLog = ctrl.Log.WithName("cluster-queue-webhook")

func (cq *ClusterQueue) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(cq).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-kueue-x-k8s-io-v1alpha1-clusterqueue,mutating=true,failurePolicy=fail,sideEffects=None,groups=kueue.x-k8s.io,resources=clusterqueues,verbs=create,versions=v1alpha1,name=mclusterqueue.kb.io,admissionReviewVersions=v1

var _ webhook.Defaulter = &ClusterQueue{}

// Default implements webhook.Defaulter so a webhook will be registered for the type
func (cq *ClusterQueue) Default() {
	clusterQueueLog.Info("defaulter", "clusterQueue", klog.KObj(cq))
	if !controllerutil.ContainsFinalizer(cq, ResourceInUseFinalizerName) {
		controllerutil.AddFinalizer(cq, ResourceInUseFinalizerName)
	}
}

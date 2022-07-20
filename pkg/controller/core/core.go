/*
Copyright 2022 The Kubernetes Authors.

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

package core

import (
	ctrl "sigs.k8s.io/controller-runtime"

	kueuev1alpha1 "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/queue"
)

const updateChBuffer = 10

// SetupControllers sets up the core controllers. It returns the name of the
// controller that failed to create and an error, if any.
func SetupControllers(mgr ctrl.Manager, qManager *queue.Manager, cc *cache.Cache) (string, error) {
	rfRec := NewResourceFlavorReconciler(mgr.GetClient(), qManager, cc)
	if err := rfRec.SetupWithManager(mgr); err != nil {
		return "ResourceFlavor", err
	}
	qRec := NewQueueReconciler(mgr.GetClient(), qManager, cc)
	if err := qRec.SetupWithManager(mgr); err != nil {
		return "Queue", err
	}
	cqRec := NewClusterQueueReconciler(mgr.GetClient(), qManager, cc, rfRec)
	if err := cqRec.SetupWithManager(mgr); err != nil {
		return "ClusterQueue", err
	}
	if err := NewWorkloadReconciler(mgr.GetClient(), qManager, cc, qRec, cqRec).SetupWithManager(mgr); err != nil {
		return "Workload", err
	}
	return "", nil
}

// SetupWebhooks sets up the webhooks for core controllers. It returns the name of the
// webhook that failed to create and an error, if any.
func SetupWebhooks(mgr ctrl.Manager) (string, error) {
	if err := (&kueuev1alpha1.Workload{}).SetupWebhookWithManager(mgr); err != nil {
		return "Workload", err
	}

	if err := (&kueuev1alpha1.ResourceFlavor{}).SetupWebhookWithManager(mgr); err != nil {
		return "ResourceFlavor", err
	}

	if err := (&kueuev1alpha1.ClusterQueue{}).SetupWebhookWithManager(mgr); err != nil {
		return "ClusterQueue", err
	}

	if err := (&kueuev1alpha1.Queue{}).SetupWebhookWithManager(mgr); err != nil {
		return "Queue", err
	}
	return "", nil
}

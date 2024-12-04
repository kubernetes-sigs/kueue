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

package jobframework

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/pkg/features"
)

func ApplyDefaultForSuspend(ctx context.Context, job GenericJob, k8sClient client.Client,
	manageJobsWithoutQueueName bool, managedJobsNamespaceSelector labels.Selector) error {
	// Do not default suspend a job whose owner is already managed by Kueue
	if owner := metav1.GetControllerOf(job.Object()); owner != nil && IsOwnerManagedByKueue(owner) {
		return nil
	}

	// Do not default suspend a job without a queue name unless the namespace selector also matches
	if features.Enabled(features.ManagedJobsNamespaceSelector) && manageJobsWithoutQueueName && QueueName(job) == "" {
		ns := corev1.Namespace{}
		err := k8sClient.Get(ctx, client.ObjectKey{Name: job.Object().GetNamespace()}, &ns)
		if err != nil {
			return fmt.Errorf("failed to get namespace: %w", err)
		}
		if !managedJobsNamespaceSelector.Matches(labels.Set(ns.GetLabels())) {
			return nil
		}
	}

	if QueueName(job) != "" || manageJobsWithoutQueueName {
		if !job.IsSuspended() {
			job.Suspend()
		}
	}
	return nil
}

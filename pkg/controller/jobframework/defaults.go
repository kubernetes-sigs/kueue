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

	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
)

func ApplyDefaultForSuspend(ctx context.Context, job GenericJob, k8sClient client.Client,
	manageJobsWithoutQueueName bool, managedJobsNamespaceSelector labels.Selector) error {
	suspend, err := WorkloadShouldBeSuspended(ctx, job.Object(), k8sClient, manageJobsWithoutQueueName, managedJobsNamespaceSelector)
	if err != nil {
		return err
	}
	if suspend && !job.IsSuspended() {
		job.Suspend()
	}
	return nil
}

// WorkloadShouldBeSuspended determines whether jobObj should be default suspended on creation
func WorkloadShouldBeSuspended(ctx context.Context, jobObj client.Object, k8sClient client.Client,
	manageJobsWithoutQueueName bool, managedJobsNamespaceSelector labels.Selector) (bool, error) {
	// Jobs with queue names are default suspended
	if QueueNameForObject(jobObj) != "" {
		return true, nil
	}

	if manageJobsWithoutQueueName {
		// Do not default a job whose owner kind is managed by Kueue -- defaulting is done at outermost level
		if owner := metav1.GetControllerOf(jobObj); owner != nil && IsOwnerManagedByKueue(owner) {
			return false, nil
		}

		if features.Enabled(features.ManagedJobsNamespaceSelector) {
			// Default suspend the job if the namespace selector matches
			ns := corev1.Namespace{}
			err := k8sClient.Get(ctx, client.ObjectKey{Name: jobObj.GetNamespace()}, &ns)
			if err != nil {
				return false, fmt.Errorf("failed to get namespace: %w", err)
			}
			return managedJobsNamespaceSelector.Matches(labels.Set(ns.GetLabels())), nil
		} else {
			// Namespace filtering is disabled; unconditionally default suspend
			return true, nil
		}
	}

	return false, nil
}

func ApplyDefaultLocalQueue(jobObj client.Object, defaultQueueExist func(string) bool) {
	if !features.Enabled(features.LocalQueueDefaulting) || !defaultQueueExist(jobObj.GetNamespace()) {
		return
	}
	if QueueNameForObject(jobObj) == "" {
		// Do not default a job whose owner kind is managed by Kueue -- defaulting is done at outermost level
		if owner := metav1.GetControllerOf(jobObj); owner != nil && IsOwnerManagedByKueue(owner) {
			return
		}
		labels := jobObj.GetLabels()
		if labels == nil {
			labels = make(map[string]string, 1)
		}
		labels[constants.QueueLabel] = constants.DefaultLocalQueueName
		jobObj.SetLabels(labels)
	}
}

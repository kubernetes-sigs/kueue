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

package jobframework

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
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
	// Do not default suspend a job whose ancestor is already managed by Kueue
	ancestorJob, err := FindAncestorJobManagedByKueue(ctx, k8sClient, jobObj, manageJobsWithoutQueueName)
	if err != nil || ancestorJob != nil {
		return false, err
	}

	// Jobs with queue names whose parents are not managed by Kueue are default suspended
	if QueueNameForObject(jobObj) != "" {
		return true, nil
	}

	// Logic for managing jobs without queue names.
	if manageJobsWithoutQueueName {
		if managedJobsNamespaceSelector != nil {
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
		// Do not default the queue-name for a job whose owner is already managed by Kueue
		if IsOwnerManagedByKueueForObject(jobObj) {
			return
		}
		labels := jobObj.GetLabels()
		if labels == nil {
			labels = make(map[string]string, 1)
		}
		labels[constants.QueueLabel] = string(constants.DefaultLocalQueueName)
		jobObj.SetLabels(labels)
	}
}

func CopyLabelAndAnnotationFromOwner(ctx context.Context, jobObj client.Object, k8sClient client.Client, log logr.Logger) {
	if QueueNameForObject(jobObj) != "" {
		return
	}
	owner := metav1.GetControllerOf(jobObj)
	if owner == nil {
		log.V(12).Info("Did not find owner for job", "jobName", jobObj.GetName())
		return
	}

	parentObj := getEmptyOwnerObject(owner)
	if parentObj == nil {
		log.V(12).Info("Did not get empty owner object for job", "jobName", jobObj.GetName(), "ownerKind", owner.Kind, "ownerName", owner.Name)
		return
	}

	err := k8sClient.Get(ctx, client.ObjectKey{Name: owner.Name, Namespace: jobObj.GetNamespace()}, parentObj)
	if err != nil {
		log.Error(err, "Failed to get owner object from k8s", "jobName", jobObj.GetName(), "ownerKind", owner.Kind, "ownerName", owner.Name)
		return
	}
	log.V(12).Info("Got owner object for job", "jobName", jobObj.GetName(), "ownerKind", owner.Kind, "ownerName", owner.Name)

	queueName := parentObj.GetLabels()[constants.QueueLabel]
	if queueName != "" {
		jobLabels := jobObj.GetLabels()
		if jobLabels == nil {
			jobLabels = make(map[string]string, 1)
		}
		jobLabels[constants.QueueLabel] = queueName
		jobObj.SetLabels(jobLabels)
		log.V(12).Info("Copied kueue queue name from owner object to job", "queueName", queueName, "jobName", jobObj.GetName(), "ownerKind", owner.Kind, "ownerName", owner.Name)
	}

	workloadslicingAnnotationValue := parentObj.GetAnnotations()[workloadslicing.EnabledAnnotationKey]
	if workloadslicingAnnotationValue != "" {
		jobAnnotations := jobObj.GetAnnotations()
		if jobAnnotations == nil {
			jobAnnotations = make(map[string]string, 1)
		}
		jobAnnotations[workloadslicing.EnabledAnnotationKey] = workloadslicingAnnotationValue
		jobObj.SetAnnotations(jobAnnotations)
		log.V(12).Info("Copied workloadslicing annotation from owner object to job", "annotationValue", workloadslicingAnnotationValue, "jobName", jobObj.GetName(), "ownerKind", owner.Kind, "ownerName", owner.Name)
	}
}

func ApplyDefaultForManagedBy(job GenericJob, queues *qcache.Manager, cache *schdcache.Cache, log logr.Logger) {
	if managedJob, ok := job.(JobWithManagedBy); ok {
		if managedJob.CanDefaultManagedBy() {
			localQueueName, found := job.Object().GetLabels()[constants.QueueLabel]
			if !found {
				return
			}
			clusterQueueName, ok := queues.ClusterQueueFromLocalQueue(utilqueue.NewLocalQueueReference(job.Object().GetNamespace(), kueue.LocalQueueName(localQueueName)))
			if !ok {
				log.V(5).Info("Cluster queue for local queue not found", "localQueueName", localQueueName)
				return
			}
			for _, admissionCheck := range cache.AdmissionChecksForClusterQueue(clusterQueueName) {
				if admissionCheck.Controller == kueue.MultiKueueControllerName {
					log.V(5).Info("Defaulting ManagedBy", "oldManagedBy", managedJob.ManagedBy(), "managedBy", kueue.MultiKueueControllerName)
					managedJob.SetManagedBy(ptr.To(kueue.MultiKueueControllerName))
					return
				}
			}
		}
	}
}

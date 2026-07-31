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

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utilpriority "sigs.k8s.io/kueue/pkg/util/priority"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
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

func ApplyDefaultLocalQueue(ctx context.Context, k8sClient client.Client, jobObj client.Object, defaultQueueExist func(string) bool, managedJobsNamespaceSelector labels.Selector) error {
	if !defaultQueueExist(jobObj.GetNamespace()) {
		return nil
	}
	if QueueNameForObject(jobObj) == "" {
		// Do not default the queue-name for a job whose owner is already managed by Kueue
		if IsOwnerManagedByKueueForObject(jobObj) {
			return nil
		}
		if managed, err := namespaceMatchesSelector(ctx, k8sClient, jobObj.GetNamespace(), managedJobsNamespaceSelector); err != nil {
			return err
		} else if !managed {
			return nil
		}
		labels := jobObj.GetLabels()
		if labels == nil {
			labels = make(map[string]string, 1)
		}
		labels[constants.QueueLabel] = string(constants.DefaultLocalQueueName)
		jobObj.SetLabels(labels)
	}
	return nil
}

func ApplyDefaultWorkloadPriorityClass(ctx context.Context, c client.Client, jobObj client.Object) {
	if !features.Enabled(features.WorkloadPriorityClassDefaulting) {
		return
	}
	if WorkloadPriorityClassName(jobObj) != "" {
		return
	}
	if IsOwnerManagedByKueueForObject(jobObj) {
		return
	}
	exists, err := utilpriority.DefaultWorkloadPriorityClassExist(ctx, c)
	if err != nil {
		log := ctrl.LoggerFrom(ctx)
		log.V(2).Error(err, "Failed to check for default WorkloadPriorityClass")
		return
	}
	if !exists {
		return
	}
	labels := jobObj.GetLabels()
	if labels == nil {
		labels = make(map[string]string, 1)
	}
	labels[constants.WorkloadPriorityClassLabel] = constants.DefaultWorkloadPriorityClassName
	jobObj.SetLabels(labels)
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

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

func (m *IntegrationManager) ApplyDefaultForSuspend(ctx context.Context, job GenericJob, k8sClient client.Client,
	manageJobsWithoutQueueName bool, managedJobsNamespaceSelector labels.Selector) error {
	suspend, err := m.WorkloadShouldBeSuspended(ctx, job.Object(), k8sClient, manageJobsWithoutQueueName, managedJobsNamespaceSelector, WithDeletingObjectTolerance(true))
	if err != nil {
		return err
	}
	if suspend && !job.IsSuspended() {
		job.Suspend()
	}
	return nil
}

func (m *IntegrationManager) ApplyDefaultLocalQueue(
	ctx context.Context,
	k8sClient client.Client,
	jobObj client.Object,
	defaultQueueExist func(string) bool,
	managedJobsNamespaceSelector labels.Selector,
) error {
	if !m.defaultLocalQueueApplies(jobObj, defaultQueueExist) {
		return nil
	}
	// Reached only when the label is about to be set, so an object that is not a
	// candidate costs no Namespace read.
	managed, err := namespaceMatchesSelector(ctx, k8sClient, jobObj.GetNamespace(), managedJobsNamespaceSelector)
	if err != nil {
		return err
	}
	if !managed {
		return nil
	}
	setDefaultLocalQueue(jobObj)
	return nil
}

func (m *IntegrationManager) defaultLocalQueueApplies(jobObj client.Object, defaultQueueExist func(string) bool) bool {
	if !defaultQueueExist(jobObj.GetNamespace()) {
		return false
	}
	if QueueNameForObject(jobObj) != "" {
		return false
	}
	// Do not default the queue-name for a job whose owner is already managed by Kueue
	return !m.IsOwnerManagedByKueueForObject(jobObj)
}

func setDefaultLocalQueue(jobObj client.Object) {
	jobLabels := jobObj.GetLabels()
	if jobLabels == nil {
		jobLabels = make(map[string]string, 1)
	}
	jobLabels[constants.QueueLabel] = string(constants.DefaultLocalQueueName)
	jobObj.SetLabels(jobLabels)
}

func (m *IntegrationManager) ApplyDefaultWorkloadPriorityClass(ctx context.Context, c client.Client, jobObj client.Object) {
	if !features.Enabled(features.WorkloadPriorityClassDefaulting) {
		return
	}
	if WorkloadPriorityClassName(jobObj) != "" {
		return
	}
	if m.IsOwnerManagedByKueueForObject(jobObj) {
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
					managedJob.SetManagedBy(new(kueue.MultiKueueControllerName))
					return
				}
			}
		}
	}
}

// skipCheckForDeletedObject reports whether suspend/ancestry processing should be skipped
// for an object that is already being deleted (e.g. during GC teardown, where owners may
// legitimately be gone already). Gated by SkipAncestorCheckForDeletedWorkloads so affected
// environments can restore the previous behavior of failing the admission request.
func skipCheckForDeletedObject(obj client.Object) bool {
	return features.Enabled(features.SkipAncestorCheckForDeletedWorkloads) && obj.GetDeletionTimestamp() != nil
}

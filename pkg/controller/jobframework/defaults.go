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
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utilpriority "sigs.k8s.io/kueue/pkg/util/priority"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
)

// ApplyDefaultForSuspend suspends a job that Kueue will manage, so that it
// waits for admission. It leaves alone a job that the job reconciler won't
// reconcile, which nothing would unsuspend.
func (m *IntegrationManager) ApplyDefaultForSuspend(ctx context.Context, job GenericJob, k8sClient client.Client,
	manageJobsWithoutQueueName bool, managedJobsNamespaceSelector labels.Selector) error {
	suspend, err := m.WorkloadShouldBeSuspended(ctx, job.Object(), k8sClient, manageJobsWithoutQueueName, managedJobsNamespaceSelector, WithDeletingObjectTolerance(true))
	if err != nil {
		return err
	}
	if !suspend || job.IsSuspended() {
		return nil
	}
	// The job reconciler skips every job in a namespace that
	// managedJobsNamespaceSelector doesn't match, even with a queue name, so a
	// job suspended there would stay suspended.
	managed, err := namespaceMatchesSelector(ctx, k8sClient, job.Object().GetNamespace(), managedJobsNamespaceSelector)
	if err != nil {
		return err
	}
	if managed {
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

// CanApplyDefaultLocalQueue reports whether the default LocalQueue can be set
// on an update. On a running workload the default is only applied to restore
// a dropped "default" label: StatefulSet and LeaderWorkerSet reject adding the
// queue-name to a running workload, and a running Deployment must not be moved
// under Kueue by an unrelated update.
func CanApplyDefaultLocalQueue(isSuspended bool, oldObj client.Object) bool {
	return isSuspended || QueueNameForObject(oldObj) == constants.DefaultLocalQueueName
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

func (m *IntegrationManager) ApplyDefaultWorkloadPriorityClass(
	ctx context.Context,
	k8sClient client.Client,
	jobObj client.Object,
	managedJobsNamespaceSelector labels.Selector,
) error {
	if !features.Enabled(features.WorkloadPriorityClassDefaulting) {
		return nil
	}
	if WorkloadPriorityClassName(jobObj) != "" {
		return nil
	}
	if m.IsOwnerManagedByKueueForObject(jobObj) {
		return nil
	}
	exists, err := utilpriority.DefaultWorkloadPriorityClassExist(ctx, k8sClient)
	if err != nil {
		return err
	}
	if !exists {
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
	jobLabels := jobObj.GetLabels()
	if jobLabels == nil {
		jobLabels = make(map[string]string, 1)
	}
	jobLabels[constants.WorkloadPriorityClassLabel] = constants.DefaultWorkloadPriorityClassName
	jobObj.SetLabels(jobLabels)
	return nil
}

// ApplyDefaultForManagedBy hands a job whose LocalQueue uses a MultiKueue
// ClusterQueue to the MultiKueue controller. A job in a namespace that
// managedJobsNamespaceSelector doesn't match is left alone: Kueue doesn't
// reconcile it, so nothing would run it.
func ApplyDefaultForManagedBy(ctx context.Context, job GenericJob, queues *qcache.Manager, cache *schdcache.Cache,
	k8sClient client.Client, managedJobsNamespaceSelector labels.Selector, log logr.Logger) error {
	managedJob, ok := job.(JobWithManagedBy)
	if !ok || !managedJob.CanDefaultManagedBy() {
		return nil
	}
	localQueueName, found := job.Object().GetLabels()[constants.QueueLabel]
	if !found {
		return nil
	}
	clusterQueueName, ok := queues.ClusterQueueFromLocalQueue(utilqueue.NewLocalQueueReference(job.Object().GetNamespace(), kueue.LocalQueueName(localQueueName)))
	if !ok {
		log.V(5).Info("Cluster queue for local queue not found", "localQueue", klog.KRef(job.Object().GetNamespace(), localQueueName))
		return nil
	}
	for _, admissionCheck := range cache.AdmissionChecksForClusterQueue(clusterQueueName) {
		if admissionCheck.Controller != kueue.MultiKueueControllerName {
			continue
		}
		managed, err := namespaceMatchesSelector(ctx, k8sClient, job.Object().GetNamespace(), managedJobsNamespaceSelector)
		if err != nil || !managed {
			return err
		}
		log.V(5).Info("Defaulting ManagedBy", "previousManagedBy", managedJob.ManagedBy(), "managedBy", kueue.MultiKueueControllerName)
		managedJob.SetManagedBy(new(kueue.MultiKueueControllerName))
		return nil
	}
	return nil
}

// skipCheckForDeletedObject reports whether suspend/ancestry processing should be skipped
// for an object that is already being deleted (e.g. during GC teardown, where owners may
// legitimately be gone already). Gated by SkipAncestorCheckForDeletedWorkloads so affected
// environments can restore the previous behavior of failing the admission request.
func skipCheckForDeletedObject(obj client.Object) bool {
	return features.Enabled(features.SkipAncestorCheckForDeletedWorkloads) && obj.GetDeletionTimestamp() != nil
}

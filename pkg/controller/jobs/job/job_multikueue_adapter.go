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

package job

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/api"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

type multiKueueAdapter struct{}

var _ jobframework.MultiKueueAdapter = (*multiKueueAdapter)(nil)

func (b *multiKueueAdapter) SyncJob(ctx context.Context, localClient client.Client, remoteClient client.Client, key types.NamespacedName, workloadName, origin string) error {
	log := ctrl.LoggerFrom(ctx)

	localJob := batchv1.Job{}
	err := localClient.Get(ctx, key, &localJob)
	if err != nil {
		log.Error(err, "Failed to get local job", "job", key)
		return err
	}

	remoteJob := batchv1.Job{}
	err = remoteClient.Get(ctx, key, &remoteJob)
	if client.IgnoreNotFound(err) != nil {
		return err
	}

	// the remote job exists
	if err == nil {
		if statusUpdate := determineStatusUpdate(log, &localJob, &remoteJob); statusUpdate != nil {
			localJob.Status = *statusUpdate
			return nil
		}

		if err := clientutil.PatchStatus(ctx, localClient, &localJob, func() (bool, error) {
			localJob.Status = remoteJob.Status
			return true, nil
		}); err != nil {
			return err
		}

		// Sync elastic workload if needed.
		if needElasticJobSync(log, workloadName, &localJob, &remoteJob) {
			return syncElasticJob(ctx, remoteClient, log, workloadName, &localJob, &remoteJob)
		}

		return nil
	}

	remoteJob = batchv1.Job{
		ObjectMeta: api.CloneObjectMetaForCreation(&localJob.ObjectMeta),
		Spec:       *localJob.Spec.DeepCopy(),
	}

	// cleanup
	// drop the selector
	remoteJob.Spec.Selector = nil
	// drop the templates cleanup labels
	cleanLabels(&remoteJob.Spec.Template)

	// Add prebuilt workload name and multikueue origin
	jobframework.SetMultiKueueMeta(&remoteJob, workloadName, origin)

	// clear the managedBy enables the batch/Job controller to take over
	remoteJob.Spec.ManagedBy = nil

	return remoteClient.Create(ctx, &remoteJob)
}

func determineStatusUpdate(log logr.Logger, localJob, remoteJob *batchv1.Job) *batchv1.JobStatus {
	localJobInfo := fromObject(localJob)

	log.V(2).Info("Checking shouldSkipSyncForSuspendedLocalJob", "localJob", localJob, "remoteJob", remoteJob)
	if !localJobInfo.IsSuspended() {
		// do not skip any syncs if the local Job is unsuspnded
		log.V(3).Info("Peforming the sync as the local Job is unsuspended")
		return &remoteJob.Status
	}
	remoteJobInfo := fromObject(remoteJob)
	if remoteJobInfo.IsSuspended() {
		// Do not skip any syncs if the remote Job is also suspended
		// This is needed to await for updating the status.active field
		// when the remote Job is evicted; see: https://github.com/kubernetes-sigs/kueue/pull/8151
		log.V(3).Info("Peforming the sync as the local and the remote Job are suspended")
		return &remoteJob.Status
	}
	if localJob.Status.StartTime == nil {
		newLocalStatus := localJob.Status.DeepCopy()
		newLocalStatus.Active = 0
		newConditions, _ := ensureJobConditionStatus(newLocalStatus.Conditions,
			batchv1.JobSuspended,
			corev1.ConditionTrue,
			"MultiKueueAdapted",
			"Set by MultiKueue adapted",
			time.Now())
		log.V(2).Info("Updating the localJob suspended Job without startTime to set the JobSuspended=True condition")
		newLocalStatus.Conditions = newConditions
		return newLocalStatus
	}

	// We skip the sync when the localJob has suspend=true, and the remote job is suspend=false
	// to prevent the race condition when the local Job is updated to suspend=false prematurely
	// so that the injection of nodeSlectors does not work; see: https://github.com/kubernetes-sigs/kueue/pull/3685
	log.V(2).Info("Skipping the sync when the localJob has suspend=true, and the remote job is suspend=false")
	return nil
}

// ensureJobConditionStatus appends or updates an existing job condition of the
// given type with the given status value. Note that this function will not
// append to the conditions list if the new condition's status is false
// (because going from nothing to false is meaningless); it can, however,
// update the status condition to false. The function returns a bool to let the
// caller know if the list was changed (either appended or updated).
func ensureJobConditionStatus(list []batchv1.JobCondition, cType batchv1.JobConditionType, status corev1.ConditionStatus, reason, message string, now time.Time) ([]batchv1.JobCondition, bool) {
	if condition := findConditionByType(list, cType); condition != nil {
		if condition.Status != status {
			*condition = *newCondition(cType, status, reason, message, now)
			return list, true
		}
		return list, false
	}
	// A condition with that type doesn't exist in the list.
	if status != corev1.ConditionFalse {
		return append(list, *newCondition(cType, status, reason, message, now)), true
	}
	return list, false
}

func findConditionByType(list []batchv1.JobCondition, cType batchv1.JobConditionType) *batchv1.JobCondition {
	for i := range list {
		if list[i].Type == cType {
			return &list[i]
		}
	}
	return nil
}

func newCondition(conditionType batchv1.JobConditionType, status corev1.ConditionStatus, reason, message string, now time.Time) *batchv1.JobCondition {
	return &batchv1.JobCondition{
		Type:               conditionType,
		Status:             status,
		LastProbeTime:      metav1.NewTime(now),
		LastTransitionTime: metav1.NewTime(now),
		Reason:             reason,
		Message:            message,
	}
}

func (b *multiKueueAdapter) DeleteRemoteObject(ctx context.Context, _ client.Client, remoteClient client.Client, key types.NamespacedName) error {
	job := batchv1.Job{}
	job.SetName(key.Name)
	job.SetNamespace(key.Namespace)
	return client.IgnoreNotFound(remoteClient.Delete(ctx, &job, client.PropagationPolicy(metav1.DeletePropagationBackground)))
}

func (b *multiKueueAdapter) IsJobManagedByKueue(ctx context.Context, c client.Client, key types.NamespacedName) (bool, string, error) {
	job := batchv1.Job{}
	err := c.Get(ctx, key, &job)
	if err != nil {
		return false, "", err
	}
	jobControllerName := ptr.Deref(job.Spec.ManagedBy, "")
	if jobControllerName != kueue.MultiKueueControllerName {
		return false, fmt.Sprintf("Expecting spec.managedBy to be %q not %q", kueue.MultiKueueControllerName, jobControllerName), nil
	}
	return true, "", nil
}

func (b *multiKueueAdapter) GVK() schema.GroupVersionKind {
	return gvk
}

var _ jobframework.MultiKueueWatcher = (*multiKueueAdapter)(nil)

func (*multiKueueAdapter) GetEmptyList() client.ObjectList {
	return &batchv1.JobList{}
}

func (*multiKueueAdapter) WorkloadKeysFor(o runtime.Object) ([]types.NamespacedName, error) {
	job, isJob := o.(*batchv1.Job)
	if !isJob {
		return nil, errors.New("not a job")
	}

	prebuiltWorkload := jobframework.PrebuiltWorkloadNameFor(job)
	if prebuiltWorkload == "" {
		return nil, fmt.Errorf("no prebuilt workload found for job: %s", klog.KObj(job))
	}

	return []types.NamespacedName{{Name: prebuiltWorkload, Namespace: job.Namespace}}, nil
}

// needElasticJobSync determines if a remote Job requires synchronization due to elastic job features.
// Returns true when elastic jobs via workload slices are enabled and workload slicing is enabled for the job,
// and either the parallelism differs between local and remote Jobs, or the remote Job lacks the workload label.
// Skips sync when detecting stale workload updates during scale-up by comparing workload names and parallelism.
func needElasticJobSync(log logr.Logger, workloadName string, localJob, remoteJob *batchv1.Job) bool {
	if !features.Enabled(features.ElasticJobsViaWorkloadSlices) || !workloadslicing.Enabled(localJob) {
		return false
	}
	oldParallelism := ptr.Deref(remoteJob.Spec.Parallelism, 0)
	newParallelism := ptr.Deref(localJob.Spec.Parallelism, 0)
	newWorkloadName := jobframework.GetWorkloadNameForOwnerWithGVKAndGeneration(localJob.GetName(), localJob.GetUID(), gvk, localJob.GetGeneration())

	// Detect and skip stale local Workload updates caused by a Job scale-up event.
	//
	// During scale-up, a race condition may occur between the GenericJobReconciler
	// and this controller. The GenericJobReconciler is responsible for creating a
	// new Workload slice that reflects the updated Job spec. Once admitted, that
	// new slice finalizes (Finishes) the old slice.
	//
	// If the current reconciliation observes the old Workload slice while the Job’s
	// parallelism has already increased, the slice is considered stale. In this case,
	// we return as if the sync is not needed to avoid propagating outdated state to the remote clusters.
	if oldParallelism < newParallelism && workloadName != newWorkloadName {
		log.V(2).Info("Skipping stale ElasticWorkload sync",
			"old.parallelism", oldParallelism,
			"new.parallelism", newParallelism,
			"workloadName", workloadName,
			"newWorkloadName", newWorkloadName)
		return false
	}

	return oldParallelism != newParallelism || remoteJob.Labels == nil || jobframework.PrebuiltWorkloadNameFor(remoteJob) != workloadName
}

// syncElasticJob updates the remote job's workload label and parallelism to match the local job configuration.
// It patches the remote job only if the parallelism value or workload name label has changed.
//
// Note: this call should be gated by needElasticJobSync to ensure it's only executed when necessary,
// and to perform check for stale workload updates during scale-up.
func syncElasticJob(ctx context.Context, remoteClient client.Client, log logr.Logger, workloadName string, localJob, remoteJob *batchv1.Job) error {
	oldParallelism := ptr.Deref(remoteJob.Spec.Parallelism, 0)
	newParallelism := ptr.Deref(localJob.Spec.Parallelism, 0)

	// Update a remote job's workload slice name and parallelism if needed.
	if err := clientutil.Patch(ctx, remoteClient, remoteJob, func() (bool, error) {
		// Update parallelism.
		remoteJob.Spec.Parallelism = localJob.Spec.Parallelism
		changed := oldParallelism != newParallelism

		// Update the workload name value.
		if cur := jobframework.PrebuiltWorkloadNameFor(remoteJob); cur == "" || cur != workloadName {
			jobframework.SetPrebuiltWorkloadName(remoteJob, workloadName)
			changed = true
		}

		return changed, nil
	}); err != nil {
		return fmt.Errorf("failed to patch remote job: %w", err)
	}

	return nil
}

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

	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
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

func (b *multiKueueAdapter) SyncJob(ctx context.Context, localClient client.Client, remoteClient client.Client, key types.NamespacedName, workloadName, origin string) (bool, error) {
	log := ctrl.LoggerFrom(ctx)

	localJob := batchv1.Job{}
	err := localClient.Get(ctx, key, &localJob)
	if err != nil {
		log.Error(err, "Failed to get local job", "job", key)
		return false, err
	}

	remoteJob := batchv1.Job{}
	err = remoteClient.Get(ctx, key, &remoteJob)
	if client.IgnoreNotFound(err) != nil {
		return false, err
	}

	// the remote job exists
	if err == nil {
		statusUpdate, deferred := determineStatusUpdate(ctx, log, &localJob, &remoteJob)
		if !equality.Semantic.DeepEqual(localJob.Status, *statusUpdate) {
			if err := clientutil.PatchStatus(ctx, localClient, &localJob, func() (bool, error) {
				localJob.Status = *statusUpdate
				return true, nil
			}); err != nil {
				return false, err
			}
		}

		// Sync elastic workload if needed. Propagate deferred: an elastic sync
		// does not unsuspend the local Job, so if determineStatusUpdate deferred
		// the status sync it is still deferred and the caller must requeue.
		if needElasticJobSync(log, workloadName, &localJob, &remoteJob) {
			return deferred, syncElasticJob(ctx, remoteClient, log, workloadName, &localJob, &remoteJob)
		}

		// Surface determineStatusUpdate's deferred flag to the caller; see
		// MultiKueueAdapter.SyncJob for the contract.
		return deferred, nil
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

	return false, remoteClient.Create(ctx, &remoteJob)
}

// determineStatusUpdate returns the JobStatus to write to the local Job, plus
// a deferred flag. deferred is true only for the "local suspended, remote
// unsuspended, not finished" branch — the one case where the real remote
// status.Active is intentionally NOT propagated. See MultiKueueAdapter.SyncJob
// for what the caller does with it. Returning both from one function keeps the
// deferred condition from drifting out of sync with the branches it describes.
func determineStatusUpdate(ctx context.Context, log logr.Logger, localJob, remoteJob *batchv1.Job) (status *batchv1.JobStatus, deferred bool) {
	localJobInfo := fromObject(localJob)

	log.V(3).Info("Determining status update for a MultiKueue Job", "localJob", localJob, "remoteJob", remoteJob)
	if !localJobInfo.IsSuspended() {
		// Sync status if the local Job is unsuspended.
		// This is safe as the local Job has already reached its final spec.
		// We expect no validation errors from further spec changes.
		log.V(3).Info("Performing the sync as the local Job is unsuspended")
		return &remoteJob.Status, false
	}
	remoteJobInfo := fromObject(remoteJob)
	if remoteJobInfo.IsSuspended() {
		// Sync status if both local and remote Job are suspended.
		// This is needed to await for updating the status.active field
		// when the remote Job is evicted; see: https://github.com/kubernetes-sigs/kueue/pull/8151
		log.V(3).Info("Peforming the sync as the local and the remote Job are both suspended")
		return &remoteJob.Status, false
	}
	if _, _, finished := remoteJobInfo.Finished(ctx); finished {
		// Sync status if the remote Job is finished.
		// Without this, the local Job could get stuck in the suspended state.
		log.V(2).Info("Performing the sync as the remote Job is finished")
		return &remoteJob.Status, false
	}

	// Remaining case: local Job is suspended, remote Job is unsuspended but not finished.
	// In this case:
	// 1. We essentially want *no sync* (to avoid prematurely setting non-zero .status.startTime
	//    which could block further spec updates; see: https://github.com/kubernetes-sigs/kueue/pull/3685).
	// 2. We rely on the MultiKueue admission lifecycle to eventually bring localJob to unsuspended state
	//    (remote admitted -> MultiKueue AdmissionCheck ready -> local admitted -> local unsuspended).
	// 3. However, to avoid clashing with K8s 1.36 validation rules
	//    (since https://github.com/kubernetes/kubernetes/pull/135104 until https://github.com/kubernetes/kubernetes/pull/139287)
	//    we manually patch a "JobSuspended=True" condition to unblock further spec updates.
	// Once we're on a K8s version unaffected by #3, the "if" block below can be removed.

	if !isJobStatusConditionTrue(localJob.Status.Conditions, batchv1.JobSuspended) {
		newLocalStatus := localJob.Status.DeepCopy()
		newLocalStatus.Conditions = setJobStatusCondition(newLocalStatus.Conditions,
			batchv1.JobCondition{
				Type:               batchv1.JobSuspended,
				Status:             corev1.ConditionTrue,
				Reason:             "MultiKueueAdapter",
				Message:            "Set by MultiKueue adapter",
				LastTransitionTime: metav1.Now(),
				LastProbeTime:      metav1.Now(),
			})
		log.V(2).Info("Updating the suspended local Job to comply with Kubernetes validation rules", "oldStatus", localJob.Status, "newStatus", newLocalStatus)
		return newLocalStatus, true
	}

	return &localJob.Status, true
}

func setJobStatusCondition(list []batchv1.JobCondition, condition batchv1.JobCondition) []batchv1.JobCondition {
	for i := range list {
		if list[i].Type == condition.Type {
			list[i] = condition
			return list
		}
	}
	list = append(list, condition)
	return list
}

func isJobStatusConditionTrue(conditions []batchv1.JobCondition, condType batchv1.JobConditionType) bool {
	for _, c := range conditions {
		if c.Type == condType {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
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
		if workloadslicing.Enabled(job) {
			prebuiltWorkload = jobframework.GetWorkloadNameForOwnerWithGVKAndGeneration(job.GetName(), job.GetUID(), gvk, job.GetGeneration())
		} else {
			prebuiltWorkload = jobframework.GetWorkloadNameForOwnerWithGVK(job.GetName(), job.GetUID(), gvk)
		}
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

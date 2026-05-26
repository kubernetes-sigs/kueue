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
		action := syncActionForSuspendedLocalJob(log, &localJob, &remoteJob)
		if action == skipSync {
			return nil
		}
		if action == skipSyncAndRetryLater {
			return jobframework.ErrPendingUnsuspend
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

func hasJobSuspendedCondition(job *batchv1.Job) bool {
	for i := range job.Status.Conditions {
		if job.Status.Conditions[i].Type == batchv1.JobSuspended && job.Status.Conditions[i].Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

type syncAction int

const (
	syncJob syncAction = iota
	skipSync
	skipSyncAndRetryLater
)

func syncActionForSuspendedLocalJob(log logr.Logger, localJob, remoteJob *batchv1.Job) syncAction {
	localJobInfo := fromObject(localJob)

	log.V(2).Info("Checking syncActionForSuspendedLocalJob", "localJob", localJob, "remoteJob", remoteJob)
	if !localJobInfo.IsSuspended() {
		// Do not skip any syncs if the local Job is unsuspended
		log.V(3).Info("Performing the sync as the local Job is unsuspended")
		return syncJob
	}

	remoteJobInfo := fromObject(remoteJob)
	if !remoteJobInfo.IsSuspended() {
		// We skip the sync when the local Job is suspended but the remote Job is not
		// to prevent the race condition when the local Job gets a non-zero .Status.StartTime prematurely
		// so that the injection of nodeSelectors does not work (PR #3685).
		// In this case, a sync will ultimately happen, once the local job gets admitted
		// (when MultiKueue notices an admitted remote) and consequently unsuspended.
		// We return skipSyncAndRetryLater so that the reconciler retries the sync after a short delay,
		// allowing time for the local Job controller to unsuspend the local Job.
		log.V(2).Info("Skipping the sync for suspended local Job when remote Job is unsuspended; retrying later")
		return skipSyncAndRetryLater
	}

	// If both the local and remote Jobs are suspended:
	// We only need to perform the status synchronization if there is a meaningful difference to propagate:
	// 1. The remote Job has the JobSuspended=True condition, but the local Job does not yet.
	// 2. The remote Job has reached 0 active pods, but the local Job still reports active or ready pods (needed for eviction).
	conditionTrigger := hasJobSuspendedCondition(remoteJob) && !hasJobSuspendedCondition(localJob)
	inactiveTrigger := !remoteJobInfo.IsActive() && localJobInfo.IsActive()

	if conditionTrigger || inactiveTrigger {
		log.V(3).Info("Performing the sync to propagate the status from a fully suspended remote Job", "conditionTrigger", conditionTrigger, "inactiveTrigger", inactiveTrigger)
		return syncJob
	}
	log.V(3).Info("Skipping the sync as the suspended status is already fully synchronized")
	return skipSync
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

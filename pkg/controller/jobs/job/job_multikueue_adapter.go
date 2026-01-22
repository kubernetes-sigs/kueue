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

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
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
		if features.Enabled(features.MultiKueueBatchJobWithManagedBy) {
			if fromObject(&localJob).IsSuspended() && !fromObject(&localJob).IsActive() {
				// Ensure the job is unsuspended before updating its status; otherwise, it will fail when patching the spec.
				log.V(2).Info("Skipping the sync since the local job is still suspended")
				return nil
			}
			return clientutil.PatchStatus(ctx, localClient, &localJob, func() (bool, error) {
				localJob.Status = remoteJob.Status
				return true, nil
			})
		}

		if _, _, remoteFinished := fromObject(&remoteJob).Finished(ctx); remoteFinished {
			return clientutil.PatchStatus(ctx, localClient, &localJob, func() (bool, error) {
				localJob.Status = remoteJob.Status
				return true, nil
			})
		}

		if workloadslicing.Enabled(&localJob) {
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
			// we skip syncing to avoid propagating outdated state to the remote clusters.
			if oldParallelism < newParallelism && workloadName != newWorkloadName {
				log.V(2).Info("Skipping stale ElasticWorkload sync",
					"old.parallelism", oldParallelism,
					"new.parallelism", newParallelism,
					"workloadName", workloadName,
					"newWorkloadName", newWorkloadName)
				return nil
			}

			// Update remote job's workload slice name and parallelism if needed.
			if err := clientutil.Patch(ctx, remoteClient, &remoteJob, func() (bool, error) {
				// Update workload name label.
				labelsChanged := false
				if remoteJob.Labels == nil {
					remoteJob.Labels = map[string]string{constants.PrebuiltWorkloadLabel: workloadName}
					labelsChanged = true
				} else {
					if cur, ok := remoteJob.Labels[constants.PrebuiltWorkloadLabel]; !ok || cur != workloadName {
						remoteJob.Labels[constants.PrebuiltWorkloadLabel] = workloadName
						labelsChanged = true
					}
				}

				// Update parallelism.
				remoteJob.Spec.Parallelism = localJob.Spec.Parallelism
				return oldParallelism != newParallelism || labelsChanged, nil
			}); err != nil {
				return fmt.Errorf("failed to patch remote job: %w", err)
			}
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

	// add the prebuilt workload
	if remoteJob.Labels == nil {
		remoteJob.Labels = map[string]string{}
	}
	remoteJob.Labels[constants.PrebuiltWorkloadLabel] = workloadName
	remoteJob.Labels[kueue.MultiKueueOriginLabel] = origin

	if features.Enabled(features.MultiKueueBatchJobWithManagedBy) {
		// clear the managedBy enables the batch/Job controller to take over
		remoteJob.Spec.ManagedBy = nil
	}

	return remoteClient.Create(ctx, &remoteJob)
}

func (b *multiKueueAdapter) DeleteRemoteObject(ctx context.Context, remoteClient client.Client, key types.NamespacedName) error {
	job := batchv1.Job{}
	job.SetName(key.Name)
	job.SetNamespace(key.Namespace)
	return client.IgnoreNotFound(remoteClient.Delete(ctx, &job, client.PropagationPolicy(metav1.DeletePropagationBackground)))
}

func (b *multiKueueAdapter) IsJobManagedByKueue(ctx context.Context, c client.Client, key types.NamespacedName) (bool, string, error) {
	if !features.Enabled(features.MultiKueueBatchJobWithManagedBy) {
		return true, "", nil
	}

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

	prebuiltWl, hasPrebuiltWorkload := job.Labels[constants.PrebuiltWorkloadLabel]
	if !hasPrebuiltWorkload {
		return nil, fmt.Errorf("no prebuilt workload found for job: %s", klog.KObj(job))
	}

	return []types.NamespacedName{{Name: prebuiltWl, Namespace: job.Namespace}}, nil
}

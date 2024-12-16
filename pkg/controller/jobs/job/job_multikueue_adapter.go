/*
Copyright 2024 The Kubernetes Authors.

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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/api"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
)

type multikueueAdapter struct{}

var _ jobframework.MultiKueueAdapter = (*multikueueAdapter)(nil)

func (b *multikueueAdapter) SyncJob(ctx context.Context, localClient client.Client, remoteClient client.Client, key types.NamespacedName, workloadName, origin string) error {
	log := ctrl.LoggerFrom(ctx)

	localJob := batchv1.Job{}
	err := localClient.Get(ctx, key, &localJob)
	if err != nil {
		return err
	}

	remoteJob := batchv1.Job{}
	err = remoteClient.Get(ctx, key, &remoteJob)
	if client.IgnoreNotFound(err) != nil {
		return err
	}

	// the remote job exists
	if err == nil {
		if fromObject(&localJob).IsSuspended() {
			// Ensure the job is unsuspended before updating its status; otherwise, it will fail when patching the spec.
			log.V(2).Info("Skipping the sync since the local job is still suspended")
			return nil
		}

		if features.Enabled(features.MultiKueueBatchJobWithManagedBy) {
			return clientutil.PatchStatus(ctx, localClient, &localJob, func() (bool, error) {
				localJob.Status = remoteJob.Status
				return true, nil
			})
		}
		remoteFinished := false
		for _, c := range remoteJob.Status.Conditions {
			if (c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed) && c.Status == corev1.ConditionTrue {
				remoteFinished = true
				break
			}
		}
		if remoteFinished {
			return clientutil.PatchStatus(ctx, localClient, &localJob, func() (bool, error) {
				localJob.Status = remoteJob.Status
				return true, nil
			})
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
	for _, cl := range ManagedLabels {
		delete(remoteJob.Spec.Template.Labels, cl)
	}

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

func (b *multikueueAdapter) DeleteRemoteObject(ctx context.Context, remoteClient client.Client, key types.NamespacedName) error {
	job := batchv1.Job{}
	err := remoteClient.Get(ctx, key, &job)
	if err != nil {
		return client.IgnoreNotFound(err)
	}
	return client.IgnoreNotFound(remoteClient.Delete(ctx, &job))
}

func (b *multikueueAdapter) KeepAdmissionCheckPending() bool {
	return !features.Enabled(features.MultiKueueBatchJobWithManagedBy)
}

func (b *multikueueAdapter) IsJobManagedByKueue(ctx context.Context, c client.Client, key types.NamespacedName) (bool, string, error) {
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

func (b *multikueueAdapter) GVK() schema.GroupVersionKind {
	return gvk
}

var _ jobframework.MultiKueueWatcher = (*multikueueAdapter)(nil)

func (*multikueueAdapter) GetEmptyList() client.ObjectList {
	return &batchv1.JobList{}
}

func (*multikueueAdapter) WorkloadKeyFor(o runtime.Object) (types.NamespacedName, error) {
	job, isJob := o.(*batchv1.Job)
	if !isJob {
		return types.NamespacedName{}, errors.New("not a job")
	}

	prebuiltWl, hasPrebuiltWorkload := job.Labels[constants.PrebuiltWorkloadLabel]
	if !hasPrebuiltWorkload {
		return types.NamespacedName{}, fmt.Errorf("no prebuilt workload found for job: %s", klog.KObj(job))
	}

	return types.NamespacedName{Name: prebuiltWl, Namespace: job.Namespace}, nil
}

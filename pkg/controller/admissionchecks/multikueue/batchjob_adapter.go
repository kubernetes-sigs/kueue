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
package multikueue

import (
	"context"
	"errors"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	kueuejob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
)

type batchJobAdapter struct{}

var _ jobAdapter = (*batchJobAdapter)(nil)

func (b *batchJobAdapter) SyncJob(ctx context.Context, localClient client.Client, remoteClient client.Client, key types.NamespacedName, workloadName, origin string) error {
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
		localJob.Status = remoteJob.Status
		return localClient.Status().Update(ctx, &localJob)
	}

	remoteJob = batchv1.Job{
		ObjectMeta: cleanObjectMeta(&localJob.ObjectMeta),
		Spec:       *localJob.Spec.DeepCopy(),
	}

	// cleanup
	// drop the selector
	remoteJob.Spec.Selector = nil
	// drop the templates cleanup labels
	for _, cl := range kueuejob.ManagedLabels {
		delete(remoteJob.Spec.Template.Labels, cl)
	}

	// add the prebuilt workload
	if remoteJob.Labels == nil {
		remoteJob.Labels = map[string]string{}
	}
	remoteJob.Labels[constants.PrebuiltWorkloadLabel] = workloadName
	remoteJob.Labels[kueuealpha.MultiKueueOriginLabel] = origin

	// clear the managedBy enables the batch/Job controller to take over
	remoteJob.Spec.ManagedBy = nil

	return remoteClient.Create(ctx, &remoteJob)
}

func (b *batchJobAdapter) DeleteRemoteObject(ctx context.Context, remoteClient client.Client, key types.NamespacedName) error {
	job := batchv1.Job{}
	err := remoteClient.Get(ctx, key, &job)
	if err != nil {
		return client.IgnoreNotFound(err)
	}
	return client.IgnoreNotFound(remoteClient.Delete(ctx, &job))
}

func (b *batchJobAdapter) IsJobManagedByKueue(ctx context.Context, c client.Client, key types.NamespacedName) (bool, string, error) {
	job := batchv1.Job{}
	err := c.Get(ctx, key, &job)
	if err != nil {
		return false, "", err
	}
	jobControllerName := ptr.Deref(job.Spec.ManagedBy, "")
	if jobControllerName != ControllerName {
		return false, fmt.Sprintf("Expecting spec.managedBy to be %q not %q", ControllerName, jobControllerName), nil
	}
	return true, "", nil
}

var _ multiKueueWatcher = (*batchJobAdapter)(nil)

func (*batchJobAdapter) GetEmptyList() client.ObjectList {
	return &batchv1.JobList{}
}

func (*batchJobAdapter) GetWorkloadKey(o runtime.Object) (types.NamespacedName, error) {
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

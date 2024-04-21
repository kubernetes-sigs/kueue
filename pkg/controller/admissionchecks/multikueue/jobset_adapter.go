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

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
)

type jobsetAdapter struct{}

var _ jobAdapter = (*jobsetAdapter)(nil)

func (b *jobsetAdapter) SyncJob(ctx context.Context, localClient client.Client, remoteClient client.Client, key types.NamespacedName, workloadName, origin string) error {
	localJob := jobset.JobSet{}
	err := localClient.Get(ctx, key, &localJob)
	if err != nil {
		return err
	}

	remoteJob := jobset.JobSet{}
	err = remoteClient.Get(ctx, key, &remoteJob)
	if client.IgnoreNotFound(err) != nil {
		return err
	}

	// if the remote exists, just copy the status
	if err == nil {
		localJob.Status = remoteJob.Status
		return localClient.Status().Update(ctx, &localJob)
	}

	remoteJob = jobset.JobSet{
		ObjectMeta: cleanObjectMeta(&localJob.ObjectMeta),
		Spec:       *localJob.Spec.DeepCopy(),
	}

	// add the prebuilt workload
	if remoteJob.Labels == nil {
		remoteJob.Labels = map[string]string{}
	}
	remoteJob.Labels[constants.PrebuiltWorkloadLabel] = workloadName
	remoteJob.Labels[kueuealpha.MultiKueueOriginLabel] = origin

	// set the manager
	remoteJob.Spec.ManagedBy = ptr.To(jobset.JobSetControllerName)

	return remoteClient.Create(ctx, &remoteJob)
}

func (b *jobsetAdapter) DeleteRemoteObject(ctx context.Context, remoteClient client.Client, key types.NamespacedName) error {
	job := jobset.JobSet{}
	err := remoteClient.Get(ctx, key, &job)
	if err != nil {
		return client.IgnoreNotFound(err)
	}
	return client.IgnoreNotFound(remoteClient.Delete(ctx, &job))
}

func (b *jobsetAdapter) KeepAdmissionCheckPending() bool {
	return false
}

func (b *jobsetAdapter) IsJobManagedByKueue(ctx context.Context, c client.Client, key types.NamespacedName) (bool, string, error) {
	js := jobset.JobSet{}
	err := c.Get(ctx, key, &js)
	if err != nil {
		return false, "", err
	}
	jobsetControllerName := ptr.Deref(js.Spec.ManagedBy, "")
	if jobsetControllerName != ControllerName {
		return false, fmt.Sprintf("Expecting spec.managedBy to be %q not %q", ControllerName, jobsetControllerName), nil
	}
	return true, "", nil
}

var _ multiKueueWatcher = (*jobsetAdapter)(nil)

func (*jobsetAdapter) GetEmptyList() client.ObjectList {
	return &jobset.JobSetList{}
}

func (*jobsetAdapter) GetWorkloadKey(o runtime.Object) (types.NamespacedName, error) {
	jobSet, isJobSet := o.(*jobset.JobSet)
	if !isJobSet {
		return types.NamespacedName{}, errors.New("not a jobset")
	}

	prebuiltWl, hasPrebuiltWorkload := jobSet.Labels[constants.PrebuiltWorkloadLabel]
	if !hasPrebuiltWorkload {
		return types.NamespacedName{}, fmt.Errorf("no prebuilt workload found for jobset: %s", klog.KObj(jobSet))
	}

	return types.NamespacedName{Name: prebuiltWl, Namespace: jobSet.Namespace}, nil
}

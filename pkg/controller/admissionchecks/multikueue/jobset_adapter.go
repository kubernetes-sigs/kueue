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

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	"sigs.k8s.io/kueue/pkg/controller/constants"
)

type jobsetAdapter struct{}

var _ jobAdapter = (*jobsetAdapter)(nil)

func (b *jobsetAdapter) CreateRemoteObject(ctx context.Context, localClient client.Client, remoteClient client.Client, key types.NamespacedName, workloadName string) error {
	localJob := jobset.JobSet{}
	err := localClient.Get(ctx, key, &localJob)
	if err != nil {
		return err
	}

	remoteJob := jobset.JobSet{
		ObjectMeta: cleanObjectMeta(&localJob.ObjectMeta),
		Spec:       *localJob.Spec.DeepCopy(),
	}

	// add the prebuilt workload
	if remoteJob.Labels == nil {
		remoteJob.Labels = map[string]string{}
	}
	remoteJob.Labels[constants.PrebuiltWorkloadLabel] = workloadName

	return remoteClient.Create(ctx, &remoteJob)
}

func (b *jobsetAdapter) CopyStatusRemoteObject(ctx context.Context, localClient client.Client, remoteClient client.Client, key types.NamespacedName) error {
	localJob := jobset.JobSet{}
	err := localClient.Get(ctx, key, &localJob)
	if err != nil {
		return client.IgnoreNotFound(err)
	}

	remoteJob := jobset.JobSet{}
	err = remoteClient.Get(ctx, key, &remoteJob)
	if err != nil {
		return err
	}
	localJob.Status = remoteJob.Status
	return localClient.Status().Update(ctx, &localJob)
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

var _ multiKueueWatcher = (*jobsetAdapter)(nil)

func (*jobsetAdapter) GetEmptyList() client.ObjectList {
	return &jobset.JobSetList{}
}

func (*jobsetAdapter) GetEventsWorkloadKey(o runtime.Object) (types.NamespacedName, error) {
	jobSet, isJobSet := o.(*jobset.JobSet)
	if !isJobSet {
		return types.NamespacedName{}, errors.New("not a jobset")
	}

	prebuiltWl, hasPrebuiltWorkload := jobSet.Labels[constants.PrebuiltWorkloadLabel]
	if !hasPrebuiltWorkload {
		return types.NamespacedName{}, errors.New("no prebuilt workload found")
	}

	return types.NamespacedName{Name: prebuiltWl, Namespace: jobSet.Namespace}, nil
}

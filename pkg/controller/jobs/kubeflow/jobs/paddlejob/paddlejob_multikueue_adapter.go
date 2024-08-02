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

package paddlejob

import (
	"context"
	"errors"
	"fmt"

	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/util/api"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
)

type multikueueAdapter struct{}

var _ jobframework.MultiKueueAdapter = (*multikueueAdapter)(nil)

func (b *multikueueAdapter) SyncJob(ctx context.Context, localClient client.Client, remoteClient client.Client, key types.NamespacedName, workloadName, origin string) error {
	localJob := kftraining.PaddleJob{}
	err := localClient.Get(ctx, key, &localJob)
	if err != nil {
		return err
	}

	remoteJob := &kftraining.PaddleJob{}
	err = remoteClient.Get(ctx, key, remoteJob)
	if client.IgnoreNotFound(err) != nil {
		return err
	}

	// if the remote exists, just copy the status
	if err == nil {
		return clientutil.PatchStatus(ctx, localClient, &localJob, func() (bool, error) {
			localJob.Status = remoteJob.Status
			return true, nil
		})
	}

	remoteJob = &kftraining.PaddleJob{
		ObjectMeta: api.CloneObjectMetaForCreation(&localJob.ObjectMeta),
		Spec:       *localJob.Spec.DeepCopy(),
	}

	// add the prebuilt workload
	if remoteJob.Labels == nil {
		remoteJob.Labels = make(map[string]string, 2)
	}
	remoteJob.Labels[constants.PrebuiltWorkloadLabel] = workloadName
	remoteJob.Labels[kueuealpha.MultiKueueOriginLabel] = origin

	return remoteClient.Create(ctx, remoteJob)
}

func (b *multikueueAdapter) DeleteRemoteObject(ctx context.Context, remoteClient client.Client, key types.NamespacedName) error {
	job := kftraining.PaddleJob{}
	err := remoteClient.Get(ctx, key, &job)
	if err != nil {
		return client.IgnoreNotFound(err)
	}
	return client.IgnoreNotFound(remoteClient.Delete(ctx, &job))
}

func (b *multikueueAdapter) KeepAdmissionCheckPending() bool {
	return false
}

func (b *multikueueAdapter) IsJobManagedByKueue(context.Context, client.Client, types.NamespacedName) (bool, string, error) {
	return true, "", nil
}

func (b *multikueueAdapter) GVK() schema.GroupVersionKind {
	return gvk
}

var _ jobframework.MultiKueueWatcher = (*multikueueAdapter)(nil)

func (*multikueueAdapter) GetEmptyList() client.ObjectList {
	return &kftraining.PaddleJobList{}
}

func (*multikueueAdapter) WorkloadKeyFor(o runtime.Object) (types.NamespacedName, error) {
	paddleJob, isPaddleJob := o.(*kftraining.PaddleJob)
	if !isPaddleJob {
		return types.NamespacedName{}, errors.New("not a PaddleJob")
	}

	prebuiltWl, hasPrebuiltWorkload := paddleJob.Labels[constants.PrebuiltWorkloadLabel]
	if !hasPrebuiltWorkload {
		return types.NamespacedName{}, fmt.Errorf("no prebuilt workload found for PaddleJob: %s", klog.KObj(paddleJob))
	}

	return types.NamespacedName{Name: prebuiltWl, Namespace: paddleJob.Namespace}, nil
}

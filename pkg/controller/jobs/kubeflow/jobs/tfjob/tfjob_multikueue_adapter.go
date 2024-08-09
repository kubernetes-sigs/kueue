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

package tfjob

import (
	"context"

	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	kfcommon "sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/common"
	"sigs.k8s.io/kueue/pkg/util/api"
)

type multikueueAdapter struct{}

var _ jobframework.MultiKueueAdapter = (*multikueueAdapter)(nil)
var _ jobframework.UpdateRemoteJob = (*multikueueAdapter)(nil)

func (b *multikueueAdapter) UpdateRemoteJobStatus(localJob, remoteJob interface{}) {
	localJob.(*kftraining.TFJob).Status = remoteJob.(*kftraining.TFJob).Status
}

func (b *multikueueAdapter) UpdateRemoteJobSpec(localJob, remoteJob interface{}) {
	*remoteJob.(*kftraining.TFJob) = kftraining.TFJob{
		ObjectMeta: api.CloneObjectMetaForCreation(&localJob.(*kftraining.TFJob).ObjectMeta),
		Spec:       *localJob.(*kftraining.TFJob).Spec.DeepCopy(),
	}
}

func (b *multikueueAdapter) SyncJob(ctx context.Context, localClient client.Client, remoteClient client.Client, key types.NamespacedName, workloadName, origin string) error {
	return kfcommon.SyncJob[*kftraining.TFJob](ctx, localClient, remoteClient, key, workloadName, origin, b)
}

func (b *multikueueAdapter) DeleteRemoteObject(ctx context.Context, remoteClient client.Client, key types.NamespacedName) error {
	return kfcommon.DeleteRemoteObject[*kftraining.TFJob](ctx, remoteClient, key)
}

func (b *multikueueAdapter) KeepAdmissionCheckPending() bool {
	return false
}

func (b *multikueueAdapter) IsJobManagedByKueue(ctx context.Context, c client.Client, key types.NamespacedName) (bool, string, error) {
	return true, "", nil
}

func (b *multikueueAdapter) GVK() schema.GroupVersionKind {
	return gvk
}

var _ jobframework.MultiKueueWatcher = (*multikueueAdapter)(nil)

func (*multikueueAdapter) GetEmptyList() client.ObjectList {
	return &kftraining.TFJobList{}
}

func (*multikueueAdapter) WorkloadKeyFor(o runtime.Object) (types.NamespacedName, error) {
	return kfcommon.WorkloadKeyFor[*kftraining.TFJob](o, kftraining.TFJobKind)
}

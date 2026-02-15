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

package rayservice

import (
	"context"
	"errors"
	"fmt"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/util/api"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
)

type multiKueueAdapter struct{}

var _ jobframework.MultiKueueAdapter = (*multiKueueAdapter)(nil)

func (b *multiKueueAdapter) SyncJob(ctx context.Context, localClient client.Client, remoteClient client.Client, key types.NamespacedName, workloadName, origin string) error {
	localJob := rayv1.RayService{}
	err := localClient.Get(ctx, key, &localJob)
	if err != nil {
		return err
	}

	remoteJob := rayv1.RayService{}
	err = remoteClient.Get(ctx, key, &remoteJob)
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

	remoteJob = rayv1.RayService{
		ObjectMeta: api.CloneObjectMetaForCreation(&localJob.ObjectMeta),
		Spec:       *localJob.Spec.DeepCopy(),
	}

	// add the prebuilt workload
	if remoteJob.Labels == nil {
		remoteJob.Labels = make(map[string]string, 2)
	}
	remoteJob.Labels[constants.PrebuiltWorkloadLabel] = workloadName
	remoteJob.Labels[kueue.MultiKueueOriginLabel] = origin

	return remoteClient.Create(ctx, &remoteJob)
}

func (b *multiKueueAdapter) DeleteRemoteObject(ctx context.Context, remoteClient client.Client, key types.NamespacedName) error {
	job := rayv1.RayService{}
	job.SetName(key.Name)
	job.SetNamespace(key.Namespace)
	return client.IgnoreNotFound(remoteClient.Delete(ctx, &job))
}

func (b *multiKueueAdapter) IsJobManagedByKueue(ctx context.Context, c client.Client, key types.NamespacedName) (bool, string, error) {
	job := rayv1.RayService{}
	err := c.Get(ctx, key, &job)
	if err != nil {
		return false, "", err
	}
	// RayService doesn't have spec.managedBy field, so we check for Kueue labels
	if jobframework.QueueName((*RayService)(&job)) != "" {
		return true, "", nil
	}
	return false, "RayService is not managed by Kueue (missing queue label)", nil
}

func (b *multiKueueAdapter) GVK() schema.GroupVersionKind {
	return gvk
}

var _ jobframework.MultiKueueWatcher = (*multiKueueAdapter)(nil)

func (*multiKueueAdapter) GetEmptyList() client.ObjectList {
	return &rayv1.RayServiceList{}
}

func (*multiKueueAdapter) WorkloadKeysFor(o runtime.Object) ([]types.NamespacedName, error) {
	job, isJob := o.(*rayv1.RayService)
	if !isJob {
		return nil, errors.New("not a rayservice")
	}

	prebuiltWl, hasPrebuiltWorkload := job.Labels[constants.PrebuiltWorkloadLabel]
	if !hasPrebuiltWorkload {
		return nil, fmt.Errorf("no prebuilt workload found for rayservice: %s", klog.KObj(job))
	}

	return []types.NamespacedName{{Name: prebuiltWl, Namespace: job.Namespace}}, nil
}

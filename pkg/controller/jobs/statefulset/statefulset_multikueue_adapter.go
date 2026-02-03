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

package statefulset

import (
	"context"
	"errors"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
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
	localStatefulSet := appsv1.StatefulSet{}
	err := localClient.Get(ctx, key, &localStatefulSet)
	if err != nil {
		return err
	}

	remoteStatefulSet := appsv1.StatefulSet{}
	err = remoteClient.Get(ctx, key, &remoteStatefulSet)
	if client.IgnoreNotFound(err) != nil {
		return err
	}

	// if the remote exists, just copy the status
	if err == nil {
		return clientutil.PatchStatus(ctx, localClient, &localStatefulSet, func() (bool, error) {
			localStatefulSet.Status = remoteStatefulSet.Status
			return true, nil
		})
	}

	remoteStatefulSet = appsv1.StatefulSet{
		ObjectMeta: api.CloneObjectMetaForCreation(&localStatefulSet.ObjectMeta),
		Spec:       *localStatefulSet.Spec.DeepCopy(),
	}

	// add the prebuilt workload
	if remoteStatefulSet.Labels == nil {
		remoteStatefulSet.Labels = map[string]string{}
	}
	remoteStatefulSet.Labels[constants.PrebuiltWorkloadLabel] = workloadName
	remoteStatefulSet.Labels[kueue.MultiKueueOriginLabel] = origin

	return remoteClient.Create(ctx, &remoteStatefulSet)
}

func (b *multiKueueAdapter) DeleteRemoteObject(ctx context.Context, remoteClient client.Client, key types.NamespacedName) error {
	ss := appsv1.StatefulSet{}
	ss.SetName(key.Name)
	ss.SetNamespace(key.Namespace)
	return client.IgnoreNotFound(remoteClient.Delete(ctx, &ss))
}

func (b *multiKueueAdapter) IsJobManagedByKueue(ctx context.Context, c client.Client, key types.NamespacedName) (bool, string, error) {
	return true, "", nil
}

func (b *multiKueueAdapter) GVK() schema.GroupVersionKind {
	return gvk
}

var _ jobframework.MultiKueueWatcher = (*multiKueueAdapter)(nil)

func (*multiKueueAdapter) GetEmptyList() client.ObjectList {
	return &appsv1.StatefulSetList{}
}

func (*multiKueueAdapter) WorkloadKeysFor(o runtime.Object) ([]types.NamespacedName, error) {
	statefulSet, isStatefulSet := o.(*appsv1.StatefulSet)
	if !isStatefulSet {
		return nil, errors.New("not a statefulset")
	}

	prebuiltWl, hasPrebuiltWorkload := statefulSet.Labels[constants.PrebuiltWorkloadLabel]
	if !hasPrebuiltWorkload {
		return nil, fmt.Errorf("no prebuilt workload found for statefulset: %s", klog.KObj(statefulSet))
	}

	return []types.NamespacedName{{Name: prebuiltWl, Namespace: statefulSet.Namespace}}, nil
}

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

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/util/api"
)

type multiKueueAdapter struct{}

var _ jobframework.MultiKueueAdapter = (*multiKueueAdapter)(nil)

func (b *multiKueueAdapter) SyncJob(ctx context.Context, localClient client.Client, remoteClient client.Client, key types.NamespacedName, workloadName, origin string) (bool, error) {
	localStatefulSet := appsv1.StatefulSet{}
	err := localClient.Get(ctx, key, &localStatefulSet)
	if err != nil {
		return false, err
	}

	remoteStatefulSet := appsv1.StatefulSet{}
	err = remoteClient.Get(ctx, key, &remoteStatefulSet)
	if client.IgnoreNotFound(err) != nil {
		return false, err
	}

	// If the remote exists, skip status sync.
	// StatefulSet doesn't support managedBy, so the local StatefulSet controller
	// would immediately overwrite any status we sync from the worker cluster.
	if err == nil {
		return false, nil
	}

	remoteStatefulSet = appsv1.StatefulSet{
		ObjectMeta: api.CloneObjectMetaForCreation(&localStatefulSet.ObjectMeta),
		Spec:       *localStatefulSet.Spec.DeepCopy(),
	}

	// Add prebuilt workload name and multikueue origin
	jobframework.SetMultiKueueMeta(&remoteStatefulSet, workloadName, origin)

	if remoteStatefulSet.Annotations == nil {
		remoteStatefulSet.Annotations = make(map[string]string, 1)
	}
	remoteStatefulSet.Annotations[kueue.MultiKueueOriginUIDAnnotation] = string(localStatefulSet.UID)

	return false, remoteClient.Create(ctx, &remoteStatefulSet)
}

func (b *multiKueueAdapter) DeleteRemoteObject(ctx context.Context, _ client.Client, remoteClient client.Client, key types.NamespacedName) error {
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

	prebuiltWorkload := jobframework.PrebuiltWorkloadNameFor(statefulSet)
	if prebuiltWorkload == "" {
		prebuiltWorkload = jobframework.GetWorkloadNameForOwnerWithGVK(statefulSet.GetName(), statefulSet.GetUID(), gvk)
	}

	return []types.NamespacedName{{Name: prebuiltWorkload, Namespace: statefulSet.Namespace}}, nil
}

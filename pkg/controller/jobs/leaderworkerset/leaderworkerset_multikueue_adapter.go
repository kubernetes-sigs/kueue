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

package leaderworkerset

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/util/api"
)

type multiKueueAdapter struct{}

var _ jobframework.MultiKueueAdapter = (*multiKueueAdapter)(nil)
var _ jobframework.MultiKueueWatcher = (*multiKueueAdapter)(nil)
var _ jobframework.MultiKueueMultiWorkloadAdapter = (*multiKueueAdapter)(nil)

func (b *multiKueueAdapter) SyncJob(ctx context.Context, localClient client.Client, remoteClient client.Client, key types.NamespacedName, workloadName, origin string) error {
	localLWS := leaderworkersetv1.LeaderWorkerSet{}
	err := localClient.Get(ctx, key, &localLWS)
	if err != nil {
		return err
	}

	remoteLWS := leaderworkersetv1.LeaderWorkerSet{}
	err = remoteClient.Get(ctx, key, &remoteLWS)
	if client.IgnoreNotFound(err) != nil {
		return err
	}

	// If the remote exists, skip status sync.
	// LWS doesn't support managedBy, so the local LWS controller would
	// immediately overwrite any status we sync from the worker cluster.
	if err == nil {
		return nil
	}

	remoteLWS = leaderworkersetv1.LeaderWorkerSet{
		ObjectMeta: api.CloneObjectMetaForCreation(&localLWS.ObjectMeta),
		Spec:       *localLWS.Spec.DeepCopy(),
	}

	if remoteLWS.Labels == nil {
		remoteLWS.Labels = map[string]string{}
	}
	if remoteLWS.Annotations == nil {
		remoteLWS.Annotations = map[string]string{}
	}
	remoteLWS.Labels[kueue.MultiKueueOriginLabel] = origin
	remoteLWS.Annotations[kueue.MultiKueueOriginUIDAnnotation] = string(localLWS.UID)

	// Use IgnoreAlreadyExists because LWS has multiple workloads (one per replica),
	// and they may all try to create the same LWS concurrently.
	return client.IgnoreAlreadyExists(remoteClient.Create(ctx, &remoteLWS))
}

func (b *multiKueueAdapter) DeleteRemoteObject(ctx context.Context, remoteClient client.Client, key types.NamespacedName) error {
	lws := leaderworkersetv1.LeaderWorkerSet{}
	lws.SetName(key.Name)
	lws.SetNamespace(key.Namespace)
	return client.IgnoreNotFound(remoteClient.Delete(ctx, &lws))
}

func (b *multiKueueAdapter) IsJobManagedByKueue(ctx context.Context, c client.Client, key types.NamespacedName) (bool, string, error) {
	return true, "", nil
}

func (b *multiKueueAdapter) GVK() schema.GroupVersionKind {
	return gvk
}

func (*multiKueueAdapter) GetEmptyList() client.ObjectList {
	return &leaderworkersetv1.LeaderWorkerSetList{}
}

func (*multiKueueAdapter) WorkloadKeysFor(o runtime.Object) ([]types.NamespacedName, error) {
	lws, ok := o.(*leaderworkersetv1.LeaderWorkerSet)
	if !ok {
		return nil, errors.New("not a leaderworkerset")
	}

	originUID, hasOriginUID := lws.Annotations[kueue.MultiKueueOriginUIDAnnotation]
	if !hasOriginUID {
		return nil, fmt.Errorf("no origin UID annotation found for leaderworkerset: %s", klog.KObj(lws))
	}

	replicas := ptr.Deref(lws.Spec.Replicas, 1)
	keys := make([]types.NamespacedName, replicas)
	for i := range replicas {
		keys[i] = types.NamespacedName{
			Name:      GetWorkloadName(types.UID(originUID), lws.Name, strconv.Itoa(int(i))),
			Namespace: lws.Namespace,
		}
	}
	return keys, nil
}

func (*multiKueueAdapter) GetExpectedWorkloadCount(ctx context.Context, c client.Client, key types.NamespacedName) (int, error) {
	lws := &leaderworkersetv1.LeaderWorkerSet{}
	if err := c.Get(ctx, key, lws); err != nil {
		return 0, err
	}
	return int(ptr.Deref(lws.Spec.Replicas, 1)), nil
}

func (*multiKueueAdapter) GetWorkloadIndex(wl *kueue.Workload) int {
	if wl == nil || wl.Annotations == nil {
		return -1
	}
	indexStr, ok := wl.Annotations[constants.ComponentWorkloadIndexAnnotation]
	if !ok {
		return -1
	}
	index, err := strconv.Atoi(indexStr)
	if err != nil {
		return -1
	}
	return index
}

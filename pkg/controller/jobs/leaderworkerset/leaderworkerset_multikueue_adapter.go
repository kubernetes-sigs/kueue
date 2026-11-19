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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/api"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
	utiltolerations "sigs.k8s.io/kueue/pkg/util/tolerations"
)

type multiKueueAdapter struct{}

var _ jobframework.MultiKueueAdapter = (*multiKueueAdapter)(nil)
var _ jobframework.MultiKueueWatcher = (*multiKueueAdapter)(nil)
var _ jobframework.MultiKueueMultiWorkloadAdapter = (*multiKueueAdapter)(nil)
var _ jobframework.MultiKueueLocalJobWatcher = (*multiKueueAdapter)(nil)

func (b *multiKueueAdapter) SyncJob(ctx context.Context, localClient client.Client, remoteClient client.Client, key types.NamespacedName, workloadName, origin string) (bool, error) {
	localLWS := leaderworkersetv1.LeaderWorkerSet{}
	err := localClient.Get(ctx, key, &localLWS)
	if err != nil {
		return false, err
	}

	remoteLWS := leaderworkersetv1.LeaderWorkerSet{}
	err = remoteClient.Get(ctx, key, &remoteLWS)
	if client.IgnoreNotFound(err) != nil {
		return false, err
	}

	// If the remote exists, skip status sync.
	// LWS doesn't support managedBy, so the local LWS controller would
	// immediately overwrite any status we sync from the worker cluster.
	if err == nil {
		// The remote LWS is kept while other groups still run on the worker,
		// so a scale down must shrink it to stop the removed groups there.
		localReplicas := ptr.Deref(localLWS.Spec.Replicas, defaultLeaderWorkerSetReplicas)
		if err := clientutil.Patch(ctx, remoteClient, &remoteLWS, func() (bool, error) {
			if localReplicas >= ptr.Deref(remoteLWS.Spec.Replicas, defaultLeaderWorkerSetReplicas) {
				return false, nil
			}
			remoteLWS.Spec.Replicas = new(localReplicas)
			return true, nil
		}); err != nil {
			return false, err
		}

		// Added tolerations and changed containers must reach the pods that the
		// remote LWS creates.
		if features.Enabled(features.AllowAddingPodTemplateTolerations) {
			if err := patchRemoteSpec(ctx, remoteClient, &remoteLWS, "tolerations", func() bool {
				return copyAddedTolerations(&remoteLWS, &localLWS)
			}); err != nil {
				return false, err
			}
		}
		if features.Enabled(features.AllowMutatingContainersWhenEffectiveRequestsDontChange) && features.Enabled(features.MultiKueueRemoteSpecSync) {
			// The manager admits changed containers with any partition, so the
			// partition is copied to let the worker update the same groups. It is
			// patched with the containers, so that the worker never has the new
			// containers with the previous, lower partition and updates the groups
			// that the manager keeps. It is also patched on its own, so that
			// lowering it after a change of the containers updates the remaining
			// groups on the worker too, even when the worker rejects the containers.
			if err := patchRemoteSpec(ctx, remoteClient, &remoteLWS, "containers", func() bool {
				if !copyContainers(&remoteLWS, &localLWS) {
					return false
				}
				copyPartition(&remoteLWS, &localLWS)
				return true
			}); err != nil {
				return false, err
			}
			if err := patchRemoteSpec(ctx, remoteClient, &remoteLWS, "partition", func() bool {
				return copyPartition(&remoteLWS, &localLWS)
			}); err != nil {
				return false, err
			}
		}

		return false, nil
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
	return false, client.IgnoreAlreadyExists(remoteClient.Create(ctx, &remoteLWS))
}

// patchRemoteSpec patches the remote LeaderWorkerSet with the change that copyFn
// makes to its spec. Each change is patched separately, so that a rejection does
// not block the scale down or the other changes.
func patchRemoteSpec(ctx context.Context, remoteClient client.Client, remoteLWS *leaderworkersetv1.LeaderWorkerSet, field string, copyFn func() bool) error {
	beforePatch := remoteLWS.DeepCopy()
	err := clientutil.Patch(ctx, remoteClient, remoteLWS, func() (bool, error) {
		return copyFn(), nil
	})
	if apierrors.IsForbidden(err) {
		// A Forbidden error is permanent: the worker rejects the change, for
		// example when it runs an older Kueue or has the feature gate disabled,
		// or denies the patch by RBAC or a policy. Retrying cannot fix it, and
		// returning the error would keep MultiKueue from finishing or evicting
		// the Workload, so the error is only logged.
		ctrl.LoggerFrom(ctx).Error(err, "Worker rejected the spec change of the remote LeaderWorkerSet", "leaderWorkerSet", klog.KObj(remoteLWS), "field", field)
		// The rejected change stays on remoteLWS. Restore remoteLWS, so that the
		// next patch is computed against the remote state.
		*remoteLWS = *beforePatch
		return nil
	}
	return err
}

// copyContainers copies the containers and the init containers of the local
// LeaderWorkerSet to the remote one, and reports whether they changed. The
// manager admits only the changes that keep the effective requests of the pods
// and add no host ports.
func copyContainers(remote, local *leaderworkersetv1.LeaderWorkerSet) bool {
	var updated bool
	localTemplates := podTemplates(local)
	for name, remoteTemplate := range podTemplates(remote) {
		localTemplate, found := localTemplates[name]
		if !found || sameContainers(&remoteTemplate.Spec, &localTemplate.Spec) {
			continue
		}
		localSpec := localTemplate.Spec.DeepCopy()
		remoteTemplate.Spec.InitContainers = localSpec.InitContainers
		remoteTemplate.Spec.Containers = localSpec.Containers
		updated = true
	}
	return updated
}

// copyAddedTolerations copies the tolerations added to the local LeaderWorkerSet
// to the remote one, and reports whether the local one added any.
func copyAddedTolerations(remote, local *leaderworkersetv1.LeaderWorkerSet) bool {
	if !utiltolerations.CopyAddedByName(podTemplates(remote), podTemplates(local)) {
		return false
	}
	// The manager admits added tolerations only with its partition at 0, so the
	// partition is copied with them to let the worker admit the tolerations and
	// update all groups as well.
	copyPartition(remote, local)
	return true
}

// copyPartition copies the rolling update partition of the local LeaderWorkerSet
// to the remote one, which otherwise keeps the partition it was created with,
// and reports whether it changed.
func copyPartition(remote, local *leaderworkersetv1.LeaderWorkerSet) bool {
	cfg := remote.Spec.RolloutStrategy.RollingUpdateConfiguration
	if cfg == nil {
		return false
	}
	partition := rollingUpdatePartition((*LeaderWorkerSet)(local))
	if ptr.Deref(cfg.Partition, 0) == partition {
		return false
	}
	cfg.Partition = new(partition)
	return true
}

func (b *multiKueueAdapter) DeleteRemoteObject(ctx context.Context, _ client.Client, remoteClient client.Client, key types.NamespacedName) error {
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

// NewEmptyLocalJob lets the MultiKueue controller watch the manager
// LeaderWorkerSet when the MultiKueueRemoteSpecSync feature gate is enabled, so
// that changed containers promptly reach the worker. Unlike added tolerations or
// a scale down, they do not change the Workloads, which would otherwise trigger
// the sync.
func (*multiKueueAdapter) NewEmptyLocalJob() client.Object {
	if !features.Enabled(features.AllowMutatingContainersWhenEffectiveRequestsDontChange) {
		return nil
	}
	return &leaderworkersetv1.LeaderWorkerSet{}
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

	replicas := ptr.Deref(lws.Spec.Replicas, defaultLeaderWorkerSetReplicas)
	if err := validateLeaderWorkerSetReplicaCount(replicas); err != nil {
		return nil, err
	}
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
	replicas := ptr.Deref(lws.Spec.Replicas, defaultLeaderWorkerSetReplicas)
	if err := validateLeaderWorkerSetReplicaCount(replicas); err != nil {
		return 0, err
	}
	return int(replicas), nil
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

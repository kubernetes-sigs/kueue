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

package pod

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/util/api"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
)

type multiKueueAdapter struct{}

var _ jobframework.MultiKueueAdapter = (*multiKueueAdapter)(nil)

func (b *multiKueueAdapter) SyncJob(ctx context.Context, localClient client.Client, remoteClient client.Client, key types.NamespacedName, workloadName, origin string) error {
	log := ctrl.LoggerFrom(ctx)

	localPod := corev1.Pod{}
	err := localClient.Get(ctx, key, &localPod)
	if err != nil {
		return err
	}

	if !isPodAPartOfGroup(localPod) {
		return syncLocalPodWithRemote(ctx, localClient, remoteClient, &localPod, workloadName, origin, &log)
	}

	groupName := podGroupName(localPod)
	return syncPodGroup(ctx, localClient, remoteClient, key, workloadName, origin, groupName)
}

func (b *multiKueueAdapter) DeleteRemoteObject(ctx context.Context, remoteClient client.Client, key types.NamespacedName) error {
	pod := corev1.Pod{}
	err := remoteClient.Get(ctx, key, &pod)
	if err != nil {
		return client.IgnoreNotFound(err)
	}

	if !isPodAPartOfGroup(pod) {
		return client.IgnoreNotFound(remoteClient.Delete(ctx, &pod))
	}

	groupName := podGroupName(pod)
	podGroup := &corev1.PodList{}
	err = remoteClient.List(ctx, podGroup, client.InNamespace(key.Namespace), client.MatchingLabels{podconstants.GroupNameLabel: groupName})
	if err != nil {
		return err
	}

	for _, remotePod := range podGroup.Items {
		if err = client.IgnoreNotFound(remoteClient.Delete(ctx, &remotePod)); err != nil {
			return err
		}
	}

	return nil
}

func (b *multiKueueAdapter) KeepAdmissionCheckPending() bool {
	return true
}

func (b *multiKueueAdapter) IsJobManagedByKueue(ctx context.Context, c client.Client, key types.NamespacedName) (bool, string, error) {
	return true, "", nil
}

func (b *multiKueueAdapter) GVK() schema.GroupVersionKind {
	return gvk
}

var _ jobframework.MultiKueueWatcher = (*multiKueueAdapter)(nil)

func (*multiKueueAdapter) GetEmptyList() client.ObjectList {
	return &corev1.PodList{}
}

func (*multiKueueAdapter) WorkloadKeyFor(o runtime.Object) (types.NamespacedName, error) {
	pod, isPod := o.(*corev1.Pod)
	if !isPod {
		return types.NamespacedName{}, errors.New("not a pod")
	}

	prebuiltWl, hasPrebuiltWorkload := pod.Labels[constants.PrebuiltWorkloadLabel]
	if !hasPrebuiltWorkload {
		return types.NamespacedName{}, fmt.Errorf("no prebuilt workload found for pod: %s", klog.KObj(pod))
	}

	return types.NamespacedName{Name: prebuiltWl, Namespace: pod.Namespace}, nil
}

// isPodAPartOfGroup checks if a pod belongs to a group by verifying the presence of a group name label.
func isPodAPartOfGroup(p corev1.Pod) bool {
	return podGroupName(p) != ""
}

func syncPodGroup(ctx context.Context, localClient client.Client, remoteClient client.Client, key types.NamespacedName, workloadName, origin, groupName string) error {
	log := ctrl.LoggerFrom(ctx)

	localPodGroup := &corev1.PodList{}
	err := localClient.List(ctx, localPodGroup, client.InNamespace(key.Namespace), client.MatchingLabels{podconstants.GroupNameLabel: groupName})
	if err != nil {
		return err
	}

	for _, localPod := range localPodGroup.Items {
		if err = syncLocalPodWithRemote(ctx, localClient, remoteClient, &localPod, workloadName, origin, &log); err != nil {
			return err
		}
	}

	return nil
}

func syncLocalPodWithRemote(
	ctx context.Context,
	localClient client.Client,
	remoteClient client.Client,
	localPod *corev1.Pod,
	workloadName, origin string,
	log *logr.Logger,
) error {
	key := types.NamespacedName{Name: localPod.Name, Namespace: localPod.Namespace}
	remotePod := corev1.Pod{}

	// Try to fetch the corresponding remote pod
	err := remoteClient.Get(ctx, key, &remotePod)
	if client.IgnoreNotFound(err) != nil {
		return err
	}

	// If the remote pod exists
	if err == nil {
		// Skip syncing if the local pod is terminating
		if localPod.DeletionTimestamp != nil {
			log.V(2).Info("Skipping sync since the local pod is terminating", "podName", localPod.Name)
			return nil
		}

		// Patch the status of the local pod to match the remote pod
		return clientutil.PatchStatus(ctx, localClient, localPod, func() (bool, error) {
			localPod.Status = remotePod.Status
			return true, nil
		})
	}

	// If the remote pod does not exist, create it
	remotePod = corev1.Pod{
		ObjectMeta: api.CloneObjectMetaForCreation(&localPod.ObjectMeta),
		Spec:       *localPod.Spec.DeepCopy(),
	}

	// add the prebuilt workload
	if remotePod.Labels == nil {
		remotePod.Labels = map[string]string{}
	}
	remotePod.Labels[constants.PrebuiltWorkloadLabel] = workloadName
	remotePod.Labels[kueue.MultiKueueOriginLabel] = origin

	if err = remoteClient.Create(ctx, &remotePod); err != nil {
		log.Error(err, "Failed to create remote pod", "podName", remotePod.Name)
		return err
	}

	return nil
}

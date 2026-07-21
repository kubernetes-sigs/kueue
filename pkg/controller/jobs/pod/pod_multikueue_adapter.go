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

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/util/api"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
)

type multiKueueAdapter struct{}

var _ jobframework.MultiKueueAdapter = (*multiKueueAdapter)(nil)

func (b *multiKueueAdapter) SyncJob(ctx context.Context, localClient client.Client, remoteClient client.Client, key types.NamespacedName, workloadName, origin string) (bool, error) {
	log := ctrl.LoggerFrom(ctx)

	localPod := corev1.Pod{}
	err := localClient.Get(ctx, key, &localPod)
	if err != nil {
		return false, err
	}

	groupName := utilpod.GetPodGroupName(&localPod)
	if groupName == "" {
		return false, syncLocalPodWithRemote(ctx, localClient, remoteClient, &localPod, workloadName, origin, &log)
	}

	return false, syncPodGroup(ctx, localClient, remoteClient, key, workloadName, origin, groupName)
}

func (b *multiKueueAdapter) DeleteRemoteObject(ctx context.Context, localClient client.Client, remoteClient client.Client, key types.NamespacedName) error {
	pod := corev1.Pod{}
	err := remoteClient.Get(ctx, key, &pod)
	if err != nil {
		return client.IgnoreNotFound(err)
	}

	groupName := utilpod.GetPodGroupName(&pod)
	if groupName == "" {
		return client.IgnoreNotFound(remoteClient.Delete(ctx, &pod))
	}

	localPodGroup, err := listLocalPods(ctx, localClient, key.Namespace, groupName)
	if err != nil {
		return err
	}

	for _, localPod := range localPodGroup.Items {
		if err = client.IgnoreNotFound(remoteClient.Delete(ctx, &localPod)); err != nil {
			return err
		}
	}

	return nil
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

func (*multiKueueAdapter) WorkloadKeysFor(o runtime.Object) ([]types.NamespacedName, error) {
	pod, isPod := o.(*corev1.Pod)
	if !isPod {
		return nil, errors.New("not a pod")
	}

	prebuiltWorkload := jobframework.PrebuiltWorkloadNameFor(pod)
	if prebuiltWorkload == "" {
		if utilpod.IsPodGroup(pod) {
			prebuiltWorkload = utilpod.GetPodGroupName(pod)
		} else {
			prebuiltWorkload = jobframework.GetWorkloadNameForOwnerWithGVK(pod.GetName(), pod.GetUID(), gvk)
		}
	}

	return []types.NamespacedName{{Name: prebuiltWorkload, Namespace: pod.Namespace}}, nil
}

func syncPodGroup(ctx context.Context, localClient client.Client, remoteClient client.Client, key types.NamespacedName, workloadName, origin, groupName string) error {
	log := ctrl.LoggerFrom(ctx)

	localPodGroup, err := listLocalPods(ctx, localClient, key.Namespace, groupName)
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

func listLocalPods(ctx context.Context, localClient client.Client, namespace, groupName string) (*corev1.PodList, error) {
	pods := &corev1.PodList{}
	if err := localClient.List(ctx, pods, client.InNamespace(namespace), client.MatchingFields{PodGroupNameCacheKey: groupName}); err != nil {
		return nil, err
	}
	return pods, nil
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
		if !localPod.DeletionTimestamp.IsZero() {
			log.V(2).Info("Skipping sync since the local pod is terminating", "podName", localPod.Name)
			return nil
		}

		// Patch the status of the local pod to match the remote pod
		return clientutil.PatchStatus(ctx, localClient, localPod, func() (bool, error) {
			// While the local (management-cluster) Pod is gated it can never be
			// scheduled here: MultiKueue runs it on the worker cluster. The local
			// pod keeps its PodScheduled condition at False/SchedulingGated,
			// which the cluster-autoscaler correctly ignores. Copying the remote
			// PodScheduled=False/Unschedulable condition verbatim would make the
			// management cluster's autoscaler treat the gated Pod as a regular
			// unschedulable Pod and trigger a spurious scale-up. Preserve the local
			// condition while the worker Pod is not yet scheduled; once the worker Pod
			// reports PodScheduled=True the remote condition is synced through so the
			// manager Pod stops showing SchedulingGated while its phase is Running.
			// Everything else (phase, container statuses, IPs) is always synced.
			//
			// The cluster-autoscaler classifies a Pod as Unschedulable (a scale-up
			// candidate) purely from PodScheduled=False/reason=Unschedulable, and
			// handles reason=SchedulingGated separately (ignored). It never consults
			// spec.schedulingGates, so the synced reason is what matters. See
			// ArrangePodsBySchedulability / isSchedulingGated in cluster-autoscaler:
			// https://github.com/kubernetes/autoscaler/blob/94dcda068/cluster-autoscaler/utils/kubernetes/listers.go#L180-L236
			localScheduled := findPodCondition(localPod.Status.Conditions, corev1.PodScheduled)
			remoteScheduled := findPodCondition(remotePod.Status.Conditions, corev1.PodScheduled)
			localPod.Status = remotePod.Status
			// Keep the local SchedulingGated condition only while the worker Pod has
			// not been scheduled yet (PodScheduled != True). Once it is scheduled, the
			// remote PodScheduled=True condition (already copied above) is left in place.
			remoteIsScheduled := remoteScheduled != nil && remoteScheduled.Status == corev1.ConditionTrue
			if isGated(localPod) && localScheduled != nil && !remoteIsScheduled {
				setPodCondition(&localPod.Status, *localScheduled)
			}
			return true, nil
		})
	}

	// If the remote pod does not exist, create it
	remotePod = corev1.Pod{
		ObjectMeta: api.CloneObjectMetaForCreation(&localPod.ObjectMeta),
		Spec:       *localPod.Spec.DeepCopy(),
	}

	// Add prebuilt workload name and multikueue origin
	jobframework.SetMultiKueueMeta(&remotePod, workloadName, origin)

	if err = remoteClient.Create(ctx, &remotePod); err != nil {
		log.Error(err, "Failed to create remote pod", "podName", remotePod.Name)
		return err
	}

	return nil
}

// findPodCondition returns a pointer to the condition of the given type, or nil
// if the Pod has no such condition.
func findPodCondition(conditions []corev1.PodCondition, condType corev1.PodConditionType) *corev1.PodCondition {
	for i := range conditions {
		if conditions[i].Type == condType {
			return &conditions[i]
		}
	}
	return nil
}

// setPodCondition replaces the condition of the same type in the status, or
// appends it if no condition of that type is present.
func setPodCondition(status *corev1.PodStatus, condition corev1.PodCondition) {
	for i := range status.Conditions {
		if status.Conditions[i].Type == condition.Type {
			status.Conditions[i] = condition
			return
		}
	}
	status.Conditions = append(status.Conditions, condition)
}

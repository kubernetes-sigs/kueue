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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/util/api"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
)

const (
	// RemotePodLostReason is the reason set on a manager Pod that MultiKueue failed
	// because its worker Pod disappeared after it started.
	RemotePodLostReason = "MultiKueueRemotePodLost"
)

type multiKueueAdapter struct {
	clock clock.Clock
}

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
		// Creating the remote pod again for a local pod that is final, or that
		// already started on the worker, would silently restart the job. The
		// workload reconciler decides whether a started pod was lost, since the
		// remote pod is also removed when its workload is evicted.
		handleMissingRemote := func(pod *corev1.Pod) (bool, error) {
			if isTerminatedOrTerminating(pod) {
				log.V(3).Info("Skipping remote pod creation since the local pod is terminated or terminating", "pod", klog.KObj(pod))
				return true, nil
			}
			if startedRemotely(pod) {
				log.V(3).Info("Skipping remote pod creation since the local pod already started on the worker", "pod", klog.KObj(pod))
				return true, nil
			}
			return false, nil
		}
		return false, syncLocalPodWithRemote(ctx, localClient, remoteClient, &localPod, workloadName, origin, handleMissingRemote, &log)
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

var _ jobframework.MultiKueueLocalStatusResetter = (*multiKueueAdapter)(nil)

// ResetLocalStatus resets the status copied from the remote Pod back to the
// status of a gated Pod. Pods that are ungated, terminated, terminating, or
// were never synced with a remote Pod are left unchanged.
func (b *multiKueueAdapter) ResetLocalStatus(ctx context.Context, localClient client.Client, key types.NamespacedName) error {
	localPod := corev1.Pod{}
	if err := localClient.Get(ctx, key, &localPod); err != nil {
		return client.IgnoreNotFound(err)
	}

	// Pod groups are not supported: a gated Pod group with a copied Running
	// phase is still active, so its evicted workload keeps the quota reservation
	// and this reset is never reached for it.
	if utilpod.GetPodGroupName(&localPod) != "" {
		return nil
	}

	if !isGated(&localPod) || utilpod.IsTerminated(&localPod) || !localPod.DeletionTimestamp.IsZero() || !hasRemoteStatus(&localPod) {
		return nil
	}

	return clientutil.PatchStatus(ctx, localClient, &localPod, func() (bool, error) {
		localPod.Status = corev1.PodStatus{
			Phase:      corev1.PodPending,
			Conditions: []corev1.PodCondition{b.schedulingGatedCondition(&localPod)},
			QOSClass:   localPod.Status.QOSClass,
		}
		return true, nil
	})
}

var _ jobframework.MultiKueueRemoteLossHandler = (*multiKueueAdapter)(nil)

// FinishLocalJobIfRemoteLost finishes a manager Pod that started on a worker
// whose Pod no longer exists. Pod groups are not supported and are left unchanged.
func (b *multiKueueAdapter) FinishLocalJobIfRemoteLost(
	ctx context.Context,
	localClient client.Client,
	remoteClient client.Client,
	key types.NamespacedName,
	remoteSucceeded bool,
	message string,
) (bool, error) {
	localPod := corev1.Pod{}
	if err := localClient.Get(ctx, key, &localPod); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	if utilpod.GetPodGroupName(&localPod) != "" || isTerminatedOrTerminating(&localPod) || !startedRemotely(&localPod) {
		return false, nil
	}
	err := remoteClient.Get(ctx, key, &corev1.Pod{})
	if !apierrors.IsNotFound(err) {
		return false, err
	}
	ctrl.LoggerFrom(ctx).V(3).Info("Finishing the local pod since its remote pod was lost", "pod", klog.KObj(&localPod), "succeeded", remoteSucceeded)
	if err := finishLocalPod(ctx, localClient, &localPod, b.clock, remoteSucceeded, message); err != nil {
		return false, err
	}
	return true, nil
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
		return nil, fmt.Errorf("no prebuilt workload found for pod: %s", klog.KObj(pod))
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
		// Pod groups are not supported: finishing a single member does not finish
		// the group, so missing remote members are always created again.
		if err = syncLocalPodWithRemote(ctx, localClient, remoteClient, &localPod, workloadName, origin, nil, &log); err != nil {
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
	handleMissingRemote func(*corev1.Pod) (bool, error),
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
			log.V(3).Info("Skipping sync since the local pod is terminating", "pod", klog.KObj(localPod))
			return nil
		}

		// A terminating remote pod of a local pod that did not start belongs to an
		// earlier dispatch, e.g. one evicted on the worker whose local status was
		// reset. Copying its status would make the local pod look started again, so
		// wait for it to be removed and the remote pod to be created again.
		if !remotePod.DeletionTimestamp.IsZero() && !startedRemotely(localPod) {
			log.V(3).Info("Skipping sync from the terminating remote pod of an earlier dispatch", "pod", klog.KObj(localPod))
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

	// The remote pod does not exist; it is created unless handleMissingRemote
	// handled the local pod.
	if handleMissingRemote != nil {
		if handled, err := handleMissingRemote(localPod); handled || err != nil {
			return err
		}
	}

	remotePod = corev1.Pod{
		ObjectMeta: api.CloneObjectMetaForCreation(&localPod.ObjectMeta),
		Spec:       *localPod.Spec.DeepCopy(),
	}
	// The API server forbids ephemeralContainers on create; they are added only via the subresource.
	remotePod.Spec.EphemeralContainers = nil

	// Add prebuilt workload name and multikueue origin
	jobframework.SetMultiKueueMeta(&remotePod, workloadName, origin)

	if err = remoteClient.Create(ctx, &remotePod); err != nil {
		log.Error(err, "Failed to create remote pod", "pod", klog.KObj(&remotePod))
		return fmt.Errorf("creating remote pod %s: %w", klog.KObj(&remotePod), err)
	}

	return nil
}

func isTerminatedOrTerminating(pod *corev1.Pod) bool {
	return utilpod.IsTerminated(pod) || !pod.DeletionTimestamp.IsZero()
}

// startedRemotely reports whether the status copied onto the local Pod shows
// that its remote Pod started: the kubelet accepted it or created any of its
// containers. A remote Pod that was only scheduled has not run any of the job.
func startedRemotely(pod *corev1.Pod) bool {
	return pod.Status.StartTime != nil ||
		pod.Status.Phase == corev1.PodRunning ||
		len(pod.Status.ContainerStatuses) > 0 ||
		len(pod.Status.InitContainerStatuses) > 0
}

// finishLocalPod marks the local Pod final because its remote Pod was lost.
// A succeeded remote Pod had all of its containers exit successfully, so they
// are marked completed. Otherwise the Pod is failed, and the TerminationTarget
// condition, which Kueue also sets on the Pods it stops, tells the Pod's owner
// that the failure did not come from the application.
func finishLocalPod(ctx context.Context, c client.Client, pod *corev1.Pod, clk clock.Clock, succeeded bool, message string) error {
	now := metav1.NewTime(clk.Now())
	return clientutil.PatchStatus(ctx, c, pod, func() (bool, error) {
		pod.Status.Reason = RemotePodLostReason
		pod.Status.Message = message
		if succeeded {
			pod.Status.Phase = corev1.PodSucceeded
			markContainersCompleted(pod.Status.InitContainerStatuses, now)
			markContainersCompleted(pod.Status.ContainerStatuses, now)
			return true, nil
		}
		pod.Status.Phase = corev1.PodFailed
		setPodCondition(&pod.Status, corev1.PodCondition{
			Type:               corev1.PodConditionType(ConditionTypeTerminationTarget),
			Status:             corev1.ConditionTrue,
			Reason:             RemotePodLostReason,
			Message:            message,
			LastTransitionTime: now,
		})
		return true, nil
	})
}

func markContainersCompleted(statuses []corev1.ContainerStatus, now metav1.Time) {
	for i := range statuses {
		status := &statuses[i]
		if status.State.Terminated != nil {
			continue
		}
		terminated := &corev1.ContainerStateTerminated{Reason: "Completed", FinishedAt: now}
		if status.State.Running != nil {
			terminated.StartedAt = status.State.Running.StartedAt
		}
		status.State = corev1.ContainerState{Terminated: terminated}
		status.Ready = false
		status.Started = new(false)
	}
}

// hasRemoteStatus reports whether the Pod status carries anything that only a
// running remote Pod can report. The API server initializes a gated Pod with
// phase Pending and a SchedulingGated PodScheduled condition only.
func hasRemoteStatus(pod *corev1.Pod) bool {
	return pod.Status.Phase != corev1.PodPending ||
		!isSchedulingGatedCondition(findPodCondition(pod.Status.Conditions, corev1.PodScheduled)) ||
		pod.Status.PodIP != "" ||
		pod.Status.HostIP != "" ||
		pod.Status.StartTime != nil ||
		len(pod.Status.ContainerStatuses) > 0 ||
		len(pod.Status.InitContainerStatuses) > 0
}

// schedulingGatedCondition returns the PodScheduled condition of a gated Pod,
// keeping the existing one so its LastTransitionTime is preserved.
func (b *multiKueueAdapter) schedulingGatedCondition(pod *corev1.Pod) corev1.PodCondition {
	if c := findPodCondition(pod.Status.Conditions, corev1.PodScheduled); isSchedulingGatedCondition(c) {
		return *c
	}
	return corev1.PodCondition{
		Type:               corev1.PodScheduled,
		Status:             corev1.ConditionFalse,
		Reason:             corev1.PodReasonSchedulingGated,
		Message:            "Scheduling is blocked due to non-empty scheduling gates",
		LastTransitionTime: metav1.NewTime(b.clock.Now()),
	}
}

func isSchedulingGatedCondition(c *corev1.PodCondition) bool {
	return c != nil && c.Status == corev1.ConditionFalse && c.Reason == corev1.PodReasonSchedulingGated
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

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
	"maps"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/util/api"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
)

type multiKueueAdapter struct{}

const remotePodCleanupPageSize int64 = 100

var _ jobframework.MultiKueueAdapter = (*multiKueueAdapter)(nil)
var _ jobframework.MultiKueueAdapterWithRemoteObjectCleanup = (*multiKueueAdapter)(nil)

func (b *multiKueueAdapter) SyncJob(ctx context.Context, localClient client.Client, remoteClient client.Client, key types.NamespacedName, workloadName, origin string) (bool, error) {
	log := ctrl.LoggerFrom(ctx)

	localPod := corev1.Pod{}
	err := localClient.Get(ctx, key, &localPod)
	if err != nil {
		return false, err
	}

	groupName, err := multiKueuePodGroupName(&localPod)
	if err != nil {
		return false, err
	}
	if groupName == "" {
		return false, syncLocalPodWithRemote(ctx, localClient, remoteClient, &localPod, workloadName, origin, groupName, &log)
	}

	return false, syncPodGroup(ctx, localClient, remoteClient, key, workloadName, origin, groupName)
}

func (b *multiKueueAdapter) DeleteRemoteObject(ctx context.Context, localClient client.Client, remoteClient client.Client, key types.NamespacedName) error {
	localPod := corev1.Pod{}
	if err := localClient.Get(ctx, key, &localPod); err != nil {
		return err
	}
	if localPod.UID == "" {
		return fmt.Errorf("%w: manager Pod %q has no UID", jobframework.ErrRemoteObjectNotOwnedByMultiKueue, klog.KObj(&localPod))
	}
	pod := corev1.Pod{}
	err := remoteClient.Get(ctx, key, &pod)
	if err != nil {
		return client.IgnoreNotFound(err)
	}
	workloadName, err := jobframework.MultiKueueWorkloadNameFor(&pod)
	if err != nil {
		return err
	}
	groupName, err := multiKueuePodGroupName(&pod)
	if err != nil {
		return err
	}
	workloadAnnotations := map[string]string(nil)
	if groupName != "" {
		workloadAnnotations = map[string]string{podconstants.IsGroupWorkloadAnnotationKey: podconstants.IsGroupWorkloadAnnotationValue}
	}
	return b.deleteRemoteObjectWithCleanupContext(ctx, localClient, remoteClient, key, jobframework.MultiKueueRemoteObjectCleanupContext{
		RemoteObjectUID: pod.UID,
		Association: jobframework.MultiKueueObjectAssociation{
			Origin:           pod.Labels[kueue.MultiKueueOriginLabel],
			WorkloadName:     workloadName,
			ManagerObjectUID: localPod.UID,
		},
		WorkloadKey:         types.NamespacedName{Name: workloadName, Namespace: pod.Namespace},
		WorkloadAnnotations: workloadAnnotations,
	}, groupName)
}

func (b *multiKueueAdapter) DeleteRemoteObjectWithCleanupContext(
	ctx context.Context,
	localClient client.Client,
	remoteClient client.Client,
	key types.NamespacedName,
	cleanupContext jobframework.MultiKueueRemoteObjectCleanupContext,
) error {
	groupName, err := expectedMultiKueuePodGroupName(ctx, localClient, key, cleanupContext)
	if err != nil {
		return err
	}
	return b.deleteRemoteObjectWithCleanupContext(ctx, localClient, remoteClient, key, cleanupContext, groupName)
}

func (b *multiKueueAdapter) deleteRemoteObjectWithCleanupContext(
	ctx context.Context,
	localClient, remoteClient client.Client,
	key types.NamespacedName,
	cleanupContext jobframework.MultiKueueRemoteObjectCleanupContext,
	groupName string,
) error {
	log := ctrl.LoggerFrom(ctx)

	pod := corev1.Pod{}
	if err := remoteClient.Get(ctx, key, &pod); err != nil {
		return client.IgnoreNotFound(err)
	}
	if pod.UID != cleanupContext.RemoteObjectUID {
		return fmt.Errorf("%w: remote Pod %q was replaced before cleanup", jobframework.ErrRemoteObjectNotOwnedByMultiKueue, klog.KObj(&pod))
	}
	if err := validateRemotePodAssociation(&pod, cleanupContext.Association, groupName); err != nil {
		return err
	}
	if err := validateRemotePodManagerUID(&pod, cleanupContext.Association.ManagerObjectUID); err != nil {
		return err
	}

	if groupName == "" {
		return deleteRemotePodIfAssociated(ctx, remoteClient, &pod, cleanupContext.Association, cleanupContext.Association.ManagerObjectUID, groupName, &log)
	}

	workloadBoundManagerUIDs := cleanupContext.ManagerObjectUIDs != nil
	managerPodUIDs := maps.Clone(cleanupContext.ManagerObjectUIDs)
	if managerPodUIDs == nil {
		managerPodUIDs = make(map[string]types.UID)
	}
	if recorded := managerPodUIDs[key.Name]; recorded != "" && recorded != cleanupContext.Association.ManagerObjectUID {
		return fmt.Errorf("%w: cleanup context has conflicting manager UIDs for Pod %q", jobframework.ErrRemoteObjectNotOwnedByMultiKueue, key)
	}
	managerPodUIDs[key.Name] = cleanupContext.Association.ManagerObjectUID
	managerPods := &corev1.PodList{}
	if err := localClient.List(ctx, managerPods, client.InNamespace(key.Namespace)); err != nil {
		return err
	}
	for i := range managerPods.Items {
		managerPod := &managerPods.Items[i]
		managerGroupName, err := multiKueuePodGroupName(managerPod)
		if err != nil {
			return err
		}
		if managerGroupName != groupName {
			continue
		}
		if managerPod.UID == "" {
			return fmt.Errorf("%w: manager Pod %q has no UID", jobframework.ErrRemoteObjectNotOwnedByMultiKueue, klog.KObj(managerPod))
		}
		if recorded := managerPodUIDs[managerPod.Name]; recorded != "" {
			if recorded != managerPod.UID {
				return fmt.Errorf("%w: manager Pod %q UID %q conflicts with Workload identity %q", jobframework.ErrRemoteObjectNotOwnedByMultiKueue, klog.KObj(managerPod), managerPod.UID, recorded)
			}
			continue
		}
		if workloadBoundManagerUIDs {
			continue
		}
		managerPodUIDs[managerPod.Name] = managerPod.UID
	}

	listOptions := &client.ListOptions{
		Namespace:     key.Namespace,
		LabelSelector: labels.SelectorFromSet(labels.Set{kueue.MultiKueueOriginLabel: cleanupContext.Association.Origin}),
		Limit:         remotePodCleanupPageSize,
	}
	remotePodsToDelete := make([]*corev1.Pod, 0)
	for {
		remotePodGroup := corev1.PodList{}
		if err := remoteClient.List(ctx, &remotePodGroup, listOptions); err != nil {
			return err
		}
		currentAnchor := corev1.Pod{}
		if err := remoteClient.Get(ctx, key, &currentAnchor); err != nil {
			return err
		}
		if currentAnchor.UID != cleanupContext.RemoteObjectUID {
			return fmt.Errorf("%w: remote Pod %q was replaced during PodGroup cleanup", jobframework.ErrRemoteObjectNotOwnedByMultiKueue, klog.KObj(&currentAnchor))
		}
		if err := validateRemotePodAssociation(&currentAnchor, cleanupContext.Association, groupName); err != nil {
			return err
		}
		if err := validateRemotePodManagerUID(&currentAnchor, cleanupContext.Association.ManagerObjectUID); err != nil {
			return err
		}
		for i := range remotePodGroup.Items {
			remotePod := &remotePodGroup.Items[i]
			podKey := client.ObjectKeyFromObject(remotePod)
			if podKey == key {
				if remotePod.UID != cleanupContext.RemoteObjectUID {
					return fmt.Errorf("%w: remote Pod %q was replaced during PodGroup cleanup", jobframework.ErrRemoteObjectNotOwnedByMultiKueue, klog.KObj(remotePod))
				}
				continue
			}
			if err := validateRemotePodAssociation(remotePod, cleanupContext.Association, groupName); err != nil {
				if errors.Is(err, jobframework.ErrRemoteObjectNotOwnedByMultiKueue) {
					log.V(4).Info("Preserving remote Pod that is not associated with this MultiKueue dispatch", "pod", klog.KObj(remotePod))
					continue
				}
				return err
			}
			expectedManagerUID, found := managerPodUIDs[remotePod.Name]
			if !found {
				return fmt.Errorf("%w: no trusted manager UID for remote Pod %q", jobframework.ErrRemoteObjectNotOwnedByMultiKueue, klog.KObj(remotePod))
			}
			if err := validateRemotePodManagerUID(remotePod, expectedManagerUID); err != nil {
				return err
			}
			remotePodsToDelete = append(remotePodsToDelete, remotePod.DeepCopy())
		}
		if remotePodGroup.Continue == "" {
			break
		}
		listOptions.Continue = remotePodGroup.Continue
	}
	for _, remotePod := range remotePodsToDelete {
		if err := client.IgnoreNotFound(remoteClient.Delete(ctx, remotePod, client.Preconditions{UID: &remotePod.UID})); err != nil {
			return err
		}
	}

	// Keep the anchor until all pages and member deletions succeed so a retry can
	// still recover after a transient list or delete failure.
	return deleteRemotePodIfAssociated(ctx, remoteClient, &pod, cleanupContext.Association, cleanupContext.Association.ManagerObjectUID, groupName, &log)
}

func expectedMultiKueuePodGroupName(ctx context.Context, localClient client.Client, key types.NamespacedName, cleanupContext jobframework.MultiKueueRemoteObjectCleanupContext) (string, error) {
	var podGroupName string
	localPod := corev1.Pod{}
	if err := localClient.Get(ctx, key, &localPod); err == nil {
		var groupErr error
		podGroupName, groupErr = multiKueuePodGroupName(&localPod)
		if groupErr != nil {
			return "", groupErr
		}
	} else if client.IgnoreNotFound(err) != nil {
		return "", err
	}

	if cleanupContext.WorkloadKey.Name != "" {
		if cleanupContext.WorkloadKey.Name != cleanupContext.Association.WorkloadName || cleanupContext.WorkloadKey.Namespace != key.Namespace {
			return "", fmt.Errorf("workload %q does not match cleanup association %q", cleanupContext.WorkloadKey, cleanupContext.Association.WorkloadName)
		}
		workloadGroupName := ""
		if cleanupContext.WorkloadAnnotations[podconstants.IsGroupWorkloadAnnotationKey] == podconstants.IsGroupWorkloadAnnotationValue {
			workloadGroupName = cleanupContext.WorkloadKey.Name
		}
		if podGroupName != "" && podGroupName != workloadGroupName {
			return "", fmt.Errorf("manager PodGroup %q does not match Workload group %q", podGroupName, workloadGroupName)
		}
		return workloadGroupName, nil
	}

	// The local Pod can still provide group context to direct adapter callers that
	// do not have Workload context. Production Workload cleanup and GC always reach
	// the branch above even after the manager Pods are finalized.
	return podGroupName, nil
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

	prebuiltWorkload, err := jobframework.MultiKueueWorkloadNameFor(pod)
	if err != nil {
		return nil, fmt.Errorf("getting prebuilt Workload for Pod %q: %w", klog.KObj(pod), err)
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
		localGroupName, err := multiKueuePodGroupName(&localPod)
		if err != nil {
			return err
		}
		if localGroupName != groupName {
			return fmt.Errorf("Pod %q belongs to PodGroup %q, expected %q", klog.KObj(&localPod), localGroupName, groupName)
		}
		if err = syncLocalPodWithRemote(ctx, localClient, remoteClient, &localPod, workloadName, origin, groupName, &log); err != nil {
			return err
		}
	}

	return nil
}

func listLocalPods(ctx context.Context, localClient client.Client, namespace, groupName string) (*corev1.PodList, error) {
	pods := &corev1.PodList{}
	if err := localClient.List(ctx, pods, client.InNamespace(namespace), client.MatchingFields{multiKueuePodGroupNameCacheKey: groupName}); err != nil {
		return nil, err
	}
	return pods, nil
}

func syncLocalPodWithRemote(
	ctx context.Context,
	localClient client.Client,
	remoteClient client.Client,
	localPod *corev1.Pod,
	workloadName, origin, groupName string,
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
		if err := validateRemotePodAssociation(&remotePod, jobframework.MultiKueueObjectAssociation{Origin: origin, WorkloadName: workloadName}, groupName); err != nil {
			return err
		}

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

func multiKueuePodGroupName(pod *corev1.Pod) (string, error) {
	labelValue := pod.Labels[podconstants.GroupNameLabel]
	annotationValue := pod.Annotations[podconstants.GroupNameAnnotation]
	if labelValue != "" && annotationValue != "" && labelValue != annotationValue {
		return "", fmt.Errorf("%w: conflicting PodGroup identifiers on Pod %q", jobframework.ErrRemoteObjectNotOwnedByMultiKueue, klog.KObj(pod))
	}
	if annotationValue != "" {
		return annotationValue, nil
	}
	return labelValue, nil
}

func validateRemotePodAssociation(pod *corev1.Pod, association jobframework.MultiKueueObjectAssociation, groupName string) error {
	if err := jobframework.ValidateMultiKueueObjectAssociation(pod, association); err != nil {
		return err
	}
	podGroupName, err := multiKueuePodGroupName(pod)
	if err != nil {
		return err
	}
	if podGroupName != groupName {
		return fmt.Errorf("%w: Pod %q belongs to PodGroup %q, expected %q", jobframework.ErrRemoteObjectNotOwnedByMultiKueue, klog.KObj(pod), podGroupName, groupName)
	}
	return nil
}

func validateRemotePodManagerUID(pod *corev1.Pod, expected types.UID) error {
	actual := types.UID(pod.Annotations[kueue.MultiKueueOriginUIDAnnotation])
	if expected == "" || actual != expected {
		return fmt.Errorf("%w: expected %q=%q on Pod %q, got %q", jobframework.ErrRemoteObjectNotOwnedByMultiKueue, kueue.MultiKueueOriginUIDAnnotation, expected, klog.KObj(pod), actual)
	}
	return nil
}

func deleteRemotePodIfAssociated(
	ctx context.Context,
	remoteClient client.Client,
	pod *corev1.Pod,
	association jobframework.MultiKueueObjectAssociation,
	expectedManagerUID types.UID,
	groupName string,
	log *logr.Logger,
) error {
	if err := validateRemotePodAssociation(pod, association, groupName); err != nil {
		if errors.Is(err, jobframework.ErrRemoteObjectNotOwnedByMultiKueue) {
			log.V(4).Info("Preserving remote Pod that is not associated with this MultiKueue dispatch", "pod", klog.KObj(pod))
			return nil
		}
		return err
	}
	if err := validateRemotePodManagerUID(pod, expectedManagerUID); err != nil {
		return err
	}
	return client.IgnoreNotFound(remoteClient.Delete(ctx, pod, client.Preconditions{UID: &pod.UID}))
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

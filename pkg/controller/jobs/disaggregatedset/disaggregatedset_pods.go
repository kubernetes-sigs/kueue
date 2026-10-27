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

package disaggregatedset

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	disaggregatedsetv1 "sigs.k8s.io/lws/api/disaggregatedset/v1"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	"sigs.k8s.io/kueue/pkg/constants"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podcontroller "sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
	"sigs.k8s.io/kueue/pkg/util/parallelize"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	utilstatefulset "sigs.k8s.io/kueue/pkg/util/statefulset"
)

const ReasonPodNotAdopted = "PodNotAdopted"

var (
	statefulSetGVK     = appsv1.SchemeGroupVersion.WithKind("StatefulSet")
	leaderWorkerSetGVK = leaderworkersetv1.SchemeGroupVersion.WithKind("LeaderWorkerSet")
	podGVK             = corev1.SchemeGroupVersion.WithKind("Pod")
)

// podLabelsFromLWS are the labels that the DisaggregatedSet controller sets on
// a child LeaderWorkerSet and on its Pods.
var podLabelsFromLWS = []string{
	disaggregatedsetv1.SetNameLabelKey,
	disaggregatedsetv1.SliceLabelKey,
	disaggregatedsetv1.RoleLabelKey,
	disaggregatedsetv1.RevisionLabelKey,
}

// ownership is the result of following the owner chain of a slice Pod.
type ownership struct {
	// gone is set when an owner in the chain no longer exists.
	gone bool
	// orphaned is set when a link of the chain has no controller reference, as
	// after its owner was deleted with --cascade=orphan.
	orphaned bool
	// notAdopted names the failed check when the owners exist but the Pod is
	// not one that they created.
	notAdopted string
}

// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch
// +kubebuilder:rbac:groups=leaderworkerset.x-k8s.io,resources=leaderworkersets,verbs=get;list;watch

// listPods returns the Pods that Kueue gated for the DisaggregatedSet.
func (r *Reconciler) listPods(ctx context.Context, namespace, dsName string) ([]corev1.Pod, error) {
	pods := &corev1.PodList{}
	if err := r.client.List(ctx, pods, client.InNamespace(namespace),
		client.MatchingLabels{disaggregatedsetv1.SetNameLabelKey: dsName},
	); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "Failed to list Pods")
		return nil, err
	}
	managed := pods.Items[:0]
	for _, pod := range pods.Items {
		if pod.Annotations[podconstants.SuspendedByParentAnnotation] == FrameworkName {
			managed = append(managed, pod)
		}
	}
	return managed, nil
}

// releasePods ungates the Pods of a DisaggregatedSet that no longer exists.
func (r *Reconciler) releasePods(ctx context.Context, namespace, dsName string) error {
	pods, err := r.listPods(ctx, namespace, dsName)
	if err != nil {
		return err
	}
	return parallelize.Until(ctx, len(pods), func(i int) error {
		return r.ungatePod(ctx, &pods[i])
	})
}

// podSlot identifies one PodSet of one slice.
type podSlot struct {
	slice      int32
	podSetName string
}

// slicePod is a Pod that passed the adoption checks, with its PodSet.
type slicePod struct {
	pod  *corev1.Pod
	slot podSlot
}

func (r *Reconciler) reconcilePods(ctx context.Context, ds *disaggregatedsetv1.DisaggregatedSet) error {
	pods, err := r.listPods(ctx, ds.Namespace, ds.Name)
	if err != nil {
		return err
	}
	podSets, err := podSets(ds)
	if err != nil {
		return err
	}
	podSetCounts := make(map[string]int32, len(podSets))
	var totalCount int32
	for _, ps := range podSets {
		podSetCounts[string(ps.Name)] = ps.Count
		totalCount += ps.Count
	}

	slots := make([]*podSlot, len(pods))
	if err := parallelize.Until(ctx, len(pods), func(i int) error {
		var err error
		slots[i], err = r.checkPod(ctx, ds, &pods[i])
		return err
	}); err != nil {
		return err
	}

	toPatch := podsToPatch(ctx, pods, slots, podSetCounts)
	return parallelize.Until(ctx, len(toPatch), func(i int) error {
		return r.patchPod(ctx, ds, toPatch[i], totalCount)
	})
}

// checkPod releases a Pod whose owners are gone, and reports a Pod that fails
// the adoption checks. It returns the PodSet of a live Pod that passes them.
func (r *Reconciler) checkPod(ctx context.Context, ds *disaggregatedsetv1.DisaggregatedSet, pod *corev1.Pod) (*podSlot, error) {
	if utilpod.IsTerminated(pod) || pod.DeletionTimestamp != nil {
		return nil, nil
	}
	log := ctrl.LoggerFrom(ctx).WithValues("pod", klog.KObj(pod))

	owners, err := r.checkOwnership(ctx, ds, pod)
	if err != nil {
		return nil, err
	}
	// Kueue no longer manages the Pods of a deleted child object, so it
	// releases them, as the LeaderWorkerSet integration does. A Pod that was
	// never adopted has no owner to lose, so it stays gated.
	if _, adopted := pod.Labels[constants.ManagedByKueueLabelKey]; owners.gone || owners.orphaned && adopted {
		return nil, r.ungatePod(ctx, pod)
	}
	reason := owners.notAdopted
	var slot podSlot
	if reason == "" {
		slot, reason = podSlotOf(ds, pod)
	}
	if reason != "" {
		log.V(3).Info("Not adopting Pod", "reason", reason)
		r.record.Eventf(pod, nil, corev1.EventTypeWarning, ReasonPodNotAdopted, ReasonPodNotAdopted,
			"Not adopted by DisaggregatedSet %s: %s", ds.Name, reason)
		return nil, nil
	}
	return &slot, nil
}

// podsToPatch returns the adopted Pods, and the Pods to adopt now. A Pod is
// adopted only while its PodSet in the slice has fewer members than its count,
// oldest Pods first. The pod-group reconciler finishes a prebuilt Workload
// that has more members than it reserves, and during a rollout a new-revision
// Pod appears while the Pod it replaces is still terminating.
func podsToPatch(ctx context.Context, pods []corev1.Pod, slots []*podSlot, podSetCounts map[string]int32) []slicePod {
	members := make(map[podSlot]int32)
	var adopted, candidates []slicePod
	for i := range pods {
		pod := &pods[i]
		_, isMember := pod.Labels[constants.ManagedByKueueLabelKey]
		if isMember && podcontroller.IsPodRunnableOrSucceeded(pod) {
			if slice, err := strconv.ParseInt(pod.Labels[disaggregatedsetv1.SliceLabelKey], 10, 32); err == nil {
				members[podSlot{slice: int32(slice), podSetName: pod.Annotations[podconstants.RoleHashAnnotation]}]++
			}
		}
		switch {
		case slots[i] == nil:
		case isMember:
			adopted = append(adopted, slicePod{pod: pod, slot: *slots[i]})
		default:
			candidates = append(candidates, slicePod{pod: pod, slot: *slots[i]})
		}
	}

	slices.SortFunc(candidates, func(a, b slicePod) int {
		return cmp.Or(
			a.pod.CreationTimestamp.Compare(b.pod.CreationTimestamp.Time),
			cmp.Compare(a.pod.Name, b.pod.Name),
		)
	})
	for _, c := range candidates {
		if members[c.slot] >= podSetCounts[c.slot.podSetName] {
			ctrl.LoggerFrom(ctx).V(3).Info("Waiting for a free slot in the slice before adopting the Pod",
				"pod", klog.KObj(c.pod), "slice", c.slot.slice, "podSet", c.slot.podSetName)
			continue
		}
		members[c.slot]++
		adopted = append(adopted, c)
	}
	return adopted
}

func (r *Reconciler) patchPod(ctx context.Context, ds *disaggregatedsetv1.DisaggregatedSet, sp slicePod, totalCount int32) error {
	err := clientutil.Patch(ctx, r.client, sp.pod, func() (bool, error) {
		return setPodDefaults(ds, sp.pod, sp.slot, totalCount), nil
	})
	if client.IgnoreNotFound(err) != nil {
		ctrl.LoggerFrom(ctx).Error(err, "Failed to set the pod group of the Pod", "pod", klog.KObj(sp.pod))
		return err
	}
	return nil
}

func (r *Reconciler) ungatePod(ctx context.Context, pod *corev1.Pod) error {
	err := clientutil.Patch(ctx, r.client, pod, func() (bool, error) {
		return utilstatefulset.UngatePod(pod), nil
	})
	if client.IgnoreNotFound(err) != nil {
		ctrl.LoggerFrom(ctx).Error(err, "Failed to ungate Pod", "pod", klog.KObj(pod))
		return err
	}
	return nil
}

// podSlotOf returns the slice and the PodSet of a Pod, or the reason why the
// Pod has none.
func podSlotOf(ds *disaggregatedsetv1.DisaggregatedSet, pod *corev1.Pod) (podSlot, string) {
	slice, err := strconv.ParseInt(pod.Labels[disaggregatedsetv1.SliceLabelKey], 10, 32)
	if err != nil || slice < 0 {
		return podSlot{}, fmt.Sprintf("invalid %s label", disaggregatedsetv1.SliceLabelKey)
	}
	roleName := pod.Labels[disaggregatedsetv1.RoleLabelKey]
	for i := range ds.Spec.Roles {
		role := &ds.Spec.Roles[i]
		if role.Name != roleName {
			continue
		}
		slot := podSlot{slice: int32(slice), podSetName: role.Name}
		if role.Spec.LeaderWorkerTemplate.LeaderTemplate != nil {
			if pod.Labels[leaderworkersetv1.WorkerIndexLabelKey] == "0" {
				slot.podSetName += leaderPodSetSuffix
			} else {
				slot.podSetName += workerPodSetSuffix
			}
		}
		return slot, ""
	}
	return podSlot{}, fmt.Sprintf("role %q is not in the spec", roleName)
}

// setPodDefaults makes the Pod a member of the pod group of its slice. A Pod
// that is already a member only gets the queue name of the DisaggregatedSet,
// and only while it is gated, because the pod webhook rejects the change on
// an ungated Pod.
func setPodDefaults(ds *disaggregatedsetv1.DisaggregatedSet, pod *corev1.Pod, slot podSlot, totalCount int32) bool {
	queueName := string(jobframework.QueueNameForObject(ds))
	setQueue := queueName != "" && utilpod.HasGate(pod, podconstants.SchedulingGateName) &&
		pod.Labels[controllerconstants.QueueLabel] != queueName

	if _, managed := pod.Labels[constants.ManagedByKueueLabelKey]; managed {
		if setQueue {
			pod.Labels[controllerconstants.QueueLabel] = queueName
		}
		return setQueue
	}

	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string, 2)
	}
	pod.Labels[constants.ManagedByKueueLabelKey] = constants.ManagedByKueueLabelValue
	if setQueue {
		pod.Labels[controllerconstants.QueueLabel] = queueName
	}
	wlName := GetWorkloadName(ds.UID, ds.Name, slot.slice)
	podcontroller.SetPodGroupName(pod, wlName)
	jobframework.SetPrebuiltWorkloadName(pod, wlName)
	pod.Annotations[podconstants.GroupTotalCountAnnotation] = strconv.Itoa(int(totalCount))
	pod.Annotations[podconstants.RoleHashAnnotation] = slot.podSetName
	return true
}

// checkOwnership follows the controller owner chain of a Pod to the
// DisaggregatedSet: Pod, StatefulSet, then the child LeaderWorkerSet. A worker
// StatefulSet is owned by its leader Pod, whose chain is followed in turn.
func (r *Reconciler) checkOwnership(ctx context.Context, ds *disaggregatedsetv1.DisaggregatedSet, pod *corev1.Pod) (ownership, error) {
	lws, owners, err := r.podLeaderWorkerSet(ctx, pod, true)
	if err != nil || lws == nil {
		return owners, err
	}
	if ref := metav1.GetControllerOf(lws); !isReferenceTo(ref, gvk) || ref.UID != ds.UID {
		return ownership{
			orphaned:   ref == nil,
			notAdopted: fmt.Sprintf("LeaderWorkerSet %s is not controlled by the DisaggregatedSet", lws.Name),
		}, nil
	}
	for _, key := range podLabelsFromLWS {
		if v := pod.Labels[key]; v == "" || v != lws.Labels[key] {
			return ownership{notAdopted: fmt.Sprintf("label %s does not match LeaderWorkerSet %s", key, lws.Name)}, nil
		}
	}
	return ownership{}, nil
}

// podLeaderWorkerSet returns the LeaderWorkerSet that created the Pod, or nil
// with the result of the check that failed.
func (r *Reconciler) podLeaderWorkerSet(ctx context.Context, pod *corev1.Pod, allowWorker bool) (*leaderworkersetv1.LeaderWorkerSet, ownership, error) {
	sts := &appsv1.StatefulSet{}
	if owners, err := r.getController(ctx, pod, statefulSetGVK, sts); err != nil || owners != (ownership{}) {
		return nil, owners, err
	}
	if reason := checkOrdinal(pod, sts); reason != "" {
		return nil, ownership{notAdopted: reason}, nil
	}

	ref := metav1.GetControllerOf(sts)
	switch {
	case isReferenceTo(ref, leaderWorkerSetGVK):
		lws := &leaderworkersetv1.LeaderWorkerSet{}
		if owners, err := r.getController(ctx, sts, leaderWorkerSetGVK, lws); err != nil || owners != (ownership{}) {
			return nil, owners, err
		}
		return lws, ownership{}, nil
	case allowWorker && isReferenceTo(ref, podGVK):
		leader := &corev1.Pod{}
		if owners, err := r.getController(ctx, sts, podGVK, leader); err != nil || owners != (ownership{}) {
			return nil, owners, err
		}
		return r.podLeaderWorkerSet(ctx, leader, false)
	default:
		return nil, ownership{
			orphaned:   ref == nil,
			notAdopted: fmt.Sprintf("StatefulSet %s is not controlled by a LeaderWorkerSet or a leader Pod", sts.Name),
		}, nil
	}
}

// getController fetches the controller of obj into owner. The controller must
// have the given kind. A controller that no longer exists, or that was
// recreated with another UID, is reported as gone.
func (r *Reconciler) getController(ctx context.Context, obj client.Object, ownerGVK schema.GroupVersionKind, owner client.Object) (ownership, error) {
	ref := metav1.GetControllerOf(obj)
	if !isReferenceTo(ref, ownerGVK) {
		return ownership{
			orphaned:   ref == nil,
			notAdopted: fmt.Sprintf("%s is not controlled by a %s", obj.GetName(), ownerGVK.Kind),
		}, nil
	}
	if err := r.client.Get(ctx, client.ObjectKey{Namespace: obj.GetNamespace(), Name: ref.Name}, owner); err != nil {
		if apierrors.IsNotFound(err) {
			return ownership{gone: true}, nil
		}
		return ownership{}, err
	}
	if owner.GetUID() != ref.UID {
		return ownership{gone: true}, nil
	}
	return ownership{}, nil
}

func isReferenceTo(ref *metav1.OwnerReference, gvk schema.GroupVersionKind) bool {
	return ref != nil && ref.APIVersion == gvk.GroupVersion().String() && ref.Kind == gvk.Kind
}

// checkOrdinal checks that the Pod name is <statefulset-name>-<ordinal>, with
// an ordinal within the StatefulSet replicas.
func checkOrdinal(pod *corev1.Pod, sts *appsv1.StatefulSet) string {
	suffix, found := strings.CutPrefix(pod.Name, sts.Name+"-")
	ordinal, err := strconv.Atoi(suffix)
	if !found || err != nil || strconv.Itoa(ordinal) != suffix {
		return fmt.Sprintf("name is not an ordinal of StatefulSet %s", sts.Name)
	}
	var start int
	if sts.Spec.Ordinals != nil {
		start = int(sts.Spec.Ordinals.Start)
	}
	if ordinal < start || ordinal >= start+int(ptr.Deref(sts.Spec.Replicas, 1)) {
		return fmt.Sprintf("ordinal %d is outside the replicas of StatefulSet %s", ordinal, sts.Name)
	}
	return ""
}

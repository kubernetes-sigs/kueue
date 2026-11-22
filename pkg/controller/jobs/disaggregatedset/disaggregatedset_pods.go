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

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podcontroller "sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
	"sigs.k8s.io/kueue/pkg/util/parallelize"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	utilslices "sigs.k8s.io/kueue/pkg/util/slices"
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
	// scaledDown names the ordinal check when the owners created the Pod, but
	// its ordinal is above the replicas of its StatefulSet, as while the
	// StatefulSet controller removes it after a scale-down. The Pod can still
	// be running.
	scaledDown string
}

// created reports whether the owners exist and created the Pod.
func (o ownership) created() bool {
	return !o.gone && !o.orphaned && o.notAdopted == ""
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
		return nil, fmt.Errorf("listing Pods: %w", err)
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

func (r *Reconciler) reconcilePods(ctx context.Context, ds *disaggregatedsetv1.DisaggregatedSet, podSets []kueue.PodSet) error {
	pods, err := r.listPods(ctx, ds.Namespace, ds.Name)
	if err != nil {
		return err
	}
	podSetCounts := make(map[string]int32, len(podSets))
	var totalCount int32
	for _, ps := range podSets {
		podSetCounts[string(ps.Name)] = ps.Count
		totalCount += ps.Count
	}

	children, err := r.listChildObjects(ctx, ds, pods)
	if err != nil {
		return err
	}
	slots := make([]*podSlot, len(pods))
	if err := parallelize.Until(ctx, len(pods), func(i int) error {
		var err error
		slots[i], err = r.checkPod(ctx, ds, children, &pods[i])
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
func (r *Reconciler) checkPod(ctx context.Context, ds *disaggregatedsetv1.DisaggregatedSet, children *childObjects, pod *corev1.Pod) (*podSlot, error) {
	if utilpod.IsTerminated(pod) || pod.DeletionTimestamp != nil {
		return nil, nil
	}
	log := ctrl.LoggerFrom(ctx).WithValues("pod", klog.KObj(pod))

	owners, err := r.checkOwnership(ctx, ds, children, pod)
	if err != nil {
		return nil, err
	}
	// Kueue no longer manages the Pods of a deleted child object, so it
	// releases them, as the LeaderWorkerSet integration does. A Pod that was
	// never adopted has no owner to lose, so it stays gated.
	_, adopted := pod.Labels[constants.ManagedByKueueLabelKey]
	if owners.gone || owners.orphaned && adopted {
		return nil, r.ungatePod(ctx, pod)
	}
	reason := owners.notAdopted
	// A member that a scale-down is removing keeps its slot until the
	// StatefulSet controller deletes it. The StatefulSet controller never
	// creates a Pod above its replicas, so a new one is not adopted.
	if reason == "" && owners.scaledDown != "" {
		if adopted {
			return nil, nil
		}
		reason = owners.scaledDown
	}
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
		return fmt.Errorf("setting the pod group of Pod %s: %w", klog.KObj(sp.pod), err)
	}
	return nil
}

func (r *Reconciler) ungatePod(ctx context.Context, pod *corev1.Pod) error {
	err := clientutil.Patch(ctx, r.client, pod, func() (bool, error) {
		return utilstatefulset.UngatePod(pod), nil
	})
	if client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("ungating Pod %s: %w", klog.KObj(pod), err)
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

// childObjects holds the StatefulSets, LeaderWorkerSets and Pods of a
// DisaggregatedSet, listed once per reconcile by the DisaggregatedSet name
// label, so that the owner checks of its Pods read maps instead of fetching
// and copying every owner of every Pod. An owner that is not in the maps,
// for example one without the label, is fetched, so the checks do not depend
// on the label. The maps are read concurrently and must not be changed.
type childObjects struct {
	statefulSets     map[string]*appsv1.StatefulSet
	leaderWorkerSets map[string]*leaderworkersetv1.LeaderWorkerSet
	pods             map[string]*corev1.Pod
}

func (r *Reconciler) listChildObjects(ctx context.Context, ds *disaggregatedsetv1.DisaggregatedSet, pods []corev1.Pod) (*childObjects, error) {
	opts := []client.ListOption{
		client.InNamespace(ds.Namespace),
		client.MatchingLabels{disaggregatedsetv1.SetNameLabelKey: ds.Name},
	}
	statefulSets := &appsv1.StatefulSetList{}
	if err := r.client.List(ctx, statefulSets, opts...); err != nil {
		return nil, fmt.Errorf("listing StatefulSets: %w", err)
	}
	leaderWorkerSets := &leaderworkersetv1.LeaderWorkerSetList{}
	if err := r.client.List(ctx, leaderWorkerSets, opts...); err != nil {
		return nil, fmt.Errorf("listing LeaderWorkerSets: %w", err)
	}
	return &childObjects{
		statefulSets:     utilslices.ToRefMap(statefulSets.Items, func(o *appsv1.StatefulSet) string { return o.Name }),
		leaderWorkerSets: utilslices.ToRefMap(leaderWorkerSets.Items, func(o *leaderworkersetv1.LeaderWorkerSet) string { return o.Name }),
		pods:             utilslices.ToRefMap(pods, func(o *corev1.Pod) string { return o.Name }),
	}, nil
}

// get returns the object of the given kind and name from the maps, or fetches
// it when it is not there.
func (c *childObjects) get(ctx context.Context, cl client.Client, kind schema.GroupVersionKind, namespace, name string) (client.Object, error) {
	var obj client.Object
	switch kind {
	case statefulSetGVK:
		if sts, found := c.statefulSets[name]; found {
			return sts, nil
		}
		obj = &appsv1.StatefulSet{}
	case leaderWorkerSetGVK:
		if lws, found := c.leaderWorkerSets[name]; found {
			return lws, nil
		}
		obj = &leaderworkersetv1.LeaderWorkerSet{}
	case podGVK:
		if pod, found := c.pods[name]; found {
			return pod, nil
		}
		obj = &corev1.Pod{}
	default:
		return nil, fmt.Errorf("unexpected owner kind %s", kind)
	}
	if err := cl.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, obj); err != nil {
		return nil, err
	}
	return obj, nil
}

// checkOwnership follows the controller owner chain of a Pod to the
// DisaggregatedSet: Pod, StatefulSet, then the child LeaderWorkerSet. A worker
// StatefulSet is owned by its leader Pod, whose chain is followed in turn.
func (r *Reconciler) checkOwnership(ctx context.Context, ds *disaggregatedsetv1.DisaggregatedSet, children *childObjects, pod *corev1.Pod) (ownership, error) {
	lws, owners, err := r.podLeaderWorkerSet(ctx, children, pod, true)
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
	return owners, nil
}

// podLeaderWorkerSet returns the LeaderWorkerSet that created the Pod, or nil
// with the result of the check that failed.
func (r *Reconciler) podLeaderWorkerSet(ctx context.Context, children *childObjects, pod *corev1.Pod, allowWorker bool) (*leaderworkersetv1.LeaderWorkerSet, ownership, error) {
	owner, owners, err := r.getController(ctx, children, pod, statefulSetGVK)
	if err != nil || owners != (ownership{}) {
		return nil, owners, err
	}
	sts := owner.(*appsv1.StatefulSet)
	ordinal := checkOrdinal(pod, sts)
	if ordinal.notAdopted != "" {
		return nil, ordinal, nil
	}

	ref := metav1.GetControllerOf(sts)
	switch {
	case isReferenceTo(ref, leaderWorkerSetGVK):
		owner, owners, err := r.getController(ctx, children, sts, leaderWorkerSetGVK)
		if err != nil || owners != (ownership{}) {
			return nil, owners, err
		}
		return owner.(*leaderworkersetv1.LeaderWorkerSet), ordinal, nil
	case allowWorker && isReferenceTo(ref, podGVK):
		owner, owners, err := r.getController(ctx, children, sts, podGVK)
		if err != nil || owners != (ownership{}) {
			return nil, owners, err
		}
		lws, owners, err := r.podLeaderWorkerSet(ctx, children, owner.(*corev1.Pod), false)
		if owners.scaledDown == "" {
			owners.scaledDown = ordinal.scaledDown
		}
		return lws, owners, err
	default:
		return nil, ownership{
			orphaned:   ref == nil,
			notAdopted: fmt.Sprintf("StatefulSet %s is not controlled by a LeaderWorkerSet or a leader Pod", sts.Name),
		}, nil
	}
}

// getController returns the controller of obj. The controller must have the
// given kind. A controller that no longer exists is reported as gone.
// A controller recreated under the same name with another UID is not the one
// that created obj, so obj is not adopted and stays gated until the garbage
// collector removes it. This happens to the worker StatefulSet of a leader Pod
// that its StatefulSet recreates.
func (r *Reconciler) getController(ctx context.Context, children *childObjects, obj client.Object, ownerGVK schema.GroupVersionKind) (client.Object, ownership, error) {
	ref := metav1.GetControllerOf(obj)
	if !isReferenceTo(ref, ownerGVK) {
		return nil, ownership{
			orphaned:   ref == nil,
			notAdopted: fmt.Sprintf("%s is not controlled by a %s", obj.GetName(), ownerGVK.Kind),
		}, nil
	}
	owner, err := children.get(ctx, r.client, ownerGVK, obj.GetNamespace(), ref.Name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, ownership{gone: true}, nil
		}
		return nil, ownership{}, err
	}
	if owner.GetUID() != ref.UID {
		return nil, ownership{notAdopted: fmt.Sprintf("%s %s that controls %s was recreated", ownerGVK.Kind, ref.Name, obj.GetName())}, nil
	}
	return owner, ownership{}, nil
}

func isReferenceTo(ref *metav1.OwnerReference, gvk schema.GroupVersionKind) bool {
	return ref != nil && ref.APIVersion == gvk.GroupVersion().String() && ref.Kind == gvk.Kind
}

// checkOrdinal checks that the Pod name is <statefulset-name>-<ordinal>, with
// an ordinal at or above the first ordinal of the StatefulSet. An ordinal above
// the replicas marks a Pod that a scale-down is removing.
func checkOrdinal(pod *corev1.Pod, sts *appsv1.StatefulSet) ownership {
	suffix, found := strings.CutPrefix(pod.Name, sts.Name+"-")
	ordinal, err := strconv.Atoi(suffix)
	if !found || err != nil || strconv.Itoa(ordinal) != suffix {
		return ownership{notAdopted: fmt.Sprintf("name is not an ordinal of StatefulSet %s", sts.Name)}
	}
	var start int
	if sts.Spec.Ordinals != nil {
		start = int(sts.Spec.Ordinals.Start)
	}
	outside := fmt.Sprintf("ordinal %d is outside the replicas of StatefulSet %s", ordinal, sts.Name)
	if ordinal < start {
		return ownership{notAdopted: outside}
	}
	if ordinal >= start+int(ptr.Deref(sts.Spec.Replicas, 1)) {
		return ownership{scaledDown: outside}
	}
	return ownership{}
}

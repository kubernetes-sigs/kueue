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

package scheduler

import (
	"maps"
	"sync"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/resources"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workload/concurrentadmission"
)

// nonTasUsageCache caches pod usage, to avoid
// the hot path documented in kueue#8449.
type nonTasUsageCache struct {
	podUsage               map[types.NamespacedName]podUsageValue
	nodeUsage              map[string]resources.Requests // pre-aggregated per-node totals
	tasPodUsage            map[types.NamespacedName]tasPodUsageValue
	tasPodsByWorkload      map[workload.Reference]map[types.NamespacedName]tasPodUsageValue
	tasPodsBySliceWorkload map[workload.Reference]map[types.NamespacedName]tasPodUsageValue
	tasNodeUsage           map[workload.Reference]map[string]resources.Requests
	releasingTASWorkloads  map[releasedTASWorkloadKey]releasedTASWorkload
	deletedWorkloadsByPod  map[client.ObjectKey]map[releasedTASWorkloadKey]types.UID
	lock                   sync.RWMutex
}

type podUsageValue struct {
	node  string
	usage resources.Requests
}

type tasPodUsageValue struct {
	podKey          client.ObjectKey
	node            string
	usage           resources.Requests
	workload        workload.Reference
	sliceWorkload   workload.Reference
	podUID          types.UID
	ownerUID        types.UID
	ownerKind       string
	ownerAPIVersion string
	deleting        bool
	releasing       bool
}

type releasedTASWorkload struct {
	owners  tasReservationOwnerIndex
	aliases []workload.Reference
	deleted bool
	pods    map[client.ObjectKey]types.UID
}

type releasedTASWorkloadKey struct {
	ref workload.Reference
	uid types.UID
}

// observeWorkload preserves eviction intent across reservation/Pod event
// ordering. It never releases physical capacity: snapshots only use this
// information to defer admission instead of choosing additional victims.
func (n *nonTasUsageCache) observeWorkload(wl *kueue.Workload) {
	n.lock.Lock()
	defer n.lock.Unlock()
	ref := workload.Key(wl)
	key := releasedTASWorkloadKey{ref: ref, uid: wl.UID}
	if workload.HasActiveQuotaReservation(wl) || !apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadEvicted) {
		delete(n.releasingTASWorkloads, key)
		return
	}
	if n.releasingTASWorkloads == nil {
		n.releasingTASWorkloads = make(map[releasedTASWorkloadKey]releasedTASWorkload)
	}
	entry := releasedTASWorkload{owners: newTASReservationOwnerIndex(wl)}
	if name := wl.Annotations[kueue.WorkloadSliceNameAnnotation]; name != "" {
		entry.aliases = append(entry.aliases, workload.NewReference(wl.Namespace, name))
	}
	if name := concurrentadmission.GetParentWorkloadName(wl); name != "" {
		entry.aliases = append(entry.aliases, workload.NewReference(wl.Namespace, name))
	}
	n.releasingTASWorkloads[key] = entry
}

func (n *nonTasUsageCache) deleteWorkload(ref workload.Reference, uid types.UID) {
	n.lock.Lock()
	defer n.lock.Unlock()
	key := releasedTASWorkloadKey{ref: ref, uid: uid}
	entry, found := n.releasingTASWorkloads[key]
	if !found || entry.deleted {
		return
	}
	entry.deleted = true
	// Once the Workload disappears, freeze exact Pod identities rather
	// than attributing a future same-name Workload's Pods to this intent.
	entry.pods = make(map[client.ObjectKey]types.UID)
	for _, alias := range append([]workload.Reference{ref}, entry.aliases...) {
		for _, pods := range []map[types.NamespacedName]tasPodUsageValue{n.tasPodsByWorkload[alias], n.tasPodsBySliceWorkload[alias]} {
			for podKey, pod := range pods {
				if entry.owners.covers(pod) {
					entry.pods[podKey] = pod.podUID
				}
			}
		}
	}
	if len(entry.pods) == 0 {
		delete(n.releasingTASWorkloads, key)
		return
	}
	if n.deletedWorkloadsByPod == nil {
		n.deletedWorkloadsByPod = make(map[client.ObjectKey]map[releasedTASWorkloadKey]types.UID)
	}
	for podKey, uid := range entry.pods {
		if n.deletedWorkloadsByPod[podKey] == nil {
			n.deletedWorkloadsByPod[podKey] = make(map[releasedTASWorkloadKey]types.UID)
		}
		n.deletedWorkloadsByPod[podKey][key] = uid
	}
	n.releasingTASWorkloads[key] = entry
}

// removeDeletedPodIntent prunes only the intents attached to this exact Pod.
// This avoids scanning a PodGroup's remaining members on every Pod deletion.
// Must be called under the write lock.
func (n *nonTasUsageCache) removeDeletedPodIntent(podKey client.ObjectKey, podUID types.UID) {
	for key, uid := range n.deletedWorkloadsByPod[podKey] {
		if uid != podUID {
			continue
		}
		entry := n.releasingTASWorkloads[key]
		delete(entry.pods, podKey)
		delete(n.deletedWorkloadsByPod[podKey], key)
		if len(entry.pods) == 0 {
			delete(n.releasingTASWorkloads, key)
		}
	}
	if len(n.deletedWorkloadsByPod[podKey]) == 0 {
		delete(n.deletedWorkloadsByPod, podKey)
	}
}

func (n *nonTasUsageCache) updateTAS(pod *corev1.Pod, log logr.Logger) string {
	n.lock.Lock()
	defer n.lock.Unlock()
	key := client.ObjectKeyFromObject(pod)
	old, hadOld := n.tasPodUsage[key]
	oldNode := n.removeTASPodUsage(key, log)
	if utilpod.IsTerminated(pod) || pod.Spec.NodeName == "" {
		if hadOld {
			n.removeDeletedPodIntent(key, old.podUID)
		}
		return oldNode
	}
	var ref workload.Reference
	if name := pod.Annotations[kueue.WorkloadAnnotation]; name != "" {
		ref = workload.NewReference(pod.Namespace, name)
	}
	var sliceRef workload.Reference
	if name := pod.Annotations[kueue.WorkloadSliceNameAnnotation]; name != "" {
		sliceRef = workload.NewReference(pod.Namespace, name)
	}
	requests := resources.NewRequestsFromPodSpec(&pod.Spec)
	if n.tasNodeUsage == nil {
		n.tasNodeUsage = make(map[workload.Reference]map[string]resources.Requests)
	}
	value := tasPodUsageValue{podKey: key, node: pod.Spec.NodeName, usage: requests, workload: ref, sliceWorkload: sliceRef, podUID: pod.UID, deleting: pod.DeletionTimestamp != nil}
	for _, owner := range pod.OwnerReferences {
		if owner.Controller != nil && *owner.Controller {
			value.ownerUID = owner.UID
			value.ownerKind = owner.Kind
			value.ownerAPIVersion = owner.APIVersion
			break
		}
	}
	n.addTASPodIndexes(value)
	if hadOld && (old.podUID != value.podUID || old.workload != value.workload || old.sliceWorkload != value.sliceWorkload ||
		old.ownerUID != value.ownerUID || old.ownerKind != value.ownerKind || old.ownerAPIVersion != value.ownerAPIVersion) {
		n.removeDeletedPodIntent(key, old.podUID)
	}
	if n.tasNodeUsage[ref] == nil {
		n.tasNodeUsage[ref] = make(map[string]resources.Requests)
	}
	nodes := n.tasNodeUsage[ref]
	if nodes[pod.Spec.NodeName] == nil {
		nodes[pod.Spec.NodeName] = resources.NewRequests()
	}
	nodes[pod.Spec.NodeName].Add(requests)
	nodes[pod.Spec.NodeName].Add(resources.OnePodRequest)
	identityChanged := hadOld && (old.workload != ref || old.sliceWorkload != sliceRef || old.podUID != value.podUID || old.deleting != value.deleting ||
		old.ownerUID != value.ownerUID || old.ownerKind != value.ownerKind ||
		old.ownerAPIVersion != value.ownerAPIVersion)
	if oldNode != pod.Spec.NodeName || identityChanged || (hadOld && requestsDecreased(old.usage, requests)) {
		return oldNode
	}
	return ""
}

// addTASPodIndexes and removeTASPodIndexes keep both reference lookups in sync
// under the write lock. Only the primary index groups physical node usage;
// the slice index lets deleted intents retain Pods from predecessor slices.
func (n *nonTasUsageCache) addTASPodIndexes(pod tasPodUsageValue) {
	if n.tasPodUsage == nil {
		n.tasPodUsage = make(map[types.NamespacedName]tasPodUsageValue)
	}
	if n.tasPodsByWorkload == nil {
		n.tasPodsByWorkload = make(map[workload.Reference]map[types.NamespacedName]tasPodUsageValue)
	}
	n.tasPodUsage[pod.podKey] = pod
	if n.tasPodsByWorkload[pod.workload] == nil {
		n.tasPodsByWorkload[pod.workload] = make(map[types.NamespacedName]tasPodUsageValue)
	}
	n.tasPodsByWorkload[pod.workload][pod.podKey] = pod
	if pod.sliceWorkload == "" {
		return
	}
	if n.tasPodsBySliceWorkload == nil {
		n.tasPodsBySliceWorkload = make(map[workload.Reference]map[types.NamespacedName]tasPodUsageValue)
	}
	if n.tasPodsBySliceWorkload[pod.sliceWorkload] == nil {
		n.tasPodsBySliceWorkload[pod.sliceWorkload] = make(map[types.NamespacedName]tasPodUsageValue)
	}
	n.tasPodsBySliceWorkload[pod.sliceWorkload][pod.podKey] = pod
}

func (n *nonTasUsageCache) removeTASPodIndexes(pod tasPodUsageValue) {
	delete(n.tasPodUsage, pod.podKey)
	delete(n.tasPodsByWorkload[pod.workload], pod.podKey)
	if len(n.tasPodsByWorkload[pod.workload]) == 0 {
		delete(n.tasPodsByWorkload, pod.workload)
	}
	delete(n.tasPodsBySliceWorkload[pod.sliceWorkload], pod.podKey)
	if len(n.tasPodsBySliceWorkload[pod.sliceWorkload]) == 0 {
		delete(n.tasPodsBySliceWorkload, pod.sliceWorkload)
	}
}

// GreaterKeys does not report a resource whose key disappeared entirely.
func requestsDecreased(old, current resources.Requests) bool {
	decreased := false
	old.ForEach(func(name corev1.ResourceName, amount resources.Amount) {
		if amount.Sign() > 0 && current.ResourceValue(name).Cmp(amount) < 0 {
			decreased = true
		}
	})
	return decreased
}

func (n *nonTasUsageCache) deleteTAS(key client.ObjectKey, log logr.Logger) string {
	n.lock.Lock()
	defer n.lock.Unlock()
	if old, found := n.tasPodUsage[key]; found {
		n.removeDeletedPodIntent(key, old.podUID)
	}
	node := n.removeTASPodUsage(key, log)
	return node
}

// removeTASPodUsage must be called under the write lock.
func (n *nonTasUsageCache) removeTASPodUsage(key client.ObjectKey, log logr.Logger) string {
	old, found := n.tasPodUsage[key]
	if !found {
		return ""
	}
	n.removeTASPodIndexes(old)
	nodes := n.tasNodeUsage[old.workload]
	if usage := nodes[old.node]; usage != nil {
		usage.Sub(old.usage)
		usage.Sub(resources.OnePodRequest)
		if pods := usage.ResourceValue(corev1.ResourcePods); pods.Sign() <= 0 {
			if pods.Sign() < 0 {
				log.V(0).Info("Unexpected negative TAS pod count", "node", old.node, "podCount", pods)
			}
			delete(nodes, old.node)
		}
	}
	if len(nodes) == 0 {
		delete(n.tasNodeUsage, old.workload)
	}
	return old.node
}

// unreservedTASUsage builds the residual physical usage from the Pod event
// cache. It never lists Pods on the scheduling path. Reservation changes are
// reflected in the next snapshot even when no Pod event follows them.
func (n *nonTasUsageCache) unreservedTASUsage(reserved map[workload.Reference]*workload.Info) map[string]resources.Requests {
	usage, _ := n.unreservedTASUsageAndPods(reserved)
	return usage
}

func (n *nonTasUsageCache) unreservedTASUsageAndPods(reserved map[workload.Reference]*workload.Info) (map[string]resources.Requests, []tasPodUsageValue) {
	n.lock.RLock()
	defer n.lock.RUnlock()
	result := make(map[string]resources.Requests)
	var residualPods []tasPodUsageValue
	releasingOwners := make(map[workload.Reference][]tasReservationOwnerIndex, len(n.releasingTASWorkloads))
	releasingPods := make(map[client.ObjectKey]types.UID)
	for key, entry := range n.releasingTASWorkloads {
		if entry.deleted {
			maps.Copy(releasingPods, entry.pods)
			continue
		}
		ref := key.ref
		releasingOwners[ref] = append(releasingOwners[ref], entry.owners)
		for _, alias := range entry.aliases {
			if alias != ref {
				releasingOwners[alias] = append(releasingOwners[alias], entry.owners)
			}
		}
	}
	addPod := func(pod tasPodUsageValue) {
		usage := resources.NewRequests()
		usage.Add(pod.usage)
		usage.Add(resources.OnePodRequest)
		addResidualTASUsage(result, pod.node, usage)
		uid, known := releasingPods[pod.podKey]
		pod.releasing = (known && uid == pod.podUID) || reservationCoversPod(releasingOwners, pod)
		residualPods = append(residualPods, pod)
	}
	// A reservation can be named after a replacement slice or a concurrent
	// admission variant, while its Pods still carry the original Workload name.
	// Build aliases once per snapshot; never resolve them through the API on the
	// scheduling path.
	ownersByRef := make(map[workload.Reference][]tasReservationOwnerIndex, len(reserved))
	for ref, info := range reserved {
		if info == nil || info.Obj == nil {
			continue
		}
		owners := newTASReservationOwnerIndex(info.Obj)
		ownersByRef[ref] = append(ownersByRef[ref], owners)
		if name := info.Obj.Annotations[kueue.WorkloadSliceNameAnnotation]; name != "" {
			alias := workload.NewReference(info.Obj.Namespace, name)
			if alias != ref {
				ownersByRef[alias] = append(ownersByRef[alias], owners)
			}
		}
		if parent := concurrentadmission.GetParentWorkloadName(info.Obj); parent != "" {
			alias := workload.NewReference(info.Obj.Namespace, parent)
			if alias != ref {
				ownersByRef[alias] = append(ownersByRef[alias], owners)
			}
		}
	}
	for ref, nodeUsages := range n.tasNodeUsage {
		// Active reservations cover Pods with matching or ambiguous ownership.
		// A provably different owner remains physical usage if the name is reused.
		// A Pod may have an old Workload annotation but a slice-chain name
		// pointing at the admitted replacement.
		hasReservation := ref != "" && len(ownersByRef[ref]) != 0
		if !hasReservation {
			for _, pod := range n.tasPodsByWorkload[ref] {
				if len(ownersByRef[pod.sliceWorkload]) != 0 {
					hasReservation = true
					break
				}
			}
		}
		if hasReservation {
			for _, pod := range n.tasPodsByWorkload[ref] {
				if !reservationCoversPod(ownersByRef, pod) {
					addPod(pod)
				}
			}
			continue
		}
		for node, usage := range nodeUsages {
			addResidualTASUsage(result, node, usage)
		}
		for _, pod := range n.tasPodsByWorkload[ref] {
			uid, known := releasingPods[pod.podKey]
			pod.releasing = (known && uid == pod.podUID) || reservationCoversPod(releasingOwners, pod)
			residualPods = append(residualPods, pod)
		}
	}
	return result, residualPods
}

func reservationCoversPod(ownersByRef map[workload.Reference][]tasReservationOwnerIndex, pod tasPodUsageValue) bool {
	for _, owners := range ownersByRef[pod.workload] {
		if owners.covers(pod) {
			return true
		}
	}
	if pod.sliceWorkload != pod.workload {
		for _, owners := range ownersByRef[pod.sliceWorkload] {
			if owners.covers(pod) {
				return true
			}
		}
	}
	return false
}

type tasOwnerType struct {
	apiVersion string
	kind       string
}

type tasOwnerIdentity struct {
	tasOwnerType
	uid types.UID
}

type tasReservationOwnerIndex struct {
	valid       bool
	hasPodOwner bool
	types       map[tasOwnerType]struct{}
	identities  map[tasOwnerIdentity]struct{}
}

// Build the index once per reservation, not per Pod. OwnerReferences can change
// between snapshots, so it must not be retained in the Pod usage cache.
func newTASReservationOwnerIndex(wl *kueue.Workload) tasReservationOwnerIndex {
	if wl == nil {
		return tasReservationOwnerIndex{}
	}
	index := tasReservationOwnerIndex{valid: true}
	if len(wl.OwnerReferences) == 0 {
		return index
	}
	index.types = make(map[tasOwnerType]struct{}, len(wl.OwnerReferences))
	index.identities = make(map[tasOwnerIdentity]struct{}, len(wl.OwnerReferences))
	for _, owner := range wl.OwnerReferences {
		ownerType := tasOwnerType{apiVersion: owner.APIVersion, kind: owner.Kind}
		index.types[ownerType] = struct{}{}
		index.identities[tasOwnerIdentity{tasOwnerType: ownerType, uid: owner.UID}] = struct{}{}
		if ownerType == (tasOwnerType{apiVersion: corev1.SchemeGroupVersion.String(), kind: "Pod"}) {
			index.hasPodOwner = true
		}
	}
	return index
}

// Different owner kinds can be part of one controller chain, so only a
// comparable owner mismatch proves that an active reservation is unrelated.
// Without a Pod owner or comparable controller owner, treat the Pod as covered
// to avoid double-counting ambiguous ownership.
func (index tasReservationOwnerIndex) covers(pod tasPodUsageValue) bool {
	if !index.valid {
		return false
	}
	if index.hasPodOwner {
		_, found := index.identities[tasOwnerIdentity{
			apiVersion: corev1.SchemeGroupVersion.String(), kind: "Pod",
			uid: pod.podUID,
		}]
		return found
	}
	if pod.ownerUID == "" {
		return true
	}
	ownerType := tasOwnerType{apiVersion: pod.ownerAPIVersion, kind: pod.ownerKind}
	if _, comparable := index.types[ownerType]; !comparable {
		return true
	}
	_, found := index.identities[tasOwnerIdentity{tasOwnerType: ownerType, uid: pod.ownerUID}]
	return found
}

// owns returns true only for a comparable, exact owner match. An ambiguous
// controller chain is safe for avoiding double-accounting against an active
// reservation, but not for crediting physical capacity to a new admission.
func (index tasReservationOwnerIndex) owns(pod tasPodUsageValue) bool {
	if !index.valid {
		return false
	}
	if index.hasPodOwner {
		_, found := index.identities[tasOwnerIdentity{
			apiVersion: corev1.SchemeGroupVersion.String(), kind: "Pod",
			uid: pod.podUID,
		}]
		return found
	}
	if pod.ownerUID == "" {
		return false
	}
	_, found := index.identities[tasOwnerIdentity{
		apiVersion: pod.ownerAPIVersion, kind: pod.ownerKind,
		uid: pod.ownerUID,
	}]
	return found
}

func addResidualTASUsage(result map[string]resources.Requests, node string, usage resources.Requests) {
	if result[node] == nil {
		result[node] = resources.NewRequests()
	}
	result[node].Add(usage)
}

// removePodUsage removes a pod entry and its node usage from the cache.
// Returns the node name if the pod was found, empty string otherwise.
// Must be called under write lock.
func (n *nonTasUsageCache) removePodUsage(key client.ObjectKey, log logr.Logger) string {
	if old, found := n.podUsage[key]; found {
		n.removeNodeUsage(old.node, old.usage, log)
		delete(n.podUsage, key)
		return old.node
	}
	delete(n.podUsage, key)
	return ""
}

// update may add a pod to the cache, or delete a terminated pod.
// Returns the node name when capacity may have been freed on a node.
func (n *nonTasUsageCache) update(pod *corev1.Pod, log logr.Logger) string {
	n.lock.Lock()
	defer n.lock.Unlock()

	key := client.ObjectKeyFromObject(pod)

	if utilpod.IsTerminated(pod) {
		log.V(5).Info("Deleting terminated pod from the cache")
		return n.removePodUsage(key, log)
	}

	return n.updatePodUsage(key, pod, log)
}

// updatePodUsage replaces or inserts a pod's usage entry and adjusts node totals.
// Returns the old node name only when capacity may have been freed (node
// migration or a decrease in any resource request).
// Must be called under write lock.
func (n *nonTasUsageCache) updatePodUsage(key client.ObjectKey, pod *corev1.Pod, log logr.Logger) string {
	var oldNode string
	var oldUsage resources.Requests
	if old, found := n.podUsage[key]; found {
		n.removeNodeUsage(old.node, old.usage, log)
		oldNode = old.node
		oldUsage = old.usage
	}
	log.V(5).Info("Adding non-TAS pod to the cache")
	requests := resources.NewRequestsFromPodSpec(&pod.Spec)
	n.podUsage[key] = podUsageValue{
		node:  pod.Spec.NodeName,
		usage: requests,
	}
	n.addNodeUsage(pod.Spec.NodeName, requests)
	if oldNode == "" {
		return ""
	}
	if oldNode != pod.Spec.NodeName || len(oldUsage.GreaterKeys(requests)) > 0 {
		return oldNode
	}
	return ""
}

// delete removes a pod from the cache.
// Returns the node name when an entry is removed.
func (n *nonTasUsageCache) delete(key client.ObjectKey, log logr.Logger) string {
	n.lock.Lock()
	defer n.lock.Unlock()
	return n.removePodUsage(key, log)
}

// forEachNodeUsage invokes fn for each node's usage while holding the read lock.
// usage is the live cache entry, not a copy: fn must only read it, and must not
// mutate or retain it beyond the call. Clone it if a longer-lived copy is needed.
func (n *nonTasUsageCache) forEachNodeUsage(fn func(node string, usage resources.Requests)) {
	n.lock.RLock()
	defer n.lock.RUnlock()
	for node, reqs := range n.nodeUsage {
		fn(node, reqs)
	}
}

// addNodeUsage increments the pre-aggregated per-node usage.
// Must be called under write lock.
func (n *nonTasUsageCache) addNodeUsage(node string, usage resources.Requests) {
	if _, found := n.nodeUsage[node]; !found {
		n.nodeUsage[node] = resources.NewRequests()
	}
	n.nodeUsage[node].Add(usage)
	n.nodeUsage[node].Add(resources.OnePodRequest)
}

// removeNodeUsage decrements the pre-aggregated per-node usage.
// Must be called under write lock.
func (n *nonTasUsageCache) removeNodeUsage(node string, usage resources.Requests, log logr.Logger) {
	existing, found := n.nodeUsage[node]
	if !found {
		return
	}
	existing.Sub(usage)
	existing.Sub(resources.OnePodRequest)
	if pods := existing.ResourceValue(corev1.ResourcePods); pods.Sign() <= 0 {
		if pods.Sign() < 0 {
			log.V(0).Info("Unexpected negative pod count in nodeUsage", "node", node, "podCount", pods)
		}
		delete(n.nodeUsage, node)
	}
}

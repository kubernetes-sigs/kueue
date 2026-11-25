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

package dra

import (
	"slices"

	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/podtracker"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workload/concurrentadmission"
)

// podOwners resolves which admitted Workload a Pod, and so a claim, belongs to.
type podOwners struct {
	pods *podtracker.Tracker
	// aliases maps the Workload names on Pods to the admitted Workloads they may mean.
	aliases   map[workload.Reference][]*kueue.Workload
	ownership PodOwnership
	// matchers are the ownership rule of each admitted Workload, built on first use.
	matchers map[*kueue.Workload]PodMatcher
}

func newPodOwners(state SnapshotState) podOwners {
	owners := podOwners{
		pods:      state.Pods,
		aliases:   workloadAliases(state.AssumedWorkloads),
		ownership: state.Ownership,
		matchers:  make(map[*kueue.Workload]PodMatcher),
	}
	if owners.pods == nil {
		owners.pods = &podtracker.Tracker{}
	}
	if owners.ownership == nil {
		owners.ownership = func(*kueue.Workload) PodMatcher {
			return func(*corev1.Pod) (bool, bool) { return true, true }
		}
	}
	return owners
}

// claimIsFreeing reports whether the claim is being deallocated: none of its Pods still
// runs, or it is deleted and nothing uses it. A Pod that is still being bound is not
// tracked either, so this only ever makes a preemptor wait.
func (o *podOwners) claimIsFreeing(claim *resourceapi.ResourceClaim, pods []resourceapi.ResourceClaimConsumerReference) bool {
	if len(claim.Status.ReservedFor) == 0 {
		return claim.DeletionTimestamp != nil
	}
	// A claim used by something other than Pods is not freed when Pods go.
	if len(pods) == 0 {
		return false
	}
	for _, ref := range pods {
		if pod := o.pods.Pod(client.ObjectKey{Namespace: claim.Namespace, Name: ref.Name}); pod != nil && pod.UID == ref.UID {
			return false
		}
	}
	return true
}

// claimPods returns the Pods that use the claim: the ones it is reserved for, or, between
// allocation and reservation, the Pod that owns a template claim. A claim that something
// other than Pods also uses gets none, so no Workload frees it.
func claimPods(claim *resourceapi.ResourceClaim) []resourceapi.ResourceClaimConsumerReference {
	var pods []resourceapi.ResourceClaimConsumerReference
	for _, ref := range claim.Status.ReservedFor {
		if ref.Resource != "pods" || ref.APIGroup != "" {
			return nil
		}
		pods = append(pods, ref)
	}
	if len(claim.Status.ReservedFor) > 0 {
		return pods
	}
	if owner := metav1.GetControllerOf(claim); owner != nil && owner.Kind == "Pod" && owner.APIVersion == "v1" {
		pods = append(pods, resourceapi.ResourceClaimConsumerReference{Resource: "pods", Name: owner.Name, UID: owner.UID})
	}
	return pods
}

// PodOwnership returns the rule that tells whether a Pod belongs to the admitted
// Workload. It is the rule that TAS uses for the Pods of released reservations.
type PodOwnership func(wl *kueue.Workload) PodMatcher

// PodMatcher tells whether a Pod belongs to one Workload. covers allows an ambiguous
// owner chain, so that a Pod is not counted twice. owns needs an exact owner match.
type PodMatcher func(pod *corev1.Pod) (covers, owns bool)

func (o *podOwners) matcher(wl *kueue.Workload) PodMatcher {
	m, found := o.matchers[wl]
	if !found {
		m = o.ownership(wl)
		o.matchers[wl] = m
	}
	return m
}

// workloadAliases maps the Workload names a Pod can carry to the admitted Workloads
// it may belong to. An elastic Job's Pods can name an earlier slice, and the slice name
// is shared by the chain. A ConcurrentAdmission variant's Pods name its parent.
func workloadAliases(assumed []*kueue.Workload) map[workload.Reference][]*kueue.Workload {
	aliases := make(map[workload.Reference][]*kueue.Workload, len(assumed))
	for _, wl := range assumed {
		wlKey := workload.Key(wl)
		aliases[wlKey] = append(aliases[wlKey], wl)
		for _, name := range []string{wl.Annotations[kueue.WorkloadSliceNameAnnotation], concurrentadmission.GetParentWorkloadName(wl)} {
			if ref := workload.NewReference(wl.Namespace, name); name != "" && ref != wlKey {
				aliases[ref] = append(aliases[ref], wl)
			}
		}
	}
	return aliases
}

// podWorkloads returns the admitted Workloads the Pod belongs to, by an exact owner match
// when exact. A Pod of a Workload that is not admitted, such as one being evicted,
// belongs to the Workload it names.
func (o *podOwners) podWorkloads(pod *corev1.Pod, exact bool) []workload.Reference {
	var refs []workload.Reference
	for _, annotation := range []string{kueue.WorkloadAnnotation, kueue.WorkloadSliceNameAnnotation} {
		if name := pod.Annotations[annotation]; name != "" {
			refs = append(refs, workload.NewReference(pod.Namespace, name))
		}
	}
	if len(refs) == 0 {
		return nil
	}
	var wlKeys []workload.Reference
	candidates := 0
	for _, ref := range refs {
		for _, wl := range o.aliases[ref] {
			candidates++
			wlKey := workload.Key(wl)
			if slices.Contains(wlKeys, wlKey) {
				continue
			}
			if covers, owns := o.matcher(wl)(pod); (exact && owns) || (!exact && covers) {
				wlKeys = append(wlKeys, wlKey)
			}
		}
	}
	if candidates == 0 {
		return refs[:1]
	}
	return wlKeys
}

// claimWorkloads returns the Workloads whose Pods use the claim, by an exact owner match.
// Pods of different Workloads, or a Pod Kueue does not know, give none, so preemption
// never frees the claim.
func (o *podOwners) claimWorkloads(namespace string, pods []resourceapi.ResourceClaimConsumerReference) []workload.Reference {
	var wlKeys []workload.Reference
	for i, ref := range pods {
		pod := o.pods.Pod(client.ObjectKey{Namespace: namespace, Name: ref.Name})
		if pod == nil || pod.UID != ref.UID {
			return nil
		}
		podKeys := o.podWorkloads(pod, true)
		if len(podKeys) == 0 || (i > 0 && !slices.Equal(podKeys, wlKeys)) {
			return nil
		}
		wlKeys = podKeys
	}
	return wlKeys
}

// boundPods counts the bound Pods that use devices of each admitted Workload, by PodSet
// and node. A Pod being deleted is left out: it can be left over from an earlier
// admission of the same Workload, and its claims hold its devices until it is gone.
// So is a Pod whose claims are not seen allocated yet: its promise holds its devices.
func (o *podOwners) boundPods(usesDevices func(*corev1.PodSpec) bool, claimsAllocated func(*corev1.Pod) bool, binding []*corev1.Pod) map[workload.Reference]map[kueue.PodSetReference]map[string]int32 {
	bound := make(map[workload.Reference]map[kueue.PodSetReference]map[string]int32)
	// A Pod is in both inputs when the tracker adds it after binding was read, and a Pod
	// with several claims is in binding once for each. Either way it counts once.
	seen := sets.New[types.UID]()
	for _, pod := range append(o.pods.Pods(), binding...) {
		if seen.Has(pod.UID) {
			continue
		}
		seen.Insert(pod.UID)
		// Promises are only for PodSets that use devices, so other Pods need no owner.
		if pod.DeletionTimestamp != nil || !usesDevices(&pod.Spec) || !claimsAllocated(pod) {
			continue
		}
		psName := kueue.PodSetReference(pod.Labels[constants.PodSetLabel])
		for _, wlKey := range o.podWorkloads(pod, false) {
			if bound[wlKey] == nil {
				bound[wlKey] = make(map[kueue.PodSetReference]map[string]int32)
			}
			if bound[wlKey][psName] == nil {
				bound[wlKey][psName] = make(map[string]int32)
			}
			bound[wlKey][psName][pod.Spec.NodeName]++
		}
	}
	return bound
}

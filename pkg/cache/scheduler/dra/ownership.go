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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/podtracker"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workload/concurrentadmission"
)

// podOwners resolves which admitted Workload a Pod belongs to.
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
			return func(*corev1.Pod) bool { return true }
		}
	}
	return owners
}

// PodOwnership returns the rule that tells whether a Pod belongs to the admitted
// Workload. It is the rule that TAS uses for the Pods of released reservations.
type PodOwnership func(wl *kueue.Workload) PodMatcher

// PodMatcher tells whether a Pod belongs to one Workload. It allows an ambiguous owner
// chain, so that a Pod is not counted twice.
type PodMatcher func(pod *corev1.Pod) bool

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

// podWorkloads returns the admitted Workloads the Pod belongs to. A Pod of a Workload that is not admitted, such as one being evicted,
// belongs to the Workload it names.
func (o *podOwners) podWorkloads(pod *corev1.Pod) []workload.Reference {
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
			if o.matcher(wl)(pod) {
				wlKeys = append(wlKeys, wlKey)
			}
		}
	}
	if candidates == 0 {
		return refs[:1]
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
		for _, wlKey := range o.podWorkloads(pod) {
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

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
	"slices"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

// HasSelfAntiAffinityOnHostname reports whether a static required anti-affinity
// selector matches another Pod created from the same template on a hostname.
// Namespace selectors and dynamic match/mismatch label keys are not evaluated;
// those constraints remain kube-scheduler's responsibility.
func HasSelfAntiAffinityOnHostname(template *corev1.PodTemplateSpec, namespace string) bool {
	if template == nil || template.Spec.Affinity == nil || template.Spec.Affinity.PodAntiAffinity == nil {
		return false
	}
	for _, term := range template.Spec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution {
		if term.TopologyKey != corev1.LabelHostname || term.LabelSelector == nil ||
			term.NamespaceSelector != nil || len(term.MatchLabelKeys) > 0 || len(term.MismatchLabelKeys) > 0 ||
			(len(term.Namespaces) > 0 && !slices.Contains(term.Namespaces, namespace)) {
			continue
		}
		selector, err := metav1.LabelSelectorAsSelector(term.LabelSelector)
		if err == nil && selector.Matches(labels.Set(template.Labels)) {
			return true
		}
	}
	return false
}

// PinnedSelfAntiAffinityConflict returns a PodSet and hostname when the
// Workload's Pods cannot run concurrently under its own placement constraints.
func PinnedSelfAntiAffinityConflict(wl *kueue.Workload) (kueue.PodSetReference, string, bool) {
	if wl == nil || wl.Status.Admission == nil {
		return "", "", false
	}
	for _, ps := range wl.Spec.PodSets {
		pinnedHostname := ps.Template.Spec.NodeSelector[corev1.LabelHostname]
		if pinnedHostname == "" || !HasSelfAntiAffinityOnHostname(&ps.Template, wl.Namespace) {
			continue
		}
		for _, psa := range wl.Status.Admission.PodSetAssignments {
			if psa.Name != ps.Name || psa.TopologyAssignment == nil {
				continue
			}
			if hostname, ok := utiltas.SingleHostnameWithMultiplePods(psa.TopologyAssignment); ok && hostname == pinnedHostname {
				return ps.Name, hostname, true
			}
		}
	}
	return "", "", false
}

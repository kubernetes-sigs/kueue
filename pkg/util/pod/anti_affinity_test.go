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
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestHasSelfAntiAffinityOnHostname(t *testing.T) {
	cases := map[string]struct {
		term corev1.PodAffinityTerm
		want bool
	}{
		"matching own pods on hostname": {
			term: corev1.PodAffinityTerm{TopologyKey: corev1.LabelHostname, LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "test"}}},
			want: true,
		},
		"other pods do not match": {
			term: corev1.PodAffinityTerm{TopologyKey: corev1.LabelHostname, LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "other"}}},
		},
		"other namespace does not match": {
			term: corev1.PodAffinityTerm{TopologyKey: corev1.LabelHostname, Namespaces: []string{"other"}, LabelSelector: &metav1.LabelSelector{}},
		},
		"different topology does not match": {
			term: corev1.PodAffinityTerm{TopologyKey: "topology.kubernetes.io/zone", LabelSelector: &metav1.LabelSelector{}},
		},
		"nil selector matches no pods": {
			term: corev1.PodAffinityTerm{TopologyKey: corev1.LabelHostname},
		},
		"dynamic selector is not guessed": {
			term: corev1.PodAffinityTerm{TopologyKey: corev1.LabelHostname, LabelSelector: &metav1.LabelSelector{}, MatchLabelKeys: []string{"app"}},
		},
		"namespace selector is not evaluated": {
			term: corev1.PodAffinityTerm{TopologyKey: corev1.LabelHostname, LabelSelector: &metav1.LabelSelector{}, NamespaceSelector: &metav1.LabelSelector{}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			template := &corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Affinity: &corev1.Affinity{
					PodAntiAffinity: &corev1.PodAntiAffinity{RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{tc.term}},
				}},
			}
			template.Labels = map[string]string{"app": "test"}
			if got := HasSelfAntiAffinityOnHostname(template, "default"); got != tc.want {
				t.Errorf("HasSelfAntiAffinityOnHostname() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestPinnedSelfAntiAffinityConflict(t *testing.T) {
	ps := utiltestingapi.MakePodSet("main", 2).
		Labels(map[string]string{"app": "test"}).
		NodeSelector(map[string]string{corev1.LabelHostname: "node-a"}).Obj()
	ps.Template.Spec.Affinity = &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
			TopologyKey:   corev1.LabelHostname,
			LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "test"}},
		}},
	}}
	tests := map[string]struct {
		pinnedHostname string
		assignment     *kueue.TopologyAssignment
		wantConflict   bool
	}{
		"two Pods assigned to pinned hostname": {
			pinnedHostname: "node-a",
			assignment: utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
				Domain(utiltas.TopologyDomainAssignment{Values: []string{"node-a"}, Count: 2}).Obj(),
			wantConflict: true,
		},
		"not pinned": {
			assignment: utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
				Domain(utiltas.TopologyDomainAssignment{Values: []string{"node-a"}, Count: 2}).Obj(),
		},
		"different assigned hostname": {
			pinnedHostname: "node-a",
			assignment: utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
				Domain(utiltas.TopologyDomainAssignment{Values: []string{"node-b"}, Count: 2}).Obj(),
		},
		"Pods split across nodes": {
			pinnedHostname: "node-a",
			assignment: utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
				Domain(utiltas.TopologyDomainAssignment{Values: []string{"node-a"}, Count: 1}).
				Domain(utiltas.TopologyDomainAssignment{Values: []string{"node-b"}, Count: 1}).Obj(),
		},
		"only one Pod": {
			pinnedHostname: "node-a",
			assignment: utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
				Domain(utiltas.TopologyDomainAssignment{Values: []string{"node-a"}, Count: 1}).Obj(),
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			podSet := ps.DeepCopy()
			if tc.pinnedHostname == "" {
				delete(podSet.Template.Spec.NodeSelector, corev1.LabelHostname)
			} else {
				podSet.Template.Spec.NodeSelector[corev1.LabelHostname] = tc.pinnedHostname
			}
			wl := utiltestingapi.MakeWorkload("wl", "ns").PodSets(*podSet).Obj()
			wl.Status.Admission = utiltestingapi.MakeAdmission("cq").PodSets(
				utiltestingapi.MakePodSetAssignment("main").TopologyAssignment(tc.assignment).Obj(),
			).Obj()
			_, _, got := PinnedSelfAntiAffinityConflict(wl)
			if got != tc.wantConflict {
				t.Errorf("PinnedSelfAntiAffinityConflict() = %t, want %t", got, tc.wantConflict)
			}
		})
	}
}

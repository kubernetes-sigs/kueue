//go:build !exclude_scheduler_library

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

package was

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestVirtualPodName(t *testing.T) {
	tests := map[string]struct {
		wlName     string
		podSetName string
		index      int
		wantPrefix string
		checkLen   bool
	}{
		"short name": {
			wlName:     "wl-1",
			podSetName: "main",
			index:      0,
			wantPrefix: "virtual-wl-1-main-0-",
		},
		"long name capped to 253 chars": {
			wlName:     strings.Repeat("a", 200),
			podSetName: strings.Repeat("b", 100),
			index:      42,
			checkLen:   true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := VirtualPodName(tc.wlName, tc.podSetName, tc.index)
			if tc.wantPrefix != "" && !strings.HasPrefix(got, tc.wantPrefix) {
				t.Errorf("VirtualPodName() = %q, want prefix %q", got, tc.wantPrefix)
			}
			if tc.checkLen && len(got) > 253 {
				t.Errorf("VirtualPodName() length = %d, exceeds 253", len(got))
			}
		})
	}
}

func TestPodsForWorkload(t *testing.T) {
	podTemplate := corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:  "c",
				Ports: []corev1.ContainerPort{{HostPort: 8080}},
			}},
		},
	}

	wlWithTAS := utiltestingapi.MakeWorkload("wl", "test-ns").
		UID("wl-uid").
		PodSets(kueue.PodSet{Name: "main", Template: podTemplate, Count: 3}).
		Admission(
			utiltestingapi.MakeAdmission("cq").
				PodSets(kueue.PodSetAssignment{
					Name:  "main",
					Count: ptr.To[int32](3),
					TopologyAssignment: utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
						Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"node-1"}, 2).Obj()).
						Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"node-2"}, 1).Obj()).
						Obj(),
				}).
				Obj(),
		).
		Obj()

	tests := map[string]struct {
		wl        *kueue.Workload
		wantNodes []string
	}{
		"nil workload": {
			wl: nil,
		},
		"unadmitted workload": {
			wl: utiltestingapi.MakeWorkload("wl", "default").Obj(),
		},
		"workload with TAS placement": {
			wl:        wlWithTAS,
			wantNodes: []string{"node-1", "node-1", "node-2"},
		},
		"finished workload": {
			wl: utiltestingapi.MakeWorkload("wl", "test-ns").
				UID("wl-uid").
				PodSets(kueue.PodSet{Name: "main", Template: podTemplate, Count: 3}).
				Admission(
					utiltestingapi.MakeAdmission("cq").
						PodSets(kueue.PodSetAssignment{
							Name:  "main",
							Count: ptr.To[int32](3),
							TopologyAssignment: utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"node-1"}, 3).Obj()).
								Obj(),
						}).
						Obj(),
				).
				Condition(metav1.Condition{
					Type:   kueue.WorkloadFinished,
					Status: metav1.ConditionTrue,
				}).
				Obj(),
		},
		"workload without hostname in TAS levels": {
			wl: utiltestingapi.MakeWorkload("wl", "test-ns").
				UID("wl-uid").
				PodSets(kueue.PodSet{Name: "main", Template: podTemplate, Count: 3}).
				Admission(
					utiltestingapi.MakeAdmission("cq").
						PodSets(kueue.PodSetAssignment{
							Name:  "main",
							Count: ptr.To[int32](3),
							TopologyAssignment: utiltestingapi.MakeTopologyAssignment([]string{"topology.kubernetes.io/zone"}).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"zone-a"}, 3).Obj()).
								Obj(),
						}).
						Obj(),
				).
				Obj(),
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := PodsForWorkload(tc.wl)
			if len(got) != len(tc.wantNodes) {
				t.Fatalf("Got %d pods, want %d", len(got), len(tc.wantNodes))
			}
			for i, wantNode := range tc.wantNodes {
				pod := got[i]
				if pod.Spec.NodeName != wantNode {
					t.Errorf("pod[%d].Spec.NodeName = %q, want %q", i, pod.Spec.NodeName, wantNode)
				}
				if pod.Namespace != tc.wl.Namespace || pod.Annotations[kueue.WorkloadAnnotation] != tc.wl.Name {
					t.Errorf("pod[%d] metadata not wired correctly", i)
				}
				if pod.Labels[constants.PodSetLabel] != "main" || pod.Status.Phase != corev1.PodRunning {
					t.Errorf("pod[%d] labels or phase not set correctly", i)
				}
				if pod.Spec.Containers[0].Ports[0].HostPort != 8080 {
					t.Errorf("pod[%d] container specs not preserved", i)
				}
			}
		})
	}
}

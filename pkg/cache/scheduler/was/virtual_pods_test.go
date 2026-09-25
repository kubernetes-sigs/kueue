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
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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
		"long name preserves distinct replica index": {
			wlName:     strings.Repeat("a", 200),
			podSetName: strings.Repeat("b", 100),
			index:      1803,
			checkLen:   true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := virtualPodName(tc.wlName, tc.podSetName, tc.index)
			if tc.wantPrefix != "" && !strings.HasPrefix(got, tc.wantPrefix) {
				t.Errorf("virtualPodName() = %q, want prefix %q", got, tc.wantPrefix)
			}
			if tc.checkLen && len(got) > 253 {
				t.Errorf("virtualPodName() length = %d, exceeds 253", len(got))
			}
		})
	}

	t.Run("distinct indices produce distinct names on long names", func(t *testing.T) {
		longWl := strings.Repeat("a", 220)
		longPs := strings.Repeat("b", 63)
		name1 := virtualPodName(longWl, longPs, 1803)
		name2 := virtualPodName(longWl, longPs, 1876)
		if name1 == name2 {
			t.Errorf("virtualPodName() collision between index 1803 and 1876: %q", name1)
		}
	})
}

func TestVirtualPodsForWorkload(t *testing.T) {
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
			got := VirtualPodsForWorkload(tc.wl)
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
				if pod.Labels[constants.PodSetLabel] != "main" {
					t.Errorf("pod[%d] PodSetLabel = %q, want %q", i, pod.Labels[constants.PodSetLabel], "main")
				}
				if pod.Status.Phase != corev1.PodRunning {
					t.Errorf("pod[%d].Status.Phase = %v, want %v", i, pod.Status.Phase, corev1.PodRunning)
				}
				if pod.Spec.Containers[0].Ports[0].HostPort != 8080 {
					t.Errorf("pod[%d] container specs not preserved", i)
				}
			}
		})
	}
}

func TestBuildCandidatePodValidation(t *testing.T) {
	baseWl := utiltestingapi.MakeWorkload("wl", "test-ns").Obj()
	basePs := &kueue.PodSet{Name: "main"}

	tests := map[string]struct {
		wl      *kueue.Workload
		ps      *kueue.PodSet
		wantErr string
	}{
		"nil workload returns error": {
			wl:      nil,
			ps:      basePs,
			wantErr: "workload and podset must be non-nil",
		},
		"nil podset returns error": {
			wl:      baseWl,
			ps:      nil,
			wantErr: "workload and podset must be non-nil",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := CandidateVirtualPodsForPodSet(tc.wl, tc.ps, 1, CandidatePodOptions{})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("CandidateVirtualPodsForPodSet() error = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}

func TestCandidateVirtualPodsForPodSetMetadataAndStatus(t *testing.T) {
	wl := utiltestingapi.MakeWorkload("wl", "test-ns").UID("wl-uid").Obj()
	ps := &kueue.PodSet{
		Name: "workers",
		Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{
				Labels:      map[string]string{"app": "train"},
				Annotations: map[string]string{"user": "alice"},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "c"}},
			},
		},
		Count: 3,
	}

	pods, err := CandidateVirtualPodsForPodSet(wl, ps, 3, CandidatePodOptions{})
	if err != nil {
		t.Fatalf("CandidateVirtualPodsForPodSet() unexpected error: %v", err)
	}
	if len(pods) != 3 {
		t.Fatalf("len(pods) = %d, want 3", len(pods))
	}

	pod := pods[2]
	if pod.Status.Phase != corev1.PodPending {
		t.Errorf("pod.Status.Phase = %v, want %v", pod.Status.Phase, corev1.PodPending)
	}
	if pod.Spec.NodeName != "" {
		t.Errorf("pod.Spec.NodeName = %q, want empty", pod.Spec.NodeName)
	}
	if pod.Namespace != "test-ns" {
		t.Errorf("pod.Namespace = %q, want %q", pod.Namespace, "test-ns")
	}
	wantUID := types.UID("virtual-wl-uid-workers-2")
	if pod.UID != wantUID {
		t.Errorf("pod.UID = %q, want %q", pod.UID, wantUID)
	}
	wantLabels := map[string]string{
		"app":                 "train",
		constants.PodSetLabel: "workers",
	}
	if diff := cmp.Diff(wantLabels, pod.Labels); diff != "" {
		t.Errorf("Unexpected labels (-want +got):\n%s", diff)
	}
	wantAnnotations := map[string]string{
		"user":                   "alice",
		kueue.WorkloadAnnotation: "wl",
	}
	if diff := cmp.Diff(wantAnnotations, pod.Annotations); diff != "" {
		t.Errorf("Unexpected annotations (-want +got):\n%s", diff)
	}
}

func TestCandidateVirtualPodsForPodSetNodeSelector(t *testing.T) {
	wl := utiltestingapi.MakeWorkload("wl", "default").Obj()

	tests := map[string]struct {
		podSetSelector   map[string]string
		updateSelector   map[string]string
		flavorNodeLabels map[string]string
		wantSelector     map[string]string
		wantErr          string
	}{
		"merge nodeSelector from PodSet, PodSetUpdate, and Flavor": {
			podSetSelector:   map[string]string{"arch": "amd64"},
			updateSelector:   map[string]string{"zone": "us-central1-a"},
			flavorNodeLabels: map[string]string{"instance-type": "a2"},
			wantSelector: map[string]string{
				"arch":          "amd64",
				"zone":          "us-central1-a",
				"instance-type": "a2",
			},
		},
		"conflict between PodSet and PodSetUpdate returns error": {
			podSetSelector: map[string]string{"arch": "amd64"},
			updateSelector: map[string]string{"arch": "arm64"},
			wantErr:        "nodeSelector conflict between PodSet and PodSetUpdate",
		},
		"conflict between PodSet and Flavor returns error": {
			podSetSelector:   map[string]string{"gpu": "a100"},
			flavorNodeLabels: map[string]string{"gpu": "t4"},
			wantErr:          "nodeSelector conflict between PodSet and ResourceFlavor",
		},
		"conflict between PodSetUpdate and Flavor returns error": {
			updateSelector:   map[string]string{"tier": "standard"},
			flavorNodeLabels: map[string]string{"tier": "premium"},
			wantErr:          "nodeSelector conflict between PodSet and ResourceFlavor",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ps := &kueue.PodSet{
				Name: "main",
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{NodeSelector: tc.podSetSelector},
				},
			}
			opts := CandidatePodOptions{
				FlavorNodeLabels: tc.flavorNodeLabels,
			}
			if tc.updateSelector != nil {
				opts.PodSetUpdates = []kueue.PodSetUpdate{{NodeSelector: tc.updateSelector}}
			}

			pods, err := CandidateVirtualPodsForPodSet(wl, ps, 1, opts)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("CandidateVirtualPodsForPodSet() error = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("CandidateVirtualPodsForPodSet() unexpected error: %v", err)
			}
			if diff := cmp.Diff(tc.wantSelector, pods[0].Spec.NodeSelector); diff != "" {
				t.Errorf("Unexpected NodeSelector (-want +got):\n%s", diff)
			}
		})
	}
}

func TestCandidateVirtualPodsForPodSetTolerations(t *testing.T) {
	wl := utiltestingapi.MakeWorkload("wl", "default").Obj()
	ps := &kueue.PodSet{
		Name: "main",
		Template: corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Tolerations: []corev1.Toleration{
					{Key: "t1", Operator: corev1.TolerationOpEqual, Value: "v1", Effect: corev1.TaintEffectNoSchedule},
				},
			},
		},
	}
	opts := CandidatePodOptions{
		FlavorTolerations: []corev1.Toleration{
			{Key: "flavor-taint", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
			{Key: "t1", Operator: corev1.TolerationOpEqual, Value: "v1", Effect: corev1.TaintEffectNoSchedule}, // duplicate
		},
		PodSetUpdates: []kueue.PodSetUpdate{
			{Tolerations: []corev1.Toleration{
				{Key: "update-taint", Operator: corev1.TolerationOpEqual, Value: "v2", Effect: corev1.TaintEffectNoExecute},
			}},
		},
	}

	pods, err := CandidateVirtualPodsForPodSet(wl, ps, 1, opts)
	if err != nil {
		t.Fatalf("CandidateVirtualPodsForPodSet() unexpected error: %v", err)
	}

	wantTolerations := []corev1.Toleration{
		{Key: "t1", Operator: corev1.TolerationOpEqual, Value: "v1", Effect: corev1.TaintEffectNoSchedule},
		{Key: "flavor-taint", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
		{Key: "update-taint", Operator: corev1.TolerationOpEqual, Value: "v2", Effect: corev1.TaintEffectNoExecute},
	}
	if diff := cmp.Diff(wantTolerations, pods[0].Spec.Tolerations, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("Unexpected Tolerations (-want +got):\n%s", diff)
	}
}

func TestCandidateVirtualPodsForPodSetImmutability(t *testing.T) {
	wl := utiltestingapi.MakeWorkload("wl", "default").Obj()
	ps := &kueue.PodSet{
		Name: "main",
		Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{"orig": "label"},
			},
			Spec: corev1.PodSpec{
				NodeSelector: map[string]string{"orig": "selector"},
				Tolerations:  []corev1.Toleration{{Key: "orig"}},
			},
		},
	}
	opts := CandidatePodOptions{
		FlavorNodeLabels:  map[string]string{"flavor": "label"},
		FlavorTolerations: []corev1.Toleration{{Key: "flavor"}},
	}

	pods, err := CandidateVirtualPodsForPodSet(wl, ps, 1, opts)
	if err != nil {
		t.Fatalf("CandidateVirtualPodsForPodSet() unexpected error: %v", err)
	}

	pod := pods[0]
	pod.Labels["new"] = "label"
	pod.Spec.NodeSelector["new"] = "selector"
	pod.Spec.Tolerations = append(pod.Spec.Tolerations, corev1.Toleration{Key: "new"})

	if _, exists := ps.Template.Labels["new"]; exists {
		t.Error("PodSet template labels were mutated")
	}
	if _, exists := ps.Template.Spec.NodeSelector["new"]; exists {
		t.Error("PodSet template nodeSelector was mutated")
	}
	if len(ps.Template.Spec.Tolerations) != 1 {
		t.Errorf("PodSet template tolerations len = %d, want 1", len(ps.Template.Spec.Tolerations))
	}
}

func TestCandidateVirtualPodsForPodSetCount(t *testing.T) {
	wl := utiltestingapi.MakeWorkload("wl", "test-ns").UID("wl-uid").Obj()
	ps := &kueue.PodSet{
		Name: "workers",
		Template: corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "c"}},
			},
		},
		Count: 5,
	}
	opts := CandidatePodOptions{
		FlavorNodeLabels: map[string]string{"instance-type": "a2"},
	}

	// Request 3 replicas (e.g. partial admission) even though ps.Count is 5
	pods, err := CandidateVirtualPodsForPodSet(wl, ps, 3, opts)
	if err != nil {
		t.Fatalf("CandidateVirtualPodsForPodSet() unexpected error: %v", err)
	}

	if len(pods) != 3 {
		t.Fatalf("len(pods) = %d, want 3", len(pods))
	}

	for i, pod := range pods {
		wantUID := types.UID(fmt.Sprintf("virtual-wl-uid-workers-%d", i))
		if pod.UID != wantUID {
			t.Errorf("pod[%d].UID = %q, want %q", i, pod.UID, wantUID)
		}
		if pod.Spec.NodeSelector["instance-type"] != "a2" {
			t.Errorf("pod[%d] missing flavor nodeSelector", i)
		}
	}
}

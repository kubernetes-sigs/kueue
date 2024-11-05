/*
Copyright 2023 The Kubernetes Authors.

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
package podset

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestFromAssignment(t *testing.T) {
	toleration1 := corev1.Toleration{
		Key:      "t1k",
		Operator: corev1.TolerationOpEqual,
		Value:    "t1v",
		Effect:   corev1.TaintEffectNoExecute,
	}
	toleration2 := corev1.Toleration{
		Key:      "t2k",
		Operator: corev1.TolerationOpExists,
		Effect:   corev1.TaintEffectNoSchedule,
	}
	toleration3 := corev1.Toleration{
		Key:      "t3k",
		Operator: corev1.TolerationOpEqual,
		Value:    "t3v",
		Effect:   corev1.TaintEffectPreferNoSchedule,
	}

	flavor1 := utiltesting.MakeResourceFlavor("flavor1").
		NodeLabel("f1l1", "f1v1").
		NodeLabel("f1l2", "f1v2").
		Toleration(*toleration1.DeepCopy()).
		Toleration(*toleration2.DeepCopy()).
		Obj()

	flavor2 := utiltesting.MakeResourceFlavor("flavor2").
		NodeLabel("f2l1", "f2v1").
		NodeLabel("f2l2", "f2v2").
		Toleration(*toleration3.DeepCopy()).
		Obj()

	cases := map[string]struct {
		enableTopologyAwareScheduling bool

		assignment   *kueue.PodSetAssignment
		defaultCount int32
		flavors      []kueue.ResourceFlavor
		wantError    error
		wantInfo     PodSetInfo
	}{
		"single flavor": {
			assignment: &kueue.PodSetAssignment{
				Name: "name",
				Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
					corev1.ResourceCPU: kueue.ResourceFlavorReference(flavor1.Name),
				},
				Count: ptr.To[int32](2),
			},
			defaultCount: 4,
			flavors:      []kueue.ResourceFlavor{*flavor1.DeepCopy()},
			wantInfo: PodSetInfo{
				Name:  "name",
				Count: 2,
				NodeSelector: map[string]string{
					"f1l1": "f1v1",
					"f1l2": "f1v2",
				},
				Tolerations: []corev1.Toleration{*toleration1.DeepCopy(), *toleration2.DeepCopy()},
			},
		},
		"multiple flavors": {
			assignment: &kueue.PodSetAssignment{
				Name: "name",
				Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
					corev1.ResourceCPU:    kueue.ResourceFlavorReference(flavor1.Name),
					corev1.ResourceMemory: kueue.ResourceFlavorReference(flavor2.Name),
				},
				Count: ptr.To[int32](2),
			},
			defaultCount: 4,
			flavors:      []kueue.ResourceFlavor{*flavor1.DeepCopy(), *flavor2.DeepCopy()},
			wantInfo: PodSetInfo{
				Name:  "name",
				Count: 2,
				NodeSelector: map[string]string{
					"f1l1": "f1v1",
					"f1l2": "f1v2",
					"f2l1": "f2v1",
					"f2l2": "f2v2",
				},
				Tolerations: []corev1.Toleration{*toleration1.DeepCopy(), *toleration2.DeepCopy(), *toleration3.DeepCopy()},
			},
		},
		"duplicate flavor": {
			assignment: &kueue.PodSetAssignment{
				Name: "name",
				Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
					corev1.ResourceCPU:    kueue.ResourceFlavorReference(flavor1.Name),
					corev1.ResourceMemory: kueue.ResourceFlavorReference(flavor1.Name),
				},
				Count: ptr.To[int32](2),
			},
			defaultCount: 4,
			flavors:      []kueue.ResourceFlavor{*flavor1.DeepCopy(), *flavor2.DeepCopy()},
			wantInfo: PodSetInfo{
				Name:  "name",
				Count: 2,
				NodeSelector: map[string]string{
					"f1l1": "f1v1",
					"f1l2": "f1v2",
				},
				Tolerations: []corev1.Toleration{*toleration1.DeepCopy(), *toleration2.DeepCopy()},
			},
		},
		"flavor not found": {
			assignment: &kueue.PodSetAssignment{
				Name: "name",
				Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
					corev1.ResourceCPU: kueue.ResourceFlavorReference(flavor1.Name),
				},
				Count: ptr.To[int32](2),
			},
			defaultCount: 4,
			wantError:    apierrors.NewNotFound(schema.GroupResource{Group: kueue.GroupVersion.Group, Resource: "resourceflavors"}, "flavor1"),
		},
		"default count": {
			assignment: &kueue.PodSetAssignment{
				Name: "name",
				Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
					corev1.ResourceCPU: kueue.ResourceFlavorReference(flavor1.Name),
				},
			},
			defaultCount: 4,
			flavors:      []kueue.ResourceFlavor{*flavor1.DeepCopy()},
			wantInfo: PodSetInfo{
				Name:  "name",
				Count: 4,
				NodeSelector: map[string]string{
					"f1l1": "f1v1",
					"f1l2": "f1v2",
				},
				Tolerations: []corev1.Toleration{*toleration1.DeepCopy(), *toleration2.DeepCopy()},
			},
		},
		"with topology assignment; TopologyAwareScheduling enabled - scheduling gate added": {
			enableTopologyAwareScheduling: true,
			assignment: &kueue.PodSetAssignment{
				Name: "name",
				Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
					corev1.ResourceCPU: kueue.ResourceFlavorReference(flavor1.Name),
				},
				TopologyAssignment: &kueue.TopologyAssignment{
					Levels: []string{"cloud.com/rack"},
					Domains: []kueue.TopologyDomainAssignment{
						{
							Values: []string{"rack1"},
							Count:  4,
						},
					},
				},
			},
			defaultCount: 4,
			flavors:      []kueue.ResourceFlavor{*flavor1.DeepCopy()},
			wantInfo: PodSetInfo{
				Name:  "name",
				Count: 4,
				Labels: map[string]string{
					kueuealpha.TASLabel: "true",
				},
				NodeSelector: map[string]string{
					"f1l1": "f1v1",
					"f1l2": "f1v2",
				},
				Tolerations: []corev1.Toleration{*toleration1.DeepCopy(), *toleration2.DeepCopy()},
				SchedulingGates: []corev1.PodSchedulingGate{
					{
						Name: kueuealpha.TopologySchedulingGate,
					},
				},
			},
		},
		"with topology assignment; TopologyAwareScheduling disabled - no scheduling gate added": {
			assignment: &kueue.PodSetAssignment{
				Name: "name",
				Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
					corev1.ResourceCPU: kueue.ResourceFlavorReference(flavor1.Name),
				},
				TopologyAssignment: &kueue.TopologyAssignment{
					Levels: []string{"cloud.com/rack"},
					Domains: []kueue.TopologyDomainAssignment{
						{
							Values: []string{"rack1"},
							Count:  4,
						},
					},
				},
			},
			defaultCount: 4,
			flavors:      []kueue.ResourceFlavor{*flavor1.DeepCopy()},
			wantInfo: PodSetInfo{
				Name:  "name",
				Count: 4,
				NodeSelector: map[string]string{
					"f1l1": "f1v1",
					"f1l2": "f1v2",
				},
				Tolerations: []corev1.Toleration{*toleration1.DeepCopy(), *toleration2.DeepCopy()},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, tc.enableTopologyAwareScheduling)
			client := utiltesting.NewClientBuilder().WithLists(&kueue.ResourceFlavorList{Items: tc.flavors}).Build()

			gotInfo, gotError := FromAssignment(ctx, client, tc.assignment, tc.defaultCount)

			if diff := cmp.Diff(tc.wantError, gotError); diff != "" {
				t.Errorf("Unexpected error (-want/+got):\n%s", diff)
			}

			if tc.wantError == nil {
				if diff := cmp.Diff(tc.wantInfo, gotInfo, cmpopts.EquateEmpty(), cmpopts.SortSlices(func(a, b corev1.Toleration) bool { return a.Key < b.Key })); diff != "" {
					t.Errorf("Unexpected info (-want/+got):\n%s", diff)
				}
			}
		})
	}
}

func TestMergeRestore(t *testing.T) {
	basePodSet := utiltesting.MakePodSet("", 1).
		NodeSelector(map[string]string{"ns0": "ns0v"}).
		Labels(map[string]string{"l0": "l0v"}).
		Annotations(map[string]string{"a0": "a0v"}).
		Toleration(corev1.Toleration{
			Key:      "t0",
			Operator: corev1.TolerationOpEqual,
			Value:    "t0v",
			Effect:   corev1.TaintEffectNoSchedule,
		}).
		Obj()

	cases := map[string]struct {
		podSet             *kueue.PodSet
		info               PodSetInfo
		wantError          bool
		wantPodSet         *kueue.PodSet
		wantRestoreChanges bool
	}{
		"empty info": {
			podSet:     basePodSet.DeepCopy(),
			wantPodSet: basePodSet.DeepCopy(),
		},
		"no conflicts": {
			podSet: basePodSet.DeepCopy(),
			info: PodSetInfo{
				Annotations: map[string]string{
					"a1": "a1v",
				},
				Labels: map[string]string{
					"l1": "l1v",
				},
				NodeSelector: map[string]string{
					"ns1": "ns1v",
				},
				Tolerations: []corev1.Toleration{
					{
						Key:      "t1",
						Operator: corev1.TolerationOpEqual,
						Value:    "t1v",
						Effect:   corev1.TaintEffectNoSchedule,
					},
				},
			},
			wantPodSet: utiltesting.MakePodSet("", 1).
				NodeSelector(map[string]string{"ns0": "ns0v", "ns1": "ns1v"}).
				Labels(map[string]string{"l0": "l0v", "l1": "l1v"}).
				Annotations(map[string]string{"a0": "a0v", "a1": "a1v"}).
				Toleration(corev1.Toleration{
					Key:      "t0",
					Operator: corev1.TolerationOpEqual,
					Value:    "t0v",
					Effect:   corev1.TaintEffectNoSchedule,
				}).
				Toleration(corev1.Toleration{
					Key:      "t1",
					Operator: corev1.TolerationOpEqual,
					Value:    "t1v",
					Effect:   corev1.TaintEffectNoSchedule,
				}).
				Obj(),
			wantRestoreChanges: true,
		},
		"don't duplicate tolerations": {
			podSet: basePodSet.DeepCopy(),
			info: PodSetInfo{
				Annotations: map[string]string{
					"a1": "a1v",
				},
				Labels: map[string]string{
					"l1": "l1v",
				},
				NodeSelector: map[string]string{
					"ns1": "ns1v",
				},
				Tolerations: []corev1.Toleration{
					{
						Key:      "t0",
						Operator: corev1.TolerationOpEqual,
						Value:    "t0v",
						Effect:   corev1.TaintEffectNoSchedule,
					},
				},
			},
			wantPodSet: utiltesting.MakePodSet("", 1).
				NodeSelector(map[string]string{"ns0": "ns0v", "ns1": "ns1v"}).
				Labels(map[string]string{"l0": "l0v", "l1": "l1v"}).
				Annotations(map[string]string{"a0": "a0v", "a1": "a1v"}).
				Toleration(corev1.Toleration{
					Key:      "t0",
					Operator: corev1.TolerationOpEqual,
					Value:    "t0v",
					Effect:   corev1.TaintEffectNoSchedule,
				}).
				Obj(),
			wantRestoreChanges: true,
		},
		"conflicting label": {
			podSet: basePodSet.DeepCopy(),
			info: PodSetInfo{
				Labels: map[string]string{
					"l0": "l0v1",
				},
			},
			wantError: true,
		},
		"conflicting annotation": {
			podSet: basePodSet.DeepCopy(),
			info: PodSetInfo{
				Annotations: map[string]string{
					"a0": "a0v1",
				},
			},
			wantError: true,
		},
		"conflicting node selector": {
			podSet: basePodSet.DeepCopy(),
			info: PodSetInfo{
				NodeSelector: map[string]string{
					"ns0": "ns0v1",
				},
			},
			wantError: true,
		},
		"podset with scheduling gate; empty info": {
			podSet: utiltesting.MakePodSet("", 1).
				SchedulingGates(corev1.PodSchedulingGate{
					Name: "example.com/gate",
				}).
				Obj(),
			wantPodSet: utiltesting.MakePodSet("", 1).
				SchedulingGates(corev1.PodSchedulingGate{
					Name: "example.com/gate",
				}).
				Obj(),
		},
		"podset with scheduling gate; info re-adds the same": {
			podSet: utiltesting.MakePodSet("", 1).
				SchedulingGates(corev1.PodSchedulingGate{
					Name: "example.com/gate",
				}).
				Obj(),
			info: PodSetInfo{
				SchedulingGates: []corev1.PodSchedulingGate{
					{
						Name: "example.com/gate",
					},
				},
			},
			wantPodSet: utiltesting.MakePodSet("", 1).
				SchedulingGates(corev1.PodSchedulingGate{
					Name: "example.com/gate",
				}).
				Obj(),
		},
		"podset with scheduling gate; info adds another": {
			podSet: utiltesting.MakePodSet("", 1).
				SchedulingGates(corev1.PodSchedulingGate{
					Name: "example.com/gate",
				}).
				Obj(),
			info: PodSetInfo{
				SchedulingGates: []corev1.PodSchedulingGate{
					{
						Name: "example.com/gate2",
					},
				},
			},
			wantPodSet: utiltesting.MakePodSet("", 1).
				SchedulingGates(corev1.PodSchedulingGate{
					Name: "example.com/gate",
				}, corev1.PodSchedulingGate{
					Name: "example.com/gate2",
				}).
				Obj(),
			wantRestoreChanges: true,
		},
		"podset with tas label; empty info": {
			podSet: utiltesting.MakePodSet("", 1).
				Labels(map[string]string{kueuealpha.TASLabel: "true"}).
				Obj(),
			wantPodSet: utiltesting.MakePodSet("", 1).
				Labels(map[string]string{kueuealpha.TASLabel: "true"}).
				Obj(),
		},
		"podset with tas label; info re-adds the same": {
			podSet: utiltesting.MakePodSet("", 1).
				Labels(map[string]string{kueuealpha.TASLabel: "true"}).
				Obj(),
			info: PodSetInfo{
				Labels: map[string]string{kueuealpha.TASLabel: "true"},
			},
			wantPodSet: utiltesting.MakePodSet("", 1).
				Labels(map[string]string{kueuealpha.TASLabel: "true"}).
				Obj(),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			orig := tc.podSet.DeepCopy()

			gotError := Merge(&tc.podSet.Template.ObjectMeta, &tc.podSet.Template.Spec, tc.info)

			if tc.wantError != (gotError != nil) {
				t.Errorf("Unexpected error status want: %v", tc.wantError)
			}

			if !tc.wantError {
				if diff := cmp.Diff(tc.wantPodSet.Template, tc.podSet.Template, cmpopts.EquateEmpty()); diff != "" {
					t.Errorf("Unexpected template (-want/+got):\n%s", diff)
				}

				restoreInfo := FromPodSet(orig)
				gotRestoreChange := RestorePodSpec(&tc.podSet.Template.ObjectMeta, &tc.podSet.Template.Spec, restoreInfo)
				if gotRestoreChange != tc.wantRestoreChanges {
					t.Errorf("Unexpected restore change status want:%v", tc.wantRestoreChanges)
				}
				if diff := cmp.Diff(orig.Template, tc.podSet.Template, cmpopts.EquateEmpty()); diff != "" {
					t.Errorf("Unexpected template (-want/+got):\n%s", diff)
				}
			}
		})
	}
}

func TestAddOrUpdateLabel(t *testing.T) {
	cases := map[string]struct {
		info     PodSetInfo
		k, v     string
		wantInfo PodSetInfo
	}{
		"add to nil labels": {
			info: PodSetInfo{},
			k:    "key",
			v:    "value",
			wantInfo: PodSetInfo{
				Labels: map[string]string{"key": "value"},
			},
		},
		"add": {
			info: PodSetInfo{
				Labels: map[string]string{"other-key": "other-value"},
			},
			k: "key",
			v: "value",
			wantInfo: PodSetInfo{
				Labels: map[string]string{"other-key": "other-value", "key": "value"},
			},
		},
		"update": {
			info: PodSetInfo{
				Labels: map[string]string{"key": "value"},
			},
			k: "key",
			v: "updated-value",
			wantInfo: PodSetInfo{
				Labels: map[string]string{"key": "updated-value"},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tc.info.AddOrUpdateLabel(tc.k, tc.v)
			if diff := cmp.Diff(tc.wantInfo, tc.info, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unexpected info (-want/+got):\n%s", diff)
			}
		})
	}
}

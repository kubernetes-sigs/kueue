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
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
)

func TestApplyFlavorConstraints(t *testing.T) {
	cases := map[string]struct {
		tmpl            corev1.PodTemplateSpec
		psa             flavorassigner.PodSetAssignment
		resourceFlavors map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor
		wantSelector    map[string]string
		wantTolerations int
	}{
		"merges nodeLabels into empty NodeSelector": {
			tmpl: corev1.PodTemplateSpec{},
			psa: flavorassigner.PodSetAssignment{
				Flavors: flavorassigner.ResourceAssignment{
					corev1.ResourceCPU: &flavorassigner.FlavorAssignment{Name: "spot"},
				},
			},
			resourceFlavors: map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{
				"spot": {Spec: kueue.ResourceFlavorSpec{
					NodeLabels: map[string]string{"node-type": "spot"},
				}},
			},
			wantSelector: map[string]string{"node-type": "spot"},
		},
		"appends flavor tolerations": {
			tmpl: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
						},
					}},
				},
			},
			psa: flavorassigner.PodSetAssignment{
				Flavors: flavorassigner.ResourceAssignment{
					corev1.ResourceCPU: &flavorassigner.FlavorAssignment{Name: "gpu"},
				},
			},
			resourceFlavors: map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{
				"gpu": {Spec: kueue.ResourceFlavorSpec{
					Tolerations: []corev1.Toleration{{Key: "gpu", Operator: corev1.TolerationOpExists}},
				}},
			},
			wantTolerations: 1,
		},
		"skips unknown flavor": {
			tmpl: corev1.PodTemplateSpec{},
			psa: flavorassigner.PodSetAssignment{
				Flavors: flavorassigner.ResourceAssignment{
					corev1.ResourceCPU: &flavorassigner.FlavorAssignment{Name: "nonexistent"},
				},
			},
			resourceFlavors: map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tmpl := tc.tmpl.DeepCopy()
			applyFlavorConstraints(tmpl, &tc.psa, tc.resourceFlavors)
			if tc.wantSelector != nil {
				for k, v := range tc.wantSelector {
					if got := tmpl.Spec.NodeSelector[k]; got != v {
						t.Errorf("nodeSelector[%s] = %s, want %s", k, got, v)
					}
				}
			}
			if tc.wantTolerations > 0 && len(tmpl.Spec.Tolerations) != tc.wantTolerations {
				t.Errorf("got %d tolerations, want %d", len(tmpl.Spec.Tolerations), tc.wantTolerations)
			}
		})
	}
}

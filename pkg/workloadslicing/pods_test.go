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

package workloadslicing

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

func TestKeyForPod(t *testing.T) {
	testCases := map[string]struct {
		pod     *corev1.Pod
		wantKey *types.NamespacedName
	}{
		"no annotations": {
			pod: testingpod.MakePod("pod", "ns").Obj(),
		},
		"workload annotation": {
			pod: testingpod.MakePod("pod", "ns").
				Annotation(kueue.WorkloadAnnotation, "wl").
				Obj(),
			wantKey: &types.NamespacedName{Namespace: "ns", Name: "wl"},
		},
		"slice annotation only": {
			pod: testingpod.MakePod("pod", "ns").
				Annotation(kueue.WorkloadSliceNameAnnotation, "origin").
				Obj(),
			wantKey: &types.NamespacedName{Namespace: "ns", Name: "origin"},
		},
		"slice annotation takes precedence": {
			pod: testingpod.MakePod("pod", "ns").
				Annotation(kueue.WorkloadSliceNameAnnotation, "origin").
				Annotation(kueue.WorkloadAnnotation, "wl").
				Obj(),
			wantKey: &types.NamespacedName{Namespace: "ns", Name: "origin"},
		},
		"empty slice annotation does not fall back to workload": {
			pod: testingpod.MakePod("pod", "ns").
				Annotation(kueue.WorkloadSliceNameAnnotation, "").
				Annotation(kueue.WorkloadAnnotation, "wl").
				Obj(),
		},
		"empty workload annotation": {
			pod: testingpod.MakePod("pod", "ns").
				Annotation(kueue.WorkloadAnnotation, "").
				Obj(),
		},
		"both annotations empty": {
			pod: testingpod.MakePod("pod", "ns").
				Annotation(kueue.WorkloadSliceNameAnnotation, "").
				Annotation(kueue.WorkloadAnnotation, "").
				Obj(),
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			gotKey := KeyForPod(tc.pod)
			if diff := cmp.Diff(tc.wantKey, gotKey); diff != "" {
				t.Errorf("Unexpected key (-want,+got):\n%s", diff)
			}
		})
	}
}

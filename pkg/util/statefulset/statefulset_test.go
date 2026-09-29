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

package statefulset

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"

	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	testingjobspod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

func TestUngatePod(t *testing.T) {
	testCases := map[string]struct {
		pod         *corev1.Pod
		wantChanged bool
		wantGates   []corev1.PodSchedulingGate
	}{
		"both gates are removed": {
			pod: testingjobspod.MakePod("pod", "ns").
				KueueSchedulingGate().
				TopologySchedulingGate().
				KueueFinalizer().
				Obj(),
			wantChanged: true,
		},
		"the scheduling gate alone is removed": {
			pod: testingjobspod.MakePod("pod", "ns").
				KueueSchedulingGate().
				KueueFinalizer().
				Obj(),
			wantChanged: true,
		},
		"the topology gate alone is removed": {
			pod: testingjobspod.MakePod("pod", "ns").
				TopologySchedulingGate().
				KueueFinalizer().
				Obj(),
			wantChanged: true,
		},
		"gates Kueue does not own are kept": {
			pod: testingjobspod.MakePod("pod", "ns").
				KueueSchedulingGate().
				Gate("example.com/gate").
				KueueFinalizer().
				Obj(),
			wantChanged: true,
			wantGates:   []corev1.PodSchedulingGate{{Name: "example.com/gate"}},
		},
		"an ungated pod is unchanged": {
			pod: testingjobspod.MakePod("pod", "ns").
				KueueFinalizer().
				Obj(),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			if changed := UngatePod(tc.pod); changed != tc.wantChanged {
				t.Errorf("UngatePod() changed = %t, want %t", changed, tc.wantChanged)
			}
			if diff := cmp.Diff(tc.wantGates, tc.pod.Spec.SchedulingGates, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("SchedulingGates after UngatePod() (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff([]string{podconstants.PodFinalizer}, tc.pod.Finalizers); diff != "" {
				t.Errorf("Finalizers after UngatePod() (-want,+got):\n%s", diff)
			}
		})
	}
}

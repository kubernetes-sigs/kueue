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

	"github.com/go-logr/logr"
	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/podset"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

func TestPrepareCapacityBufferGate(t *testing.T) {
	info := podset.PodSetInfo{
		SchedulingGates: []corev1.PodSchedulingGate{{Name: kueuealpha.CapacityBufferSchedulingGate}},
	}
	cases := map[string]struct {
		pod       *corev1.Pod
		wantGates []corev1.PodSchedulingGate
	}{
		"does not add the gate to an existing Pod": {
			pod:       testingpod.MakePod("pod", "ns").Gate(podconstants.SchedulingGateName).Obj(),
			wantGates: []corev1.PodSchedulingGate{},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if err := prepare(logr.Discard(), tc.pod, info); err != nil {
				t.Fatalf("prepare() error: %v", err)
			}
			if diff := cmp.Diff(tc.wantGates, tc.pod.Spec.SchedulingGates); diff != "" {
				t.Errorf("SchedulingGates mismatch (-want,+got):\n%s", diff)
			}
			if len(info.SchedulingGates) != 1 {
				t.Errorf("prepare() modified the shared PodSetInfo gates: %v", info.SchedulingGates)
			}
		})
	}
}

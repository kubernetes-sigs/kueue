/*
CCopyright The Kubernetes Authors.

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

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
)

func TestHasGate(t *testing.T) {
	testCases := map[string]struct {
		gateName string
		pod      corev1.Pod
		want     bool
	}{
		"scheduling gate present": {
			gateName: "example.com/gate",
			pod: corev1.Pod{
				Spec: corev1.PodSpec{
					SchedulingGates: []corev1.PodSchedulingGate{
						{
							Name: "example.com/gate",
						},
					},
				},
			},
			want: true,
		},
		"another gate present": {
			gateName: "example.com/gate",
			pod: corev1.Pod{
				Spec: corev1.PodSpec{
					SchedulingGates: []corev1.PodSchedulingGate{
						{
							Name: "example.com/gate2",
						},
					},
				},
			},
			want: false,
		},
		"no scheduling gates": {
			pod:  corev1.Pod{},
			want: false,
		},
	}

	for desc, tc := range testCases {
		t.Run(desc, func(t *testing.T) {
			got := HasGate(&tc.pod, tc.gateName)
			if got != tc.want {
				t.Errorf("Unexpected result: want=%v, got=%v", tc.want, got)
			}
		})
	}
}

func TestUngate(t *testing.T) {
	testCases := map[string]struct {
		gateName string
		pod      corev1.Pod
		wantPod  corev1.Pod
		want     bool
	}{
		"ungate when scheduling gate present": {
			gateName: "example.com/gate",
			pod: corev1.Pod{
				Spec: corev1.PodSpec{
					SchedulingGates: []corev1.PodSchedulingGate{
						{
							Name: "example.com/gate",
						},
					},
				},
			},
			wantPod: corev1.Pod{},
			want:    true,
		},
		"ungate when scheduling gate missing": {
			gateName: "example.com/gate",
			pod: corev1.Pod{
				Spec: corev1.PodSpec{
					SchedulingGates: []corev1.PodSchedulingGate{
						{
							Name: "example.com/gate2",
						},
					},
				},
			},
			wantPod: corev1.Pod{
				Spec: corev1.PodSpec{
					SchedulingGates: []corev1.PodSchedulingGate{
						{
							Name: "example.com/gate2",
						},
					},
				},
			},
			want: false,
		},
	}
	for desc, tc := range testCases {
		t.Run(desc, func(t *testing.T) {
			got := Ungate(&tc.pod, tc.gateName)
			if got != tc.want {
				t.Errorf("Unexpected result: want=%v, got=%v", tc.want, got)
			}
			if diff := cmp.Diff(tc.wantPod.Spec.SchedulingGates, tc.pod.Spec.SchedulingGates, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unexpected scheduling gates\ndiff=%s", diff)
			}
		})
	}
}

func TestGate(t *testing.T) {
	testCases := map[string]struct {
		gateName string
		pod      corev1.Pod
		wantPod  corev1.Pod
		want     bool
	}{
		"gate when scheduling gate present": {
			gateName: "example.com/gate",
			pod: corev1.Pod{
				Spec: corev1.PodSpec{
					SchedulingGates: []corev1.PodSchedulingGate{
						{
							Name: "example.com/gate",
						},
					},
				},
			},
			wantPod: corev1.Pod{
				Spec: corev1.PodSpec{
					SchedulingGates: []corev1.PodSchedulingGate{
						{
							Name: "example.com/gate",
						},
					},
				},
			},
			want: false,
		},
		"gate when scheduling gate missing": {
			gateName: "example.com/gate",
			pod: corev1.Pod{
				Spec: corev1.PodSpec{
					SchedulingGates: []corev1.PodSchedulingGate{
						{
							Name: "example.com/gate2",
						},
					},
				},
			},
			wantPod: corev1.Pod{
				Spec: corev1.PodSpec{
					SchedulingGates: []corev1.PodSchedulingGate{
						{
							Name: "example.com/gate2",
						},
						{
							Name: "example.com/gate",
						},
					},
				},
			},
			want: true,
		},
	}

	for desc, tc := range testCases {
		t.Run(desc, func(t *testing.T) {
			got := Gate(&tc.pod, tc.gateName)
			if got != tc.want {
				t.Errorf("Unexpected result: want=%v, got=%v", tc.want, got)
			}
			if diff := cmp.Diff(tc.wantPod.Spec.SchedulingGates, tc.pod.Spec.SchedulingGates); diff != "" {
				t.Errorf("Unexpected scheduling gates\ndiff=%s", diff)
			}
		})
	}
}

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
	"math"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

func TestHasGate(t *testing.T) {
	basePod := testingpod.MakePod("", "")

	testCases := map[string]struct {
		gateName string
		pod      *corev1.Pod
		want     bool
	}{
		"scheduling gate present": {
			gateName: "example.com/gate",
			pod: basePod.Clone().
				Gate("example.com/gate").
				Obj(),
			want: true,
		},
		"another gate present": {
			gateName: "example.com/gate",
			pod: basePod.Clone().
				Gate("example.com/gate2").
				Obj(),
			want: false,
		},
		"no scheduling gates": {
			pod:  basePod.Clone().Obj(),
			want: false,
		},
	}

	for desc, tc := range testCases {
		t.Run(desc, func(t *testing.T) {
			got := HasGate(tc.pod, tc.gateName)
			if got != tc.want {
				t.Errorf("Unexpected result: want=%v, got=%v", tc.want, got)
			}
		})
	}
}

func TestUngate(t *testing.T) {
	basePod := testingpod.MakePod("", "")

	testCases := map[string]struct {
		gateName string
		pod      *corev1.Pod
		wantPod  *corev1.Pod
		want     bool
	}{
		"ungate when scheduling gate present": {
			gateName: "example.com/gate",
			pod: basePod.Clone().
				Gate("example.com/gate").
				Obj(),
			wantPod: basePod.Clone().
				Obj(),
			want: true,
		},
		"ungate when scheduling gate missing": {
			gateName: "example.com/gate",
			pod: basePod.Clone().
				Gate("example.com/gate2").
				Obj(),
			wantPod: basePod.Clone().
				Gate("example.com/gate2").
				Obj(),
			want: false,
		},
	}
	for desc, tc := range testCases {
		t.Run(desc, func(t *testing.T) {
			got := Ungate(tc.pod, tc.gateName)
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
	basePod := testingpod.MakePod("", "")

	testCases := map[string]struct {
		gateName string
		pod      *corev1.Pod
		wantPod  *corev1.Pod
		want     bool
	}{
		"gate when scheduling gate present": {
			gateName: "example.com/gate",
			pod: basePod.Clone().
				Gate("example.com/gate").
				Obj(),
			wantPod: basePod.Clone().
				Gate("example.com/gate").
				Obj(),
			want: false,
		},
		"gate when scheduling gate missing": {
			gateName: "example.com/gate",
			pod: basePod.Clone().
				Gate("example.com/gate2").
				Obj(),
			wantPod: basePod.Clone().
				Gate("example.com/gate", "example.com/gate2").
				Obj(),
			want: true,
		},
	}

	for desc, tc := range testCases {
		t.Run(desc, func(t *testing.T) {
			got := Gate(tc.pod, tc.gateName)
			if got != tc.want {
				t.Errorf("Unexpected result: want=%v, got=%v", tc.want, got)
			}
			if diff := cmp.Diff(tc.wantPod.Spec.SchedulingGates, tc.pod.Spec.SchedulingGates, cmpopts.SortSlices(func(a, b corev1.PodSchedulingGate) bool {
				return a.Name < b.Name
			})); diff != "" {
				t.Errorf("Unexpected scheduling gates\ndiff=%s", diff)
			}
		})
	}
}

func TestReadUIntFromLabel(t *testing.T) {
	basePod := testingpod.MakePod("pod", "ns")

	testCases := map[string]struct {
		obj     client.Object
		label   string
		max     int
		wantVal *int
		wantErr error
	}{
		"label not found": {
			obj: basePod.Clone().
				Obj(),
			label:   "label",
			max:     math.MaxInt,
			wantErr: ErrLabelNotFound,
		},
		"valid label value": {
			obj: basePod.Clone().
				Label("label", "1000").
				Obj(),
			label:   "label",
			max:     math.MaxInt,
			wantVal: ptr.To(1000),
		},
		"invalid label value": {
			obj: basePod.Clone().
				Label("label", "value").
				Obj(),
			label:   "label",
			wantErr: ErrInvalidUInt,
		},
		"less than zero": {
			obj: basePod.Clone().
				Label("label", "-1").
				Obj(),
			label:   "label",
			wantErr: ErrInvalidUInt,
		},
		"equal to bound": {
			obj: basePod.Clone().
				Label("label", "1001").
				Obj(),
			label:   "label",
			max:     1001,
			wantErr: ErrValidation,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			gotValue, gotErr := ReadUIntFromLabelBelowBound(tc.obj, tc.label, tc.max)

			if diff := cmp.Diff(tc.wantVal, gotValue); diff != "" {
				t.Errorf("Unexpected value (-want,+got):\n%s", diff)
			}

			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Reconcile returned error (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestIsTerminated(t *testing.T) {
	basePod := testingpod.MakePod("", "")

	cases := map[string]struct {
		pod            *corev1.Pod
		wantTerminated bool
	}{
		"pod is failed": {
			pod: basePod.Clone().
				StatusPhase(corev1.PodFailed).
				Obj(),
			wantTerminated: true,
		},
		"pod is succeeded": {
			pod: basePod.Clone().
				StatusPhase(corev1.PodSucceeded).
				Obj(),
			wantTerminated: true,
		},
		"pod is running": {
			pod: basePod.Clone().
				StatusPhase(corev1.PodRunning).
				Obj(),
			wantTerminated: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := IsTerminated(tc.pod)
			if tc.wantTerminated != got {
				t.Errorf("Unexpected Pod terminal\nwant: %v\ngot: %v\n", tc.wantTerminated, got)
			}
		})
	}
}

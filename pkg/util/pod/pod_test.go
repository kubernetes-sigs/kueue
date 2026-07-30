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
	"math"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
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
			pod:  basePod.DeepCopy(),
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
			wantPod: basePod.DeepCopy(),
			want:    true,
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
			obj:     basePod.DeepCopy(),
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
			wantVal: new(1000),
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

func TestSpecShape(t *testing.T) {
	podResources := &corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
		Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")},
	}
	cases := map[string]struct {
		podSpec       *corev1.PodSpec
		wantResources any
		wantPresent   bool
	}{
		"pod-level resources omitted when unset": {
			podSpec:     &corev1.PodSpec{Containers: []corev1.Container{{Name: "c"}}},
			wantPresent: false,
		},
		"pod-level resources included when set": {
			podSpec:       &corev1.PodSpec{Containers: []corev1.Container{{Name: "c"}}, Resources: podResources},
			wantResources: podResources.Requests,
			wantPresent:   true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, present := SpecShape(tc.podSpec)["resources"]
			if present != tc.wantPresent {
				t.Fatalf("Unexpected presence of \"resources\" key: want %v, got %v", tc.wantPresent, present)
			}
			if diff := cmp.Diff(tc.wantResources, got); diff != "" {
				t.Errorf("Unexpected resources shape (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestGenerateRoleHash(t *testing.T) {
	withoutPodResources := &corev1.PodSpec{Containers: []corev1.Container{{Name: "c"}}}
	withPodResources := &corev1.PodSpec{
		Containers: []corev1.Container{{Name: "c"}},
		Resources: &corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
		},
	}

	baseHash, err := GenerateRoleHash(withoutPodResources)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	cases := map[string]struct {
		podSpec      *corev1.PodSpec
		wantBaseHash bool
	}{
		"pod-level resources change the role hash": {
			podSpec: withPodResources,
		},
		"spec without pod-level resources has a stable role hash": {
			podSpec:      withoutPodResources.DeepCopy(),
			wantBaseHash: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gotHash, err := GenerateRoleHash(tc.podSpec)
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}
			if gotBaseHash := gotHash == baseHash; gotBaseHash != tc.wantBaseHash {
				t.Errorf("Unexpected role hash comparison with base hash: want equality %t, base hash %q, got hash %q", tc.wantBaseHash, baseHash, gotHash)
			}
		})
	}
}

func withSchedulingGroup(name string) *corev1.Pod {
	p := testingpod.MakePod("pod", "ns").Obj()
	p.Spec.SchedulingGroup = &corev1.PodSchedulingGroup{PodGroupName: new(name)}
	return p
}

func TestGetPodGroupName(t *testing.T) {
	cases := map[string]struct {
		bringYourOwnPodGroup bool
		pod                  *corev1.Pod
		want                 string
	}{
		"no group markers at all": {
			pod:  testingpod.MakePod("pod", "ns").Obj(),
			want: "",
		},
		"legacy label": {
			pod:  testingpod.MakePod("pod", "ns").Label(podconstants.GroupNameLabel, "legacy-group").Obj(),
			want: "legacy-group",
		},
		"standard field, gate disabled": {
			bringYourOwnPodGroup: false,
			pod:                  withSchedulingGroup("standard-group"),
			want:                 "",
		},
		"standard field, gate enabled": {
			bringYourOwnPodGroup: true,
			pod:                  withSchedulingGroup("standard-group"),
			want:                 "standard-group",
		},
		"legacy label takes precedence over standard field": {
			bringYourOwnPodGroup: true,
			pod: func() *corev1.Pod {
				p := withSchedulingGroup("standard-group")
				p.Labels = map[string]string{podconstants.GroupNameLabel: "legacy-group"}
				return p
			}(),
			want: "legacy-group",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.BringYourOwnPodGroup, tc.bringYourOwnPodGroup)
			if got := GetPodGroupName(tc.pod); got != tc.want {
				t.Errorf("GetPodGroupName() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHasStandardPodGroupName(t *testing.T) {
	cases := map[string]struct {
		bringYourOwnPodGroup bool
		pod                  *corev1.Pod
		want                 bool
	}{
		"no group markers at all": {
			bringYourOwnPodGroup: true,
			pod:                  testingpod.MakePod("pod", "ns").Obj(),
			want:                 false,
		},
		"standard field, gate disabled": {
			bringYourOwnPodGroup: false,
			pod:                  withSchedulingGroup("standard-group"),
			want:                 false,
		},
		"standard field, gate enabled": {
			bringYourOwnPodGroup: true,
			pod:                  withSchedulingGroup("standard-group"),
			want:                 true,
		},
		"legacy label set alongside standard field": {
			bringYourOwnPodGroup: true,
			pod: func() *corev1.Pod {
				p := withSchedulingGroup("standard-group")
				p.Labels = map[string]string{podconstants.GroupNameLabel: "legacy-group"}
				return p
			}(),
			want: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.BringYourOwnPodGroup, tc.bringYourOwnPodGroup)
			if got := HasStandardPodGroupName(tc.pod); got != tc.want {
				t.Errorf("HasStandardPodGroupName() = %v, want %v", got, tc.want)
			}
		})
	}
}

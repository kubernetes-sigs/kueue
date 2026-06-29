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
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	schedulingv1alpha2 "k8s.io/api/scheduling/v1alpha2"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/component-base/featuregate"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

func TestWantsWASPodGroup(t *testing.T) {
	cases := map[string]struct {
		pod  *corev1.Pod
		want bool
	}{
		"annotation set to true": {
			pod:  testingpod.MakePod("p", "ns").WASPodGroupAnnotation().Obj(),
			want: true,
		},
		"annotation set to false": {
			pod:  testingpod.MakePod("p", "ns").Annotation(podconstants.WASPodGroupAnnotation, "false").Obj(),
			want: false,
		},
		"annotation missing": {
			pod:  testingpod.MakePod("p", "ns").Obj(),
			want: false,
		},
		"annotation empty string": {
			pod:  testingpod.MakePod("p", "ns").Annotation(podconstants.WASPodGroupAnnotation, "").Obj(),
			want: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := wantsWASPodGroup(tc.pod); got != tc.want {
				t.Errorf("wantsWASPodGroup() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNativePodGroupNameForPod(t *testing.T) {
	cases := map[string]struct {
		pod  *corev1.Pod
		want string
	}{
		"schedulingGroup nil": {
			pod:  testingpod.MakePod("p", "ns").Obj(),
			want: "",
		},
		"podGroupName set": {
			pod:  testingpod.MakePod("p", "ns").SchedulingGroupPodGroupName("my-group").Obj(),
			want: "my-group",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := nativePodGroupNameForPod(tc.pod); got != tc.want {
				t.Errorf("nativePodGroupNameForPod() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSetNativePodGroupName(t *testing.T) {
	pod := testingpod.MakePod("p", "ns").Obj()
	setNativePodGroupName(pod, "test-group")

	if pod.Spec.SchedulingGroup == nil {
		t.Fatal("expected SchedulingGroup to be set")
	}
	if got := *pod.Spec.SchedulingGroup.PodGroupName; got != "test-group" {
		t.Errorf("PodGroupName = %q, want %q", got, "test-group")
	}
}

func TestShouldDefaultNativePodGroup(t *testing.T) {
	cases := map[string]struct {
		enabled      bool
		pod          *corev1.Pod
		featureGates map[featuregate.Feature]bool
		want         bool
	}{
		"all conditions met": {
			enabled: true,
			pod: testingpod.MakePod("p", "ns").
				GroupNameLabel("my-group").
				WASPodGroupAnnotation().
				Obj(),
			want: true,
		},
		"not enabled": {
			enabled: false,
			pod: testingpod.MakePod("p", "ns").
				GroupNameLabel("my-group").
				WASPodGroupAnnotation().
				Obj(),
			want: false,
		},
		"missing annotation": {
			enabled: true,
			pod: testingpod.MakePod("p", "ns").
				GroupNameLabel("my-group").
				Obj(),
			want: false,
		},
		"no pod group name": {
			enabled: true,
			pod: testingpod.MakePod("p", "ns").
				WASPodGroupAnnotation().
				Obj(),
			want: false,
		},
		"schedulingGroup already set": {
			enabled: true,
			pod: testingpod.MakePod("p", "ns").
				GroupNameLabel("my-group").
				WASPodGroupAnnotation().
				SchedulingGroupPodGroupName("external").
				Obj(),
			want: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := shouldDefaultNativePodGroup(tc.enabled, tc.pod); got != tc.want {
				t.Errorf("shouldDefaultNativePodGroup() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestShouldEnsureNativePodGroup(t *testing.T) {
	cases := map[string]struct {
		pod  *Pod
		want bool
	}{
		"all conditions met": {
			pod: &Pod{
				pod: *testingpod.MakePod("p", "ns").
					GroupNameLabel("my-group").
					GroupTotalCount("2").
					WASPodGroupAnnotation().
					SchedulingGroupPodGroupName("my-group").
					Obj(),
				isGroup:                true,
				nativePodGroupsEnabled: true,
			},
			want: true,
		},
		"native pod groups disabled": {
			pod: &Pod{
				pod: *testingpod.MakePod("p", "ns").
					GroupNameLabel("my-group").
					GroupTotalCount("2").
					WASPodGroupAnnotation().
					SchedulingGroupPodGroupName("my-group").
					Obj(),
				isGroup:                true,
				nativePodGroupsEnabled: false,
			},
			want: false,
		},
		"not a group": {
			pod: &Pod{
				pod: *testingpod.MakePod("p", "ns").
					WASPodGroupAnnotation().
					SchedulingGroupPodGroupName("my-group").
					Obj(),
				isGroup:                false,
				nativePodGroupsEnabled: true,
			},
			want: false,
		},
		"missing annotation": {
			pod: &Pod{
				pod: *testingpod.MakePod("p", "ns").
					GroupNameLabel("my-group").
					GroupTotalCount("2").
					SchedulingGroupPodGroupName("my-group").
					Obj(),
				isGroup:                true,
				nativePodGroupsEnabled: true,
			},
			want: false,
		},
		"no schedulingGroup set": {
			pod: &Pod{
				pod: *testingpod.MakePod("p", "ns").
					GroupNameLabel("my-group").
					GroupTotalCount("2").
					WASPodGroupAnnotation().
					Obj(),
				isGroup:                true,
				nativePodGroupsEnabled: true,
			},
			want: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.pod.shouldEnsureNativePodGroup(); got != tc.want {
				t.Errorf("shouldEnsureNativePodGroup() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNativePodGroupsAvailability(t *testing.T) {
	cases := map[string]struct {
		featureGates map[featuregate.Feature]bool
		mapperGVKs   []schema.GroupVersionKind
		wantEnabled  bool
		wantReason   string
	}{
		"feature gate disabled": {
			featureGates: map[featuregate.Feature]bool{features.WASPodGroups: false},
			wantEnabled:  false,
			wantReason:   "feature gate disabled",
		},
		"feature gate enabled but API unavailable": {
			featureGates: map[featuregate.Feature]bool{features.WASPodGroups: true},
			mapperGVKs:   nil,
			wantEnabled:  false,
		},
		"feature gate enabled and API available": {
			featureGates: map[featuregate.Feature]bool{features.WASPodGroups: true},
			mapperGVKs: []schema.GroupVersionKind{
				schedulingv1alpha2.SchemeGroupVersion.WithKind("PodGroup"),
			},
			wantEnabled: true,
			wantReason:  "native PodGroups supported",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)

			mapper := apimeta.NewDefaultRESTMapper(nil)
			for _, gvk := range tc.mapperGVKs {
				mapper.Add(gvk, apimeta.RESTScopeNamespace)
			}

			gotEnabled, gotReason := nativePodGroupsAvailability(mapper)
			if gotEnabled != tc.wantEnabled {
				t.Errorf("enabled = %v, want %v", gotEnabled, tc.wantEnabled)
			}
			if tc.wantReason != "" && gotReason != tc.wantReason {
				t.Errorf("reason = %q, want %q", gotReason, tc.wantReason)
			}
		})
	}
}

func TestEnsureNativePodGroupOwnershipConflict(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)

	existingPodGroup := &schedulingv1alpha2.PodGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-group",
			Namespace: metav1.NamespaceDefault,
			Labels: map[string]string{
				constants.ManagedByKueueLabelKey: constants.ManagedByKueueLabelValue,
			},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: kueue.SchemeGroupVersion.String(),
				Kind:       "Workload",
				Name:       "other-workload",
				UID:        "other-uid",
				Controller: new(true),
			}},
		},
	}

	kClient := utiltesting.NewClientBuilder().WithObjects(existingPodGroup).Build()
	recorder := &utiltesting.EventRecorder{}

	p := &Pod{
		pod: *testingpod.MakePod("driver", metav1.NamespaceDefault).
			GroupNameLabel("test-group").
			GroupTotalCount("2").
			WASPodGroupAnnotation().
			SchedulingGroupPodGroupName("test-group").
			Obj(),
		isGroup:                true,
		nativePodGroupsEnabled: true,
	}
	wl := utiltestingapi.MakeWorkload("test-group", metav1.NamespaceDefault).UID("current-uid").Obj()

	gotErr := p.ensureNativePodGroup(ctx, kClient, wl, recorder)
	if gotErr == nil {
		t.Fatal("expected error for ownership conflict")
	}
	if !strings.Contains(gotErr.Error(), "owned by a different workload") {
		t.Fatalf("unexpected error: %v", gotErr)
	}
}

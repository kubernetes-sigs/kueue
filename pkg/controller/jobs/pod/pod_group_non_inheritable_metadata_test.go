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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

// The marker used to reach the Workload here: the group copies its labels after
// NewWorkload has already run, so the guard there never saw them.
func TestConstructComposableWorkloadDropsTheOriginLabel(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	mk := func(name, origin string) corev1.Pod {
		p := testingpod.MakePod(name, "test-ns").GroupNameLabel("test-group").GroupTotalCount("2").Obj()
		p.Labels["team"] = "alpha"
		if origin != "" {
			p.Labels[kueue.MultiKueueOriginLabel] = origin
		}
		return *p
	}
	for _, tc := range []struct{ label, a, b string }{
		{"same origin on both pods", "multikueue", "multikueue"},
		{"different origin per pod", "multikueue", "other"},
	} {
		pods := []corev1.Pod{mk("driver", tc.a), mk("worker", tc.b)}
		p := &Pod{pod: pods[0], isGroup: true, list: corev1.PodList{Items: pods}}
		keys := sets.New("team", kueue.MultiKueueOriginLabel)
		cl := utiltesting.NewClientBuilder().WithObjects(&pods[0], &pods[1]).Build()
		wl, err := p.ConstructComposableWorkload(ctx, cl, &utiltesting.EventRecorder{}, keys, nil)
		if err != nil {
			t.Fatalf("%s: ConstructComposableWorkload() error = %v", tc.label, err)
		}
		if got, ok := wl.Labels[kueue.MultiKueueOriginLabel]; ok {
			t.Errorf("%s: Workload carries the origin label %q", tc.label, got)
		}
		if got := wl.Labels["team"]; got != "alpha" {
			t.Errorf("%s: ordinary label = %q, want alpha", tc.label, got)
		}
		if !keys.Has(kueue.MultiKueueOriginLabel) {
			t.Errorf("%s: the caller's key set was modified", tc.label)
		}
	}
}

// The group compares its members before it settles on one set of metadata, so a
// non-inheritable key has to be gone by then: two Pods disagreeing about a value
// neither of them may pass on must not be read as two different groups.
func TestConstructComposableWorkloadDropsNonInheritableAnnotations(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.CustomMetricLabels, true)
	ctx, _ := utiltesting.ContextWithLog(t)

	// The group marker is spoofed alongside these, but NewGroupWorkload writes
	// the real one back afterwards, so it is the one checked by value below.
	dropped := []string{
		controllerconstants.JobOwnerNameAnnotation,
		controllerconstants.PriorityBoostAnnotationKey,
		kueue.WorkloadSliceNameAnnotation,
	}
	reserved := append([]string{podconstants.IsGroupWorkloadAnnotationKey}, dropped...)
	mk := func(name, spoofed string) corev1.Pod {
		p := testingpod.MakePod(name, "test-ns").GroupNameLabel("test-group").GroupTotalCount("2").Obj()
		p.Annotations["team"] = "alpha"
		for _, key := range reserved {
			p.Annotations[key] = spoofed
		}
		return *p
	}
	pods := []corev1.Pod{mk("driver", "from-driver"), mk("worker", "from-worker")}
	p := &Pod{pod: pods[0], isGroup: true, list: corev1.PodList{Items: pods}}
	keys := sets.New(append([]string{"team"}, reserved...)...)
	cl := utiltesting.NewClientBuilder().WithObjects(&pods[0], &pods[1]).Build()

	wl, err := p.ConstructComposableWorkload(ctx, cl, &utiltesting.EventRecorder{}, nil, keys)
	if err != nil {
		t.Fatalf("ConstructComposableWorkload() error = %v", err)
	}
	for _, key := range dropped {
		if got, found := wl.Annotations[key]; found {
			t.Errorf("Workload carries %s from a Pod: %q", key, got)
		}
	}
	if got := wl.Annotations["team"]; got != "alpha" {
		t.Errorf("ordinary annotation = %q, want alpha", got)
	}
	// Written after the filtering, so the group still says what it is.
	if got := wl.Annotations[podconstants.IsGroupWorkloadAnnotationKey]; got != podconstants.IsGroupWorkloadAnnotationValue {
		t.Errorf("group marker = %q, want %q", got, podconstants.IsGroupWorkloadAnnotationValue)
	}
	for _, key := range reserved {
		if !keys.Has(key) {
			t.Errorf("the caller's key set lost %s", key)
		}
	}
}
